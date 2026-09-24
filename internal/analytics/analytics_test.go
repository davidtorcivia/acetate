package analytics

import (
	"encoding/json"
	"testing"
	"time"

	"acetate/internal/database"
)

const testSessionID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testStems = map[string]bool{"01-gathering": true, "01. Intro": true}

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

	if err := c.RecordBatch(testSessionID, data, 0, testStems); err != nil {
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

	stats, err := GetTrackStatsFiltered(db, QueryFilter{})
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

	if err := c.RecordBatch(testSessionID, data, 0, testStems); err == nil {
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
	if err := c.RecordBatch(testSessionID, data, 0, testStems); err != nil {
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
	if err := c.RecordBatch("invalid-session", data, 0, testStems); err == nil {
		t.Fatal("expected invalid session error")
	}
}

func TestRecordBatchAcceptsSeekDottedStemsAndRejectsUnknownStems(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	c := NewCollector(db)

	data := []byte(`[
		{"event_type":"seek","track_stem":"01. Intro","position_seconds":12,"metadata":{"from":3,"to":12}},
		{"event_type":"pause","track_stem":"01. Intro","position_seconds":12,"metadata":{"duration":200}},
		{"event_type":"play","track_stem":"not-in-album"},
		{"event_type":"pause","track_stem":"01. Intro","position_seconds":12,"metadata":{"duration":-1}}
	]`)
	if err := c.RecordBatch(testSessionID, data, 0, testStems); err != nil {
		t.Fatalf("RecordBatch: %v", err)
	}
	c.Close()

	var types string
	db.QueryRow("SELECT group_concat(event_type) FROM events WHERE session_id = ?", testSessionID).Scan(&types)
	if types != "seek,pause" {
		t.Fatalf("stored events = %q, want seek,pause", types)
	}
}

func TestRecordBatchDeadlineCoversWholeBatch(t *testing.T) {
	// No flush loop: the channel stays full, so every high-value event must drop.
	c := &Collector{events: make(chan Event, 1), done: make(chan struct{})}
	c.events <- Event{EventType: "heartbeat"}

	data := []byte(`[
		{"event_type":"play","track_stem":"01-gathering"},
		{"event_type":"play","track_stem":"01-gathering"},
		{"event_type":"play","track_stem":"01-gathering"}
	]`)
	done := make(chan error, 1)
	go func() { done <- c.RecordBatch(testSessionID, data, 0, testStems) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RecordBatch still blocked after 2s: the deadline fired only for the first event")
	}
	if c.DroppedCount() != 3 {
		t.Fatalf("dropped %d, want 3", c.DroppedCount())
	}
}

func TestDropoutHeatmapBinsByDuration(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, e := range []struct {
		pos  float64
		meta string
	}{
		{30, `{"duration":300}`},    // 10% -> bin 1
		{150, `{"duration":300}`},   // 50% -> bin 5
		{100, `{"duration":1e-18}`}, // absurd ratio stored before validation -> last bin, no panic
		{60, `{}`},                  // no duration: falls back to the longest known (300) -> bin 2
	} {
		db.Exec("INSERT INTO events (session_id, event_type, track_stem, position_seconds, metadata) VALUES ('s', 'dropout', 'a', ?, ?)", e.pos, e.meta)
	}

	bins, err := GetDropoutHeatmapFiltered(db, "a", QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int, len(bins))
	for i, b := range bins {
		got[i] = b.Count
	}
	want := []int{0, 1, 1, 0, 0, 1, 0, 0, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bins = %v, want %v", got, want)
		}
	}
}
