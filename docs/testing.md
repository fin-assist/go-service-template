# Проверки шаблона (FIN-238)

Шаблон предоставляет CLI `serve`/`migrate`, HTTP operational endpoints, gRPC health/reflection и инфраструктурные компоненты для будущих сервисов. Его `AccessPolicy()` пуст, `wire()` не регистрирует бизнес-RPC, исходящих клиентов или заданий. `AUTH_MODE=verify` намеренно отказывает до подключения identity library. В `MigrationSources()` пока только service source без SQL.

## Реально проверенное поведение

- Unit: изолированные валидаторы, конфигурация, прямые interceptor/helper contracts; неизвестная CLI-команда отвергается как usage, некорректные migration sources отклоняются конкретной ошибкой до попытки соединения с БД.
- Integration: реальный PostgreSQL18, транзакции и mapping, migration runner, transport clients/interceptors и фактические запуски jobs. Непустые service/library SQL-fixtures runner проходят up → down → up. Service source самого шаблона без SQL тоже проверяется на PostgreSQL: повторный Up, чистая версия0, отсутствие таблиц приложения/применённых версий, отказ Down/Force. Тест не skip и не создаёт фиктивную миграцию.
- API: полная `apitest.Start` assembly и настоящая CLI assembly через TCP/HTTP: migration deployment версии0, health Watch/Check и неизвестный health service, отсутствие бизнес-RPC, `/healthz`, `/readyz`, `/metrics`, HTTP404, orderly shutdown с недоступностью обоих listeners. После наблюдавшегося healthy baseline неудачные rollout при неподключённой verify identity, недоступной БД и занятом HTTP-порту не оставляют обслуживаемый health API.

API-тест не добавляет тестовые RPC или строки access policy. Прямые вызовы handler/validator и transport fixtures остаются unit/integration соответственно.

## Native Go1.26 baseline 24.10.2026

Собрано на `go1.26.0 darwin/amd64`, PostgreSQL18, `-race -count=1 -timeout=8m -coverpkg=./...`. Все три collectors и execution guards прошли без skipped Test. Порог проверен штатным `go-test-coverage v2.20.1`; Go1.27 для самого analyzer выбирается отдельно от compiler профилей.

| Набор | Native statements | Порог80 |
|---|---|---|
| Unit |52.4% (532/1015)|FAIL|
| Integration |69.7% (707/1015)|FAIL|
| API |46.1% (468/1015)|FAIL|
| Combined |89.2% (905/1015)|Информационное объединение|

Каждый набор инструментирует весь одинаковый модуль, без exclusions. Combined не заменяет отдельные требования. Это невыполненные критерии FIN-238; PR остаётся draft, required `go` должен быть красным.

## Структурные границы

Следующие значения взяты из native per-file breakdown (covered/total statements), а не из собственного алгоритма процентов:

| Файл | Unit | Integration | API |
|---|---|---|---|
| `internal/adapter/postgres/migrate/migrate.go` |54/173|136/173|75/173|
| `internal/adapter/postgres/migrate/migratetest/migratetest.go` |0/99|69/99|26/99|
| `internal/adapter/client/client.go` |15/53|41/53|4/53|
| `internal/adapter/grpc/server/interceptors.go` |130/131|126/131|58/131|
| `internal/adapter/postgres/db.go` |8/42|38/42|18/42|
| `internal/app/apitest/apitest.go` |3/35|0/35|26/35|
| `internal/worker/worker.go` |0/26|25/26|3/26|

Положительные PostgreSQL/transport/jobs сценарии по определению не unit. Через нынешнюю API assembly невозможно пройти бизнес-авторизацию и user/tombstone paths с пустой policy, обращения невключённых исходящих клиентов, реальные job bodies отсутствующего wiring или применение отсутствующих service SQL. Полный lifecycle SQL-fixtures runner проверяет integration, а не API самого шаблона. Файлы `apitest`/`migratetest` в обычных `.go` остаются в общем знаменателе. Порог не снижается; тесты не переименовываются и production seams/RPC/SQL ради процентов не добавляются.

`make test`, `make lint`, actionlint и CHANGELOG format прошли с ограничениями времени процессов. CI на Linux/Go1.26 повторно проверяет актуальный commit и публикует собственные profiles, execution evidence и native reports в artifact `coverage`.
