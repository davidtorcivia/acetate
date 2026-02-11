package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
			r.With(bodyLimiter(10 << 20)).Post("/api/cover", s.handleAdminUploadCover) // 10MB
			r.Get("/api/config", s.handleAdminGetConfig)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		Secure:   true,
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
		Secure:   true,
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
	stem := chi.URLParam(r, "stem")

	if !album.ValidateStem(stem) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	cfg := s.config.Get()
	if !album.StemInConfig(stem, cfg) {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}

	album.StreamTrack(w, r, s.albumPath, stem)
}

func (s *Server) handleGetLyrics(w http.ResponseWriter, r *http.Request) {
	stem := chi.URLParam(r, "stem")

	if !album.ValidateStem(stem) {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	cfg := s.config.Get()
	if !album.StemInConfig(stem, cfg) {
		jsonError(w, "not found", http.StatusNotFound)
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
	if s.adminToken == "" {
		jsonError(w, "admin disabled", http.StatusForbidden)
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.adminToken)) != 1 {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID, err := s.sessions.CreateAdminSession()
	if err != nil {
		log.Printf("create admin session error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_admin",
		Value:    sessionID,
		Path:     "/admin",
		MaxAge:   3600, // 1 hour
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	jsonOK(w, map[string]string{"status": "ok"})
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
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminAnalytics(w http.ResponseWriter, r *http.Request) {
	trackStats, err := analytics.GetTrackStats(s.db)
	if err != nil {
		log.Printf("track stats error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	overall, err := analytics.GetOverallStats(s.db)
	if err != nil {
		log.Printf("overall stats error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessions, err := analytics.GetSessionTimeline(s.db, 50)
	if err != nil {
		log.Printf("session timeline error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Get heatmaps for each track with stats
	heatmaps := make(map[string][]analytics.DropoutBin)
	for _, ts := range trackStats {
		bins, err := analytics.GetDropoutHeatmap(s.db, ts.Stem)
		if err == nil {
			heatmaps[ts.Stem] = bins
		}
	}

	jsonOK(w, map[string]interface{}{
		"tracks":   trackStats,
		"overall":  overall,
		"sessions": sessions,
		"heatmaps": heatmaps,
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	cfg := s.config.Get()
	if req.Title != "" {
		cfg.Title = req.Title
	}
	if req.Artist != "" {
		cfg.Artist = req.Artist
	}
	if req.Tracks != nil {
		cfg.Tracks = nil
		for _, t := range req.Tracks {
			cfg.Tracks = append(cfg.Tracks, config.Track{
				Stem:         t.Stem,
				Title:        t.Title,
				DisplayIndex: t.DisplayIndex,
			})
		}
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	if req.Passphrase == "" {
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

	coverPath := filepath.Join(s.dataPath, "cover_override.jpg")
	if err := os.WriteFile(coverPath, data, 0644); err != nil {
		log.Printf("write cover error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.Get()
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

	f, err := staticFS.Open(path)
	if err != nil {
		// SPA fallback — serve index.html for all unmatched routes
		path = "index.html"
		f, err = staticFS.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	f.Close()

	// Set cache headers for static assets
	if path != "index.html" && path != "sw.js" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
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

	f, err := staticFS.Open(path)
	if err != nil {
		path = "index.html"
		f, err = staticFS.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	f.Close()

	w.Header().Set("Cache-Control", "no-cache")
	http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
}
