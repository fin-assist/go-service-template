# История изменений

## [Unreleased]

- FIN-372: отдельный источник fin-assist/go-ci переиспользуемых Go CI и профилей линтеров с неизменяемым SHA, проверками исключений и контрольными lint-прогонами. Runtime и API не меняются.

- FIN-148: CI сохраняет stage-бинарь из проверенного образа с SHA, manifest и контрольной суммой.

### Добавлено

- FIN-323: `AUTH_MODE=verify` собирается через `identity.NewAssembly` из `go-identity` v0.2.0: при `ENV=prod` — токены ServiceAccount Kubernetes, при `ENV=stage` — JWT стенда на ключах из файлов, ключи пользователя — из `user.GetSigningKeys` по `PEER_USER_ADDR`; задание `identity-keys` обновляет ключи, `Close` — после остановки заданий. Новые переменные `AUTH_USER_ISSUER`, `AUTH_USER_AUDIENCE`, `AUTH_KUBERNETES_*`, `AUTH_STAGE_*`; `verify` с `ENV=dev`, неполная настройка и один файл для сервисного и API-токена (в том числе через симлинк или жёсткую ссылку) — отказ старта. Раньше `verify` всегда отказывал при старте.

### Исправлено

- FIN-351: проверка systemd unit стенда отклоняет повтор имени `LoadCredential=`, `LoadCredentialEncrypted=` и `ImportCredential=`; добавлены отрицательные мутации unit-файла, включая чужой источник перед правильным. Изменены только тесты, действующий unit и runtime не меняются.
- FIN-349: пример unit-файла стенда `deploy/systemd/service.service` использует общий JWKS стенда `/etc/finassist/stage-public.jwks` (0644, читается под `DynamicUser` напрямую) вместо `/etc/finassist/stage-keys.json`, сам задаёт `SERVICE_NAME=service` (kid/sub ключа подписи стенда), шапка перечисляет файлы ключей с режимами и предупреждает, что `EnvironmentFile=` перекрывает `Environment=`. Unit-тест `TestUnitStandUnitSatisfiesStageIdentity` загружает конфигурацию из unit-файла, так что сервисы из шаблона получают проверку unit из коробки. Код сервиса не менялся.
- FIN-269: во время `SHUTDOWN_DELAY` HTTP `/readyz` отвечает 503 вместе с gRPC health `NOT_SERVING`; раньше `/readyz` до конца паузы отвечал 200.
- FIN-270: `app.New` отказывает в `AUTH_MODE=insecure-dev` вне `ENV=dev` и без `config.Load`; `DB_MAX_CONNS` вне диапазона int32 — ошибка конфигурации, а не молча усечённое значение; глобальное исключение gosec G115 снято, сужающие преобразования снова проверяются линтером.
- FIN-271: устранена нестабильность интеграционного теста закрытия пулов при конфликте метрик.

### Изменено

- FIN-225: rollout-документация обновлена под опубликованный go-idempotency v0.6.0: сохранена историческая граница v0.5.0, обязательны consumer pin и проверка реального replay; фазовое решение permission и ограничения TTL не меняются. Зависимость и runtime шаблона не добавлены.

- FIN-225: для подключённого contracts/gen/go v0.5.0 описаны выбор разрешения повтора в usecase, передача RequestRetryInfo, границы COMMIT/replay и независимый вложенный Status результата. Шаблон не добавляет бизнес-обработчики, UI-каталог или вывод permission по коду ошибки.
- FIN-347: версии библиотек в `AGENTS.md`, README и `docs/` приведены к `go.mod`: go-postgres v0.3.0, go-grpc v0.2.0, go-migrate v0.1.1. Только документация: код и поведение шаблона не менялись.
- FIN-272: `make test-names` проверяет и fuzz-цели (`FuzzUnit…`/`FuzzIntegration…`/`FuzzAPI…`, seed-корпус — в своём наборе) и отклоняет `Example…`, которые выполнялись бы вне трёх наборов покрытия.
- FIN-274: README — недостающие шаги создания сервиса: identity, очистка `CHANGELOG.md` и `docs/testing.md`, имя образа в CI.
- FIN-246: шаблон использует выпущенные go-grpc v0.1.0, go-migrate v0.1.0, go-worker v0.1.0 и go-postgres v0.2.0 вместо локальных копий инфраструктуры. Явная сборка, CLI, конфигурация, operational API и маршрутизация транзакций остаются в сервисе; бизнес-RPC, SQL и задания в пустой шаблон не добавлены.
- FIN-246: добавлены проверки сборки, CLI lifecycle, конфигурации, логов и тестовых helpers по назначению unit/integration/API. Все обычные Go-файлы helpers продолжают учитываться в трёх независимых профилях; пороги и guards сохранены.
