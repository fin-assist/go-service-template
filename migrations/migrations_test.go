package migrations_test

import (
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fin-assist/go-migrate"
	"github.com/fin-assist/go-migrate/migratetest"

	"github.com/fin-assist/go-service-template/internal/testdb"
	"github.com/fin-assist/go-service-template/migrations"
)

// Every migration of the service applies, rolls back and applies again on an empty database.
func TestIntegrationUpDownUp(t *testing.T) {
	src := migrate.Source{Name: "service", Table: "schema_migrations", FS: migrations.FS}
	if err := migrate.Validate(src); err != nil {
		t.Fatal(err)
	}
	if !hasMigrations(t) {
		// A fresh template is a valid empty source, not an unexecuted test.
		// PostgreSQL still validates runner serialization and metadata handling.
		dsn := testdb.DSN(t)
		cfg := migrate.DefaultConfig(dsn)
		cfg.Logger = slog.New(slog.DiscardHandler)
		for range 2 {
			if err := migrate.Up(t.Context(), cfg, []migrate.Source{src}); err != nil {
				t.Fatal(err)
			}
		}
		versions, err := migrate.Versions(t.Context(), cfg, []migrate.Source{src})
		if err != nil || len(versions) != 1 || versions[0].Version != 0 || versions[0].Dirty {
			t.Fatalf("empty source version: %v %v", versions, err)
		}
		if err := migrate.Down(t.Context(), cfg, src, 1); err == nil {
			t.Fatal("empty source cannot roll back SQL")
		}
		if err := migrate.Force(t.Context(), cfg, src, 1); err == nil {
			t.Fatal("empty source cannot fabricate a migration version")
		}
		db, err := pgx.Connect(t.Context(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close(t.Context()) }()
		var businessTables, appliedVersions int
		err = db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename<>'schema_migrations'), (SELECT count(*) FROM schema_migrations)`).Scan(&businessTables, &appliedVersions)
		if err != nil || businessTables != 0 || appliedVersions != 0 {
			t.Fatalf("empty source created application schema: tables=%d versions=%d err=%v", businessTables, appliedVersions, err)
		}
		return
	}
	migratetest.CheckUpDownUp(t, testdb.DSN(t), src)
}

func hasMigrations(t *testing.T) bool {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) > 7 && e.Name()[len(e.Name())-7:] == ".up.sql" {
			return true
		}
	}
	return false
}
