# Instructions for code agents

A Go service of «Понятные финансы» (B2C personal finance: OFX statement import from T-Bank, operations, analytics, LLM
assistant), made from `fin-assist/go-service-template`. Read by Claude Code (through `CLAUDE.md`) and by Codex. This
file is the orientation map: read it and `README.md` first, then open only what the task touches.

## Keep this file current

A PR that changes what this file, `README.md` or `docs/` say (layout, commands, CI checks, conventions, process)
updates them in the same PR. A service made from the template rewrites the first paragraph for itself.

## Where things are decided

- Contracts: `fin-assist/contracts` — `docs/api.md` (REST), `docs/grpc-api.md` (services, call models, metadata,
  errors, idempotency, outbox, transport), `docs/calc.md` (calculations), `proto/`. The service imports the generated
  module `github.com/fin-assist/contracts/gen/go` (messages, gRPC, `finerr`) at a tag `gen/go/vX.Y.Z`. A service
  never defines its own `.proto`, error codes or REST fields: a change goes to `contracts` first (its own FIN issue
  and PR), then the service bumps the module.
- Architecture, MVP scope, product decisions: Confluence PF (https://finassist.atlassian.net/wiki/spaces/PF),
  «Архитектура» (including «Миграции и доступ к БД» and «Репозитории и структура кода»). When the docs here and the
  contracts differ, the contracts and the owner's latest decision win; say so in the PR.
- Every change goes through a YouTrack issue (`FIN-<n>`, finassist.youtrack.cloud) referenced in the PR.

## Repository map

| What | Where |
|---|---|
| Commands `serve`, `migrate`; wiring of adapters, usecases and handlers; the access table | `cmd/service/` (`serve.go`, `wire.go`, `migrate.go`) |
| Entities and invariants (no pgx, gRPC or generated code) | `internal/domain/` |
| Usecases and the interfaces of repositories and clients they need | `internal/<feature>/` |
| gRPC server, interceptors, access table types, identity and tombstone interfaces | `internal/adapter/grpc/server/`; rules — `docs/server.md` |
| gRPC handlers (messages ↔ usecases) | `internal/adapter/grpc/<feature>/` |
| Pools, transaction manager, repositories | `internal/adapter/postgres/`; rules — `docs/migrations.md`, «Запросы на pgx» |
| Migration runner and the PostgreSQL for tests | `internal/adapter/postgres/migrate/`, `migratetest/` |
| SQL migrations (embedded) | `migrations/`; rules — `docs/migrations.md` |
| Clients of other services, service token source | `internal/adapter/client/` |
| Background jobs | `internal/worker/` |
| Configuration | `internal/config/`; variables — `docs/config.md` |
| Request-scoped values (request ID, caller, user, bank-bff hints) | `internal/reqctx/` |
| Logger, metrics endpoint, readiness | `internal/logx/`, `internal/ops/` |
| Image, local run, stand | `Dockerfile`, `compose.yaml`, `Makefile`, `deploy/systemd/` |
| CI, lint | `.github/workflows/ci.yml`, `.golangci.yml`, `.squawk.toml` |
| PR review by Claude | `.github/workflows/claude.yml` |

## Writing code

- Code, comments, identifiers, commit messages — English. `docs/*.md`, `README.md`, PR title and description —
  Russian. Comments where the logic is not obvious; a doc comment on every exported identifier.
- Layers: `domain` ← `<feature>` ← adapters ← `cmd`. A usecase declares the interface it needs (consumer side) and
  never imports an adapter, pgx or gRPC; a handler converts messages and calls a usecase; `wire()` connects them.
- Errors to callers: only `finerr` constructors (`finerr.NotFound()`, `finerr.LimitExceeded(resetsAt)`).
  `status.Error`, `status.New`, `errdetails.ErrorInfo` are forbidden by the linter; an internal refusal is a bare
  code through `bare()` in the server package. A new client reaction needs a new code in `contracts` first.
- Identity: the user of a user call is `reqctx.UserID(ctx)` (the verified `sub`), never a request field; a system
  call takes `user_id` from the request. Every new method gets its row in `accessPolicy()`, copied from
  grpc-api.md 3.3; the server does not start without it.
- Database: SQL with parameters only (`$1`), table and column names are constants. A usecase opens the transaction
  with `db.Run`; repositories take `db.Writer(ctx)` / `db.Reader(ctx)`; no calls to other services inside `Run`.
  Migrations follow `docs/migrations.md` (two releases for an incompatible change, `CONCURRENTLY` alone in a file).
- Clients of other services: `client.Dial` with `Retry` listing only reads and methods idempotent by construction;
  the context carries the deadline.
- Logs: `slog` `*Context` methods; no tokens, DSNs, statement contents or assistant dialogue in logs or errors.
  Metric labels: bounded values only — never `user_id`, an operation ID or free text.
- Tests: every usecase, handler and repository change comes with tests in the same PR; repositories and migrations
  run against PostgreSQL (`migratetest.DSN`), not mocks. A refused or failing path (foreign caller, invalid input,
  not found, database error) is a scenario too.

## Checks

CI (table in `README.md`): `go vet`, `go test -race` with PostgreSQL 18 in Docker, golangci-lint, image build, and on
PRs the migration checks (existing files unchanged, squawk on new ones). Run `make test` and `make lint` before
pushing.

## Workflow

- Branches: `<kind>/<slug>` — `feature/…`, `fix/…`, `tooling/…`; a PR targets `main`. Commits: imperative English
  subject (`Add …`, `Fix …`, `Address review: …`).
- PR title and description in Russian: what changed and why, deviations from the contracts or docs, how it was
  checked. A PR for a single issue starts its title with the key (`FIN-42 Поиск операций по сумме`); a PR for several
  issues lists all of them in the description, each linked (`https://finassist.youtrack.cloud/issue/FIN-<n>`).
- A dependency on a contract change: the contract PR is merged and `gen/go` tagged first, then the service PR bumps
  the module. Order of rollout for new enum values and error metadata — grpc-api.md 5.1 and 8.
- After the PR is open and all planned changes are pushed, request a review with one PR comment in Russian addressed
  to both reviewers; it starts with `@codex @claude`:

  ```
  @codex @claude проведите ревью этого PR по Code Review Rules в AGENTS.md, включая разделы Security и Tests.
  Замечания — на русском, inline на изменённых строках. Не придирайтесь к формулировкам документации и комментариев:
  замечание по тексту только если он искажает поведение кода. Особое внимание: <что именно и почему>.
  ```

  - `@claude` — `.github/workflows/claude.yml` (runs from the default branch), Claude Fable 5.1
    (`claude-fable-5-1`). Needs the Claude GitHub App and the `CLAUDE_CODE_OAUTH_TOKEN` secret.
  - `@codex` — the Codex GitHub integration under the owner's ChatGPT subscription.
- Waiting for the review: react to the PR's GitHub events when the session receives them; otherwise check 3 minutes
  after the request, then every 2 minutes. A reviewer has answered when it posted its review or summary for the
  latest commit; for Codex (`chatgpt-codex-connector[bot]`) «Didn't find any major issues» or a 👍 without a comment
  also counts. A reviewer silent for 15 minutes after the last request: say so in the PR and to the owner, stop
  waiting for it. The cycle ends when both answered the latest request and no thread is open.
- Address findings in follow-up commits on the same branch, reply in the thread and resolve it, then request a
  review again. Agents never merge a PR; the owner does.

## Code Review Rules

Read by `@claude` (through `CLAUDE.md`) and by the Codex integration, which looks for this heading. Two areas get a
dedicated pass on every PR: Security and Tests.

### General

- Write all review comments in Russian (summary and inline). Identifiers and quotes stay as they are.
- Report only verified problems with a concrete failure scenario: which caller, request or data, what goes wrong.
  Skip style nits, naming preferences and wording of comments and docs unless the text misstates the behaviour.
- Post each finding as an inline comment on the changed line. The summary lists what was checked, with a
  «Безопасность» part and a «Тесты» part; «not affected» is a valid answer, with one line why.
- Check that the PR references its YouTrack issue and updates `README.md`, this file and `docs/` when it changes
  what they describe.

### Security

The service handles a user's bank operations, phone number, statement files and assistant dialogue (personal data
under 152-ФЗ). A finding names what leaks or breaks and how a client, another user or a compromised service gets to
it. Check:

- Object access: every query by an object ID is scoped by the user (`WHERE user_id = $n` with the user from
  `reqctx.UserID` or the system call's `user_id`); another user's object gives `NOT_FOUND`, not its data or a
  different error that confirms it exists (IDOR / BOLA).
- Identity: the user never comes from a request field in a user call; a new method has its row in `accessPolicy()`
  matching grpc-api.md 3.3 (callers and model); hints `x-client-ip`, `x-client-version`, `x-read-primary` are used
  only as the server package sets them; `x-client-version` never decides access or limits.
- Errors: client-facing errors only through `finerr`; `status.message`, `error` details and logs contain no SQL,
  stack traces, tokens, DSNs or other users' data; an internal refusal is a bare code.
- Secrets: tokens and DSNs only in headers, files or `*_FILE` variables, never in logs, metric labels, URLs or error
  texts; the masking list in `docs/server.md` covers new secret metadata or fields.
- SQL: parameters only; nothing from a request selects a table, column, `ORDER BY` fragment or file path.
- Untrusted input: bounds come from protovalidate annotations in `contracts`; a handler that reads a size, count or
  page relies on them or checks itself; long-running or costly work (file parsing, LLM, calls, SMS) has a limit
  and a deadline.
- Transactions: an effect and its idempotency record or outbox row are written in one `db.Run`; no external call
  inside `Run`; a deleted user's data is not written back (tombstone).
- Clients: no user token forwarded to another service; retries only for reads and idempotent methods.

### Tests

- Every changed behaviour has a test in the same PR: usecase rules, handler mapping of errors, repository queries
  against PostgreSQL, refused paths (foreign caller, invalid input, not found, another user's object).
- A migration passes the up → down → up check; an irreversible step is marked as `docs/migrations.md` says.
- A bug fix comes with a test that fails without the fix.
- A finding names the scenario that is not covered and why it matters, not a coverage percentage.

### Consistency

- Field meanings, error codes, limits and call models match `fin-assist/contracts`; a deviation is stated in the PR.
- Names follow the existing ones; no second way to do what a package here already does (transactions, clients,
  config, logging).
- Metric labels stay bounded; a new metric has a help text and is listed in `docs/server.md`.
