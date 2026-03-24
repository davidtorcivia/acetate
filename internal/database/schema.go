package database

import (
	"database/sql"
	"fmt"
)

const schemaTables = `
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    started_at DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL,
    ip_hash TEXT
);

CREATE TABLE IF NOT EXISTS admin_sessions (
    id TEXT PRIMARY KEY,
    created_at DATETIME NOT NULL,
    last_seen_at DATETIME,
    ip_hash TEXT,
    user_agent_hash TEXT,
    user_id INTEGER
);

CREATE TABLE IF NOT EXISTS admin_users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1,
    require_password_reset INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_login_at DATETIME
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    track_stem TEXT,
    position_seconds REAL,
    metadata TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS analytics_rollups_daily (
    day TEXT NOT NULL,
    track_stem TEXT NOT NULL,
    event_type TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    PRIMARY KEY (day, track_stem, event_type)
);

CREATE TABLE IF NOT EXISTS admin_auth_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    client_ip_hash TEXT,
    user_agent_hash TEXT,
    attempted_username TEXT,
    outcome TEXT NOT NULL,
    reason TEXT
);

CREATE TABLE IF NOT EXISTS albums (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    artist TEXT NOT NULL DEFAULT '',
    album_path TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS album_tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    stem TEXT NOT NULL,
    title TEXT NOT NULL,
    display_index TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    UNIQUE(album_id, stem)
);

CREATE TABLE IF NOT EXISTS listener_passwords (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    label TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS password_album_access (
    password_id INTEGER NOT NULL REFERENCES listener_passwords(id) ON DELETE CASCADE,
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    PRIMARY KEY (password_id, album_id)
);
`

// Migrate applies the database schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaTables); err != nil {
		return err
	}

	if err := ensureColumnExists(db, "admin_sessions", "last_seen_at", "DATETIME"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "admin_sessions", "ip_hash", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "admin_sessions", "user_agent_hash", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "admin_sessions", "user_id", "INTEGER"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "admin_auth_audit", "attempted_username", "TEXT"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "admin_users", "require_password_reset", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	// Multi-album columns on existing tables
	if err := ensureColumnExists(db, "sessions", "password_id", "INTEGER"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "events", "album_id", "INTEGER"); err != nil {
		return err
	}
	if err := ensureColumnExists(db, "analytics_rollups_daily", "album_id", "INTEGER"); err != nil {
		return err
	}

	if err := ensureIndexes(db); err != nil {
		return err
	}

	return nil
}

func ensureIndexes(db *sql.DB) error {
	stmts := []string{
		"CREATE INDEX IF NOT EXISTS idx_events_track ON events(track_stem)",
		"CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type)",
		"CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id)",
		"CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_sessions_last_seen ON sessions(last_seen_at)",
		"CREATE INDEX IF NOT EXISTS idx_admin_sessions_user ON admin_sessions(user_id)",
		"CREATE INDEX IF NOT EXISTS idx_admin_users_username ON admin_users(username)",
		"CREATE INDEX IF NOT EXISTS idx_admin_users_active ON admin_users(is_active)",
		"CREATE INDEX IF NOT EXISTS idx_rollups_day ON analytics_rollups_daily(day)",
		"CREATE INDEX IF NOT EXISTS idx_rollups_track ON analytics_rollups_daily(track_stem)",
		"CREATE INDEX IF NOT EXISTS idx_admin_auth_audit_occurred ON admin_auth_audit(occurred_at)",
		// Multi-album indexes
		"CREATE INDEX IF NOT EXISTS idx_albums_slug ON albums(slug)",
		"CREATE INDEX IF NOT EXISTS idx_album_tracks_album ON album_tracks(album_id)",
		"CREATE INDEX IF NOT EXISTS idx_album_tracks_album_sort ON album_tracks(album_id, sort_order)",
		"CREATE INDEX IF NOT EXISTS idx_password_album_access_password ON password_album_access(password_id)",
		"CREATE INDEX IF NOT EXISTS idx_password_album_access_album ON password_album_access(album_id)",
		"CREATE INDEX IF NOT EXISTS idx_sessions_password ON sessions(password_id)",
		"CREATE INDEX IF NOT EXISTS idx_events_album ON events(album_id)",
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	return nil
}

func ensureColumnExists(db *sql.DB, table, column, def string) error {
	exists, err := columnExists(db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def)
	_, err = db.Exec(stmt)
	return err
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			defValue   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}

	return false, rows.Err()
}
