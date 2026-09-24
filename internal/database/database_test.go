package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify db file exists
	if _, err := os.Stat(filepath.Join(dir, "acetate.db")); err != nil {
		t.Fatalf("db file not created: %v", err)
	}

	// Verify tables exist
	tables := []string{"sessions", "admin_sessions", "admin_users", "events", "admin_auth_audit"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}

	// Verify we can insert and query
	_, err = db.Exec("INSERT INTO sessions (id, started_at, last_seen_at, ip_hash) VALUES (?, datetime('now'), datetime('now'), ?)", "test-session", "hash123")
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	var id string
	err = db.QueryRow("SELECT id FROM sessions WHERE id = ?", "test-session").Scan(&id)
	if err != nil {
		t.Fatalf("query session: %v", err)
	}
	if id != "test-session" {
		t.Errorf("expected test-session, got %s", id)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	db.Close()

	// Open again — migration should be idempotent
	db, err = Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	db.Close()
}

func TestForeignKeysCascade(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	res, err := db.Exec("INSERT INTO albums (slug, title, album_path) VALUES ('a', 'A', '/x')")
	if err != nil {
		t.Fatalf("insert album: %v", err)
	}
	albumID, _ := res.LastInsertId()
	if _, err := db.Exec("INSERT INTO album_tracks (album_id, stem, title) VALUES (?, 's', 'S')", albumID); err != nil {
		t.Fatalf("insert track: %v", err)
	}
	if _, err := db.Exec("DELETE FROM albums WHERE id = ?", albumID); err != nil {
		t.Fatalf("delete album: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM album_tracks").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("album_tracks rows after album delete = %d, want 0", n)
	}
}
