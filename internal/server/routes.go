package server

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	acetate "acetate"
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

	// Public API endpoints
	r.Route("/api", func(r chi.Router) {
		// Auth — no session required
		r.With(bodyLimiter(1024)).Post("/auth", s.handleAuth)

		// Session-gated endpoints
		r.Group(func(r chi.Router) {
			r.Use(s.requireSession)

			r.Delete("/auth", s.handleLogout)
			r.Get("/session", s.handleSessionCheck)
			r.Get("/albums", s.handleListAccessibleAlbums)

			// Album-scoped endpoints
			r.Route("/albums/{slug}", func(r chi.Router) {
				r.Use(s.requireAlbumAccess)
				r.With(cacheControl("private, no-cache")).Get("/tracks", s.handleGetTracks)
				r.Get("/cover", s.handleGetCover)
				r.Get("/stream/{stem}", s.handleStreamTrack)
				r.With(cacheControl("private, max-age=3600")).Get("/lyrics/{stem}", s.handleGetLyrics)
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

// Ensure interfaces are used to prevent "imported and not used" errors.
var _ = auth.VerifyPassphrase

// --- Auth handlers ---

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	clientIP := s.cfIPs.GetClientIP(r)

	if !s.rateLimiter.Allow(clientIP) {
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

	passwordID, albumIDs, err := s.albumStore.VerifyPassword(req.Passphrase)
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
	if oldCookie, err := r.Cookie("acetate_session"); err == nil && oldCookie.Value != "" {
		_ = s.sessions.DeleteSession(oldCookie.Value)
	}

	sessionID, err := s.sessions.CreateSession(clientIP, passwordID)
	if err != nil {
		log.Printf("create session error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_session",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60, // 7 days
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	// Record session start
	s.collector.Record(analytics.Event{
		SessionID: sessionID,
		EventType: "session_start",
	})

	// Build album list for response
	accessibleAlbums, err := s.albumStore.GetAlbumsForPassword(passwordID)
	if err != nil {
		log.Printf("get albums for password error: %v", err)
		accessibleAlbums = nil
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

	_ = albumIDs // album IDs already captured via GetAlbumsForPassword
	jsonOK(w, map[string]interface{}{
		"status": "ok",
		"albums": albumList,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := s.getSessionID(r)
	if sessionID != "" {
		s.sessions.DeleteSession(sessionID)

		s.collector.Record(analytics.Event{
			SessionID: sessionID,
			EventType: "session_end",
		})
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleSessionCheck(w http.ResponseWriter, r *http.Request) {
	passwordID := passwordIDFromContext(r)
	type albumResponse struct {
		Slug   string `json:"slug"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	albumList := make([]albumResponse, 0)
	if passwordID > 0 {
		accessibleAlbums, err := s.albumStore.GetAlbumsForPassword(passwordID)
		if err == nil {
			for _, a := range accessibleAlbums {
				albumList = append(albumList, albumResponse{Slug: a.Slug, Title: a.Title, Artist: a.Artist})
			}
		}
	}
	jsonOK(w, map[string]interface{}{
		"status": "ok",
		"albums": albumList,
	})
}

func (s *Server) handleListAccessibleAlbums(w http.ResponseWriter, r *http.Request) {
	passwordID := passwordIDFromContext(r)
	type albumResponse struct {
		Slug   string `json:"slug"`
		Title  string `json:"title"`
		Artist string `json:"artist"`
	}
	albumList := make([]albumResponse, 0)
	if passwordID > 0 {
		accessibleAlbums, err := s.albumStore.GetAlbumsForPassword(passwordID)
		if err == nil {
			for _, a := range accessibleAlbums {
				albumList = append(albumList, albumResponse{Slug: a.Slug, Title: a.Title, Artist: a.Artist})
			}
		}
	}
	jsonOK(w, map[string]interface{}{"albums": albumList})
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
		"title":  alb.Title,
		"artist": alb.Artist,
		"tracks": trackInfos,
	})
}

func (s *Server) handleGetCover(w http.ResponseWriter, r *http.Request) {
	alb := albumFromContext(r)
	album.ServeCover(w, r, alb.AlbumPath, s.dataPath)
}

func (s *Server) handleStreamTrack(w http.ResponseWriter, r *http.Request) {
	rawStem := chi.URLParam(r, "stem")
	stem, err := normalizeStemParam(rawStem)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if !album.ValidateStem(stem) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	alb := albumFromContext(r)
	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !album.StemInTracks(stem, tracks) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	album.StreamTrack(w, r, alb.AlbumPath, stem)
}

func (s *Server) handleGetLyrics(w http.ResponseWriter, r *http.Request) {
	rawStem := chi.URLParam(r, "stem")
	stem, err := normalizeStemParam(rawStem)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if !album.ValidateStem(stem) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	alb := albumFromContext(r)
	tracks, err := s.albumStore.GetTracks(alb.ID)
	if err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !album.StemInTracks(stem, tracks) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	resp := album.ServeLyrics(w, alb.AlbumPath, stem)
	if resp == nil {
		jsonError(w, "no lyrics", http.StatusNotFound)
		return
	}

	jsonOK(w, resp)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	sessionID := s.getSessionID(r)
	alb := albumFromContext(r)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	var albumID int64
	if alb != nil {
		albumID = alb.ID
	}

	if err := s.collector.RecordBatch(sessionID, body, albumID); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Admin handlers ---

func (s *Server) handleAdminAuth(w http.ResponseWriter, r *http.Request) {
	clientIP := s.cfIPs.GetClientIP(r)
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

	loginGuardKey := strings.ToLower(strings.TrimSpace(username)) + "|" + strings.TrimSpace(clientIP)
	if allowed, retryAfter := s.adminLoginGuard.allow(loginGuardKey, time.Now().UTC()); !allowed {
		retrySeconds := int(retryAfter.Seconds())
		if retryAfter%time.Second != 0 {
			retrySeconds++
		}
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
		s.recordAdminAuthAttempt(r, username, "rejected", "lockout")
		jsonError(w, "try again later", http.StatusTooManyRequests)
		return
	}
	// Rate-limit by client + attempted username to reduce brute-force effectiveness.
	rateKey := "admin:" + clientIP + ":" + strings.ToLower(username)
	if !s.rateLimiter.Allow(rateKey) {
		s.recordAdminAuthAttempt(r, username, "rejected", "rate_limited")
		jsonError(w, "rate limited", http.StatusTooManyRequests)
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

	if oldCookie, err := r.Cookie("acetate_admin"); err == nil && oldCookie.Value != "" {
		_ = s.sessions.DeleteAdminSession(oldCookie.Value)
	}

	sessionID, err := s.sessions.CreateAdminSessionWithContext(user.ID, clientIP, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		log.Printf("create admin session error: %v", err)
		s.recordAdminAuthAttempt(r, username, "error", "session_create_failed")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_admin",
		Value:    sessionID,
		Path:     "/admin",
		MaxAge:   3600, // 1 hour
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	s.recordAdminAuthAttempt(r, user.Username, "success", "ok")
	jsonOK(w, map[string]interface{}{
		"status":                  "ok",
		"username":                user.Username,
		"password_reset_required": user.RequirePasswordReset,
	})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("acetate_admin")
	if err == nil {
		s.sessions.DeleteAdminSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_admin",
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

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
		if err == nil {
			heatmaps[ts.Stem] = bins
		}
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
		Title  string `json:"title"`
		Artist string `json:"artist"`
		Tracks []struct {
			Stem         string `json:"stem"`
			Title        string `json:"title"`
			DisplayIndex string `json:"display_index,omitempty"`
		} `json:"tracks"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.Title != "" || req.Artist != "" {
		title := alb.Title
		artist := alb.Artist
		if req.Title != "" {
			title = trimAndCollapseSpaces(req.Title)
		}
		if req.Artist != "" {
			artist = trimAndCollapseSpaces(req.Artist)
		}
		if err := s.albumStore.UpdateAlbum(alb.ID, title, artist); err != nil {
			log.Printf("update album error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if req.Tracks != nil {
		existingTracks, err := s.albumStore.GetTracks(alb.ID)
		if err != nil {
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}

		normalized, err := normalizeAdminTrackUpdate(req.Tracks, existingTracks, alb.AlbumPath)
		if err != nil {
			jsonError(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.albumStore.SetTracks(alb.ID, normalized); err != nil {
			log.Printf("update tracks error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	jsonOK(w, map[string]string{"status": "ok"})
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
			jsonError(w, "new password does not meet policy", http.StatusBadRequest)
		default:
			log.Printf("admin password update error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Force re-auth after password rotation.
	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_admin",
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

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

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > 4096 || b.Dy() > 4096 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	coverPath := filepath.Join(alb.AlbumPath, "cover_override.jpg")
	if err := os.WriteFile(coverPath, encoded.Bytes(), 0644); err != nil {
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
	passwords, _ := s.albumStore.ListPasswords()

	jsonOK(w, map[string]interface{}{
		"admin_user":              adminUsername,
		"password_reset_required": passwordResetRequired,
		"album_count":             albumCount,
		"password_count":          len(passwords),
	})
}

// --- Static file handlers ---

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	staticFS, err := fs.Sub(acetate.StaticFS, "static")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	if strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}

	if _, err := fs.Stat(staticFS, path); err != nil {
		path = "index.html"
	}

	// Set cache headers for static assets
	if path != "index.html" && path != "sw.js" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	serveEmbeddedFile(w, r, staticFS, path)
}

func (s *Server) handleAdminStatic(w http.ResponseWriter, r *http.Request) {
	staticFS, err := fs.Sub(acetate.StaticFS, "static/admin")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	if path == "" || path == "/" {
		path = "index.html"
	}

	// Don't serve admin static for API paths
	if strings.HasPrefix(path, "api/") {
		http.NotFound(w, r)
		return
	}

	if strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}

	if _, err := fs.Stat(staticFS, path); err != nil {
		path = "index.html"
	}

	w.Header().Set("Cache-Control", "no-store")
	serveEmbeddedFile(w, r, staticFS, path)
}

func normalizeAdminTrackUpdate(input []struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
}, existing []albums.Track, albumPath string) ([]albums.Track, error) {
	if len(input) == 0 || len(input) != len(existing) {
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

// adminAlbumFromRequest extracts and validates the album {id} from the admin URL.
func (s *Server) adminAlbumFromRequest(w http.ResponseWriter, r *http.Request) *albums.Album {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
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

func serveEmbeddedFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, path string) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if ctype := mime.TypeByExtension(filepath.Ext(path)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(data))
}
