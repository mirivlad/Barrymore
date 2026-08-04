# AGENTS.md

Прочитай `README.md`, все `docs/*.md`, `docs/adr/*.md` и `IMPLEMENTATION_STATUS.md`.

Проект foundation-first. Не превращай его в чат с памятью или встроенный coding agent.

Основные инварианты:

- Thread, а не Chat/Task, является главным доменом.
- Barrymore identity, память и shared history находятся вне модели.
- Модель возвращает proposals; runtime валидирует.
- External workers сменяемы и не пишут memory напрямую.
- Side effects проходят policy.
- LLM — deliberative layer, а не event loop.
- Observation, Expectation, Discrepancy, Probe и bounded ReflexPolicy принадлежат runtime.
- Audit-only delegation — первый реальный контур.
- Рабочий статус подтверждается tests и Verification.
- Никакого push без прямого разрешения.

Подробные полномочия: `docs/11_ENGINEERING_AUTHORITY.md`.
Стартовый план: `docs/12_BOOTSTRAP_PROMPT.md`.
