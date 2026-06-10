package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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
	if err != nil || cookie.Value == "" {
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

// rateLimiter tracks the last request time per IP.
type rateLimiter struct {
	mu      sync.Mutex
	clients map[string]time.Time
	window  time.Duration
}

func newRateLimiter(window time.Duration) *rateLimiter {
	return &rateLimiter{
		clients: make(map[string]time.Time),
		window:  window,
	}
}

func (rl *rateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if last, ok := rl.clients[ip]; ok && now.Sub(last) < rl.window {
		return false
	}
	rl.clients[ip] = now
	rl.purgeLocked(now)
	return true
}

// Blocked reports whether the IP is inside the rate-limit window without
// consuming a slot. Used to gate endpoints where only failures should count.
func (rl *rateLimiter) Blocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	last, ok := rl.clients[ip]
	return ok && time.Since(last) < rl.window
}

// Record marks a (failed) attempt for the IP, starting its rate-limit window.
func (rl *rateLimiter) Record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.clients[ip] = now
	rl.purgeLocked(now)
}

// purgeLocked lazily removes stale entries when the map grows large.
// Caller must hold rl.mu.
func (rl *rateLimiter) purgeLocked(now time.Time) {
	if len(rl.clients) > 1000 {
		for k, v := range rl.clients {
			if now.Sub(v) > rl.window*10 {
				delete(rl.clients, k)
			}
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if isPrivateIP(host) {
		// Use the RIGHTMOST X-Forwarded-For entry: it is the one appended by
		// our own (trusted) reverse proxy. Earlier entries are client-supplied
		// and trivially spoofable, which would defeat rate limiting.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	return host
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
