package download

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestResumeByDBID_DoesNotExecuteStaleQueuedJob(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 4),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}
	m.runCtx, m.runCancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		if m.runCancel != nil {
			m.runCancel()
		}
	})

	const id = "id-1"
	const dbID = int64(42)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.Update(id, func(it *Item) {
		it.queueToken = 1
	}); err != nil {
		t.Fatalf("registry update failed: %v", err)
	}
	// Simulate pre-existing queued work still in channel.
	m.jobs <- job{id: id, url: mediaURL, token: 1}

	if ok := m.PauseByDBID(dbID); !ok {
		t.Fatalf("expected pause to succeed")
	}
	resumed, err := m.ResumeByDBID(dbID)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if !resumed {
		t.Fatalf("expected resume to succeed")
	}

	var calls atomic.Int32
	m.workerDownload = func(ctx context.Context, id, url string) error {
		calls.Add(1)
		return nil
	}

	close(m.jobs)
	m.wg.Add(1)
	go m.worker(0)
	m.wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly one download execution, got %d", got)
	}
	item := m.registry.Get(id)
	if item == nil {
		t.Fatalf("expected item to exist")
	}
	if item.State != StateCompleted {
		t.Fatalf("expected final state completed, got %s", item.State)
	}
}

func TestResumeByDBID_QueueFullRollsBackState(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 1),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}

	const id = "id-queue-full"
	const dbID = int64(77)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.SetState(id, StatePaused, ""); err != nil {
		t.Fatalf("registry set state failed: %v", err)
	}

	// Saturate queue so first resume cannot enqueue.
	m.jobs <- job{id: "other", url: "https://example.com/other", token: 1}

	resumed, err := m.ResumeByDBID(dbID)
	if err != ErrQueueFull {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
	if resumed {
		t.Fatalf("expected resumed=false on full queue")
	}

	item := m.registry.Get(id)
	if item == nil {
		t.Fatalf("expected item to exist")
	}
	if item.State != StatePaused {
		t.Fatalf("expected paused state rollback, got %s", item.State)
	}

	// Free queue slot and ensure a second resume enqueues successfully.
	select {
	case <-m.jobs:
	default:
		t.Fatalf("expected saturated queue entry")
	}

	resumed, err = m.ResumeByDBID(dbID)
	if err != nil {
		t.Fatalf("second resume failed: %v", err)
	}
	if !resumed {
		t.Fatalf("expected second resume to succeed")
	}
}

func TestResumeByDBID_CompletedItemIsNotResumed(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 2),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}

	const id = "id-complete"
	const dbID = int64(88)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.SetState(id, StateCompleted, ""); err != nil {
		t.Fatalf("registry set state failed: %v", err)
	}

	resumed, err := m.ResumeByDBID(dbID)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resumed {
		t.Fatalf("expected resumed=false for completed item")
	}
	if got := len(m.jobs); got != 0 {
		t.Fatalf("expected no queue job for completed item, got %d", got)
	}
	item := m.registry.Get(id)
	if item == nil || item.State != StateCompleted {
		t.Fatalf("expected completed state to remain, got %+v", item)
	}
}

func TestPauseByDBID_QueuedJobIsRemovedFromQueue(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 2),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}

	const id = "id-paused"
	const dbID = int64(51)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.Update(id, func(it *Item) {
		it.queueToken = 1
	}); err != nil {
		t.Fatalf("registry update failed: %v", err)
	}
	m.jobs <- job{id: id, url: mediaURL, token: 1}

	if ok := m.PauseByDBID(dbID); !ok {
		t.Fatalf("expected pause to succeed")
	}
	if got := len(m.jobs); got != 0 {
		t.Fatalf("expected paused queued job to be removed from queue, len=%d", got)
	}
	item := m.registry.Get(id)
	if item == nil || item.State != StatePaused {
		t.Fatalf("expected paused state, got %+v", item)
	}
}

func TestCancelByDBID_QueuedJobIsRemovedFromQueue(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 2),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}

	const id = "id-canceled"
	const dbID = int64(52)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.Update(id, func(it *Item) {
		it.queueToken = 1
	}); err != nil {
		t.Fatalf("registry update failed: %v", err)
	}
	m.jobs <- job{id: id, url: mediaURL, token: 1}

	if ok := m.CancelByDBID(dbID); !ok {
		t.Fatalf("expected cancel to succeed")
	}
	if got := len(m.jobs); got != 0 {
		t.Fatalf("expected canceled queued job to be removed from queue, len=%d", got)
	}
	item := m.registry.Get(id)
	if item == nil || item.State != StateCanceled {
		t.Fatalf("expected canceled state, got %+v", item)
	}
}

func TestCancelByDBID_QueuedSnapshotButActiveEntry_RequestsStop(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 2),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}

	const id = "id-active"
	const dbID = int64(53)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	// Simulate stale queued snapshot with an already-active DB binding.
	if err := m.registry.SetState(id, StateQueued, ""); err != nil {
		t.Fatalf("registry set state failed: %v", err)
	}
	var cancelCalled atomic.Bool
	m.activeMu.Lock()
	m.activeByID[id] = &activeDownload{id: id, dbID: dbID, cancel: func() { cancelCalled.Store(true) }}
	m.activeByDB[dbID] = m.activeByID[id]
	m.activeMu.Unlock()

	if ok := m.CancelByDBID(dbID); !ok {
		t.Fatalf("expected cancel to succeed via active stop request")
	}
	if !cancelCalled.Load() {
		t.Fatalf("expected active cancel callback to be called")
	}
	item := m.registry.Get(id)
	if item == nil || item.State != StateQueued {
		t.Fatalf("expected queued state unchanged until worker handles cancel, got %+v", item)
	}
}

func TestWorkerSuccess_ClearsLateStopIntent(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 2),
		registry:    NewItemRegistry(4),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 4),
		activeByDB:  make(map[int64]*activeDownload, 4),
		stopIntents: make(map[string]State, 4),
		artifacts:   make(map[string]map[string]struct{}, 4),
	}
	m.runCtx, m.runCancel = context.WithCancel(context.Background())
	t.Cleanup(func() {
		if m.runCancel != nil {
			m.runCancel()
		}
	})

	const id = "id-complete"
	const dbID = int64(61)
	const mediaURL = "https://example.com/video"
	if _, err := m.registry.Create(id, mediaURL); err != nil {
		t.Fatalf("registry create failed: %v", err)
	}
	if err := m.registry.Attach(id, dbID); err != nil {
		t.Fatalf("registry attach failed: %v", err)
	}
	if err := m.registry.Update(id, func(it *Item) {
		it.queueToken = 1
	}); err != nil {
		t.Fatalf("registry update failed: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	m.workerDownload = func(ctx context.Context, id, url string) error {
		close(started)
		<-release
		return nil
	}

	m.jobs <- job{id: id, url: mediaURL, token: 1}
	close(m.jobs)
	m.wg.Add(1)
	go m.worker(0)

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("worker did not start")
	}
	if ok := m.PauseByDBID(dbID); !ok {
		t.Fatalf("expected pause request to register stop intent")
	}
	close(release)
	m.wg.Wait()

	item := m.registry.Get(id)
	if item == nil || item.State != StateCompleted {
		t.Fatalf("expected completed state, got %+v", item)
	}
	m.activeMu.Lock()
	defer m.activeMu.Unlock()
	if len(m.stopIntents) != 0 {
		t.Fatalf("expected stop intents to be cleared on success, got %d", len(m.stopIntents))
	}
}

func TestDropQueuedJobsByID_DuringShutdownDoesNotPanic(t *testing.T) {
	outputDir := t.TempDir()
	m := &Manager{
		outDir:      outputDir,
		jobs:        make(chan job, 8),
		registry:    NewItemRegistry(8),
		downloader:  NewDownloader(outputDir),
		activeByID:  make(map[string]*activeDownload, 8),
		activeByDB:  make(map[int64]*activeDownload, 8),
		stopIntents: make(map[string]State, 8),
		artifacts:   make(map[string]map[string]struct{}, 8),
	}
	m.jobs <- job{id: "a", url: "https://example.com/a", token: 1}
	m.jobs <- job{id: "b", url: "https://example.com/b", token: 1}

	done := make(chan struct{})
	go func() {
		m.Shutdown()
		close(done)
	}()

	for i := 0; i < 200; i++ {
		_ = m.dropQueuedJobsByID("a")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("shutdown did not complete")
	}
}
