package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"acetate/internal/analytics"
	"acetate/internal/auth"
	"acetate/internal/config"
)

// Server is the main HTTP server.
type Server struct {
	httpServer             *http.Server
	db                     *sql.DB
	config                 *config.Manager
	sessions               *auth.SessionStore
	rateLimiter            *auth.RateLimiter
	cfIPs                  *auth.CloudflareIPs
	collector              *analytics.Collector
	albumPath              string
	dataPath               string
	adminToken             string
	adminTokenHash         string
	analyticsRetentionDays int
	maintenanceInterval    time.Duration
	startedAt              time.Time
	maintenanceDone        chan struct{}
	maintenanceWG          sync.WaitGroup
	maintenanceStopOnce    sync.Once
}

// Config holds server configuration.
type Config struct {
	ListenAddr             string
	AlbumPath              string
	DataPath               string
	AdminToken             string
	AdminTokenHash         string
	AnalyticsRetentionDays int
	MaintenanceInterval    time.Duration
	DB                     *sql.DB
	ConfigMgr              *config.Manager
}

// New creates a new Server with all dependencies wired.
func New(cfg Config) *Server {
	sessions := auth.NewSessionStore(cfg.DB)
	rateLimiter := auth.NewRateLimiter()
	cfIPs := auth.NewCloudflareIPs()
	collector := analytics.NewCollector(cfg.DB)

	s := &Server{
		db:                     cfg.DB,
		config:                 cfg.ConfigMgr,
		sessions:               sessions,
		rateLimiter:            rateLimiter,
		cfIPs:                  cfIPs,
		collector:              collector,
		albumPath:              cfg.AlbumPath,
		dataPath:               cfg.DataPath,
		adminToken:             cfg.AdminToken,
		adminTokenHash:         cfg.AdminTokenHash,
		analyticsRetentionDays: cfg.AnalyticsRetentionDays,
		maintenanceInterval:    cfg.MaintenanceInterval,
		startedAt:              time.Now().UTC(),
		maintenanceDone:        make(chan struct{}),
	}
	if s.maintenanceInterval <= 0 {
		s.maintenanceInterval = 12 * time.Hour
	}

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      s.routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // Long for MP3 streaming
		IdleTimeout:  120 * time.Second,
	}

	s.startMaintenanceLoop()

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

	s.stopMaintenanceLoop()

	log.Println("flushing analytics...")
	s.collector.Close()

	log.Println("stopping background tasks...")
	s.sessions.Close()
	s.rateLimiter.Close()
	s.cfIPs.Close()
}

func (s *Server) startMaintenanceLoop() {
	s.maintenanceWG.Add(1)
	go func() {
		defer s.maintenanceWG.Done()

		run := func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.collector.FlushNow(flushCtx)
			cancel()

			res, err := analytics.RunMaintenance(s.db, time.Now().UTC(), s.analyticsRetentionDays)
			if err != nil {
				log.Printf("analytics maintenance error: %v", err)
				return
			}
			if res.RolledDays > 0 || res.PrunedRows > 0 {
				log.Printf("analytics maintenance: rolled_days=%d rollup_rows=%d pruned_rows=%d retention_days=%d",
					res.RolledDays, res.RollupRows, res.PrunedRows, res.RetentionDays)
			}
		}

		run()
		ticker := time.NewTicker(s.maintenanceInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				run()
			case <-s.maintenanceDone:
				return
			}
		}
	}()
}

func (s *Server) stopMaintenanceLoop() {
	s.maintenanceStopOnce.Do(func() {
		close(s.maintenanceDone)
		s.maintenanceWG.Wait()
	})
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

func decodeJSONBody(r *http.Request, dst interface{}) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return err
	}

	var extra struct{}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON payload")
	}

	return nil
}

func isSecureRequest(r *http.Request) bool {
	return requestScheme(r) == "https"
}

func trimAndCollapseSpaces(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func normalizeStemParam(raw string) (string, error) {
	stem, err := url.PathUnescape(raw)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stem), nil
}
