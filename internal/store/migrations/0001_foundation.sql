-- Этап 1: журнал событий и примитивы предиктивного runtime.
--
-- ADR 0003: события append-only, проекции обновляются транзакционно.
-- ADR 0009: у ожиданий есть next_check_at, их выбирает один scheduler-тик.
-- ADR 0010: seq — глобальная монотонная последовательность для возобновляемого SSE.

-- Журнал событий. Единственный источник аудита и восстановления.
CREATE TABLE events (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    id              TEXT    NOT NULL UNIQUE,
    stream_type     TEXT    NOT NULL,
    stream_id       TEXT    NOT NULL,
    stream_revision INTEGER NOT NULL,
    event_type      TEXT    NOT NULL,
    schema_version  INTEGER NOT NULL DEFAULT 1,
    occurred_at     TEXT    NOT NULL,
    actor_type      TEXT    NOT NULL,
    actor_id        TEXT    NOT NULL DEFAULT '',
    correlation_id  TEXT    NOT NULL DEFAULT '',
    causation_id    TEXT    NOT NULL DEFAULT '',
    idempotency_key TEXT,
    payload         TEXT    NOT NULL,
    UNIQUE (stream_type, stream_id, stream_revision)
);

CREATE UNIQUE INDEX events_idempotency
    ON events (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX events_type_seq ON events (event_type, seq);
CREATE INDEX events_occurred ON events (occurred_at);

-- Головы потоков: оптимистичный контроль конкурентности.
CREATE TABLE streams (
    stream_type TEXT    NOT NULL,
    stream_id   TEXT    NOT NULL,
    revision    INTEGER NOT NULL,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (stream_type, stream_id)
) WITHOUT ROWID;

-- Наблюдения. Наблюдение не становится фактом автоматически (02_DOMAIN_MODEL §3).
CREATE TABLE observations (
    id             TEXT PRIMARY KEY,
    kind           TEXT    NOT NULL,
    subject_type   TEXT    NOT NULL,
    subject_id     TEXT    NOT NULL,
    observed_at    TEXT    NOT NULL,
    recorded_at    TEXT    NOT NULL,
    source         TEXT    NOT NULL,
    source_quality TEXT    NOT NULL,   -- direct | derived | reported
    confidence     REAL    NOT NULL,
    dedupe_key     TEXT,
    payload        TEXT    NOT NULL,
    event_seq      INTEGER
);

CREATE UNIQUE INDEX observations_dedupe
    ON observations (dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX observations_subject ON observations (subject_type, subject_id, observed_at DESC);
CREATE INDEX observations_kind ON observations (kind, observed_at DESC);

-- Снимки наблюдаемого состояния. Снимок — наблюдение с TTL, а не вечный факт.
CREATE TABLE system_snapshots (
    id          TEXT PRIMARY KEY,
    scope       TEXT NOT NULL,          -- system | worker:<id> | provider:<id> | storage
    status      TEXT NOT NULL,
    confidence  REAL NOT NULL,
    observed_at TEXT NOT NULL,
    valid_until TEXT,
    source      TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL
);

CREATE INDEX system_snapshots_scope ON system_snapshots (scope, observed_at DESC);

-- Ожидания. Явные проверяемые предсказания runtime.
CREATE TABLE expectations (
    id                 TEXT PRIMARY KEY,
    subject_type       TEXT    NOT NULL,
    subject_id         TEXT    NOT NULL,
    kind               TEXT    NOT NULL,   -- зарегистрированный вид ожидания
    params             TEXT    NOT NULL,   -- JSON
    basis              TEXT    NOT NULL,
    confidence         REAL    NOT NULL,
    severity_if_missed TEXT    NOT NULL,   -- info | warning | critical
    window_from        TEXT    NOT NULL,
    window_until       TEXT,
    next_check_at      TEXT,
    check_interval_ms  INTEGER NOT NULL DEFAULT 0,
    probe_policy       TEXT    NOT NULL DEFAULT '',
    reaction_policy    TEXT    NOT NULL DEFAULT '',
    status             TEXT    NOT NULL,   -- pending | satisfied | expired | superseded | cancelled
    satisfied_at       TEXT,
    expired_at         TEXT,
    superseded_by      TEXT,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
);

CREATE INDEX expectations_due ON expectations (next_check_at) WHERE status = 'pending';
CREATE INDEX expectations_subject ON expectations (subject_type, subject_id, status);

-- Расхождения. Расхождение не обязательно является ошибкой.
CREATE TABLE discrepancies (
    id             TEXT PRIMARY KEY,
    expectation_id TEXT REFERENCES expectations (id),
    subject_type   TEXT    NOT NULL,
    subject_id     TEXT    NOT NULL,
    kind           TEXT    NOT NULL,
    expected       TEXT    NOT NULL,
    observed       TEXT    NOT NULL,
    severity       TEXT    NOT NULL,
    confidence     REAL    NOT NULL,
    first_seen     TEXT    NOT NULL,
    last_seen      TEXT    NOT NULL,
    occurrences    INTEGER NOT NULL DEFAULT 1,
    status         TEXT    NOT NULL,   -- open | probing | reacting | escalated | resolved | acknowledged
    resolution     TEXT    NOT NULL DEFAULT '',
    dedupe_key     TEXT,
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

-- Пока расхождение живо, повторный сигнал того же класса объединяется с ним,
-- а не создаёт лавину одинаковых уведомлений (03_SYSTEM_ARCHITECTURE §6).
CREATE UNIQUE INDEX discrepancies_active_dedupe
    ON discrepancies (dedupe_key)
    WHERE dedupe_key IS NOT NULL
      AND status IN ('open', 'probing', 'reacting', 'escalated');
CREATE INDEX discrepancies_subject ON discrepancies (subject_type, subject_id, status);

-- Попытки локальных реакций. ADR 0008: бюджет переживает рестарт.
CREATE TABLE reflex_attempts (
    id             TEXT PRIMARY KEY,
    discrepancy_id TEXT    NOT NULL REFERENCES discrepancies (id),
    policy_id      TEXT    NOT NULL,
    attempt_no     INTEGER NOT NULL,
    started_at     TEXT    NOT NULL,
    finished_at    TEXT,
    outcome        TEXT    NOT NULL DEFAULT 'started', -- started | succeeded | failed | denied
    detail         TEXT    NOT NULL DEFAULT '',
    UNIQUE (discrepancy_id, policy_id, attempt_no)
);

CREATE INDEX reflex_attempts_budget
    ON reflex_attempts (discrepancy_id, policy_id, started_at DESC);

-- Probes: ограниченные действия для уменьшения неопределённости.
CREATE TABLE probes (
    id             TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    subject_type   TEXT NOT NULL,
    subject_id     TEXT NOT NULL,
    requested_by   TEXT NOT NULL,   -- scheduler | reflex:<policy> | api | deliberation
    discrepancy_id TEXT REFERENCES discrepancies (id),
    params         TEXT NOT NULL,
    status         TEXT NOT NULL,   -- requested | running | completed | failed | denied
    requested_at   TEXT NOT NULL,
    completed_at   TEXT,
    result         TEXT NOT NULL DEFAULT '',
    error          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX probes_subject ON probes (subject_type, subject_id, requested_at DESC);

-- Решения политик. Пишутся и при разрешении, и при отказе (06_SECURITY §13).
CREATE TABLE policy_decisions (
    id           TEXT PRIMARY KEY,
    decided_at   TEXT NOT NULL,
    actor_type   TEXT NOT NULL,
    actor_id     TEXT NOT NULL DEFAULT '',
    action_class TEXT NOT NULL,   -- read | local_write | workspace_write | process_execute | ...
    subject_type TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    allowed      INTEGER NOT NULL,
    rule         TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    detail       TEXT NOT NULL DEFAULT ''
);

CREATE INDEX policy_decisions_subject ON policy_decisions (subject_type, subject_id, decided_at DESC);
