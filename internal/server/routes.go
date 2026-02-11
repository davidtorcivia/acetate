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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	acetate "acetate"
	"acetate/internal/album"
	"acetate/internal/analytics"
	"acetate/internal/auth"
	"acetate/internal/config"
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

			r.With(cacheControl("private, no-cache")).Get("/tracks", s.handleGetTracks)
			r.Get("/cover", s.handleGetCover)
			r.Get("/stream/{stem}", s.handleStreamTrack)
			r.With(cacheControl("private, max-age=3600")).Get("/lyrics/{stem}", s.handleGetLyrics)
			r.With(bodyLimiter(102400)).Post("/analytics", s.handleAnalytics) // 100KB
			r.Get("/session", s.handleSessionCheck)
		})
	})

	// Admin routes
	r.Route("/admin", func(r chi.Router) {
		r.With(bodyLimiter(1024)).Post("/api/auth", s.handleAdminAuth)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Use(cacheControl("no-store"))

			r.Delete("/api/auth", s.handleAdminLogout)
			r.Get("/api/analytics", s.handleAdminAnalytics)
			r.Get("/api/tracks", s.handleAdminGetTracks)
			r.With(bodyLimiter(102400)).Put("/api/tracks", s.handleAdminUpdateTracks)
			r.With(bodyLimiter(1024)).Put("/api/password", s.handleAdminUpdatePassword)
			r.With(bodyLimiter(4096)).Put("/api/admin-password", s.handleAdminUpdateAdminPassword)
			r.With(bodyLimiter(10<<20)).Post("/api/cover", s.handleAdminUploadCover) // 10MB
			r.Get("/api/config", s.handleAdminGetConfig)
			r.Get("/api/reconcile", s.handleAdminReconcilePreview)
			r.With(bodyLimiter(4096)).Post("/api/reconcile", s.handleAdminReconcileApply)
			r.Get("/api/ops/health", s.handleAdminOpsHealth)
			r.Get("/api/ops/stats", s.handleAdminOpsStats)
			r.With(bodyLimiter(4096)).Post("/api/ops/maintenance", s.handleAdminOpsMaintenance)
			r.Get("/api/export/events", s.handleAdminExportEvents)
			r.Get("/api/export/backup", s.handleAdminExportBackup)
		})

		// Serve admin static files
		r.Get("/*", s.handleAdminStatic)
	})

	// SPA static files (public)
	r.Get("/*", s.handleSPA)

	return r
}

// Ensure interfaces are used to prevent "imported and not used" errors.
var (
	_ = auth.VerifyPassphrase
	_ config.Track
)

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

	cfg := s.config.Get()
	if cfg.Password == "" {
		log.Println("WARNING: password not set — rejecting all auth attempts")
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if !auth.VerifyPassphrase(req.Passphrase, cfg.Password) {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Rotate an existing session ID to prevent fixation and stale buildup.
	if oldCookie, err := r.Cookie("acetate_session"); err == nil && oldCookie.Value != "" {
		_ = s.sessions.DeleteSession(oldCookie.Value)
	}

	sessionID, err := s.sessions.CreateSession(clientIP)
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

	jsonOK(w, map[string]string{"status": "ok"})
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
	jsonOK(w, map[string]string{"status": "ok"})
}

// --- Content handlers ---

func (s *Server) handleGetTracks(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.Get()
	tracks := album.GetTrackList(cfg, s.albumPath)

	jsonOK(w, map[string]interface{}{
		"title":  cfg.Title,
		"artist": cfg.Artist,
		"tracks": tracks,
	})
}

func (s *Server) handleGetCover(w http.ResponseWriter, r *http.Request) {
	album.ServeCover(w, r, s.albumPath, s.dataPath)
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

	cfg := s.config.Get()
	if !album.StemInConfig(stem, cfg) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	album.StreamTrack(w, r, s.albumPath, stem)
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

	cfg := s.config.Get()
	if !album.StemInConfig(stem, cfg) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	resp := album.ServeLyrics(w, s.albumPath, stem)
	if resp == nil {
		jsonError(w, "no lyrics", http.StatusNotFound)
		return
	}

	jsonOK(w, resp)
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	sessionID := s.getSessionID(r)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := s.collector.RecordBatch(sessionID, body); err != nil {
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
			s.recordAdminAuthAttempt(r, username, "rejected", "invalid_credentials")
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		log.Printf("admin auth error: %v", err)
		s.recordAdminAuthAttempt(r, username, "error", "auth_query_failed")
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

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
	jsonOK(w, map[string]string{
		"status":   "ok",
		"username": user.Username,
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
	filter, err := parseAnalyticsFilter(r.URL.Query())
	if err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

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

	// Get heatmaps for each track with stats
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
	cfg := s.config.Get()
	tracks := album.GetTrackList(cfg, s.albumPath)
	jsonOK(w, tracks)
}

func (s *Server) handleAdminUpdateTracks(w http.ResponseWriter, r *http.Request) {
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

	cfg := s.config.Get()
	if req.Title != "" {
		cfg.Title = trimAndCollapseSpaces(req.Title)
	}
	if req.Artist != "" {
		cfg.Artist = trimAndCollapseSpaces(req.Artist)
	}
	if req.Tracks != nil {
		normalized, err := normalizeTrackUpdate(req.Tracks, cfg.Tracks, s.albumPath)
		if err != nil {
			jsonError(w, "bad request", http.StatusBadRequest)
			return
		}
		cfg.Tracks = normalized
	}

	if err := s.config.Update(cfg); err != nil {
		log.Printf("update config error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminUpdatePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Passphrase) == "" {
		jsonError(w, "passphrase cannot be empty", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Passphrase), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("hash passphrase error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	cfg := s.config.Get()
	cfg.Password = string(hash)
	if err := s.config.Update(cfg); err != nil {
		log.Printf("update config error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
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

	coverPath := filepath.Join(s.dataPath, "cover_override.jpg")
	if err := os.WriteFile(coverPath, encoded.Bytes(), 0644); err != nil {
		log.Printf("write cover error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.Get()
	adminUsername := ""
	if adminUserID, ok := adminUserIDFromContext(r); ok {
		if username, err := s.getAdminUsernameByID(adminUserID); err == nil {
			adminUsername = username
		}
	}
	// Truncate password hash for display
	passDisplay := ""
	if cfg.Password != "" {
		if len(cfg.Password) > 20 {
			passDisplay = cfg.Password[:20] + "..."
		} else {
			passDisplay = cfg.Password
		}
	}

	jsonOK(w, map[string]interface{}{
		"title":         cfg.Title,
		"artist":        cfg.Artist,
		"admin_user":    adminUsername,
		"password_set":  cfg.Password != "",
		"password_hash": passDisplay,
		"track_count":   len(cfg.Tracks),
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

func normalizeTrackUpdate(input []struct {
	Stem         string `json:"stem"`
	Title        string `json:"title"`
	DisplayIndex string `json:"display_index,omitempty"`
}, existing []config.Track, albumPath string) ([]config.Track, error) {
	if len(input) == 0 || len(input) != len(existing) {
		return nil, errors.New("invalid track count")
	}

	existingStems := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		existingStems[t.Stem] = struct{}{}
	}

	seen := make(map[string]struct{}, len(input))
	normalized := make([]config.Track, 0, len(input))

	for _, t := range input {
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
		normalized = append(normalized, config.Track{
			Stem:         stem,
			Title:        title,
			DisplayIndex: display,
		})
	}

	return normalized, nil
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
