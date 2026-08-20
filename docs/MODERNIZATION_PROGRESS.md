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

Состояние: **фундамент реализован в `master`, ожидает CI/живого `git pull`-теста**.

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

Этот срез ещё не меняет поведение разговорного planner. Подключение Recall и
создание Episode на реальных вопросах — Stage 3; до этого новая служба является
проверяемым доменным фундаментом, а не заявленной пользовательской функцией.
