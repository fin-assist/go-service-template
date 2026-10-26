package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// standUnit is the settings the example systemd unit of the stand gives the process:
// active Environment= lines with %d resolved to the credentials directory, the
// EnvironmentFile= paths, and the LoadCredential= names that directory holds.
type standUnit struct {
	env         map[string]string
	envFiles    []string
	credentials map[string]bool
}

// A service made from the template replaces standServiceName with its own name; the unit
// file is <service>.service, and the credentials directory follows from it.
const (
	standServiceName = "service"
	standUnitFile    = standServiceName + ".service"
	standCredentials = "/run/credentials/" + standUnitFile
	standSourceDir   = "/etc/finassist/" + standServiceName
	standEnvFile     = standSourceDir + "/env"
)

func readStandUnit(t *testing.T) standUnit {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "deploy", "systemd", standUnitFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	unit := standUnit{env: map[string]string{}, credentials: map[string]bool{}}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		// systemd allows spaces around the key's "=" (LoadCredential = a:b).
		key, value, _ := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Environment":
			// Only the simple form NAME=value is parsed: quotes, several assignments on one line
			// or escapes (Environment="A=1" B=2) would hide the real names from the checks below.
			if strings.ContainsAny(value, "\"' \t\\") {
				t.Fatalf("unit line %q: use the simple form Environment=NAME=value, without quotes, spaces or escapes", line)
			}
			name, setting, found := strings.Cut(value, "=")
			if !found || name == "" {
				t.Fatalf("malformed unit line %q", line)
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
			if filepath.Dir(clean) != standSourceDir || strings.TrimSuffix(base, filepath.Ext(base)) != name {
				t.Fatalf("LoadCredential=%s reads %q, want %s/%s[.ext]", name, source, standSourceDir, name)
			}
			unit.credentials[name] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return unit
}

// FIN-349: the example unit sets ENV=stage and keeps the default AUTH_MODE=verify, so it
// must itself pass SERVICE_NAME and the stand key files; with only the non-secret settings
// its comment names in the env file, the configuration of the stand loads.
func TestUnitStandUnitSatisfiesStageIdentity(t *testing.T) {
	unit := readStandUnit(t)
	if unit.env["ENV"] != "stage" {
		t.Fatalf("unit ENV=%q, want stage", unit.env["ENV"])
	}
	// SERVICE_NAME is the kid/sub of the stand signing key, fixed by the unit itself.
	if unit.env["SERVICE_NAME"] != standServiceName {
		t.Fatalf("unit SERVICE_NAME=%q, want %s", unit.env["SERVICE_NAME"], standServiceName)
	}
	for _, name := range []string{"AUTH_STAGE_SIGNING_KEY_FILE", "AUTH_STAGE_PUBLIC_KEYS_FILE", "DB_PRIMARY_DSN_FILE"} {
		if unit.env[name] == "" {
			t.Fatalf("unit does not set %s", name)
		}
	}
	// The shared JWKS of the stand is one world-readable file read directly, not a credential.
	if got := unit.env["AUTH_STAGE_PUBLIC_KEYS_FILE"]; got != "/etc/finassist/stage-public.jwks" {
		t.Fatalf("unit AUTH_STAGE_PUBLIC_KEYS_FILE=%q, want the shared /etc/finassist/stage-public.jwks", got)
	}
	// Every secret file comes through LoadCredential: under DynamicUser a 0600 root file named
	// directly would not be readable, and readFile below would not notice that.
	for _, v := range Vars {
		if !v.Secret {
			continue
		}
		// A secret value in Environment= shows up in "systemctl show" and /proc/<pid>/environ.
		if _, ok := unit.env[v.Name]; ok {
			t.Fatalf("unit sets secret %s in Environment=, not through LoadCredential", v.Name)
		}
		if file, ok := unit.env[v.Name+"_FILE"]; ok && !strings.HasPrefix(file, standCredentials+"/") {
			t.Fatalf("unit reads secret %s_FILE=%q directly, not through LoadCredential", v.Name, file)
		}
	}
	used := map[string]bool{}
	for name, value := range unit.env {
		if credential, ok := strings.CutPrefix(value, standCredentials+"/"); ok {
			if !unit.credentials[credential] {
				t.Fatalf("%s names credential %q that the unit does not load", name, credential)
			}
			used[credential] = true
		}
	}
	// And back: a loaded credential no variable reads is a leftover or a typo in the other half.
	for credential := range unit.credentials {
		if !used[credential] {
			t.Fatalf("LoadCredential=%s is not read by any Environment= variable", credential)
		}
	}
	// The env file of the stand: only non-secret settings named in the unit comment. It is
	// the content of standEnvFile and reaches the process only through that EnvironmentFile=;
	// a missing directive or the env file of another service is refused.
	envFiles := map[string]map[string]string{standEnvFile: {
		"AUTH_STAGE_ISSUER": "stage-services", "AUTH_USER_ISSUER": "https://user.example", "AUTH_USER_AUDIENCE": "finassist", "PEER_USER_ADDR": "dns:///user:9090",
	}}
	if len(unit.envFiles) != 1 || unit.envFiles[0] != standEnvFile {
		t.Fatalf("unit EnvironmentFile=%q, want exactly %s", unit.envFiles, standEnvFile)
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
				t.Fatalf("the env file would override the unit setting %s", name)
			}
			vars[name] = value
		}
	}
	// Only the declared credentials are readable: a direct path to a root-owned source file is
	// unreadable under DynamicUser, so the fake refuses every other path. The secret files
	// config reads are DSNs.
	readFile := func(path string) ([]byte, error) {
		if name, ok := strings.CutPrefix(path, standCredentials+"/"); ok && unit.credentials[name] {
			return []byte("postgres://" + name + "/" + standServiceName), nil
		}
		return noFiles(path)
	}
	get, list := env(vars)
	c, err := load(get, readFile, list)
	if err != nil {
		t.Fatalf("stand unit configuration rejected: %v", err)
	}
	if c.Env != EnvStage || c.Auth.Mode != AuthVerify || c.Service != standServiceName {
		t.Fatalf("stand runs ENV=%s AUTH_MODE=%s SERVICE_NAME=%s, want stage, verify and %s", c.Env, c.Auth.Mode, c.Service, standServiceName)
	}
	// The fake puts the credential name into the content: the DSN comes from db-primary-dsn.
	if want := "postgres://db-primary-dsn/" + standServiceName; c.DB.PrimaryDSN != want {
		t.Fatalf("DB_PRIMARY_DSN comes from %q, want the credential db-primary-dsn", c.DB.PrimaryDSN)
	}
	stage := c.Auth.Identity.Stage
	if stage.SigningKeyFile != standCredentials+"/stage-signing-key" || stage.PublicKeysFile != "/etc/finassist/stage-public.jwks" ||
		stage.Issuer != "stage-services" {
		t.Fatalf("stage identity: %+v", stage)
	}
}
