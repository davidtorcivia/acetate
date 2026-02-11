package server

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// requireSession checks for a valid listener session cookie.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("acetate_session")
		if err != nil || cookie.Value == "" {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		valid, err := s.sessions.ValidateSession(cookie.Value)
		if err != nil {
			log.Printf("session validate error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !valid {
			// Clear invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:     "acetate_session",
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteStrictMode,
			})
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireAdmin checks for a valid admin session cookie.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("acetate_admin")
		if err != nil || cookie.Value == "" {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		valid, err := s.sessions.ValidateAdminSession(cookie.Value)
		if err != nil {
			log.Printf("admin session validate error: %v", err)
			jsonError(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !valid {
			http.SetCookie(w, &http.Cookie{
				Name:     "acetate_admin",
				Value:    "",
				Path:     "/admin",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteStrictMode,
			})
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// csrfCheck validates the Origin header on state-mutating requests.
func csrfCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				// Allow same-origin requests
				host := r.Host
				if !strings.HasSuffix(origin, "://"+host) {
					jsonError(w, "forbidden", http.StatusForbidden)
					return
				}
			}
			// If no Origin header (e.g., sendBeacon), allow — the session cookie check is sufficient
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs HTTP requests.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, wrapped.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// cacheControl sets the Cache-Control header.
func cacheControl(value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", value)
			next.ServeHTTP(w, r)
		})
	}
}
