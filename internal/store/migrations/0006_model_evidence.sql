-- Каталог моделей меняется: провайдеры вводят и убирают бесплатные модели,
-- переименовывают их и меняют цену. Поэтому стоимость модели — наблюдение
-- с уверенностью и сроком годности, а не постоянное свойство.
--
-- Пометка в названии ("-free", ":free") даёт лишь слабое основание.
-- Сильное основание даёт фактическая стоимость выполненного запуска.

ALTER TABLE worker_models ADD COLUMN confidence REAL NOT NULL DEFAULT 0.5;
-- last_cost — стоимость, сообщённая исполнителем в последнем запуске.
-- Отрицательное значение означает «не наблюдалась».
ALTER TABLE worker_models ADD COLUMN last_cost REAL NOT NULL DEFAULT -1;
ALTER TABLE worker_models ADD COLUMN verified_at TEXT;

-- Когда каталог моделей исполнителя обновлялся целиком.
ALTER TABLE workers ADD COLUMN models_refreshed_at TEXT;
