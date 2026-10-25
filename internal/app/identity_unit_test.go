package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

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

// verifyConfig is a complete prod configuration whose files do not exist: it passes
// every validation and is refused only when NewAssembly reads the CA file.
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

// verifyStageConfig is the stage counterpart: refused only when the key files are read.
func verifyStageConfig() config.Config {
	c := verifyConfig()
	c.Env, c.Auth.ServiceTokenFile, c.Auth.Identity.Kubernetes = config.EnvStage, "", config.KubernetesIdentity{}
	c.Auth.Identity.Stage = config.StageIdentity{Issuer: "https://stage.example", SigningKeyFile: "/secret/k.pem", PublicKeysFile: "/secret/p.json"}
	return c
}

// refusal is one invalid verify configuration and the condition its error must name.
type refusal struct {
	name   string
	stage  bool // applied to the stage base instead of the prod one
	change func(*config.Config)
	want   string
}

// verifyRefusals each break exactly one condition of a valid base configuration. The
// unit and the integration tests apply them to their own bases.
func verifyRefusals() []refusal {
	return []refusal{
		{"dev environment", false, func(c *config.Config) { c.Env = config.EnvDev }, "requires env prod or stage"},
		{"empty environment", false, func(c *config.Config) { c.Env = "" }, "requires env prod or stage"},
		{"unknown environment", false, func(c *config.Config) { c.Env = "qa" }, "requires env prod or stage"},
		{"no user peer", false, func(c *config.Config) { delete(c.Peers, "user") }, "PEER_USER_ADDR"},
		{"no user issuer", false, func(c *config.Config) { c.Auth.Identity.UserIssuer = " " }, "user issuer and audience"},
		{"no user audience", false, func(c *config.Config) { c.Auth.Identity.UserAudience = "" }, "user issuer and audience"},
		{"no service name", false, func(c *config.Config) { c.Service = "" }, "service name"},
		{"no service token", false, func(c *config.Config) { c.Auth.ServiceTokenFile = "" }, "separate service and API token files"},
		{"shared token path", false, func(c *config.Config) { c.Auth.ServiceTokenFile = c.Auth.Identity.Kubernetes.APITokenFile + "/." }, "separate service and API token files"},
		{"stage files in prod", false, func(c *config.Config) { c.Auth.Identity.Stage.SigningKeyFile = "/secret/stage.pem" }, "stage keys are not allowed in prod"},
		{"bad namespace", false, func(c *config.Config) { c.Auth.Identity.Kubernetes.Namespace = "bad:namespace" }, "issuer or namespace"},
		{"no cluster issuer", false, func(c *config.Config) { c.Auth.Identity.Kubernetes.Issuer = " " }, "issuer or namespace"},
		{"stage without issuer", true, func(c *config.Config) { c.Auth.Identity.Stage.Issuer = "" }, "stage issuer and key files"},
		{"stage without signing key", true, func(c *config.Config) { c.Auth.Identity.Stage.SigningKeyFile = "" }, "stage issuer and key files"},
		{"stage without public keys", true, func(c *config.Config) { c.Auth.Identity.Stage.PublicKeysFile = "" }, "stage issuer and key files"},
	}
}

// assertRefused checks a refusal: the named condition, no parts, no path in the
// error and no metric left in the registry.
func assertRefused(t *testing.T, id identityParts, err error, reg *prometheus.Registry, want string) {
	t.Helper()
	if err == nil {
		id.close()
		t.Fatal("invalid verify configuration accepted")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name %q", err, want)
	}
	if id.services != nil || id.users != nil || id.tokens != nil || id.jobs != nil || id.close != nil {
		t.Fatal("refused identity returned parts")
	}
	if strings.Contains(err.Error(), "/secret") {
		t.Fatalf("error exposes a configured path: %v", err)
	}
	if families, gatherErr := reg.Gather(); gatherErr != nil || len(families) != 0 {
		t.Fatalf("refused identity left metrics registered: %v %v", families, gatherErr)
	}
}

// Verify never falls back to a weaker mode: each broken condition refuses the start
// with its own error, before NewAssembly reads any file (the unchanged bases are
// refused only by their missing files), and leaves nothing registered.
func TestUnitIdentityVerifyRefusesInvalidConfigurationWithoutSideEffects(t *testing.T) {
	for _, base := range []struct {
		name  string
		cfg   func() config.Config
		files string
	}{{"prod", verifyConfig, "Kubernetes CA file unavailable"}, {"stage", verifyStageConfig, "stage public keys unavailable"}} {
		t.Run("unchanged "+base.name+" base reaches the files", func(t *testing.T) {
			reg := prometheus.NewRegistry()
			id, err := identity(base.cfg(), reg, nil, nil)
			assertRefused(t, id, err, reg, base.files)
		})
	}
	for _, r := range verifyRefusals() {
		t.Run(r.name, func(t *testing.T) {
			cfg := verifyConfig()
			if r.stage {
				cfg = verifyStageConfig()
			}
			r.change(&cfg)
			reg := prometheus.NewRegistry()
			id, err := identity(cfg, reg, nil, nil)
			assertRefused(t, id, err, reg, r.want)
		})
	}
}

// The service and API tokens must be different files, not only different paths: a
// symlink (as the ..data link of a projected volume) or a hard link is refused.
func TestUnitIdentityRefusesTheSameTokenFileUnderAnotherName(t *testing.T) {
	dir := t.TempDir()
	apiToken := filepath.Join(dir, "token")
	if err := os.WriteFile(apiToken, []byte("api-audience-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink, hardlink := filepath.Join(dir, "symlink"), filepath.Join(dir, "hardlink")
	if err := os.Symlink(apiToken, symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(apiToken, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{symlink, hardlink} {
		cfg := verifyConfig()
		cfg.Auth.ServiceTokenFile, cfg.Auth.Identity.Kubernetes.APITokenFile = path, apiToken
		reg := prometheus.NewRegistry()
		id, err := identity(cfg, reg, nil, nil)
		assertRefused(t, id, err, reg, "must be separate files")
		if strings.Contains(err.Error(), dir) {
			t.Fatalf("error exposes a path: %v", err)
		}
	}
	other := filepath.Join(dir, "internal-token")
	if err := os.WriteFile(other, []byte("api-audience-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sameFile(other, apiToken) || sameFile(filepath.Join(dir, "missing"), apiToken) || sameFile("", apiToken) {
		t.Fatal("distinct, missing or empty paths reported as the same file")
	}
	if kubernetesAPITokenFile("") != defaultKubernetesAPITokenFile || kubernetesAPITokenFile("/x") != "/x" {
		t.Fatal("API token file default")
	}
}

func TestUnitStageFilesMapping(t *testing.T) {
	got := stageFiles(config.StageIdentity{Issuer: "i", SigningKeyFile: "k", PublicKeysFile: "p"})
	if got.Issuer != "i" || got.SigningKeyFile != "k" || got.PublicKeysFile != "p" {
		t.Fatalf("stage files not passed through: %+v", got)
	}
}
