package capability

// Coverage C2: management is not health. Who manages a component and
// whether Klyx can observe its health are two facts, each resting on the
// evidence it names. Platform management is claimed only on evidence that
// distinguishes it, and management never suppresses a fault: an observed
// component that is not ready reports it, whoever manages it, and a
// forbidden read stays unreadable, never "managed".

// Manager is who manages a component.
type Manager string

const (
	ManagerSelfHosted Manager = "self-hosted"
	ManagerPlatform   Manager = "platform"
	ManagerUnknown    Manager = "unknown"
)

// Observability is whether Klyx can read a component's health.
type Observability string

const (
	Observed    Observability = "observed"
	NotObserved Observability = "not-observed"
)

// Management is the two facts with their evidence.
type Management struct {
	Manager          Manager       `json:"manager"`
	ManagerEvidence  string        `json:"managerEvidence,omitempty"`
	Observed         Observability `json:"observed"`
	ObservedEvidence string        `json:"observedEvidence,omitempty"`
}

// Unobserved is the reading for a component whose health Klyx cannot
// observe: neither healthy nor failed.
func (m Management) Unobserved() string {
	if m.Manager == ManagerPlatform {
		return "managed by the platform; health not directly observed"
	}
	return "manager unverified; health not directly observed"
}

// HealthReading is a component's health as said: the observed health as
// given (never suppressed by who manages it), or the unobserved reading.
func (m Management) HealthReading(observedHealth string) string {
	if m.Observed == NotObserved {
		return m.Unobserved()
	}
	return observedHealth
}
