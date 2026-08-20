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

Повторный dev-прогон 2026-08-20 через `make run` на свободном порту подтвердил
`поднята_бэрримором=true`: Barrymore сам запустил найденный рядом с repository
`llama-server`, дождался готовности и продолжил обслуживать HTTP во время
загрузки. Отдельный standalone gate всё ещё уже этого факта: нужно повторить
самозапуск именно из свежераспакованного bundle, а не из dev repository.

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

Состояние: **кодовый и живой Ornith gates подтверждены**.

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

После подтверждённого gate исправлен persistence-дефект Source freshness:
миграция 0016, projection и replay-тест теперь сохраняют `stability` через
restart/rebuild. Этот fix также вошёл в последующий зелёный полный gate на
`215fd16`.

Живой gate Stage 3 выполнен 2026-08-20 на настоящей Ornith прямым HTTP-запросом
к тому же conversation endpoint, которым пользуется UI. На вопрос «Какая модель
у тебя сейчас запущена?» Barrymore:

- выполнил ровно один fresh `runtime.provider.inspect`;
- ответил по realtime evidence: Ornith, `model=local`, status `ready`, endpoint
  `http://127.0.0.1:18080`;
- не предложил `git.worktree.diagnose` и не запросил подтверждение read-only probe;
- завершил Episode с `outcome=success`, `successful_steps=1`, `failed_steps=0`;
- сохранил один realtime Source и активную read-only Procedure с единственным
  typed step `runtime.provider.inspect`.

Живой прогон также обнаружил слабость bounded loop: Ornith повторяла уже
успешный probe. Runtime не исполнял его повторно, но считал отказ failure, из-за
чего Episode становился `partial`, а Procedure не сохранялась. Regression test
теперь воспроизводит этот класс: повтор после fresh evidence завершает
исследование принудительным финальным вызовом, не обесценивая успешный маршрут.

## Stage 4 — explicit feedback

Состояние: **кодовый и живой браузерный gates подтверждены**.

На реальном хосте владельца выполнены:

- `go test ./...` — весь репозиторий зелёный;
- `node --check internal/api/web/feedback.js` — синтаксических ошибок нет.

Срез вводит:

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

Живой gate Stage 4 выполнен 2026-08-20 в настоящем браузере против настоящего
backend и Ornith: реплика «Привет, как тебя зовут?» получила обычный ответ и
отдельный Episode; под ответом появились 👍/👎. После нажатия 👍 и полной
перезагрузки `aria-pressed=true` сохранился на той же реплике.

## Восстановление длинного HTTP conversation path

При разборе браузерного `NetworkError` текущий upstream `4412fe7` оказался
некомпилируемым: commit `aa869da`, добавлявший panic diagnostics, одновременно
заменил значительную часть актуального `internal/api/api.go` старыми версиями
обработчиков (`140` добавлений, `302` удаления). Это проявлялось десятками
несовместимых вызовов уже изменённых domain API и не позволяло проверить свежий
бинарник.

В `8a75c75` актуальный API восстановлен, а intended recovery оставлен
минимальной надстройкой. Regression `TestHTTPPanicBecomesProblemResponse`
подтверждает класс ошибки: panic не уходит в `net/http` как немой socket close,
а логируется со stack trace и возвращает `application/problem+json` с 500.

После восстановления один прямой и один браузерный длинный POST с фразой
«Привет, как тебя зовут?» завершились `200 OK`; браузерный запрос оставался
жив 112 секунд и показал ответ с Episode и feedback controls. Исходный немой
разрыв в свежем бинарнике не повторился, поэтому несуществующий новый stack
trace ему не приписывается.
