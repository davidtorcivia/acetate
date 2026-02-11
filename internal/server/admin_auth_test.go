package server

import (
	"testing"

	"acetate/internal/database"
)

func TestEnsureAdminBootstrapCreatesFirstUser(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := EnsureAdminBootstrap(db, "admin", "admin-pass-1234", ""); err != nil {
		t.Fatalf("EnsureAdminBootstrap: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		t.Fatalf("count admin users: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 admin user, got %d", count)
	}
}

func TestEnsureAdminBootstrapRequiresCredentials(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := EnsureAdminBootstrap(db, "admin", "", ""); err == nil {
		t.Fatal("expected bootstrap credential error")
	}
}
