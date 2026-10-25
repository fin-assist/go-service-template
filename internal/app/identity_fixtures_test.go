package app

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	userv1 "github.com/fin-assist/contracts/gen/go/finassist/user/v1"
	"github.com/fin-assist/go-grpc/reqctx"
	"github.com/fin-assist/go-grpc/server"
)

// Fixtures of the identity tests: signing keys, JWKS documents, a fake user peer and
// a probe service behind the real go-grpc server chain. The probe exists only in
// tests: the template serves no business RPC.

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func writeFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func signJWT(t *testing.T, key ed25519.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	data := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	return data + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(data)))
}

// forgeJWT builds a token with an arbitrary header: alg none or an HMAC keyed with a
// public key, the classic algorithm substitutions.
func forgeJWT(t *testing.T, header map[string]string, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	data := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(body)
	return data + "." + base64.RawURLEncoding.EncodeToString(sign([]byte(data)))
}

func noneJWT(t *testing.T, kid string, claims map[string]any) string {
	return forgeJWT(t, map[string]string{"alg": "none", "typ": "JWT", "kid": kid}, claims, func([]byte) []byte { return nil })
}

func hs256WithPublicKey(t *testing.T, public ed25519.PublicKey, kid string, claims map[string]any) string {
	return forgeJWT(t, map[string]string{"alg": "HS256", "typ": "JWT", "kid": kid}, claims, func(data []byte) []byte {
		mac := hmac.New(sha256.New, public)
		mac.Write(data)
		return mac.Sum(nil)
	})
}

func jwks(t *testing.T, keys map[string]ed25519.PublicKey) string {
	t.Helper()
	var list []any
	for kid, key := range keys {
		list = append(list, map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(key)})
	}
	b, err := json.Marshal(map[string]any{"keys": list})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func privateKeyPEM(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// clusterKeys is the HTTPS JWKS endpoint of the Kubernetes API server. It answers only
// the API-audience token and counts the requests it served.
func clusterKeys(t *testing.T, document, apiToken string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var served atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		served.Add(1)
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(srv.Close)
	return srv, &served
}

func caFile(t *testing.T, srv *httptest.Server) string {
	return writeFile(t, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

// userPeer plays the user service: GetSigningKeys answers the user JWKS to a caller
// that presents an accepted service token and no user token.
type userPeer struct {
	userv1.UnimplementedUserInternalServiceServer
	jwks    string
	accept  func(token string) bool
	fail    bool
	mu      sync.Mutex
	calls   int
	refused int
}

func (p *userPeer) GetSigningKeys(ctx context.Context, _ *userv1.GetSigningKeysRequest) (*userv1.GetSigningKeysResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	service := md.Get("x-service-authorization")
	if len(service) != 1 || len(md.Get("authorization")) != 0 || len(service[0]) < 8 || !p.accept(service[0][len("Bearer "):]) {
		p.refused++
		return nil, status.Error(codes.Unauthenticated, "")
	}
	p.calls++
	if p.fail {
		return nil, status.Error(codes.Unavailable, "")
	}
	return &userv1.GetSigningKeysResponse{JwksJson: p.jwks}, nil
}

func (p *userPeer) counts() (calls, refused int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.refused
}

// serveBufconn serves srv on an in-memory listener and returns a dialer for it.
func serveBufconn(t *testing.T, serve func(net.Listener) error, stop func()) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	done := make(chan struct{})
	go func() { defer close(done); _ = serve(lis) }()
	t.Cleanup(func() { stop(); _ = lis.Close(); <-done })
	return func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }
}

func startUserPeer(t *testing.T, peer *userPeer) []grpc.DialOption {
	t.Helper()
	opts, _ := startTrackedUserPeer(t, peer)
	return opts
}

// connTracker counts the client connections to the user peer and how many of them
// the client closed, so a test can prove the identity released its connection.
type connTracker struct {
	opened, closed atomic.Int32
}

type trackedConn struct {
	net.Conn
	once    sync.Once
	tracker *connTracker
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.tracker.closed.Add(1) })
	return c.Conn.Close()
}

// allClosed waits until every opened connection is closed; false at the deadline.
func (c *connTracker) allClosed(ctx context.Context) bool {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if opened := c.opened.Load(); opened > 0 && c.closed.Load() == opened {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

func startTrackedUserPeer(t *testing.T, peer *userPeer) ([]grpc.DialOption, *connTracker) {
	t.Helper()
	srv := grpc.NewServer()
	userv1.RegisterUserInternalServiceServer(srv, peer)
	dial := serveBufconn(t, srv.Serve, srv.Stop)
	tracker := &connTracker{}
	return []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		conn, err := dial(ctx, addr)
		if err != nil {
			return nil, err
		}
		tracker.opened.Add(1)
		return &trackedConn{Conn: conn, tracker: tracker}, nil
	})}, tracker
}

// probe records which verified identities reached a handler.
type probe struct {
	userv1.UnimplementedProfileServiceServer
	userv1.UnimplementedUserInternalServiceServer
	mu      sync.Mutex
	callers []string
	users   []string
}

func (p *probe) GetProfile(ctx context.Context, _ *userv1.GetProfileRequest) (*userv1.GetProfileResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	caller := reqctx.Caller(ctx)
	user, _ := reqctx.UserID(ctx)
	p.callers, p.users = append(p.callers, caller), append(p.users, user)
	return &userv1.GetProfileResponse{}, nil
}

func (p *probe) GetSigningKeys(ctx context.Context, _ *userv1.GetSigningKeysRequest) (*userv1.GetSigningKeysResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	caller := reqctx.Caller(ctx)
	p.callers = append(p.callers, caller)
	return &userv1.GetSigningKeysResponse{}, nil
}

func (p *probe) seen() (callers, users []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.callers...), append([]string(nil), p.users...)
}

// probePolicy admits bank-bff to a user-model and a no-user-model method.
func probePolicy() server.Policy {
	return server.Policy{
		"/finassist.user.v1.ProfileService/*":      {Callers: []string{"bank-bff"}, Model: server.ModelUser},
		"/finassist.user.v1.UserInternalService/*": {Callers: []string{"bank-bff"}, Model: server.ModelNoUser},
	}
}

// startChain serves the probe through server.New with the identity under test and
// returns a plain client connection to it.
func startChain(t *testing.T, parts identityParts, deps server.Deps) (*grpc.ClientConn, *probe) {
	t.Helper()
	deps.Policy, deps.Services, deps.Users, deps.Tombstones = probePolicy(), parts.services, parts.users, server.NoTombstones{}
	srv, err := server.New(server.Options{MaxRecvMsgSize: 4 << 20}, deps)
	if err != nil {
		t.Fatal(err)
	}
	p := &probe{}
	userv1.RegisterProfileServiceServer(srv.GRPC(), p)
	userv1.RegisterUserInternalServiceServer(srv.GRPC(), p)
	if err := srv.Check(); err != nil {
		t.Fatal(err)
	}
	srv.SetServing(true)
	dial := serveBufconn(t, srv.Serve, srv.GRPC().Stop)
	conn, err := grpc.NewClient("passthrough:///chain", grpc.WithContextDialer(dial), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, p
}

// call invokes GetProfile with the given service and user tokens; empty means absent.
func callProfile(ctx context.Context, conn *grpc.ClientConn, serviceToken, userToken string) error {
	var kv []string
	if serviceToken != "" {
		kv = append(kv, "x-service-authorization", "Bearer "+serviceToken)
	}
	if userToken != "" {
		kv = append(kv, "authorization", "Bearer "+userToken)
	}
	_, err := userv1.NewProfileServiceClient(conn).GetProfile(metadata.AppendToOutgoingContext(ctx, kv...), &userv1.GetProfileRequest{})
	return err
}
