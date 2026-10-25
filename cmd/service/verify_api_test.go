package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/fin-assist/go-service-template/internal/testdb"
)

type keysPeer struct {
	userv1.UnimplementedUserInternalServiceServer
	jwks     string
	accepted atomic.Int32
}

func (p *keysPeer) GetSigningKeys(ctx context.Context, _ *userv1.GetSigningKeysRequest) (*userv1.GetSigningKeysResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("x-service-authorization"); len(got) != 1 || got[0] != "Bearer outgoing-service" {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	p.accepted.Add(1)
	return &userv1.GetSigningKeysResponse{JwksJson: p.jwks}, nil
}

func ed25519JWKS(t *testing.T, kid string) string {
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

// The binary started with ENV=prod and AUTH_MODE=verify from the environment serves,
// and its supervised identity-keys job fetches both key sets: user keys over TCP with
// the projected service token, ServiceAccount keys over TLS pinned to the CA file.
func TestAPICommandServesWithVerifyIdentity(t *testing.T) {
	dsn := testdb.DSN(t)
	dir := t.TempDir()
	write := func(name string, content []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var clusterServed atomic.Int32
	cluster := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-audience-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		clusterServed.Add(1)
		_, _ = w.Write([]byte(ed25519JWKS(t, "cluster-key")))
	}))
	defer cluster.Close()
	peer := &keysPeer{jwks: ed25519JWKS(t, "user-key")}
	userLis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	userSrv := grpc.NewServer()
	userv1.RegisterUserInternalServiceServer(userSrv, peer)
	go func() { _ = userSrv.Serve(userLis) }()
	defer userSrv.Stop()
	free := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		return l.Addr().String()
	}
	grpcAddr, httpAddr := free(), free()
	for k, v := range map[string]string{
		"SERVICE_NAME": "service", "ENV": "prod", "AUTH_MODE": "verify", "LOG_LEVEL": "error",
		"DB_PRIMARY_DSN": dsn, "GRPC_ADDR": grpcAddr, "HTTP_ADDR": httpAddr, "SHUTDOWN_DELAY": "0s", "SHUTDOWN_TIMEOUT": "5s",
		"AUTH_USER_ISSUER": "https://user.example", "AUTH_USER_AUDIENCE": "mobile", "PEER_USER_ADDR": "dns:///" + userLis.Addr().String(),
		"SERVICE_TOKEN_FILE":             write("internal-token", []byte("outgoing-service")),
		"AUTH_KUBERNETES_ISSUER":         "https://cluster.example",
		"AUTH_KUBERNETES_JWKS_URL":       cluster.URL,
		"AUTH_KUBERNETES_CA_FILE":        write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cluster.Certificate().Raw})),
		"AUTH_KUBERNETES_API_TOKEN_FILE": write("api-token", []byte("api-audience-token")),
	} {
		t.Setenv(k, v)
	}
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	serveCtx, stopServe := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- run(serveCtx, []string{"serve"}) }()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := healthv1.NewHealthClient(conn).Check(ctx, &healthv1.HealthCheckRequest{}, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("health: %v", err)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for peer.accepted.Load() == 0 || clusterServed.Load() == 0 {
		select {
		case <-ctx.Done():
			stopServe()
			t.Fatalf("identity-keys job did not run: user=%d cluster=%d", peer.accepted.Load(), clusterServed.Load())
		case <-tick.C:
		}
	}
	stopServe()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}
