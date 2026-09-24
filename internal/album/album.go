package album

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"acetate/internal/albums"
)

type TrackInfo struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
	LyricFormat  string `json:"lyric_format,omitempty"`
}

// ValidateStem guards the path built from a stem; it deliberately allows any
// other filename character, since stems come from real files on disk and the
// callers separately check the stem against the album's track list.
func ValidateStem(stem string) bool {
	// Invalid UTF-8 would be substituted on the way out through JSON, so the stem
	// the client sends back would no longer match the one stored for the track.
	if stem == "" || len(stem) > 255 || !utf8.ValidString(stem) {
		return false
	}
	// Rejecting separators keeps the stem a single path element, and rejecting a
	// leading dot is then what stops that element from being ".." or ".".
	if strings.ContainsAny(stem, "/\\") || strings.HasPrefix(stem, ".") {
		return false
	}
	for _, r := range stem {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// GetTrackList builds the track list response with lyric format info.
func GetTrackList(tracks []albums.Track, albumPath string) []TrackInfo {
	out := make([]TrackInfo, 0, len(tracks))
	for _, t := range tracks {
		info := TrackInfo{
			Stem:         t.Stem,
			Title:        t.Title,
			DisplayIndex: t.DisplayIndex,
			LyricFormat:  detectLyricFormat(albumPath, t.Stem),
		}
		out = append(out, info)
	}
	return out
}

// Priority order must match ServeLyrics in lyrics.go.
func detectLyricFormat(albumPath, stem string) string {
	if _, err := os.Stat(filepath.Join(albumPath, stem+".lrc")); err == nil {
		return "lrc"
	}
	if _, err := os.Stat(filepath.Join(albumPath, stem+".txt")); err == nil {
		return "text"
	}
	if _, err := os.Stat(filepath.Join(albumPath, stem+".md")); err == nil {
		return "markdown"
	}
	return ""
}

// CoverOverrideName is the file an admin-uploaded cover is stored under, in
// dataPath/covers/<album id>/.
const CoverOverrideName = "cover_override.jpg"

// ServeCover serves the album's uploaded cover, else a cover image from the
// album folder.
func ServeCover(w http.ResponseWriter, r *http.Request, albumPath, dataPath string, albumID int64) {
	candidates := []string{filepath.Join(dataPath, "covers", strconv.FormatInt(albumID, 10), CoverOverrideName)}
	for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png"} {
		candidates = append(candidates, filepath.Join(albumPath, name))
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			serveCoverFile(w, r, path, info)
			return
		}
	}
	http.NotFound(w, r)
}

func serveCoverFile(w http.ResponseWriter, r *http.Request, path string, info os.FileInfo) {
	// ETag + modtime let http.ServeContent answer conditional requests (304).
	w.Header().Set("Cache-Control", "private, max-age=3600")

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().Unix(), info.Size()))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

func StreamTrack(w http.ResponseWriter, r *http.Request, albumPath, stem string) {
	mp3Path := filepath.Join(albumPath, stem+".mp3")
	info, err := os.Stat(mp3Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(mp3Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	// ETag lets http.ServeContent answer conditional and range requests.
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s-%d-%d", stem, info.ModTime().Unix(), info.Size())))
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, h.Sum(nil)[:8]))
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, stem+".mp3", time.Time{}, f)
}
