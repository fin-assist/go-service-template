# gRPC, фоновые задания и сборка сервиса

Контракт межсервисного взаимодействия — [`fin-assist/contracts/docs/grpc-api.md`](https://github.com/fin-assist/contracts/blob/main/docs/grpc-api.md). FIN-246 подключает опубликованные механизмы вместо локальных копий. Публичные параметры, порядок интерцепторов, правила metadata, ошибок и retry описаны в [`go-grpc v0.1.0`](https://github.com/fin-assist/go-grpc/tree/v0.1.0); lifecycle заданий — в [`go-worker v0.1.0`](https://github.com/fin-assist/go-worker/tree/v0.1.0).

## Ответственность сервиса

`internal/app` связывает `go-grpc/server` с identity/tombstone реализациями, logger, реестром метрик и настройками сервиса. `wire()` регистрирует прикладные обработчики и возвращает реальные исходящие клиенты и jobs; `AccessPolicy()` задаёт правила зарегистрированных методов по таблице grpc-api.md 3.3. Пустой шаблон не добавляет бизнес-RPC, клиентов или заданий. Health и dev reflection принадлежат инфраструктурной сборке.

Обработчики остаются в `internal/adapter/grpc/<feature>/`, usecase — в `internal/<feature>/`. Данные контекста читаются через `github.com/fin-assist/go-grpc/reqctx`; локального `internal/reqctx` нет. Ошибки контракта создаются через `finerr`; внутренние bare status создаёт только библиотека `go-grpc/server`. Запрет обхода `finerr` в `.golangci.yml` сохраняется.

## Identity и удалённые пользователи

Сервис передаёт серверу реализации интерфейсов проверки сервисного и пользовательского токенов. `internal/app/identity.go` выбирает их по `AUTH_MODE` (FIN-323):

- `verify` (по умолчанию) — один `identity.NewAssembly` из [`go-identity` v0.2.0](https://github.com/fin-assist/go-identity/tree/v0.2.0) («Внедрение», шаг 0). `Env` = `ENV`, `Service` = `SERVICE_NAME`, часы `time.Now`, реестр метрик сервиса. Ключи access JWT пользователя приходят из `user.GetSigningKeys` по соединению `client.Dial(PEER_USER_ADDR)` с исходящим токеном самой сборки; повторяется только `GetSigningKeys`, вызовы считаются в `grpc_client_handled_total{peer="user"}`. При `ENV=prod` сервисы проверяются по токенам ServiceAccount (JWKS API-сервера по HTTPS с доверием только `AUTH_KUBERNETES_CA_FILE` и отдельным API-токеном, namespace `AUTH_KUBERNETES_NAMESPACE`), исходящий токен — projected-файл `SERVICE_TOKEN_FILE`. При `ENV=stage` — короткие JWT стенда: собственный ключ `AUTH_STAGE_SIGNING_KEY_FILE`, ключи сервисов `AUTH_STAGE_PUBLIC_KEYS_FILE`, общий `AUTH_STAGE_ISSUER`; файлы читаются при старте. `verify` с `ENV=dev`, неполные или противоречивые настройки (файлы стенда в prod, общий файл сервисного и API-токена, JWKS не по HTTPS) и нечитаемые файлы — отказ старта до открытия пулов, без перехода на dev-режим. `*Assembly` реализует `server.ServiceVerifier`, `server.UserVerifier` и `client.TokenSource`; вторая цепочка аутентификации не ставится. Отказ доступа считает `finassist_grpc_denied_total`, отказы токенов — `finassist_identity_rejected_total`, пропущенные JWK — `finassist_identity_jwks_skipped_keys`. Недоступность ключей — голый `UNAVAILABLE`, а не `INVALID_TOKEN` (FIN-293).
- Задание go-worker `identity-keys` (`Run` сборки) обновляет ключи в фоне; до первого обновления ключи загружаются лениво на первом запросе. `Close` сборки — в `App.Close` после остановки заданий.
- `insecure-dev` — токены `dev:<имя>` без проверки, только при `ENV=dev`: это проверяют и `config.Load`, и сама сборка `app.New` (для вызовов в обход `config.Load`, например `apitest`). Это не обход проверки в stage/prod.

Сервис `user` выпускает ключи сам и вместо `DialUser` передаёт `UserKeys` — локальный провайдер; эта сборка шаблона ему не подходит без замены.

`NoTombstones` остаётся временной явной реализацией для шаблона. Сервис с данными пользователей обязан заменить её интеграцией `go-tombstone` в `internal/app`; вынос transport-кода не реализует эту интеграцию автоматически.

## Клиенты и задания

Общий dial, service token и request metadata — `github.com/fin-assist/go-grpc/client`. Адреса peers, retry-списки безопасных методов, таймауты и token source выбирает сервис. Прикладной адаптер конкретного peer появляется в `internal/adapter/client/<peer>/` только вместе с usecase; транспортную реализацию библиотеки не копируют.

Общий запуск и периодичность — `github.com/fin-assist/go-worker`. Сервис явно передаёт logger, clock и RestartDelay=5s, сохраняя прежнюю политику перезапуска независимо от default библиотеки. Реальные jobs размещаются в `internal/worker/<job>/`. Задания запускаются в каждом поде: единственность эффекта обеспечивает прикладная блокировка или `FOR UPDATE SKIP LOCKED`, а не сам runner.

## PostgreSQL и lifecycle

Сервис передаёт pool options в `go-postgres.OpenPools` v0.2.0. Локальный `postgres.DB` остаётся тонким адаптером менеджера транзакций и маршрутизации: `Writer` выбирает транзакцию/primary, `Reader` — транзакцию, primary для `x-read-primary` от `bank-bff` или реплику. Репозитории и domain mapping принадлежат сервису; подробнее — [migrations.md](migrations.md).

`cmd/service` владеет listeners и порядком shutdown: HTTP `/readyz` (`ops.Readiness.Drain`, необратимо до конца процесса) и gRPC health одновременно перестают сообщать готовность, выдерживается `SHUTDOWN_DELAY` (вызовы ещё принимаются, `/healthz` отвечает 200), gRPC graceful stop ограничен `SHUTDOWN_TIMEOUT`, затем останавливаются jobs и закрываются пулы. Вынос библиотек не меняет этот порядок или конфигурационные defaults.

## Наблюдаемость

Logger и Prometheus registry создаёт сервис и явно передаёт библиотекам. `go-grpc` не должен молча отключить прежние server/client metrics из-за nil metrics/registry; logger по умолчанию в библиотеке не заменяет сервисный logger. Имена и метки сохраняются: `grpc_server_handled_total{method,code,reason}`, `grpc_server_handling_seconds{method}`, `finassist_grpc_denied_total{method,caller,cause}`, `grpc_client_handled_total{peer,method,code}`, `grpc_client_handling_seconds{peer,method}`. Пользовательские идентификаторы и другие неограниченные значения в labels запрещены.

`internal/logx` использует значения из `go-grpc/reqctx`. Логи не раскрывают токены, ключи, DSN и банковские данные; параметры маскирования и безопасного сообщения об ошибке задаёт контракт библиотеки. `/metrics`, `/healthz`, `/readyz` и readiness ping primary остаются в `internal/ops` и сборке сервиса. Смена зависимостей не добавляет новые метрики или process-wide logger/registry.
