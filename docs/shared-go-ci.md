# Общий Go CI

Каноническая документация и реализация находятся в отдельном [fin-assist/go-ci](https://github.com/fin-assist/go-ci/blob/main/docs/shared-go-ci.md) ([FIN-372](https://finassist.youtrack.cloud/issue/FIN-372)). Сервисный шаблон является обычным потребителем, общие workflows/configs/self-tests здесь не публикуются.

Профиль шаблона: `service`, `gosec_g115=false`, `squawk_smallint=false`, бинарный образец `service`. Полный source SHA задаётся в `.finassist-ci.json` и совпадает со всеми refs/inputs в `.github/workflows/ci.yml`.

Локально: `make lint-config`, `make lint`, `make fmt`, `make lint-migrations BASE_REF=origin/main`. Сначала получите базовую ветку; generated configs и pinned cache игнорируются Git. Нужны Python3.11+ с PyYAML, Git, Go и Node/npm; холодный source cache требует GitHub, tool caches — их сети или предварительного заполнения.

Renovate обновляет pins одной группой. При изменении source bootstrap синхронизируется scripts/common_ci.py в том же PR; contract отклоняет несовпадение. Human commit исключает такой PR из bot-only FIN-211. Откат возвращает matching pins/bootstrap; откат переноса между репозиториями также возвращает прежние source coordinates. Общий CI сначала сливается отдельным source PR, затем подключаются потребители. Merge требует команды владельца.
