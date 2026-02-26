package download

import (
	"context"
	"testing"
	"time"
)

// TestShutdownIdempotent ensures Manager.Shutdown can be called multiple times
// without panicking, which is important for clean server exits.
func TestShutdownIdempotent(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 8)
	// Call Shutdown twice; should not panic
	m.Shutdown()
	m.Shutdown()
}

func TestManagerShutdown_CancelsInFlightDownload(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 4)
	started := make(chan struct{})
	m.workerDownload = func(ctx context.Context, id, url string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}

	if _, err := m.Enqueue("https://example.com/video"); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("worker did not start in time")
	}

	done := make(chan struct{})
	go func() {
		m.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("shutdown did not complete after canceling in-flight download")
	}
}

type recordingStore struct {
	updateStatusCalls int
}

func (s *recordingStore) UpdateProgress(ctx context.Context, id int64, progress float64) error {
	return nil
}

func (s *recordingStore) UpdateStatus(ctx context.Context, id int64, status, errMsg string) error {
	s.updateStatusCalls++
	return nil
}

func (s *recordingStore) UpdateFilename(ctx context.Context, id int64, filename string) error {
	return nil
}

func (s *recordingStore) UpdateArtifacts(ctx context.Context, id int64, paths []string) error {
	return nil
}

func TestUpdateState_PersistsDuringClosing(t *testing.T) {
	st := &recordingStore{}
	m := &Manager{
		registry: NewItemRegistry(4),
		store:    st,
	}

	if _, err := m.registry.Create("id-1", "https://example.com/video"); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach("id-1", 42); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}

	m.closing.Store(true)
	m.updateState("id-1", StateCompleted, "")
	m.bgWg.Wait()

	if st.updateStatusCalls == 0 {
		t.Fatalf("expected UpdateStatus to be called while closing")
	}
}

type claimOnlyStore struct {
	claimResult     bool
	updateMetaCalls int
}

func (s *claimOnlyStore) TryClaimPending(ctx context.Context, id int64) (bool, error) {
	return s.claimResult, nil
}

func (s *claimOnlyStore) UpdateStatus(ctx context.Context, id int64, status string, errMsg string) error {
	return nil
}

func (s *claimOnlyStore) UpdateMeta(ctx context.Context, id int64, title string, duration int64, thumbnail string) error {
	s.updateMetaCalls++
	return nil
}

func TestProcessPendingDownload_ClaimConflictSkipsWork(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 4)
	defer m.Shutdown()

	st := &claimOnlyStore{claimResult: false}
	err := m.ProcessPendingDownload(context.Background(), 99, "https://example.com/video", st)
	if err != nil {
		t.Fatalf("expected nil error on claim conflict, got %v", err)
	}
	if st.updateMetaCalls != 0 {
		t.Fatalf("expected no metadata work when claim fails")
	}
	if len(m.Snapshot("")) != 0 {
		t.Fatalf("expected no queued manager items on claim conflict")
	}
}

func TestProcessPendingDownload_UsesBoundedMetadataTimeout(t *testing.T) {
	m := NewManager(t.TempDir(), 1, 4)
	defer m.Shutdown()

	st := &claimOnlyStore{claimResult: true}
	done := make(chan struct{})

	origFetch := fetchMediaInfo
	t.Cleanup(func() { fetchMediaInfo = origFetch })
	fetchMediaInfo = func(ctx context.Context, inputURL string) (MediaInfo, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("expected metadata context deadline to be set")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > pendingMetadataTimeout+time.Second {
			t.Fatalf("unexpected metadata deadline window: %s", remaining)
		}
		close(done)
		return MediaInfo{}, context.DeadlineExceeded
	}

	err := m.ProcessPendingDownload(context.Background(), 7, "https://example.com/video", st)
	if err == nil {
		t.Fatalf("expected metadata failure error")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("metadata fetch hook was not called")
	}
}
