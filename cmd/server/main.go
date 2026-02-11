package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"acetate/internal/config"
	"acetate/internal/database"
	"acetate/internal/server"
)

func main() {
	// Environment configuration
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	albumPath := envOr("ALBUM_PATH", "./album")
	dataPath := envOr("DATA_PATH", "./data")
	adminUsername := envOr("ADMIN_USERNAME", "admin")
	adminPassword := os.Getenv("ADMIN_PASSWORD")
	adminPasswordHash := os.Getenv("ADMIN_PASSWORD_HASH")
	legacyAdminToken := os.Getenv("ADMIN_TOKEN")
	analyticsRetentionDays := envInt("ANALYTICS_RETENTION_DAYS", 0)
	maintenanceInterval := envDuration("ANALYTICS_MAINTENANCE_INTERVAL", 12*time.Hour)

	if strings.TrimSpace(legacyAdminToken) != "" {
		log.Println("WARNING: ADMIN_TOKEN is deprecated and ignored; use ADMIN_USERNAME + ADMIN_PASSWORD_HASH")
	}

	// Validate album path exists
	if _, err := os.Stat(albumPath); os.IsNotExist(err) {
		log.Fatalf("album path does not exist: %s", albumPath)
	}

	// Open database
	db, err := database.Open(dataPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := server.EnsureAdminBootstrap(db, adminUsername, adminPassword, adminPasswordHash); err != nil {
		log.Fatalf("bootstrap admin user: %v", err)
	}

	// Load or generate config
	cfgMgr, err := config.NewManager(dataPath, albumPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	cfg := cfgMgr.Get()
	if cfg.Password == "" {
		log.Println("WARNING: no password set — listeners cannot authenticate until one is configured")
	}
	log.Printf("loaded %d tracks", len(cfg.Tracks))

	// Create and start server
	srv := server.New(server.Config{
		ListenAddr:             listenAddr,
		AlbumPath:              albumPath,
		DataPath:               dataPath,
		AnalyticsRetentionDays: analyticsRetentionDays,
		MaintenanceInterval:    maintenanceInterval,
		DB:                     db,
		ConfigMgr:              cfgMgr,
	})

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	// Wait for shutdown signal or server error
	select {
	case err := <-errCh:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Println("shutdown signal received")
	}

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	srv.Shutdown(shutdownCtx)
	log.Println("shutdown complete")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("WARNING: invalid %s=%q, using %d", key, raw, fallback)
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("WARNING: invalid %s=%q, using %s", key, raw, fallback)
		return fallback
	}
	return v
}
