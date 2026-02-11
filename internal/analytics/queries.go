package analytics

import (
	"database/sql"
	"fmt"
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

// GetTrackStats returns per-track analytics.
func GetTrackStats(db *sql.DB) ([]TrackStats, error) {
	rows, err := db.Query(`
		SELECT
			e.track_stem,
			COUNT(CASE WHEN e.event_type = 'play' THEN 1 END) as total_plays,
			COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.session_id END) as unique_sessions,
			COUNT(CASE WHEN e.event_type = 'complete' THEN 1 END) as completions
		FROM events e
		WHERE e.track_stem IS NOT NULL AND e.track_stem != ''
		GROUP BY e.track_stem
		ORDER BY total_plays DESC
	`)
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
	// Get all dropout/pause positions for this track
	rows, err := db.Query(`
		SELECT position_seconds
		FROM events
		WHERE track_stem = ? AND event_type IN ('dropout', 'pause')
		AND position_seconds > 0
	`, stem)
	if err != nil {
		return nil, fmt.Errorf("query dropout positions: %w", err)
	}
	defer rows.Close()

	// Find max position to normalize
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

	// Build 10 bins
	bins := make([]DropoutBin, 10)
	for i := range bins {
		bins[i].BinStart = float64(i) * 0.1
		bins[i].BinEnd = float64(i+1) * 0.1
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
	if limit <= 0 {
		limit = 50
	}

	rows, err := db.Query(`
		SELECT
			s.id,
			s.started_at,
			s.last_seen_at,
			s.ip_hash,
			COUNT(DISTINCT CASE WHEN e.event_type = 'play' THEN e.track_stem END) as tracks_heard
		FROM sessions s
		LEFT JOIN events e ON s.id = e.session_id
		GROUP BY s.id
		ORDER BY s.started_at DESC
		LIMIT ?
	`, limit)
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
			// Truncate for display
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
	stats := &OverallStats{}

	// Total sessions
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&stats.TotalSessions)

	// Average tracks per session
	db.QueryRow(`
		SELECT COALESCE(AVG(track_count), 0) FROM (
			SELECT COUNT(DISTINCT CASE WHEN event_type = 'play' THEN track_stem END) as track_count
			FROM events
			GROUP BY session_id
		)
	`).Scan(&stats.AvgTracksPerSess)

	// Most completed track
	db.QueryRow(`
		SELECT track_stem FROM events
		WHERE event_type = 'complete' AND track_stem IS NOT NULL AND track_stem != ''
		GROUP BY track_stem
		ORDER BY COUNT(*) DESC LIMIT 1
	`).Scan(&stats.MostCompleted)

	// Least completed track (among those that have been played)
	db.QueryRow(`
		SELECT track_stem FROM (
			SELECT track_stem,
				CAST(SUM(CASE WHEN event_type = 'complete' THEN 1 ELSE 0 END) AS REAL) /
				NULLIF(SUM(CASE WHEN event_type = 'play' THEN 1 ELSE 0 END), 0) as rate
			FROM events
			WHERE track_stem IS NOT NULL AND track_stem != ''
			GROUP BY track_stem
			HAVING SUM(CASE WHEN event_type = 'play' THEN 1 ELSE 0 END) > 0
		) ORDER BY rate ASC LIMIT 1
	`).Scan(&stats.LeastCompleted)

	return stats, nil
}
