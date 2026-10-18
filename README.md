# go-service-template

Шаблон репозитория Go-сервиса «Понятные финансы» ([FIN-117](https://finassist.youtrack.cloud/issue/FIN-117)). Из него создаются `bank-bff`, `user`, `bank-statement-data`, `analytics`, `assistant`.

Готово:

- миграции и запуск `migrate` — [docs/migrations.md](docs/migrations.md) ([FIN-115](https://finassist.youtrack.cloud/issue/FIN-115)).

Остальное (gRPC-сервер, интерцепторы, менеджер транзакций, конфигурация, наблюдаемость, Dockerfile, `AGENTS.md`) — FIN-117.

## Раскладка

```
cmd/service/                       main: commands (migrate; gRPC server — FIN-117)
internal/adapter/postgres/migrate/ migration runner, sources, timeouts, locks
  migratetest/                     PostgreSQL for tests, up -> down -> up check
migrations/                        SQL migrations of the service, embedded into the binary
docs/migrations.md                 rules for migrations and pgx
```

## Проверки

```bash
# unit and integration tests (PostgreSQL in Docker, or TEST_DATABASE_URL)
go vet ./...
go test ./...
```
