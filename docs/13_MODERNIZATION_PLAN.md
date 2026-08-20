# План модернизации Бэрримора

Status: Active roadmap
Date: 2026-08-20

Этот документ фиксирует следующий цикл развития Бэрримора после первого живого
запуска с локальной моделью. Он не отменяет принятые ADR и не заменяет доменную
модель. Новый контур расширяет уже существующие Observation, Probe,
Deliberation, Memory, Practice, Verification и Policy.

## 1. Цель

Бэрримор должен быть полноценным локальным дворецким, который:

- работает без внешних AI-агентов и становится сильнее, если они доступны;
- если не знает ответа, сначала пытается его выяснить безопасными способами;
- помнит не только факты, но и успешные способы снова получить актуальный ответ
  или привести систему к нужному состоянию;
- отличает долговечный факт от изменяемого состояния;
- при изменениях сначала исследует текущее состояние, затем планирует,
  оценивает риск, действует, проверяет результат и только после этого считает
  задачу выполненной;
- учитывает явную оценку владельца: `like` усиливает хороший опыт, `dislike`
  помечает эпизод как требующий пересмотра; отсутствие оценки ничего не значит;
- запускается как самостоятельный продукт: всё необходимое для runtime идёт в
  комплекте, пользователю достаточно положить GGUF-модель в каталог данных и
  подтвердить/выбрать её при первом запуске.

## 2. Общий контур

```text
Goal / question
      ↓
Recall relevant memory and prior episodes
      ↓
Freshness / applicability check
      ↓
Inspect current reality
      ↓
Research / plan
      ↓
Policy + risk
      ↓
Observe ─ or ─ Act
      ↓
Verify
      ↓
Respond
      ↓
Feedback
      ↓
Learn: facts + procedures + failures + provenance
```

Это один контур для вопросов и действий. Различается только наличие побочного
эффекта.

## 3. Память: четыре долгоживущих сущности

### Fact

Что известно. Минимальные свойства:

- content;
- scope;
- provenance;
- confidence;
- learned_at / verified_at;
- stability;
- valid_until / supersedes.

Классы стабильности первой версии:

- `immutable` — практически не меняется;
- `stable` — меняется редко;
- `volatile` — может измениться между обращениями;
- `realtime` — старое значение нельзя использовать как текущий ответ.

### Procedure

Как что-либо снова выяснить или сделать. Хранит:

- intent;
- preconditions;
- ordered steps;
- required capabilities;
- expected result;
- verification;
- rollback, если есть side effect;
- risk class;
- success/failure history;
- user feedback.

Procedure не является записью shell-команд из ответа модели. Исполнимые шаги
ссылаются только на типизированные runtime capabilities или контролируемые
worker adapters.

### Episode

Один конкретный случай работы:

- goal;
- initial context;
- recalled experience;
- observations;
- attempts;
- sources;
- actions;
- result;
- verification;
- feedback;
- extracted facts/procedures/failures.

Episode является основной единицей обучения. Разговор остаётся разговором, а
эпизод отвечает на вопрос «что мы пытались сделать и чему научились».

### Feedback

Явная и неявная оценка результата.

- `like` — сильный положительный сигнал;
- `dislike` — сильный отрицательный сигнал;
- отсутствие оценки — нейтрально и ничего не меняет;
- текстовая коррекция после оценки может уточнить причину успеха/ошибки;
- objective verification и satisfaction владельца хранятся раздельно.

Лайк не превращает неверный факт в истину. Успешная техническая проверка не
делает плохой метод предпочтительным, если владелец явно его отверг.

## 4. Freshness: запоминать ответ или способ получения

При повторном вопросе Бэрримор сначала ищет похожий Episode/Fact/Procedure.

- если результат достаточно стабилен и ещё актуален — использует его;
- если результат мог измениться — не повторяет старое значение, а повторно
  применяет сохранённую Procedure;
- если предыдущий способ больше не применим — исследует заново и обновляет
  опыт.

Пример погоды: хранится местоположение и способ получить текущую погоду, а не
вчерашняя температура как ответ на сегодняшний вопрос.

Пример состояния системы: хранится способ проверить service/process/runtime,
а не прежний статус как вечный факт.

## 5. Research: неизвестность запускает исследование

Базовое правило:

> Если ответа нет или уверенности недостаточно, Бэрримор не выдумывает ответ и
> не сдаётся сразу. Он пытается получить недостающие сведения доступными
> безопасными способами.

Предпочтительный порядок источников:

1. релевантная память, Episode и Procedure;
2. собственное runtime-состояние;
3. локальная машина и разрешённые read-only capabilities;
4. локальная документация, файлы и журналы;
5. внешние источники и документация в сети;
6. внешний AI-worker, если задача действительно требует тяжёлого анализа;
7. ограниченный безопасный эксперимент;
8. честное «не знаю» или вопрос владельцу, если получить факт нельзя без
   дополнительного решения.

Это не жёсткая линейная лестница: planner выбирает минимальный достоверный и
дешёвый источник, но обязан учитывать уже накопленный опыт.

## 6. Capabilities вместо произвольного shell

Read-only исследование должно быть самостоятельным и не превращаться в поток
подтверждений владельца.

Целевая первая группа capabilities:

- runtime/model inspection;
- process/service inspection;
- filesystem read/stat;
- git inspection;
- log inspection;
- hardware/system inspection;
- network/http probe;
- local documentation lookup;
- web search/fetch.

Каждая capability объявляет:

- что она умеет;
- что она не умеет;
- arguments schema;
- result schema;
- scope;
- side effect class;
- стоимость и типичные ограничения.

Модель выбирает capability из предоставленного runtime списка. Произвольная
команда из model output не исполняется.

## 7. Intervention: изменения используют тот же контур

Для кода, локальной системы и удалённых серверов применяется тот же подход:

```text
Goal
 ↓
Recall similar episodes/procedures
 ↓
Inspect current state and preconditions
 ↓
Plan
 ↓
Risk + policy
 ↓
Act
 ↓
Verify
 ↓
Rollback / retry when needed
 ↓
Learn
```

Ключевой принцип: повторяется не старая команда, а проверенный способ решения с
повторной проверкой предпосылок.

## 8. Классы риска

Начальная классификация:

- `observe` — без изменения состояния;
- `safe-change` — локальное, обратимое, ограниченное изменение;
- `significant-change` — сервис, production config, package/install/push и т.п.;
- `destructive` — удаление, force operation, необратимое изменение.

Policy зависит не только от вида действия, но и от target/scope. Владелец может
разрешить безопасную автономию для одного хоста и требовать подтверждение того
же действия на production.

## 9. Verify — обязательная стадия

Утверждение исполнителя «готово» не является результатом.

Примеры:

- код: diff + targeted tests + общий набор проверок;
- сервис: config validation + state + реальный probe;
- web endpoint: HTTP result;
- worker result: проверяемые acceptance criteria.

Только подтверждённое наблюдение закрывает Episode как успешный.

## 10. External workers

AI-агенты остаются сменяемыми усилителями, а не обязательной частью продукта.

Barrymore обязан запускаться и работать без них. При наличии workers runtime
может делегировать тяжёлое исследование/кодирование через существующий
WorkOrder/ContextPack/policy/verification контур.

В ближайшем цикле не строим сложный рейтинг агентов без реальных наблюдений.
Достаточно:

- discovery;
- capability description;
- availability;
- execute/cancel/result;
- накопление реального опыта по мере появления задач.

## 11. Standalone / first run

Обычный пользователь не должен собирать `llama.cpp` и помнить dev-команды.
Целевой Linux bundle:

```text
barrymore/
├── barrymore
├── libexec/
│   └── llama-server
└── data/
    └── local_models/
        └── *.gguf
```

Первый запуск:

1. создаёт data/runtime и выполняет migrations;
2. предпочитает комплектный `libexec/llama-server`;
3. обнаруживает GGUF в `data/local_models`;
4. если выбор модели ещё не подтверждён — показывает first-run selection;
5. владелец подтверждает единственную модель либо выбирает одну из нескольких;
6. выбор сохраняется;
7. Barrymore поднимает и наблюдает локальный сервер модели;
8. отсутствие внешних workers не считается ошибкой запуска.

Dev-путь (`make`, host-audit, внешний llama.cpp) остаётся для разработки, но не
является продуктовым сценарием.

## 12. Хранение

Основное хранилище остаётся SQLite WAL.

В SQLite живут:

- event journal и projections;
- messages/conversations;
- facts;
- procedures;
- episodes;
- feedback;
- sources/provenance;
- metadata артефактов;
- FTS indexes.

Большие логи, diff, screenshots, reports и downloads хранятся как artifacts на
диске; в БД лежат metadata/path/hash, а не бесконтрольные BLOB.

FTS5 и структурированные фильтры — первый retrieval. Embeddings могут быть
добавлены позже как индекс, но не как источник истины.

## 13. UI

ADR 0022 сохраняется: обычная поверхность — разговор + Стол.

Research/Episode/Procedure/WorkOrder являются внутренними сущностями. В обычном
режиме владелец видит человеческое состояние вроде «проверяю локально» или
«сверяю документацию», а технический режим раскрывает trace и источники.

У существенных ответов появляется ненавязчивое `like/dislike`.

## 14. Очерёдность реализации

Каждый пункт должен оставлять `master` собираемым и пригодным для живого
`git pull`-теста.

### Stage 1 — standalone foundation

- [ ] bundle-aware поиск `llama-server`;
- [ ] единый каталог моделей рядом с bundle/repository;
- [ ] first-run состояние: модель найдена, но ещё не подтверждена;
- [ ] подтверждение/выбор модели из обычного интерфейса;
- [ ] release/bundle recipe, в котором `llama-server` входит в комплект;
- [ ] обновлённая документация запуска.

### Stage 2 — durable experience schema

- [ ] migrations: Episode, Fact, Procedure, ProcedureStep, Feedback, Source,
  Artifact metadata;
- [ ] provenance/freshness/stability;
- [ ] FTS retrieval foundation;
- [ ] migration/replay tests.

### Stage 3 — research loop

- [ ] Recall;
- [ ] applicability/freshness decision;
- [ ] research plan;
- [ ] iterative typed probes;
- [ ] bounded loop/retry;
- [ ] trace in Episode;
- [ ] regression: «какая модель сейчас работает?» без случайного git skill.

### Stage 4 — read-only capability set

- [ ] runtime/model;
- [ ] process/service;
- [ ] filesystem/git/logs;
- [ ] hardware/network/http;
- [ ] documentation lookup;
- [ ] web research adapter.

### Stage 5 — procedural learning

- [ ] successful research produces/refines Procedure;
- [ ] volatile/realtime facts trigger re-execution rather than stale reply;
- [ ] failures and preconditions affect future planning;
- [ ] existing Practice model is reconciled with Procedure rather than
  duplicated.

### Stage 6 — intervention / risk / verification

- [ ] unified plan for code/system/remote changes;
- [ ] risk classes and target-scoped policy;
- [ ] explicit verification contracts;
- [ ] rollback metadata and recovery;
- [ ] worker result remains untrusted until verified.

### Stage 7 — user feedback

- [ ] like/dislike storage and UI;
- [ ] feedback attached to Episode/result;
- [ ] user correction can explain failure;
- [ ] feedback influences Procedure/Practice confidence without overriding
  objective truth.

### Stage 8 — workers evolve from real evidence

- [ ] preserve no-worker operation;
- [ ] normalize worker capabilities only where needed;
- [ ] accumulate real execution outcomes;
- [ ] postpone sophisticated routing until there is enough evidence.

### Stage 9 — product polish

- [ ] research progress on the Desk without exposing internal machinery;
- [ ] technical trace inspector;
- [ ] artifact/source views;
- [ ] retention/minimization controls;
- [ ] long-run DB maintenance and export/backup checks.

## 15. Acceptance milestones

### Research milestone

Unknown question → Recall → safe investigation → verified answer → reusable
procedure saved. No hallucinated fact and no unnecessary user confirmation.

### Volatile-answer milestone

A repeated realtime/volatile question reuses the successful acquisition method,
not the previous value.

### Intervention milestone

A requested change is preceded by current-state inspection, executed under
policy, independently verified and recorded as an Episode with rollback data.

### Feedback milestone

Explicit like/dislike changes future preference/confidence while objective
verification remains a separate signal.

### Standalone milestone

Fresh unpacked Barrymore + GGUF in `data/local_models` starts without installing
or compiling llama.cpp; first-run UI asks the owner to confirm/select the model.
