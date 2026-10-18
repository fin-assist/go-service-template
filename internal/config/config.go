// Package config reads the service configuration from environment variables.
//
// A secret is read from VAR or from the file named by VAR_FILE (systemd LoadCredential, a
// mounted Kubernetes Secret); both set is an error. Nothing here depends on Kubernetes:
// addresses of other services, database DSNs and the token file are plain settings.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Env is the environment the binary runs in.
type Env string

const (
	EnvDev   Env = "dev"   // a developer machine or compose
	EnvStage Env = "stage" // the stand under systemd
	EnvProd  Env = "prod"  // k3s
)

// AuthMode selects how incoming identities are verified.
type AuthMode string

const (
	// AuthVerify checks JWT and ServiceAccount tokens (library of FIN-119).
	AuthVerify AuthMode = "verify"
	// AuthInsecureDev trusts "Bearer dev:<name>" tokens; allowed only with ENV=dev.
	AuthInsecureDev AuthMode = "insecure-dev"
)

// Config is the whole configuration of the serve command.
type Config struct {
	Service string // service name: logs, metrics, application_name, the caller name it presents
	Env     Env
	Log     LogConfig
	GRPC    GRPCConfig
	HTTP    HTTPConfig
	DB      DBConfig
	Auth    AuthConfig
	Peers   map[string]string // other services: name -> gRPC target, from PEER_<NAME>_ADDR
	// ShutdownDelay keeps serving after NOT_SERVING so balancers stop sending new calls.
	ShutdownDelay time.Duration
	// ShutdownTimeout bounds the graceful stop; calls still running after it are cut.
	ShutdownTimeout time.Duration
}

// LogConfig configures slog.
type LogConfig struct {
	Level  slog.Level
	Format string // json or text
}

// GRPCConfig configures the gRPC server.
type GRPCConfig struct {
	Addr           string
	MaxRecvMsgSize int // bytes; SearchOperations needs 16 MiB (grpc-api.md, 9)
	Reflection     bool
}

// HTTPConfig configures the operational HTTP listener: /metrics, /healthz, /readyz.
type HTTPConfig struct {
	Addr string
}

// DBConfig configures the PostgreSQL pools.
type DBConfig struct {
	PrimaryDSN string
	ReplicaDSN string // empty: reads go to the primary pool
	MaxConns   int32
}

// AuthConfig configures identity checks.
type AuthConfig struct {
	Mode AuthMode
	// ServiceTokenFile holds this service's ServiceAccount token, presented on outgoing calls.
	ServiceTokenFile string
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return load(os.Getenv, os.ReadFile, os.Environ) }

// Secret reads one secret by the same rule as Load: VAR or the file named by VAR_FILE.
// It is for commands that need a single setting, such as migrate.
func Secret(name string) (string, error) {
	r := reader{getenv: os.Getenv, readFile: os.ReadFile}
	v := r.secret(name)
	return v, errors.Join(r.errs...)
}

var peerVar = regexp.MustCompile(`^PEER_([A-Z0-9_]+)_ADDR$`)

func load(getenv func(string) string, readFile func(string) ([]byte, error), environ func() []string) (Config, error) {
	r := reader{getenv: getenv, readFile: readFile}
	c := Config{
		Service: r.str("SERVICE_NAME", ""),
		Env:     Env(r.str("ENV", string(EnvProd))),
		Log: LogConfig{
			Level:  r.level("LOG_LEVEL", slog.LevelInfo),
			Format: r.str("LOG_FORMAT", "json"),
		},
		GRPC: GRPCConfig{
			Addr:           r.str("GRPC_ADDR", ":9090"),
			MaxRecvMsgSize: r.int("GRPC_MAX_RECV_MSG_SIZE", 4<<20),
		},
		HTTP: HTTPConfig{Addr: r.str("HTTP_ADDR", ":9091")},
		DB: DBConfig{
			PrimaryDSN: r.secret("DB_PRIMARY_DSN"),
			ReplicaDSN: r.secret("DB_REPLICA_DSN"),
			MaxConns:   int32(r.int("DB_MAX_CONNS", 10)),
		},
		Auth: AuthConfig{
			Mode:             AuthMode(r.str("AUTH_MODE", string(AuthVerify))),
			ServiceTokenFile: r.str("SERVICE_TOKEN_FILE", ""),
		},
		Peers:           map[string]string{},
		ShutdownDelay:   r.duration("SHUTDOWN_DELAY", 5*time.Second),
		ShutdownTimeout: r.duration("SHUTDOWN_TIMEOUT", 20*time.Second),
	}
	c.GRPC.Reflection = c.Env == EnvDev

	for _, kv := range environ() {
		name, value, _ := strings.Cut(kv, "=")
		if m := peerVar.FindStringSubmatch(name); m != nil && value != "" {
			c.Peers[strings.ReplaceAll(strings.ToLower(m[1]), "_", "-")] = value
		}
	}

	if err := errors.Join(append(r.errs, c.validate()...)...); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() []error {
	var errs []error
	if c.Service == "" {
		errs = append(errs, errors.New("SERVICE_NAME is required"))
	}
	if !slices.Contains([]Env{EnvDev, EnvStage, EnvProd}, c.Env) {
		errs = append(errs, fmt.Errorf("ENV=%q, want dev, stage or prod", c.Env))
	}
	if !slices.Contains([]string{"json", "text"}, c.Log.Format) {
		errs = append(errs, fmt.Errorf("LOG_FORMAT=%q, want json or text", c.Log.Format))
	}
	if c.DB.PrimaryDSN == "" {
		errs = append(errs, errors.New("DB_PRIMARY_DSN (or DB_PRIMARY_DSN_FILE) is required"))
	}
	if c.DB.MaxConns < 1 {
		errs = append(errs, errors.New("DB_MAX_CONNS must be positive"))
	}
	if c.GRPC.MaxRecvMsgSize < 1 {
		errs = append(errs, errors.New("GRPC_MAX_RECV_MSG_SIZE must be positive"))
	}
	switch c.Auth.Mode {
	case AuthVerify:
	case AuthInsecureDev:
		if c.Env != EnvDev {
			errs = append(errs, fmt.Errorf("AUTH_MODE=insecure-dev is allowed only with ENV=dev, not %q", c.Env))
		}
	default:
		errs = append(errs, fmt.Errorf("AUTH_MODE=%q, want verify or insecure-dev", c.Auth.Mode))
	}
	return errs
}

type reader struct {
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	errs     []error
}

func (r *reader) str(name, def string) string {
	if v := r.getenv(name); v != "" {
		return v
	}
	return def
}

// secret reads name, or the file named by name_FILE (trailing newline trimmed).
func (r *reader) secret(name string) string {
	v, file := r.getenv(name), r.getenv(name+"_FILE")
	switch {
	case v != "" && file != "":
		r.errs = append(r.errs, fmt.Errorf("both %s and %s_FILE are set", name, name))
		return ""
	case file != "":
		b, err := r.readFile(file)
		if err != nil {
			// The error names the variable and the file, never the content.
			r.errs = append(r.errs, fmt.Errorf("%s_FILE: %w", name, err))
			return ""
		}
		return strings.TrimRight(string(b), "\r\n")
	}
	return v
}

func (r *reader) int(name string, def int) int {
	v := r.getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: not an integer", name, v))
	}
	return n
}

func (r *reader) duration(name string, def time.Duration) time.Duration {
	v := r.getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: not a non-negative duration like 5s", name, v))
	}
	return d
}

func (r *reader) level(name string, def slog.Level) slog.Level {
	v := r.getenv(name)
	if v == "" {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s=%q: want debug, info, warn or error", name, v))
	}
	return l
}
