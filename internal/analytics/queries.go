package analytics

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TrackStats holds per-track analytics.
type TrackStats struct {
	Stem           string  `json:"stem"`
	TotalPlays     int     `json:"total_plays"`
	UniqueSessions int     `json:"unique_sessions"`
	Completions    int     `json:"completions"`
	CompletionRate float64 `json:"completion_rate"`
}

// DropoutBin represents a bin in the dropout heatmap.
type DropoutBin struct {
	BinStart float64 `json:"bin_start"` // 0.0 - 0.9
	BinEnd   float64 `json:"bin_end"`   // 0.1 - 1.0
	Count    int     `json:"count"`
}

// SessionInfo represents a session in the timeline.
type SessionInfo struct {
	SessionID   string `json:"session_id"`
	StartedAt   string `json:"started_at"`
	LastSeenAt  string `json:"last_seen_at"`
	TracksHeard int    `json:"tracks_heard"`
	IPHash      string `json:"ip_hash"`
}

// OverallStats holds aggregate analytics.
type OverallStats struct {
	TotalSessions    int     `json:"total_sessions"`
	AvgTracksPerSess float64 `json:"avg_tracks_per_session"`
	MostCompleted    string  `json:"most_completed"`
	LeastCompleted   string  `json:"least_completed"`
}

// QueryFilter scopes analytics queries.
type QueryFilter struct {
	From       *time.Time
	To         *time.Time
	Stems      []string
	EventTypes []string
	AlbumID    *int64
}

// GetTrackStatsFiltered returns per-track analytics with optional filtering.
func GetTrackStatsFiltered(db *sql.DB, filter QueryFilter) ([]TrackStats, error) {
	filter = normalizeFilter(filter)

	where := []string{
		"e.track_stem IS NOT NULL",
		"e.track_stem != ''",
		"e.event_type IN ('play', 'complete')",
	}
	args := make([]interface{}, 0, 8)
	appendTimeFilter(&where, &args, "e.created_at", filter)
	appendStemFilter(&where, &args, "e.track_stem", filter.Stems)
	appendAlbumFilter(&where, &args, "e.album_id", filter.AlbumID)

	query := `
		SELECT
			e.track_stem,
			COUNT(CASE WHEN e.event_type = 'play' THEN 1 END) as total_plays,
			COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.session_id END) as unique_sessions,
			COUNT(CASE WHEN e.event_type = 'complete' THEN 1 END) as completions
		FROM events e
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY e.track_stem
		ORDER BY total_plays DESC
	`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query track stats: %w", err)
	}
	defer rows.Close()

	var stats []TrackStats
	for rows.Next() {
		var s TrackStats
		if err := rows.Scan(&s.Stem, &s.TotalPlays, &s.UniqueSessions, &s.Completions); err != nil {
			return nil, fmt.Errorf("scan track stats: %w", err)
		}
		if s.TotalPlays > 0 {
			s.CompletionRate = float64(s.Completions) / float64(s.TotalPlays)
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// GetDropoutHeatmapFiltered returns dropout distribution for a track in 10 bins with optional date filtering.
func GetDropoutHeatmapFiltered(db *sql.DB, stem string, filter QueryFilter) ([]DropoutBin, error) {
	filter = normalizeFilter(filter)

	eventTypes := []string{"dropout", "pause"}
	if len(filter.EventTypes) > 0 {
		allowed := make([]string, 0, 2)
		for _, et := range filter.EventTypes {
			if et == "dropout" || et == "pause" {
				allowed = append(allowed, et)
			}
		}
		if len(allowed) == 0 {
			eventTypes = nil
		} else {
			eventTypes = allowed
		}
	}

	bins := make([]DropoutBin, 10)
	for i := range bins {
		bins[i].BinStart = float64(i) * 0.1
		bins[i].BinEnd = float64(i+1) * 0.1
	}
	if len(eventTypes) == 0 {
		return bins, nil
	}

	where := []string{
		"track_stem = ?",
		"position_seconds > 0",
	}
	args := []interface{}{stem}
	appendEventTypeFilter(&where, &args, "event_type", eventTypes)
	appendTimeFilter(&where, &args, "created_at", filter)
	appendAlbumFilter(&where, &args, "album_id", filter.AlbumID)

	rows, err := db.Query(`
		SELECT position_seconds, COALESCE(json_extract(metadata, '$.duration'), 0)
		FROM events
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, fmt.Errorf("query dropout positions: %w", err)
	}
	defer rows.Close()

	type point struct{ pos, duration float64 }
	var points []point
	var maxPos, maxDuration float64
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.pos, &p.duration); err != nil {
			return nil, fmt.Errorf("scan dropout position: %w", err)
		}
		points = append(points, p)
		maxPos = max(maxPos, p.pos)
		maxDuration = max(maxDuration, p.duration)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Events from clients that predate the duration field fall back to the
	// longest known duration, or to the furthest position seen.
	fallback := maxDuration
	if fallback <= 0 {
		fallback = maxPos
	}
	for _, p := range points {
		d := p.duration
		if d <= 0 {
			d = fallback
		}
		// Compare as a float first: a huge ratio overflows the int conversion.
		frac := p.pos / d
		binIdx := 9
		if frac < 1 {
			binIdx = int(frac * 10)
		}
		bins[binIdx].Count++
	}

	return bins, nil
}

// GetSessionTimelineFiltered returns the most recent listening sessions, built
// from events so that sessions outlive the 7-day sessions table rows.
func GetSessionTimelineFiltered(db *sql.DB, limit int, filter QueryFilter) ([]SessionInfo, error) {
	filter = normalizeFilter(filter)
	if limit <= 0 {
		limit = 50
	}

	where, args := eventFilter(filter)
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT
			e.session_id,
			MIN(e.created_at),
			MAX(e.created_at),
			COALESCE(MAX(s.ip_hash), ''),
			COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.track_stem END)
		FROM events e
		LEFT JOIN sessions s ON s.id = e.session_id
		WHERE `+where+`
		GROUP BY e.session_id
		ORDER BY MIN(e.created_at) DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query session timeline: %w", err)
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.SessionID, &s.StartedAt, &s.LastSeenAt, &s.IPHash, &s.TracksHeard); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.StartedAt = sqliteTimeToRFC3339(s.StartedAt)
		s.LastSeenAt = sqliteTimeToRFC3339(s.LastSeenAt)
		if len(s.IPHash) > 12 {
			s.IPHash = s.IPHash[:12] + "..."
		}
		if len(s.SessionID) > 12 {
			s.SessionID = s.SessionID[:12]
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// eventFilter builds a WHERE clause over events aliased "e".
func eventFilter(filter QueryFilter) (string, []interface{}) {
	where := []string{"1=1"}
	args := make([]interface{}, 0, 8)
	appendTimeFilter(&where, &args, "e.created_at", filter)
	appendStemFilter(&where, &args, "e.track_stem", filter.Stems)
	appendEventTypeFilter(&where, &args, "e.event_type", filter.EventTypes)
	appendAlbumFilter(&where, &args, "e.album_id", filter.AlbumID)
	return strings.Join(where, " AND "), args
}

func sqliteTimeToRFC3339(v string) string {
	t, err := time.ParseInLocation(sqliteTimeLayout, v, time.UTC)
	if err != nil {
		return v
	}
	return t.Format(time.RFC3339)
}

// GetOverallStatsFiltered returns aggregate analytics with optional filtering.
func GetOverallStatsFiltered(db *sql.DB, filter QueryFilter) (*OverallStats, error) {
	filter = normalizeFilter(filter)
	stats := &OverallStats{}
	where, args := eventFilter(filter)

	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(track_count), 0) FROM (
			SELECT COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.track_stem END) AS track_count
			FROM events e
			WHERE `+where+`
			GROUP BY e.session_id
		)`, args...).Scan(&stats.TotalSessions, &stats.AvgTracksPerSess); err != nil {
		return nil, fmt.Errorf("query session totals: %w", err)
	}

	err := db.QueryRow(`
		SELECT e.track_stem FROM events e
		WHERE `+where+` AND e.event_type = 'complete' AND COALESCE(e.track_stem, '') != ''
		GROUP BY e.track_stem
		ORDER BY COUNT(*) DESC LIMIT 1
	`, args...).Scan(&stats.MostCompleted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query most completed: %w", err)
	}

	// Least completed among tracks that have been played.
	err = db.QueryRow(`
		SELECT e.track_stem FROM events e
		WHERE `+where+` AND COALESCE(e.track_stem, '') != ''
		GROUP BY e.track_stem
		HAVING SUM(e.event_type = 'play') > 0
		ORDER BY CAST(SUM(e.event_type = 'complete') AS REAL) / SUM(e.event_type = 'play') ASC
		LIMIT 1
	`, args...).Scan(&stats.LeastCompleted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query least completed: %w", err)
	}

	return stats, nil
}

func normalizeFilter(filter QueryFilter) QueryFilter {
	out := QueryFilter{}
	if filter.From != nil {
		from := filter.From.UTC()
		out.From = &from
	}
	if filter.To != nil {
		to := filter.To.UTC()
		out.To = &to
	}

	// Stems are bound as parameters, so an unknown one simply matches nothing;
	// dropping it would silently widen the filter to every track.
	stemSeen := make(map[string]struct{}, len(filter.Stems))
	for _, stem := range filter.Stems {
		if _, ok := stemSeen[stem]; ok {
			continue
		}
		stemSeen[stem] = struct{}{}
		out.Stems = append(out.Stems, stem)
	}

	eventSeen := make(map[string]struct{}, len(filter.EventTypes))
	for _, et := range filter.EventTypes {
		et = strings.TrimSpace(et)
		if !validEventTypes[et] {
			continue
		}
		if _, ok := eventSeen[et]; ok {
			continue
		}
		eventSeen[et] = struct{}{}
		out.EventTypes = append(out.EventTypes, et)
	}

	out.AlbumID = filter.AlbumID

	return out
}

func appendTimeFilter(where *[]string, args *[]interface{}, column string, filter QueryFilter) {
	if filter.From != nil {
		*where = append(*where, column+" >= ?")
		*args = append(*args, formatSQLiteTime(*filter.From))
	}
	if filter.To != nil {
		*where = append(*where, column+" < ?")
		*args = append(*args, formatSQLiteTime(*filter.To))
	}
}

func appendStemFilter(where *[]string, args *[]interface{}, column string, stems []string) {
	if len(stems) == 0 {
		return
	}
	*where = append(*where, column+" IN ("+placeholders(len(stems))+")")
	for _, stem := range stems {
		*args = append(*args, stem)
	}
}

func appendEventTypeFilter(where *[]string, args *[]interface{}, column string, eventTypes []string) {
	if len(eventTypes) == 0 {
		return
	}
	*where = append(*where, column+" IN ("+placeholders(len(eventTypes))+")")
	for _, et := range eventTypes {
		*args = append(*args, et)
	}
}

func appendAlbumFilter(where *[]string, args *[]interface{}, column string, albumID *int64) {
	if albumID == nil {
		return
	}
	*where = append(*where, column+" = ?")
	*args = append(*args, *albumID)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
