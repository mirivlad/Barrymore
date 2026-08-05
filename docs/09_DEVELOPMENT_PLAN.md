# План разработки

## 1. Подход

Это не одноразовый MVP. Реализуется узкий, но архитектурно законченный вертикальный контур. Каждый этап заканчивается работающим сценарием, тестами, документацией, миграциями, честным статусом и отсутствием скрытого долга.

## 2. Этап 0. Репозиторий и assumptions

Git repository, сохранённая документация, host audit, ADR по стеку, threat model, dev commands, local CI и `IMPLEMENTATION_STATUS.md`. Актуальные CLI и версии проверяются на реальном хосте.

## 3. Этап 1. Foundation

Go workspace, Svelte frontend, SQLite WAL, migrations, event journal, logging/redaction, config validation, health/readiness, API versioning, reproducible build, tests and migration backup.

С foundation создаются минимальные primitives предиктивного runtime: Observation, SystemStateSnapshot, Expectation, Discrepancy, typed Probe, ReflexPolicy registry, attempt limits и event-driven scheduler. Они не откладываются до «умной автономности», потому что на них опираются recovery и безопасное делегирование.

## 4. Этап 2. Нити и разговор

Создать Thread, Conversation, вызвать configured provider, получить structured response, записать Message, предложить ThreadPosition update, stream через SSE и восстановить после reload.

Один provider допустим; adapter architecture обязательна.

## 5. Этап 3. Память и общая история

MemoryCandidate, accept/reject, MemoryItem, SharedHistoryEntry, provenance, FTS5, retrieval trace, edit/revoke, no hidden auto-write and regression tests.

## 6. Этап 4. Штат

Worker registry, adapter manifest, executable discovery, version probe, capability evidence, availability snapshot, confidence/freshness, UI and manual override. Первый adapter выбирается после host audit.

## 7. Этап 5. Первое поручение: audit-only

1. пользователь открывает нить;
2. просит выбрать исполнителя;
3. runtime ранжирует workers;
4. UI показывает rationale;
5. пользователь утверждает audit-only WorkOrder;
6. ContextPack сохраняется;
7. runner запускает worker в read-only workspace;
8. runtime создаёт Expectations о liveness, scope и обязательном результате;
9. события идут в UI и обновляют state snapshot;
10. тестовый loss of attachment или stale heartbeat вызывает bounded probe/reconnect без LLM;
11. подтверждённый выход за scope ставит run на паузу;
12. результат собирается;
13. deterministic checks проверяют отчёт;
14. результат связывается с нитью;
15. после рестарта состояние, ожидания и recovery history доступны.

## 8. Этап 6. Controlled write

Git inventory, worktree creation, snapshot, worktree_write trust, file events, pause/cancel, project checks, diff review, commit policy, separate merge and recovery.

## 9. Этап 7. Репутация и второй adapter

Второй worker, comparable task categories, AgentPerformanceCandidate, feedback, deterministic ranking, retry with another worker, cost/quota policies. Без ML-router на старте.

## 10. Этап 8. Инициатива

Commitments, waiting conditions, scheduler, quiet hours, mute, frequency limit, reason display and no nagging.

## 11. Этап 9. Remote and integrations

WireGuard-first remote, Telegram notifications/approvals, files/connectors, calendar, email, browser extension and Verstak integration — после устойчивости core.

## 12. Первый большой проход Claude Code/Codex

Минимальный результат:

1. foundation repository;
2. event journal/migrations;
3. Thread CRUD;
4. Conversation с реальным model adapter;
5. MemoryCandidate accept/reject;
6. Observation/Expectation/Discrepancy foundation;
7. Worker registry;
8. discovery/probe одного installed CLI worker;
9. audit-only WorkOrder с operational contract;
10. ContextPack;
11. supervised runner;
12. bounded liveness recovery без LLM;
13. SSE status UI;
14. restart recovery;
15. tests/build passing;
16. actual walkthrough;
17. честный status.

Если объём не помещается:

```text
foundation + predictive primitives
→ threads
→ memory candidates
→ worker registry
→ audit-only delegation + bounded recovery
```

## 13. Definition of Done

Функция реализована, протестирована, доступна через UI/CLI, имеет Expectations и error states там, где это применимо, восстанавливается после restart, соблюдает policy, не требует LLM для типового control loop, документирована и показана на реальном сценарии.

## 14. Запрещённые сокращения

Один JSON вместо БД, chat history вместо memory, вечный polling через LLM, shell из model output, model-authored reflex без registry/policy, hardcode одного worker, secrets в DB, статические fake limits, success по exit code, fake worker вместо реального adapter, migrations «потом» и скрытые failing checks.


---

## Дополнение 2026-08-05: что сделано сверх плана

Между этапом 8 (инициатива) и этапом 9 (удалённый доступ) вклинился срез,
которого в плане не было и который оказался важнее очерёдности: Бэрримор
перестал быть только диспетчером.

| Что | ADR |
|---|---|
| Приёмная — разговор, а не пульт | — |
| собственные умения Бэрримора | 0019 |
| опыт меняет поведение и порождает умения | 0020 |
| подключение незнакомых инструментов | 0021 |

Этап 7 (репутация исполнителей) частично закрыт следствием: практика ведётся
и для внешних исполнителей по той же мерке, что и для собственных умений.
Отдельного ранжирующего признака в выборе исполнителя пока нет.
