# ADR 0002: Специализированные исполнители внешние и сменяемые

Status: Accepted

## Context

Claude Code, Codex, OpenCode, Pi, Hermes и будущие инструменты развиваются быстрее собственного встроенного coding agent.

## Decision

Бэрримор подключает workers через adapter API. Runtime владеет контекстом, policies, workspace, наблюдением, проверкой и историей. Worker владеет только исполнением WorkOrder.

## Consequences

Adapters first-class; worker identity не смешивается с Barrymore identity; доступность/стоимость наблюдаются отдельно; WorkOrder повторяется другим worker; первый slice использует реальный CLI.
