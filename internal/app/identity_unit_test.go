package app

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"github.com/fin-assist/go-service-template/internal/config"
)

func TestUnitIdentityInsecureDevOnlyInDev(t *testing.T) {
	cfg := config.Config{Service: "template", Env: config.EnvDev, Auth: config.AuthConfig{Mode: config.AuthInsecureDev}}
	id, err := identity(cfg, prometheus.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer id.close()
	if got, err := id.services.VerifyService(t.Context(), "dev:bank-bff"); got != "bank-bff" || err != nil {
		t.Fatalf("service=%q err=%v", got, err)
	}
	if got, err := id.users.VerifyUser(t.Context(), "dev:u1"); got != "u1" || err != nil {
		t.Fatalf("user=%q err=%v", got, err)
	}
	if got, err := id.tokens.Token(t.Context()); got != "dev:template" || err != nil {
		t.Fatalf("token=%q err=%v", got, err)
	}
	if len(id.jobs) != 0 {
		t.Fatal("dev identity must not start key refresh jobs")
	}
	cfg.Auth.Mode = "trust-everyone"
	if id, err := identity(cfg, prometheus.NewRegistry(), nil, nil); err == nil || id.services != nil {
		t.Fatal("unknown AUTH_MODE accepted")
	}
}

// verifyConfig is a complete prod configuration. Its files do not exist: every case
// below is refused by validation before any file, connection or metric is touched.
func verifyConfig() config.Config {
	return config.Config{Service: "template", Env: config.EnvProd, Peers: map[string]string{"user": "passthrough:///user"},
		Auth: config.AuthConfig{Mode: config.AuthVerify, ServiceTokenFile: "/secret/internal-token", Identity: config.IdentityConfig{
			UserIssuer: "https://user.example", UserAudience: "mobile",
			Kubernetes: config.KubernetesIdentity{
				Issuer: "https://cluster.example", JWKSURL: "https://cluster.example/keys", CAFile: "/secret/ca.pem",
				APITokenFile: "/secret/api-token", Namespace: "finassist",
			},
		}}}
}

// Verify never falls back to a weaker mode: an incomplete or contradictory
// configuration refuses the start, leaves nothing registered and never dials user.
func TestUnitIdentityVerifyRefusesInvalidConfigurationWithoutSideEffects(t *testing.T) {
	tests := map[string]func(*config.Config){
		"dev environment":     func(c *config.Config) { c.Env = config.EnvDev },
		"empty environment":   func(c *config.Config) { c.Env = "" },
		"unknown environment": func(c *config.Config) { c.Env = "qa" },
		"no user peer":        func(c *config.Config) { delete(c.Peers, "user") },
		"no user issuer":      func(c *config.Config) { c.Auth.Identity.UserIssuer = " " },
		"no user audience":    func(c *config.Config) { c.Auth.Identity.UserAudience = "" },
		"no service name":     func(c *config.Config) { c.Service = "" },
		"no service token":    func(c *config.Config) { c.Auth.ServiceTokenFile = "" },
		"shared token file":   func(c *config.Config) { c.Auth.ServiceTokenFile = "/secret/api-token" },
		"stage files in prod": func(c *config.Config) { c.Auth.Identity.Stage.SigningKeyFile = "/secret/stage.pem" },
		"bad namespace":       func(c *config.Config) { c.Auth.Identity.Kubernetes.Namespace = "bad:namespace" },
		"stage without issuer": func(c *config.Config) {
			c.Env = config.EnvStage
			c.Auth.Identity.Stage = config.StageIdentity{SigningKeyFile: "/secret/k", PublicKeysFile: "/secret/p"}
		},
		"stage without keys": func(c *config.Config) {
			c.Env = config.EnvStage
			c.Auth.Identity.Stage = config.StageIdentity{Issuer: "https://stage.example"}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := verifyConfig()
			change(&cfg)
			reg := prometheus.NewRegistry()
			dialed := false
			dial := []grpc.DialOption{grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
				dialed = true
				return invoker(ctx, method, req, reply, cc, opts...)
			})}
			id, err := identity(cfg, reg, nil, dial)
			if err == nil {
				id.close()
				t.Fatal("invalid verify configuration accepted")
			}
			if id.services != nil || id.users != nil || id.tokens != nil || id.jobs != nil || id.close != nil || dialed {
				t.Fatal("refused identity returned parts or dialed user")
			}
			if strings.Contains(err.Error(), "/secret") {
				t.Fatalf("error exposes a configured path: %v", err)
			}
			families, gatherErr := reg.Gather()
			if gatherErr != nil || len(families) != 0 {
				t.Fatalf("refused identity left metrics registered: %v %v", families, gatherErr)
			}
		})
	}
}

func TestUnitStageFilesMapping(t *testing.T) {
	got := stageFiles(config.StageIdentity{Issuer: "i", SigningKeyFile: "k", PublicKeysFile: "p"})
	if got.Issuer != "i" || got.SigningKeyFile != "k" || got.PublicKeysFile != "p" {
		t.Fatalf("stage files not passed through: %+v", got)
	}
}
