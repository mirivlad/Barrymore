-- Долговечный опыт Бэрримора: эпизоды, источники, процедуры, артефакты и
-- явная оценка владельца.
--
-- Fact не дублируется: подтверждённые факты уже живут в memory_items.
-- Этой миграцией память получает метаданные свежести, необходимые для решения
-- «ответить из памяти или перепроверить состояние».

ALTER TABLE memory_items ADD COLUMN stability TEXT NOT NULL DEFAULT 'stable';
ALTER TABLE memory_items ADD COLUMN verified_at TEXT;

CREATE TABLE episodes (
    id               TEXT PRIMARY KEY,
    goal             TEXT NOT NULL,
    scope            TEXT NOT NULL DEFAULT '',
    thread_id        TEXT REFERENCES threads (id),
    conversation_id  TEXT REFERENCES conversations (id),
    status           TEXT NOT NULL CHECK (status IN ('open', 'completed')),
    outcome          TEXT NOT NULL DEFAULT '',
    initial_context  TEXT NOT NULL DEFAULT '{}',
    result           TEXT NOT NULL DEFAULT '',
    verification     TEXT NOT NULL DEFAULT '{}',
    started_at       TEXT NOT NULL,
    finished_at      TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX episodes_status ON episodes (status, updated_at DESC);
CREATE INDEX episodes_thread ON episodes (thread_id, updated_at DESC);
CREATE INDEX episodes_conversation ON episodes (conversation_id, updated_at DESC);

CREATE TABLE experience_sources (
    id          TEXT PRIMARY KEY,
    episode_id  TEXT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    locator     TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    evidence    TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 1.0,
    observed_at TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE INDEX experience_sources_episode ON experience_sources (episode_id, observed_at);

CREATE TABLE procedures (
    id                    TEXT PRIMARY KEY,
    intent                TEXT NOT NULL,
    title                 TEXT NOT NULL,
    scope                 TEXT NOT NULL DEFAULT '',
    source_episode_id     TEXT REFERENCES episodes (id),
    preconditions         TEXT NOT NULL DEFAULT '[]',
    required_capabilities TEXT NOT NULL DEFAULT '[]',
    expected_result       TEXT NOT NULL DEFAULT '',
    verification          TEXT NOT NULL DEFAULT '[]',
    risk_class            TEXT NOT NULL DEFAULT 'read_only',
    status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stale', 'retired')),
    succeeded             INTEGER NOT NULL DEFAULT 0,
    failed                INTEGER NOT NULL DEFAULT 0,
    last_used_at          TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

CREATE INDEX procedures_intent ON procedures (intent, status, updated_at DESC);
CREATE INDEX procedures_source_episode ON procedures (source_episode_id);

-- Шаги отделены от процедуры намеренно: capability можно проверять и искать
-- структурированно, а не разбирать непрозрачный массив JSON. args остаётся JSON,
-- но исполняется только зарегистрированной типизированной capability.
CREATE TABLE procedure_steps (
    procedure_id TEXT NOT NULL REFERENCES procedures (id) ON DELETE CASCADE,
    rollback     INTEGER NOT NULL DEFAULT 0,
    ordinal      INTEGER NOT NULL,
    capability   TEXT NOT NULL,
    purpose      TEXT NOT NULL DEFAULT '',
    args         TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (procedure_id, rollback, ordinal)
) WITHOUT ROWID;

CREATE INDEX procedure_steps_capability ON procedure_steps (capability, procedure_id);

CREATE TABLE experience_feedback (
    id          TEXT PRIMARY KEY,
    episode_id  TEXT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    value       TEXT NOT NULL CHECK (value IN ('like', 'dislike')),
    note        TEXT NOT NULL DEFAULT '',
    actor_type  TEXT NOT NULL,
    actor_id    TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE INDEX experience_feedback_episode ON experience_feedback (episode_id, created_at DESC);

-- Артефакты исследования не обязаны происходить из worker_run, поэтому не
-- подменяют существующую таблицу artifacts из delegation. Здесь только metadata;
-- большие файлы остаются на диске.
CREATE TABLE experience_artifacts (
    id          TEXT PRIMARY KEY,
    episode_id  TEXT NOT NULL REFERENCES episodes (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    path        TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'file',
    size        INTEGER NOT NULL DEFAULT 0,
    checksum    TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE INDEX experience_artifacts_episode ON experience_artifacts (episode_id, created_at);

-- Первый retrieval — структурированные фильтры плюс FTS5. Embeddings могут
-- появиться позже только как индекс, а не как источник истины.
CREATE VIRTUAL TABLE experience_fts USING fts5(
    entity_type UNINDEXED,
    entity_id   UNINDEXED,
    text
);
