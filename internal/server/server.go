package server

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"Solis/internal/config"
	"Solis/internal/db"
)

type Server struct {
	cfg      *config.Config
	db       *db.DB
	sessions *sessionStore
	limiter  *rateLimiter
	catalog  []config.CatalogCategory
}

func New(cfg *config.Config, database *db.DB) *Server {
	ttl := time.Duration(cfg.Admin.SessionHours) * time.Hour
	cat, err := config.LoadCatalog("catalog.json")
	if err != nil {
		log.Printf("Warning: failed to load catalog.json: %v. Using empty catalog.", err)
		cat = []config.CatalogCategory{}
	}
	return &Server{
		cfg:      cfg,
		db:       database,
		sessions: newSessionStore(ttl),
		limiter:  newRateLimiter(2 * time.Second), // 2 seconds between login attempts
		catalog:  cat,
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		
		// Offline-first CSP: Only allows local static resources and fonts from Google Fonts
		w.Header().Set("Content-Security-Policy", 
			"default-src 'self'; "+
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
			"font-src 'self' https://fonts.gstatic.com; "+
			"img-src 'self' data:; "+
			"connect-src 'self';")
			
		next.ServeHTTP(w, r)
	})
}

// lowercasePath normalises URL paths to lowercase so that short links are case-insensitive.
// Admin, uploads, and static routes are excluded to preserve asset names.
func lowercasePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lower := strings.ToLower(r.URL.Path)
		if lower != r.URL.Path && 
			!strings.HasPrefix(r.URL.Path, "/static/") && 
			!strings.HasPrefix(lower, "/admin") && 
			!strings.HasPrefix(r.URL.Path, "/uploads/") {
			r.URL.Path = lower
			http.Redirect(w, r, r.URL.String(), http.StatusMovedPermanently)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Start() error {
	initTemplates()

	// Ensure upload directory exists
	if err := os.MkdirAll(s.cfg.Upload.Dir, 0755); err != nil {
		return fmt.Errorf("creating upload directory: %w", err)
	}

	mux := http.NewServeMux()

	// 1. Embedded static files
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// 2. Safe Uploads Serving
	mux.HandleFunc("GET /uploads/", s.handleServeUploads)

	// 3. Root redirect to Admin panel
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	// 4. Short Slug Redirect Handler
	mux.HandleFunc("GET /s/{slug}", s.handleShortRedirect)

	// 5. Authentication handlers
	mux.HandleFunc("GET /admin/login", s.handleLoginPage)
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.HandleFunc("POST /admin/logout", s.requireAuth(s.requireCSRF(s.handleLogout)))

	// 6. Admin items management (HTMX & standard CRUD)
	mux.HandleFunc("GET /admin", s.requireAuth(s.handleAdminItems))
	mux.HandleFunc("GET /admin/items/new", s.requireAuth(s.handleItemNew))
	mux.HandleFunc("POST /admin/items/new", s.requireAuth(s.requireCSRF(s.handleItemCreate)))
	mux.HandleFunc("GET /admin/items/edit/{id}", s.requireAuth(s.handleItemEdit))
	mux.HandleFunc("POST /admin/items/edit/{id}", s.requireAuth(s.requireCSRF(s.handleItemSave)))
	mux.HandleFunc("POST /admin/items/delete/{id}", s.requireAuth(s.requireCSRF(s.handleItemDelete)))

	// 7. Admin locations management
	mux.HandleFunc("GET /admin/locations", s.requireAuth(s.handleAdminLocations))
	mux.HandleFunc("POST /admin/locations/new", s.requireAuth(s.requireCSRF(s.handleLocationCreate)))
	mux.HandleFunc("POST /admin/locations/edit/{id}", s.requireAuth(s.requireCSRF(s.handleLocationEdit)))
	mux.HandleFunc("POST /admin/locations/delete/{id}", s.requireAuth(s.requireCSRF(s.handleLocationDelete)))

	// 8. Admin tags management
	mux.HandleFunc("GET /admin/tags", s.requireAuth(s.handleAdminTags))
	mux.HandleFunc("POST /admin/tags/new", s.requireAuth(s.requireCSRF(s.handleTagCreate)))
	mux.HandleFunc("POST /admin/tags/edit/{id}", s.requireAuth(s.requireCSRF(s.handleTagEdit)))
	mux.HandleFunc("POST /admin/tags/delete/{id}", s.requireAuth(s.requireCSRF(s.handleTagDelete)))

	// 9. Admin custom shortlinks manager
	mux.HandleFunc("GET /admin/shortener", s.requireAuth(s.handleAdminShortener))
	mux.HandleFunc("POST /admin/shortener/new", s.requireAuth(s.requireCSRF(s.handleLinkCreate)))
	mux.HandleFunc("POST /admin/shortener/edit/{id}", s.requireAuth(s.requireCSRF(s.handleLinkSave)))
	mux.HandleFunc("POST /admin/shortener/delete/{id}", s.requireAuth(s.requireCSRF(s.handleLinkDelete)))

	// 10. QR & Labels
	mux.HandleFunc("GET /admin/qr/{slug}", s.requireAuth(s.handleQR))
	mux.HandleFunc("GET /admin/print", s.requireAuth(s.handlePrintLabels))

	// Listen and serve
	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      lowercasePath(securityHeaders(mux)),
		ReadTimeout:  s.cfg.Server.ReadTimeoutDuration(),
		WriteTimeout: s.cfg.Server.WriteTimeoutDuration(),
	}

	log.Printf("Solis running on http://localhost:%d (Base URL: %s)", s.cfg.Server.Port, s.cfg.Server.BaseURL)
	return srv.ListenAndServe()
}

// handleServeUploads serving local photos/receipts safely
func (s *Server) handleServeUploads(w http.ResponseWriter, r *http.Request) {
	// Extract clean filename to prevent directory traversal
	filename := filepath.Base(r.URL.Path)
	filePath := filepath.Join(s.cfg.Upload.Dir, filename)

	// Validate path is strictly inside s.cfg.Upload.Dir
	if !strings.HasPrefix(filepath.Clean(filePath), filepath.Clean(s.cfg.Upload.Dir)) {
		http.Error(w, "Access Denied", http.StatusForbidden)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	// Sniff MIME type to prevent same-origin executable execution (e.g. SVG/HTML uploads running JS)
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	mimeType := http.DetectContentType(buffer[:n])
	file.Seek(0, io.SeekStart)

	// Direct rendering is allowed for safe image MIME types only
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Cache-Control", "public, max-age=31536000") // Cache heavily
	default:
		// Force download/attachment for all other mime types (e.g. PDF/TXT receipts or arbitrary files)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	}

	io.Copy(w, file)
}
