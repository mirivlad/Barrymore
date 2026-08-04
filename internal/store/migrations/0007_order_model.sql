-- Модель и её стоимость выбираются Бэрримором и должны быть частью поручения:
-- без них нельзя ни объяснить выбор задним числом, ни создать ожидание
-- «на бесплатной модели списаний быть не должно».

ALTER TABLE work_orders ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN model_cost_tier TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN model_rationale TEXT NOT NULL DEFAULT '';
