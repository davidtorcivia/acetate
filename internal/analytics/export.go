package analytics

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
)

// ExportEvent represents a single raw analytics event for exports.
type ExportEvent struct {
	ID              int64   `json:"id"`
	SessionID       string  `json:"session_id"`
	EventType       string  `json:"event_type"`
	TrackStem       string  `json:"track_stem,omitempty"`
	PositionSeconds float64 `json:"position_seconds,omitempty"`
	Metadata        string  `json:"metadata,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

// GetEventsForExport returns raw events ordered by creation time with optional filters.
func GetEventsForExport(db *sql.DB, filter QueryFilter, limit int) ([]ExportEvent, error) {
	filter = normalizeFilter(filter)

	where := []string{"1=1"}
	args := make([]interface{}, 0, 8)
	appendTimeFilter(&where, &args, "created_at", filter)
	appendStemFilter(&where, &args, "track_stem", filter.Stems)
	appendEventTypeFilter(&where, &args, "event_type", filter.EventTypes)

	query := `
		SELECT id, session_id, event_type, COALESCE(track_stem, ''), COALESCE(position_seconds, 0), COALESCE(metadata, '{}'), created_at
		FROM events
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY created_at ASC, id ASC
	`
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query export events: %w", err)
	}
	defer rows.Close()

	events := make([]ExportEvent, 0, 256)
	for rows.Next() {
		var e ExportEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.EventType, &e.TrackStem, &e.PositionSeconds, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan export event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// MarshalEventsJSON serializes events as a JSON document.
func MarshalEventsJSON(events []ExportEvent) ([]byte, error) {
	return json.MarshalIndent(events, "", "  ")
}

// MarshalEventsCSV serializes events as CSV.
func MarshalEventsCSV(events []ExportEvent) ([]byte, error) {
	buf := &bytes.Buffer{}
	w := csv.NewWriter(buf)

	if err := w.Write([]string{
		"id",
		"session_id",
		"event_type",
		"track_stem",
		"position_seconds",
		"metadata",
		"created_at",
	}); err != nil {
		return nil, err
	}

	for _, e := range events {
		record := []string{
			fmt.Sprintf("%d", e.ID),
			e.SessionID,
			e.EventType,
			e.TrackStem,
			fmt.Sprintf("%.3f", e.PositionSeconds),
			e.Metadata,
			e.CreatedAt,
		}
		if err := w.Write(record); err != nil {
			return nil, err
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
