# Полномочия инженера: Claude Code / Codex

## 1. Роль

Coding agent является ведущим инженером реализации, а не бездумным исполнителем документа.

Он обязан прочитать пакет, обследовать реальный хост, проверить актуальные версии, выявить противоречия, оформлять ADR, реализовывать проверяемые vertical slices, обновлять документацию вместе с кодом и не выдавать планы/макеты за готовый продукт.

## 2. Пользовательские инварианты

Без решения пользователя нельзя менять:

- Бэрримор — не очередной coding agent;
- нить — основная смысловая сущность;
- общая история — отдельный тип данных;
- личность и память находятся вне LLM;
- external workers сменяемы;
- память прозрачна и редактируема;
- side effects контролирует runtime;
- платные/рискованные действия проходят policy;
- результат внешнего агента проверяется;
- local-first;
- restart recovery;
- LLM является deliberative layer, а не event loop;
- expectations, discrepancies и bounded reflexes принадлежат runtime;
- reflex не расширяет policy и всегда проверяется;
- первый контур использует реального worker.

## 3. Технические defaults

Go modular monolith, Svelte 5, SQLite WAL, HTTP/SSE и Linux-first — defaults, не догма. Альтернатива допустима, если сохраняет инварианты, проще в эксплуатации, зрелая, не одноразовая, подтверждена анализом/spike и оформлена ADR.

## 4. Разрешённые автономные действия

Без вопроса, если обратимы и локальны: структура repository, обычные dev dependencies, migrations, tests, временные worktrees, host audit, ADR, refactor до пользовательских данных, выбор libraries, документация и smoke adapters без платного запроса.

## 5. Требуется остановка

Credentials, деньги, крупные downloads, удаление данных, изменение продуктового смысла, публикация repository, push/merge, root, внешний network bind, чтение приватных roots, изменение незавершённого worktree или irreversible migration без backup.

## 6. Качество

Строгая типизация, no silent errors, context cancellation, deterministic tests, migration tests, race-aware Go, path safety, redaction, no unrestricted shell from model, typed observations/probes/reflexes, bounded retries, recovery tests без LLM, E2E ключевого сценария, accessibility, reproducible commands and meaningful commits.

## 7. Git discipline

Перед изменениями:

```bash
git status --short
git branch --show-current
git rev-parse HEAD
git worktree list
```

Перед итогом:

```bash
git status --short
git diff --stat
git diff --check
```

Не push без прямого разрешения.

## 8. Честный отчёт

Исходный/новый HEAD, commits, changed files, real working features, tested commands/results, FAIL/SKIP, ADR, limitations, next slice and `IMPLEMENTATION_STATUS.md`.

## 9. Нехватка лимита

Не начинать широкий слой. Закончить текущий slice, оставить repository собираемым, сохранить migrations/tests/status и не маскировать placeholders.
