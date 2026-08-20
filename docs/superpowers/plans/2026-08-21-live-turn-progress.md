# Live Turn Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the blocking conversation POST with a durable asynchronous TurnRun whose truthful stage, elapsed time, token usage, and provider-supported speed appear in one reload-safe UI status row.

**Architecture:** Conversation owns the event-sourced TurnRun and its projection; App owns execution lifetime; an optional streaming provider reports telemetry without exposing partial structured JSON. Durable stage changes use the existing journal/SSE, while a bounded in-process progress broker carries ephemeral counters and keeps the latest snapshot for reload.

**Tech Stack:** Go 1.24, SQLite migrations and projections, net/http, OpenAI-compatible SSE, vanilla JavaScript, existing journal broker and browser UI.

## Global Constraints

- Work directly on `master`; do not create a branch or pull request.
- The model returns proposals; runtime validates them before any proposal is recorded or settled.
- No partial structured JSON or chain-of-thought appears in conversation history or progress UI.
- One unfinished turn is allowed per conversation; other conversations may run concurrently.
- Page reload restores a turn; Barrymore restart marks unfinished work interrupted and does not replay it.
- Journal only semantic lifecycle/stage changes, never per-token ticks.
- Exact token/rate values require provider evidence; approximate live values carry `approximate=true` and render with `≈`.
- Do not add a general job queue, cancellation UI, Capability Acquisition, Intervention, or model-parameter changes.
- Every production change follows RED → GREEN and each independently green task is committed and pushed.

---

## File map

- Create `internal/store/migrations/0018_conversation_turn_runs.sql`: durable TurnRun projection table and one-active-turn index.
- Create `internal/conversation/turn_run.go`: TurnRun domain, lifecycle writes, reads, and recovery.
- Create `internal/conversation/turn_projection.go`: event projectors and row scanning.
- Create `internal/conversation/progress.go`: bounded latest-value progress broker.
- Create `internal/conversation/turn_run_test.go`: lifecycle, atomicity, projection rebuild, recovery, and token aggregation tests.
- Modify `internal/conversation/types.go`: lifecycle constants and payloads.
- Modify `internal/conversation/service.go`: split synchronous `Send` into `BeginTurn` and `ExecuteTurn` while preserving the final `Turn` contract.
- Modify `internal/conversation/deliberation.go`: stage reporting and correct aggregate telemetry.
- Modify `internal/model/model.go`: optional streaming provider contract and timing fields.
- Modify `internal/model/openai.go`: OpenAI SSE accumulation, usage/timings parsing, and progress callbacks.
- Modify `internal/model/openai_test.go`: streaming, telemetry, disconnect, and fallback tests.
- Modify `internal/app/app.go`: application-owned turn execution and shutdown/recovery.
- Create `internal/api/turns.go`: asynchronous turn endpoints and error mapping.
- Modify `internal/api/api.go`: routes and journal/ephemeral SSE multiplexing.
- Modify `internal/api/talkflow_test.go`: async acceptance flow.
- Create `internal/api/turns_test.go`: quick 202, reload-safe reads, conflicts, and disconnect independence.
- Create `internal/api/web/turn-progress.js`: pure formatting and event-correlation helpers.
- Create `internal/api/web/turn-progress.test.mjs`: Node tests for status copy and correlation.
- Modify `internal/api/web/app.js`: submit/restore/render one dynamic progress row.
- Modify `internal/api/web/index.html`: accessible status-row styling only.
- Modify `IMPLEMENTATION_STATUS.md` and `docs/MODERNIZATION_PROGRESS.md`: fresh verified status only.

---

### Task 1: Add the durable TurnRun domain and projection

**Files:**
- Create: `internal/store/migrations/0018_conversation_turn_runs.sql`
- Create: `internal/conversation/turn_run.go`
- Create: `internal/conversation/turn_projection.go`
- Create: `internal/conversation/turn_run_test.go`
- Modify: `internal/conversation/types.go`
- Modify: `internal/conversation/service.go`

**Interfaces:**
- Produces:
  - `BeginTurn(ctx context.Context, conversationID, text string) (TurnRun, error)`
- `ExecuteTurn(ctx context.Context, turnID string) (TurnRun, error)`
  - `TurnRun(ctx context.Context, conversationID, turnID string) (TurnRun, error)`
  - `ActiveTurn(ctx context.Context, conversationID string) (TurnRun, error)`
  - `InterruptUnfinished(ctx context.Context) (int, error)`
  - `ErrTurnActive` and `ErrNoActiveTurn`.
- Preserves: completed `Turn` as `Result`/`result_json`.
- Preserves: synchronous `Send` as a non-HTTP compatibility wrapper around
  `BeginTurn` plus `ExecuteTurn`; the HTTP handler does not call it.

- [ ] **Step 1: Write migration and failing lifecycle tests**

Use a partial unique index to enforce one active turn:

```sql
CREATE TABLE conversation_turn_runs (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    thread_id TEXT,
    user_message_id TEXT NOT NULL REFERENCES messages(id),
    reply_message_id TEXT,
    status TEXT NOT NULL,
    stage TEXT NOT NULL,
    stage_label TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    prompt_ms REAL NOT NULL DEFAULT 0,
    generation_ms REAL NOT NULL DEFAULT 0,
    prompt_tokens_per_second REAL NOT NULL DEFAULT 0,
    generation_tokens_per_second REAL NOT NULL DEFAULT 0,
    total_latency_ms INTEGER NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    result_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    started_at TEXT,
    updated_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE UNIQUE INDEX conversation_turn_runs_one_active
ON conversation_turn_runs(conversation_id)
WHERE status IN ('queued', 'running');
```

Tests must cover atomic owner message + queued turn, same-conversation conflict,
terminal states, completed result round trip, projection rebuild, and recovery.

- [ ] **Step 2: Run lifecycle tests and observe RED**

Run: `go test ./internal/conversation -run 'TestTurnRun|TestBeginTurn' -count=1`

Expected: build failure because TurnRun APIs do not exist.

- [ ] **Step 3: Define domain types and events**

```go
type TurnRun struct {
    ID, ConversationID, ThreadID, UserMessageID, ReplyMessageID string
    Status, Stage, StageLabel, Provider, Model                   string
    PromptTokens, OutputTokens                                  int
    PromptMS, GenerationMS, PromptTokensPerSecond               float64
    GenerationTokensPerSecond                                   float64
    TotalLatencyMS                                               int64
    ErrorCode, ErrorMessage                                      string
    Result                                                       Turn
    CreatedAt, UpdatedAt                                         time.Time
    StartedAt, FinishedAt                                        *time.Time
}

const (
    TurnQueued = "queued"
    TurnRunning = "running"
    TurnCompleted = "completed"
    TurnFailed = "failed"
    TurnInterrupted = "interrupted"
)
```

Add the six event names from the approved design and projector payloads that
contain a full replacement snapshot, keeping rebuild deterministic.

- [ ] **Step 4: Implement BeginTurn atomically**

Generate both IDs before `Journal.Write`, append `conversation.message.recorded`
then `conversation.turn.queued`, and apply both projections in the same SQL
transaction. Translate the partial-index conflict to `ErrTurnActive`.

- [ ] **Step 5: Split execution from submission**

Move the current post-record pipeline into `ExecuteTurn`. It loads the already
recorded owner message, marks the run started, builds context/history, deliberates,
records the final reply and proposal, settles existing outcomes, then records a
completed snapshot containing the full `Turn`. On error, record failed and never
create a partial Barrymore message.

- [ ] **Step 6: Implement reads and recovery**

Scan JSON result only for completed rows. `InterruptUnfinished` writes one
runtime-authored interrupted event per queued/running turn and is idempotent.

- [ ] **Step 7: Register projectors and verify GREEN**

Run: `go test ./internal/conversation -run 'TestTurnRun|TestBeginTurn|TestResearch' -count=1`

Expected: PASS.

- [ ] **Step 8: Run migration/rebuild coverage**

Run: `go test ./internal/store ./internal/projection ./internal/conversation -count=1`

Expected: PASS.

- [ ] **Step 9: Commit and push**

```bash
git add internal/store/migrations/0018_conversation_turn_runs.sql internal/conversation
git commit -m "feat: persist conversation turn runs"
git push origin master
```

---

### Task 2: Stream provider output privately and expose trustworthy telemetry

**Files:**
- Modify: `internal/model/model.go`
- Modify: `internal/model/openai.go`
- Modify: `internal/model/openai_test.go`
- Create: `internal/conversation/progress.go`
- Create: `internal/conversation/progress_test.go`
- Modify: `internal/conversation/service.go`
- Modify: `internal/conversation/deliberation.go`

**Interfaces:**
- Produces:
  - `model.StreamingProvider.CompleteStream(context.Context, model.Request, func(model.Progress)) (model.Response, error)`
  - optional response timing fields `PromptDuration`, `GenerationDuration`, `PromptTokensPerSecond`, `GenerationTokensPerSecond`.
  - `ProgressBroker.Publish`, `Subscribe`, `Latest`, and bounded latest-value delivery.

- [ ] **Step 1: Write failing OpenAI SSE tests**

Serve an SSE fixture with role/content fragments, a finish chunk, final usage and
`timings`, and `[DONE]`. Assert assembled content remains private until return:

```go
got, err := provider.CompleteStream(ctx, req, func(p model.Progress) {
    progress = append(progress, p)
})
if err != nil { t.Fatal(err) }
if got.Content != `{"reply":"ready"}` { t.Fatalf("content=%q", got.Content) }
if got.CompletionTokens != 7 || got.GenerationTokensPerSecond != 13.25 {
    t.Fatalf("telemetry=%+v", got)
}
```

Add cases for provider error events, malformed SSE, disconnect before `[DONE]`,
missing timings, and response size above 16 MiB.

- [ ] **Step 2: Run model tests and observe RED**

Run: `go test ./internal/model -run 'TestOpenAI.*Stream' -count=1`

Expected: build failure because `CompleteStream` does not exist.

- [ ] **Step 3: Add optional streaming contract**

```go
type Progress struct {
    OutputUnits int
    Elapsed     time.Duration
}

type StreamingProvider interface {
    Provider
    CompleteStream(context.Context, Request, func(Progress)) (Response, error)
}
```

Extend `Response` with optional timing fields. Keep `Provider` unchanged so
existing fake/cloud providers compile and retain non-streaming fallback.

- [ ] **Step 4: Implement SSE parsing and final timings**

Set `stream=true` and `stream_options.include_usage=true`, scan `data:` frames
with an enlarged bounded scanner buffer, append only `delta.content`, emit
coalescible progress, require `[DONE]`, and parse final usage/timings. Reuse the
same status/error validation as non-streaming completion.

- [ ] **Step 5: Write broker RED tests**

Assert latest snapshot replacement, non-blocking slow subscriber behaviour,
subscription close, and lookup by turn ID.

- [ ] **Step 6: Implement the bounded progress broker**

Use a mutex-protected latest map plus subscriber channels of size one. Publishing
replaces a stale buffered snapshot before sending the newest value; completion
removes the latest entry only after the durable completed event is committed.

- [ ] **Step 7: Wire stage and provider progress into ExecuteTurn**

Report runtime-owned labels before recall/context/research/provider/finalization.
Use streaming only through the optional interface. Accumulate exact final usage
across all model calls and publish approximate live values with `Approximate=true`.

- [ ] **Step 8: Verify GREEN**

Run: `go test ./internal/model ./internal/conversation -count=1`

Expected: PASS.

- [ ] **Step 9: Commit and push**

```bash
git add internal/model internal/conversation
git commit -m "feat: stream private model telemetry"
git push origin master
```

---

### Task 3: Execute turns under App lifetime and expose async API/SSE

**Files:**
- Modify: `internal/app/app.go`
- Create: `internal/api/turns.go`
- Create: `internal/api/turns_test.go`
- Modify: `internal/api/api.go`
- Modify: `internal/api/talkflow_test.go`

**Interfaces:**
- Produces:
  - `App.BeginTurn(ctx, conversationID, text) (conversation.TurnRun, error)`
  - `POST .../messages -> 202 {turn_id,status}`
  - `GET .../turns/{turn_id}` and `GET .../turns/active`
  - named ephemeral SSE `conversation.turn.progress` without journal sequence.

- [ ] **Step 1: Write failing async API tests**

Use a blocking fake provider and assert POST returns before release, active GET
shows running, handler cancellation does not stop execution, completion GET
contains one final result, second same-conversation submit returns 409, and a
different conversation may start.

- [ ] **Step 2: Run API tests and observe RED**

Run: `go test ./internal/api -run 'TestAsyncTurn|TestReceptionCarries' -count=1`

Expected: POST still blocks/returns 200 and turn routes are missing.

- [ ] **Step 3: Add App-owned launch lifecycle**

Create a cancellable turn context during `App.New`. `BeginTurn` calls
`Talk.BeginTurn`, registers `wg.Add(1)` before returning, and launches
`Talk.ExecuteTurn` using the App context. `Close` prevents new launches, cancels
turns, and waits. `Start` calls `InterruptUnfinished` before accepting work.

- [ ] **Step 4: Add async routes and responses**

Move handlers into `turns.go`. Map `ErrTurnActive` to 409, missing active turn to
404, no provider to 503, and all validated starts to 202.

- [ ] **Step 5: Multiplex ephemeral progress with journal SSE**

Subscribe to both brokers in `/api/v1/stream`. Journal envelopes retain `id` and
normal event type; progress uses only:

```text
event: conversation.turn.progress
data: <progress snapshot JSON>
```

Closing one HTTP stream closes both subscriptions. A slow browser cannot block
the producer.

- [ ] **Step 6: Migrate end-to-end API tests**

Replace synchronous `mustDo` calls with start → poll completed TurnRun → use
`result` for the existing thread/work-order assertions. Keep all prior product
assertions intact.

- [ ] **Step 7: Verify GREEN and race safety**

Run: `go test -race ./internal/app ./internal/api ./internal/conversation -count=1`

Expected: PASS with no WaitGroup/Add race or duplicate completion.

- [ ] **Step 8: Commit and push**

```bash
git add internal/app/app.go internal/api
git commit -m "feat: run conversation turns asynchronously"
git push origin master
```

---

### Task 4: Render one reload-safe activity row

**Files:**
- Create: `internal/api/web/turn-progress.js`
- Create: `internal/api/web/turn-progress.test.mjs`
- Modify: `internal/api/web/app.js`
- Modify: `internal/api/web/index.html`
- Test: `internal/api/web/turn-progress.test.mjs`

**Interfaces:**
- Consumes: async POST, active/completed turn GET, durable turn events, ephemeral progress SSE.
- Produces: exactly one `#turn-progress` element with `role=status` and `aria-live=polite`.

- [ ] **Step 1: Add failing DOM contract assertions**

Create pure helpers with exported functions and tests for stage copy, exact versus
approximate metrics, elapsed formatting, and turn/conversation correlation:

```js
import test from "node:test";
import assert from "node:assert/strict";
import { formatTurnProgress, matchesTurn } from "./turn-progress.js";

test("marks live telemetry approximate", () => {
  const text = formatTurnProgress({
    stage: "provider_generation", elapsed_ms: 17000,
    approximate: true, output_tokens: 84, generation_tokens_per_second: 13.1,
  });
  assert.equal(text, "Формирую ответ · ≈84 токена · ≈13.1 ток/с · 17 с");
});
```

- [ ] **Step 2: Run frontend checks and observe RED**

Run: `node --test internal/api/web/turn-progress.test.mjs`

Expected: FAIL because `turn-progress.js` does not exist.

- [ ] **Step 3: Replace the blocking send flow**

POST the message, retain `turn_id`, disable only that conversation composer,
render the queued row, and let SSE/GET drive completion. Do not call `takeTurn`
until completed `result` is available.

- [ ] **Step 4: Implement deterministic status copy**

Map known stages to concise Russian labels and append elapsed/telemetry only when
present. Prefix live estimates with `≈`; never label chunk count exact.
Import these pure helpers from `turn-progress.js` in `app.js`.

- [ ] **Step 5: Restore after reload and resolve races**

After messages load, request `/turns/active`; render its snapshot before SSE.
Ignore progress for another conversation/turn and ignore stale progress after a
terminal durable event. Reload chat once on completion, call `takeTurn(result)`,
then restore feedback and sidebar state.

- [ ] **Step 6: Add accessible minimal styling**

Reuse muted conversation typography, one line with ellipsis on narrow screens,
no spinner requirement, and the existing reduced-motion rule. Do not add panels,
drawers, token charts, or technical IDs.

- [ ] **Step 7: Verify frontend checks**

Run: `node --check internal/api/web/app.js`

Run: `node --check internal/api/web/feedback.js`

Run: `node --check internal/api/web/turn-progress.js`

Run: `node --test internal/api/web/turn-progress.test.mjs`

Expected: PASS.

- [ ] **Step 8: Commit and push**

```bash
git add internal/api/web/app.js internal/api/web/index.html internal/api/web/turn-progress.js internal/api/web/turn-progress.test.mjs
git commit -m "feat: show live turn progress in conversation"
git push origin master
```

---

### Task 5: Full verification, real browser evidence, and status docs

**Files:**
- Modify: `IMPLEMENTATION_STATUS.md`
- Modify: `docs/MODERNIZATION_PROGRESS.md`

**Interfaces:**
- Consumes: complete vertical slice from Tasks 1–4.
- Produces: fresh repository and runtime evidence, clean synchronized master.

- [ ] **Step 1: Run focused and full automated gates**

Run: `go test ./internal/model ./internal/conversation ./internal/app ./internal/api -count=1`

Run: `make ci`

Run: `node --check internal/api/web/app.js`

Run: `node --check internal/api/web/feedback.js`

Run: `node --test internal/api/web/turn-progress.test.mjs`

Expected: all PASS.

- [ ] **Step 2: Run the real local stack**

Run: `make run`

Expected: Barrymore listens on 127.0.0.1:7717 and the restored Ornith server
reports ready on 127.0.0.1:18080 with the existing launch parameters.

- [ ] **Step 3: Perform browser acceptance**

In the in-app browser, submit a question that triggers research. Verify one row
shows truthful stage changes, elapsed time advances, reload restores the same
turn, final answer appears exactly once, exact final tokens/rate appear, and
feedback persists after reload. Repeat at a narrow viewport and with reduced
motion.

- [ ] **Step 4: Verify restart handling separately**

With a blocking test provider or controlled local request, restart Barrymore and
verify the prior queued/running TurnRun becomes interrupted and is not replayed.
Do not use a side-effecting or paid provider for this gate.

- [ ] **Step 5: Update status documents with only observed claims**

Record exact commands, browser scenario, limitations, and that live values are
approximate until final provider usage. Keep Capability Acquisition explicitly
unimplemented and next.

- [ ] **Step 6: Final gates and clean-tree audit**

Run: `git diff --check`

Run: `make ci`

Run: `git status --short --branch`

Expected: gates PASS and only intended documentation changes remain before commit.

- [ ] **Step 7: Commit and push verification docs**

```bash
git add IMPLEMENTATION_STATUS.md docs/MODERNIZATION_PROGRESS.md
git commit -m "docs: verify durable live turn progress"
git push origin master
```

- [ ] **Step 8: Confirm synchronized final state**

Run: `git rev-parse HEAD`

Run: `git rev-parse origin/master`

Run: `git status --short --branch`

Expected: local and origin HEAD match and `master` is clean.
