package album

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"
)

// LyricsResponse is the JSON response for lyrics.
type LyricsResponse struct {
	Format           string `json:"format"`
	Content          string `json:"content"`
	StructureFormat  string `json:"structure_format,omitempty"`
	StructureContent string `json:"structure_content,omitempty"`
}

// ServeLyrics finds and serves lyrics for a track stem.
func ServeLyrics(w http.ResponseWriter, albumPath, stem string) *LyricsResponse {
	// Priority: lrc > txt > md
	checks := []struct {
		ext    string
		format string
	}{
		{".lrc", "lrc"},
		{".txt", "text"},
		{".md", "markdown"},
	}

	for _, c := range checks {
		path := filepath.Join(albumPath, stem+c.ext)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		content := string(data)

		if c.format == "markdown" {
			content = renderMarkdown(data)
		}

		resp := &LyricsResponse{
			Format:  c.format,
			Content: content,
		}
		// When LRC is primary, optionally load companion text/markdown for section labels
		// like [Verse], [Chorus], and intentional spacing.
		if c.format == "lrc" {
			if auxFormat, auxContent, ok := loadStructureLyrics(albumPath, stem); ok {
				resp.StructureFormat = auxFormat
				resp.StructureContent = auxContent
			}
		}
		return resp
	}

	return nil
}

func loadStructureLyrics(albumPath, stem string) (string, string, bool) {
	checks := []struct {
		ext    string
		format string
	}{
		{".txt", "text"},
		{".md", "markdown"},
	}
	for _, c := range checks {
		path := filepath.Join(albumPath, stem+c.ext)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		return c.format, content, true
	}
	return "", "", false
}

var (
	allHTMLTags = regexp.MustCompile(`(?is)<[^>]+>`)
)

// renderMarkdown converts markdown to safe HTML using goldmark.
// Raw HTML is disabled by default (goldmark's secure default).
func renderMarkdown(src []byte) string {
	md := goldmark.New(
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
		),
	)

	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return string(src) // fallback to raw text
	}

	// Preserve only simple typography tags used by the frontend styles.
	html := allHTMLTags.ReplaceAllStringFunc(buf.String(), func(tag string) string {
		return normalizeAllowedTag(tag)
	})
	return html
}

func normalizeAllowedTag(tag string) string {
	raw := strings.TrimSpace(strings.ToLower(tag))
	if raw == "" {
		return ""
	}

	isClosing := strings.HasPrefix(raw, "</")
	raw = strings.TrimPrefix(raw, "</")
	raw = strings.TrimPrefix(raw, "<")
	raw = strings.TrimSuffix(raw, ">")
	raw = strings.TrimLeft(raw, "/")
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}

	name := parts[0]
	switch name {
	case "p", "em", "strong":
		if isClosing {
			return "</" + name + ">"
		}
		return "<" + name + ">"
	case "br":
		return "<br>"
	default:
		return ""
	}
}
