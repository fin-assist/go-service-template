package main

import (
	"strings"
	"testing"

	"github.com/fin-assist/go-service-template/internal/config"
)

// Outside dev the service refuses to start until real verifiers are wired (FIN-119),
// instead of serving with identities nobody checks.
func TestIdentityFailsClosed(t *testing.T) {
	_, _, _, err := identity(config.Config{Service: "user", Env: config.EnvProd, Auth: config.AuthConfig{Mode: config.AuthVerify}})
	if err == nil || !strings.Contains(err.Error(), "FIN-119") {
		t.Fatalf("error %v", err)
	}
	s, u, tok, err := identity(config.Config{Service: "user", Env: config.EnvDev, Auth: config.AuthConfig{Mode: config.AuthInsecureDev}})
	if err != nil || s == nil || u == nil || tok == nil {
		t.Fatalf("dev: %v", err)
	}
}

func TestServeFailsBeforeConnectingWithoutVerifiers(t *testing.T) {
	t.Setenv("SERVICE_NAME", "user")
	t.Setenv("DB_PRIMARY_DSN", "postgres://nowhere.invalid/db")
	t.Setenv("ENV", "prod")
	err := runServe(t.Context())
	if err == nil || !strings.Contains(err.Error(), "FIN-119") {
		t.Fatalf("error %v", err)
	}
}
