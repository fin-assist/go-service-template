package config

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// IdentityConfig is the AUTH_MODE=verify configuration passed to go-identity
// NewAssembly. ENV selects the source of service identities: prod uses Kubernetes
// ServiceAccount tokens, stage uses short JWTs signed with file keys.
type IdentityConfig struct {
	// UserIssuer and UserAudience pin access JWTs issued by the user service.
	UserIssuer   string
	UserAudience string
	Kubernetes   KubernetesIdentity
	Stage        StageIdentity
}

// KubernetesIdentity configures ServiceAccount token verification in prod.
type KubernetesIdentity struct {
	Issuer       string
	JWKSURL      string // pinned HTTPS endpoint of the API server keys
	CAFile       string // the only CA trusted for JWKSURL
	APITokenFile string // API-server-audience token for JWKSURL, never the outgoing one
	Namespace    string // namespace of trusted callers
}

// StageIdentity configures the file keys of the stand. Every field is required in
// stage and forbidden in prod.
type StageIdentity struct {
	Issuer         string
	SigningKeyFile string // PKCS#8 PEM Ed25519 key of this service
	PublicKeysFile string // JWKS of the stand services, kid = service name
}

func (s StageIdentity) empty() bool {
	return s.Issuer == "" && s.SigningKeyFile == "" && s.PublicKeysFile == ""
}

func (r *reader) identity() IdentityConfig {
	return IdentityConfig{
		UserIssuer:   r.str("AUTH_USER_ISSUER"),
		UserAudience: r.str("AUTH_USER_AUDIENCE"),
		Kubernetes: KubernetesIdentity{
			Issuer:       r.str("AUTH_KUBERNETES_ISSUER"),
			JWKSURL:      r.str("AUTH_KUBERNETES_JWKS_URL"),
			CAFile:       r.str("AUTH_KUBERNETES_CA_FILE"),
			APITokenFile: r.str("AUTH_KUBERNETES_API_TOKEN_FILE"),
			Namespace:    r.str("AUTH_KUBERNETES_NAMESPACE"),
		},
		Stage: StageIdentity{
			Issuer:         r.str("AUTH_STAGE_ISSUER"),
			SigningKeyFile: r.str("AUTH_STAGE_SIGNING_KEY_FILE"),
			PublicKeysFile: r.str("AUTH_STAGE_PUBLIC_KEYS_FILE"),
		},
	}
}

type setting struct{ name, value string }

func missing(mode string, settings ...setting) []error {
	var errs []error
	for _, s := range settings {
		if strings.TrimSpace(s.value) == "" {
			errs = append(errs, errors.New(s.name+" is required with "+mode))
		}
	}
	return errs
}

// validateIdentity checks the verify settings by name before NewAssembly runs, so a
// misconfigured rollout fails with the variable to fix. Errors name variables only,
// never their values: a URL may carry credentials.
func (c Config) validateIdentity() []error {
	if c.Auth.Mode != AuthVerify {
		return nil
	}
	id := c.Auth.Identity
	errs := missing("AUTH_MODE=verify",
		setting{"AUTH_USER_ISSUER", id.UserIssuer},
		setting{"AUTH_USER_AUDIENCE", id.UserAudience},
		setting{"PEER_USER_ADDR", c.Peers["user"]},
	)
	switch c.Env {
	case EnvProd:
		errs = append(errs, missing("ENV=prod",
			setting{"SERVICE_TOKEN_FILE", c.Auth.ServiceTokenFile},
			setting{"AUTH_KUBERNETES_ISSUER", id.Kubernetes.Issuer},
			setting{"AUTH_KUBERNETES_JWKS_URL", id.Kubernetes.JWKSURL},
			setting{"AUTH_KUBERNETES_CA_FILE", id.Kubernetes.CAFile},
			setting{"AUTH_KUBERNETES_API_TOKEN_FILE", id.Kubernetes.APITokenFile},
			setting{"AUTH_KUBERNETES_NAMESPACE", id.Kubernetes.Namespace},
		)...)
		if v := id.Kubernetes.JWKSURL; v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				errs = append(errs, errors.New("AUTH_KUBERNETES_JWKS_URL must be HTTPS without credentials, query or fragment"))
			}
		}
		if f := c.Auth.ServiceTokenFile; f != "" && filepath.Clean(f) == filepath.Clean(id.Kubernetes.APITokenFile) {
			errs = append(errs, errors.New("SERVICE_TOKEN_FILE and AUTH_KUBERNETES_API_TOKEN_FILE must be separate files"))
		}
		if !id.Stage.empty() {
			errs = append(errs, errors.New("AUTH_STAGE_* settings are not allowed with ENV=prod"))
		}
	case EnvStage:
		errs = append(errs, missing("ENV=stage",
			setting{"AUTH_STAGE_ISSUER", id.Stage.Issuer},
			setting{"AUTH_STAGE_SIGNING_KEY_FILE", id.Stage.SigningKeyFile},
			setting{"AUTH_STAGE_PUBLIC_KEYS_FILE", id.Stage.PublicKeysFile},
		)...)
	default:
		// dev keeps the explicit insecure-dev mode; verify has no dev identity source.
		errs = append(errs, errors.New("AUTH_MODE=verify requires ENV=prod or ENV=stage; use AUTH_MODE=insecure-dev with ENV=dev"))
	}
	return errs
}
