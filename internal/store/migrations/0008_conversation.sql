-- Этап 6-7: разговор и кандидаты в память.
--
-- 01_PRODUCT_BOUNDARY §2.2: история сообщений не является каноническим
-- состоянием. Значимое из разговора попадает в нить и в память только как
-- видимый кандидат, который владелец принимает или отклоняет.

CREATE TABLE conversations (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    thread_id  TEXT REFERENCES threads (id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX conversations_thread ON conversations (thread_id, updated_at DESC);

CREATE TABLE messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT    NOT NULL REFERENCES conversations (id),
    thread_id       TEXT REFERENCES threads (id),
    role            TEXT    NOT NULL,   -- person | barrymore
    content         TEXT    NOT NULL,
    -- provider и model фиксируют, чем именно был получен ответ:
    -- смена модели не должна незаметно менять историю.
    provider        TEXT    NOT NULL DEFAULT '',
    model           TEXT    NOT NULL DEFAULT '',
    prompt_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    latency_ms      INTEGER NOT NULL DEFAULT 0,
    -- retrieval_trace объясняет, что было подано модели в контекст.
    retrieval_trace TEXT    NOT NULL DEFAULT '[]',
    created_at      TEXT    NOT NULL
);

CREATE INDEX messages_conversation ON messages (conversation_id, created_at);
CREATE INDEX messages_thread ON messages (thread_id, created_at);

-- Кандидат в память: предложение записать, а не запись.
CREATE TABLE memory_candidates (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,   -- fact | preference | decision | open_question | ...
    content         TEXT NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    proposed_by     TEXT NOT NULL,   -- barrymore | person
    thread_id       TEXT REFERENCES threads (id),
    conversation_id TEXT REFERENCES conversations (id),
    message_id      TEXT REFERENCES messages (id),
    sensitivity     TEXT NOT NULL DEFAULT 'normal',
    confidence      REAL NOT NULL DEFAULT 0.5,
    status          TEXT NOT NULL,   -- pending | accepted | rejected | expired | merged
    decided_at      TEXT,
    decided_by      TEXT NOT NULL DEFAULT '',
    decision_note   TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);

CREATE INDEX memory_candidates_status ON memory_candidates (status, created_at DESC);

-- Подтверждённая память. Появляется только из принятого кандидата.
CREATE TABLE memory_items (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    content      TEXT NOT NULL,
    -- provenance хранит происхождение: без него запись нельзя оспорить.
    provenance   TEXT NOT NULL DEFAULT '{}',
    candidate_id TEXT REFERENCES memory_candidates (id),
    thread_id    TEXT REFERENCES threads (id),
    sensitivity  TEXT NOT NULL DEFAULT 'normal',
    confidence   REAL NOT NULL DEFAULT 0.5,
    valid_from   TEXT NOT NULL,
    valid_until  TEXT,
    superseded_by TEXT,
    revoked_at   TEXT,
    revoke_reason TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

CREATE INDEX memory_items_active ON memory_items (type) WHERE revoked_at IS NULL;
CREATE INDEX memory_items_thread ON memory_items (thread_id);

-- Полнотекстовый поиск по подтверждённой памяти.
CREATE VIRTUAL TABLE memory_fts USING fts5(
    content,
    content='memory_items',
    content_rowid='rowid'
);
