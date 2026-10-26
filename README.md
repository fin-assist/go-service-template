# go-service-template

Выпуск версии сервиса и повторная публикация GitHub Release описаны в [docs/releasing.md](docs/releasing.md). История изменений — [CHANGELOG.md](CHANGELOG.md). Новый сервис начинает с пустого `Unreleased`; версии и теги создаёт владелец после PR и post-merge CI. Публикация образа и deployment — отдельные этапы.

Шаблон репозитория Go-сервиса «Понятные финансы» ([FIN-117](https://finassist.youtrack.cloud/issue/FIN-117)). Из него создаются `bank-bff`, `user`, `bank-statement-data`, `analytics`, `assistant`. Контракты (`.proto`, сгенерированный код, реестр ошибок `finerr`) — в [`fin-assist/contracts`](https://github.com/fin-assist/contracts), модуль `github.com/fin-assist/contracts/gen/go`.

FIN-225 подключает опубликованный `contracts/gen/go v0.5.0` и правила передачи service-owned `RequestRetryInfo` и вложенного `google.rpc.Status`: [docs/server.md](docs/server.md). Решения о повторе остаются в конкретном usecase; шаблон не добавляет бизнес-RPC, каталог UI-текстов или универсальное разрешение по коду ошибки.

- [docs/server.md](docs/server.md) — gRPC-сервер и интерцепторы, идентичность, таблица доступа, клиенты, транзакции, логи и метрики.
- [docs/config.md](docs/config.md) — переменные окружения.
- [docs/migrations.md](docs/migrations.md) — миграции и запросы на pgx ([FIN-115](https://finassist.youtrack.cloud/issue/FIN-115)).
- [AGENTS.md](AGENTS.md) — карта и правила для агентов, Code Review Rules.

## Автоматизация обновлений зависимостей

[Политика Renovate](docs/renovate-policy.md) (FIN-211): одна актуальная release/main, автомерж полностью ботских PR после обязательного CI, один повтор красного CI и закрытие с явным повтором через Dependency Dashboard. AndroidX/Compose остаются на ручной проверке. Исходные группировки, ограничения версий и расписания — `renovate-base.json`; generated target и quarantine — ветка `renovate-policy`. Активация требует отдельного токена и строгой защиты веток; по умолчанию выключена.

## Новый сервис из шаблона

1. Создать репозиторий из шаблона (GitHub → «Use this template»).
2. Заменить модуль: `go mod edit -module github.com/fin-assist/<service>` и пути импорта (`grep -rl go-service-template | xargs sed -i 's#fin-assist/go-service-template#fin-assist/<service>#g'`).
3. Переименовать `cmd/service` → `cmd/<service>`, `deploy/systemd/service.service`, имя в `compose.yaml`, `Makefile` (`BIN`, `build`, тег в `image`, `SERVICE_NAME` в `run`; DSN локального PostgreSQL — вместе с `compose.yaml`), `Dockerfile` (путь сборки и бинаря) и `.github/workflows/ci.yml` (job `image`: тег образа).
4. Заполнить `AccessPolicy()` строками своего сервиса из таблицы grpc-api.md 3.3 и зарегистрировать обработчики в `wire()` (`internal/app/wire.go`).
5. Настроить проверку идентичности. `AUTH_MODE=verify` (по умолчанию) собирается через `identity.NewAssembly` из [`go-identity` v0.2.0](https://github.com/fin-assist/go-identity/tree/v0.2.0) ([FIN-323](https://finassist.youtrack.cloud/issue/FIN-323), `internal/app/identity.go`): при `ENV=prod` — токены ServiceAccount Kubernetes, при `ENV=stage` — JWT стенда на ключах из файлов, ключи пользователя — из `user.GetSigningKeys` по `PEER_USER_ADDR`. Задать `AUTH_USER_ISSUER`, `AUTH_USER_AUDIENCE`, `PEER_USER_ADDR` и переменные своего окружения ([docs/config.md](docs/config.md), [docs/server.md](docs/server.md#identity-и-удалённые-пользователи)). Неполная настройка — отказ старта, перехода на dev-режим нет; `AUTH_MODE=insecure-dev` — только с `ENV=dev`. Сам `user` подключает свои ключи локально (`UserKeys` вместо `DialUser`), а не по этой сборке.
6. Сервис с данными пользователей — заменить `NoTombstones` на библиотеку `go-tombstone` (FIN-110); остальные библиотеки — по «Библиотеки и обновления» ниже.
7. Очистить историю шаблона: в `CHANGELOG.md` оставить пустой `## [Unreleased]` (записи шаблона к сервису не относятся); `docs/testing.md` переписать под сценарии сервиса — сейчас там границы и исторические замеры самого шаблона (FIN-238/FIN-246).
8. Секреты репозитория: `CLAUDE_CODE_OAUTH_TOKEN` (ревью `@claude`), `GO_MODULES_TOKEN`, если подключаются приватные модули fin-assist. Установить Claude GitHub App и Codex.
9. Обновить README и `AGENTS.md` под сервис.

Перед первым запуском Renovate заменить **оба** `local>` ref в `renovate.json` на новый `fin-assist/<repo>`. GitHub template не копирует служебную ветку: создать собственную `renovate-policy` по [инструкции bootstrap](docs/renovate-policy.md#подготовка-и-включение), затем защитить её и настроить `controllerLogin`, обязательные checks и Environment `renovate-policy` с запуском только из `main` и доступ к organization secret `RENOVATE_POLICY_TOKEN` (выбранный владельцем вариант; новому репозиторию явно проверить доступность секрета). Проверить, что оба presets разрешаются из нового репозитория, а не из шаблона. До проверки оставить `RENOVATE_POLICY_ENABLED` выключенным; секреты, environments и branch protection из шаблона не наследуются.

## Раскладка

```
cmd/service/                       main: serve and migrate commands, listeners, shutdown
internal/app/                      assembly: identity, pools, server, wire() (handlers, clients, jobs), access table, migration sources
internal/app/apitest/              the whole service in-process for API tests
internal/domain/                   entities and invariants, no infrastructure imports
internal/<feature>/                usecases; interfaces of repositories and clients they need
internal/adapter/grpc/<feature>/   gRPC handlers: messages <-> usecases
internal/adapter/postgres/         primary/replica routing, go-postgres transaction adapter, repositories
internal/adapter/client/<peer>/    service-specific client adapters, when needed
internal/worker/<job>/             application jobs, when needed
internal/config/                   configuration from the environment
internal/testdb/                   test-only PostgreSQL server selection; public migratetest harness
internal/logx/                     slog with values from the context
internal/ops/                      /metrics, /healthz, /readyz, readiness
migrations/                        SQL migrations, embedded into the binary
deploy/systemd/                    example unit for the stand
```

Пути с `<feature>`, `<peer>` и `<job>` обозначают место будущего прикладного кода, а не созданные пустые каталоги: шаблон не содержит бизнес-обработчиков, peer adapters или jobs. Общая инфраструктура импортируется из библиотек ниже.

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

FIN-246: общие механизмы подключены как опубликованные модули: [`go-grpc v0.2.0`](https://github.com/fin-assist/go-grpc/tree/v0.2.0) (`server`, `client`, `reqctx`), [`go-migrate v0.1.1`](https://github.com/fin-assist/go-migrate/tree/v0.1.1), [`go-worker v0.1.0`](https://github.com/fin-assist/go-worker/tree/v0.1.0) и [`go-postgres v0.3.0`](https://github.com/fin-assist/go-postgres/tree/v0.3.0). Публичные контракты и примеры — в README библиотек. Сервис владеет конфигурацией, access policy, identity/tombstone wiring, регистрацией обработчиков, источниками SQL и порядком запуска/остановки. Прикладные peer adapters и jobs добавляются вместе с реальными сценариями.

Транзакции и распознавание нарушений ограничений — [`go-postgres`](https://github.com/fin-assist/go-postgres) ([FIN-195](https://finassist.youtrack.cloud/issue/FIN-195)): шаблон передаёт настройки в `go-postgres.OpenPools`, а локальный `postgres.DB` сохраняет выбор primary/replica и тонкий адаптер менеджера транзакций. Соответствие ограничений доменным ошибкам задаёт адаптер конкретной операции. Проверяемый пример с ошибкой COMMIT и `finerr.NOT_FOUND` — `internal/adapter/postgres/mapping_example_test.go`; правила — [docs/migrations.md](docs/migrations.md).

Общий код сервисов — отдельные Go-библиотеки из шаблона [`fin-assist/go-library-template`](https://github.com/fin-assist/go-library-template) ([FIN-118](https://finassist.youtrack.cloud/issue/FIN-118)): [`go-identity`](https://github.com/fin-assist/go-identity) (FIN-119), [`go-idempotency`](https://github.com/fin-assist/go-idempotency) (FIN-78), [`go-outbox`](https://github.com/fin-assist/go-outbox) (FIN-109), [`go-tombstone`](https://github.com/fin-assist/go-tombstone) (FIN-110).

- **Подключение** — по разделу «Внедрение» README библиотеки: `go get github.com/fin-assist/go-<имя>@vX.Y.Z`, источник миграций в `MigrationSources()` до источника сервиса, сборка в `wire()` — библиотека получает от сервиса транзакцию (`db.Tx`), логгер, реестр метрик, часы и настройки.
- **Версии** — semver-теги библиотеки, `go.mod` сервиса закрепляет версию; до первого прода — `v0`, несовместимое изменение — новый MINOR с записью «Несовместимо» в CHANGELOG библиотеки. Правила — `docs/releasing.md` в `fin-assist/go-library-template`.
- **Обновления приносит Renovate** (`renovate.json`): модули `github.com/fin-assist/*` (библиотеки и `contracts/gen/go`) — отдельный PR сразу после тега, остальные зависимости — один PR в неделю, версия Go — вручную. PR Renovate проходит CI сервиса и обрабатывается по [политике FIN-211](docs/renovate-policy.md). При несовместимом обновлении человеческий коммит в открытом PR переводит его на ручное сопровождение; если ботский PR уже закрыт после повторного падения, исправления сервиса идут отдельным PR с задачей YouTrack. Порядок миграции — по CHANGELOG библиотеки.
- **Проверить библиотеку до выпуска** — локально `go mod edit -replace github.com/fin-assist/go-<имя>=../go-<имя>`; `replace` в PR сервиса не попадает.

## Проверки

CI (`.github/workflows/ci.yml`):

| Job | Что |
|---|---|
| `go` | `go mod tidy` без изменений, `go vet`, `go test -race` с PostgreSQL 18 в Docker (testcontainers), включая «вверх → вниз → вверх» для миграций; тесты скриптов; отдельные обязательные total80 для unit/integration/API; combined дополнительно, отчёты и профили в artifacts |
| `lint` | golangci-lint (`.golangci.yml`), в том числе `forbidigo` против обхода `finerr` и `depguard` против фреймворков, ORM и DI-контейнеров |
| `image` | сборка образа и запуск бинаря в нём |
| `migrations` (PR) | существующие миграции не изменены, squawk по новым |

Локально:

```bash
make test   # Docker for testcontainers, or TEST_DATABASE_URL=postgres://... to use your PostgreSQL
make lint
```

### Покрытие трёх наборов (FIN-238)

Каждый набор отдельно должен покрывать **80% statements всего модуля**, с согласованным округлением `go-test-coverage v2.20.1`: 79,95% принимается как 80,0%. Результат объединения не компенсирует недостаток отдельного набора. Это замена прежней информационной метрики изменённых строк FIN-210; старые проценты строк с новой метрикой statements напрямую не сравниваются.

Имена проверяет `make test-names` до прогонов: каждый тест — `TestUnit…`/`TestIntegration…`/`TestAPI…`, каждая fuzz-цель — `FuzzUnit…`/`FuzzIntegration…`/`FuzzAPI…` (её seed-корпус выполняется в своём наборе). `Example…` в сервисе отклоняется: публичного Go API у сервиса нет, а префикс набора в имени примера невозможен, и он выполнялся бы вне всех трёх наборов.

- **Unit** — `TestUnit…`: изолированные правила, преобразования и валидация на фейках, без БД и сети.
- **Integration** — `TestIntegration…`: настоящие PostgreSQL/миграции/транзакции, транспортные клиенты на тестовых серверах, реальные запуски фоновых заданий.
- **API** — `TestAPI…`: полная сборка сервиса и вызов публичного gRPC API. У шаблона это health и отказ неизвестному методу, а также настоящая CLI assembly с `/healthz`, `/readyz`, `/metrics`, завершением работы и отказом API после неудачного restart; бизнес-RPC шаблон не реализует.

У всех трёх прогонов одинаковый `-coverpkg=./...`: весь production-код, generated-код и тестовая инфраструктура в обычных `.go` файлах учитываются одинаково. `internal/testdb` и `internal/app/apitest` остаются в знаменателе; библиотечное исключение `internal/pgtest` в сервис не переносится. Исключений ради порога нет. Тестовые `_test.go` файлы и `testdata/` Go штатно не инструментирует. Существующие тесты распределены по фактической сборке, без удаления или подмены уровня.

```bash
# Requires Go, Bash, jq and Python 3.11+ for the script checks.
make cover-unit          # collect unit.out/unit.jsonl, validate evidence, enforce total80
make cover-integration   # real PostgreSQL: Docker or TEST_DATABASE_URL
make cover-api           # full assembly on real PostgreSQL
make cover               # three profiles and independent gates
make cover-combined      # informational union of existing profiles, built-in tool merge
python3 -m unittest discover -s scripts -p 'test_*.py'
```

Все прогоны используют `-race -count=1 -timeout=8m` и `CI=true`: недоступная БД даёт ошибку. Проверка входов `scripts/check-suite.sh` требует хотя бы один реально прошедший Test и отвергает пропуски/ошибки тестов, отсутствующий, пустой, header-only или нулевой профиль. Пакет production-кода без собственных тестов допустим: он остаётся в знаменателе. Никакого собственного расчёта процентов и объединения профилей нет.

CI сохраняет обязательный check `go`: он собирает и проверяет каждый набор, даже при недостаточном покрытии другого; итог падает при любом неуспешном наборе или пороге. Сводка и artifact `coverage` содержат `.out`, `.jsonl`, штатные `.coverage.txt`/`.report.txt` для unit/integration/API/combined. Ссылка на artifact находится в сводке CI. Права записи в PR, в том числе из fork, не нужны. Порог statements не доказывает полноту бизнес-сценариев — она проверяется отдельно на ревью.

Исторический замер FIN-238 до выноса библиотек (24.10.2026, Go1.26.0/PostgreSQL18): unit52.4%, integration69.7%, API46.1%, combined89.2% (1015 statements). В том замере collectors и execution guards прошли без skip, а все три80% критерия были невыполнены. Это не результат сборки FIN-246; её актуальные числа публикует CI текущего commit. Сценарии и точные native per-file границы — [docs/testing.md](docs/testing.md). Пустой service migration source проверяется на PostgreSQL без SQL-пустышек.

Модули `github.com/fin-assist/*` берутся из git, минуя прокси (`GOPRIVATE`, задан в `Makefile` и CI). Для приватного — токен с чтением репозиториев fin-assist: локально `git config --global url."https://x-access-token:<token>@github.com/fin-assist/".insteadOf "https://github.com/fin-assist/"`, в CI — секрет `GO_MODULES_TOKEN`, в `docker build` — секрет `netrc`.
