package analytics

import (
	"database/sql"
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
}

// GetTrackStats returns per-track analytics.
func GetTrackStats(db *sql.DB) ([]TrackStats, error) {
	return GetTrackStatsFiltered(db, QueryFilter{})
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

// GetDropoutHeatmap returns dropout distribution for a track in 10 bins.
func GetDropoutHeatmap(db *sql.DB, stem string) ([]DropoutBin, error) {
	return GetDropoutHeatmapFiltered(db, stem, QueryFilter{})
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

	query := `
		SELECT position_seconds
		FROM events
		WHERE ` + strings.Join(where, " AND ")

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query dropout positions: %w", err)
	}
	defer rows.Close()

	var positions []float64
	var maxPos float64
	for rows.Next() {
		var pos float64
		if err := rows.Scan(&pos); err != nil {
			continue
		}
		positions = append(positions, pos)
		if pos > maxPos {
			maxPos = pos
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if maxPos > 0 {
		for _, pos := range positions {
			normalized := pos / maxPos
			binIdx := int(normalized * 10)
			if binIdx >= 10 {
				binIdx = 9
			}
			bins[binIdx].Count++
		}
	}

	return bins, nil
}

// GetSessionTimeline returns recent sessions with track counts.
func GetSessionTimeline(db *sql.DB, limit int) ([]SessionInfo, error) {
	return GetSessionTimelineFiltered(db, limit, QueryFilter{})
}

// GetSessionTimelineFiltered returns recent sessions with optional filters.
func GetSessionTimelineFiltered(db *sql.DB, limit int, filter QueryFilter) ([]SessionInfo, error) {
	filter = normalizeFilter(filter)

	if limit <= 0 {
		limit = 50
	}

	joinClauses := []string{"s.id = e.session_id"}
	joinArgs := make([]interface{}, 0, 8)
	appendTimeFilter(&joinClauses, &joinArgs, "e.created_at", filter)
	appendStemFilter(&joinClauses, &joinArgs, "e.track_stem", filter.Stems)

	where := []string{"1=1"}
	args := make([]interface{}, 0, 8)
	appendTimeFilter(&where, &args, "s.started_at", filter)

	queryArgs := make([]interface{}, 0, len(joinArgs)+len(args)+1)
	queryArgs = append(queryArgs, joinArgs...)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)

	rows, err := db.Query(`
		SELECT
			s.id,
			s.started_at,
			s.last_seen_at,
			s.ip_hash,
			COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.track_stem END) as tracks_heard
		FROM sessions s
		LEFT JOIN events e ON `+strings.Join(joinClauses, " AND ")+`
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY s.id
		ORDER BY s.started_at DESC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("query session timeline: %w", err)
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		var ipHash sql.NullString
		if err := rows.Scan(&s.SessionID, &s.StartedAt, &s.LastSeenAt, &ipHash, &s.TracksHeard); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if ipHash.Valid {
			if len(ipHash.String) > 12 {
				s.IPHash = ipHash.String[:12] + "..."
			} else {
				s.IPHash = ipHash.String
			}
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// GetOverallStats returns aggregate analytics.
func GetOverallStats(db *sql.DB) (*OverallStats, error) {
	return GetOverallStatsFiltered(db, QueryFilter{})
}

// GetOverallStatsFiltered returns aggregate analytics with optional filtering.
func GetOverallStatsFiltered(db *sql.DB, filter QueryFilter) (*OverallStats, error) {
	filter = normalizeFilter(filter)
	stats := &OverallStats{}

	// Total sessions (session start time-based filter).
	whereSessions := []string{"1=1"}
	argsSessions := make([]interface{}, 0, 4)
	appendTimeFilter(&whereSessions, &argsSessions, "started_at", filter)
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions WHERE "+strings.Join(whereSessions, " AND "), argsSessions...).Scan(&stats.TotalSessions); err != nil {
		return nil, fmt.Errorf("query total sessions: %w", err)
	}

	// Build event filter once for aggregate event queries.
	eventWhere := []string{"1=1"}
	eventArgs := make([]interface{}, 0, 8)
	appendTimeFilter(&eventWhere, &eventArgs, "created_at", filter)
	appendStemFilter(&eventWhere, &eventArgs, "track_stem", filter.Stems)
	appendEventTypeFilter(&eventWhere, &eventArgs, "event_type", filter.EventTypes)

	// Average tracks per session.
	avgQuery := `
		SELECT COALESCE(AVG(track_count), 0) FROM (
			SELECT COUNT(DISTINCT CASE WHEN event_type = 'play' THEN track_stem END) as track_count
			FROM events
			WHERE ` + strings.Join(eventWhere, " AND ") + `
			GROUP BY session_id
		)
	`
	if err := db.QueryRow(avgQuery, eventArgs...).Scan(&stats.AvgTracksPerSess); err != nil {
		return nil, fmt.Errorf("query avg tracks/session: %w", err)
	}

	// Most completed track.
	mostWhere := cloneStrings(eventWhere)
	mostArgs := cloneInterfaces(eventArgs)
	mostWhere = append(mostWhere, "event_type = 'complete'", "track_stem IS NOT NULL", "track_stem != ''")
	_ = db.QueryRow(`
		SELECT track_stem FROM events
		WHERE `+strings.Join(mostWhere, " AND ")+`
		GROUP BY track_stem
		ORDER BY COUNT(*) DESC LIMIT 1
	`, mostArgs...).Scan(&stats.MostCompleted)

	// Least completed track (among those that have been played).
	leastWhere := cloneStrings(eventWhere)
	leastArgs := cloneInterfaces(eventArgs)
	leastWhere = append(leastWhere, "track_stem IS NOT NULL", "track_stem != ''")
	_ = db.QueryRow(`
		SELECT track_stem FROM (
			SELECT track_stem,
				CAST(SUM(CASE WHEN event_type = 'complete' THEN 1 ELSE 0 END) AS REAL) /
				NULLIF(SUM(CASE WHEN event_type = 'play' THEN 1 ELSE 0 END), 0) as rate
			FROM events
			WHERE `+strings.Join(leastWhere, " AND ")+`
			GROUP BY track_stem
			HAVING SUM(CASE WHEN event_type = 'play' THEN 1 ELSE 0 END) > 0
		) ORDER BY rate ASC LIMIT 1
	`, leastArgs...).Scan(&stats.LeastCompleted)

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

	stemSeen := make(map[string]struct{}, len(filter.Stems))
	for _, stem := range filter.Stems {
		stem = strings.TrimSpace(stem)
		if !validTrackStem(stem) {
			continue
		}
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

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneInterfaces(in []interface{}) []interface{} {
	out := make([]interface{}, len(in))
	copy(out, in)
	return out
}
