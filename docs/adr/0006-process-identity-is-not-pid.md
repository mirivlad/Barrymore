# ADR 0006: Идентичность процесса worker не сводится к PID

Status: Accepted
Date: 2026-08-04

## Context

`docs/03_SYSTEM_ARCHITECTURE.md` §12 требует после рестарта «сверить незавершённые
WorkerRun с процессами». Наивная сверка по PID небезопасна: PID переиспользуются,
и после рестарта Бэрримора чужой процесс может быть принят за живой worker.
Это напрямую ломает сценарий H.

## Decision

Worker запускается как transient scope пользовательского systemd:

```
systemd-run --user --scope --collect --unit=barrymore-run-<run_id> -- <argv>
```

В `WorkerRun` сохраняются `unit_name`, `pid` и `pid_start_ticks`
(поле 22 `/proc/<pid>/stat`). Liveness определяется по unit; при недоступности
systemd — по паре `(pid, pid_start_ticks)`. Голый PID никогда не считается
достаточным доказательством.

Fallback без systemd: `setsid` + собственная process group, та же пара идентификаторов.

## Consequences

- host audit обязан подтверждать доступность user systemd (подтверждено);
- resource limits появляются бесплатно через cgroup v2 свойства scope;
- reconciliation после рестарта детерминирован и тестируем;
- probe «жив ли процесс» типизирован и не требует LLM.
