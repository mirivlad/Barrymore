-- Долговечное выполнение хода разговора. Реплики остаются историей,
-- Episode — опытом, а TurnRun отвечает только на вопрос «что происходит сейчас».
CREATE TABLE conversation_turn_runs (
    id                           TEXT PRIMARY KEY,
    conversation_id              TEXT NOT NULL REFERENCES conversations (id),
    thread_id                    TEXT,
    user_message_id              TEXT NOT NULL REFERENCES messages (id),
    reply_message_id             TEXT,
    status                       TEXT NOT NULL,
    stage                        TEXT NOT NULL,
    stage_label                  TEXT NOT NULL DEFAULT '',
    provider                     TEXT NOT NULL DEFAULT '',
    model                        TEXT NOT NULL DEFAULT '',
    prompt_tokens                INTEGER NOT NULL DEFAULT 0,
    output_tokens                INTEGER NOT NULL DEFAULT 0,
    prompt_ms                    REAL NOT NULL DEFAULT 0,
    generation_ms                REAL NOT NULL DEFAULT 0,
    prompt_tokens_per_second     REAL NOT NULL DEFAULT 0,
    generation_tokens_per_second REAL NOT NULL DEFAULT 0,
    total_latency_ms             INTEGER NOT NULL DEFAULT 0,
    error_code                   TEXT NOT NULL DEFAULT '',
    error_message                TEXT NOT NULL DEFAULT '',
    result_json                  TEXT NOT NULL DEFAULT '{}',
    created_at                   TEXT NOT NULL,
    started_at                   TEXT,
    updated_at                   TEXT NOT NULL,
    finished_at                  TEXT
);

CREATE INDEX conversation_turn_runs_recent
    ON conversation_turn_runs (conversation_id, created_at DESC);

CREATE UNIQUE INDEX conversation_turn_runs_one_active
    ON conversation_turn_runs (conversation_id)
    WHERE status IN ('queued', 'running');
