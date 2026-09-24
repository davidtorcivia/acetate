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

func TestRunMaintenancePrunes(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	now := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)
	db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s1', 'play', '01-a', '2025-01-01 10:00:00')")
	db.Exec("INSERT INTO events (session_id, event_type, track_stem, created_at) VALUES ('s1', 'play', '01-a', '2026-02-10 10:00:00')")
	db.Exec("INSERT INTO admin_auth_audit (outcome, occurred_at) VALUES ('success', '2025-01-01 00:00:00')")
	db.Exec("INSERT INTO admin_auth_audit (outcome, occurred_at) VALUES ('success', '2026-02-10 00:00:00')")

	res, err := RunMaintenance(db, now, 0)
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.PrunedRows != 0 || res.PrunedAuditRows != 1 {
		t.Fatalf("retention 0: %+v, want 0 events and 1 audit row pruned", res)
	}

	res, err = RunMaintenance(db, now, 365)
	if err != nil {
		t.Fatalf("RunMaintenance: %v", err)
	}
	if res.PrunedRows != 1 {
		t.Fatalf("retention 365: pruned %d events, want 1", res.PrunedRows)
	}
}
