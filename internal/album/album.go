package album

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"

	"acetate/internal/config"
)

var stemRegexp = regexp.MustCompile(`^[a-zA-Z0-9 _'()\-]+$`)

type TrackInfo struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
	LyricFormat  string `json:"lyric_format,omitempty"`
}

func ValidateStem(stem string) bool {
	if stem == "" || strings.Contains(stem, "..") || strings.ContainsAny(stem, "/\\") {
		return false
	}
	return stemRegexp.MatchString(stem)
}

func StemInConfig(stem string, cfg config.Config) bool {
	for _, t := range cfg.Tracks {
		if t.Stem == stem {
			return true
		}
	}
	return false
}

func GetTrackList(cfg config.Config, albumPath string) []TrackInfo {
	tracks := make([]TrackInfo, 0, len(cfg.Tracks))
	for _, t := range cfg.Tracks {
		info := TrackInfo{
			Stem:         t.Stem,
			Title:        t.Title,
			DisplayIndex: t.DisplayIndex,
			LyricFormat:  detectLyricFormat(albumPath, t.Stem),
		}
		tracks = append(tracks, info)
	}
	return tracks
}

func detectLyricFormat(albumPath, stem string) string {
	if _, err := os.Stat(filepath.Join(albumPath, stem+".lrc")); err == nil {
		return "lrc"
	}
	if _, err := os.Stat(filepath.Join(albumPath, stem+".srt")); err == nil {
		return "lrc"
	}
	if _, err := os.Stat(filepath.Join(albumPath, stem+".md")); err == nil {
		return "markdown"
	}
	if _, err := os.Stat(filepath.Join(albumPath, stem+".txt")); err == nil {
		return "text"
	}
	return ""
}

func ServeCover(w http.ResponseWriter, r *http.Request, albumPath, dataPath string) {
	// Check for admin-uploaded override first.
	overridePath := filepath.Join(dataPath, "cover_override.jpg")
	if info, err := os.Stat(overridePath); err == nil {
		serveCoverFile(w, r, overridePath, info)
		return
	}

	// Fall back to album directory cover.
	for _, name := range []string{"cover.jpg", "cover.jpeg", "cover.png"} {
		coverPath := filepath.Join(albumPath, name)
		if info, err := os.Stat(coverPath); err == nil {
			serveCoverFile(w, r, coverPath, info)
			return
		}
	}

	http.NotFound(w, r)
}

func serveCoverFile(w http.ResponseWriter, r *http.Request, path string, info os.FileInfo) {
	etag := fmt.Sprintf(`"%x-%x"`, info.ModTime().Unix(), info.Size())
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

func StreamTrack(w http.ResponseWriter, r *http.Request, albumPath, stem string) {
	mp3Path := filepath.Join(albumPath, stem+".mp3")
	info, err := os.Stat(mp3Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// ETag for caching.
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s-%d-%d", stem, info.ModTime().Unix(), info.Size())))
	etag := fmt.Sprintf(`"%x"`, h.Sum(nil)[:8])
	w.Header().Set("ETag", etag)

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	f, err := os.Open(mp3Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, stem+".mp3", time.Time{}, f)
}

func ServeLyrics(w http.ResponseWriter, albumPath, stem string) interface{} {
	// Try .lrc first (synced lyrics).
	if data, err := os.ReadFile(filepath.Join(albumPath, stem+".lrc")); err == nil {
		resp := map[string]interface{}{
			"format":  "lrc",
			"content": string(data),
		}
		// Check for structure file: explicit .structure.* first, then fall back
		// to the plain .txt which typically carries verse/chorus annotations.
		if sData, err := os.ReadFile(filepath.Join(albumPath, stem+".structure.lrc")); err == nil {
			resp["structure_content"] = string(sData)
		} else if sData, err := os.ReadFile(filepath.Join(albumPath, stem+".structure.txt")); err == nil {
			resp["structure_content"] = string(sData)
		} else if sData, err := os.ReadFile(filepath.Join(albumPath, stem+".txt")); err == nil {
			resp["structure_content"] = string(sData)
		}
		return resp
	}

	// Try .srt (SubRip subtitle format, treated as lrc).
	if data, err := os.ReadFile(filepath.Join(albumPath, stem+".srt")); err == nil {
		return map[string]interface{}{
			"format":  "lrc",
			"content": string(data),
		}
	}

	// Try .md (markdown, rendered to HTML).
	if data, err := os.ReadFile(filepath.Join(albumPath, stem+".md")); err == nil {
		var buf bytes.Buffer
		if err := goldmark.Convert(data, &buf); err == nil {
			return map[string]interface{}{
				"format":  "markdown",
				"content": buf.String(),
			}
		}
		// Fallback to raw if goldmark fails.
		return map[string]interface{}{
			"format":  "text",
			"content": string(data),
		}
	}

	// Try .txt (plain text).
	if data, err := os.ReadFile(filepath.Join(albumPath, stem+".txt")); err == nil {
		return map[string]interface{}{
			"format":  "text",
			"content": strings.TrimSpace(string(data)),
		}
	}

	return nil
}
