# ADR 0004: Предиктивный runtime и ограниченные локальные реакции

Status: Accepted

## Context

Если каждое событие, heartbeat, retry и изменение состояния отправлять в LLM, Бэрримор станет дорогим, медленным и хрупким чат-оркестратором. Личность и непрерывность уже определены как свойства runtime и данных, поэтому низкоуровневое управление также не должно зависеть от конкретной модели.

## Decision

Runtime поддерживает типизированные Observation, SystemStateSnapshot, Expectation и Discrepancy. Для известных классов расхождений применяются зарегистрированные ReflexPolicy с guards, policy scope, attempt limit, cooldown и обязательной Verification.

LLM используется как сменяемый deliberative layer для неоднозначной интерпретации, нового планирования и общения. Она может предлагать probes и expectations, но не создаёт исполняемый reflex или произвольный shell action.

Инструменты и среда используются для активного уменьшения неопределённости: runtime сначала получает недостающее наблюдение, когда это дешевле и надёжнее рассуждения.

## Consequences

- predictive primitives входят в foundation и event schema;
- WorkOrder содержит operational contract;
- recovery и liveness checks работают при недоступной LLM;
- UI различает наблюдение, ожидание, расхождение, реакцию и вывод;
- reflex не расширяет Approval и не повторяется бесконечно;
- transient operational state не загрязняет долговременную память;
- первый vertical slice демонстрирует bounded recovery на реальном worker run.
