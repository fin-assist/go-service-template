package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	migratetest "github.com/fin-assist/go-service-template/internal/testdb"
)

func TestUnitProcessBoundaryReportsUsageAndConfigurationFailures(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	t.Setenv("DB_PRIMARY_DSN", "")
	t.Setenv("DB_PRIMARY_DSN_FILE", "")
	for _, args := range [][]string{nil, {"unknown"}, {"serve", "extra"}, {"migrate"}} {
		var stderr bytes.Buffer
		if code := mainExit(args, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	for _, args := range [][]string{{"serve"}, {"migrate", "up"}} {
		var stderr bytes.Buffer
		if code := mainExit(args, &stderr); code != 1 || !strings.Contains(stderr.String(), "failed") {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestAPICommandConfigurationAndUsageAreRealExitStatuses(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, args := range [][]string{{"not-a-command"}, {"serve", "unexpected"}, {"migrate"}} {
		var stderr bytes.Buffer
		if code := mainExit(args, &stderr); code != 2 || !strings.Contains(stderr.String(), "service migrate") {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	for _, variant := range []string{"missing-primary", "invalid-env", "invalid-log", "dev-auth-in-prod", "missing-secret-file", "invalid-migration-timeout"} {
		t.Run(variant, func(t *testing.T) {
			for k, v := range map[string]string{"SERVICE_NAME": "service", "ENV": "dev", "AUTH_MODE": "insecure-dev", "LOG_FORMAT": "json", "LOG_LEVEL": "error", "DB_PRIMARY_DSN": "postgres://unused.invalid/db", "DB_PRIMARY_DSN_FILE": ""} {
				t.Setenv(k, v)
			}
			args := []string{"serve"}
			switch variant {
			case "missing-primary":
				t.Setenv("DB_PRIMARY_DSN", "")
			case "invalid-env":
				t.Setenv("ENV", "not-an-env")
			case "invalid-log":
				t.Setenv("LOG_LEVEL", "not-a-level")
			case "dev-auth-in-prod":
				t.Setenv("ENV", "prod")
			case "missing-secret-file":
				t.Setenv("DB_PRIMARY_DSN", "")
				t.Setenv("DB_PRIMARY_DSN_FILE", filepath.Join(t.TempDir(), "missing"))
			case "invalid-migration-timeout":
				args = []string{"migrate", "up"}
				t.Setenv("MIGRATE_LOCK_TIMEOUT", "later")
			}
			var stderr bytes.Buffer
			if code := mainExit(args, &stderr); code != 1 || !strings.Contains(stderr.String(), "failed") {
				t.Fatalf("configuration accepted: code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestIntegrationCommandEmptySourceMigrationsAndVersions(t *testing.T) {
	dsn := migratetest.DSN(t)
	t.Setenv("DB_PRIMARY_DSN", dsn)
	t.Setenv("DB_PRIMARY_DSN_FILE", "")
	if err := runMigrate(t.Context(), []string{"up"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runMigrate(t.Context(), []string{"version"}, &out); err != nil || out.String() != "service\t0\n" {
		t.Fatalf("version=%q err=%v", out.String(), err)
	}
	if err := runMigrate(t.Context(), []string{"force", "service", "0"}, &bytes.Buffer{}); err == nil {
		t.Fatal("empty migration source must not accept force")
	}
	for _, args := range [][]string{{"down", "service", "1"}, {"force", "service", "-2"}, {"down", "service", "0"}, {"version", "extra"}, {"force", "service", "not-a-number"}} {
		if err := runMigrate(t.Context(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid empty-source operation succeeded: %v", args)
		}
	}
}

func TestAPIProcessMigrationCommandsUsePublishedRunnerAndReturnSuccess(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	t.Setenv("DB_PRIMARY_DSN", migratetest.DSN(t))
	t.Setenv("DB_PRIMARY_DSN_FILE", "")
	for _, args := range [][]string{{"migrate", "up"}, {"migrate", "version"}} {
		var stderr bytes.Buffer
		if code := mainExit(args, &stderr); code != 0 {
			t.Fatalf("deployment command %v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	for _, args := range [][]string{{"migrate", "down", "service", "1"}, {"migrate", "force", "service", "0"}, {"migrate", "force", "service", "-2"}, {"migrate", "down", "service", "0"}, {"migrate", "down", "unknown", "1"}} {
		var stderr bytes.Buffer
		if code := mainExit(args, &stderr); code != 1 || !strings.Contains(stderr.String(), "failed") {
			t.Fatalf("invalid operation %v code=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestUnitServeConfigErrorsNeverStartAssembly(t *testing.T) {
	t.Setenv("DB_PRIMARY_DSN", "")
	t.Setenv("DB_PRIMARY_DSN_FILE", "")
	if err := runServe(t.Context()); err == nil {
		t.Fatal("missing database configuration accepted")
	}
}
func TestAPICommandMigrateReadsDSNCredentialFile(t *testing.T) {
	dsn := migratetest.DSN(t)
	path := filepath.Join(t.TempDir(), "primary-dsn")
	if err := os.WriteFile(path, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PRIMARY_DSN", "")
	t.Setenv("DB_PRIMARY_DSN_FILE", path)
	var out bytes.Buffer
	if err := runMigrate(t.Context(), []string{"up"}, &out); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runMigrate(ctx, []string{"version"}, &out); err == nil {
		t.Fatal("cancelled deployment read ignored cancellation")
	}
}
func TestUnitMigrationUsageAndUnknownSourceDoNotDial(t *testing.T) {
	t.Setenv("DB_PRIMARY_DSN", "postgres://unreachable.invalid/db")
	t.Setenv("DB_PRIMARY_DSN_FILE", "")
	for _, args := range [][]string{{"down", "service", "invalid"}, {"force", "service", "invalid"}, {"down", "missing", "1"}} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err := runMigrate(ctx, args, &bytes.Buffer{})
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("validation attempted IO: %v", err)
		}
	}
}
