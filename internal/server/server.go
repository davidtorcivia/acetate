package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"acetate/internal/albums"
	"acetate/internal/analytics"
	"acetate/internal/auth"
)

// Server is the main HTTP server.
type Server struct {
	httpServer             *http.Server
	db                     *sql.DB
	albumStore             *albums.Store
	sessions               *auth.SessionStore
	rateLimiter            *auth.RateLimiter
	adminLoginGuard        *adminLoginGuard
	clientIPs              *auth.ClientIPResolver
	collector              *analytics.Collector
	dataPath               string
	albumBasePath          string
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
	DataPath               string
	AlbumBasePath          string
	AnalyticsRetentionDays int
	MaintenanceInterval    time.Duration
	DB                     *sql.DB
	AlbumStore             *albums.Store
	ClientIPs              *auth.ClientIPResolver // nil trusts no proxy
}

// New creates a new Server with all dependencies wired.
func New(cfg Config) *Server {
	sessions := auth.NewSessionStore(cfg.DB)
	rateLimiter := auth.NewRateLimiter()
	collector := analytics.NewCollector(cfg.DB)
	if cfg.ClientIPs == nil {
		cfg.ClientIPs = &auth.ClientIPResolver{}
	}

	s := &Server{
		db:                     cfg.DB,
		albumStore:             cfg.AlbumStore,
		sessions:               sessions,
		rateLimiter:            rateLimiter,
		adminLoginGuard:        newAdminLoginGuard(),
		clientIPs:              cfg.ClientIPs,
		collector:              collector,
		dataPath:               cfg.DataPath,
		albumBasePath:          cfg.AlbumBasePath,
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
			if res.PrunedRows > 0 || res.PrunedAuditRows > 0 {
				log.Printf("analytics maintenance: pruned_rows=%d pruned_audit_rows=%d retention_days=%d",
					res.PrunedRows, res.PrunedAuditRows, res.RetentionDays)
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

const (
	listenerCookie = "acetate_session"
	adminCookie    = "acetate_admin"
)

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// setListenerCookie sets, or with maxAge -1 clears, the listener session cookie.
func setListenerCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	setSessionCookie(w, r, listenerCookie, "/", value, maxAge)
}

// setAdminCookie sets, or with maxAge -1 clears, the admin session cookie.
func setAdminCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	setSessionCookie(w, r, adminCookie, "/admin", value, maxAge)
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, name, path, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
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

// urlID parses the positive integer {id} URL parameter, answering 400 when it is not one.
func urlID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
