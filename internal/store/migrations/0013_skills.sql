-- Умения Бэрримора: то, что он делает сам, без внешнего исполнителя.
--
-- ADR 0019. Встроенные умения живут в коде — таблица нужна для освоенных
-- и для снятых с применения. Причина снятия хранится здесь же: «прежний
-- способ больше не годится» — это знание, и оно должно пережить перезапуск,
-- иначе умение вернулось бы само.

CREATE TABLE skills (
    id           TEXT PRIMARY KEY,
    title        TEXT NOT NULL DEFAULT '',
    question     TEXT NOT NULL DEFAULT '',
    needs_target INTEGER NOT NULL DEFAULT 0,
    steps        TEXT NOT NULL DEFAULT '[]',
    origin       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    retired_why  TEXT NOT NULL DEFAULT ''
);

-- Применения. Дешевизна умения — не обещание, а измеряемая величина:
-- took_ms хранится, чтобы сравнение с поручением опиралось на факты.
CREATE TABLE skill_runs (
    id              TEXT PRIMARY KEY,
    skill_id        TEXT NOT NULL,
    skill_title     TEXT NOT NULL DEFAULT '',
    target          TEXT NOT NULL DEFAULT '',
    thread_id       TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    answer          TEXT NOT NULL DEFAULT '',
    failure         TEXT NOT NULL DEFAULT '',
    steps           TEXT NOT NULL DEFAULT '[]',
    started_at      TEXT NOT NULL,
    finished_at     TEXT NOT NULL,
    took_ms         INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX skill_runs_skill ON skill_runs (skill_id, started_at);
CREATE INDEX skill_runs_thread ON skill_runs (thread_id, started_at);
