package runtime

// Типы событий предиктивного контура (08_API_AND_EVENTS §4).
const (
	EvObservationRecorded  = "observation.recorded"
	EvSystemStateUpdated   = "system_state.updated"
	EvExpectationCreated   = "expectation.created"
	EvExpectationChecked   = "expectation.checked"
	EvExpectationSatisfied = "expectation.satisfied"
	EvExpectationExpired   = "expectation.expired"
	EvExpectationCancelled = "expectation.cancelled"
	EvDiscrepancyDetected  = "discrepancy.detected"
	EvDiscrepancyUpdated   = "discrepancy.updated"
	EvDiscrepancyResolved  = "discrepancy.resolved"
	EvDiscrepancyAcked     = "discrepancy.acknowledged"
	EvProbeRequested       = "probe.requested"
	EvProbeCompleted       = "probe.completed"
	EvProbeFailed          = "probe.failed"
	EvReflexStarted        = "reflex.started"
	EvReflexCompleted      = "reflex.completed"
	EvReflexFailed         = "reflex.failed"
	EvEscalationRequested  = "escalation.requested"
	EvPolicyDecided        = "policy.decided"
)

// Таблицы проекций предиктивного контура. Порядок соответствует внешним ключам.
var ProjectionTables = []string{
	"observations",
	"system_snapshots",
	"expectations",
	"discrepancies",
	"probes",
	"reflex_attempts",
	"policy_decisions",
}
