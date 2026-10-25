package apitest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"

	"github.com/fin-assist/go-service-template/internal/config"
	"github.com/fin-assist/go-service-template/internal/testdb"
)

// A real user peer on TCP: the whole assembly dials it by PEER_USER_ADDR exactly as
// in production. It answers keys only to this service's outgoing token.
type tcpUserPeer struct {
	userv1.UnimplementedUserInternalServiceServer
	jwks     string
	token    string
	accepted atomic.Int32
	refused  atomic.Int32
}

func (p *tcpUserPeer) GetSigningKeys(ctx context.Context, _ *userv1.GetSigningKeysRequest) (*userv1.GetSigningKeysResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("x-service-authorization"); len(got) != 1 || got[0] != "Bearer "+p.token || len(md.Get("authorization")) != 0 {
		p.refused.Add(1)
		return nil, status.Error(codes.Unauthenticated, "")
	}
	p.accepted.Add(1)
	return &userv1.GetSigningKeysResponse{JwksJson: p.jwks}, nil
}

func keyDocument(t *testing.T, kid string) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(public)}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// prodIdentity returns a mutation that configures AUTH_MODE=verify with ENV=prod
// against a TCP user peer and a TLS Kubernetes JWKS endpoint with its own CA.
func prodIdentity(t *testing.T) (func(*config.Config), *tcpUserPeer, *atomic.Int32) {
	t.Helper()
	var clusterServed atomic.Int32
	cluster := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-audience-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		clusterServed.Add(1)
		_, _ = w.Write([]byte(keyDocument(t, "cluster-key")))
	}))
	t.Cleanup(cluster.Close)
	peer := &tcpUserPeer{jwks: keyDocument(t, "user-key"), token: "outgoing-service"}
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	userv1.RegisterUserInternalServiceServer(srv, peer)
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.Stop(); <-done })
	ca := tempFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cluster.Certificate().Raw}))
	serviceToken := tempFile(t, "internal-token", []byte("outgoing-service"))
	apiToken := tempFile(t, "api-token", []byte("api-audience-token"))
	return func(cfg *config.Config) {
		cfg.Env = config.EnvProd
		cfg.Peers = map[string]string{"user": "dns:///" + lis.Addr().String()}
		cfg.Auth = config.AuthConfig{Mode: config.AuthVerify, ServiceTokenFile: serviceToken, Identity: config.IdentityConfig{
			UserIssuer: "https://user.example", UserAudience: "mobile",
			Kubernetes: config.KubernetesIdentity{Issuer: "https://cluster.example", JWKSURL: cluster.URL, CAFile: ca, APITokenFile: apiToken, Namespace: "finassist"},
		}}
	}, peer, &clusterServed
}

// With AUTH_MODE=verify the whole assembly starts on real key sources: it serves
// health, presents the projected token on outgoing calls, and its identity-keys job
// fetches user keys from user over TCP and ServiceAccount keys over pinned TLS.
func TestAPIVerifyIdentityStartsOnRealKeySources(t *testing.T) {
	mutate, peer, clusterServed := prodIdentity(t)
	env := Start(t, mutate)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(env.Conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health: %v %v", resp, err)
	}
	if token, err := env.App.Tokens.Token(ctx); token != "outgoing-service" || err != nil {
		t.Fatalf("outgoing token: %q %v", token, err)
	}
	if len(env.App.Jobs) != 1 || env.App.Jobs[0].Name != "identity-keys" {
		t.Fatalf("jobs: %+v", env.App.Jobs)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- env.App.Jobs[0].Run(runCtx) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for peer.accepted.Load() == 0 || clusterServed.Load() == 0 {
		select {
		case <-ctx.Done():
			stop()
			t.Fatalf("keys not fetched: user=%d cluster=%d", peer.accepted.Load(), clusterServed.Load())
		case <-tick.C:
		}
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("identity-keys job: %v", err)
	}
	if peer.refused.Load() != 0 {
		t.Fatal("user peer saw a call without the service token")
	}
	// The template serves no business RPC: an unknown method stays unimplemented
	// whatever credentials are presented.
	err = env.Conn.Invoke(metadata.AppendToOutgoingContext(ctx, "x-service-authorization", "Bearer forged"), "/finassist.nothing.v1.Nope/Get", &healthpb.HealthCheckRequest{}, &healthpb.HealthCheckResponse{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("unknown method: %v", err)
	}
}

// A misconfigured verify start fails before anything is served, with no fallback to
// dev identities.
func TestAPIVerifyIdentityRefusesMisconfiguredStart(t *testing.T) {
	mutate, peer, _ := prodIdentity(t)
	cases := map[string]func(*config.Config){
		"dev environment": func(c *config.Config) { c.Env = config.EnvDev },
		"no user peer":    func(c *config.Config) { delete(c.Peers, "user") },
		"no user issuer":  func(c *config.Config) { c.Auth.Identity.UserIssuer = "" },
		"missing CA":      func(c *config.Config) { c.Auth.Identity.Kubernetes.CAFile = filepath.Join(t.TempDir(), "missing") },
		"shared token":    func(c *config.Config) { c.Auth.Identity.Kubernetes.APITokenFile = c.Auth.ServiceTokenFile },
		"stage key in prod": func(c *config.Config) {
			c.Auth.Identity.Stage.SigningKeyFile = tempFile(t, "stage.pem", []byte("key"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			env, cleanup, err := start(t.Context(), testdb.DSN(t), realDependencies(), mutate, change)
			if err == nil || env != nil || cleanup != nil || !strings.HasPrefix(err.Error(), "app:") {
				if cleanup != nil {
					cleanup()
				}
				t.Fatalf("misconfigured identity started: %v", err)
			}
		})
	}
	if peer.accepted.Load() != 0 || peer.refused.Load() != 0 {
		t.Fatal("refused start contacted user")
	}
}
