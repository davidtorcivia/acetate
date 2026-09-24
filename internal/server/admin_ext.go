package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"acetate/internal/album"
	"acetate/internal/albums"
	"acetate/internal/analytics"
)

type reconcileTrack struct {
	Stem  string `json:"stem"`
	Title string `json:"title"`
}

type reconcileTitleMismatch struct {
	Stem        string `json:"stem"`
	ConfigTitle string `json:"config_title"`
	AlbumTitle  string `json:"album_title"`
}

type reconcileReport struct {
	ConfigOnly      []reconcileTrack         `json:"config_only"`
	AlbumOnly       []reconcileTrack         `json:"album_only"`
	TitleMismatches []reconcileTitleMismatch `json:"title_mismatches"`
	ConfigCount     int                      `json:"config_count"`
	AlbumCount      int                      `json:"album_count"`
}

type reconcileApplyResult struct {
	Added         int `json:"added"`
	Removed       int `json:"removed"`
	TitlesUpdated int `json:"titles_updated"`
}

func (s *Server) recordAdminAuthAttempt(r *http.Request, attemptedUsername, outcome, reason string) {
	clientIP := s.clientIPs.ClientIP(r)
	ipHash := hashForAudit(clientIP)
	uaHash := hashForAudit(strings.TrimSpace(r.UserAgent()))

	if _, err := s.db.Exec(
		"INSERT INTO admin_auth_audit (client_ip_hash, user_agent_hash, attempted_username, outcome, reason) VALUES (?, ?, ?, ?, ?)",
		ipHash, uaHash, strings.ToLower(strings.TrimSpace(attemptedUsername)), strings.TrimSpace(outcome), strings.TrimSpace(reason),
	); err != nil {
		// Best effort; auth should not fail because audit logging failed.
		return
	}
}

func hashForAudit(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func parseAnalyticsFilter(values url.Values) (analytics.QueryFilter, error) {
	var filter analytics.QueryFilter

	if raw := strings.TrimSpace(values.Get("from")); raw != "" {
		from, err := parseFilterTime(raw, false)
		if err != nil {
			return filter, err
		}
		filter.From = &from
	}

	if raw := strings.TrimSpace(values.Get("to")); raw != "" {
		to, err := parseFilterTime(raw, true)
		if err != nil {
			return filter, err
		}
		filter.To = &to
	}

	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		return filter, errors.New("from must be before to")
	}

	if raw := strings.TrimSpace(values.Get("album_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return filter, errors.New("invalid album_id")
		}
		filter.AlbumID = &id
	}

	filter.Stems = splitCSV(values.Get("stems"))
	filter.EventTypes = splitCSV(values.Get("event_types"))
	return filter, nil
}

func parseFilterTime(raw string, endBoundary bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.UTC); err == nil {
		if endBoundary {
			t = t.AddDate(0, 0, 1)
		}
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("invalid datetime")
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func parseOptionalInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func formatFilterTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) handleAdminReconcilePreview(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	dbTracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	diskTracks, err := album.ScanTracks(alb.AlbumPath)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	report := buildReconcileReport(dbTracks, diskTracks)
	jsonOK(w, report)
}

func (s *Server) handleAdminReconcileApply(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	var req struct {
		AdoptMetadataTitles bool `json:"adopt_metadata_titles"`
		KeepMissing         bool `json:"keep_missing"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	dbTracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	diskTracks, err := album.ScanTracks(alb.AlbumPath)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	updated, applied := applyReconcile(dbTracks, diskTracks, req.AdoptMetadataTitles, req.KeepMissing)

	if err := s.albumStore.SetTracks(alb.ID, updated); err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	report := buildReconcileReport(updated, diskTracks)
	jsonOK(w, map[string]interface{}{
		"status":  "ok",
		"applied": applied,
		"report":  report,
	})
}

func buildReconcileReport(configTracks, albumTracks []albums.Track) reconcileReport {
	report := reconcileReport{
		ConfigCount: len(configTracks),
		AlbumCount:  len(albumTracks),
	}

	configMap := make(map[string]albums.Track, len(configTracks))
	for _, t := range configTracks {
		configMap[t.Stem] = t
	}

	albumMap := make(map[string]albums.Track, len(albumTracks))
	for _, t := range albumTracks {
		albumMap[t.Stem] = t
	}

	for _, t := range configTracks {
		albumTrack, ok := albumMap[t.Stem]
		if !ok {
			report.ConfigOnly = append(report.ConfigOnly, reconcileTrack{Stem: t.Stem, Title: t.Title})
			continue
		}
		if trimAndCollapseSpaces(t.Title) != trimAndCollapseSpaces(albumTrack.Title) {
			report.TitleMismatches = append(report.TitleMismatches, reconcileTitleMismatch{
				Stem:        t.Stem,
				ConfigTitle: t.Title,
				AlbumTitle:  albumTrack.Title,
			})
		}
	}

	for _, t := range albumTracks {
		if _, ok := configMap[t.Stem]; !ok {
			report.AlbumOnly = append(report.AlbumOnly, reconcileTrack{Stem: t.Stem, Title: t.Title})
		}
	}

	sort.Slice(report.ConfigOnly, func(i, j int) bool { return report.ConfigOnly[i].Stem < report.ConfigOnly[j].Stem })
	sort.Slice(report.AlbumOnly, func(i, j int) bool { return report.AlbumOnly[i].Stem < report.AlbumOnly[j].Stem })
	sort.Slice(report.TitleMismatches, func(i, j int) bool { return report.TitleMismatches[i].Stem < report.TitleMismatches[j].Stem })

	return report
}

func applyReconcile(current, albumTracks []albums.Track, adoptTitles, keepMissing bool) ([]albums.Track, reconcileApplyResult) {
	albumMap := make(map[string]albums.Track, len(albumTracks))
	for _, t := range albumTracks {
		albumMap[t.Stem] = t
	}

	result := reconcileApplyResult{}
	seen := make(map[string]struct{}, len(current))
	updated := make([]albums.Track, 0, len(albumTracks))

	for _, t := range current {
		albumTrack, ok := albumMap[t.Stem]
		if !ok {
			if keepMissing {
				updated = append(updated, t)
			} else {
				result.Removed++
			}
			continue
		}

		next := t
		if strings.TrimSpace(next.Title) == "" {
			next.Title = albumTrack.Title
			result.TitlesUpdated++
		} else if adoptTitles && strings.TrimSpace(albumTrack.Title) != "" && trimAndCollapseSpaces(next.Title) != trimAndCollapseSpaces(albumTrack.Title) {
			next.Title = albumTrack.Title
			result.TitlesUpdated++
		}

		updated = append(updated, next)
		seen[t.Stem] = struct{}{}
	}

	for _, t := range albumTracks {
		if _, ok := seen[t.Stem]; ok {
			continue
		}
		updated = append(updated, t)
		result.Added++
	}

	return updated, result
}

func (s *Server) handleAdminOpsHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"

	dbErr := s.db.PingContext(r.Context())
	if dbErr != nil {
		status = "degraded"
	}

	dataErr := checkPathExists(s.dataPath)
	if dataErr != nil {
		status = "degraded"
	}

	albumCount, _ := s.albumStore.AlbumCount()

	jsonOK(w, map[string]interface{}{
		"status":                    status,
		"now_utc":                   time.Now().UTC().Format(time.RFC3339),
		"uptime_seconds":            int(time.Since(s.startedAt).Seconds()),
		"analytics_retention_days":  s.analyticsRetentionDays,
		"maintenance_interval_secs": int(s.maintenanceInterval.Seconds()),
		"album_count":               albumCount,
		"analytics": map[string]interface{}{
			"dropped_events":  s.collector.DroppedCount(),
			"rejected_events": s.collector.RejectedCount(),
		},
		"database": map[string]interface{}{
			"ok":    dbErr == nil,
			"error": errorString(dbErr),
		},
		"paths": map[string]interface{}{
			"data_ok":  dataErr == nil,
			"data_err": errorString(dataErr),
		},
	})
}

func (s *Server) handleAdminOpsStats(w http.ResponseWriter, r *http.Request) {
	sessions, err := queryCount(s.db, "SELECT COUNT(*) FROM sessions")
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	adminSessions, err := queryCount(s.db, "SELECT COUNT(*) FROM admin_sessions")
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	events, err := queryCount(s.db, "SELECT COUNT(*) FROM events")
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	auditRows, err := queryCount(s.db, "SELECT COUNT(*) FROM admin_auth_audit")
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	dbStats := s.db.Stats()
	dbFile := filepath.Join(s.dataPath, "acetate.db")
	walFile := dbFile + "-wal"

	jsonOK(w, map[string]interface{}{
		"counts": map[string]interface{}{
			"sessions":       sessions,
			"admin_sessions": adminSessions,
			"events":         events,
			"auth_audit":     auditRows,
		},
		"database": map[string]interface{}{
			"open_connections": dbStats.OpenConnections,
			"in_use":           dbStats.InUse,
			"idle":             dbStats.Idle,
			"db_file_bytes":    fileSizeOrZero(dbFile),
			"wal_file_bytes":   fileSizeOrZero(walFile),
		},
		"server": map[string]interface{}{
			"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
		},
	})
}

func (s *Server) handleAdminOpsMaintenance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RetentionDays *int `json:"retention_days,omitempty"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			jsonError(w, "bad request", http.StatusBadRequest)
			return
		}
	}

	retentionDays := s.analyticsRetentionDays
	if req.RetentionDays != nil {
		retentionDays = *req.RetentionDays
	}
	if retentionDays < 0 || retentionDays > 3650 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	flushCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_ = s.collector.FlushNow(flushCtx)
	cancel()

	result, err := analytics.RunMaintenance(s.db, time.Now().UTC(), retentionDays)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]interface{}{
		"status": "ok",
		"result": result,
	})
}

func (s *Server) handleAdminExportEvents(w http.ResponseWriter, r *http.Request) {
	filter, err := parseAnalyticsFilter(r.URL.Query())
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	contentType := map[string]string{"json": "application/json", "csv": "text/csv; charset=utf-8"}[format]
	if contentType == "" {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	limit := parseOptionalInt(r.URL.Query().Get("limit"), 0)
	if limit < 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	flushCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_ = s.collector.FlushNow(flushCtx)
	cancel()

	started := false
	err = analytics.ExportEvents(w, s.db, filter, limit, format, func() {
		started = true
		allowLongWrite(w)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="analytics-events-%s.%s"`, time.Now().UTC().Format("20060102-150405"), format))
	})
	if err != nil {
		log.Printf("export events error: %v", err)
		if !started {
			jsonError(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func (s *Server) handleAdminExportBackup(w http.ResponseWriter, r *http.Request) {
	flushCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_ = s.collector.FlushNow(flushCtx)
	cancel()

	snapshot, err := s.createDatabaseSnapshot()
	if err != nil {
		log.Printf("backup snapshot error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(snapshot)

	allowLongWrite(w)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="acetate-backup-%s.zip"`, time.Now().UTC().Format("20060102-150405")))
	if err := s.writeBackupZip(w, snapshot); err != nil {
		log.Printf("backup zip error: %v", err)
	}
}

// allowLongWrite extends the server's write timeout for a large download. It
// stays finite: a stalled client must not hold a database cursor forever.
func allowLongWrite(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Minute))
}

// createDatabaseSnapshot writes a consistent copy of the database to a temp
// file and returns its path.
func (s *Server) createDatabaseSnapshot() (string, error) {
	dir, err := os.MkdirTemp("", "acetate-backup-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "acetate.db")
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// writeBackupZip streams the database snapshot and uploaded covers as a zip.
func (s *Server) writeBackupZip(w io.Writer, snapshotPath string) error {
	defer os.RemoveAll(filepath.Dir(snapshotPath))
	zw := zip.NewWriter(w)

	if err := addFileToZip(zw, snapshotPath, "acetate.db"); err != nil {
		return err
	}

	coversDir := filepath.Join(s.dataPath, "covers")
	err := filepath.WalkDir(coversDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(s.dataPath, path)
		if err != nil {
			return err
		}
		return addFileToZip(zw, path, filepath.ToSlash(rel))
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	manifest, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(manifest).Encode(map[string]interface{}{
		"exported_at_utc":          time.Now().UTC().Format(time.RFC3339),
		"analytics_retention_days": s.analyticsRetentionDays,
	}); err != nil {
		return err
	}
	return zw.Close()
}

func addFileToZip(zw *zip.Writer, sourcePath, zipPath string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()

	w, err := zw.Create(zipPath)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func checkPathExists(path string) error {
	_, err := os.Stat(path)
	return err
}

func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func queryCount(db *sql.DB, query string) (int64, error) {
	var v int64
	if err := db.QueryRow(query).Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
