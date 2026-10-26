# История изменений

## [Unreleased]

### Добавлено

- FIN-323: `AUTH_MODE=verify` собирается через `identity.NewAssembly` из `go-identity` v0.2.0: при `ENV=prod` — токены ServiceAccount Kubernetes, при `ENV=stage` — JWT стенда на ключах из файлов, ключи пользователя — из `user.GetSigningKeys` по `PEER_USER_ADDR`; задание `identity-keys` обновляет ключи, `Close` — после остановки заданий. Новые переменные `AUTH_USER_ISSUER`, `AUTH_USER_AUDIENCE`, `AUTH_KUBERNETES_*`, `AUTH_STAGE_*`; `verify` с `ENV=dev`, неполная настройка и один файл для сервисного и API-токена (в том числе через симлинк или жёсткую ссылку) — отказ старта. Раньше `verify` всегда отказывал при старте.

### Исправлено

- FIN-269: во время `SHUTDOWN_DELAY` HTTP `/readyz` отвечает 503 вместе с gRPC health `NOT_SERVING`; раньше `/readyz` до конца паузы отвечал 200.
- FIN-270: `app.New` отказывает в `AUTH_MODE=insecure-dev` вне `ENV=dev` и без `config.Load`; `DB_MAX_CONNS` вне диапазона int32 — ошибка конфигурации, а не молча усечённое значение; глобальное исключение gosec G115 снято, сужающие преобразования снова проверяются линтером.
- FIN-271: устранена нестабильность интеграционного теста закрытия пулов при конфликте метрик.

### Изменено

- FIN-347: версии библиотек в `AGENTS.md`, README и `docs/` приведены к `go.mod`: go-postgres v0.3.0, go-grpc v0.2.0, go-migrate v0.1.1. Только документация: код и поведение шаблона не менялись.
- FIN-272: `make test-names` проверяет и fuzz-цели (`FuzzUnit…`/`FuzzIntegration…`/`FuzzAPI…`, seed-корпус — в своём наборе) и отклоняет `Example…`, которые выполнялись бы вне трёх наборов покрытия.
- FIN-274: README — недостающие шаги создания сервиса: identity, очистка `CHANGELOG.md` и `docs/testing.md`, имя образа в CI.
- FIN-246: шаблон использует выпущенные go-grpc v0.1.0, go-migrate v0.1.0, go-worker v0.1.0 и go-postgres v0.2.0 вместо локальных копий инфраструктуры. Явная сборка, CLI, конфигурация, operational API и маршрутизация транзакций остаются в сервисе; бизнес-RPC, SQL и задания в пустой шаблон не добавлены.
- FIN-246: добавлены проверки сборки, CLI lifecycle, конфигурации, логов и тестовых helpers по назначению unit/integration/API. Все обычные Go-файлы helpers продолжают учитываться в трёх независимых профилях; пороги и guards сохранены.
