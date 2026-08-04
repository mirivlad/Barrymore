-- Этап 2: нити.
--
-- ADR 0001: Thread — главная смысловая сущность. Позиции пользователя и
-- Бэрримора хранятся раздельно и могут не совпадать; сообщения не являются
-- каноническим состоянием.

CREATE TABLE threads (
    id                          TEXT PRIMARY KEY,
    title                       TEXT    NOT NULL,
    kind                        TEXT    NOT NULL,
    state                       TEXT    NOT NULL,
    summary                     TEXT    NOT NULL DEFAULT '',
    origin                      TEXT    NOT NULL DEFAULT '',
    importance                  TEXT    NOT NULL DEFAULT 'normal',
    sensitivity                 TEXT    NOT NULL DEFAULT 'normal',
    workspace_id                TEXT,
    created_at                  TEXT    NOT NULL,
    updated_at                  TEXT    NOT NULL,
    last_meaningful_activity_at TEXT,
    next_review_at              TEXT,
    muted_until                 TEXT,
    released_reason             TEXT    NOT NULL DEFAULT '',
    revision                    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX threads_state ON threads (state, updated_at DESC);
CREATE INDEX threads_kind ON threads (kind);

-- Позиция участника по нити. Позиции сторон различаются намеренно.
CREATE TABLE thread_positions (
    id            TEXT PRIMARY KEY,
    thread_id     TEXT NOT NULL REFERENCES threads (id),
    owner         TEXT NOT NULL,           -- person | barrymore
    statement     TEXT NOT NULL,
    confidence    REAL NOT NULL,
    basis         TEXT NOT NULL DEFAULT '',
    valid_from    TEXT NOT NULL,
    valid_until   TEXT,
    superseded_by TEXT,
    created_at    TEXT NOT NULL
);

CREATE INDEX thread_positions_current
    ON thread_positions (thread_id, owner, valid_from DESC);

-- Решение с причинами и альтернативами.
CREATE TABLE thread_decisions (
    id           TEXT PRIMARY KEY,
    thread_id    TEXT NOT NULL REFERENCES threads (id),
    statement    TEXT NOT NULL,
    decided_by   TEXT NOT NULL,
    rationale    TEXT NOT NULL DEFAULT '',
    alternatives TEXT NOT NULL DEFAULT '[]',
    consequences TEXT NOT NULL DEFAULT '',
    review_at    TEXT,
    decided_at   TEXT NOT NULL
);

CREATE INDEX thread_decisions_thread ON thread_decisions (thread_id, decided_at DESC);

-- Открытый вопрос: то, что не следует превращать ни в факт, ни в задачу.
CREATE TABLE thread_questions (
    id         TEXT PRIMARY KEY,
    thread_id  TEXT NOT NULL REFERENCES threads (id),
    question   TEXT NOT NULL,
    asked_by   TEXT NOT NULL,
    status     TEXT NOT NULL,   -- open | answered | dropped
    answer     TEXT NOT NULL DEFAULT '',
    opened_at  TEXT NOT NULL,
    closed_at  TEXT
);

CREATE INDEX thread_questions_thread ON thread_questions (thread_id, status);

CREATE TABLE thread_links (
    id         TEXT PRIMARY KEY,
    from_id    TEXT NOT NULL REFERENCES threads (id),
    to_id      TEXT NOT NULL REFERENCES threads (id),
    kind       TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (from_id, to_id, kind)
);

CREATE INDEX thread_links_to ON thread_links (to_id);
