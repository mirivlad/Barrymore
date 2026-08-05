-- Каноническое состояние нити.
--
-- 00_PRODUCT_VISION §4 требует, чтобы нить содержала не только сообщения:
-- смысл, текущую позицию сторон, решения, открытые вопросы и условия
-- следующего движения. Позиции, решения и вопросы уже живут отдельными
-- сущностями. Здесь хранится то, чего не хватало: чего мы хотим, где
-- остановились, что мешает, чего ждём и какой шаг следующий.
--
-- Это не пересказ переписки. Пересказ восстанавливается из сообщений и потому
-- каноническим состоянием быть не может (01_PRODUCT_BOUNDARY §2.2). Здесь —
-- то, что Бэрримор утверждает о нити и за что отвечает.
--
-- Ведёт это поле он сам, а владелец правит. Поэтому рядом хранится источник:
-- запись, сделанная после поручения, и запись со слов владельца имеют разный
-- вес, и скрывать разницу нельзя.

ALTER TABLE threads ADD COLUMN canon_goal       TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN canon_situation  TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN canon_next_step  TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN canon_obstacles  TEXT NOT NULL DEFAULT '[]';
ALTER TABLE threads ADD COLUMN canon_waiting    TEXT NOT NULL DEFAULT '[]';
ALTER TABLE threads ADD COLUMN canon_source     TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN canon_updated_at TEXT;
