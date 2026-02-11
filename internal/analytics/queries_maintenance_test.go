package analytics

import (
	"testing"
	"time"

	"acetate/internal/database"
)

func TestGetTrackStatsFiltered(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, _ = db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES (?, 'play', '01-a', ?)", "s1", "2026-01-01 00:00:00")
	_, _ = db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES (?, 'play', '02-b', ?)", "s2", "2026-01-01 00:00:00")

	stats, err := GetTrackStatsFiltered(db, QueryFilter{Stems: []string{"01-a"}})
	if err != nil {
		t.Fatalf("GetTrackStatsFiltered: %v", err)
	}
	if len(stats) != 1 || stats[0].Stem != "01-a" {
		t.Fatalf("unexpected stats result: %+v", stats)
	}
}

func TestRunMaintenanceRollupAndPrune(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, _ = db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES (?, 'play', '01-a', ?)", "s1", "2025-01-01 10:00:00")
	_, _ = db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES (?, 'complete', '01-a', ?)", "s1", "2025-01-01 10:03:00")

	now := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)
	res, err := RunMaintenance(db, now, 365)
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.RolledDays == 0 {
		t.Fatalf("expected rolled days > 0, got %+v", res)
	}

	var rollupCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM analytics_rollups_daily WHERE day = '2025-01-01'").Scan(&rollupCount); err != nil {
		t.Fatalf("query rollups: %v", err)
	}
	if rollupCount == 0 {
		t.Fatal("expected rollup rows for 2025-01-01")
	}

	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&remaining); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected pruned events, remaining=%d", remaining)
	}
}
