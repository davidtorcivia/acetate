package server

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	minAdminUsernameLen = 3
	maxAdminUsernameLen = 64
	minAdminPasswordLen = 12
	maxAdminPasswordLen = 256
)

var (
	adminUsernameRe           = regexp.MustCompile(`^[a-z0-9._-]+$`)
	dummyAdminPasswordHash    = "$2a$10$7EqJtq98hPqEX7fNZaFWo.O9JmMbiY4rQ6hJ7PV5K56ZtPcNI0fS"
	errAdminInvalidCreds      = errors.New("invalid admin credentials")
	errAdminWeakPassword      = errors.New("weak admin password")
	errAdminBootstrapMissing  = errors.New("admin bootstrap credentials not configured")
	errAdminAlreadyConfigured = errors.New("admin already configured")
)

// ErrAdminBootstrapMissing indicates startup bootstrap credentials were not provided
// while the admin_users table is empty.
var ErrAdminBootstrapMissing = errAdminBootstrapMissing

type adminUserRecord struct {
	ID           int64
	Username     string
	PasswordHash string
}

// EnsureAdminBootstrap creates the first admin account when the database has no admin users.
func EnsureAdminBootstrap(db *sql.DB, username, password, passwordHash string) error {
	count, err := adminUserCount(db)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	normalizedUsername, err := normalizeAdminUsername(username)
	if err != nil {
		return err
	}

	hash, err := resolveAdminPasswordHash(password, passwordHash)
	if err != nil {
		if errors.Is(err, errAdminWeakPassword) {
			return fmt.Errorf("invalid ADMIN_PASSWORD: %w", err)
		}
		if errors.Is(err, errAdminBootstrapMissing) {
			return errAdminBootstrapMissing
		}
		return err
	}

	now := time.Now().UTC()
	_, err = db.Exec(
		"INSERT INTO admin_users (username, password_hash, is_active, created_at, updated_at) VALUES (?, ?, 1, ?, ?)",
		normalizedUsername, hash, now, now,
	)
	if err != nil {
		return fmt.Errorf("create bootstrap admin user: %w", err)
	}

	return nil
}

func resolveAdminPasswordHash(password, passwordHash string) (string, error) {
	hash := strings.TrimSpace(passwordHash)
	if hash != "" {
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return "", fmt.Errorf("invalid ADMIN_PASSWORD_HASH")
		}
		return hash, nil
	}

	pw := strings.TrimSpace(password)
	if pw == "" {
		return "", errAdminBootstrapMissing
	}
	if err := validateAdminPassword(pw); err != nil {
		return "", err
	}
	out, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func adminUserCount(db *sql.DB) (int, error) {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		return 0, fmt.Errorf("count admin users: %w", err)
	}
	return count, nil
}

func normalizeAdminUsername(username string) (string, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	if u == "" {
		u = "admin"
	}
	if len(u) < minAdminUsernameLen || len(u) > maxAdminUsernameLen {
		return "", errors.New("invalid ADMIN_USERNAME length")
	}
	if !adminUsernameRe.MatchString(u) {
		return "", errors.New("invalid ADMIN_USERNAME format")
	}
	return u, nil
}

func validateAdminPassword(password string) error {
	pw := strings.TrimSpace(password)
	if len(pw) < minAdminPasswordLen || len(pw) > maxAdminPasswordLen {
		return errAdminWeakPassword
	}

	hasLetter := false
	hasDigit := false
	for _, r := range pw {
		if unicode.IsLetter(r) {
			hasLetter = true
		}
		if unicode.IsDigit(r) {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errAdminWeakPassword
	}
	return nil
}

func (s *Server) authenticateAdminCredentials(username, password string) (adminUserRecord, error) {
	user := adminUserRecord{}

	normalizedUsername, err := normalizeAdminUsername(username)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyAdminPasswordHash), []byte(password))
		return user, errAdminInvalidCreds
	}

	err = s.db.QueryRow(
		"SELECT id, username, password_hash FROM admin_users WHERE username = ? AND is_active = 1",
		normalizedUsername,
	).Scan(&user.ID, &user.Username, &user.PasswordHash)
	if err == sql.ErrNoRows {
		_ = bcrypt.CompareHashAndPassword([]byte(dummyAdminPasswordHash), []byte(password))
		return user, errAdminInvalidCreds
	}
	if err != nil {
		return user, fmt.Errorf("query admin user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return adminUserRecord{}, errAdminInvalidCreds
	}

	_, _ = s.db.Exec(
		"UPDATE admin_users SET last_login_at = ?, updated_at = ? WHERE id = ?",
		time.Now().UTC(), time.Now().UTC(), user.ID,
	)

	return user, nil
}

func (s *Server) updateAdminPassword(userID int64, currentPassword, newPassword string) error {
	if userID <= 0 {
		return errAdminInvalidCreds
	}
	if err := validateAdminPassword(newPassword); err != nil {
		return err
	}

	var currentHash string
	err := s.db.QueryRow("SELECT password_hash FROM admin_users WHERE id = ? AND is_active = 1", userID).Scan(&currentHash)
	if err == sql.ErrNoRows {
		return errAdminInvalidCreds
	}
	if err != nil {
		return fmt.Errorf("query current admin password: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(currentPassword)) != nil {
		return errAdminInvalidCreds
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(newPassword)), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	_, err = s.db.Exec(
		"UPDATE admin_users SET password_hash = ?, updated_at = ? WHERE id = ?",
		string(newHash), time.Now().UTC(), userID,
	)
	if err != nil {
		return fmt.Errorf("update admin password: %w", err)
	}

	if err := s.sessions.DeleteAdminSessionsForUser(userID); err != nil {
		return fmt.Errorf("revoke admin sessions: %w", err)
	}
	return nil
}

func (s *Server) getAdminUsernameByID(userID int64) (string, error) {
	if userID <= 0 {
		return "", errors.New("invalid admin user id")
	}

	var username string
	err := s.db.QueryRow("SELECT username FROM admin_users WHERE id = ? AND is_active = 1", userID).Scan(&username)
	if err != nil {
		return "", err
	}
	return username, nil
}

func (s *Server) needsAdminSetup() (bool, error) {
	count, err := adminUserCount(s.db)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Server) createInitialAdminUser(username, password string) (adminUserRecord, error) {
	user := adminUserRecord{}

	needsSetup, err := s.needsAdminSetup()
	if err != nil {
		return user, err
	}
	if !needsSetup {
		return user, errAdminAlreadyConfigured
	}

	normalizedUsername, err := normalizeAdminUsername(username)
	if err != nil {
		return user, err
	}
	if err := validateAdminPassword(password); err != nil {
		return user, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
	if err != nil {
		return user, fmt.Errorf("hash bootstrap admin password: %w", err)
	}

	now := time.Now().UTC()
	res, err := s.db.Exec(
		"INSERT INTO admin_users (username, password_hash, is_active, created_at, updated_at, last_login_at) VALUES (?, ?, 1, ?, ?, ?)",
		normalizedUsername, string(hash), now, now, now,
	)
	if err != nil {
		return user, fmt.Errorf("create bootstrap admin user: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return user, fmt.Errorf("bootstrap admin user id: %w", err)
	}

	user = adminUserRecord{
		ID:       id,
		Username: normalizedUsername,
	}
	return user, nil
}
