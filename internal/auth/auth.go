package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionExpiry      = 7 * 24 * time.Hour
	AdminSessionExpiry = 1 * time.Hour
	CleanupInterval    = 1 * time.Hour
	SessionTouchWindow = 1 * time.Minute
	AdminTouchWindow   = 5 * time.Minute
)

// SessionStore manages listener and admin sessions in SQLite.
type SessionStore struct {
	db   *sql.DB
	salt string
	done chan struct{}
	once sync.Once
}

// NewSessionStore creates a session store and starts the cleanup goroutine.
func NewSessionStore(db *sql.DB) *SessionStore {
	// Generate a random salt for IP hashing
	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		// Extremely rare; keep startup non-fatal and continue with a best-effort salt.
		fallback := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
		copy(saltBytes, fallback[:16])
	}

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
	s.once.Do(func() {
		close(s.done)
	})
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

	now := time.Now().UTC()
	if now.Sub(lastSeen.UTC()) > SessionExpiry {
		if _, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id); err != nil {
			return false, fmt.Errorf("delete expired session: %w", err)
		}
		return false, nil
	}

	// Update sliding window at most once per minute to reduce write amplification.
	if now.Sub(lastSeen.UTC()) >= SessionTouchWindow {
		if _, err := s.db.Exec("UPDATE sessions SET last_seen_at = ? WHERE id = ?", now, id); err != nil {
			return false, fmt.Errorf("touch session: %w", err)
		}
	}
	return true, nil
}

// DeleteSession removes a listener session.
func (s *SessionStore) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

// CreateAdminSession generates a new admin session.
func (s *SessionStore) CreateAdminSession() (string, error) {
	return s.CreateAdminSessionWithContext("", "")
}

// CreateAdminSessionWithContext generates a new admin session bound to coarse client fingerprints.
func (s *SessionStore) CreateAdminSessionWithContext(ip, userAgent string) (string, error) {
	id, err := generateSessionID()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	ipHash := hashIP(strings.TrimSpace(ip), s.salt)
	uaHash := hashIP(strings.TrimSpace(userAgent), s.salt)

	_, err = s.db.Exec(
		"INSERT INTO admin_sessions (id, created_at, last_seen_at, ip_hash, user_agent_hash) VALUES (?, ?, ?, ?, ?)",
		id, now, now, ipHash, uaHash,
	)
	if err != nil {
		return "", fmt.Errorf("create admin session: %w", err)
	}

	return id, nil
}

// ValidateAdminSession checks if an admin session is valid (1 hour, no sliding).
func (s *SessionStore) ValidateAdminSession(id string) (bool, error) {
	return s.ValidateAdminSessionWithContext(id, "", "")
}

// ValidateAdminSessionWithContext checks if an admin session is valid and optionally verifies client fingerprints.
func (s *SessionStore) ValidateAdminSessionWithContext(id, ip, userAgent string) (bool, error) {
	var createdAt time.Time
	var lastSeenAt sql.NullTime
	var storedIPHash sql.NullString
	var storedUAHash sql.NullString
	err := s.db.QueryRow(
		"SELECT created_at, last_seen_at, ip_hash, user_agent_hash FROM admin_sessions WHERE id = ?", id,
	).Scan(&createdAt, &lastSeenAt, &storedIPHash, &storedUAHash)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query admin session: %w", err)
	}

	if time.Since(createdAt) > AdminSessionExpiry {
		if _, err := s.db.Exec("DELETE FROM admin_sessions WHERE id = ?", id); err != nil {
			return false, fmt.Errorf("delete expired admin session: %w", err)
		}
		return false, nil
	}

	if strings.TrimSpace(ip) != "" && storedIPHash.Valid && storedIPHash.String != "" {
		reqIPHash := hashIP(strings.TrimSpace(ip), s.salt)
		if !secureHashEqual(storedIPHash.String, reqIPHash) {
			_, _ = s.db.Exec("DELETE FROM admin_sessions WHERE id = ?", id)
			return false, nil
		}
	}

	if strings.TrimSpace(userAgent) != "" && storedUAHash.Valid && storedUAHash.String != "" {
		reqUAHash := hashIP(strings.TrimSpace(userAgent), s.salt)
		if !secureHashEqual(storedUAHash.String, reqUAHash) {
			_, _ = s.db.Exec("DELETE FROM admin_sessions WHERE id = ?", id)
			return false, nil
		}
	}

	if !lastSeenAt.Valid || time.Since(lastSeenAt.Time.UTC()) >= AdminTouchWindow {
		if _, err := s.db.Exec("UPDATE admin_sessions SET last_seen_at = ? WHERE id = ?", time.Now().UTC(), id); err != nil {
			return false, fmt.Errorf("touch admin session: %w", err)
		}
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
	if _, err := s.db.Exec("DELETE FROM sessions WHERE last_seen_at < ?", cutoff); err != nil {
		log.Printf("session cleanup error: %v", err)
	}

	adminCutoff := time.Now().UTC().Add(-AdminSessionExpiry)
	if _, err := s.db.Exec("DELETE FROM admin_sessions WHERE created_at < ?", adminCutoff); err != nil {
		log.Printf("admin session cleanup error: %v", err)
	}
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

func secureHashEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
