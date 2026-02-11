package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	errAdminUserExists           = errors.New("admin user already exists")
	errAdminUserNotFound         = errors.New("admin user not found")
	errAdminInvalidUserUpdate    = errors.New("invalid admin user update")
	errAdminCannotDeactivateSelf = errors.New("cannot deactivate your own account")
	errAdminLastActiveAdmin      = errors.New("at least one active admin is required")
)

type adminUserView struct {
	ID                   int64  `json:"id"`
	Username             string `json:"username"`
	IsActive             bool   `json:"is_active"`
	RequirePasswordReset bool   `json:"require_password_reset"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
	LastLoginAt          string `json:"last_login_at,omitempty"`
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.listAdminUsers()
	if err != nil {
		log.Printf("list admin users error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]interface{}{"users": users})
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username             string `json:"username"`
		Password             string `json:"password"`
		RequirePasswordReset *bool  `json:"require_password_reset,omitempty"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	requireReset := true
	if req.RequirePasswordReset != nil {
		requireReset = *req.RequirePasswordReset
	}

	user, err := s.createAdminUser(req.Username, req.Password, requireReset)
	if err != nil {
		switch {
		case errors.Is(err, errAdminWeakPassword):
			jsonError(w, "password does not meet policy", http.StatusBadRequest)
		case errors.Is(err, errAdminUserExists):
			jsonError(w, "username already exists", http.StatusConflict)
		default:
			if strings.Contains(strings.ToLower(err.Error()), "username") {
				jsonError(w, "invalid username", http.StatusBadRequest)
			} else {
				log.Printf("create admin user error: %v", err)
				jsonError(w, "internal error", http.StatusInternalServerError)
			}
		}
		return
	}

	jsonCreated(w, user)
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	targetID, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "id")), 10, 64)
	if err != nil || targetID <= 0 {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	var req struct {
		IsActive             *bool `json:"is_active,omitempty"`
		RequirePasswordReset *bool `json:"require_password_reset,omitempty"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	actorID, ok := adminUserIDFromContext(r)
	if !ok {
		jsonError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	user, err := s.updateAdminUser(actorID, targetID, req.IsActive, req.RequirePasswordReset)
	if err != nil {
		switch {
		case errors.Is(err, errAdminUserNotFound):
			jsonError(w, "not found", http.StatusNotFound)
		case errors.Is(err, errAdminInvalidUserUpdate),
			errors.Is(err, errAdminCannotDeactivateSelf),
			errors.Is(err, errAdminLastActiveAdmin):
			jsonError(w, err.Error(), http.StatusBadRequest)
		default:
			log.Printf("update admin user error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	jsonOK(w, user)
}

func (s *Server) listAdminUsers() ([]adminUserView, error) {
	rows, err := s.db.Query(
		"SELECT id, username, is_active, require_password_reset, created_at, updated_at, COALESCE(last_login_at, '') FROM admin_users ORDER BY username ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("query admin users: %w", err)
	}
	defer rows.Close()

	users := make([]adminUserView, 0)
	for rows.Next() {
		var (
			user       adminUserView
			isActive   int
			needsReset int
		)
		if err := rows.Scan(
			&user.ID,
			&user.Username,
			&isActive,
			&needsReset,
			&user.CreatedAt,
			&user.UpdatedAt,
			&user.LastLoginAt,
		); err != nil {
			return nil, fmt.Errorf("scan admin user: %w", err)
		}
		user.IsActive = isActive == 1
		user.RequirePasswordReset = needsReset == 1
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin users: %w", err)
	}
	return users, nil
}

func (s *Server) createAdminUser(username, password string, requirePasswordReset bool) (adminUserView, error) {
	user := adminUserView{}

	normalizedUsername, err := normalizeAdminUsername(username)
	if err != nil {
		return user, err
	}
	if err := validateAdminPassword(password); err != nil {
		return user, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
	if err != nil {
		return user, fmt.Errorf("hash admin password: %w", err)
	}

	now := time.Now().UTC()
	res, err := s.db.Exec(
		"INSERT INTO admin_users (username, password_hash, is_active, require_password_reset, created_at, updated_at) VALUES (?, ?, 1, ?, ?, ?)",
		normalizedUsername,
		string(hash),
		boolToInt(requirePasswordReset),
		now,
		now,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return user, errAdminUserExists
		}
		return user, fmt.Errorf("create admin user: %w", err)
	}

	userID, err := res.LastInsertId()
	if err != nil {
		return user, fmt.Errorf("admin user id: %w", err)
	}
	return s.getAdminUserViewByID(userID)
}

func (s *Server) updateAdminUser(actorID, targetID int64, isActive, requirePasswordReset *bool) (adminUserView, error) {
	user := adminUserView{}
	if actorID <= 0 || targetID <= 0 {
		return user, errAdminInvalidUserUpdate
	}
	if isActive == nil && requirePasswordReset == nil {
		return user, errAdminInvalidUserUpdate
	}

	tx, err := s.db.Begin()
	if err != nil {
		return user, fmt.Errorf("begin admin user update: %w", err)
	}
	defer tx.Rollback()

	var (
		currentActive int
		currentReset  int
	)
	if err := tx.QueryRow(
		"SELECT is_active, require_password_reset FROM admin_users WHERE id = ?",
		targetID,
	).Scan(&currentActive, &currentReset); err != nil {
		if err == sql.ErrNoRows {
			return user, errAdminUserNotFound
		}
		return user, fmt.Errorf("query admin user for update: %w", err)
	}

	nextActive := currentActive == 1
	if isActive != nil {
		nextActive = *isActive
	}
	nextReset := currentReset == 1
	if requirePasswordReset != nil {
		nextReset = *requirePasswordReset
	}

	if !nextActive {
		if actorID == targetID {
			return user, errAdminCannotDeactivateSelf
		}
		var otherActive int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM admin_users WHERE is_active = 1 AND id != ?",
			targetID,
		).Scan(&otherActive); err != nil {
			return user, fmt.Errorf("count remaining active admins: %w", err)
		}
		if otherActive == 0 {
			return user, errAdminLastActiveAdmin
		}
	}

	_, err = tx.Exec(
		"UPDATE admin_users SET is_active = ?, require_password_reset = ?, updated_at = ? WHERE id = ?",
		boolToInt(nextActive),
		boolToInt(nextReset),
		time.Now().UTC(),
		targetID,
	)
	if err != nil {
		return user, fmt.Errorf("update admin user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return user, fmt.Errorf("commit admin user update: %w", err)
	}

	if !nextActive {
		_ = s.sessions.DeleteAdminSessionsForUser(targetID)
	}

	return s.getAdminUserViewByID(targetID)
}

func (s *Server) getAdminUserViewByID(userID int64) (adminUserView, error) {
	user := adminUserView{}
	var (
		isActive   int
		needsReset int
	)
	err := s.db.QueryRow(
		"SELECT id, username, is_active, require_password_reset, created_at, updated_at, COALESCE(last_login_at, '') FROM admin_users WHERE id = ?",
		userID,
	).Scan(
		&user.ID,
		&user.Username,
		&isActive,
		&needsReset,
		&user.CreatedAt,
		&user.UpdatedAt,
		&user.LastLoginAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return user, errAdminUserNotFound
		}
		return user, fmt.Errorf("query admin user by id: %w", err)
	}

	user.IsActive = isActive == 1
	user.RequirePasswordReset = needsReset == 1
	return user, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
