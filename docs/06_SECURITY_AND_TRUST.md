# Безопасность и доверие

## 1. Модель угроз

Ошибочная или манипулируемая LLM, prompt injection, агент с широкими правами, утечка secrets, destructive command, незаметный платный вызов, публикация приватных данных, compromised plugin, stale memory, ложный success, захват browser session и повреждение event journal.

## 2. Основные правила

1. Безопасность реализуется runtime, а не инструкцией модели.
2. Любой model output считается недоверенным.
3. Side effects проходят через типизированные boundaries.
4. Scope минимален.
5. Secrets передаются по ссылке и только разрешённому adapter.
6. Платные действия видимы.
7. Необратимые действия требуют Approval.
8. Raw transcript доступен для аудита, но redacted в UI по умолчанию.
9. Внешний агент не пишет память напрямую.
10. Успех требует Verification.
11. Probe и reflex не получают больше прав, чем исходное действие.
12. Автоматическое восстановление ограничено attempts, cooldown, scope и проверяемым ожидаемым результатом.

## 3. Классы действий

`read`, `local_write`, `workspace_write`, `process_execute`, `network_read`, `network_write`, `paid_model_call`, `secret_access`, `publish`, `deploy`, `delete`, `privileged`.

Политика учитывает actor, thread, workspace, worker, resource, cost, sensitivity, time window и grant duration.

## 4. Bounded autonomy

ReflexPolicy может без отдельного вопроса выполнить только заранее разрешённое, обратимое и локальное действие: перечитать состояние, повторить идемпотентный probe, восстановить attachment, сохранить checkpoint, остановить или поставить run на паузу.

Запрещено автоматически расширять filesystem/network scope, получать новый secret, подтверждать платный вызов, менять цель, публиковать, deploy, удалять данные или повторять действие бесконечно. При исчерпании лимита попыток создаётся Discrepancy и эскалация.

## 5. Подтверждения

Approval UI показывает точное действие, worker, файлы/repository, network access, возможную стоимость, срок разрешения, сохраняемые данные и способ остановки.

Расплывчатое «разрешить всё навсегда» не является default.

## 6. Secrets

База хранит только SecretRef metadata: identifier, backend, scope, label, last used and policy. Значение не попадает в DB, event payload, prompt archive, UI log, crash report или обычный export.

## 7. Workspace security

Canonical path resolution, traversal rejection, symlink policy, reserved Barrymore paths, allowlisted roots, temporary directory, file size limits, binary handling, atomic writes and backup before dangerous changes.

## 8. Prompt injection

Repository, web, email и documents маркируются как untrusted data. Инструкции внутри них не становятся command. Tool boundary проверяет action независимо.

## 9. Runner isolation

Linux-first варианты: отдельный user, bubblewrap, systemd transient units, namespaces, resource limits, worktree isolation, network allowlist и позднее seccomp.

Первый проход может использовать worktree + process group + path policies, но архитектура должна позволять усиление изоляции.

## 10. Remote access

Loopback по умолчанию. Для удалённого доступа: WireGuard-first, authentication, secure sessions, CSRF, rate limit, re-auth для raw terminal, no model-server exposure и audit event.

## 11. Память и приватность

Sensitivity labels, thread/connector scope, retrieval policy, visible provenance, redact before cloud, cloud deny list, export/delete. High-sensitive memory не передаётся в cloud без policy.

## 12. Supply chain

Pinned dependencies, checksums, reproducible build where practical, no execution from model repositories, reviewed adapters, manifests, compatibility and migration backup.

## 13. Аудит

Журналируются observations, expectation changes, discrepancies, probes, reflex attempts/results, escalations, approvals, policy decisions, worker launches, secret reference use, network side effects, file writes, memory changes, exports, remote sessions и provider selection. Sensitive fields redacted до append.
