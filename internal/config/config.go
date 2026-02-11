package config

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
)

// Track represents a single track in the album.
type Track struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
}

// Config represents the album configuration.
type Config struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Password string  `json:"password"`
	Tracks   []Track `json:"tracks"`
}

// Manager provides thread-safe access to the album configuration.
// It caches config in memory with RWMutex protection.
type Manager struct {
	mu       sync.RWMutex
	config   *Config
	dataPath string
}

// NewManager creates a config manager. If config.json doesn't exist,
// it generates one by scanning the album directory for MP3 files.
func NewManager(dataPath, albumPath string) (*Manager, error) {
	m := &Manager{dataPath: dataPath}

	configPath := filepath.Join(dataPath, "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := m.generateDefault(albumPath); err != nil {
			return nil, fmt.Errorf("generate default config: %w", err)
		}
	}

	if err := m.load(); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return m, nil
}

// Get returns a copy of the current configuration.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(*m.config)
}

// Update writes a new configuration to disk and reloads it into memory.
func (m *Manager) Update(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.save(&cfg); err != nil {
		return err
	}

	cloned := cloneConfig(cfg)
	m.config = &cloned
	return nil
}

// Reload re-reads config.json from disk.
func (m *Manager) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() error {
	configPath := filepath.Join(m.dataPath, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	cloned := cloneConfig(cfg)
	m.config = &cloned
	return nil
}

func (m *Manager) save(cfg *Config) error {
	if err := os.MkdirAll(m.dataPath, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	configPath := filepath.Join(m.dataPath, "config.json")
	return os.WriteFile(configPath, data, 0644)
}

var numericPrefixRe = regexp.MustCompile(`^\d+[-_]?`)

func (m *Manager) generateDefault(albumPath string) error {
	tracks, err := ScanAlbumTracks(albumPath)
	if err != nil {
		return err
	}

	cfg := Config{
		Title:  "Album Title",
		Artist: "Artist Name",
		Tracks: tracks,
	}

	return m.save(&cfg)
}

// ScanAlbumTracks reads MP3 files from disk and returns a sorted default track list.
func ScanAlbumTracks(albumPath string) ([]Track, error) {
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		return nil, fmt.Errorf("scan album directory: %w", err)
	}

	var tracks []Track
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".mp3") {
			continue
		}

		stem := strings.TrimSuffix(name, filepath.Ext(name))
		title := deriveTitleFromMetadata(filepath.Join(albumPath, name), stem)
		tracks = append(tracks, Track{Stem: stem, Title: title})
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].Stem < tracks[j].Stem
	})

	return tracks, nil
}

func deriveTitleFromMetadata(mp3Path, stem string) string {
	if title, err := readMP3Title(mp3Path); err == nil {
		title = strings.TrimSpace(title)
		if title != "" {
			return title
		}
	}
	return deriveTitle(stem)
}

func readMP3Title(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}

	// Try ID3v2 first (header at start of file).
	if title, ok, err := readID3v2Title(f); err != nil {
		return "", err
	} else if ok {
		return title, nil
	}

	// Fallback to ID3v1 (TAG block at end of file).
	if stat.Size() >= 128 {
		if title, ok, err := readID3v1Title(f, stat.Size()); err != nil {
			return "", err
		} else if ok {
			return title, nil
		}
	}

	return "", nil
}

func readID3v2Title(rs io.ReadSeeker) (string, bool, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}

	header := make([]byte, 10)
	if _, err := io.ReadFull(rs, header); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", false, nil
		}
		return "", false, err
	}
	if !bytes.Equal(header[:3], []byte("ID3")) {
		return "", false, nil
	}

	version := header[3]
	tagSize := decodeSyncSafeInt(header[6:10])
	if tagSize <= 0 {
		return "", false, nil
	}

	tagData := make([]byte, tagSize)
	if _, err := io.ReadFull(rs, tagData); err != nil {
		return "", false, nil
	}

	title := extractID3v2Title(version, tagData)
	if strings.TrimSpace(title) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(title), true, nil
}

func readID3v1Title(rs io.ReadSeeker, fileSize int64) (string, bool, error) {
	if _, err := rs.Seek(fileSize-128, io.SeekStart); err != nil {
		return "", false, err
	}
	buf := make([]byte, 128)
	if _, err := io.ReadFull(rs, buf); err != nil {
		return "", false, err
	}
	if !bytes.Equal(buf[:3], []byte("TAG")) {
		return "", false, nil
	}

	title := strings.TrimRight(string(buf[3:33]), "\x00 ")
	title = strings.TrimSpace(title)
	if title == "" {
		return "", false, nil
	}
	return title, true, nil
}

func decodeSyncSafeInt(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func extractID3v2Title(version byte, data []byte) string {
	pos := 0
	for pos+10 <= len(data) {
		frameHeader := data[pos : pos+10]
		if bytes.Equal(frameHeader, make([]byte, 10)) {
			break
		}

		frameID := string(frameHeader[:4])
		if strings.TrimSpace(frameID) == "" {
			break
		}

		var frameSize int
		if version == 4 {
			frameSize = decodeSyncSafeInt(frameHeader[4:8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(frameHeader[4:8]))
		}
		if frameSize <= 0 {
			pos += 10
			continue
		}

		start := pos + 10
		end := start + frameSize
		if end > len(data) {
			break
		}

		if frameID == "TIT2" {
			return decodeID3TextFrame(data[start:end])
		}

		pos = end
	}
	return ""
}

func decodeID3TextFrame(frame []byte) string {
	if len(frame) == 0 {
		return ""
	}

	encoding := frame[0]
	payload := frame[1:]
	if len(payload) == 0 {
		return ""
	}

	switch encoding {
	case 0: // ISO-8859-1
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		return strings.TrimSpace(string(payload))
	case 3: // UTF-8
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		return strings.TrimSpace(string(payload))
	case 1, 2: // UTF-16 (with BOM for 1, BE for 2)
		return decodeUTF16Text(payload, encoding == 1)
	default:
		return ""
	}
}

func decodeUTF16Text(payload []byte, withBOM bool) string {
	if len(payload) < 2 {
		return ""
	}

	var order binary.ByteOrder = binary.BigEndian
	start := 0
	if withBOM && len(payload) >= 2 {
		switch {
		case payload[0] == 0xff && payload[1] == 0xfe:
			order = binary.LittleEndian
			start = 2
		case payload[0] == 0xfe && payload[1] == 0xff:
			order = binary.BigEndian
			start = 2
		}
	}

	payload = payload[start:]
	u16 := make([]uint16, 0, len(payload)/2)
	for i := 0; i+1 < len(payload); i += 2 {
		v := order.Uint16(payload[i : i+2])
		if v == 0 {
			break
		}
		u16 = append(u16, v)
	}
	if len(u16) == 0 {
		return ""
	}

	return strings.TrimSpace(string(utf16.Decode(u16)))
}

// deriveTitle converts a stem like "01-gathering" to "Gathering".
func deriveTitle(stem string) string {
	// Strip numeric prefix
	title := numericPrefixRe.ReplaceAllString(stem, "")
	if title == "" {
		title = stem
	}
	// Replace hyphens and underscores with spaces
	title = strings.NewReplacer("-", " ", "_", " ").Replace(title)
	title = strings.TrimSpace(title)
	// Title case
	words := strings.Fields(title)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	if len(words) == 0 {
		return stem
	}
	return strings.Join(words, " ")
}

func cloneConfig(cfg Config) Config {
	out := cfg
	if cfg.Tracks != nil {
		out.Tracks = make([]Track, len(cfg.Tracks))
		copy(out.Tracks, cfg.Tracks)
	}
	return out
}
