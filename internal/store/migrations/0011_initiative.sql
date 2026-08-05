-- Инициатива: обращения Бэрримора по наблюдаемым поводам.
--
-- 07_USER_EXPERIENCE §4: каждое обращение отвечает на вопрос «почему сейчас».
-- Поэтому `why` не может быть пустым — уведомления без причины не бывает.
--
-- dedupe_key не даёт обратиться дважды по одному поводу: два письма об одном
-- и том же — уже назойливость, а не забота.

CREATE TABLE initiative_notices (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    subject_type TEXT NOT NULL DEFAULT '',
    subject_id   TEXT NOT NULL DEFAULT '',
    level        TEXT NOT NULL,
    title        TEXT NOT NULL,
    why          TEXT NOT NULL,
    status       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    deliver_at   TEXT NOT NULL,
    delivered_at TEXT,
    read_at      TEXT,
    dedupe_key   TEXT NOT NULL UNIQUE
);

CREATE INDEX initiative_notices_status ON initiative_notices (status, deliver_at);
CREATE INDEX initiative_notices_subject ON initiative_notices (subject_type, subject_id);
