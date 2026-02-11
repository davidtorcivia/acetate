package analytics

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	sqliteDayLayout  = "2006-01-02"
	sqliteTimeLayout = "2006-01-02 15:04:05"
)

// MaintenanceResult summarizes a maintenance run.
type MaintenanceResult struct {
	RanAtUTC      string `json:"ran_at_utc"`
	RetentionDays int    `json:"retention_days"`
	RolledDays    int    `json:"rolled_days"`
	RollupRows    int64  `json:"rollup_rows"`
	PrunedRows    int64  `json:"pruned_rows"`
}

// RunMaintenance materializes daily rollups for completed days and optionally prunes old raw events.
func RunMaintenance(db *sql.DB, now time.Time, retentionDays int) (MaintenanceResult, error) {
	res := MaintenanceResult{
		RanAtUTC:      now.UTC().Format(time.RFC3339),
		RetentionDays: retentionDays,
	}

	days, rows, err := rollupClosedDays(db, now)
	if err != nil {
		return res, err
	}
	res.RolledDays = days
	res.RollupRows = rows

	pruned, err := pruneOldEvents(db, now, retentionDays)
	if err != nil {
		return res, err
	}
	res.PrunedRows = pruned

	return res, nil
}

func rollupClosedDays(db *sql.DB, now time.Time) (int, int64, error) {
	startDay, ok, err := nextRollupDay(db)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, nil
	}

	endDay := dayStartUTC(now).AddDate(0, 0, -1)
	if startDay.After(endDay) {
		return 0, 0, nil
	}

	totalDays := 0
	var totalRows int64
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		dayStart := day.UTC()
		dayEnd := dayStart.AddDate(0, 0, 1)

		result, err := db.Exec(`
			INSERT INTO analytics_rollups_daily (day, track_stem, event_type, total_count)
			SELECT ?, COALESCE(track_stem, ''), event_type, COUNT(*)
			FROM events
			WHERE created_at >= ? AND created_at < ?
			GROUP BY COALESCE(track_stem, ''), event_type
			ON CONFLICT(day, track_stem, event_type)
			DO UPDATE SET total_count = excluded.total_count
		`, dayStart.Format(sqliteDayLayout), formatSQLiteTime(dayStart), formatSQLiteTime(dayEnd))
		if err != nil {
			return totalDays, totalRows, fmt.Errorf("rollup day %s: %w", dayStart.Format(sqliteDayLayout), err)
		}

		if rows, err := result.RowsAffected(); err == nil {
			totalRows += rows
		}
		totalDays++
	}

	return totalDays, totalRows, nil
}

func nextRollupDay(db *sql.DB) (time.Time, bool, error) {
	var maxDay sql.NullString
	if err := db.QueryRow("SELECT MAX(day) FROM analytics_rollups_daily").Scan(&maxDay); err != nil {
		return time.Time{}, false, fmt.Errorf("query max rollup day: %w", err)
	}
	if maxDay.Valid && maxDay.String != "" {
		parsed, err := time.ParseInLocation(sqliteDayLayout, maxDay.String, time.UTC)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("parse max rollup day: %w", err)
		}
		return parsed.AddDate(0, 0, 1), true, nil
	}

	var minDay sql.NullString
	if err := db.QueryRow("SELECT MIN(substr(created_at, 1, 10)) FROM events").Scan(&minDay); err != nil {
		return time.Time{}, false, fmt.Errorf("query min event day: %w", err)
	}
	if !minDay.Valid || minDay.String == "" {
		return time.Time{}, false, nil
	}

	parsed, err := time.ParseInLocation(sqliteDayLayout, minDay.String, time.UTC)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse min event day: %w", err)
	}
	return parsed, true, nil
}

func pruneOldEvents(db *sql.DB, now time.Time, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}

	cutoff := now.UTC().AddDate(0, 0, -retentionDays)
	result, err := db.Exec("DELETE FROM events WHERE created_at < ?", formatSQLiteTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune events older than %d days: %w", retentionDays, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return rows, nil
}

func dayStartUTC(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
