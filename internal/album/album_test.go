package album

import (
	"os"
	"path/filepath"
	"testing"

	"acetate/internal/albums"
)

func TestValidateStem(t *testing.T) {
	valid := []string{"01-gathering", "track_name", "MyTrack", "a1b2c3", "track name", "Track (Live)"}
	invalid := []string{"../etc/passwd", "track.mp3", "track/name", "", "track\x00name", ".", ".."}

	for _, s := range valid {
		if !ValidateStem(s) {
			t.Errorf("ValidateStem(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidateStem(s) {
			t.Errorf("ValidateStem(%q) = true, want false", s)
		}
	}
}

func TestStemInTracks(t *testing.T) {
	tracks := []albums.Track{
		{Stem: "01-gathering", Title: "Gathering"},
		{Stem: "02-hollow", Title: "Hollow"},
	}

	if !StemInTracks("01-gathering", tracks) {
		t.Error("01-gathering should be in tracks")
	}
	if StemInTracks("03-unknown", tracks) {
		t.Error("03-unknown should not be in tracks")
	}
}

func TestDetectLyricFormat(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	os.WriteFile(filepath.Join(dir, "track1.lrc"), []byte("[00:00.00] test"), 0644)
	os.WriteFile(filepath.Join(dir, "track2.txt"), []byte("plain text"), 0644)
	os.WriteFile(filepath.Join(dir, "track3.md"), []byte("# markdown"), 0644)

	tests := []struct {
		stem, want string
	}{
		{"track1", "lrc"},
		{"track2", "text"},
		{"track3", "markdown"},
		{"track4", ""},
	}

	for _, tt := range tests {
		got := detectLyricFormat(dir, tt.stem)
		if got != tt.want {
			t.Errorf("detectLyricFormat(%q) = %q, want %q", tt.stem, got, tt.want)
		}
	}
}

func TestGetTrackList(t *testing.T) {
	dir := t.TempDir()

	// Create test files
	os.WriteFile(filepath.Join(dir, "01-gathering.mp3"), make([]byte, 240000), 0644) // ~10s at 192kbps
	os.WriteFile(filepath.Join(dir, "01-gathering.lrc"), []byte("[00:00.00] test"), 0644)
	os.WriteFile(filepath.Join(dir, "02-hollow.mp3"), make([]byte, 120000), 0644)

	tracks := []albums.Track{
		{Stem: "01-gathering", Title: "Gathering"},
		{Stem: "02-hollow", Title: "Hollow"},
	}

	result := GetTrackList(tracks, dir)
	if len(result) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(result))
	}

	if result[0].LyricFormat != "lrc" {
		t.Errorf("track 0 lyric format = %q, want lrc", result[0].LyricFormat)
	}
	if result[1].LyricFormat != "" {
		t.Errorf("track 1 lyric format = %q, want empty", result[1].LyricFormat)
	}
}

func TestLyricFormatPriority(t *testing.T) {
	dir := t.TempDir()

	// Create both lrc and txt for same stem — lrc should win
	os.WriteFile(filepath.Join(dir, "track.lrc"), []byte("[00:00.00] synced"), 0644)
	os.WriteFile(filepath.Join(dir, "track.txt"), []byte("plain"), 0644)

	got := detectLyricFormat(dir, "track")
	if got != "lrc" {
		t.Errorf("expected lrc to take priority, got %q", got)
	}
}
