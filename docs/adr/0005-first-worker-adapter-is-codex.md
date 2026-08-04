# ADR 0005: Первый worker adapter — codex

Status: Accepted
Date: 2026-08-04

## Context

`docs/08_API_AND_EVENTS.md` §5 приводит пример манифеста `claude-code`, README и
`docs/07_USER_EXPERIENCE.md` §3 иллюстрируют сценарии с Claude. При этом
`docs/12_BOOTSTRAP_PROMPT.md` предписывает выбирать первый worker по фактически
установленным CLI и прямо запрещает hardcode продукта под Claude Code.

Host audit показал: `claude` в PATH отсутствует. Установлены и авторизованы
codex 0.146.0, opencode 1.17.9, pi 0.80.10, qwen 0.21.0, hermes.

## Decision

Первым полноценным adapter становится **codex**. Основание — `codex exec`
предоставляет все точки контроля, требуемые доменом: `--json` (JSONL-события),
`--sandbox read-only`, `-C`, `--output-schema`, `-o`, `--ephemeral`,
`--ignore-user-config`.

opencode, pi, qwen, hermes на этапе 3 получают только discovery и version probe.
Манифест `claude-code` остаётся примером в документации и активируется, если
`claude` появится в PATH.

## Consequences

- пример манифеста в `08_API_AND_EVENTS.md` не считается реализуемым требованием;
- парсер событий codex версионируется отдельно и покрывается golden-фикстурами;
- смена первого adapter не требует изменения домена — только новый манифест;
- Бэрримор не читает `~/.codex/auth.json`; дочерний процесс аутентифицируется сам.
