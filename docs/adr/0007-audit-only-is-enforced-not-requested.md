# ADR 0007: Audit-only обеспечивается изоляцией, а не просьбой

Status: Accepted
Date: 2026-08-04

## Context

`docs/10_ACCEPTANCE_SCENARIOS.md` F требует, чтобы попытка записи при read-only
scope блокировалась. Спецификация формулирует запрет, но не механизм. Полагаться
на послушание worker запрещено `docs/06_SECURITY_AND_TRUST.md` §2.1.

## Decision

Три слоя, в порядке приоритета:

1. **Превентивный, внешний.** `bwrap --ro-bind <workspace> <workspace>` плюс
   отдельный writable tmpfs под рабочие файлы агента. Настоящий барьер ядра.
2. **Defense in depth, внутренний.** `codex --sandbox read-only`. Второй барьер
   на случай ошибки в профиле bwrap.
3. **Детективный.** Verification: снимок `git status --porcelain`, `git diff --check`,
   список mtime/размеров до и после запуска. Обнаруживает то, что прошло мимо 1 и 2.

Обнаружение записи слоем 3 при работающих слоях 1–2 является дефектом изоляции
и создаёт Discrepancy высокой severity, а не просто провал WorkOrder.

## Consequences

- `bwrap` становится обязательной зависимостью для audit-only (подтверждён host audit);
- профиль изоляции — часть WorkOrder, а не глобальная настройка;
- те же слои переиспользуются для controlled write с заменой `--ro-bind` на `--bind` worktree;
- отсутствие bwrap на хосте — честная блокировка запуска, а не тихий downgrade до слоя 3.
