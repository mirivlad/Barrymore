# API, события и контракты

## 1. API principles

`/api/v1`, JSON, RFC 7807 problem details, request id, idempotency keys, optimistic concurrency через revision, SSE, explicit pagination, UTC timestamps и стабильные domain enums.

## 2. Основные endpoints

```text
GET/POST /api/v1/threads
GET/PATCH /api/v1/threads/{id}
POST      /api/v1/threads/{id}/positions
POST      /api/v1/threads/{id}/decisions
GET       /api/v1/threads/{id}/timeline

POST      /api/v1/conversations
POST      /api/v1/conversations/{id}/messages
GET       /api/v1/conversations/{id}/events
POST      /api/v1/conversations/{id}/cancel

GET       /api/v1/memory/candidates
POST      /api/v1/memory/candidates/{id}/accept
POST      /api/v1/memory/candidates/{id}/reject
GET/PATCH/DELETE /api/v1/memories/{id}

GET       /api/v1/workers
POST      /api/v1/workers/discover
POST      /api/v1/workers/{id}/probe
GET       /api/v1/workers/{id}/history

POST/GET  /api/v1/work-orders
POST      /api/v1/work-orders/{id}/approve
POST      /api/v1/work-orders/{id}/start
POST      /api/v1/work-orders/{id}/pause
POST      /api/v1/work-orders/{id}/resume
POST      /api/v1/work-orders/{id}/cancel
GET       /api/v1/work-orders/{id}/events
GET       /api/v1/work-orders/{id}/artifacts

GET       /api/v1/approvals/pending
POST      /api/v1/approvals/{id}/grant
POST      /api/v1/approvals/{id}/deny

GET       /api/v1/system/state
GET       /api/v1/expectations
GET       /api/v1/expectations/{id}
GET       /api/v1/discrepancies
POST      /api/v1/discrepancies/{id}/acknowledge
POST      /api/v1/discrepancies/{id}/retry
POST      /api/v1/probes
```

## 3. Event envelope

```json
{
  "id": "evt_...",
  "stream_type": "thread",
  "stream_id": "thr_...",
  "stream_revision": 42,
  "event_type": "thread.position.updated",
  "schema_version": 1,
  "occurred_at": "2026-08-04T00:00:00Z",
  "actor": {"type": "person", "id": "person_owner"},
  "correlation_id": "corr_...",
  "causation_id": "evt_...",
  "payload": {}
}
```

## 4. Основные события

Threads: `thread.created`, `thread.updated`, `thread.state.changed`, `thread.position.updated`, `thread.linked`, `thread.decision.recorded`, `thread.question.opened`, `thread.released`.

Memory: `memory.candidate.proposed`, `accepted`, `rejected`, `memory.created`, `superseded`, `revoked`, `retrieved`.

Staff: `worker.discovered`, `worker.probed`, `worker.availability.observed`, `worker.capability.observed`, `worker.trust.changed`, `worker.performance.recorded`.

Delegation: `work_order.proposed`, `approved`, `prepared`, `worker_run.started`, `worker_run.event`, `paused`, `completed`, `failed`, `verification.started`, `verification.completed`, `work_order.completed`.

Operational state: `observation.recorded`, `system_state.updated`, `expectation.created`, `expectation.satisfied`, `expectation.expired`, `discrepancy.detected`, `discrepancy.updated`, `probe.requested`, `probe.completed`, `reflex.started`, `reflex.completed`, `reflex.failed`, `escalation.requested`.

Security: `approval.requested`, `granted`, `denied`, `policy.allowed`, `policy.denied`, `secret_ref.used`, `remote_session.started`.

## 5. Adapter manifest

```yaml
apiVersion: barrymore/v1
kind: WorkerAdapter
metadata:
  id: claude-code
  displayName: Claude Code
spec:
  executable:
    candidates: ["claude"]
  probe:
    command: ["${executable}", "--version"]
    timeout: 10s
  modes: [interactive-pty, non-interactive]
  capabilities:
    declared: [repository-audit, code-edit, tests]
  policy:
    defaultTrust: worktree_write
    network: provider-required
  availability:
    strategy: plugin
```

Command templates строятся без shell string interpolation.

## 6. Context pack schema

```json
{
  "schema_version": 1,
  "work_order_id": "wo_...",
  "thread": {"id": "thr_...", "title": "Rollboard"},
  "goal": "...",
  "why": "...",
  "confirmed_decisions": [],
  "constraints": [],
  "workspace": {
    "root": "...",
    "git_head": "...",
    "worktree_policy": "isolated"
  },
  "allowed_actions": [],
  "forbidden_actions": [],
  "acceptance_criteria": [],
  "operational_contract": {
    "milestones": [],
    "heartbeat_policy": {},
    "allowed_probes": [],
    "allowed_recovery_actions": [],
    "stop_conditions": []
  },
  "verification_commands": [],
  "required_report": []
}
```

## 7. Model result schema

Conversational model возвращает proposal objects. Runtime применяет их только после schema validation и policies. Свободный `shell_command` в основном conversational contract запрещён.

Модель может предложить Expectation или Probe, но runtime нормализует их в зарегистрированный тип, проверяет стоимость/scope и не принимает произвольную модельную «реакцию» как ReflexPolicy.

## 8. Versioning

Database migrations, event upcasters, adapter API versions, prompt profile versions, context pack versions, parser versions и export archive versions. Изменения сопровождаются migration/replay tests.
