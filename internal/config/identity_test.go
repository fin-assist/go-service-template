package config

import (
	"strings"
	"testing"
)

func TestUnitIdentityDefaultsAndOverrides(t *testing.T) {
	get, list := env(base())
	c, err := load(get, noFiles, list)
	if err != nil {
		t.Fatal(err)
	}
	k := c.Auth.Identity.Kubernetes
	if k.Issuer != "https://kubernetes.default.svc.cluster.local" || k.JWKSURL != "https://kubernetes.default.svc/openid/v1/jwks" ||
		k.CAFile != "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt" || k.APITokenFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" ||
		k.Namespace != "finassist" || !c.Auth.Identity.Stage.empty() {
		t.Fatalf("defaults: %+v", c.Auth.Identity)
	}
	if c.Auth.Identity.UserIssuer != "https://user.example" || c.Auth.Identity.UserAudience != "mobile" || c.Peers["user"] != "dns:///user:9090" {
		t.Fatalf("user identity: %+v", c.Auth.Identity)
	}
	vars := base()
	delete(vars, "SERVICE_TOKEN_FILE")
	vars["ENV"] = "stage"
	vars["AUTH_STAGE_ISSUER"] = "https://stage.example"
	vars["AUTH_STAGE_SIGNING_KEY_FILE"] = "/run/credentials/service/key.pem"
	vars["AUTH_STAGE_PUBLIC_KEYS_FILE"] = "/etc/finassist/stage-keys.json"
	vars["AUTH_KUBERNETES_NAMESPACE"] = "isolated"
	get, list = env(vars)
	c, err = load(get, noFiles, list)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.Identity.Stage != (StageIdentity{Issuer: "https://stage.example", SigningKeyFile: "/run/credentials/service/key.pem", PublicKeysFile: "/etc/finassist/stage-keys.json"}) ||
		c.Auth.Identity.Kubernetes.Namespace != "isolated" {
		t.Fatalf("stage overrides: %+v", c.Auth.Identity)
	}
}

func TestUnitIdentityValidation(t *testing.T) {
	stage := map[string]string{"ENV": "stage", "AUTH_STAGE_ISSUER": "https://stage.example",
		"AUTH_STAGE_SIGNING_KEY_FILE": "/k.pem", "AUTH_STAGE_PUBLIC_KEYS_FILE": "/p.json"}
	tests := []struct {
		name string
		set  map[string]string
		drop []string
		want string
	}{
		{"user issuer", nil, []string{"AUTH_USER_ISSUER"}, "AUTH_USER_ISSUER is required"},
		{"user audience", map[string]string{"AUTH_USER_AUDIENCE": " "}, nil, "AUTH_USER_AUDIENCE is required"},
		{"user peer", nil, []string{"PEER_USER_ADDR"}, "PEER_USER_ADDR is required"},
		{"dev environment", map[string]string{"ENV": "dev"}, nil, "requires ENV=prod or ENV=stage"},
		{"prod service token", nil, []string{"SERVICE_TOKEN_FILE"}, "SERVICE_TOKEN_FILE is required with ENV=prod"},
		{"prod empty namespace", map[string]string{"AUTH_KUBERNETES_NAMESPACE": " "}, nil, "AUTH_KUBERNETES_NAMESPACE is required"},
		{"shared token file", map[string]string{"AUTH_KUBERNETES_API_TOKEN_FILE": "/var/run/secrets/finassist//token"}, nil, "must be separate files"},
		{"stage file in prod", map[string]string{"AUTH_STAGE_PUBLIC_KEYS_FILE": "/p.json"}, nil, "not allowed with ENV=prod"},
		{"stage issuer", merge(stage, map[string]string{"AUTH_STAGE_ISSUER": ""}), nil, "AUTH_STAGE_ISSUER is required with ENV=stage"},
		{"stage signing key", merge(stage, map[string]string{"AUTH_STAGE_SIGNING_KEY_FILE": ""}), nil, "AUTH_STAGE_SIGNING_KEY_FILE is required"},
		{"stage public keys", merge(stage, map[string]string{"AUTH_STAGE_PUBLIC_KEYS_FILE": ""}), nil, "AUTH_STAGE_PUBLIC_KEYS_FILE is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := merge(base(), tt.set)
			for _, k := range tt.drop {
				delete(vars, k)
			}
			get, list := env(vars)
			if _, err := load(get, noFiles, list); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
		})
	}
	// Stage needs no Kubernetes token; insecure-dev needs no identity settings at all.
	vars := merge(base(), stage)
	delete(vars, "SERVICE_TOKEN_FILE")
	get, list := env(vars)
	if _, err := load(get, noFiles, list); err != nil {
		t.Fatalf("stage: %v", err)
	}
	get, list = env(map[string]string{"SERVICE_NAME": "s", "DB_PRIMARY_DSN": "postgres://p/db", "ENV": "dev", "AUTH_MODE": "insecure-dev"})
	if _, err := load(get, noFiles, list); err != nil {
		t.Fatalf("insecure-dev: %v", err)
	}
}

// The JWKS endpoint is pinned HTTPS; a URL with credentials is refused without
// echoing them.
func TestUnitIdentityJWKSURL(t *testing.T) {
	for _, address := range []string{"http://cluster/jwks", "https://name:secret@cluster/jwks", "https://cluster/jwks?token=secret", "https://cluster/jwks#secret", "https:///jwks", ":secret"} {
		t.Run(address, func(t *testing.T) {
			get, list := env(merge(base(), map[string]string{"AUTH_KUBERNETES_JWKS_URL": address}))
			_, err := load(get, noFiles, list)
			if err == nil || !strings.Contains(err.Error(), "AUTH_KUBERNETES_JWKS_URL must be HTTPS") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe URL accepted or leaked: %v", err)
			}
		})
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
