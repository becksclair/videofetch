package server

import (
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	rl := newIPRateLimiter(2, time.Minute)
	defer rl.Stop()

	// First request should be allowed
	if !rl.Allow("192.168.1.1") {
		t.Error("First request should be allowed")
	}

	// Second request should be allowed
	if !rl.Allow("192.168.1.1") {
		t.Error("Second request should be allowed")
	}

	// Third request should be denied (capacity exceeded)
	if rl.Allow("192.168.1.1") {
		t.Error("Third request should be denied")
	}
}

func TestRateLimiter_DifferentIPs(t *testing.T) {
	rl := newIPRateLimiter(1, time.Minute)
	defer rl.Stop()

	// Each IP should have its own bucket
	if !rl.Allow("192.168.1.1") {
		t.Error("First IP first request should be allowed")
	}

	if !rl.Allow("192.168.1.2") {
		t.Error("Second IP first request should be allowed")
	}

	// Both IPs should now be at capacity
	if rl.Allow("192.168.1.1") {
		t.Error("First IP second request should be denied")
	}

	if rl.Allow("192.168.1.2") {
		t.Error("Second IP second request should be denied")
	}
}

func TestRateLimiter_Refill(t *testing.T) {
	rl := newIPRateLimiter(1, 50*time.Millisecond)
	defer rl.Stop()

	// Use up the token
	if !rl.Allow("192.168.1.1") {
		t.Error("First request should be allowed")
	}

	// Should be denied immediately
	if rl.Allow("192.168.1.1") {
		t.Error("Second request should be denied")
	}

	// Wait for refill
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again after refill
	if !rl.Allow("192.168.1.1") {
		t.Error("Request after refill should be allowed")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	rl := newIPRateLimiter(1, time.Minute)
	defer rl.Stop()

	// Add some buckets
	rl.Allow("192.168.1.1")
	rl.Allow("192.168.1.2")
	rl.Allow("192.168.1.3")

	initialCount := len(rl.buckets)
	if initialCount != 3 {
		t.Errorf("Expected 3 buckets, got %d", initialCount)
	}

	// Manually set old timestamps to simulate stale buckets
	rl.mu.Lock()
	for _, bucket := range rl.buckets {
		bucket.last = time.Now().Add(-25 * time.Hour) // Older than 24 hours
	}
	rl.mu.Unlock()

	// Trigger cleanup
	rl.cleanup()

	// All buckets should be cleaned up
	finalCount := len(rl.buckets)
	if finalCount != 0 {
		t.Errorf("Expected 0 buckets after cleanup, got %d", finalCount)
	}
}

func TestRateLimiter_CleanupPreservesRecent(t *testing.T) {
	rl := newIPRateLimiter(1, time.Minute)
	defer rl.Stop()

	// Add buckets at different times
	rl.Allow("old-ip")
	rl.Allow("recent-ip")

	// Make one bucket old
	rl.mu.Lock()
	for ip, bucket := range rl.buckets {
		if ip == "old-ip" {
			bucket.last = time.Now().Add(-25 * time.Hour)
		}
	}
	rl.mu.Unlock()

	// Trigger cleanup
	rl.cleanup()

	// Only recent bucket should remain
	rl.mu.Lock()
	count := len(rl.buckets)
	_, hasRecent := rl.buckets["recent-ip"]
	_, hasOld := rl.buckets["old-ip"]
	rl.mu.Unlock()

	if count != 1 {
		t.Errorf("Expected 1 bucket after cleanup, got %d", count)
	}
	if !hasRecent {
		t.Error("Recent bucket should be preserved")
	}
	if hasOld {
		t.Error("Old bucket should be cleaned up")
	}
}

func TestRateLimiter_Stop(t *testing.T) {
	rl := newIPRateLimiter(1, time.Minute)

	// Stop should not panic and should stop the cleanup goroutine
	rl.Stop()

	// Multiple stops should not panic
	rl.Stop()
}
