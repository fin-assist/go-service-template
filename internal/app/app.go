// Package app assembles the service: identity, database pools, the gRPC server with its
// handlers, clients of other services and background jobs. cmd/service runs it with real
// listeners; apitest runs the same assembly in-process for API tests, so what the tests
// call is what production serves.
package app

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/fin-assist/go-grpc/client"
	"github.com/fin-assist/go-grpc/server"
	"github.com/fin-assist/go-worker"

	"github.com/fin-assist/go-service-template/internal/adapter/postgres"
	"github.com/fin-assist/go-service-template/internal/config"
)

// App is the assembled service. Wire reads its fields to build adapters and usecases.
type App struct {
	Cfg     config.Config
	Log     *slog.Logger
	DB      *postgres.DB
	GRPC    *server.Server
	Clients *client.Metrics
	Tokens  client.TokenSource
	Jobs    []worker.Job

	closers []func()
}

// New assembles the service from cfg. Close releases what it opened.
func New(ctx context.Context, cfg config.Config, log *slog.Logger, reg prometheus.Registerer) (*App, error) {
	return assemble(ctx, cfg, log, reg, postgres.Open)
}

func assemble(ctx context.Context, cfg config.Config, log *slog.Logger, reg prometheus.Registerer, openDB func(context.Context, postgres.Options) (*postgres.DB, error)) (*App, error) {
	// Client metrics come first: the identity's own connection to user is counted too.
	clients, err := client.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	id, err := identity(cfg, reg, clients, nil)
	if err != nil {
		return nil, err
	}
	db, err := openDB(ctx, postgres.Options{
		PrimaryDSN: cfg.DB.PrimaryDSN, ReplicaDSN: cfg.DB.ReplicaDSN,
		MaxConns: cfg.DB.MaxConns, AppName: cfg.Service,
	})
	if err != nil {
		id.close()
		return nil, err
	}
	// Close runs after the jobs have stopped (cmd/service), so the identity key
	// refresh job never outlives its connection to user.
	a := &App{Cfg: cfg, Log: log, DB: db, Clients: clients, Tokens: id.tokens, Jobs: id.jobs, closers: []func(){id.close, db.Close}}
	fail := func(err error) (*App, error) { a.Close(); return nil, err }

	if a.GRPC, err = server.New(server.Options{MaxRecvMsgSize: cfg.GRPC.MaxRecvMsgSize, Reflection: cfg.GRPC.Reflection}, server.Deps{
		Logger: log, Metrics: reg, Policy: AccessPolicy(),
		Services: id.services, Users: id.users, Tombstones: tombstones(db),
	}); err != nil {
		return fail(err)
	}
	jobs, closeWired, err := wire(a)
	if err != nil {
		return fail(err)
	}
	a.Jobs = append(a.Jobs, jobs...)
	a.closers = append(a.closers, closeWired)
	return a, nil
}

// Close releases what New opened, in reverse order.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// tombstones returns the check of deleted users.
func tombstones(*postgres.DB) server.Tombstones {
	// TODO(FIN-110): the tombstone library on db. A service that stores no user data keeps
	// NoTombstones; every data service must replace it (grpc-api.md 7).
	return server.NoTombstones{}
}
