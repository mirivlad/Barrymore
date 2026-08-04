-- Этап 3: штат.
--
-- ADR 0002: workers внешние и сменяемые. Реестр хранит наблюдаемые сведения
-- об установках, а не обещания из документации.
--
-- Снимки доступности живут в system_snapshots со scope 'worker:<id>':
-- у доступности есть TTL и уверенность, как у любого другого наблюдения.

CREATE TABLE workers (
    id              TEXT PRIMARY KEY,
    adapter_id      TEXT    NOT NULL,
    display_name    TEXT    NOT NULL,
    executable_path TEXT    NOT NULL DEFAULT '',
    version         TEXT    NOT NULL DEFAULT '',
    trust_level     TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    auth_state      TEXT    NOT NULL DEFAULT 'unknown',
    cost_policy     TEXT    NOT NULL DEFAULT 'unknown',
    discovered_at   TEXT    NOT NULL,
    last_probe_at   TEXT,
    notes           TEXT    NOT NULL DEFAULT '',
    UNIQUE (adapter_id, executable_path)
);

CREATE INDEX workers_adapter ON workers (adapter_id);

-- Возможность подтверждается основанием, а не объявляется.
CREATE TABLE worker_capabilities (
    id          TEXT PRIMARY KEY,
    worker_id   TEXT NOT NULL REFERENCES workers (id),
    capability  TEXT NOT NULL,
    evidence    TEXT NOT NULL,   -- declared | probe | execution | user
    confidence  REAL NOT NULL,
    observed_at TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    UNIQUE (worker_id, capability, evidence)
);

CREATE INDEX worker_capabilities_worker ON worker_capabilities (worker_id);
