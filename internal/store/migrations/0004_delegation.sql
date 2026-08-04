-- Этап 4: поручения, запуски, артефакты, проверки и подтверждения.
--
-- ADR 0006: идентичность процесса — unit systemd плюс пара (pid, pid_start_ticks).
-- ADR 0007: audit-only держится на изоляции, а не на просьбе к исполнителю.
-- ADR 0011: команды верификации приходят из конфигурации, а не из отчёта worker.

CREATE TABLE work_orders (
    id                    TEXT PRIMARY KEY,
    thread_id             TEXT    NOT NULL REFERENCES threads (id),
    title                 TEXT    NOT NULL,
    goal                  TEXT    NOT NULL,
    why                   TEXT    NOT NULL DEFAULT '',
    state                 TEXT    NOT NULL,
    worker_id             TEXT,
    worker_rationale      TEXT    NOT NULL DEFAULT '',
    trust_level           TEXT    NOT NULL,
    audit_only            INTEGER NOT NULL DEFAULT 1,
    workspace_root        TEXT    NOT NULL DEFAULT '',
    workspace_git_head    TEXT    NOT NULL DEFAULT '',
    workspace_baseline    TEXT    NOT NULL DEFAULT '',
    context_pack_path     TEXT    NOT NULL DEFAULT '',
    context_pack_checksum TEXT    NOT NULL DEFAULT '',
    context_pack_revision INTEGER NOT NULL DEFAULT 0,
    operational_contract  TEXT    NOT NULL DEFAULT '{}',
    acceptance_criteria   TEXT    NOT NULL DEFAULT '[]',
    constraints_json      TEXT    NOT NULL DEFAULT '[]',
    required_artifacts    TEXT    NOT NULL DEFAULT '[]',
    created_at            TEXT    NOT NULL,
    updated_at            TEXT    NOT NULL,
    approved_at           TEXT,
    started_at            TEXT,
    finished_at           TEXT,
    outcome               TEXT    NOT NULL DEFAULT '',
    failure_reason        TEXT    NOT NULL DEFAULT '',
    revision              INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX work_orders_thread ON work_orders (thread_id, created_at DESC);
CREATE INDEX work_orders_state ON work_orders (state);

-- Конкретный запуск исполнителя.
CREATE TABLE worker_runs (
    id               TEXT PRIMARY KEY,
    work_order_id    TEXT    NOT NULL REFERENCES work_orders (id),
    worker_id        TEXT    NOT NULL,
    run_dir          TEXT    NOT NULL,
    unit_name        TEXT    NOT NULL DEFAULT '',
    pid              INTEGER NOT NULL DEFAULT 0,
    pid_start_ticks  INTEGER NOT NULL DEFAULT 0,
    argv             TEXT    NOT NULL DEFAULT '[]',
    sandbox_profile  TEXT    NOT NULL DEFAULT '',
    status           TEXT    NOT NULL,   -- starting | running | exited | cancelled | orphaned | failed
    attachment_state TEXT    NOT NULL DEFAULT 'attached', -- attached | lost | closed
    stdout_offset    INTEGER NOT NULL DEFAULT 0,
    started_at       TEXT    NOT NULL,
    exited_at        TEXT,
    exit_code        INTEGER,
    last_signal_at   TEXT,
    error            TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX worker_runs_order ON worker_runs (work_order_id, started_at DESC);
CREATE INDEX worker_runs_active ON worker_runs (status) WHERE status IN ('starting', 'running');

CREATE TABLE artifacts (
    id            TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES worker_runs (id),
    work_order_id TEXT NOT NULL REFERENCES work_orders (id),
    name          TEXT NOT NULL,
    path          TEXT NOT NULL,
    size          INTEGER NOT NULL DEFAULT 0,
    checksum      TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL DEFAULT 'file',
    collected_at  TEXT NOT NULL,
    UNIQUE (run_id, name)
);

CREATE TABLE verifications (
    id            TEXT PRIMARY KEY,
    work_order_id TEXT NOT NULL REFERENCES work_orders (id),
    run_id        TEXT REFERENCES worker_runs (id),
    kind          TEXT NOT NULL,   -- deterministic | user | second_worker | policy
    name          TEXT NOT NULL,
    status        TEXT NOT NULL,   -- pending | passed | failed | skipped
    detail        TEXT NOT NULL DEFAULT '',
    command       TEXT NOT NULL DEFAULT '[]',
    started_at    TEXT NOT NULL,
    finished_at   TEXT
);

CREATE INDEX verifications_order ON verifications (work_order_id, started_at);

CREATE TABLE approvals (
    id            TEXT PRIMARY KEY,
    work_order_id TEXT REFERENCES work_orders (id),
    action_class  TEXT NOT NULL,
    summary       TEXT NOT NULL,
    scope         TEXT NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL,   -- pending | granted | denied | expired
    requested_at  TEXT NOT NULL,
    decided_at    TEXT,
    decided_by    TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    expires_at    TEXT,
    max_cost      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX approvals_pending ON approvals (status, requested_at);
