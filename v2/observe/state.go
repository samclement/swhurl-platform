package observe

// The states an instance can be in: the one vocabulary the console shows.
const (
	StateSuspended  = "suspended"
	StateFailing    = "failing"
	StateUpdating   = "updating"
	StateStalled    = "stalled"
	StateRolledBack = "rolled back"
	StateStopped    = "stopped"
	StateUnknown    = "unknown"
	StateHealthy    = "healthy"
)

// State is an instance's state and the fact that decided it, assigned by apps from delivery's verdict and
// observed availability. Field names deployment, unit.revision or an unread fact; a healthy state names nothing.
type State struct {
	Name, Field, Reason string
}
