# Прогресс модернизации Бэрримора

Дата: 2026-08-20

Этот файл фиксирует фактически проверенные срезы из `docs/13_MODERNIZATION_PLAN.md`.
План задаёт цель, здесь отмечается только то, что уже существует в коде или
проверено живым запуском.

## Stage 1 — standalone foundation

Состояние: **частично подтверждено живым запуском**.

Коммит `b4a3dcc` проверен на реальном хосте владельца:

- standalone bundle нашёл собственный `libexec/llama-server`;
- bundle нашёл `data/local_models/Ornith-1.5-9B-AD-Q5_K-Q4_K.gguf`;
- first-run явно предложил найденную модель и сохранил подтверждение владельца;
- чистая база дошла до готового HTTP runtime.

Последняя строка живого прогона была `поднята_бэрримором=false`: на
`127.0.0.1:18080` уже отвечал внешний `llama-server`. Это правильное поведение
ADR 0014 — чужой процесс не присваивается и второй сервер не запускается.
Поэтому отдельный gate «комплектный llama-server действительно поднят самим
bundle после освобождения порта» ещё не закрыт.

Обычный web first-run остаётся отдельным UX-срезом: текущий standalone gate
использует интерактивный terminal prompt.

## Stage 2 — durable experience schema

Состояние: **доменный фундамент реализован и подтверждён полным локальным тестом**.

На реальном хосте владельца после `git pull` выполнен `go test ./...`; весь
репозиторий, включая `internal/experience`, прошёл без ошибок. Компиляционный
дефект в `Service.Begin` (`:=` вместо `=` после уже объявленного `err`) исправлен
коммитом `663136f` до этого gate.

Миграция 0015 и пакет `internal/experience` вводят:

- Episode с целью, исходным контекстом, результатом и verification;
- Source с provenance и confidence;
- Procedure и отдельные ProcedureStep, привязанные к typed capabilities;
- явный Feedback только `like`/`dislike`; отсутствие оценки не создаёт запись;
- metadata исследовательских Artifact без хранения больших BLOB в SQLite;
- `stability` и `verified_at` для существующего Fact (`memory_items`), без
  второго параллельного хранилища фактов;
- FTS5-индекс для Episode/Source/Procedure;
- event-journal + transactional projections для всех новых доменных записей;
- replay-тест: Episode, Source, Procedure, Artifact и Feedback восстанавливаются
  из журнала вместе с FTS.

Интеграционный хвост Stage 2 закрыт в Stage 3: `experience` теперь принадлежит
разговорному runtime и регистрирует свои проекторы в общем rebuild.

## Stage 3 — recall + bounded research loop

Состояние: **кодовый gate подтверждён полным локальным тестом на `f205bb3`; живой gate Ornith ещё не выполнен**.

Владелец выполнил `go test ./...` на HEAD `f205bb3`; весь репозиторий прошёл,
включая `conversation`, `experience`, `research` и `retrieval`.

Реализовано:

- query-specific Recall вместо подачи последних 40 воспоминаний безотносительно вопроса;
- поиск по Facts, Episodes, Sources и Procedures;
- freshness `immutable/stable/volatile/realtime` видна planner-у;
- typed read-only Research Registry: модель выбирает только зарегистрированную capability, а не shell-команду;
- первая capability `runtime.provider.inspect` делает fresh probe реального разговорного provider;
- bounded loop максимум из трёх наблюдений плюс финальный ответ;
- промежуточные черновики модели не становятся репликами или действиями;
- evidence записывается как Source, успешный маршрут — как Procedure;
- regression: вопрос о текущей модели проходит через fresh provider inspection и не требует `git.worktree.diagnose`.

После подтверждённого gate найден и исправлен отдельный persistence-дефект:
модель Source уже несла `stability`, но проекция её не сохраняла. Миграция 0016,
проекция и replay-тест теперь сохраняют freshness; старые события без поля
replay-ятся с совместимым default `stable`. Этот последующий fix ещё ждёт
следующего полного локального gate вместе со Stage 4.

Живой gate Stage 3: спросить настоящую Ornith «Какая модель у тебя сейчас
запущена?» и убедиться, что Бэрримор делает fresh provider inspection, отвечает
по evidence и не предлагает случайное git-умение.

## Stage 4 — explicit feedback

Состояние: **реализуется; полный локальный gate ещё не выполнен**.

Текущий срез вводит:

- Episode как единицу каждого успешного финального ответа, даже если Research не понадобился;
- без объективной проверки такой Episode имеет `outcome=unknown`, а не ложный `success`;
- долговечную корреляцию финальной реплики с Episode в read model;
- backend `GET/POST /api/v1/episodes/<id>/feedback`;
- только `like`/`dislike`; отсутствие оценки остаётся отсутствием;
- повтор той же оценки идемпотентен и не увеличивает её вес;
- изменение оценки добавляет новый сигнал, сохраняя историю; последняя явная оценка считается текущей;
- сообщения возвращают текущий feedback вместе с `episode_id`;
- тихие кнопки 👍/👎 под ответами Бэрримора;
- Recall показывает current feedback сильнее истории и не позволяет лайку сделать устаревший факт свежим.

Следующий gate: полный `go test ./...`, затем живой браузерный клик 👍/👎 и
проверка сохранения оценки после перезагрузки страницы.
