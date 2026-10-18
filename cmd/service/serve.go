package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/fin-assist/go-service-template/internal/adapter/client"
	"github.com/fin-assist/go-service-template/internal/adapter/grpc/server"
	"github.com/fin-assist/go-service-template/internal/adapter/postgres"
	"github.com/fin-assist/go-service-template/internal/config"
	"github.com/fin-assist/go-service-template/internal/logx"
	"github.com/fin-assist/go-service-template/internal/ops"
	"github.com/fin-assist/go-service-template/internal/worker"
)

// app is what wire builds: everything the handlers, jobs and clients need.
type app struct {
	cfg     config.Config
	log     *slog.Logger
	db      *postgres.DB
	grpc    *server.Server
	clients *client.Metrics
	tokens  client.TokenSource
}

// runServe starts the gRPC server, the ops listener and the background jobs, and stops
// them in order on SIGTERM: NOT_SERVING, a pause for balancers, graceful stop, jobs, pools.
func runServe(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(os.Stderr, cfg.Log.Format, cfg.Log.Level, cfg.Service)
	slog.SetDefault(log)
	if cfg.Auth.Mode == config.AuthInsecureDev {
		log.Warn("AUTH_MODE=insecure-dev: identities are not verified, dev only")
	}

	services, users, tokens, err := identity(cfg)
	if err != nil {
		return err
	}
	reg := ops.NewRegistry()
	db, err := postgres.Open(ctx, postgres.Options{
		PrimaryDSN: cfg.DB.PrimaryDSN, ReplicaDSN: cfg.DB.ReplicaDSN,
		MaxConns: cfg.DB.MaxConns, AppName: cfg.Service,
	})
	if err != nil {
		return err
	}
	defer db.Close()

	srv, err := server.New(server.Options{MaxRecvMsgSize: cfg.GRPC.MaxRecvMsgSize, Reflection: cfg.GRPC.Reflection}, server.Deps{
		Logger: log, Metrics: reg, Policy: accessPolicy(),
		Services: services, Users: users, Tombstones: tombstones(db),
	})
	if err != nil {
		return err
	}
	clientMetrics, err := client.NewMetrics(reg)
	if err != nil {
		return err
	}
	a := &app{cfg: cfg, log: log, db: db, grpc: srv, clients: clientMetrics, tokens: tokens}
	jobs, closeAll, err := wire(a)
	if err != nil {
		return err
	}
	defer closeAll()

	grpcLis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.GRPC.Addr)
	if err != nil {
		return err
	}
	readiness := &ops.Readiness{}
	httpSrv := ops.Server(cfg.HTTP.Addr, ops.Handler(reg, readiness))

	runCtx, stopRun := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRun()
	var wg sync.WaitGroup
	errc := make(chan error, 2)
	wg.Go(func() {
		if err := srv.Serve(grpcLis); err != nil {
			errc <- fmt.Errorf("grpc: %w", err)
		}
	})
	wg.Go(func() {
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http: %w", err)
		}
	})
	wg.Go(func() { readiness.Watch(runCtx, 5*time.Second, log, db.Ping, srv.SetServing) })
	wg.Go(func() { worker.Run(runCtx, log, jobs...) })
	log.Info("serving", "version", version, "grpc", cfg.GRPC.Addr, "http", cfg.HTTP.Addr, "env", cfg.Env)

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errc:
		log.Error("listener failed, shutting down", "error", runErr)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownDelay+cfg.ShutdownTimeout)
	defer cancel()
	srv.Shutdown(shutdownCtx, cfg.ShutdownDelay)
	stopRun()
	_ = httpSrv.Shutdown(shutdownCtx)
	wg.Wait()
	return runErr
}

// identity returns the verifiers of incoming calls and the token of outgoing ones.
func identity(cfg config.Config) (server.ServiceVerifier, server.UserVerifier, client.TokenSource, error) {
	switch cfg.Auth.Mode {
	case config.AuthInsecureDev:
		return server.DevServiceVerifier{}, server.DevUserVerifier{}, client.DevToken(cfg.Service), nil
	default:
		// TODO(FIN-119): JWT (Ed25519, GetSigningKeys) and ServiceAccount tokens (JWKS of
		// the API server); the outgoing token from client.FileToken{Path: SERVICE_TOKEN_FILE}.
		// Until then the service does not start outside dev rather than trust anyone.
		return nil, nil, nil, errors.New("AUTH_MODE=verify needs the identity library (FIN-119); use AUTH_MODE=insecure-dev with ENV=dev")
	}
}

// tombstones returns the check of deleted users.
func tombstones(*postgres.DB) server.Tombstones {
	// TODO(FIN-110): the tombstone library on db. A service that stores no user data keeps
	// NoTombstones; every data service must replace it (grpc-api.md 7).
	return server.NoTombstones{}
}
