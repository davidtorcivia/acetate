package server

import (
	"sync"
	"time"
)

const (
	adminLoginLockThreshold = 5
	adminLoginBackoffBase   = 5 * time.Second
	adminLoginBackoffMax    = 5 * time.Minute
	adminLoginResetWindow   = 30 * time.Minute
	adminLoginEntryTTL      = 2 * time.Hour
)

type adminLoginGuard struct {
	mu      sync.Mutex
	entries map[string]adminLoginEntry
}

type adminLoginEntry struct {
	failures    int
	lastFailure time.Time
	lockUntil   time.Time
}

func newAdminLoginGuard() *adminLoginGuard {
	return &adminLoginGuard{
		entries: make(map[string]adminLoginEntry),
	}
}

func (g *adminLoginGuard) allow(key string, now time.Time) (bool, time.Duration) {
	if key == "" {
		return true, 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.purgeStale(now)

	entry, ok := g.entries[key]
	if !ok {
		return true, 0
	}
	if now.Sub(entry.lastFailure) > adminLoginResetWindow {
		delete(g.entries, key)
		return true, 0
	}
	if now.Before(entry.lockUntil) {
		return false, entry.lockUntil.Sub(now)
	}
	return true, 0
}

func (g *adminLoginGuard) markFailure(key string, now time.Time) time.Duration {
	if key == "" {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.purgeStale(now)

	entry := g.entries[key]
	if now.Sub(entry.lastFailure) > adminLoginResetWindow {
		entry = adminLoginEntry{}
	}

	entry.failures++
	entry.lastFailure = now

	backoff := adminLoginBackoffDuration(entry.failures)
	if backoff > 0 {
		entry.lockUntil = now.Add(backoff)
	}

	g.entries[key] = entry
	return backoff
}

func (g *adminLoginGuard) markSuccess(key string) {
	if key == "" {
		return
	}

	g.mu.Lock()
	delete(g.entries, key)
	g.mu.Unlock()
}

func (g *adminLoginGuard) purgeStale(now time.Time) {
	for key, entry := range g.entries {
		if now.Sub(entry.lastFailure) > adminLoginEntryTTL {
			delete(g.entries, key)
		}
	}
}

func adminLoginBackoffDuration(failures int) time.Duration {
	if failures < adminLoginLockThreshold {
		return 0
	}

	step := failures - adminLoginLockThreshold
	if step > 8 {
		step = 8
	}

	backoff := adminLoginBackoffBase * time.Duration(1<<step)
	if backoff > adminLoginBackoffMax {
		return adminLoginBackoffMax
	}
	return backoff
}
