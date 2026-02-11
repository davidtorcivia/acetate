package server

import (
	"errors"
	"log"
	"net/http"
	"strings"
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

	clientIP := s.cfIPs.GetClientIP(r)
	if !s.rateLimiter.Allow("admin:setup:" + clientIP) {
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
			jsonError(w, "password does not meet policy", http.StatusBadRequest)
		default:
			// normalizeAdminUsername returns plain errors for format issues.
			if strings.Contains(strings.ToLower(err.Error()), "username") {
				jsonError(w, "invalid username", http.StatusBadRequest)
			} else {
				log.Printf("admin setup create user error: %v", err)
				jsonError(w, "internal error", http.StatusInternalServerError)
			}
		}
		return
	}

	sessionID, err := s.sessions.CreateAdminSessionWithContext(user.ID, clientIP, strings.TrimSpace(r.UserAgent()))
	if err != nil {
		log.Printf("admin setup create session error: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "acetate_admin",
		Value:    sessionID,
		Path:     "/admin",
		MaxAge:   3600, // 1 hour
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	s.recordAdminAuthAttempt(r, user.Username, "success", "bootstrap_setup")
	jsonCreated(w, map[string]interface{}{
		"status":                  "ok",
		"username":                user.Username,
		"password_reset_required": false,
	})
}
