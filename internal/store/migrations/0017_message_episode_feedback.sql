-- Финальная реплика Бэрримора должна знать, к какому Episode относится её
-- результат. Это производная корреляция, а не cross-projection FK: при
-- rebuild-projections таблицы conversation и experience очищаются разными
-- сервисами, и внешний ключ сделал бы порядок очистки скрытым контрактом.
ALTER TABLE messages
    ADD COLUMN episode_id TEXT NOT NULL DEFAULT '';

CREATE INDEX messages_episode ON messages (episode_id) WHERE episode_id <> '';
