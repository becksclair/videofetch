package download

import (
	"testing"
)

// TestShutdownIdempotent ensures Manager.Shutdown can be called multiple times
// without panicking, which is important for clean server exits.
func TestShutdownIdempotent(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 8)
	// Call Shutdown twice; should not panic
	m.Shutdown()
	m.Shutdown()
}
