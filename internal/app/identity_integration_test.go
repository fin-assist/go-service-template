package app

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/go-grpc/client"
	"github.com/fin-assist/go-grpc/server"
	identitylib "github.com/fin-assist/go-identity"

	"github.com/fin-assist/go-service-template/internal/config"
)

const (
	userIssuer   = "https://user.example"
	userAudience = "mobile"
	clusterIss   = "https://cluster.example"
)

func userClaims(sub string, exp time.Time) map[string]any {
	return map[string]any{"iss": userIssuer, "aud": userAudience, "sub": sub, "exp": exp.Unix()}
}

func serviceAccountClaims(namespace, name string, exp time.Time) map[string]any {
	return map[string]any{
		"iss": clusterIss, "aud": "finassist-internal", "exp": exp.Unix(),
		"sub":           "system:serviceaccount:" + namespace + ":" + name,
		"kubernetes.io": map[string]any{"namespace": namespace, "serviceaccount": map[string]string{"name": name}},
	}
}

type prodFixture struct {
	cfg          config.Config
	userPrivate  ed25519.PrivateKey
	clusterKey   ed25519.PrivateKey
	peer         *userPeer
	userDial     []grpc.DialOption
	clusterCalls func() int32
}

// newProdFixture is a prod deployment: the Kubernetes JWKS behind TLS with its own CA
// and API-audience token, the user peer on bufconn and a separate outgoing token file.
func newProdFixture(t *testing.T, peerFails bool) prodFixture {
	t.Helper()
	userPublic, userPrivate := newKey(t)
	clusterPublic, clusterPrivate := newKey(t)
	keys, served := clusterKeys(t, jwks(t, map[string]ed25519.PublicKey{"cluster-key": clusterPublic}), "api-audience-token")
	peer := &userPeer{jwks: jwks(t, map[string]ed25519.PublicKey{"user-key": userPublic}), fail: peerFails,
		accept: func(token string) bool { return token == "outgoing-service" }}
	cfg := config.Config{Service: "template", Env: config.EnvProd, Peers: map[string]string{"user": "passthrough:///user"},
		Auth: config.AuthConfig{Mode: config.AuthVerify, ServiceTokenFile: writeFile(t, "internal-token", []byte("outgoing-service")),
			Identity: config.IdentityConfig{UserIssuer: userIssuer, UserAudience: userAudience, Kubernetes: config.KubernetesIdentity{
				Issuer: clusterIss, JWKSURL: keys.URL, CAFile: caFile(t, keys), Namespace: "finassist",
				APITokenFile: writeFile(t, "api-token", []byte("api-audience-token")),
			}}}}
	return prodFixture{cfg: cfg, userPrivate: userPrivate, clusterKey: clusterPrivate, peer: peer,
		userDial: startUserPeer(t, peer), clusterCalls: served.Load}
}

func chainDeps(reg prometheus.Registerer) server.Deps {
	return server.Deps{Logger: slog.New(slog.DiscardHandler), Metrics: reg}
}

func reasonOf(err error) (codes.Code, string) { return status.Code(err), server.Reason(err) }

// The prod identity verifies real tokens in the go-grpc server chain: the user key
// set comes from user.GetSigningKeys with this service's token, the ServiceAccount
// keys from the pinned HTTPS endpoint, and every forged, foreign, expired or
// misplaced credential is refused before the handler runs.
func TestIntegrationIdentityProdVerifiesTokensInServerChain(t *testing.T) {
	f := newProdFixture(t, false)
	reg := prometheus.NewRegistry()
	clients, err := client.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity(f.cfg, reg, clients, f.userDial)
	if err != nil {
		t.Fatal(err)
	}
	defer id.close()
	conn, p := startChain(t, id, chainDeps(reg))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Now()
	bff := signJWT(t, f.clusterKey, "cluster-key", serviceAccountClaims("finassist", "bank-bff", now.Add(time.Minute)))
	user := signJWT(t, f.userPrivate, "user-key", userClaims("user-42", now.Add(time.Minute)))

	if err := callProfile(ctx, conn, bff, user); err != nil {
		t.Fatalf("valid credentials refused: %v", err)
	}
	if _, err := userv1.NewUserInternalServiceClient(conn).GetSigningKeys(metadata.AppendToOutgoingContext(ctx, "x-service-authorization", "Bearer "+bff), &userv1.GetSigningKeysRequest{}); err != nil {
		t.Fatalf("no-user call refused: %v", err)
	}
	_, forgedKey := newKey(t)
	analytics := signJWT(t, f.clusterKey, "cluster-key", serviceAccountClaims("finassist", "analytics", now.Add(time.Minute)))
	foreignNamespace := signJWT(t, f.clusterKey, "cluster-key", serviceAccountClaims("other", "bank-bff", now.Add(time.Minute)))
	mismatched := serviceAccountClaims("finassist", "bank-bff", now.Add(time.Minute))
	mismatched["sub"] = "system:serviceaccount:finassist:analytics"
	foreignAudience := userClaims("user-42", now.Add(time.Minute))
	foreignAudience["aud"] = "another-app"
	refusals := []struct {
		name, service, user string
		code                codes.Code
		reason              string
	}{
		{"no service token", "", user, codes.Unauthenticated, ""},
		{"forged service token", signJWT(t, forgedKey, "cluster-key", serviceAccountClaims("finassist", "bank-bff", now.Add(time.Minute))), user, codes.Unauthenticated, ""},
		{"expired service token", signJWT(t, f.clusterKey, "cluster-key", serviceAccountClaims("finassist", "bank-bff", now.Add(-time.Minute))), user, codes.Unauthenticated, ""},
		{"foreign namespace", foreignNamespace, user, codes.Unauthenticated, ""},
		{"subject differs from serviceaccount", signJWT(t, f.clusterKey, "cluster-key", mismatched), user, codes.Unauthenticated, ""},
		{"user token as service token", user, user, codes.Unauthenticated, ""},
		{"foreign caller", analytics, user, codes.PermissionDenied, ""},
		{"no user token", bff, "", codes.Unauthenticated, "INVALID_TOKEN"},
		{"forged user token", bff, signJWT(t, forgedKey, "user-key", userClaims("user-42", now.Add(time.Minute))), codes.Unauthenticated, "INVALID_TOKEN"},
		{"expired user token", bff, signJWT(t, f.userPrivate, "user-key", userClaims("user-42", now.Add(-time.Minute))), codes.Unauthenticated, "INVALID_TOKEN"},
		{"foreign audience", bff, signJWT(t, f.userPrivate, "user-key", foreignAudience), codes.Unauthenticated, "INVALID_TOKEN"},
		{"service token as user token", bff, bff, codes.Unauthenticated, "INVALID_TOKEN"},
		{"unknown user key", bff, signJWT(t, f.userPrivate, "rotated-away", userClaims("user-42", now.Add(time.Minute))), codes.Unauthenticated, "INVALID_TOKEN"},
	}
	for _, r := range refusals {
		t.Run(r.name, func(t *testing.T) {
			code, reason := reasonOf(callProfile(ctx, conn, r.service, r.user))
			if code != r.code || reason != r.reason {
				t.Fatalf("got %s %q, want %s %q", code, reason, r.code, r.reason)
			}
		})
	}
	callers, users := p.seen()
	if !slices.Equal(callers, []string{"bank-bff", "bank-bff"}) || !slices.Equal(users, []string{"user-42"}) {
		t.Fatalf("handlers ran for refused credentials: callers=%v users=%v", callers, users)
	}
	if calls, refused := f.peer.counts(); calls < 1 || refused != 0 {
		t.Fatalf("user keys not fetched with the service token: calls=%d refused=%d", calls, refused)
	}
	if f.clusterCalls() < 1 {
		t.Fatal("ServiceAccount keys were not fetched from the pinned endpoint")
	}
	if token, err := id.tokens.Token(ctx); token != "outgoing-service" || err != nil {
		t.Fatalf("outgoing token: %q %v", token, err)
	}
	assertMetric(t, reg, "finassist_identity_rejected_total")
	assertMetric(t, reg, "grpc_client_handled_total")
}

func assertMetric(t *testing.T, reg *prometheus.Registry, name string) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name && len(f.GetMetric()) > 0 {
			return
		}
	}
	t.Fatalf("metric %s was not recorded", name)
}

// A user key outage is not a verdict on the token (FIN-293): the caller gets a bare
// UNAVAILABLE to retry, not INVALID_TOKEN that would sign the user out.
func TestIntegrationIdentityUserKeyOutageIsUnavailable(t *testing.T) {
	f := newProdFixture(t, true)
	reg := prometheus.NewRegistry()
	id, err := identity(f.cfg, reg, nil, f.userDial)
	if err != nil {
		t.Fatal(err)
	}
	defer id.close()
	conn, p := startChain(t, id, chainDeps(reg))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := time.Now()
	bff := signJWT(t, f.clusterKey, "cluster-key", serviceAccountClaims("finassist", "bank-bff", now.Add(time.Minute)))
	user := signJWT(t, f.userPrivate, "user-key", userClaims("user-42", now.Add(time.Minute)))
	if code, reason := reasonOf(callProfile(ctx, conn, bff, user)); code != codes.Unavailable || reason != "" {
		t.Fatalf("key outage: %s %q", code, reason)
	}
	if _, users := p.seen(); len(users) != 0 {
		t.Fatal("handler ran without verified user keys")
	}
}

// The identity-keys job refreshes the key sets in the background and stops with its
// context; Close then releases the user connection and may be repeated.
func TestIntegrationIdentityKeysJobRunsUntilCancelled(t *testing.T) {
	f := newProdFixture(t, false)
	id, err := identity(f.cfg, prometheus.NewRegistry(), nil, f.userDial)
	if err != nil {
		t.Fatal(err)
	}
	if len(id.jobs) != 1 || id.jobs[0].Name != identityKeysJob || id.jobs[0].Run == nil {
		t.Fatalf("jobs: %+v", id.jobs)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- id.jobs[0].Run(ctx) }()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for calls, _ := f.peer.counts(); calls == 0 || f.clusterCalls() == 0; calls, _ = f.peer.counts() {
		select {
		case <-deadline:
			cancel()
			t.Fatal("job did not refresh both key sets")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("job returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job outlived its context")
	}
	id.close()
	id.close()
}

func stageConfig(t *testing.T, own ed25519.PrivateKey, public map[string]ed25519.PublicKey) config.Config {
	t.Helper()
	return config.Config{Service: "template", Env: config.EnvStage, Peers: map[string]string{"user": "passthrough:///user"},
		Auth: config.AuthConfig{Mode: config.AuthVerify, Identity: config.IdentityConfig{UserIssuer: userIssuer, UserAudience: userAudience,
			Stage: config.StageIdentity{Issuer: "https://stage.example",
				SigningKeyFile: writeFile(t, "template.pem", privateKeyPEM(t, own)),
				PublicKeysFile: writeFile(t, "stage-keys.json", []byte(jwks(t, public)))}}}}
}

// The stage identity signs this service's outgoing tokens with its file key and
// accepts only stage services listed in the public key file.
func TestIntegrationIdentityStageUsesFileKeys(t *testing.T) {
	ownPublic, ownPrivate := newKey(t)
	bffPublic, bffPrivate := newKey(t)
	userPublic, userPrivate := newKey(t)
	cfg := stageConfig(t, ownPrivate, map[string]ed25519.PublicKey{"template": ownPublic, "bank-bff": bffPublic})
	stageVerifier, err := identitylib.NewStageVerifier("stage", identitylib.VerifyOptions{Issuer: "https://stage.example", Audience: "finassist-internal", Clock: time.Now},
		map[string]ed25519.PublicKey{"template": ownPublic})
	if err != nil {
		t.Fatal(err)
	}
	peer := &userPeer{jwks: jwks(t, map[string]ed25519.PublicKey{"user-key": userPublic}), accept: func(token string) bool {
		caller, err := stageVerifier.VerifyService(context.Background(), token)
		return err == nil && caller == "template"
	}}
	reg := prometheus.NewRegistry()
	id, err := identity(cfg, reg, nil, startUserPeer(t, peer))
	if err != nil {
		t.Fatal(err)
	}
	defer id.close()
	conn, p := startChain(t, id, chainDeps(reg))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stageToken := func(caller string, key ed25519.PrivateKey) string {
		source, err := identitylib.NewStageSource("stage", identitylib.StageOptions{KeyFile: writeFile(t, caller+".pem", privateKeyPEM(t, key)), Caller: caller, Issuer: "https://stage.example", Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		token, err := source.Token(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	user := signJWT(t, userPrivate, "user-key", userClaims("user-7", time.Now().Add(time.Minute)))
	if err := callProfile(ctx, conn, stageToken("bank-bff", bffPrivate), user); err != nil {
		t.Fatalf("stage bank-bff refused: %v", err)
	}
	_, impostor := newKey(t)
	if code, _ := reasonOf(callProfile(ctx, conn, stageToken("bank-bff", impostor), user)); code != codes.Unauthenticated {
		t.Fatalf("token signed by an unlisted key: %s", code)
	}
	if code, _ := reasonOf(callProfile(ctx, conn, stageToken("template", ownPrivate), user)); code != codes.PermissionDenied {
		t.Fatalf("listed service outside the access table: %s", code)
	}
	if _, users := p.seen(); !slices.Equal(users, []string{"user-7"}) {
		t.Fatalf("handlers ran for refused credentials: %v", users)
	}
	if calls, refused := peer.counts(); calls < 1 || refused != 0 {
		t.Fatalf("stage token not accepted by user: calls=%d refused=%d", calls, refused)
	}
}

// Files are read at construction: a missing or invalid one refuses the start with an
// error that names neither the path nor the content.
func TestIntegrationIdentityRefusesUnusableFiles(t *testing.T) {
	ownPublic, ownPrivate := newKey(t)
	tests := map[string]func(t *testing.T) config.Config{
		"missing CA": func(t *testing.T) config.Config {
			cfg := newProdFixture(t, false).cfg
			cfg.Auth.Identity.Kubernetes.CAFile = "/secret/missing-ca.pem"
			return cfg
		},
		"invalid CA": func(t *testing.T) config.Config {
			cfg := newProdFixture(t, false).cfg
			cfg.Auth.Identity.Kubernetes.CAFile = writeFile(t, "ca.pem", []byte("secret-not-a-certificate"))
			return cfg
		},
		"plain HTTP JWKS": func(t *testing.T) config.Config {
			cfg := newProdFixture(t, false).cfg
			cfg.Auth.Identity.Kubernetes.JWKSURL = strings.Replace(cfg.Auth.Identity.Kubernetes.JWKSURL, "https://", "http://", 1)
			return cfg
		},
		"missing stage key": func(t *testing.T) config.Config {
			cfg := stageConfig(t, ownPrivate, map[string]ed25519.PublicKey{"template": ownPublic})
			cfg.Auth.Identity.Stage.SigningKeyFile = "/secret/missing.pem"
			return cfg
		},
		"stage key is not a key": func(t *testing.T) config.Config {
			cfg := stageConfig(t, ownPrivate, map[string]ed25519.PublicKey{"template": ownPublic})
			cfg.Auth.Identity.Stage.SigningKeyFile = writeFile(t, "bad.pem", []byte("secret-not-a-key"))
			return cfg
		},
		"stage public keys are not a JWKS": func(t *testing.T) config.Config {
			cfg := stageConfig(t, ownPrivate, map[string]ed25519.PublicKey{"template": ownPublic})
			cfg.Auth.Identity.Stage.PublicKeysFile = writeFile(t, "keys.json", []byte("secret-not-json"))
			return cfg
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			id, err := identity(build(t), reg, nil, nil)
			if err == nil {
				id.close()
				t.Fatal("unusable identity files accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), ".pem") || strings.Contains(err.Error(), ".json") {
				t.Fatalf("error exposes a path or content: %v", err)
			}
			if families, _ := reg.Gather(); len(families) != 0 {
				t.Fatal("refused identity left metrics registered")
			}
		})
	}
}

// A second identity on the same registry is refused instead of sharing metrics.
func TestIntegrationIdentityRefusesDuplicateMetrics(t *testing.T) {
	f := newProdFixture(t, false)
	reg := prometheus.NewRegistry()
	id, err := identity(f.cfg, reg, nil, f.userDial)
	if err != nil {
		t.Fatal(err)
	}
	defer id.close()
	if second, err := identity(f.cfg, reg, nil, f.userDial); err == nil {
		second.close()
		t.Fatal("duplicate identity metrics accepted")
	}
}
