package postgres

import (
	"strings"
	"testing"
)

func TestUnitOpenRefusesInvalidDSNWithoutDisclosingSecrets(t *testing.T) {
	db, err := Open(t.Context(), Options{PrimaryDSN: "postgres://startup:startup-secret@%zz/db"})
	if db != nil || err == nil || strings.Contains(err.Error(), "startup-secret") {
		t.Fatal("invalid startup DSN must fail safely before connecting")
	}
}
