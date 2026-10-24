# go-service-template

Выпуск версии сервиса и повторная публикация GitHub Release описаны в [docs/releasing.md](docs/releasing.md). История изменений — [CHANGELOG.md](CHANGELOG.md). Новый сервис начинает с пустого `Unreleased`; версии и теги создаёт владелец после PR и post-merge CI. Публикация образа и deployment — отдельные этапы.

Шаблон репозитория Go-сервиса «Понятные финансы» ([FIN-117](https://finassist.youtrack.cloud/issue/FIN-117)). Из него создаются `bank-bff`, `user`, `bank-statement-data`, `analytics`, `assistant`. Контракты (`.proto`, сгенерированный код, реестр ошибок `finerr`) — в [`fin-assist/contracts`](https://github.com/fin-assist/contracts), модуль `github.com/fin-assist/contracts/gen/go`.

- [docs/server.md](docs/server.md) — gRPC-сервер и интерцепторы, идентичность, таблица доступа, клиенты, транзакции, логи и метрики.
- [docs/config.md](docs/config.md) — переменные окружения.
- [docs/migrations.md](docs/migrations.md) — миграции и запросы на pgx ([FIN-115](https://finassist.youtrack.cloud/issue/FIN-115)).
- [AGENTS.md](AGENTS.md) — карта и правила для агентов, Code Review Rules.

## Автоматизация обновлений зависимостей

[Политика Renovate](docs/renovate-policy.md) (FIN-211): одна актуальная release/main, автомерж полностью ботских PR после обязательного CI, один повтор красного CI и закрытие с явным повтором через Dependency Dashboard. AndroidX/Compose остаются на ручной проверке. Исходные группировки, ограничения версий и расписания — `renovate-base.json`; generated target и quarantine — ветка `renovate-policy`. Активация требует отдельного токена и строгой защиты веток; по умолчанию выключена.

## Новый сервис из шаблона

1. Создать репозиторий из шаблона (GitHub → «Use this template»).
2. Заменить модуль: `go mod edit -module github.com/fin-assist/<service>` и пути импорта (`grep -rl go-service-template | xargs sed -i 's#fin-assist/go-service-template#fin-assist/<service>#g'`).
3. Переименовать `cmd/service` → `cmd/<service>`, `deploy/systemd/service.service`, имя в `compose.yaml`, `Makefile`, `Dockerfile`.
4. Заполнить `AccessPolicy()` строками своего сервиса из таблицы grpc-api.md 3.3 и зарегистрировать обработчики в `wire()` (`internal/app/wire.go`).
5. Сервис с данными пользователей — заменить `NoTombstones` на библиотеку `go-tombstone` (FIN-110); остальные библиотеки — по «Библиотеки и обновления» ниже.
6. Секреты репозитория: `CLAUDE_CODE_OAUTH_TOKEN` (ревью `@claude`), `GO_MODULES_TOKEN`, если подключаются приватные модули fin-assist. Установить Claude GitHub App и Codex.
7. Обновить README и `AGENTS.md` под сервис.

Перед первым запуском Renovate заменить **оба** `local>` ref в `renovate.json` на новый `fin-assist/<repo>`. GitHub template не копирует служебную ветку: создать собственную `renovate-policy` по [инструкции bootstrap](docs/renovate-policy.md#подготовка-и-включение), затем защитить её и настроить `controllerLogin`, обязательные checks и Environment `renovate-policy` с запуском только из `main` и доступ к organization secret `RENOVATE_POLICY_TOKEN` (выбранный владельцем вариант; новому репозиторию явно проверить доступность секрета). Проверить, что оба presets разрешаются из нового репозитория, а не из шаблона. До проверки оставить `RENOVATE_POLICY_ENABLED` выключенным; секреты, environments и branch protection из шаблона не наследуются.

## Раскладка

```
cmd/service/                       main: serve and migrate commands, listeners, shutdown
internal/app/                      assembly: identity, pools, server, wire() (handlers, clients, jobs), access table, migration sources
internal/app/apitest/              the whole service in-process for API tests
internal/domain/                   entities and invariants, no infrastructure imports
internal/<feature>/                usecases; interfaces of repositories and clients they need
internal/adapter/grpc/server/      gRPC server, interceptors, access table types, identity interfaces
internal/adapter/grpc/<feature>/   gRPC handlers: messages <-> usecases
internal/adapter/postgres/         pools, go-postgres adapter, repositories on pgx
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

## Библиотеки и обновления

Транзакции и распознавание нарушений ограничений — [`go-postgres`](https://github.com/fin-assist/go-postgres) ([FIN-195](https://finassist.youtrack.cloud/issue/FIN-195)): шаблон сохраняет настройку пулов и выбор primary/replica, а общий механизм вызывает из библиотеки. Соответствие ограничений доменным ошибкам задаёт адаптер конкретной операции. Проверяемый пример с ошибкой COMMIT и `finerr.NOT_FOUND` — `internal/adapter/postgres/mapping_example_test.go`; правила — [docs/migrations.md](docs/migrations.md).

Общий код сервисов — отдельные Go-библиотеки из шаблона [`fin-assist/go-library-template`](https://github.com/fin-assist/go-library-template) ([FIN-118](https://finassist.youtrack.cloud/issue/FIN-118)): [`go-identity`](https://github.com/fin-assist/go-identity) (FIN-119), [`go-idempotency`](https://github.com/fin-assist/go-idempotency) (FIN-78), [`go-outbox`](https://github.com/fin-assist/go-outbox) (FIN-109), [`go-tombstone`](https://github.com/fin-assist/go-tombstone) (FIN-110).

- **Подключение** — по разделу «Внедрение» README библиотеки: `go get github.com/fin-assist/go-<имя>@vX.Y.Z`, источник миграций в `MigrationSources()` до источника сервиса, сборка в `wire()` — библиотека получает от сервиса транзакцию (`db.Tx`), логгер, реестр метрик, часы и настройки.
- **Версии** — semver-теги библиотеки, `go.mod` сервиса закрепляет версию; до первого прода — `v0`, несовместимое изменение — новый MINOR с записью «Несовместимо» в CHANGELOG библиотеки. Правила — `docs/releasing.md` в `fin-assist/go-library-template`.
- **Обновления приносит Renovate** (`renovate.json`): модули `github.com/fin-assist/*` (библиотеки и `contracts/gen/go`) — отдельный PR сразу после тега, остальные зависимости — один PR в неделю, версия Go — вручную. PR Renovate проходит CI сервиса и обрабатывается по [политике FIN-211](docs/renovate-policy.md). При несовместимом обновлении человеческий коммит в открытом PR переводит его на ручное сопровождение; если ботский PR уже закрыт после повторного падения, исправления сервиса идут отдельным PR с задачей YouTrack. Порядок миграции — по CHANGELOG библиотеки.
- **Проверить библиотеку до выпуска** — локально `go mod edit -replace github.com/fin-assist/go-<имя>=../go-<имя>`; `replace` в PR сервиса не попадает.

## Проверки

CI (`.github/workflows/ci.yml`):

| Job | Что |
|---|---|
| `go` | `go mod tidy` без изменений, `go vet`, `go test -race` с PostgreSQL 18 в Docker (testcontainers), включая «вверх → вниз → вверх» для миграций; тесты скриптов; общее покрытие и покрытие изменённых строк — в сводке прогона и комментарием в PR |
| `lint` | golangci-lint (`.golangci.yml`), в том числе `forbidigo` против обхода `finerr` и `depguard` против фреймворков, ORM и DI-контейнеров |
| `image` | сборка образа и запуск бинаря в нём |
| `migrations` (PR) | существующие миграции не изменены, squawk по новым |

Локально:

```bash
make test   # Docker for testcontainers, or TEST_DATABASE_URL=postgres://... to use your PostgreSQL
make lint
```

### Покрытие изменённых строк

Цель — не ниже **80 %** изменённых исполняемых строк production-кода (без `_test.go` и `testdata/`). CI считает его скриптом `scripts/coverage-diff.py` по профилю `go test -coverpkg=./... -coverprofile=cover.out` и диффу PR против базы (`base...HEAD`) и публикует таблицу в сводке прогона и одним комментарием в PR, который обновляется на каждом пуше. Ничего не падает: порог проверяется на ревью. Непокрытые строки перечисляются в описании PR («Как проверено») с причиной — например, ветка ошибки, которую не вызвать без отказа ОС.

Строка учитывается, если её содержит блок покрытия Go с ненулевым числом statements. Это метрика диапазонов строк: комментарии и пустые строки внутри блока тоже учитываются. С `-coverpkg=./...` строка покрыта, тесты какого бы пакета её ни выполнили.

```bash
make cover   # cover.out and the table against origin/main
python3 scripts/coverage-diff.py                 # project totals
python3 -m unittest discover -s scripts -p 'test_*.py'
```

Модули `github.com/fin-assist/*` берутся из git, минуя прокси (`GOPRIVATE`, задан в `Makefile` и CI). Для приватного — токен с чтением репозиториев fin-assist: локально `git config --global url."https://x-access-token:<token>@github.com/fin-assist/".insteadOf "https://github.com/fin-assist/"`, в CI — секрет `GO_MODULES_TOKEN`, в `docker build` — секрет `netrc`.

### Общее покрытие и изменённые строки

CI публикует разделы «Покрытие проекта» и «Покрытие изменённых строк» в сводке запуска и одном обновляемом комментарии PR. Оба раздела используют один профиль корневого Go-модуля, без повторного запуска тестов; на push сохраняется общее покрытие. Комментарий для PR из fork не записывается. При ошибке построения или пустом файле отчёта последний корректный комментарий сохраняется.

Метрика — уникальные строки внутри блоков Go-профиля с ненулевым числом statements, а не процент statements из `go tool cover`. `_test.go` и `testdata/` исключены; сгенерированный production-код включён на тех же условиях. Повторные блоки разных тестовых бинарей объединяются: достаточно одного выполнения. Без исполняемых строк отчёт сообщает «Нет данных», а не 100 %. Цель **80 %** относится только к изменённым строкам и проверяется на ревью; общий процент её не заменяет.
