package auth

import (
	"testing"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	ip := "192.168.1.1"

	// First 5 should be allowed
	for i := 0; i < RateLimit; i++ {
		if !rl.Allow(ip) {
			t.Errorf("attempt %d should be allowed", i+1)
		}
	}

	// 6th should be denied
	if rl.Allow(ip) {
		t.Error("6th attempt should be denied")
	}

	// Different IP should still be allowed
	if !rl.Allow("10.0.0.1") {
		t.Error("different IP should be allowed")
	}
}

func TestRateLimiterPurge(t *testing.T) {
	rl := NewRateLimiter()
	defer rl.Close()

	rl.Allow("192.168.1.1")

	// Manually set old timestamps to simulate expiry
	rl.mu.Lock()
	rl.windows["192.168.1.1"].attempts = nil
	rl.mu.Unlock()

	rl.purgeStale()

	rl.mu.Lock()
	_, exists := rl.windows["192.168.1.1"]
	rl.mu.Unlock()

	if exists {
		t.Error("stale window should have been purged")
	}
}
