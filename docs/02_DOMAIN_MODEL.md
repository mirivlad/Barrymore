# Доменная модель

## 1. Человек и Бэрримор

### Person

Профиль владельца системы. Хранит только явно разрешённые и подтверждённые сведения: `display_name`, `locale`, `timezone`, `interaction_preferences`, policy инициативы и timestamps.

### BarrymoreIdentity

Стабильная конфигурация поведения, независимая от модели: имя и обращение, стиль речи, прямота, степень юмора, допустимая инициативность, правила несогласия, запрещённые формы антропоморфизма, версия системного профиля и набор проверенных примеров поведения.

## 2. Нить

### Thread

Каноническая долгоживущая линия.

Поля: `id`, `title`, `kind`, `state`, `summary`, `origin`, `importance`, `sensitivity`, `workspace_id`, `created_at`, `updated_at`, `last_meaningful_activity_at`, `next_review_at`, `muted_until`, `released_reason`.

Виды: `project`, `idea`, `problem`, `decision`, `conversation`, `research`, `waiting`, `personal`, `relationship`, `other`.

Состояния: `active`, `maturing`, `waiting`, `blocked`, `paused`, `resolved`, `released`, `archived`.

### ThreadPosition

Раздельная позиция участника по нити: `owner` (`person` или `barrymore`), statement, confidence, basis, validity and supersession. Позиции могут не совпадать.

### ThreadLink

Связи: `depends_on`, `conflicts_with`, `derived_from`, `related_to`, `supersedes`, `blocks`, `inspired_by`.

## 3. Сообщения и наблюдения

Conversation является контейнером сессии общения и может затрагивать несколько нитей.

Message не является единственным хранилищем истины. Он содержит автора, текст, модель/revision, связанные нити, ссылки на использованную память, redaction metadata и timestamp.

Observation — типизированное наблюдение из разговора, файла, инструмента или внешнего агента. Наблюдение не становится фактом автоматически.

## 4. Память

### MemoryCandidate

Предложение записать или изменить память: type, content, source refs, scope, confidence, sensitivity, proposed_by, reason и status.

Статусы: `pending`, `accepted`, `rejected`, `expired`, `merged`.

### MemoryItem

Типы: `fact`, `preference`, `decision`, `episode`, `procedure`, `known_failure`, `relationship_history`, `barrymore_lesson`, `agent_performance`, `open_question`.

Каждая запись имеет provenance, scope, confidence, validity interval, sensitivity, version и supersession chain.

### SharedHistoryEntry

Отдельная запись общей истории: что произошло, как это понял пользователь, как это понял Бэрримор, что изменилось, осталось ли разногласие и какие нити затронуты.

## 5. Решения, вопросы и обязательства

Decision фиксирует формулировку, автора, альтернативы, причины, последствия, дату пересмотра и связи.

OpenQuestion хранит вопрос, который не следует превращать в факт или задачу.

Commitment — обещание или обязательство пользователя, Бэрримора, внешнего человека, агента или системы. Бэрримор не создаёт обязательство пользователя без явного согласия.

## 6. Штат и исполнители

### Worker

Абстрактный исполнитель: CLI coding agent, research agent, local/cloud model, deterministic tool, человек или plugin.

Поля: adapter type, display name, installation, auth state, trust level, cost policy, enabled and last probe.

### Capability

Например: repository audit, code editing, tests, browser interaction, web research, image understanding, long context, Russian, structured output, tool use, offline operation.

### AvailabilitySnapshot

Содержит status, confidence, observed_at, valid_until, source, quota summary, cost summary and reason.

Статусы: `available`, `likely_available`, `unknown`, `quota_exhausted`, `auth_required`, `payment_confirmation_required`, `offline`, `broken`.

## 7. Поручение

### WorkOrder

Формализованное поручение: нить, цель, ограничения, критерии готовности, разрешения, бюджет, выбранный worker, основание выбора, context pack revision, workspace policy, verification plan и operational contract.

Operational contract задаёт ожидаемые milestones, heartbeat policy, допустимые периоды тишины, stop conditions, разрешённые probes и bounded recovery actions.

Состояния: `draft`, `proposed`, `approved`, `preparing`, `running`, `paused`, `awaiting_user`, `verifying`, `completed`, `failed`, `cancelled`.

### ContextPack

Версионируемый пакет цели, истории нити, решений, ограничений, состояния workspace, прошлых попыток, acceptance criteria, permissions и формата отчёта.

### WorkerRun, Artifact, Verification

WorkerRun — конкретный запуск. Artifact — отчёт, diff, patch, commit, лог тестов или иной результат. Verification может быть deterministic, user, second worker, policy или composite.

`completed` разрешён только после требуемых Verification.

## 8. Операционное состояние и предиктивный контур

### SystemStateSnapshot

Версионированный снимок наблюдаемого состояния: model provider, context budget, workers, process liveness, очереди, storage, network, configured roots, активные approvals, freshness и качество источника. Snapshot является наблюдением с TTL, а не вечным фактом.

### Expectation

Явное проверяемое ожидание, связанное с Thread, WorkOrder, WorkerRun, Commitment или системным ресурсом.

Поля: `subject`, `condition`, `time_window`, `confidence`, `basis`, `severity_if_missed`, `probe_policy`, `reaction_policy`, `status`, `satisfied_at`, `expired_at`.

Примеры: heartbeat в течение интервала; отсутствие writes в audit-only run; появление отчёта после process exit; обновление quota snapshot до выбора worker.

### Discrepancy

Зафиксированное расхождение между ожиданием и наблюдением: expected, observed, confidence, severity, first_seen, last_seen, attempts, status и resolution.

Расхождение не обязательно является ошибкой. Оно может означать устаревшее ожидание, неполное наблюдение, штатную задержку, сбой или изменение внешнего мира.

### Probe

Ограниченное действие для уменьшения неопределённости: проверить PID, прочитать status, получить `git diff`, обновить availability, запустить deterministic check или запросить structured report. Probe не должен незаметно превращаться в рабочее side effect.

### ReflexPolicy

Детерминированная реакция на класс расхождений: guards, allowed actions, max attempts, cooldown, escalation target, audit requirements и required policy scope.

Примеры: повторить идемпотентный probe; восстановить SSE attachment; поставить run на паузу при запрещённом write; сохранить checkpoint; пометить snapshot stale; эскалировать пользователю.

## 9. Политики и подтверждения

Policy — правило доступа, стоимости, приватности, инициативы, делегирования, probes или reflex actions.

Approval содержит точный scope, срок, объект, ожидаемый side effect, requester и approver. ReflexPolicy не расширяет Approval и не создаёт разрешения задним числом.

## 10. Event

Все значимые изменения представлены append-only событиями. Проекции ускоряют чтение, но событие остаётся источником аудита и восстановления. Наблюдения, ожидания, расхождения, probes, локальные реакции и эскалации также представлены событиями.
