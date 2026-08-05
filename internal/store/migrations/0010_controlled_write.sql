-- Контролируемая запись: исполнитель работает в копии каталога, а изменения
-- доходят до владельца только по его отдельному решению
-- (05_STAFF_AND_DELEGATION §10: слияние — отдельное действие).
--
-- Копия хранится в каталоге запуска, поэтому здесь только путь к ней, ветка
-- и коммит с состоянием «до». Всё, что отличается от этого коммита, сделал
-- исполнитель — и ничего больше.

ALTER TABLE work_orders ADD COLUMN work_copy_path TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN work_copy_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN work_copy_baseline TEXT NOT NULL DEFAULT '';

-- Состояние изменений отдельно от состояния поручения: поручение может быть
-- выполнено, а изменения — ещё не рассмотрены.
--   none | collected | applied | discarded
ALTER TABLE work_orders ADD COLUMN change_state TEXT NOT NULL DEFAULT 'none';
ALTER TABLE work_orders ADD COLUMN change_summary TEXT NOT NULL DEFAULT '{}';
ALTER TABLE work_orders ADD COLUMN change_decided_at TEXT;
ALTER TABLE work_orders ADD COLUMN change_decision_note TEXT NOT NULL DEFAULT '';
