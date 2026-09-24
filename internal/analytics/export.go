package analytics

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ExportEvent represents a single raw analytics event for exports.
type ExportEvent struct {
	ID              int64   `json:"id"`
	SessionID       string  `json:"session_id"`
	AlbumID         int64   `json:"album_id,omitempty"`
	EventType       string  `json:"event_type"`
	TrackStem       string  `json:"track_stem,omitempty"`
	PositionSeconds float64 `json:"position_seconds,omitempty"`
	Metadata        string  `json:"metadata,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

// ExportEvents streams raw events, oldest first, to w as "json" (an array)
// or "csv". A limit of 0 exports everything that matches the filter. start
// runs once the query has succeeded and before anything is written, so a
// caller can still send an error response when it fails.
func ExportEvents(w io.Writer, db *sql.DB, filter QueryFilter, limit int, format string, start func()) error {
	where, args := eventFilter(normalizeFilter(filter))
	query := `
		SELECT e.id, e.session_id, COALESCE(e.album_id, 0), e.event_type, COALESCE(e.track_stem, ''),
			COALESCE(e.position_seconds, 0), COALESCE(e.metadata, '{}'), e.created_at
		FROM events e
		WHERE ` + where + `
		ORDER BY e.created_at ASC, e.id ASC`
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("query export events: %w", err)
	}
	defer rows.Close()
	start()

	var write func(ExportEvent) error
	var finish func() error
	switch format {
	case "csv":
		cw := csv.NewWriter(w)
		if err := cw.Write([]string{"id", "session_id", "album_id", "event_type", "track_stem", "position_seconds", "metadata", "created_at"}); err != nil {
			return err
		}
		write = func(e ExportEvent) error {
			return cw.Write([]string{
				strconv.FormatInt(e.ID, 10),
				e.SessionID,
				strconv.FormatInt(e.AlbumID, 10),
				e.EventType,
				csvSafe(e.TrackStem),
				strconv.FormatFloat(e.PositionSeconds, 'f', 3, 64),
				csvSafe(e.Metadata),
				e.CreatedAt,
			})
		}
		finish = func() error { cw.Flush(); return cw.Error() }
	case "json":
		enc := json.NewEncoder(w)
		sep := "["
		write = func(e ExportEvent) error {
			if _, err := io.WriteString(w, sep); err != nil {
				return err
			}
			sep = ","
			return enc.Encode(e)
		}
		finish = func() error {
			if sep == "[" {
				_, err := io.WriteString(w, "[]\n")
				return err
			}
			_, err := io.WriteString(w, "]\n")
			return err
		}
	default:
		return fmt.Errorf("unknown export format %q", format)
	}

	for rows.Next() {
		var e ExportEvent
		if err := rows.Scan(&e.ID, &e.SessionID, &e.AlbumID, &e.EventType, &e.TrackStem, &e.PositionSeconds, &e.Metadata, &e.CreatedAt); err != nil {
			return fmt.Errorf("scan export event: %w", err)
		}
		if err := write(e); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return finish()
}

// csvSafe stops spreadsheet apps from evaluating listener-supplied text as a formula.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
