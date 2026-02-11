package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open creates or opens the SQLite database at the given data path.
// It enables WAL mode and sets a busy timeout for concurrent access.
func Open(dataPath string) (*sql.DB, error) {
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	dbPath := filepath.Join(dataPath, "acetate.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Keep a small pool so reads can proceed while writes are serialized by SQLite/WAL.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	if err := Migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return db, nil
}
