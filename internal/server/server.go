package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"acetate/internal/analytics"
	"acetate/internal/auth"
	"acetate/internal/config"
)

// Server is the main HTTP server.
type Server struct {
	httpServer  *http.Server
	db          *sql.DB
	config      *config.Manager
	sessions    *auth.SessionStore
	rateLimiter *auth.RateLimiter
	cfIPs       *auth.CloudflareIPs
	collector   *analytics.Collector
	albumPath   string
	dataPath    string
	adminToken  string
}

// Config holds server configuration.
type Config struct {
	ListenAddr string
	AlbumPath  string
	DataPath   string
	AdminToken string
	DB         *sql.DB
	ConfigMgr  *config.Manager
}

// New creates a new Server with all dependencies wired.
func New(cfg Config) *Server {
	sessions := auth.NewSessionStore(cfg.DB)
	rateLimiter := auth.NewRateLimiter()
	cfIPs := auth.NewCloudflareIPs()
	collector := analytics.NewCollector(cfg.DB)

	s := &Server{
		db:          cfg.DB,
		config:      cfg.ConfigMgr,
		sessions:    sessions,
		rateLimiter: rateLimiter,
		cfIPs:       cfIPs,
		collector:   collector,
		albumPath:   cfg.AlbumPath,
		dataPath:    cfg.DataPath,
		adminToken:  cfg.AdminToken,
	}

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      s.routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Long for MP3 streaming
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// Start begins listening for HTTP requests.
func (s *Server) Start() error {
	log.Printf("listening on %s", s.httpServer.Addr)
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully shuts down all components.
func (s *Server) Shutdown(ctx context.Context) {
	log.Println("shutting down HTTP server...")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	log.Println("flushing analytics...")
	s.collector.Close()

	log.Println("stopping background tasks...")
	s.sessions.Close()
	s.rateLimiter.Close()
	s.cfIPs.Close()
}

// Helper functions

func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func jsonOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func jsonCreated(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func (s *Server) getSessionID(r *http.Request) string {
	cookie, err := r.Cookie("acetate_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func limitBody(r *http.Request, maxBytes int64) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
}

// bodyLimiter middleware limits the request body size.
func bodyLimiter(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

