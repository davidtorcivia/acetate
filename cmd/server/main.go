package main

import (
	"context"
	"log"
	"os"
	"os/signal"
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
	adminToken := os.Getenv("ADMIN_TOKEN")

	if adminToken == "" {
		log.Println("WARNING: ADMIN_TOKEN not set — admin interface disabled")
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
		ListenAddr: listenAddr,
		AlbumPath:  albumPath,
		DataPath:   dataPath,
		AdminToken: adminToken,
		DB:         db,
		ConfigMgr:  cfgMgr,
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
