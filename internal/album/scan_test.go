package album

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
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
		{"04-élan", "Élan"},
	}
	for _, tt := range tests {
		if got := deriveTitle(tt.stem); got != tt.want {
			t.Errorf("deriveTitle(%q) = %q, want %q", tt.stem, got, tt.want)
		}
	}
}

// id3v23 builds an ID3v2.3 tag holding the given frames, followed by fake audio.
func id3v23(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	n := len(body)
	header := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	return append(append(header, body...), 0xff, 0xfb, 0, 0)
}

func frame(id string, payload []byte) []byte {
	f := make([]byte, 10+len(payload))
	copy(f, id)
	binary.BigEndian.PutUint32(f[4:8], uint32(len(payload)))
	copy(f[10:], payload)
	return f
}

func TestScanTracksTitles(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		// Artwork before the title must be skipped, not decoded.
		"01-tagged.mp3": id3v23(frame("APIC", make([]byte, 5000)), frame("TIT2", append([]byte{3}, "A Real Track"...))),
		"02-latin.mp3":  id3v23(frame("TIT2", []byte{0, 'C', 'a', 'f', 0xe9})),
		"03-plain.mp3":  []byte("fake"),
		"04-UPPER.MP3":  []byte("fake"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	tracks, err := ScanTracks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tr := range tracks {
		got[tr.Stem] = tr.Title
	}
	want := map[string]string{"01-tagged": "A Real Track", "02-latin": "Café", "03-plain": "Plain"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tracks = %v, want %v", got, want)
	}
}

func TestScanTracksSkipsUnrequestableNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Real Track.mp3", "Wait....mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0644); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	// Some mounts refuse a non-UTF-8 name outright, which is just as good as skipping it.
	for _, name := range []string{"._Real Track.mp3", ".hidden.mp3", ".mp3", "Intro .mp3", "Caf\xe9.mp3"} {
		os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0644)
	}

	tracks, err := ScanTracks(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		got = append(got, tr.Stem)
	}
	want := []string{"Real Track", "Wait..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stems = %q, want %q", got, want)
	}
}
