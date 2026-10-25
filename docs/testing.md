# Проверки шаблона (FIN-238 / FIN-246)

Шаблон предоставляет CLI `serve`/`migrate`, HTTP operational endpoints, gRPC health/reflection и инфраструктурные компоненты для будущих сервисов. Его `AccessPolicy()` пуст, `wire()` не регистрирует бизнес-RPC, исходящих клиентов или заданий. `AUTH_MODE=verify` намеренно отказывает до подключения identity library. В `MigrationSources()` пока только service source без SQL.

## Границы проверок после FIN-246

- Unit: изолированные сервисные валидаторы, конфигурация и routing/assembly contracts; неизвестная CLI-команда отвергается как usage, некорректные migration sources отклоняются конкретной ошибкой до попытки соединения с БД.
- Integration: реальный PostgreSQL18, сервисный transaction/routing adapter и mapping, подключение публичного migration runner и сборка инфраструктуры. Самостоятельные ветви transport/worker/runner проверяются в соответствующих библиотеках; их тесты не копируются в сервис. Service source самого шаблона без SQL тоже проверяется на PostgreSQL: повторный Up, чистая версия0, отсутствие таблиц приложения/применённых версий, отказ Down/Force. Тест не skip и не создаёт фиктивную миграцию.
- API: полная `apitest.Start` assembly и настоящая CLI assembly через TCP/HTTP: migration deployment версии0, health Watch/Check и неизвестный health service, отсутствие бизнес-RPC, `/healthz`, `/readyz`, `/metrics`, HTTP404, orderly shutdown с недоступностью обоих listeners. После наблюдавшегося healthy baseline неудачные rollout при неподключённой verify identity, недоступной БД и занятом HTTP-порту не оставляют обслуживаемый health API.

API-тест не добавляет тестовые RPC или строки access policy. Прямые вызовы handler/validator и transport fixtures остаются unit/integration соответственно.

## Выбор PostgreSQL и ownership хелперов

`internal/testdb` выбирает PG18 через `TEST_DATABASE_URL` либо testcontainer и передаёт adminDSN в публичный `go-migrate/migratetest.DSN(t, adminDSN)`. Этот helper создаёт отдельную БД и регистрирует её bounded cleanup; он не читает environment, не запускает container и не удаляет чужую БД. `migratetest.CheckUpDownUp(t, dsn, source)` проверяет переданный источник, не владея lifetime БД. Контракт и примеры — [README go-migrate](https://github.com/fin-assist/go-migrate/tree/v0.1.0); service source и lifetime сервера принадлежат сервису.

`internal/testdb` и `internal/app/apitest` не являются runtime business logic, но их обычные `.go` входят во все три профиля `-coverpkg=./...`. Никакое библиотечное исключение `internal/pgtest` не переносится в сервис. Unit не обращается к БД/сети; классификация сценариев и guards остаются прежними. Актуальные native проценты FIN-246 следует читать из artifact текущего CI; таблица ниже сохранена только как историческое evidence.

## Результат FIN-246 на полном production-знаменателе

Локально, Go1.26.0/darwin-amd64 и PostgreSQL18, `-race -count=1 -timeout=8m`:

| Набор | Native statements | Порог80 |
|---|---|---|
| Unit |81.7% (344/421)|PASS|
| Integration |81.9% (345/421)|PASS|
| API |80.3% (338/421)|PASS|
| Combined |95.0% (400/421)|Только информация|

Каждый suite действительно выполнен без skipped/failed Test; структуры блоков всех трёх профилей совпадают. Это результат штатного go-test-coverage v2.20.1, а не ручного расчёта. CI на Linux повторяет проверку и сохраняет свои отчёты.

Содержательные сценарии FIN-246: ошибки/порядок сборки и cleanup пулов при конфликте метрик; owned/foreign transactions и read-primary; CLI usage/configuration/exit status, empty-source migration deployment, drain/shutdown/jobs на unit-фейках; integration файлов конфигурации/логов, HTTP readiness и реальных PostgreSQL транзакций. API вызывает настоящую CLI assembly, проверяет health/reflection/operational HTTP, отказ отсутствующим бизнес-RPC, закрытие при bind failure и переход readiness/health при запрете PostgreSQL-подключений и восстановлении. Задания, SQL и RPC для бизнес-сценариев не добавлены. Unit стадии API harness проверяют IO через маленькие consumer-интерфейсы без сети и БД; integration подтверждает реальную изоляцию БД, конфигураций и очистку ресурсов.

## Исторический native Go1.26 baseline до FIN-246 (25.10.2026)

Собрано на `go1.26.0 darwin/amd64`, PostgreSQL18, `-race -count=1 -timeout=8m -coverpkg=./...`. Все три collectors и execution guards прошли без skipped Test. Порог проверен штатным `go-test-coverage v2.20.1`; Go1.27 для самого analyzer выбирается отдельно от compiler профилей.

| Набор | Native statements | Порог80 |
|---|---|---|
| Unit |52.4% (532/1015)|FAIL|
| Integration |69.7% (707/1015)|FAIL|
| API |46.1% (468/1015)|FAIL|
| Combined |89.2% (905/1015)|Информационное объединение|

Каждый набор инструментирует весь одинаковый модуль, без exclusions. Combined не заменяет отдельные требования. Эти исторические результаты не удовлетворяли FIN-238. После выноса библиотек знаменатель изменён составом модуля; текущие результаты подтверждаются заново в CI, прежние проценты не являются текущим verdict.

## Исторические границы до выноса библиотек

Следующие пути относятся к прежней раскладке, а не к текущим локальным пакетам. Значения взяты из native per-file breakdown (covered/total statements), а не из собственного алгоритма процентов:

| Файл | Unit | Integration | API |
|---|---|---|---|
| `internal/adapter/postgres/migrate/migrate.go` |54/173|136/173|75/173|
| `internal/adapter/postgres/migrate/migratetest/migratetest.go` |0/99|69/99|26/99|
| `internal/adapter/client/client.go` |15/53|41/53|4/53|
| `internal/adapter/grpc/server/interceptors.go` |130/131|126/131|58/131|
| `internal/adapter/postgres/db.go` |8/42|38/42|18/42|
| `internal/app/apitest/apitest.go` |3/35|0/35|26/35|
| `internal/worker/worker.go` |0/26|25/26|3/26|

Положительные PostgreSQL/transport/jobs сценарии по определению не unit. Через историческую API assembly невозможно пройти бизнес-авторизацию и user/tombstone paths с пустой policy, обращения невключённых исходящих клиентов, реальные job bodies отсутствующего wiring или применение отсутствующих service SQL. Полный lifecycle SQL-fixtures runner проверяет integration, а не API самого шаблона. В том замере локальные `apitest`/`migratetest` в обычных `.go` входили в общий знаменатель. После FIN-246 локального migratetest нет; сервисные `internal/testdb` и `internal/app/apitest` по-прежнему учитываются без исключений. Порог не снижается; тесты не переименовываются и production seams/RPC/SQL ради процентов не добавляются.

В историческом прогоне `make test`, `make lint`, actionlint и CHANGELOG format прошли с ограничениями времени процессов. CI на Linux/Go1.26 повторно проверяет актуальный commit и публикует собственные profiles, execution evidence и native reports в artifact `coverage`.
