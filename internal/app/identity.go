package app

import (
	"errors"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"github.com/fin-assist/go-grpc/client"
	"github.com/fin-assist/go-grpc/server"
	identitylib "github.com/fin-assist/go-identity"
	"github.com/fin-assist/go-worker"

	"github.com/fin-assist/go-service-template/internal/config"
)

// identityKeysJob is the go-worker job that refreshes the user and service key sets.
const identityKeysJob = "identity-keys"

// getSigningKeys is the only method retried on the connection to user that the
// identity assembly owns.
const getSigningKeys = "finassist.user.v1.UserInternalService/GetSigningKeys"

// identityParts are the verifiers of incoming calls, the token of outgoing ones, the
// jobs that keep the key sets fresh and the release of what the identity opened.
type identityParts struct {
	services server.ServiceVerifier
	users    server.UserVerifier
	tokens   client.TokenSource
	jobs     []worker.Job
	close    func()
}

// identity builds the identity of cfg.Auth.Mode. verify uses go-identity NewAssembly:
// Kubernetes ServiceAccount tokens with ENV=prod, file-key JWTs with ENV=stage and
// user keys from user.GetSigningKeys in both; any failure refuses the start, with no
// fallback to a weaker mode. insecure-dev trusts dev tokens and only with ENV=dev.
// clients counts the calls to user like every other outgoing call; userDial adds dial options to the user connection (bufconn in tests); nil in production.
func identity(cfg config.Config, reg prometheus.Registerer, clients *client.Metrics, userDial []grpc.DialOption) (identityParts, error) {
	switch cfg.Auth.Mode {
	case config.AuthInsecureDev:
		// New is exported and callable without config.Load, so the dev-only gate is
		// enforced here as well: unverified identities never outside ENV=dev.
		if cfg.Env != config.EnvDev {
			return identityParts{}, errors.New("AUTH_MODE=insecure-dev is allowed only with ENV=dev")
		}
		return identityParts{
			services: server.DevServiceVerifier{}, users: server.DevUserVerifier{},
			tokens: client.DevToken(cfg.Service), close: func() {},
		}, nil
	case config.AuthVerify:
	default:
		return identityParts{}, errors.New("unsupported AUTH_MODE")
	}
	// config.Load already refuses these; New may be called without it.
	if cfg.Peers["user"] == "" {
		return identityParts{}, errors.New("identity: PEER_USER_ADDR is required with AUTH_MODE=verify")
	}
	id := cfg.Auth.Identity
	opts := identitylib.AssemblyOptions{
		Env: string(cfg.Env), Service: cfg.Service,
		UserIssuer: id.UserIssuer, UserAudience: id.UserAudience,
		DialUser: func(tokens identitylib.TokenSource) (*grpc.ClientConn, error) {
			return client.Dial(cfg.Peers["user"], client.Options{
				Peer: "user", Tokens: tokens, Metrics: clients, DialOptions: userDial,
				Retry: []string{getSigningKeys},
			})
		},
		Clock: time.Now, Registerer: reg,
	}
	switch cfg.Env {
	case config.EnvProd:
		// NewAssembly compares the two token paths lexically; a symlink of a projected
		// volume (…/serviceaccount/..data/token) or a relative path would pass it and
		// send the API-server token to every peer. Refuse the same file under any name.
		if sameFile(cfg.Auth.ServiceTokenFile, kubernetesAPITokenFile(id.Kubernetes.APITokenFile)) {
			return identityParts{}, errors.New("identity: SERVICE_TOKEN_FILE and AUTH_KUBERNETES_API_TOKEN_FILE must be separate files")
		}
		opts.Kubernetes = identitylib.KubernetesOptions{
			TokenFile: cfg.Auth.ServiceTokenFile, Issuer: id.Kubernetes.Issuer, JWKSURL: id.Kubernetes.JWKSURL,
			CAFile: id.Kubernetes.CAFile, APITokenFile: id.Kubernetes.APITokenFile, Namespace: id.Kubernetes.Namespace,
		}
		// Stage files given in prod are passed on so that NewAssembly refuses them.
		opts.Stage = stageFiles(id.Stage)
	case config.EnvStage:
		opts.Stage = stageFiles(id.Stage)
	}
	assembly, err := identitylib.NewAssembly(opts)
	if err != nil {
		// NewAssembly errors carry no paths, tokens or key material.
		return identityParts{}, err
	}
	return identityParts{
		services: assembly, users: assembly, tokens: assembly,
		jobs:  []worker.Job{{Name: identityKeysJob, Run: assembly.Run}},
		close: func() { _ = assembly.Close() },
	}, nil
}

// defaultKubernetesAPITokenFile is the default of go-identity KubernetesOptions.APITokenFile
// and of AUTH_KUBERNETES_API_TOKEN_FILE; NewAssembly applies it to an empty value.
const defaultKubernetesAPITokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" //nolint:gosec // A file path, not a credential.

func kubernetesAPITokenFile(path string) string {
	if path == "" {
		return defaultKubernetesAPITokenFile
	}
	return path
}

// sameFile reports whether both paths name one existing file. A missing file is not
// the same file: the outgoing token then fails closed on its first use.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

func stageFiles(s config.StageIdentity) identitylib.StageKeyFiles {
	return identitylib.StageKeyFiles{Issuer: s.Issuer, SigningKeyFile: s.SigningKeyFile, PublicKeysFile: s.PublicKeysFile}
}
