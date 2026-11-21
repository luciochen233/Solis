package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"Solis/internal/httpguard"
)

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
	ttl      time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	s := &sessionStore{
		sessions: make(map[string]time.Time),
		ttl:      ttl,
	}
	go s.cleanup()
	return s
}

func (s *sessionStore) cleanup() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for token, expiry := range s.sessions {
			if now.After(expiry) {
				delete(s.sessions, token)
			}
		}
		s.mu.Unlock()
	}
}

func (s *sessionStore) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return token, nil
}

func (s *sessionStore) Valid(token string) bool {
	s.mu.RLock()
	expiry, ok := s.sessions[token]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		s.mu.Lock()
		delete(s.sessions, token)
		s.mu.Unlock()
		return false
	}
	return true
}

func (s *sessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (srv *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || !srv.sessions.Valid(cookie.Value) {
			// Check if it's an HTMX request. If so, trigger a client-side redirect
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/admin/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf"); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Never issue a predictable token; fail closed with an empty token
		// that can never match a submitted value.
		return ""
	}
	token := hex.EncodeToString(b)
	// HttpOnly is safe here: the frontend reads the token from the page's
	// <meta name="csrf-token"> tag, never from the cookie itself.
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isHTTPS(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
	return token
}

func verifyCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("csrf")
	// Require a full-length token: an empty or truncated cookie must never
	// "match" an empty/missing form field.
	if err != nil || len(cookie.Value) != 64 {
		return false
	}

	// 1. Verify via form value
	formToken := r.FormValue("csrf_token")
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formToken)) == 1 {
		return true
	}

	// 2. Verify via standard headers (useful for HTMX / AJAX calls)
	headerToken := r.Header.Get("X-CSRF-Token")
	if headerToken == "" {
		headerToken = r.Header.Get("HX-CSRF-Token")
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(headerToken)) == 1
}

func (srv *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyCSRF(r) {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// clientIP returns the client IP for rate limiting and logs, using the
// server's proxy settings (see httpguard.ClientIP).
func (srv *Server) clientIP(r *http.Request) string {
	hops := 0
	if srv.cfg.Server.TrustProxy {
		hops = srv.cfg.Server.ProxyHops
		if hops < 1 {
			hops = 1
		}
	}
	return httpguard.ClientIP(r, hops)
}

// isAuthenticated reports whether the request carries a valid admin session.
func (srv *Server) isAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("session")
	return err == nil && srv.sessions.Valid(cookie.Value)
}

// limitUpload prepares a multipart upload request: it caps the body at the
// configured upload size, extends the short global timeouts, and parses the
// form with a small in-memory budget so large files spill to temp files.
//
// It must wrap requireCSRF, not the other way round: requireCSRF reads a form
// value, which would otherwise parse the whole body first with no size cap at
// all and with Go's default 32 MB in-memory budget.
func (srv *Server) limitUpload(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		maxBytes := srv.cfg.Upload.MaxSize*1024*1024 + 1<<20 // files + form fields
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(120 * time.Second))
		_ = rc.SetWriteDeadline(time.Now().Add(120 * time.Second))

		if err := r.ParseMultipartForm(4 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, fmt.Sprintf("Upload exceeds the %d MB limit", srv.cfg.Upload.MaxSize), http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "Invalid upload", http.StatusBadRequest)
			}
			return
		}
		next(w, r)
	}
}

// isUploadRoute matches the item forms, which carry file uploads and get
// their (larger) body cap from limitUpload instead of the global one.
func isUploadRoute(r *http.Request) bool {
	return r.URL.Path == "/admin/items/new" || strings.HasPrefix(r.URL.Path, "/admin/items/edit/")
}
