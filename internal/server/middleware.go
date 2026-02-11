package server

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type contextKey string

const adminUserIDKey contextKey = "admin_user_id"

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
				Secure:   isSecureRequest(r),
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

		clientIP := s.cfIPs.GetClientIP(r)
		userAgent := strings.TrimSpace(r.UserAgent())
		valid, userID, needsPasswordReset, err := s.sessions.ValidateAdminSessionWithContext(cookie.Value, clientIP, userAgent)
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
				Secure:   isSecureRequest(r),
				SameSite: http.SameSiteStrictMode,
			})
			jsonError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if needsPasswordReset && !allowDuringForcedPasswordReset(r.Method, r.URL.Path) {
			jsonError(w, "password reset required", http.StatusForbidden)
			return
		}

		ctx := context.WithValue(r.Context(), adminUserIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func allowDuringForcedPasswordReset(method, path string) bool {
	return (method == http.MethodPut && path == "/admin/api/admin-password") ||
		(method == http.MethodDelete && path == "/admin/api/auth") ||
		(method == http.MethodGet && path == "/admin/api/config")
}

// csrfCheck validates the Origin header on state-mutating requests.
func csrfCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutatingMethod(r.Method) && strings.HasPrefix(r.URL.Path, "/admin/api/") {
			origin := strings.TrimSpace(r.Header.Get("Origin"))
			if origin == "" || !sameOrigin(r, origin) {
				jsonError(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutatingMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete || method == http.MethodPatch
}

func sameOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}

	expectedScheme := requestScheme(r)
	expectedHost := strings.ToLower(r.Host)

	return strings.EqualFold(u.Scheme, expectedScheme) && strings.EqualFold(u.Host, expectedHost)
}

func requestScheme(r *http.Request) string {
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if i := strings.Index(proto, ","); i > 0 {
			proto = proto[:i]
		}
		proto = strings.TrimSpace(strings.ToLower(proto))
		if proto == "http" || proto == "https" {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// securityHeaders sets secure defaults for every response.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self'; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), microphone=(), payment=(), usb=()")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs HTTP requests.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		if strings.HasPrefix(r.URL.Path, "/api/stream/") && wrapped.status < http.StatusBadRequest {
			return
		}
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

func adminUserIDFromContext(r *http.Request) (int64, bool) {
	v := r.Context().Value(adminUserIDKey)
	id, ok := v.(int64)
	if !ok || id <= 0 {
		return 0, false
	}
	return id, true
}
