# Штат и делегирование

## 1. Принцип

Бэрримор не обязан выполнять специализированную работу собственной conversational LLM. Он выбирает подходящего работника, выдаёт ограниченное поручение и остаётся владельцем контекста и результата.

## 2. Типы работников

CLI coding agents (Claude Code, Codex CLI, OpenCode, Pi, Hermes), API agents, локальные модели, research adapters, browser automation, deterministic tools, future plugins и люди.

## 3. Worker Adapter

```go
type WorkerAdapter interface {
    Descriptor(ctx context.Context) (WorkerDescriptor, error)
    Probe(ctx context.Context) (ProbeResult, error)
    Capabilities(ctx context.Context) ([]CapabilityEvidence, error)
    Availability(ctx context.Context) (AvailabilitySnapshot, error)
    Prepare(ctx context.Context, order WorkOrder, pack ContextPack) (PreparedRun, error)
    Start(ctx context.Context, prepared PreparedRun) (RunHandle, error)
    Attach(ctx context.Context, runID string) (<-chan RunEvent, error)
    Pause(ctx context.Context, runID string) error
    Resume(ctx context.Context, runID string) error
    Cancel(ctx context.Context, runID string) error
    Collect(ctx context.Context, runID string) (RunResult, error)
}
```

Конкретная сигнатура может измениться, но границы обязанностей сохраняются.

## 4. Обнаружение

Ручное добавление, поиск executable в PATH, конфигурационные adapters, проверка версии, auth state, smoke probe, отключение сломанного adapter и несколько установок одного worker.

Обнаружение не запускает платный запрос без согласия.

## 5. Доступность и лимиты

Источники в порядке предпочтения: официальный API, официальная CLI-команда, документированный local state, structured response, известная quota error, ручная отметка, безопасный пробный запрос по policy.

Каждый snapshot имеет confidence и freshness. Нельзя показывать «доступен» без основания.

Статусы: точно доступен, вероятно доступен, неизвестен, лимит исчерпан, требуется авторизация, возможен платный запуск, adapter неисправен.

Бэрримор не обходит квоты и не маскирует автоматизацию под другого пользователя.

## 6. Capability Matrix

Capability собирается из официальных возможностей, версии adapter, smoke tests, фактических выполнений, пользовательской оценки и независимых проверок.

Измерения: audit quality, code success, regression rate, instruction adherence, retries, time to verified result, cost, context fit, Russian quality и scope expansion risk.

## 7. Trust Level

| Уровень | Разрешение |
|---|---|
| `observe` | читать подготовленный пакет, не видеть workspace |
| `workspace_read` | читать разрешённый workspace |
| `proposal_only` | возвращать patch/plan без применения |
| `worktree_write` | писать только в выделенный Git worktree |
| `workspace_write` | писать в разрешённый workspace |
| `external_side_effects` | сеть, публикации, deployments по отдельным policies |

По умолчанию coding agents получают `worktree_write`, а не доступ к master.

## 8. Выбор работника

Runtime строит объяснимый score:

```text
task capability fit
× availability confidence
× historical success
× trust compatibility
× privacy compatibility
× context fit
× verification fit
− expected cost
− quota risk
− scope expansion risk
− setup overhead
```

LLM может классифицировать задачу, но итоговый список формирует runtime. Пользователь может переопределить выбор.

## 9. Context Pack

Обязательные секции: Goal, Why this matters, Thread history, Confirmed decisions, User constraints, Workspace state, Known problems, Past attempts, Allowed actions, Forbidden actions, Acceptance criteria, Verification commands, Required report, Stop conditions.

Пакет сохраняется как артефакт с revision и checksum.

## 10. Изоляция Git

Фиксируется исходный HEAD, сохраняются status/diff/untracked inventory, существующая работа не уничтожается, создаётся новый worktree или явно выбирается существующий, push запрещён по умолчанию, изменения проверяются, commit выполняется по policy, merge является отдельным действием.

## 11. Operational contract и ожидания

Для каждого WorkerRun создаётся набор явных Expectations:

- процесс запустится или вернёт диагностируемую ошибку;
- heartbeat/observed action поступит в допустимом интервале;
- worker не выйдет за workspace и trust scope;
- audit-only run не создаст writes;
- после process exit будут собраны raw output и обязательные артефакты;
- Verification завершится до перевода WorkOrder в `completed`.

Допустимая тишина зависит от worker mode и текущего действия. Отсутствие stdout само по себе не считается зависанием.

## 12. Наблюдение

RunEvent: process started, prompt delivered, worker message, tool/action detected, file changed, command started/completed, checkpoint, approval requested, waiting for input, warning, error, heartbeat, attachment lost/restored, process exited, artifact produced.

Adapters не обязаны раскрывать скрытое рассуждение. Нужны наблюдаемые действия и достаточно сигналов, чтобы отличить работу, ожидание, потерю связи и завершение.

## 13. Контроль отклонения

При расхождении runtime сначала выполняет разрешённые диагностические probes: проверка процесса, attachment, waiting state, последних событий и workspace diff.

Run ставится на паузу при подтверждённом выходе за scope, запрещённых файлах, destructive command, росте стоимости, нарушении stop condition, повторяющемся цикле, необъяснённой потере heartbeat, запросе нового secret или изменении архитектурного замысла без ADR.

Автоматические действия ограничены operational contract: reconnect, checkpoint, один или несколько bounded retries, pause и сбор диагностики. Изменение цели, расширение scope, платный повтор или destructive recovery требуют новой политики или пользователя.

## 14. Приём результата

WorkerResult: summary, changed files, commits, commands, tests, warnings, failures, skipped checks, artifacts, remaining work и raw transcript reference.

Summary сопоставляется с реальным состоянием.

## 15. Проверка coding work

`git status --short`, `git diff --stat`, `git diff --check`, project tests, build/lint, acceptance scenario, forbidden file check, secret scan and user review where needed.

## 16. Обучение на опыте

После завершения создаётся AgentPerformanceCandidate: тип задачи, результат, проверки, retries, time, cost, нарушения и оценка пользователя. Метрика становится активной только после достаточного evidence.

## 17. Класс исполнителя и стоимость модели

Уточнено владельцем 2026-08-05, подробности в ADR 0013.

### Класс

| Класс | Кто | Когда привлекается |
|---|---|---|
| `routine` | OpenCode, Hermes | повседневно, по умолчанию |
| `specialist` | Codex, Claude Code | трудная задача или ручной выбор владельца |

Повседневная работа не должна расходовать платную квоту. При обычной задаче
специалист получает штраф к оценке, при политике «только бесплатные» —
не рассматривается вовсе.

### Стоимость определяется до запуска

Бесплатность модели решается **до** её использования, по пометке провайдера в
названии (`-free`, `:free`, локальные модели). Другого бесплатного способа
узнать цену заранее нет. Модель без явной пометки бесплатной не считается.

Фактическое списание никогда не используется, чтобы узнать цену: если списание
произошло, деньги уже потрачены. Оно служит страховкой — ожидание
`worker_run.cost_policy` с пределом 0 останавливает запуск, навсегда помечает
модель платной и переводит поручение в `failed`.

### Каталог моделей — наблюдение

Состав бесплатных моделей меняется, поэтому каталог перечитывается целиком,
исчезнувшие модели удаляются, у каталога есть срок годности, а отметки о
списаниях переносятся на обновлённый список.

### Политика стоимости

`free` (умолчание), `prefer-free`, `any` — флаг `-model-policy`.
Владелец может закрепить конкретную модель за исполнителем вручную; ручной
выбор не обходит политику стоимости.

---

## Дополнение 2026-08-05: как реализована контролируемая запись

§10 требует зафиксировать HEAD, не уничтожать существующую работу, запретить
push и сделать слияние отдельным действием. Реализовано через изолированную
копию (ADR 0016), а не через `git worktree`.

### Почему копия

Worktree создаёт записи внутри `.git` настоящего репозитория. То есть уже
меняет каталог, неизменность которого Бэрримор потом проверяет, и делит
с исполнителем объекты git. Копия дороже, зато проверка «каталог владельца не
изменился» остаётся настоящей проверкой, а не оговоркой.

### Что видит исполнитель

Полную копию каталога, включая незакоммиченную работу владельца. Иначе он
работал бы не с тем, что видит владелец, и расхождение вскрылось бы при
применении.

Состояние «до» зафиксировано коммитом внутри копии. Всё, что отличается от
него, сделал исполнитель — и ничего больше. Работа владельца никогда не будет
выдана за чужую (сценарий G).

### Что происходит после

| Состояние | Что значит |
|---|---|
| `none` | изменений нет либо поручение только на чтение |
| `collected` | изменения собраны и ждут решения владельца |
| `applied` | владелец применил их к своему каталогу |
| `discarded` | владелец отказался, копия удалена |

Судьба изменений отдельна от судьбы поручения: поручение может быть выполнено,
а изменения ещё не рассмотрены. И наоборот — поручение может быть помечено
`failed` из-за плохого отчёта, а изменения при этом остаются годными. Работа
и отчёт о работе оцениваются отдельно.

### Применение

`git apply --check --3way` до всякой записи: применить наполовину хуже, чем
не применить вовсе. Ничего не коммитится — изменения остаются
незакоммиченными, чтобы владелец посмотрел их своими инструментами; откат
остаётся обычным `git checkout`.

Если каталог ушёл вперёд и патч не ложится, Бэрримор отказывается и говорит
почему, а не накладывает что получится.
