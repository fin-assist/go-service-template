package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/fin-assist/go-service-template/internal/adapter/postgres"
)

const startupTestSecret = "startup-secret"
const startupPingPhase = "ping"

// stalledPrimary accepts a real pgx connection. It either withholds the handshake
// or authenticates and withholds Ping's response. No database semantics are faked.
func stalledPrimary(t *testing.T, phase string) (string, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered, closed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(); close(closed) }()
		_ = conn.SetDeadline(time.Now().Add(40 * time.Second))
		if phase == startupPingPhase {
			backend := pgproto3.NewBackend(conn, conn)
			if _, err := backend.ReceiveStartupMessage(); err != nil {
				t.Error("startup packet:", err)
				return
			}
			backend.Send(&pgproto3.AuthenticationOk{})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err := backend.Flush(); err != nil {
				t.Error("authentication response:", err)
				return
			}
			message, err := backend.Receive()
			if _, ok := message.(*pgproto3.Query); err != nil || !ok {
				t.Error("expected startup Ping")
				return
			}
		}
		close(entered)
		_, _ = io.Copy(io.Discard, conn)
	}()
	t.Cleanup(func() { _ = lis.Close(); <-done })
	return "postgres://startup:" + startupTestSecret + "@" + lis.Addr().String() + "/db?sslmode=disable", entered, closed
}

func TestIntegrationPoolStartupHonorsCancellationAndClosesPendingConnections(t *testing.T) {
	for _, phase := range []string{"connect", startupPingPhase} {
		t.Run(phase, func(t *testing.T) {
			dsn, entered, closed := stalledPrimary(t, phase)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				deadlineCtx, stop := context.WithTimeout(ctx, 3*time.Second)
				defer stop()
				db, err := postgres.Open(deadlineCtx, postgres.Options{PrimaryDSN: dsn})
				if db != nil {
					db.Close()
				}
				done <- err
			}()
			defer cancel()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("pgx did not reach the stalled phase")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), startupTestSecret) {
					t.Fatal("cancellation must return a safe context error")
				}
			case <-time.After(17 * time.Second):
				t.Fatal("startup cleanup waited for the abandoned connection")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("failed startup retained its connection")
			}
		})
	}
}

func TestIntegrationPoolStartupPreservesEarlierDeadline(t *testing.T) {
	dsn, entered, closed := stalledPrimary(t, "connect")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := postgres.Open(ctx, postgres.Options{PrimaryDSN: dsn})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pgx did not reach the stalled connection")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("startup must preserve the earlier caller deadline")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup extended the caller deadline")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("deadline failure retained its connection")
	}
}

func TestAPIProcessStartupTimesOutBeforeOpeningListeners(t *testing.T) {
	dsn, entered, closed := stalledPrimary(t, startupPingPhase)
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	addresses := make([]string, 2)
	for i := range addresses {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addresses[i] = lis.Addr().String()
		_ = lis.Close()
	}
	for key, value := range map[string]string{
		"SERVICE_NAME": "startup-api", "ENV": "dev", "AUTH_MODE": "insecure-dev",
		"DB_PRIMARY_DSN": dsn, "DB_PRIMARY_DSN_FILE": "", "DB_REPLICA_DSN": "", "DB_REPLICA_DSN_FILE": "",
		"GRPC_ADDR": addresses[0], "HTTP_ADDR": addresses[1], "LOG_LEVEL": "error",
	} {
		t.Setenv(key, value)
	}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- mainExit([]string{"serve"}, &stderr) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not reach startup Ping")
	}
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(stderr.String(), "context deadline exceeded") || strings.Contains(stderr.String(), startupTestSecret) {
			t.Fatalf("startup failure: code=%d log=%q", code, stderr.String())
		}
	case <-time.After(28 * time.Second):
		t.Fatal("serve has no effective startup deadline")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("startup Ping connection was not released")
	}
	for _, address := range addresses {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatal("failed startup opened a service listener")
		}
	}
}
