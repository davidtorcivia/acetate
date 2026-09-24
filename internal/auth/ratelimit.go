package auth

import (
	"sync"
	"time"
)

// RateWindow is the sliding window every limit applies over.
const RateWindow = 1 * time.Minute

// RateLimiter implements a per-IP sliding window rate limiter.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
	done    chan struct{}
	once    sync.Once
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
	rl.once.Do(func() {
		close(rl.done)
	})
}

// Allow records an attempt for key and reports whether it is within limit
// attempts per RateWindow.
func (rl *RateLimiter) Allow(key string, limit int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-RateWindow)

	w, ok := rl.windows[key]
	if !ok {
		w = &window{}
		rl.windows[key] = w
	}

	// Prune old attempts
	valid := w.attempts[:0]
	for _, t := range w.attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	w.attempts = valid

	if len(w.attempts) >= limit {
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
