package analytics

import (
	"encoding/json"
	"testing"
	"time"

	"acetate/internal/database"
)

const testSessionID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testCollector(t *testing.T) *Collector {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	c := NewCollector(db)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRecordAndFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	c := NewCollector(db)

	// Record events
	for i := 0; i < 5; i++ {
		c.Record(Event{
			SessionID: "sess1",
			EventType: "play",
			TrackStem: "01-gathering",
		})
	}

	// Close triggers flush
	c.Close()

	// Verify events were written
	var count int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE session_id = 'sess1'").Scan(&count)
	if count != 5 {
		t.Errorf("expected 5 events, got %d", count)
	}
}

func TestBackpressureHighValue(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Don't start flushLoop — we want the channel to stay full
	c := &Collector{
		db:     db,
		events: make(chan Event, 1), // tiny buffer
		done:   make(chan struct{}),
	}

	// Fill the channel
	c.events <- Event{SessionID: "s", EventType: "heartbeat"}

	// High-value event should block briefly then drop
	start := time.Now()
	c.Record(Event{SessionID: "s", EventType: "play"})
	elapsed := time.Since(start)

	if elapsed < 80*time.Millisecond {
		t.Errorf("high-value event should have blocked briefly, elapsed: %v", elapsed)
	}

	if c.DroppedCount() == 0 {
		t.Error("should have dropped at least one event")
	}
}

func TestBackpressureLowValue(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Don't start flushLoop — we want the channel to stay full
	c := &Collector{
		db:     db,
		events: make(chan Event, 1), // tiny buffer
		done:   make(chan struct{}),
	}

	// Fill the channel
	c.events <- Event{SessionID: "s", EventType: "heartbeat"}

	// Low-value event should be dropped immediately
	start := time.Now()
	c.Record(Event{SessionID: "s", EventType: "heartbeat"})
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("low-value event should have been dropped immediately, elapsed: %v", elapsed)
	}

	if c.DroppedCount() == 0 {
		t.Error("should have dropped at least one event")
	}
}

func TestRecordBatch(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	c := NewCollector(db)

	data := []byte(`[
		{"event_type": "play", "track_stem": "01-gathering"},
		{"event_type": "pause", "track_stem": "01-gathering", "position_seconds": 30.5}
	]`)

	if err := c.RecordBatch(testSessionID, data); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}

	c.Close()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE session_id = ?", testSessionID).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 events, got %d", count)
	}
}

func TestGetTrackStats(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Insert test data directly
	db.Exec("INSERT INTO events (session_id, event_type, track_stem) VALUES ('s1', 'play', '01-gathering')")
	db.Exec("INSERT INTO events (session_id, event_type, track_stem) VALUES ('s1', 'complete', '01-gathering')")
	db.Exec("INSERT INTO events (session_id, event_type, track_stem) VALUES ('s2', 'play', '01-gathering')")

	stats, err := GetTrackStats(db)
	if err != nil {
		t.Fatalf("GetTrackStats: %v", err)
	}

	if len(stats) != 1 {
		t.Fatalf("expected 1 track stat, got %d", len(stats))
	}

	s := stats[0]
	if s.TotalPlays != 2 {
		t.Errorf("total plays = %d, want 2", s.TotalPlays)
	}
	if s.UniqueSessions != 2 {
		t.Errorf("unique sessions = %d, want 2", s.UniqueSessions)
	}
	if s.Completions != 1 {
		t.Errorf("completions = %d, want 1", s.Completions)
	}
	if s.CompletionRate != 0.5 {
		t.Errorf("completion rate = %f, want 0.5", s.CompletionRate)
	}
}

func TestRecordBatchRejectsOversizedBatch(t *testing.T) {
	c := testCollector(t)

	events := make([]map[string]string, MaxBatchSize+1)
	for i := range events {
		events[i] = map[string]string{"event_type": "heartbeat"}
	}
	data, _ := json.Marshal(events)

	if err := c.RecordBatch(testSessionID, data); err == nil {
		t.Fatal("expected oversized batch error")
	}
}

func TestRecordBatchSkipsInvalidEvents(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	c := NewCollector(db)

	data := []byte(`[
		{"event_type":"play","track_stem":"01-gathering"},
		{"event_type":"bogus","track_stem":"01-gathering"},
		{"event_type":"pause","track_stem":"../../etc/passwd"}
	]`)
	if err := c.RecordBatch(testSessionID, data); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	c.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE session_id=?", testSessionID).Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 valid event, got %d", count)
	}
}

func TestRecordBatchRejectsInvalidSessionID(t *testing.T) {
	c := testCollector(t)

	data := []byte(`[{"event_type":"play","track_stem":"01-gathering"}]`)
	if err := c.RecordBatch("invalid-session", data); err == nil {
		t.Fatal("expected invalid session error")
	}
}
