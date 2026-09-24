package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"acetate"
	"acetate/internal/album"
	"acetate/internal/albums"
	"acetate/internal/analytics"
	"acetate/internal/auth"
)

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(securityHeaders)
	r.Use(requestLogger)
	r.Use(csrfCheck)
	r.Use(middleware.GetHead)

	// Public API endpoints
	r.Route("/api", func(r chi.Router) {
		// Auth: no session required. Logout is idempotent so the page can
		// clear a stale cookie without first proving it is valid.
		r.With(bodyLimiter(1024)).Post("/auth", s.handleAuth)
		r.Delete("/auth", s.handleLogout)

		// Session-gated endpoints
		r.Group(func(r chi.Router) {
			r.Use(s.requireSession)

			// Album-scoped endpoints
			r.Route("/albums/{slug}", func(r chi.Router) {
				r.Use(s.requireAlbumAccess)
				r.With(cacheControl("private, no-cache")).Get("/tracks", s.handleGetTracks)
				r.Get("/cover", s.handleGetCover)
				// private keeps shared caches (Cloudflare) out; no-cache re-checks the session each time.
				r.With(cacheControl("private, no-cache")).Get("/stream/{stem}", s.handleStreamTrack)
				r.Get("/lyrics/{stem}", s.handleGetLyrics)
				r.With(bodyLimiter(102400)).Post("/analytics", s.handleAnalytics)
			})
		})
	})

	// Admin routes
	r.Route("/admin", func(r chi.Router) {
		r.With(bodyLimiter(1024)).Post("/api/auth", s.handleAdminAuth)
		r.Get("/api/setup/status", s.handleAdminSetupStatus)
		r.With(bodyLimiter(4096)).Post("/api/setup", s.handleAdminSetupBootstrap)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Use(cacheControl("no-store"))

			r.Delete("/api/auth", s.handleAdminLogout)
			r.Get("/api/admin-users", s.handleAdminListUsers)
			r.With(bodyLimiter(4096)).Post("/api/admin-users", s.handleAdminCreateUser)
			r.With(bodyLimiter(4096)).Put("/api/admin-users/{id}", s.handleAdminUpdateUser)
			r.With(bodyLimiter(4096)).Put("/api/admin-password", s.handleAdminUpdateAdminPassword)
			r.Get("/api/config", s.handleAdminGetConfig)
			r.Get("/api/ops/health", s.handleAdminOpsHealth)
			r.Get("/api/ops/stats", s.handleAdminOpsStats)
			r.With(bodyLimiter(4096)).Post("/api/ops/maintenance", s.handleAdminOpsMaintenance)
			r.Get("/api/export/events", s.handleAdminExportEvents)
			r.Get("/api/export/backup", s.handleAdminExportBackup)

			// Album CRUD
			r.Get("/api/album-folders", s.handleAdminListAlbumFolders)
			r.Get("/api/albums", s.handleAdminListAlbums)
			r.With(bodyLimiter(4096)).Post("/api/albums", s.handleAdminCreateAlbum)
			r.Get("/api/albums/{id}", s.handleAdminGetAlbum)
			r.With(bodyLimiter(4096)).Put("/api/albums/{id}", s.handleAdminUpdateAlbum)
			r.Delete("/api/albums/{id}", s.handleAdminDeleteAlbum)

			// Album-scoped admin operations
			r.Get("/api/albums/{id}/tracks", s.handleAdminGetTracks)
			r.With(bodyLimiter(102400)).Put("/api/albums/{id}/tracks", s.handleAdminUpdateTracks)
			r.With(bodyLimiter(10<<20)).Post("/api/albums/{id}/cover", s.handleAdminUploadCover)
			r.Get("/api/albums/{id}/reconcile", s.handleAdminReconcilePreview)
			r.With(bodyLimiter(4096)).Post("/api/albums/{id}/reconcile", s.handleAdminReconcileApply)
			r.Get("/api/albums/{id}/analytics", s.handleAdminAnalytics)

			// Password CRUD
			r.Get("/api/passwords", s.handleAdminListPasswords)
			r.With(bodyLimiter(4096)).Post("/api/passwords", s.handleAdminCreatePassword)
			r.With(bodyLimiter(4096)).Put("/api/passwords/{id}", s.handleAdminUpdatePassword)
			r.Delete("/api/passwords/{id}", s.handleAdminDeletePassword)
		})

		// Serve admin static files
		r.Get("/*", s.handleAdminStatic)
	})

	// SPA static files (public)
	r.Get("/*", s.handleSPA)

	return r
}

// Login attempts allowed per client IP (IPv6 /64) per auth.RateWindow.
const (
	listenerLoginLimit = 5
	adminLoginLimit    = 20
	adminSetupLimit    = 5
)

// --- Auth handlers ---

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	clientIP := s.clientIPs.ClientIP(r)

	if !s.rateLimiter.Allow("listener:"+auth.RateKey(clientIP), listenerLoginLimit) {
		w.Header().Set("Retry-After", strconv.Itoa(int(auth.RateWindow.Seconds())))
		jsonError(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	// Passphrases are stored trimmed; anything empty or past bcrypt's limit cannot match.
	passphrase := strings.TrimSpace(req.Passphrase)
	if passphrase == "" || len(passphrase) > maxPassphraseLen {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	passwordID, err := s.albumStore.VerifyPassword(passphrase)
	if err != nil {
		log.Printf("verify password error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if passwordID == 0 {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Rotate an existing session ID to prevent fixation and stale buildup.
	if oldCookie, err := r.Cookie(listenerCookie); err == nil && oldCookie.Value != "" {
		_ = s.sessions.DeleteSession(oldCookie.Value)
	}

	sessionID, err := s.sessions.CreateSession(clientIP, passwordID)
	if err != nil {
		log.Printf("create session error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	setListenerCookie(w, r, sessionID, int(auth.SessionExpiry.Seconds()))

	// Record session start
	s.collector.Record(analytics.Event{
		SessionID: auth.HashToken(sessionID),
		EventType: "session_start",
	})

	accessibleAlbums, err := s.albumStore.GetAlbumsForPassword(passwordID)
	if err != nil {
		log.Printf("get albums for password error: %v", err)
	}

	type albumResponse struct {
		Slug   string `json:"slug"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	albumList := make([]albumResponse, 0, len(accessibleAlbums))
	for _, a := range accessibleAlbums {
		albumList = append(albumList, albumResponse{Slug: a.Slug, Title: a.Title, Artist: a.Artist})
	}

	jsonOK(w, map[string]interface{}{
		"status": "ok",
		"albums": albumList,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := cookieValue(r, listenerCookie)
	// Only a live session gets a session_end event; the cookie is client-controlled.
	if valid, _, err := s.sessions.ValidateSession(sessionID); err == nil && valid {
		if err := s.sessions.DeleteSession(sessionID); err != nil {
			log.Printf("delete session error: %v", err)
		}

		s.collector.Record(analytics.Event{
			SessionID: auth.HashToken(sessionID),
			EventType: "session_end",
		})
	}

	setListenerCookie(w, r, "", -1)

	jsonOK(w, map[string]string{"status": "ok"})
}

// --- Content handlers ---

func (s *Server) handleGetTracks(w http.ResponseWriter, r *http.Request) {
	alb := albumFromContext(r)
	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		log.Printf("get tracks error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	trackInfos := album.GetTrackList(tracks, alb.AlbumPath)
	jsonOK(w, map[string]interface{}{
		"title":             alb.Title,
		"artist":            alb.Artist,
		"tracks":            trackInfos,
		"downloads_enabled": alb.DownloadsEnabled,
	})
}

func (s *Server) handleGetCover(w http.ResponseWriter, r *http.Request) {
	alb := albumFromContext(r)
	album.ServeCover(w, r, alb.AlbumPath, s.dataPath, alb.ID)
}

// requestTrack resolves the {stem} URL parameter to a track of the request's
// album, writing an error response and returning false when it does not name one.
func (s *Server) requestTrack(w http.ResponseWriter, r *http.Request) (*albums.Album, albums.Track, bool) {
	stem := chi.URLParam(r, "stem")
	// chi hands back the escaped form only when the request used a non-canonical
	// escaping (RawPath set); decoding unconditionally would corrupt stems with '%'.
	if r.URL.RawPath != "" {
		unescaped, err := url.PathUnescape(stem)
		if err != nil {
			jsonError(w, "bad request", http.StatusBadRequest)
			return nil, albums.Track{}, false
		}
		stem = unescaped
	}
	if !album.ValidateStem(stem) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return nil, albums.Track{}, false
	}

	alb := albumFromContext(r)
	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		log.Printf("get tracks error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return nil, albums.Track{}, false
	}
	for _, t := range tracks {
		if t.Stem == stem {
			return alb, t, true
		}
	}
	jsonError(w, "not found", http.StatusNotFound)
	return nil, albums.Track{}, false
}

func (s *Server) handleStreamTrack(w http.ResponseWriter, r *http.Request) {
	alb, track, ok := s.requestTrack(w, r)
	if !ok {
		return
	}

	if r.URL.Query().Get("dl") == "1" && alb.DownloadsEnabled {
		name := track.Title
		if name == "" {
			name = track.Stem
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": sanitizeDownloadFilename(name) + ".mp3",
		}))
	}

	album.StreamTrack(w, r, alb.AlbumPath, track.Stem)
}

func (s *Server) handleGetLyrics(w http.ResponseWriter, r *http.Request) {
	alb, track, ok := s.requestTrack(w, r)
	if !ok {
		return
	}

	resp := album.ServeLyrics(alb.AlbumPath, track.Stem)
	if resp == nil {
		jsonError(w, "no lyrics", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "private, max-age=3600")
	jsonOK(w, resp)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	alb := albumFromContext(r)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		log.Printf("get tracks error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	stems := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		stems[t.Stem] = true
	}

	if err := s.collector.RecordBatch(auth.HashToken(cookieValue(r, listenerCookie)), body, alb.ID, stems); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Admin handlers ---

func (s *Server) handleAdminAuth(w http.ResponseWriter, r *http.Request) {
	clientIP := s.clientIPs.ClientIP(r)
	// The per-IP limit comes first so that nothing below (bcrypt, audit rows,
	// lockout entries) can be driven faster than it allows.
	if !s.rateLimiter.Allow("admin:"+auth.RateKey(clientIP), adminLoginLimit) {
		w.Header().Set("Retry-After", strconv.Itoa(int(auth.RateWindow.Seconds())))
		jsonError(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		s.recordAdminAuthAttempt(r, "", "rejected", "bad_request")
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(req.Username)
	guardName := strings.ToLower(username)
	if normalized, err := normalizeAdminUsername(username); err == nil {
		guardName = normalized
	}

	loginGuardKey := guardName + "|" + auth.RateKey(clientIP)
	if allowed, retryAfter := s.adminLoginGuard.allow(loginGuardKey, time.Now().UTC()); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(max(1, math.Ceil(retryAfter.Seconds())))))
		s.recordAdminAuthAttempt(r, username, "rejected", "lockout")
		jsonError(w, "try again later", http.StatusTooManyRequests)
		return
	}

	user, err := s.authenticateAdminCredentials(username, req.Password)
	if err != nil {
		if errors.Is(err, errAdminInvalidCreds) {
			s.adminLoginGuard.markFailure(loginGuardKey, time.Now().UTC())
			s.recordAdminAuthAttempt(r, username, "rejected", "invalid_credentials")
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		log.Printf("admin auth error: %v", err)
		s.recordAdminAuthAttempt(r, username, "error", "auth_query_failed")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.adminLoginGuard.markSuccess(loginGuardKey)

	if oldCookie, err := r.Cookie(adminCookie); err == nil && oldCookie.Value != "" {
		_ = s.sessions.DeleteAdminSession(oldCookie.Value)
	}

	sessionID, err := s.sessions.CreateAdminSession(user.ID, auth.RateKey(clientIP), strings.TrimSpace(r.UserAgent()))
	if err != nil {
		log.Printf("create admin session error: %v", err)
		s.recordAdminAuthAttempt(r, username, "error", "session_create_failed")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	setAdminCookie(w, r, sessionID, int(auth.AdminSessionExpiry.Seconds()))

	s.recordAdminAuthAttempt(r, user.Username, "success", "ok")
	jsonOK(w, map[string]interface{}{
		"status":                  "ok",
		"username":                user.Username,
		"password_reset_required": user.RequirePasswordReset,
	})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(adminCookie)
	if err == nil {
		s.sessions.DeleteAdminSession(cookie.Value)
	}

	setAdminCookie(w, r, "", -1)

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminAnalytics(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	filter, err := parseAnalyticsFilter(r.URL.Query())
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	filter.AlbumID = &alb.ID

	limit := clampInt(parseOptionalInt(r.URL.Query().Get("sessions_limit"), 50), 1, 200)

	trackStats, err := analytics.GetTrackStatsFiltered(s.db, filter)
	if err != nil {
		log.Printf("track stats error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	overall, err := analytics.GetOverallStatsFiltered(s.db, filter)
	if err != nil {
		log.Printf("overall stats error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessions, err := analytics.GetSessionTimelineFiltered(s.db, limit, filter)
	if err != nil {
		log.Printf("session timeline error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	heatmaps := make(map[string][]analytics.DropoutBin)
	for _, ts := range trackStats {
		bins, err := analytics.GetDropoutHeatmapFiltered(s.db, ts.Stem, filter)
		if err != nil {
			log.Printf("dropout heatmap error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		heatmaps[ts.Stem] = bins
	}

	jsonOK(w, map[string]interface{}{
		"tracks":   trackStats,
		"overall":  overall,
		"sessions": sessions,
		"heatmaps": heatmaps,
		"filter": map[string]interface{}{
			"from":        formatFilterTime(filter.From),
			"to":          formatFilterTime(filter.To),
			"stems":       filter.Stems,
			"event_types": filter.EventTypes,
		},
	})
}

func (s *Server) handleAdminGetTracks(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}
	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	trackInfos := album.GetTrackList(tracks, alb.AlbumPath)
	jsonOK(w, trackInfos)
}

func (s *Server) handleAdminUpdateTracks(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	var req struct {
		Title  *string           `json:"title"`
		Artist *string           `json:"artist"`
		Tracks []adminTrackInput `json:"tracks"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	// Validate everything before writing anything, so a rejected track list
	// does not leave a half-applied rename behind.
	var normalized []albums.Track
	if req.Tracks != nil {
		existingTracks, err := s.albumStore.GetTracks(alb.ID)
		if err != nil {
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		normalized, err = normalizeAdminTrackUpdate(req.Tracks, existingTracks, alb.AlbumPath)
		if err != nil {
			jsonError(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	title, artist, ok := applyAlbumMetadata(alb, req.Title, req.Artist)
	if !ok {
		jsonError(w, "bad request: title and artist must be at most 256 characters", http.StatusBadRequest)
		return
	}
	if title != alb.Title || artist != alb.Artist {
		if err := s.albumStore.UpdateAlbum(alb.ID, title, artist); err != nil {
			log.Printf("update album error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if req.Tracks != nil {
		if err := s.albumStore.SetTracks(alb.ID, normalized); err != nil {
			log.Printf("update tracks error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

// applyAlbumMetadata merges optional title/artist edits into the album's
// current values. A blank title keeps the old one; a blank artist clears it.
func applyAlbumMetadata(alb *albums.Album, title, artist *string) (string, string, bool) {
	nextTitle, nextArtist := alb.Title, alb.Artist
	if title != nil {
		if t := trimAndCollapseSpaces(*title); t != "" {
			nextTitle = t
		}
	}
	if artist != nil {
		nextArtist = trimAndCollapseSpaces(*artist)
	}
	return nextTitle, nextArtist, len(nextTitle) <= 256 && len(nextArtist) <= 256
}

func (s *Server) handleAdminUpdateAdminPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	adminUserID, ok := adminUserIDFromContext(r)
	if !ok {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	err := s.updateAdminPassword(adminUserID, req.CurrentPassword, req.NewPassword)
	if err != nil {
		switch {
		case errors.Is(err, errAdminInvalidCreds):
			jsonError(w, "unauthorized", http.StatusUnauthorized)
		case errors.Is(err, errAdminWeakPassword):
			jsonError(w, err.Error(), http.StatusBadRequest)
		default:
			log.Printf("admin password update error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Force re-auth after password rotation.
	setAdminCookie(w, r, "", -1)

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminUploadCover(w http.ResponseWriter, r *http.Request) {
	alb := s.adminAlbumFromRequest(w, r)
	if alb == nil {
		return
	}

	file, _, err := r.FormFile("cover")
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, "read error", http.StatusBadRequest)
		return
	}

	if len(data) == 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	contentType := http.DetectContentType(data)
	if contentType != "image/jpeg" && contentType != "image/png" {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	// Check dimensions from the header before decoding — a small compressed
	// payload can otherwise expand into a huge allocation (decompression bomb).
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 4096 || cfg.Height > 4096 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := writeFileAtomic(filepath.Join(s.coverDir(alb.ID), album.CoverOverrideName), encoded.Bytes()); err != nil {
		log.Printf("write cover error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminGetConfig(w http.ResponseWriter, r *http.Request) {
	adminUsername := ""
	passwordResetRequired := false
	if adminUserID, ok := adminUserIDFromContext(r); ok {
		if identity, err := s.getAdminIdentityByID(adminUserID); err == nil {
			adminUsername = identity.Username
			passwordResetRequired = identity.RequirePasswordReset
		}
	}

	albumCount, _ := s.albumStore.AlbumCount()
	passwordCount, _ := queryCount(s.db, "SELECT COUNT(*) FROM listener_passwords")

	jsonOK(w, map[string]interface{}{
		"admin_user":              adminUsername,
		"password_reset_required": passwordResetRequired,
		"album_count":             albumCount,
		"password_count":          passwordCount,
	})
}

func (s *Server) coverDir(albumID int64) string {
	return filepath.Join(s.dataPath, "covers", strconv.FormatInt(albumID, 10))
}

// writeFileAtomic replaces path via a rename, so readers never see a partial file.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// --- Static file handlers ---

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	serveStatic(w, r, "static", strings.TrimPrefix(r.URL.Path, "/"))
}

func (s *Server) handleAdminStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	if strings.HasPrefix(path, "api/") {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	serveStatic(w, r, "static/admin", path)
}

// staticETags maps each embedded file to a content hash, computed once.
var staticETags = sync.OnceValue(func() map[string]string {
	etags := map[string]string{}
	_ = fs.WalkDir(acetate.StaticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(acetate.StaticFS, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return etags
})

// serveStatic serves dir/name from the embedded files. Extension-less paths
// that name no file get the page shell, so client-side URLs still load.
// Files revalidate on every use (no-cache + ETag), so a deploy is picked up
// immediately and the service worker never caches a stale copy.
func serveStatic(w http.ResponseWriter, r *http.Request, dir, name string) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	full := dir + "/" + name
	etag, ok := staticETags()[full]
	if !ok {
		if filepath.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		full = dir + "/index.html"
		etag = staticETags()[full]
	}
	data, err := fs.ReadFile(acetate.StaticFS, full)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if ctype := mime.TypeByExtension(filepath.Ext(full)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, full, time.Time{}, bytes.NewReader(data))
}

type adminTrackInput struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
}

func normalizeAdminTrackUpdate(input []adminTrackInput, existing []albums.Track, albumPath string) ([]albums.Track, error) {
	if len(input) != len(existing) {
		return nil, errors.New("invalid track count")
	}

	existingStems := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		existingStems[t.Stem] = struct{}{}
	}

	seen := make(map[string]struct{}, len(input))
	normalized := make([]albums.Track, 0, len(input))

	for i, t := range input {
		stem := strings.TrimSpace(t.Stem)
		title := trimAndCollapseSpaces(t.Title)
		display := strings.TrimSpace(t.DisplayIndex)

		if !album.ValidateStem(stem) || title == "" || len(title) > 256 || len(display) > 32 {
			return nil, errors.New("invalid track fields")
		}
		if _, ok := existingStems[stem]; !ok {
			return nil, errors.New("unknown stem")
		}
		if _, ok := seen[stem]; ok {
			return nil, errors.New("duplicate stem")
		}
		if _, err := os.Stat(filepath.Join(albumPath, stem+".mp3")); err != nil {
			return nil, errors.New("missing mp3")
		}

		seen[stem] = struct{}{}
		normalized = append(normalized, albums.Track{
			Stem:         stem,
			Title:        title,
			DisplayIndex: display,
			SortOrder:    i,
		})
	}

	return normalized, nil
}

// sanitizeDownloadFilename keeps a track title usable as a file name: it drops
// control characters and characters that filesystems or shells treat specially.
func sanitizeDownloadFilename(name string) string {
	out := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return -1
		}
		return r
	}, name)
	// Leading dots would make a hidden file.
	out = strings.TrimLeft(strings.TrimSpace(out), ".")
	if out == "" {
		return "track"
	}
	return out
}

// adminAlbumFromRequest extracts and validates the album {id} from the admin URL.
func (s *Server) adminAlbumFromRequest(w http.ResponseWriter, r *http.Request) *albums.Album {
	id, ok := urlID(w, r)
	if !ok {
		return nil
	}
	alb, err := s.albumStore.GetAlbum(id)
	if err != nil {
		log.Printf("get album error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return nil
	}
	if alb == nil {
		jsonError(w, "album not found", http.StatusNotFound)
		return nil
	}
	return alb
}
