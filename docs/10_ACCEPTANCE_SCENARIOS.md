# Приёмочные сценарии

## A. Нить переживает перезапуск

Создать нить, добавить историю, две различающиеся позиции, перезапустить backend и убедиться, что данные/revisions сохранены без восстановления из chat transcript.

## B. Память не записывается скрытно

Модель предлагает MemoryCandidate; до accept retrieval его не использует; после accept использует; после revoke исключает.

## C. Смена модели не меняет личность

Повторить regression dialog через два providers. Допустима разница формулировок, недопустимы исчезновение истории и изменение policies.

## D. Обнаружение worker

Найти executable, получить version, не выполнять платный запрос, сохранить evidence/timestamp и показать unknown quota честно.

## E. Объяснимый выбор

Один worker сильнее, но quota exhausted; второй доступен и бесплатен. Для небольшой задачи выбран второй с объяснением. Ручной override первого требует подтверждение риска/стоимости.

## F. Audit-only

Worker пытается записать файл при read-only scope. Действие блокируется, run не становится completed, событие видно.

## G. Controlled worktree write

Repository имеет незакоммиченные изменения. Они не теряются; worker работает в отдельном worktree; tests/diff доступны; push не выполняется; merge отдельный.

## H. Перезапуск во время run

После рестарта runtime attach/resume или честно помечает orphaned; logs/workspace сохранены; WorkOrder не completed.

## I. Ошибка worker

Non-zero exit сохраняет raw output; Бэрримор объясняет и предлагает retry/другого worker/ручной разбор.

## J. Verification

Worker утверждает, что tests прошли, но реальная проверка падает. WorkOrder остаётся verifying/failed, противоречие видно, нить обновляется подтверждёнными фактами.

## K. Privacy

High-sensitive memory исключается из cloud context по policy; retrieval trace показывает причину; prompt archive не содержит данные.

## L. Платный запуск

Без pre-approved budget создаётся Approval; до grant процесс не запускается; grant ограничен WorkOrder и max cost.

## M. Инициатива

Mute и frequency limit соблюдаются. Сообщение после mute содержит причину обращения.

## N. Экспорт и удаление

Экспорт thread/memory/work orders версионирован; revoked memory не используется; backup/restore сохраняет связи/revisions.

## O. Потеря attachment не считается зависанием

Runner перестаёт получать stream, но process жив. Runtime выполняет разрешённый process/attachment probe, восстанавливает attach, фиксирует reflex events и продолжает run без вызова LLM и без уведомления пользователя как о катастрофе.

## P. Stale heartbeat диагностируется перед реакцией

Heartbeat просрочен. Runtime проверяет PID, waiting-for-input state, последние file/command events и scope. Только после подтверждения проблемы ставит run на паузу или эскалирует. Отсутствие stdout само по себе не завершает процесс.

## Q. Reflex ограничен политикой

Recovery rule имеет максимум две попытки reconnect. После двух неудач третья не запускается автоматически; создаётся Discrepancy, сохраняется диагностика и требуется deliberation или решение пользователя. Scope и стоимость не расширяются.

## R. Активный probe уменьшает неопределённость

Перед выбором worker availability snapshot просрочен. Runtime выполняет разрешённый probe, обновляет confidence/freshness и выбирает worker по новому состоянию. При невозможности проверки показывает `unknown`, а не придумывает доступность.

## S. Смена LLM не ломает локальные контуры

Conversational provider выключен или заменён. Heartbeat checks, audit-only write denial, restart reconciliation, verification и bounded recovery продолжают работать. UI честно показывает недоступность deliberative слоя.

## T. Operational state не загрязняет память

Краткий network error, heartbeat gap и успешный reconnect остаются событиями и operational history. MemoryCandidate не создаётся автоматически. После повторяющегося подтверждённого дефекта может быть предложен `known_failure` с provenance.

