# ADR 0012: Локальная модель обслуживается llama.cpp, а не ollama

Status: Proposed
Date: 2026-08-04

## Context

Разговорный слой должен работать локально (`docs/01_PRODUCT_BOUNDARY.md` §2.7).
Host audit: ollama установлен, но сервер не запущен и моделей на диске нет.
Владелец предоставил `Qwen3.6-35B-A3B-UD-Q4_K_M.gguf` (20.6 GiB, arch `qwen35moe`,
256 экспертов, активны 8) и поручил проверить, как он будет работать.

## Decision

Локальный provider — `llama-server` из llama.cpp, запускаемый как user systemd unit
и отдающий OpenAI-совместимый `/v1/chat/completions`. Бэрримор обращается к нему
тем же generic-адаптером, что и к любому облачному endpoint, и не линкует llama.cpp.

Причина предпочтения перед ollama: `llama-server` поддерживает GBNF-грамматики и
decoding, ограниченный JSON Schema. Требование «невалидный ответ не должен частично
менять состояние» (`docs/03_SYSTEM_ARCHITECTURE.md` §6) перестаёт быть только
post-hoc проверкой — схема proposals навязывается на уровне сэмплера. Дополнительно:
slot save/restore KV-кэша, явный контроль контекста, потоков и offload.

Status остаётся `Proposed` до результатов спайка S1.

## Spike S1 — критерии приёмки

1. свежая сборка llama.cpp загружает архитектуру `qwen35moe`;
2. замер `llama-bench`: prefill и generation, CPU и Vulkan-offload на RX 580 (8 GiB);
3. проверка ответа, ограниченного JSON Schema;
4. рабочий контекст 32k помещается в память вместе с моделью.

Порог годности: генерация не ниже ~8 tok/s и prefill, при котором ContextPack на
8k токенов обрабатывается менее чем за минуту при переиспользовании KV-префикса.

Если порог не достигнут — ADR переводится в `Rejected`, модель по решению владельца
удаляется, разговорный слой поставляется в honest disabled state до выбора провайдера.

## Consequences

- llama.cpp собирается локально в `third_party/`, вне git;
- узкое место — prefill, поэтому префикс промпта (Identity, policies, память)
  проектируется стабильным ради переиспользования KV-кэша;
- контекст ограничивается 32k: KV-кэш ~40 KB/токен при f16;
- `llama-server` наблюдается Бэрримором как обычный provider с AvailabilitySnapshot.
