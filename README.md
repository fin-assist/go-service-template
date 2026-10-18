# go-service-template

Шаблон репозитория Go-сервиса «Понятные финансы» ([FIN-117](https://finassist.youtrack.cloud/issue/FIN-117)). Из него создаются `bank-bff`, `user`, `bank-statement-data`, `analytics`, `assistant`. Контракты (`.proto`, сгенерированный код, реестр ошибок `finerr`) — в [`fin-assist/contracts`](https://github.com/fin-assist/contracts), модуль `github.com/fin-assist/contracts/gen/go`.

- [docs/server.md](docs/server.md) — gRPC-сервер и интерцепторы, идентичность, таблица доступа, клиенты, транзакции, логи и метрики.
- [docs/config.md](docs/config.md) — переменные окружения.
- [docs/migrations.md](docs/migrations.md) — миграции и запросы на pgx ([FIN-115](https://finassist.youtrack.cloud/issue/FIN-115)).
- [AGENTS.md](AGENTS.md) — карта и правила для агентов, Code Review Rules.

## Новый сервис из шаблона

1. Создать репозиторий из шаблона (GitHub → «Use this template»).
2. Заменить модуль: `go mod edit -module github.com/fin-assist/<service>` и пути импорта (`grep -rl go-service-template | xargs sed -i 's#fin-assist/go-service-template#fin-assist/<service>#g'`).
3. Переименовать `cmd/service` → `cmd/<service>`, `deploy/systemd/service.service`, имя в `compose.yaml`, `Makefile`, `Dockerfile`.
4. Заполнить `accessPolicy()` строками своего сервиса из таблицы grpc-api.md 3.3 и зарегистрировать обработчики в `wire()` (`cmd/service/wire.go`).
5. Сервис с данными пользователей — заменить `NoTombstones` на библиотеку FIN-110.
6. Секреты репозитория: `CLAUDE_CODE_OAUTH_TOKEN` (ревью `@claude`), `GO_MODULES_TOKEN`, если подключаются приватные модули fin-assist. Установить Claude GitHub App и Codex.
7. Обновить README и `AGENTS.md` под сервис.

## Раскладка

```
cmd/service/                       main: serve and migrate commands, wiring (wire.go), access table
internal/domain/                   entities and invariants, no infrastructure imports
internal/<feature>/                usecases; interfaces of repositories and clients they need
internal/adapter/grpc/server/      gRPC server, interceptors, access table types, identity interfaces
internal/adapter/grpc/<feature>/   gRPC handlers: messages <-> usecases
internal/adapter/postgres/         pools, transaction manager, repositories on pgx
internal/adapter/postgres/migrate/ migration runner (migratetest: PostgreSQL for tests)
internal/adapter/client/           clients of other services, service token source
internal/worker/                   background jobs: outbox dispatcher, scheduled jobs
internal/config/                   configuration from the environment
internal/reqctx/                   request-scoped values: request ID, caller, user, hints
internal/logx/                     slog with values from the context
internal/ops/                      /metrics, /healthz, /readyz, readiness
migrations/                        SQL migrations, embedded into the binary
deploy/systemd/                    example unit for the stand
```

Раскладка — по руководству go.dev «Organizing a Go module» (серверный проект), слои и интерфейсы на стороне потребителя — по go-clean-arch v4: сценарий в `internal/<feature>` объявляет интерфейсы нужных ему репозиториев и клиентов, адаптеры их реализуют, связывает всё `wire()`.

## Запуск

```bash
# PostgreSQL 18, migrations and the service in Docker (dev mode, docs/config.md)
make up
# the same service on the host against the compose PostgreSQL
make run
```

- **Образ** — `Dockerfile`: статический бинарь на distroless, пользователь `nonroot`. Один бинарь: `serve` в Deployment, `migrate up` в Job. Публикация в GHCR — FIN-148.
- **Стенд** (FIN-149) — тот же бинарь под systemd: пример unit-файла с секретами через `LoadCredential` — [deploy/systemd/service.service](deploy/systemd/service.service). Скрипт деплоя запускает `migrate up` перед перезапуском unit.
- **k3s** — манифесты в `fin-assist/infra` (FIN-147): ServiceAccount, projected token, headless Service, пробы `grpc`.

## Проверки

CI (`.github/workflows/ci.yml`):

| Job | Что |
|---|---|
| `go` | `go vet`, `go test -race` с PostgreSQL 18 в Docker (testcontainers), включая «вверх → вниз → вверх» для миграций |
| `lint` | golangci-lint (`.golangci.yml`), в том числе `forbidigo` против обхода `finerr` |
| `image` | сборка образа и запуск бинаря в нём |
| `migrations` (PR) | существующие миграции не изменены, squawk по новым |

Локально:

```bash
make test   # Docker for testcontainers, or TEST_DATABASE_URL=postgres://... to use your PostgreSQL
make lint
```

Модули `github.com/fin-assist/*` берутся из git, минуя прокси (`GOPRIVATE`, задан в `Makefile` и CI). Для приватного — токен с чтением репозиториев fin-assist: локально `git config --global url."https://x-access-token:<token>@github.com/fin-assist/".insteadOf "https://github.com/fin-assist/"`, в CI — секрет `GO_MODULES_TOKEN`, в `docker build` — секрет `netrc`.
