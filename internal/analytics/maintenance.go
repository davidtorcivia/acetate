package analytics

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	sqliteTimeLayout = "2006-01-02 15:04:05"

	// AuditRetention bounds admin_auth_audit, which gains a row per login attempt.
	AuditRetention = 180 * 24 * time.Hour
)

// MaintenanceResult summarizes a maintenance run.
type MaintenanceResult struct {
	RanAtUTC        string `json:"ran_at_utc"`
	RetentionDays   int    `json:"retention_days"`
	PrunedRows      int64  `json:"pruned_rows"`
	PrunedAuditRows int64  `json:"pruned_audit_rows"`
}

// RunMaintenance prunes raw events older than retentionDays (0 keeps them
// forever) and admin login audit rows older than AuditRetention.
func RunMaintenance(db *sql.DB, now time.Time, retentionDays int) (MaintenanceResult, error) {
	res := MaintenanceResult{
		RanAtUTC:      now.UTC().Format(time.RFC3339),
		RetentionDays: retentionDays,
	}

	if retentionDays > 0 {
		n, err := deleteBefore(db, "DELETE FROM events WHERE created_at < ?", now.AddDate(0, 0, -retentionDays))
		if err != nil {
			return res, fmt.Errorf("prune events older than %d days: %w", retentionDays, err)
		}
		res.PrunedRows = n
	}

	n, err := deleteBefore(db, "DELETE FROM admin_auth_audit WHERE occurred_at < ?", now.Add(-AuditRetention))
	if err != nil {
		return res, fmt.Errorf("prune admin auth audit: %w", err)
	}
	res.PrunedAuditRows = n
	return res, nil
}

func deleteBefore(db *sql.DB, query string, cutoff time.Time) (int64, error) {
	result, err := db.Exec(query, formatSQLiteTime(cutoff))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
