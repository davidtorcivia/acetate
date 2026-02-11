package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	entries, err := os.ReadDir(albumPath)
	if err != nil {
		return fmt.Errorf("scan album directory: %w", err)
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
		title := deriveTitle(stem)
		tracks = append(tracks, Track{Stem: stem, Title: title})
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].Stem < tracks[j].Stem
	})

	cfg := Config{
		Title:  "Album Title",
		Artist: "Artist Name",
		Tracks: tracks,
	}

	return m.save(&cfg)
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
