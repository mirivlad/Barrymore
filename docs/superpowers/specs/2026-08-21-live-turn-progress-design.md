# Live Turn Progress

**Status:** approved and implemented
**Date:** 2026-08-21  
**Scope:** durable conversation turns, live progress, and provider telemetry

## 1. Purpose

A long Barrymore turn must not look like a frozen browser. The conversation UI
shows one unobtrusive, changing status line with the current runtime-owned
activity, elapsed time, and provider telemetry when the provider supplies it.

The turn continues if the page reloads or the browser connection disappears.
The final answer remains a validated structured proposal: partial model output
is telemetry, not conversation content and not an actionable proposal.

This is a prerequisite for later Capability Acquisition and Intervention work,
but it does not implement either subsystem.

## 2. Decisions

- A submitted message creates a durable `TurnRun`; the HTTP request does not
  remain open while the model works.
- The API returns `202 Accepted` with the new turn identifier.
- An application-owned runner executes the turn outside the request context.
- At most one unfinished turn may exist per conversation. A conflicting submit
  returns `409 Conflict`; different conversations may run concurrently.
- Coarse lifecycle and stage transitions are journaled. High-frequency provider
  progress is ephemeral and never creates one journal event per token.
- Page reload restores the durable turn and, while the same Barrymore process is
  alive, its latest ephemeral telemetry snapshot.
- A Barrymore process restart does not replay an interrupted model call. An
  unfinished turn becomes `interrupted`, because a future turn may contain paid
  calls or side effects that must not be duplicated implicitly.
- Exact token counts and rates are shown only when reported by the provider.
  Streaming progress may show an explicitly approximate `≈` count and rate.
- The UI contains one changing line, not a visible technical event log.

## 3. Alternatives considered

### 3.1 Client-only timer

Keep the blocking POST and replace `Бэрримор думает…` with guessed labels.
This is small but cannot name real runtime work, survive reload, or distinguish
provider work from a stalled request. Rejected.

### 3.2 Streaming response bound to the POST

Return NDJSON or SSE from the message POST. This can expose provider chunks, but
reload cancels the only connection and loses the turn handle. It also leaves a
multi-minute HTTP request as the owner of application work. Rejected.

### 3.3 Durable TurnRun plus resumable observation

Create the turn transactionally, execute it under the application lifecycle,
and observe it through the existing journal/SSE architecture. This adds a small
domain object and projection but removes the long-request failure mode and gives
future research/intervention work a stable place to report progress. Selected.

## 4. Domain model

`TurnRun` is execution state, not a replacement for `Message` or `Episode`.

```text
TurnRun
  id
  conversation_id
  thread_id
  user_message_id
  reply_message_id
  status
  stage
  stage_label
  provider
  model
  prompt_tokens
  output_tokens
  prompt_ms
  generation_ms
  prompt_tokens_per_second
  generation_tokens_per_second
  total_latency_ms
  error_code
  error_message
  result_json
  created_at
  started_at
  updated_at
  finished_at
```

Statuses are:

```text
queued -> running -> completed
                  -> failed
                  -> interrupted
```

Initial stage vocabulary is intentionally small:

```text
queued
recall
context
research
capability
provider_prompt
provider_generation
verification
finalization
```

The runtime owns `stage` and `stage_label`. The model cannot write arbitrary
activity text into the UI. A capability execution may supply a validated title
such as `Проверяю текущую модель`, while its internal identifier remains
available only in technical mode.

## 5. Durable events and projection

The conversation event stream gains:

```text
conversation.turn.queued
conversation.turn.started
conversation.turn.stage.changed
conversation.turn.completed
conversation.turn.failed
conversation.turn.interrupted
```

`conversation.turn.queued` and the owner's `conversation.message.recorded` are
written in the same journal transaction. The turn projection is updated in that
transaction, so a user message cannot be accepted without a corresponding turn.

The completed event carries the final `Turn` result and the projection stores it
as `result_json`. This preserves the current response contract—proposal,
Episode, memory candidates, thread outcome, and own actions—without trying to
reconstruct transient settlement results from unrelated projections.

Stage changes are durable only when the semantic activity changes. Timer ticks,
stream chunks, and rate updates do not enter the journal.

On startup, all `queued` or `running` projections from the previous process are
terminalized as `interrupted` with a runtime-authored event. They remain visible
and auditable but are not retried automatically.

## 6. Components and boundaries

### Conversation service

- `BeginTurn` validates the request and transactionally records the owner
  message plus queued TurnRun.
- `ExecuteTurn` contains the current `Send` pipeline and reports structured
  stage transitions through a narrow progress sink.
- `Turn`, `ActiveTurn`, and `TurnResult` read projections without consulting the
  model.
- Existing proposal validation, Episode creation, memory candidates, thread
  settlement, and work-order proposals remain inside the execution pipeline.

### Application-owned turn runner

- Registers work in `App`'s lifecycle and `WaitGroup` before the API returns.
- Uses an application context, not `r.Context()`.
- Recovers a panic at the turn boundary, logs the stack, and records `failed`.
- Refuses new work after shutdown begins.
- Does not implement a general durable job queue in this slice.

### Provider streaming

The model package gains an optional streaming interface. Providers that do not
implement it continue to use `Complete` and expose stage plus elapsed time only.

The OpenAI-compatible provider:

1. requests `stream: true` and final usage;
2. accumulates content privately;
3. reports bounded progress snapshots;
4. parses final `usage` and optional `timings`;
5. returns one ordinary `model.Response` for existing schema validation.

Structured JSON fragments are never shown as Barrymore's answer. Only the fully
assembled and validated proposal is recorded.

Provider timing fields are optional. If absent, Barrymore stores token counts
and total latency when available and leaves unsupported rates empty. It never
fabricates a precise rate by dividing tokens by unrelated wall-clock work.

### Ephemeral progress broker

An in-process broker keeps the latest snapshot per active turn and broadcasts
coalesced progress at no more than a few updates per second. Slow subscribers
receive the newest snapshot rather than an unbounded backlog.

The existing `/api/v1/stream` multiplexes these named progress events with
journal events. Ephemeral events carry no journal sequence and are not replayed
through `Last-Event-ID`; reload obtains the latest snapshot from the turn GET
endpoint before resuming SSE.

## 7. HTTP contract

### Start a turn

```http
POST /api/v1/conversations/{conversation_id}/messages
-> 202 Accepted
```

```json
{
  "turn_id": "trn_...",
  "status": "queued"
}
```

The existing synchronous response is intentionally replaced for this versioned
local API. Tests and the bundled UI migrate in the same slice.

### Read a turn

```http
GET /api/v1/conversations/{conversation_id}/turns/{turn_id}
GET /api/v1/conversations/{conversation_id}/turns/active
```

The result includes durable state, final message/proposal references when
completed, the final `Turn` result when completed, and the latest in-process
telemetry snapshot when available.

### Progress event

```text
event: conversation.turn.progress
data: {
  turn_id,
  conversation_id,
  stage,
  label,
  elapsed_ms,
  approximate_output_units,
  approximate_generation_rate
}
```

Durable `conversation.turn.*` events continue to use normal journal envelopes.

## 8. Metrics semantics

For a turn with several deliberation/research model calls:

- prompt and output tokens are summed exactly once across calls;
- prompt and generation milliseconds are summed across provider-reported calls;
- final generation speed is `sum(output_tokens) / sum(generation_ms)`;
- total latency is wall-clock duration of the whole turn;
- model latency is not presented as total turn latency;
- current per-call approximate progress resets when another provider call starts,
  while the displayed turn total remains cumulative.

The existing three-call research-loop regression test confirms that
`aggregateResponse` already sums prompt and completion tokens exactly once
(`30` prompt and `15` output for three `10`/`5` calls). New TurnRun/provider
telemetry tests must preserve that established behaviour.

## 9. UI behaviour

After submit, the owner's bubble appears immediately and one status row follows:

```text
Вспоминаю похожий опыт · 2 с
Проверяю состояние модели · 8 с
Формирую ответ · ≈84 токена · ≈13.1 ток/с · 17 с
```

Only one row exists; its contents change in place. It uses `role="status"` and
`aria-live="polite"`, respects reduced motion, and does not steal focus from the
composer. No animated spinner is required; changing text and elapsed time are
sufficient evidence of life.

Reload behaviour:

1. load messages;
2. request the active turn;
3. render its current status row;
4. connect/resume SSE;
5. replace the row when newer progress arrives.

On completion, the status row is replaced by the normal Barrymore bubble. Its
compact metadata shows exact totals and rates that the provider actually
reported. Technical retrieval trace remains technical-mode-only.

While a conversation has an unfinished turn, its composer is disabled. Other
conversations remain usable.

## 10. Errors and recovery

- Validation failures before `BeginTurn` return normal problem details and do
  not create a message or turn.
- Failure after queueing records `conversation.turn.failed`; the owner's message
  remains, and the UI replaces progress with a concise failure bubble.
- Provider disconnect preserves no partial assistant message. Accumulated JSON
  fragments are discarded.
- A malformed final proposal fails the turn and records the existing contract
  error; no partial proposal side effects are settled.
- SSE loss changes the connection indicator and reconnects normally. The turn
  continues independently.
- Reload races are resolved by comparing durable `updated_at`/status and the
  current progress snapshot; a completed turn always wins over stale progress.
- Application shutdown stops accepting turns, cancels in-process model calls,
  waits for runners within normal shutdown bounds, and terminalizes unfinished
  turns as interrupted on the next startup if necessary.

## 11. Verification

### Domain and projection tests

- owner message and queued TurnRun are atomic;
- a second active turn in the same conversation is rejected;
- stage transitions accept only known statuses/stages;
- completed, failed, and interrupted turns are terminal;
- projection rebuild reproduces TurnRun state;
- startup recovery interrupts orphaned queued/running turns;
- multi-call output tokens are aggregated exactly once.

### Provider tests

- OpenAI SSE fragments assemble into the same content as non-streaming output;
- final usage and `timings` populate exact telemetry;
- a provider without timings leaves rates absent;
- disconnect, malformed SSE, provider error, and oversized response fail cleanly;
- partial structured JSON is never exposed or persisted.

### API tests

- POST returns `202` quickly with `turn_id`;
- active-turn GET works before and after completion;
- same-conversation conflict returns `409`;
- progress SSE is correlated to turn and conversation;
- handler return or browser disconnect does not cancel the turn;
- completed GET exposes the final reply/proposal references.

### Browser acceptance

Using the real local Ornith provider:

1. submit a prompt and observe at least two truthful stage labels;
2. see elapsed time change without multiple rows appearing;
3. reload during generation and recover the same turn/status;
4. receive the final answer once;
5. see exact token totals and generation rate after completion;
6. confirm feedback controls still work and persist;
7. confirm narrow and reduced-motion layouts remain usable.

## 12. Rollout and documentation

Implement as one vertical slice with migration, projection rebuild support,
provider telemetry, API, UI, focused tests, browser capture, and documentation.
Update `IMPLEMENTATION_STATUS.md` and `docs/MODERNIZATION_PROGRESS.md` only after
fresh automated and real-browser gates.

The model-performance audit remains evidence, not a configuration change:
`threads=14`, full GPU offload, Flash Attention auto, and default `ubatch=512`
are retained because local benchmarks found no material faster launch setting.
The stale Qwen 35B values in `data/runtime/settings.json` are runtime data and are
not silently rewritten by this feature.

## 13. Non-goals

- Capability Acquisition or self-writing executors;
- intervention planning, approval, or rollback;
- automatic replay after a Barrymore restart;
- user cancellation controls;
- rendering model chain-of-thought or partial structured JSON;
- a general distributed job queue;
- changing the selected model or its launch parameters.

## 14. Acceptance criteria

The slice is complete only when:

- a turn is no longer owned by a long-lived message POST;
- current runtime activity is visible in one dynamic conversation row;
- reload during work restores the same TurnRun;
- exact final usage and provider-supported timing are persisted and displayed;
- approximate live values are visibly marked approximate;
- provider or contract failure leaves no partial Barrymore message;
- old synchronous API/UI tests are migrated, all repository gates pass, and a
  real browser run demonstrates the required behaviour.
