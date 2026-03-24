package albums

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// MigrateFromConfigJSON imports data from a legacy config.json into the
// multi-album database tables. It runs once: if the albums table already
// has rows, it is a no-op. On success it renames config.json to
// config.json.migrated so the migration is not re-attempted.
func MigrateFromConfigJSON(db *sql.DB, dataPath, albumPath string) error {
	// Already migrated?
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM albums").Scan(&count); err != nil {
		return fmt.Errorf("check album count: %w", err)
	}
	if count > 0 {
		return nil
	}

	configPath := filepath.Join(dataPath, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return fmt.Errorf("read config.json: %w", err)
	}

	var cfg struct {
		Title    string `json:"title"`
		Artist   string `json:"artist"`
		Password string `json:"password"`
		Tracks   []struct {
			Stem         string `json:"stem"`
			Title        string `json:"title"`
			DisplayIndex string `json:"display_index"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config.json: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. Create album
	slug := generateSlug(cfg.Title)
	if slug == "" {
		slug = "album"
	}
	res, err := tx.Exec(
		"INSERT INTO albums (slug, title, artist, album_path) VALUES (?, ?, ?, ?)",
		slug, cfg.Title, cfg.Artist, albumPath,
	)
	if err != nil {
		return fmt.Errorf("insert album: %w", err)
	}
	albumID, _ := res.LastInsertId()

	// 2. Create tracks
	for i, t := range cfg.Tracks {
		if _, err := tx.Exec(
			"INSERT INTO album_tracks (album_id, stem, title, display_index, sort_order) VALUES (?, ?, ?, ?, ?)",
			albumID, t.Stem, t.Title, t.DisplayIndex, i,
		); err != nil {
			return fmt.Errorf("insert track %q: %w", t.Stem, err)
		}
	}

	// 3. Create password (if set)
	if strings.TrimSpace(cfg.Password) != "" {
		passwordHash := cfg.Password
		if !isLikelyBcryptHash(passwordHash) {
			hashed, err := bcrypt.GenerateFromPassword([]byte(passwordHash), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			passwordHash = string(hashed)
		}

		res, err := tx.Exec(
			"INSERT INTO listener_passwords (label, password_hash) VALUES (?, ?)",
			"Default", passwordHash,
		)
		if err != nil {
			return fmt.Errorf("insert password: %w", err)
		}
		passwordID, _ := res.LastInsertId()

		if _, err := tx.Exec(
			"INSERT INTO password_album_access (password_id, album_id) VALUES (?, ?)",
			passwordID, albumID,
		); err != nil {
			return fmt.Errorf("link password to album: %w", err)
		}

		// Backfill existing sessions with password_id
		if _, err := tx.Exec(
			"UPDATE sessions SET password_id = ? WHERE password_id IS NULL", passwordID,
		); err != nil {
			log.Printf("warning: backfill sessions password_id: %v", err)
		}
	}

	// 4. Backfill existing events with album_id
	if _, err := tx.Exec(
		"UPDATE events SET album_id = ? WHERE album_id IS NULL", albumID,
	); err != nil {
		log.Printf("warning: backfill events album_id: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	// Rename config.json so migration won't run again
	if err := os.Rename(configPath, configPath+".migrated"); err != nil {
		log.Printf("warning: could not rename config.json: %v", err)
	}

	log.Printf("migrated config.json → database: album=%q tracks=%d", cfg.Title, len(cfg.Tracks))
	return nil
}

func isLikelyBcryptHash(v string) bool {
	if len(v) < 59 {
		return false
	}
	return strings.HasPrefix(v, "$2a$") || strings.HasPrefix(v, "$2b$") || strings.HasPrefix(v, "$2y$")
}
