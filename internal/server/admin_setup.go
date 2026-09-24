package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"acetate/internal/auth"
)

func (s *Server) handleAdminSetupStatus(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := s.needsAdminSetup()
	if err != nil {
		log.Printf("admin setup status error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]interface{}{
		"needs_setup": needsSetup,
	})
}

func (s *Server) handleAdminSetupBootstrap(w http.ResponseWriter, r *http.Request) {
	needsSetup, err := s.needsAdminSetup()
	if err != nil {
		log.Printf("admin setup precheck error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !needsSetup {
		jsonError(w, "already configured", http.StatusConflict)
		return
	}

	clientIP := s.clientIPs.ClientIP(r)
	if !s.rateLimiter.Allow("setup:"+auth.RateKey(clientIP), adminSetupLimit) {
		jsonError(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		jsonError(w, "bad request", http.StatusBadRequest)
		return
	}

	user, err := s.createInitialAdminUser(req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, errAdminAlreadyConfigured):
			jsonError(w, "already configured", http.StatusConflict)
		case errors.Is(err, errAdminWeakPassword):
			jsonError(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errAdminInvalidUsername):
			jsonError(w, err.Error(), http.StatusBadRequest)
		default:
			log.Printf("admin setup create user error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	sessionID, err := s.sessions.CreateAdminSession(user.ID, auth.RateKey(clientIP), strings.TrimSpace(r.UserAgent()))
	if err != nil {
		log.Printf("admin setup create session error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	setAdminCookie(w, r, sessionID, int(auth.AdminSessionExpiry.Seconds()))

	s.recordAdminAuthAttempt(r, user.Username, "success", "bootstrap_setup")
	jsonCreated(w, map[string]interface{}{
		"status":                  "ok",
		"username":                user.Username,
		"password_reset_required": false,
	})
}
