package main

import (
	"github.com/fin-assist/go-service-template/internal/adapter/grpc/server"
	"github.com/fin-assist/go-service-template/internal/worker"
)

// accessPolicy is the table «method → callers → model» of this service, copied from
// grpc-api.md 3.3 (the source of truth). The server refuses to start while a registered
// method has no rule. For example, in user:
//
//	"/finassist.user.v1.ProfileService/*":                               {Callers: []string{"bank-bff"}, Model: server.ModelUser},
//	"/finassist.user.v1.AuthService/*":                                  {Callers: []string{"bank-bff"}, Model: server.ModelNoUser},
//	"/finassist.user.v1.AuthService/Logout":                             {Callers: []string{"bank-bff"}, Model: server.ModelUser},
//	"/finassist.user.v1.UserInternalService/GetAssistantConsentStatus":  {Callers: []string{"assistant"}, Model: server.ModelMixed},
//	"/finassist.user.v1.UserInternalService/GetSigningKeys":             {Callers: []string{server.AnyCaller}, Model: server.ModelNoUser},
func accessPolicy() server.Policy {
	return server.Policy{}
}

// wire builds repositories, clients, usecases and handlers, registers the handlers on
// a.grpc.GRPC() and returns the background jobs and a function that closes what it
// opened (client connections). The order is adapters → usecases → delivery:
//
//	conn, err := client.Dial(a.cfg.Peers["analytics"], client.Options{
//		Peer: "analytics", Tokens: a.tokens, Metrics: a.clients,
//		Retry: []string{"finassist.analytics.v1.AnalyticsService/GetAnalytics"},
//	})
//	repo := postgres.NewOperations(a.db)                      // internal/adapter/postgres
//	uc := operations.New(a.db, repo, analytics.New(conn))     // internal/operations
//	operationsgrpc.Register(a.grpc.GRPC(), uc)                // internal/adapter/grpc/operations
//	jobs = append(jobs, outbox.Dispatcher(a.db, ...))         // FIN-109
func wire(a *app) (jobs []worker.Job, closeAll func(), err error) {
	return nil, func() {}, nil
}
