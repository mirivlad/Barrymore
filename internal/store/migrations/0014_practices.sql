-- Практики: что известно о способах работы из опыта.
--
-- ADR 0020. Это не статистика ради статистики: запись применений показывается
-- модели при выборе способа, а три неудачи подряд снимают способ с применения.
-- Поэтому здесь же хранится причина — «прежний способ больше не годится»
-- должно быть высказано словами, иначе это не знание, а молчаливый сбой.

CREATE TABLE practices (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    ref          TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    question     TEXT NOT NULL DEFAULT '',
    applied      INTEGER NOT NULL DEFAULT 0,
    succeeded    INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    streak       INTEGER NOT NULL DEFAULT 0,
    avg_ms       INTEGER NOT NULL DEFAULT 0,
    last_at      TEXT NOT NULL DEFAULT '',
    last_outcome TEXT NOT NULL DEFAULT '',
    last_note    TEXT NOT NULL DEFAULT '',
    stale        INTEGER NOT NULL DEFAULT 0,
    stale_why    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX practices_kind ON practices (kind, applied);
