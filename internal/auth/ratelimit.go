package auth

import (
	"sync"
	"time"
)

const (
	RateLimit  = 5
	RateWindow = 1 * time.Minute
)

// RateLimiter implements a per-IP sliding window rate limiter.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
	done    chan struct{}
}

type window struct {
	attempts []time.Time
}

// NewRateLimiter creates a rate limiter and starts the cleanup goroutine.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string]*window),
		done:    make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Close stops the cleanup goroutine.
func (rl *RateLimiter) Close() {
	close(rl.done)
}

// Allow checks if the given IP is within the rate limit.
// Returns true if the request is allowed.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-RateWindow)

	w, ok := rl.windows[ip]
	if !ok {
		w = &window{}
		rl.windows[ip] = w
	}

	// Prune old attempts
	valid := w.attempts[:0]
	for _, t := range w.attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	w.attempts = valid

	if len(w.attempts) >= RateLimit {
		return false
	}

	w.attempts = append(w.attempts, now)
	return true
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.purgeStale()
		case <-rl.done:
			return
		}
	}
}

func (rl *RateLimiter) purgeStale() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-RateWindow)
	for ip, w := range rl.windows {
		// Remove entries with no recent attempts
		allStale := true
		for _, t := range w.attempts {
			if t.After(cutoff) {
				allStale = false
				break
			}
		}
		if allStale {
			delete(rl.windows, ip)
		}
	}
}
