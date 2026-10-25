package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/fin-assist/go-service-template/internal/testdb"
)

// During SHUTDOWN_DELAY the process still accepts calls, but both readiness signals a
// balancer can watch — HTTP /readyz and gRPC health — already say «not ready», while
// liveness stays up. Previously /readyz kept answering 200 for the whole delay.
func TestAPICommandShutdownDelayReportsNotReadyOnBothListeners(t *testing.T) {
	dsn := testdb.DSN(t)
	free := func() string {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		return addr
	}
	grpcAddr, httpAddr := free(), free()
	for k, v := range map[string]string{"SERVICE_NAME": "drain", "ENV": "dev", "AUTH_MODE": "insecure-dev", "LOG_LEVEL": "error",
		"DB_PRIMARY_DSN": dsn, "DB_PRIMARY_DSN_FILE": "", "GRPC_ADDR": grpcAddr, "HTTP_ADDR": httpAddr,
		"SHUTDOWN_DELAY": "3s", "SHUTDOWN_TIMEOUT": "2s"} {
		t.Setenv(k, v)
	}
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	serveCtx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- run(serveCtx, []string{"serve"}) }()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	get := func(path string) int {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+httpAddr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode
	}
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	health := healthv1.NewHealthClient(conn)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	await := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s: timed out", what)
			case err := <-done:
				t.Fatalf("%s: serve stopped early: %v", what, err)
			case <-tick.C:
			}
		}
	}
	await("ready", func() bool { return get("/readyz") == http.StatusOK })

	stop()
	drainStarted := time.Now()
	await("not ready while draining", func() bool {
		st, err := health.Check(ctx, &healthv1.HealthCheckRequest{})
		return get("/readyz") == http.StatusServiceUnavailable && err == nil && st.Status == healthv1.HealthCheckResponse_NOT_SERVING
	})
	if elapsed := time.Since(drainStarted); elapsed >= 3*time.Second {
		t.Fatalf("readiness went down only after the delay: %v", elapsed)
	}
	// Still inside SHUTDOWN_DELAY: liveness is up and readiness stays down.
	for range 5 {
		if code := get("/readyz"); code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz during drain: %d", code)
		}
		if code := get("/healthz"); code != http.StatusOK {
			t.Fatalf("/healthz during drain: %d", code)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("serve did not stop")
	}
}
