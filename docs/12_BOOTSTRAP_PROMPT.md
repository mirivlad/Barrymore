# Bootstrap prompt для Claude Code или Codex

Ты работаешь в корне нового проекта **Barrymore / Бэрримор**.

Бэрримор — локальная персональная система непрерывности, общения и делегирования работы внешним агентам. Это не очередной coding agent и не чат с памятью. Пользователь взаимодействует с постоянным Бэрримором, а внешние агенты являются сменяемыми работниками.

Архитектурно Бэрримор — event-driven predictive runtime: он наблюдает состояние, поддерживает Expectations, обнаруживает Discrepancies, выполняет только зарегистрированные bounded reflexes и обращается к LLM для неоднозначного рассуждения и разговора. Не превращай каждый timer, heartbeat или retry в model call.

## Сначала

1. Полностью прочитай `README.md`, `AGENTS.md`, `CLAUDE.md` (для Claude), все `docs/*.md`, `docs/adr/*.md` и `IMPLEMENTATION_STATUS.md`.
2. Проведи read-only host audit.
3. Проверь Git status, branches и worktrees.
4. Не сокращай проект до чата с Ollama.
5. Не строй собственный coding agent вместо worker adapters.
6. Не создавай fake UI с mock workers как итог.
7. Если Git не инициализирован, инициализируй и сделай отдельный commit документации.
8. Создай ADR по выбранному стеку.

## Непереговорные инварианты

- Thread — главная смысловая сущность;
- chat history не является памятью;
- shared history хранится отдельно;
- личность и память не принадлежат одной LLM;
- LLM создаёт proposals, runtime валидирует и применяет;
- workers не пишут memory напрямую;
- side effects проходят Policy & Approval;
- worker selection объясним;
- quota/cost status имеет confidence/freshness;
- Бэрримор не обходит лимиты;
- coding work по умолчанию изолируется worktree;
- push запрещён без разрешения;
- success требует Verification;
- state восстанавливается после restart;
- Observation, Expectation, Discrepancy, Probe и ReflexPolicy — runtime primitives;
- типовые recovery loops работают без LLM;
- reflex ограничен policy, attempts и verification;
- operational state не становится memory автоматически;
- secrets не хранятся в DB/logs/prompts.

## Технический default

Current stable Go, modular monolith, Svelte 5 + TypeScript, SQLite WAL + migrations, versioned HTTP API, SSE, Linux-first, user systemd, controlled subprocess runner, один configured conversational model adapter и worker adapter API.

Default можно изменить через ADR, сохранив инварианты.

## Первый большой проход

Реализуй один архитектурно полный vertical slice.

### Foundation

Repository structure, build/test/lint/dev, config validation, SQLite migrations, append-only journal, transactional projections, typed Observation/Expectation/Discrepancy, registered Probe/ReflexPolicy, bounded attempts, structured logging/redaction, health/readiness, local CI and migration backup.

### Threads

Thread CRUD, state, person/Barrymore positions, timeline, revisions and restart persistence.

### Conversation

Один реальный configured provider, streaming, structured response schema, no direct side effects, Thread links and model metadata.

Не требуй cloud key, если доступен local/OpenAI-compatible provider. Если provider не настроен, реализуй honest disabled state, не mock response.

### Memory

MemoryCandidate, accept/reject, confirmed MemoryItem, provenance, retrieval trace and no hidden write.

### Staff

Worker registry, adapter manifest, discovery, version probe, capability evidence, AvailabilitySnapshot, confidence/freshness and UI.

Выбери первый worker по фактически установленным CLI. Не hardcode продукт под Claude Code.

### Audit-only delegation

WorkOrder, runtime ranking/rationale, Approval, ContextPack artifact, operational contract, read-only policy, supervised worker process, RunEvent streaming, liveness Expectations, bounded attachment/heartbeat recovery без LLM, cancellation, raw output artifact, deterministic report verification, thread linkage and restart recovery.

Первое поручение не изменяет repository. Controlled write — следующий slice.

## UI

Минимальные разделы: Приёмная, Нити, Штат, Поручения, Память, Журнал. Debug noise раскрывается отдельно.

## Tests

Migrations, event append/projection, optimistic concurrency, thread restart, memory workflow, policy denial, worker discovery, unknown quota, expectation satisfaction/expiry, stale heartbeat diagnosis, bounded reconnect, reflex attempt limit, context schema, audit-only write denial, worker crash, restart reconciliation, provider unavailable while local control loops continue, API, frontend critical flows and Playwright E2E.

## Definition of Done

Показать walkthrough:

1. создать нить;
2. обменяться сообщением;
3. принять memory candidate;
4. обнаружить worker;
5. увидеть availability evidence;
6. создать audit WorkOrder;
7. одобрить;
8. запустить реальный worker;
9. увидеть Expectations и свежесть последнего сигнала;
10. продемонстрировать безопасный reconnect/probe без LLM;
11. получить report artifact;
12. пройти Verification;
13. перезапустить backend;
14. увидеть сохранённое состояние, ожидания и recovery history.

## Итог

Обнови `IMPLEMENTATION_STATUS.md`. Покажи HEAD, commits, ADR, команды, tests, FAIL/SKIP, Playwright artifacts, limitations and next slice. Не push.
