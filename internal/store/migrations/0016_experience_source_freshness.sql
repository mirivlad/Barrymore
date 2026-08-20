-- Источник исследования должен помнить не только что было увидено, но и как
-- долго это наблюдение имеет смысл. Иначе после рестарта realtime evidence
-- неотличим от долговечного факта.
ALTER TABLE experience_sources
    ADD COLUMN stability TEXT NOT NULL DEFAULT 'stable'
    CHECK (stability IN ('immutable', 'stable', 'volatile', 'realtime'));
