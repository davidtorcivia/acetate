package auth

import (
	"testing"
	"time"

	"acetate/internal/database"

	"golang.org/x/crypto/bcrypt"
)

func testDB(t *testing.T) *SessionStore {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := NewSessionStore(db)
	t.Cleanup(func() { store.Close() })
	return store
}

func seedAdminUser(t *testing.T, store *SessionStore) int64 {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte("admin-pass-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate admin hash: %v", err)
	}
	res, err := store.db.Exec(
		"INSERT INTO admin_users (username, password_hash, is_active, created_at, updated_at) VALUES (?, ?, 1, datetime('now'), datetime('now'))",
		"admin", string(hash),
	)
	if err != nil {
		t.Fatalf("insert admin user: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("admin user id: %v", err)
	}
	return id
}

func TestCreateAndValidateSession(t *testing.T) {
	store := testDB(t)

	id, err := store.CreateSession("127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	valid, err := store.ValidateSession(id)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if !valid {
		t.Error("session should be valid")
	}
}

func TestDeleteSession(t *testing.T) {
	store := testDB(t)

	id, err := store.CreateSession("127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := store.DeleteSession(id); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	valid, err := store.ValidateSession(id)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if valid {
		t.Error("session should be invalid after delete")
	}
}

func TestInvalidSession(t *testing.T) {
	store := testDB(t)

	valid, err := store.ValidateSession("nonexistent")
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if valid {
		t.Error("nonexistent session should be invalid")
	}
}

func TestAdminSession(t *testing.T) {
	store := testDB(t)
	adminUserID := seedAdminUser(t, store)

	id, err := store.CreateAdminSessionWithContext(adminUserID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("CreateAdminSessionWithContext: %v", err)
	}

	valid, err := store.ValidateAdminSession(id)
	if err != nil {
		t.Fatalf("ValidateAdminSession: %v", err)
	}
	if !valid {
		t.Error("admin session should be valid")
	}

	if err := store.DeleteAdminSession(id); err != nil {
		t.Fatalf("DeleteAdminSession: %v", err)
	}

	valid, err = store.ValidateAdminSession(id)
	if err != nil {
		t.Fatalf("ValidateAdminSession after delete: %v", err)
	}
	if valid {
		t.Error("admin session should be invalid after delete")
	}
}

func TestAdminSessionFingerprintBinding(t *testing.T) {
	store := testDB(t)
	adminUserID := seedAdminUser(t, store)

	id, err := store.CreateAdminSessionWithContext(adminUserID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("CreateAdminSessionWithContext: %v", err)
	}

	valid, _, err := store.ValidateAdminSessionWithContext(id, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("ValidateAdminSessionWithContext: %v", err)
	}
	if !valid {
		t.Fatal("session should validate for same fingerprint")
	}

	valid, _, err = store.ValidateAdminSessionWithContext(id, "127.0.0.2", "test-agent")
	if err != nil {
		t.Fatalf("ValidateAdminSessionWithContext mismatch: %v", err)
	}
	if valid {
		t.Fatal("session should be invalid for mismatched fingerprint")
	}
}

func TestVerifyPassphrase(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("testpass"), bcrypt.DefaultCost)

	if !VerifyPassphrase("testpass", string(hash)) {
		t.Error("correct passphrase should verify")
	}
	if VerifyPassphrase("wrongpass", string(hash)) {
		t.Error("wrong passphrase should not verify")
	}
}

func TestSessionRotation(t *testing.T) {
	store := testDB(t)

	id1, _ := store.CreateSession("127.0.0.1")
	id2, _ := store.CreateSession("127.0.0.1")

	if id1 == id2 {
		t.Error("session IDs should be unique (rotation)")
	}

	// Both sessions should be valid
	v1, _ := store.ValidateSession(id1)
	v2, _ := store.ValidateSession(id2)
	if !v1 || !v2 {
		t.Error("both sessions should be valid")
	}
}

func TestIPHashing(t *testing.T) {
	h1 := hashIP("192.168.1.1", "salt1")
	h2 := hashIP("192.168.1.1", "salt1")
	h3 := hashIP("192.168.1.1", "salt2")

	if h1 != h2 {
		t.Error("same IP and salt should produce same hash")
	}
	if h1 == h3 {
		t.Error("different salt should produce different hash")
	}
}

func TestCleanup(t *testing.T) {
	store := testDB(t)

	// Manually insert an expired session
	expired := time.Now().UTC().Add(-8 * 24 * time.Hour)
	store.db.Exec(
		"INSERT INTO sessions (id, started_at, last_seen_at, ip_hash) VALUES (?, ?, ?, ?)",
		"expired-session", expired, expired, "hash",
	)

	store.cleanup()

	valid, _ := store.ValidateSession("expired-session")
	if valid {
		t.Error("expired session should have been cleaned up")
	}
}
