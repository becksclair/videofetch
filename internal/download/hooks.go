package download

// Hooks provide optional callbacks for persistence / external tracking.
// Implementations should be fast and non-blocking; Manager invokes them in goroutines.
type Hooks interface {
	OnProgress(dbID int64, progress float64)
	OnStateChange(dbID int64, state State, errMsg string)
}
