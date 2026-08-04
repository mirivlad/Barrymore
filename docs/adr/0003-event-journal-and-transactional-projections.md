# ADR 0003: Append-only события и транзакционные проекции

Status: Accepted

## Context

Нити, память, поручения и внешние процессы должны переживать рестарт, поддерживать аудит и не зависеть от chat transcript.

## Decision

Значимые изменения записываются append-only events. Текущее состояние хранится в транзакционно обновляемых SQLite projections.

## Consequences

Schema versioning, migrations/replay tests, correlation/causation, redaction до записи и recovery reconciliation обязательны.
