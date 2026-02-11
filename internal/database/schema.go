package database

import (
	"database/sql"
	"fmt"
)

const schema = `
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
    user_agent_hash TEXT
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
    outcome TEXT NOT NULL,
    reason TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_track ON events(track_stem);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(event_type);
CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);
CREATE INDEX IF NOT EXISTS idx_sessions_last_seen ON sessions(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_rollups_day ON analytics_rollups_daily(day);
CREATE INDEX IF NOT EXISTS idx_rollups_track ON analytics_rollups_daily(track_stem);
CREATE INDEX IF NOT EXISTS idx_admin_auth_audit_occurred ON admin_auth_audit(occurred_at);
`

// Migrate applies the database schema.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
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
