package config

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveTitle(t *testing.T) {
	tests := []struct {
		stem, want string
	}{
		{"01-gathering", "Gathering"},
		{"02-hollow-ground", "Hollow Ground"},
		{"03_the_descent", "The Descent"},
		{"gathering", "Gathering"},
		{"01-", "01"},
		{"track", "Track"},
	}
	for _, tt := range tests {
		got := deriveTitle(tt.stem)
		if got != tt.want {
			t.Errorf("deriveTitle(%q) = %q, want %q", tt.stem, got, tt.want)
		}
	}
}

func TestGenerateDefault(t *testing.T) {
	albumDir := t.TempDir()
	dataDir := t.TempDir()

	// Create test MP3 files
	for _, name := range []string{"02-hollow.mp3", "01-gathering.mp3", "cover.jpg"} {
		os.WriteFile(filepath.Join(albumDir, name), []byte("fake"), 0644)
	}

	mgr, err := NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := mgr.Get()
	if len(cfg.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(cfg.Tracks))
	}

	// Should be sorted alphabetically by stem
	if cfg.Tracks[0].Stem != "01-gathering" {
		t.Errorf("first track stem = %q, want 01-gathering", cfg.Tracks[0].Stem)
	}
	if cfg.Tracks[0].Title != "Gathering" {
		t.Errorf("first track title = %q, want Gathering", cfg.Tracks[0].Title)
	}
	if cfg.Tracks[1].Stem != "02-hollow" {
		t.Errorf("second track stem = %q, want 02-hollow", cfg.Tracks[1].Stem)
	}

	// Password should be empty
	if cfg.Password != "" {
		t.Errorf("password should be empty on first boot, got %q", cfg.Password)
	}
}

func TestGenerateDefaultPrefersMetadataTitle(t *testing.T) {
	albumDir := t.TempDir()
	dataDir := t.TempDir()

	taggedPath := filepath.Join(albumDir, "01-tagged.mp3")
	if err := os.WriteFile(taggedPath, makeID3v23TaggedMP3("A Real Track Name"), 0644); err != nil {
		t.Fatalf("write tagged mp3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(albumDir, "02-fallback.mp3"), []byte("fake"), 0644); err != nil {
		t.Fatalf("write fallback mp3: %v", err)
	}

	mgr, err := NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := mgr.Get()
	if len(cfg.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(cfg.Tracks))
	}

	if cfg.Tracks[0].Stem != "01-tagged" {
		t.Fatalf("first stem = %q, want 01-tagged", cfg.Tracks[0].Stem)
	}
	if cfg.Tracks[0].Title != "A Real Track Name" {
		t.Errorf("metadata title = %q, want A Real Track Name", cfg.Tracks[0].Title)
	}
	if cfg.Tracks[1].Stem != "02-fallback" {
		t.Fatalf("second stem = %q, want 02-fallback", cfg.Tracks[1].Stem)
	}
	if cfg.Tracks[1].Title != "Fallback" {
		t.Errorf("fallback title = %q, want Fallback", cfg.Tracks[1].Title)
	}
}

func TestUpdateAndReload(t *testing.T) {
	albumDir := t.TempDir()
	dataDir := t.TempDir()
	os.WriteFile(filepath.Join(albumDir, "01-test.mp3"), []byte("fake"), 0644)

	mgr, err := NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := mgr.Get()
	cfg.Title = "New Title"
	cfg.Password = "$2a$10$test"

	if err := mgr.Update(cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify in memory
	got := mgr.Get()
	if got.Title != "New Title" {
		t.Errorf("title = %q, want New Title", got.Title)
	}

	// Verify persisted to disk by creating a new manager
	mgr2, err := NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("NewManager reload: %v", err)
	}
	got2 := mgr2.Get()
	if got2.Title != "New Title" {
		t.Errorf("reloaded title = %q, want New Title", got2.Title)
	}
	if got2.Password != "$2a$10$test" {
		t.Errorf("reloaded password = %q, want $2a$10$test", got2.Password)
	}
}

func TestGetReturnsDeepCopy(t *testing.T) {
	albumDir := t.TempDir()
	dataDir := t.TempDir()
	os.WriteFile(filepath.Join(albumDir, "01-test.mp3"), []byte("fake"), 0644)

	mgr, err := NewManager(dataDir, albumDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := mgr.Get()
	cfg.Tracks[0].Title = "Mutated"

	fresh := mgr.Get()
	if fresh.Tracks[0].Title == "Mutated" {
		t.Fatal("Get should return a deep copy of tracks")
	}
}

func makeID3v23TaggedMP3(title string) []byte {
	payload := append([]byte{0x03}, []byte(title)...) // UTF-8

	frame := make([]byte, 10+len(payload))
	copy(frame[0:4], []byte("TIT2"))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[10:], payload)

	tagSize := len(frame)
	header := []byte{
		'I', 'D', '3',
		0x03, 0x00, 0x00, // v2.3.0, flags 0
		byte((tagSize >> 21) & 0x7f),
		byte((tagSize >> 14) & 0x7f),
		byte((tagSize >> 7) & 0x7f),
		byte(tagSize & 0x7f),
	}

	data := append(header, frame...)
	data = append(data, []byte{0x00, 0x00, 0x00, 0x00}...) // fake audio bytes
	return data
}
