package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service made from the template replaces standServiceName with its own name; the unit
// file is <service>.service, and the credentials directory follows from it.
const (
	standServiceName = "service"
	standUnitFile    = standServiceName + ".service"
	standCredentials = "/run/credentials/" + standUnitFile
	standSourceDir   = "/etc/finassist/" + standServiceName
	standEnvFile     = standSourceDir + "/env"
	standPublicJWKS  = "/etc/finassist/stage-public.jwks"
)

// standUnit is the settings the example systemd unit of the stand gives the process:
// active Environment= lines with %d resolved to the credentials directory, the
// EnvironmentFile= paths, and the LoadCredential= names that directory holds.
type standUnit struct {
	env         map[string]string
	envFiles    []string
	credentials map[string]bool
}

// parseStandUnit reads the directives of the [Service] section that shape the environment.
func parseStandUnit(text string) (standUnit, error) {
	unit := standUnit{env: map[string]string{}, credentials: map[string]bool{}}
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		// systemd allows spaces around the key's "=" (LoadCredential = a:b).
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Environment", "EnvironmentFile", "LoadCredential", "SetCredential", "SetCredentialEncrypted":
			// systemd ignores these keys outside [Service]: the process would not get them.
			if section != "[Service]" {
				return unit, fmt.Errorf("unit line %q is in %s, want [Service]", line, section)
			}
		}
		switch key {
		case "Environment":
			// Only the simple form NAME=value is parsed: quotes, several assignments on one line
			// or escapes (Environment="A=1" B=2) would hide the real names from the checks below.
			if strings.ContainsAny(value, "\"' \t\\") {
				return unit, fmt.Errorf("unit line %q: use the simple form Environment=NAME=value, without quotes, spaces or escapes", line)
			}
			name, setting, found := strings.Cut(value, "=")
			if !found || name == "" {
				return unit, fmt.Errorf("malformed unit line %q", line)
			}
			unit.env[name] = strings.ReplaceAll(setting, "%d", standCredentials)
		case "EnvironmentFile":
			unit.envFiles = append(unit.envFiles, value)
		case "LoadCredential":
			name, source, _ := strings.Cut(value, ":")
			// Each source is this service's file named after its credential (an extension is
			// allowed): a path left from the template, copied from another unit or with another
			// name does not exist on the stand or holds a foreign key.
			clean := filepath.Clean(source)
			base := filepath.Base(clean)
			if filepath.Dir(clean) != standSourceDir || (base != name && strings.TrimSuffix(base, filepath.Ext(base)) != name) {
				return unit, fmt.Errorf("LoadCredential=%s reads %q, want %s/%s[.ext]", name, source, standSourceDir, name)
			}
			unit.credentials[name] = true
		case "SetCredential", "SetCredentialEncrypted":
			return unit, fmt.Errorf("unit line %q: secrets come from files through LoadCredential=, not from the unit text", line)
		}
	}
	return unit, scanner.Err()
}

// checkStandUnit tells why the unit text would not start the service on the stand, or nil.
func checkStandUnit(text string) error {
	unit, err := parseStandUnit(text)
	if err != nil {
		return err
	}
	if unit.env["ENV"] != "stage" {
		return fmt.Errorf("unit ENV=%q, want stage", unit.env["ENV"])
	}
	// SERVICE_NAME is the kid/sub of the stand signing key, fixed by the unit itself.
	if unit.env["SERVICE_NAME"] != standServiceName {
		return fmt.Errorf("unit SERVICE_NAME=%q, want %s", unit.env["SERVICE_NAME"], standServiceName)
	}
	for _, name := range []string{"AUTH_STAGE_SIGNING_KEY_FILE", "AUTH_STAGE_PUBLIC_KEYS_FILE", "DB_PRIMARY_DSN_FILE"} {
		if unit.env[name] == "" {
			return fmt.Errorf("unit does not set %s", name)
		}
	}
	// The shared JWKS of the stand is one world-readable file read directly, not a credential.
	if got := unit.env["AUTH_STAGE_PUBLIC_KEYS_FILE"]; got != standPublicJWKS {
		return fmt.Errorf("unit AUTH_STAGE_PUBLIC_KEYS_FILE=%q, want the shared %s", got, standPublicJWKS)
	}
	plain, secret := map[string]bool{}, map[string]bool{}
	for _, v := range Vars {
		if v.Secret {
			secret[v.Name] = true
		} else {
			plain[v.Name] = true
		}
	}
	used := map[string]bool{}
	for name, value := range unit.env {
		switch base, isFile := strings.CutSuffix(name, "_FILE"); {
		case secret[name]:
			// A secret value in Environment= shows up in "systemctl show" and /proc/<pid>/environ.
			return fmt.Errorf("unit sets secret %s in Environment=, not through LoadCredential", name)
		case isFile && secret[base]:
			// Under DynamicUser a 0600 root file named directly would not be readable.
			if !strings.HasPrefix(value, standCredentials+"/") {
				return fmt.Errorf("unit reads secret %s=%q directly, not through LoadCredential", name, value)
			}
		case plain[name] || peerVar.MatchString(name):
		default:
			// Anything else (PGPASSWORD, a typo) is not configuration of the service.
			return fmt.Errorf("unit sets %s, which is not a variable of internal/config", name)
		}
		if credential, ok := strings.CutPrefix(value, standCredentials+"/"); ok {
			if !unit.credentials[credential] {
				return fmt.Errorf("%s names credential %q that the unit does not load", name, credential)
			}
			used[credential] = true
		}
	}
	// And back: a loaded credential no variable reads is a leftover or a typo in the other half.
	for credential := range unit.credentials {
		if !used[credential] {
			return fmt.Errorf("LoadCredential=%s is not read by any Environment= variable", credential)
		}
	}
	// The env file of the stand: only non-secret settings named in the unit comment. It is
	// the content of standEnvFile and reaches the process only through that EnvironmentFile=;
	// a missing directive or the env file of another service is refused.
	envFiles := map[string]map[string]string{standEnvFile: {
		"AUTH_STAGE_ISSUER": "stage-services", "AUTH_USER_ISSUER": "https://user.example", "AUTH_USER_AUDIENCE": "finassist", "PEER_USER_ADDR": "dns:///user:9090",
	}}
	if len(unit.envFiles) != 1 || unit.envFiles[0] != standEnvFile {
		return fmt.Errorf("unit EnvironmentFile=%q, want exactly %s", unit.envFiles, standEnvFile)
	}
	// Merge in systemd order: Environment= of the unit first, EnvironmentFile= over it.
	// An env file that repeated a unit setting would silently replace it, so refuse that.
	vars := map[string]string{}
	for name, value := range unit.env {
		vars[name] = value
	}
	for _, path := range unit.envFiles {
		for name, value := range envFiles[path] {
			if _, ok := unit.env[name]; ok {
				return fmt.Errorf("the env file would override the unit setting %s", name)
			}
			vars[name] = value
		}
	}
	// Only the declared credentials are readable: a direct path to a root-owned source file is
	// unreadable under DynamicUser, so the fake refuses every other path. The secret files
	// config reads are DSNs; the content names the credential it came from.
	readFile := func(path string) ([]byte, error) {
		if name, ok := strings.CutPrefix(path, standCredentials+"/"); ok && unit.credentials[name] {
			return []byte("postgres://" + name + "/" + standServiceName), nil
		}
		return noFiles(path)
	}
	get, list := env(vars)
	c, err := load(get, readFile, list)
	if err != nil {
		return fmt.Errorf("stand unit configuration rejected: %w", err)
	}
	if c.Env != EnvStage || c.Auth.Mode != AuthVerify || c.Service != standServiceName {
		return fmt.Errorf("stand runs ENV=%s AUTH_MODE=%s SERVICE_NAME=%s, want stage, verify and %s", c.Env, c.Auth.Mode, c.Service, standServiceName)
	}
	if want := "postgres://db-primary-dsn/" + standServiceName; c.DB.PrimaryDSN != want {
		return fmt.Errorf("DB_PRIMARY_DSN comes from %q, want the credential db-primary-dsn", c.DB.PrimaryDSN)
	}
	stage := c.Auth.Identity.Stage
	if stage.SigningKeyFile != unit.env["AUTH_STAGE_SIGNING_KEY_FILE"] || stage.PublicKeysFile != standPublicJWKS || stage.Issuer != "stage-services" {
		return errors.New("stage identity does not come from the unit and its env file")
	}
	return nil
}

func readStandUnitText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "systemd", standUnitFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// unitEdit replaces the first occurrence of old in the unit text.
type unitEdit struct{ old, new string }

func editStandUnit(t *testing.T, text string, edits []unitEdit) string {
	t.Helper()
	for _, e := range edits {
		if !strings.Contains(text, e.old) {
			t.Fatalf("the unit has no %q to edit", e.old)
		}
		text = strings.Replace(text, e.old, e.new, 1)
	}
	return text
}

// add puts a line into [Service] right after ENV=stage.
func add(line string) unitEdit {
	return unitEdit{"Environment=ENV=stage\n", "Environment=ENV=stage\n" + line + "\n"}
}

// FIN-349: the example unit sets ENV=stage and keeps the default AUTH_MODE=verify, so it
// must itself pass SERVICE_NAME and the stand key files; with only the non-secret settings
// its comment names in the env file, the configuration of the stand loads.
func TestUnitStandUnitSatisfiesStageIdentity(t *testing.T) {
	if err := checkStandUnit(readStandUnitText(t)); err != nil {
		t.Fatal(err)
	}
}

func TestUnitStandUnitRejectsMismatches(t *testing.T) {
	const dsn = "LoadCredential=db-primary-dsn:/etc/finassist/service/db-primary-dsn"
	const dsnFile = "Environment=DB_PRIMARY_DSN_FILE=%d/db-primary-dsn"
	tests := []struct {
		name  string
		edits []unitEdit
		want  string
	}{
		{"quoted assignment", []unitEdit{add(`Environment="DB_REPLICA_DSN=postgres://app:pw@replica/db"`)}, "use the simple form"},
		{"several assignments", []unitEdit{add("Environment=LOG_LEVEL=info DB_REPLICA_DSN=postgres://app:pw@replica/db")}, "use the simple form"},
		{"tab between assignments", []unitEdit{add("Environment=LOG_LEVEL=info\tDB_REPLICA_DSN=x")}, "use the simple form"},
		{"escape", []unitEdit{{"Environment=SERVICE_NAME=service", `Environment=SERVICE_NAME=serv\ice`}}, "use the simple form"},
		{"secret value", []unitEdit{add("Environment=DB_REPLICA_DSN=postgres://app:pw@replica/db")}, "sets secret DB_REPLICA_DSN"},
		{"secret value with spaced key", []unitEdit{add("Environment = DB_REPLICA_DSN=postgres://app:pw@replica/db")}, "sets secret DB_REPLICA_DSN"},
		{"primary DSN value", []unitEdit{add("Environment=DB_PRIMARY_DSN=postgres://app:pw@primary/db")}, "sets secret DB_PRIMARY_DSN"},
		{"unknown variable", []unitEdit{add("Environment=PGPASSWORD=pw")}, "PGPASSWORD, which is not a variable"},
		{"SetCredential", []unitEdit{add("SetCredential=db-replica-dsn:postgres://app:pw@replica/db")}, "not from the unit text"},
		{"direct DSN path", []unitEdit{{dsn + "\n", ""}, {dsnFile, "Environment=DB_PRIMARY_DSN_FILE=/etc/finassist/service/db-primary-dsn"}}, "directly, not through LoadCredential"},
		{"DSN credential not loaded", []unitEdit{{dsn + "\n", ""}}, `credential "db-primary-dsn" that the unit does not load`},
		{"DSN from the signing key", []unitEdit{{dsn + "\n", ""}, {dsnFile, "Environment=DB_PRIMARY_DSN_FILE=%d/stage-signing-key"}}, "want the credential db-primary-dsn"},
		{"DSN file names the signing key", []unitEdit{{dsnFile, "Environment=DB_PRIMARY_DSN_FILE=%d/stage-signing-key"}}, "LoadCredential=db-primary-dsn is not read"},
		{"source of another service", []unitEdit{{"stage-signing-key:/etc/finassist/service/", "stage-signing-key:/etc/finassist/other/"}}, "LoadCredential=stage-signing-key reads"},
		{"source with another name", []unitEdit{{dsn, "LoadCredential=db-primary-dsn:/etc/finassist/service/missing-dsn"}}, "LoadCredential=db-primary-dsn reads"},
		{"source escaping the directory", []unitEdit{{dsn, "LoadCredential=db-primary-dsn:/etc/finassist/service/../other/db-primary-dsn"}}, "LoadCredential=db-primary-dsn reads"},
		{"credential nobody reads", []unitEdit{add("LoadCredential=jwks:/etc/finassist/service/jwks")}, "LoadCredential=jwks is not read"},
		{"JWKS through LoadCredential", []unitEdit{add("LoadCredential = jwks:/etc/finassist/stage-public.jwks")}, "LoadCredential=jwks reads"},
		{"JWKS old path", []unitEdit{{"stage-public.jwks\n", "stage-keys.json\n"}}, "want the shared"},
		{"signing key not loaded", []unitEdit{{"LoadCredential=stage-signing-key:", "#LoadCredential=stage-signing-key:"}}, `credential "stage-signing-key" that the unit does not load`},
		{"no EnvironmentFile", []unitEdit{{"EnvironmentFile=/etc/finassist/service/env\n", ""}}, "want exactly /etc/finassist/service/env"},
		{"two EnvironmentFile", []unitEdit{add("EnvironmentFile=/etc/finassist/service/env.extra")}, "want exactly /etc/finassist/service/env"},
		{"EnvironmentFile of another service", []unitEdit{{"EnvironmentFile=/etc/finassist/service/env", "EnvironmentFile=/etc/finassist/analytics/env"}}, "want exactly /etc/finassist/service/env"},
		{"env file overrides the unit", []unitEdit{add("Environment=AUTH_STAGE_ISSUER=x")}, "would override the unit setting AUTH_STAGE_ISSUER"},
		{"wrong SERVICE_NAME", []unitEdit{{"Environment=SERVICE_NAME=service", "Environment=SERVICE_NAME=other"}}, `SERVICE_NAME="other"`},
		{"ENV not stage", []unitEdit{{"Environment=ENV=stage\n", "Environment=ENV=prod\n"}}, `ENV="prod"`},
		{"directive outside [Service]", []unitEdit{{"Environment=SERVICE_NAME=service\n", ""}, {"[Unit]\n", "[Unit]\nEnvironment=SERVICE_NAME=service\n"}}, "is in [Unit], want [Service]"},
	}
	text := readStandUnitText(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkStandUnit(editStandUnit(t, text, tt.edits))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("checkStandUnit = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestUnitStandUnitAcceptsEquivalentForms(t *testing.T) {
	tests := []struct {
		name  string
		edits []unitEdit
	}{
		{"spaces around the key", []unitEdit{
			{"LoadCredential=db-primary-dsn:", "LoadCredential = db-primary-dsn:"},
			{"Environment=SERVICE_NAME=", "Environment =SERVICE_NAME="},
		}},
		{"source normalised by Clean", []unitEdit{{"db-primary-dsn:/etc/finassist/service/db-primary-dsn", "db-primary-dsn:/etc/finassist/service/./db-primary-dsn"}}},
		{"credential named exactly as its file", []unitEdit{
			{"LoadCredential=stage-signing-key:", "LoadCredential=stage-signing-key.pem:"},
			{"AUTH_STAGE_SIGNING_KEY_FILE=%d/stage-signing-key", "AUTH_STAGE_SIGNING_KEY_FILE=%d/stage-signing-key.pem"},
		}},
		{"replica DSN credential", []unitEdit{
			add("LoadCredential=db-replica-dsn:/etc/finassist/service/db-replica-dsn"),
			add("Environment=DB_REPLICA_DSN_FILE=%d/db-replica-dsn"),
		}},
		{"peer address", []unitEdit{add("Environment=PEER_ANALYTICS_ADDR=dns:///127.0.0.1:9093")}},
	}
	text := readStandUnitText(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkStandUnit(editStandUnit(t, text, tt.edits)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
