-- Этап 3, продолжение: каталог моделей исполнителей.
--
-- Бэрримор выбирает не только исполнителя, но и модель внутри него, и по
-- умолчанию предпочитает бесплатные. Стоимость наблюдается, а не выдумывается:
-- у каждой записи есть источник и основание.

CREATE TABLE worker_models (
    id          TEXT PRIMARY KEY,
    worker_id   TEXT    NOT NULL REFERENCES workers (id),
    model_ref   TEXT    NOT NULL,   -- строка, которую понимает сам исполнитель
    provider    TEXT    NOT NULL DEFAULT '',
    name        TEXT    NOT NULL DEFAULT '',
    cost_tier   TEXT    NOT NULL,   -- free | subscription | paid | unknown
    source      TEXT    NOT NULL,   -- cli-list | config | run-observed | manual
    evidence    TEXT    NOT NULL DEFAULT '',
    is_default  INTEGER NOT NULL DEFAULT 0,
    observed_at TEXT    NOT NULL,
    UNIQUE (worker_id, model_ref)
);

CREATE INDEX worker_models_worker ON worker_models (worker_id, cost_tier);
CREATE INDEX worker_models_free ON worker_models (cost_tier) WHERE cost_tier = 'free';

-- Класс исполнителя: повседневная работа против мастера по вызову.
ALTER TABLE workers ADD COLUMN class TEXT NOT NULL DEFAULT 'routine';
-- Модель, выбранная владельцем вручную; пустая строка означает выбор runtime.
ALTER TABLE workers ADD COLUMN preferred_model TEXT NOT NULL DEFAULT '';
