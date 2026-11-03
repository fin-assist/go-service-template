# Распространение FIN-421

Снимок инвентаризации fin-assist от 02.11.2026: 19 доступных действующих репозиториев, архивных нет.
Сначала подготовлены оба Go-шаблона, затем каждый действующий потребитель. Ни один потребитель не считается
обновлённым только по изменению шаблона. Включены infra и go-ci, поскольку у них также есть PR-ревьюеры.
В contracts правило Go относится к handwritten коду/генераторам; generated результат вручную не меняется.
Android использует release/0.2.16: после прошлого release PR эта ветка получила новые коммиты, отсутствующие в main.

| Репозиторий | База PR | Константы | Исследованный SHA | SHA256 workflow (префикс) |
| --- | --- | --- | --- | --- |
| go-service-template | main | Go | 93b4ea587d3e | 9a9bd7ec4485 |
| bank-bff | main | Go | 19de657c4968 | 4c96b923f792 |
| user | main | Go | 6e6399b3a825 | 9a9bd7ec4485 |
| bank-statement-data | main | Go | c21bf161cb4f | 9a9bd7ec4485 |
| analytics | main | Go | aab3a8c46bde | 9a9bd7ec4485 |
| assistant | main | Go | 4cf4daaed5d6 | 1e01385ee1c6 |
| go-library-template | main | Go | 9d1184fcb945 | cc6be60c1073 |
| go-idempotency | main | Go | c31142aca1b5 | cc6be60c1073 |
| go-outbox | main | Go | d992dea7be17 | cc6be60c1073 |
| go-tombstone | main | Go | 73f4aa3c54dd | cc6be60c1073 |
| go-identity | main | Go | a944a476725b | cc6be60c1073 |
| go-postgres | main | Go | 24eee63b24b3 | cc6be60c1073 |
| go-grpc | main | Go | 6cff28f0d157 | 9a9bd7ec4485 |
| go-migrate | main | Go | b83c267f62cb | 9a9bd7ec4485 |
| go-worker | main | Go | a9cfe4509d14 | 9a9bd7ec4485 |
| contracts | main | Go | 60fa09c0224b | 016a5921a534 |
| android | release/0.2.16 | Go неприменим | db53a6617304 | ae3325589d3c |
| infra | main | Go неприменим | 4466c50a92a8 | f41e515c3739 |
| go-ci | main | Go неприменим | c144e79f545e | 4c96b923f792 |

## Среды и доставка

Во всех строках проверены корневые AGENTS.md, CLAUDE.md с импортом @AGENTS.md и workflow Claude в main.
Workflow делает checkout и вызывает anthropics/claude-code-action@v1; автоматического общего каталога
профильных skills в конфигурации нет. Это наблюдение конфигурации, не доказательство наличия токена или
успешного чтения в облаке. У Codex GitHub интеграции нет доступной из репозитория настройки локального
каталога владельца. Поэтому выбран переносимый путь: явное чтение обычных файлов текущей ревизии.

[OpenAI о Code Review Rules](https://developers.openai.com/blog/custom-code-review-rules-for-codex)
описывает инструкции через AGENTS.md;
[документация Claude action](https://github.com/anthropics/claude-code-action/blob/main/docs/usage.md)
описывает checkout и инструменты action. Эти источники не подтверждают выполнение конкретного ревью.
Для первого PR запрос явно требует читать добавленные файлы PR head: workflow из main сам по себе
не означает, что новые правила уже активированы на default branch.

Workflows, permissions, secrets, required checks, защиты, deployment и UI tests не изменяются.
Локальная статическая проверка использует только versioned файлы без пользовательского каталога навыков;
она проверяет ссылки и сохранность правил, но не может доказать соблюдение инструкции моделью.
Сценарии выборки находятся в docs/review-skills/acceptance.md. В сводке ревьюер перечисляет названия применённых навыков, если они применялись.

## Состояние внедрения

Подготовлены изменения для всех 19 строк; до слияния соответствующего PR репозиторий не считается внедрённым.
Ссылки, результаты CI/ревью и ограничения по каждому PR ведутся в
[FIN-421](https://finassist.youtrack.cloud/issue/FIN-421). Задача не закрывается до завершения всего объёма.
