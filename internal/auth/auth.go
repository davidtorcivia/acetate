package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionExpiry      = 7 * 24 * time.Hour
	AdminSessionExpiry = 1 * time.Hour
	CleanupInterval    = 1 * time.Hour
)

// SessionStore manages listener and admin sessions in SQLite.
type SessionStore struct {
	db   *sql.DB
	salt string
	done chan struct{}
}

// NewSessionStore creates a session store and starts the cleanup goroutine.
func NewSessionStore(db *sql.DB) *SessionStore {
	// Generate a random salt for IP hashing
	saltBytes := make([]byte, 16)
	rand.Read(saltBytes)

	s := &SessionStore{
		db:   db,
		salt: hex.EncodeToString(saltBytes),
		done: make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Close stops the cleanup goroutine.
func (s *SessionStore) Close() {
	close(s.done)
}

// CreateSession generates a new listener session and stores it.
func (s *SessionStore) CreateSession(ip string) (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	ipHash := hashIP(ip, s.salt)
	now := time.Now().UTC()

	_, err = s.db.Exec(
		"INSERT INTO sessions (id, started_at, last_seen_at, ip_hash) VALUES (?, ?, ?, ?)",
		id, now, now, ipHash,
	)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	return id, nil
}

// ValidateSession checks if a session ID is valid and not expired.
// On success, it updates last_seen_at (sliding window).
func (s *SessionStore) ValidateSession(id string) (bool, error) {
	var lastSeen time.Time
	err := s.db.QueryRow(
		"SELECT last_seen_at FROM sessions WHERE id = ?", id,
	).Scan(&lastSeen)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query session: %w", err)
	}

	if time.Since(lastSeen) > SessionExpiry {
		s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
		return false, nil
	}

	// Update sliding window
	s.db.Exec("UPDATE sessions SET last_seen_at = ? WHERE id = ?", time.Now().UTC(), id)
	return true, nil
}

// DeleteSession removes a listener session.
func (s *SessionStore) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

// CreateAdminSession generates a new admin session.
func (s *SessionStore) CreateAdminSession() (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(
		"INSERT INTO admin_sessions (id, created_at) VALUES (?, ?)",
		id, time.Now().UTC(),
	)
	if err != nil {
		return "", fmt.Errorf("create admin session: %w", err)
	}

	return id, nil
}

// ValidateAdminSession checks if an admin session is valid (1 hour, no sliding).
func (s *SessionStore) ValidateAdminSession(id string) (bool, error) {
	var createdAt time.Time
	err := s.db.QueryRow(
		"SELECT created_at FROM admin_sessions WHERE id = ?", id,
	).Scan(&createdAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query admin session: %w", err)
	}

	if time.Since(createdAt) > AdminSessionExpiry {
		s.db.Exec("DELETE FROM admin_sessions WHERE id = ?", id)
		return false, nil
	}

	return true, nil
}

// DeleteAdminSession removes an admin session.
func (s *SessionStore) DeleteAdminSession(id string) error {
	_, err := s.db.Exec("DELETE FROM admin_sessions WHERE id = ?", id)
	return err
}

// VerifyPassphrase compares a plaintext passphrase against a bcrypt hash.
func VerifyPassphrase(passphrase, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(passphrase))
	return err == nil
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.done:
			return
		}
	}
}

func (s *SessionStore) cleanup() {
	cutoff := time.Now().UTC().Add(-SessionExpiry)
	s.db.Exec("DELETE FROM sessions WHERE last_seen_at < ?", cutoff)

	adminCutoff := time.Now().UTC().Add(-AdminSessionExpiry)
	s.db.Exec("DELETE FROM admin_sessions WHERE created_at < ?", adminCutoff)
}

func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashIP(ip, salt string) string {
	h := sha256.Sum256([]byte(salt + ip))
	return hex.EncodeToString(h[:])
}
