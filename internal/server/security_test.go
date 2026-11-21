package server

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Solis/internal/config"
	"Solis/internal/db"
	"Solis/internal/httpguard"
	"Solis/internal/i18n"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct horse"

// webTestServer builds a Server wired like production (temp DB, templates,
// sessions, limiters) for exercising handlers. mutate can adjust the config.
func webTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	translator, err := i18n.New("en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	initTemplates(translator)

	database, err := db.Open(filepath.Join(t.TempDir(), "solis.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8889", PublicItems: true, PublicLocations: true},
		Admin:  config.AdminConfig{Username: "admin", PasswordHash: string(hash), SessionHours: 1},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
		Slugs:  config.SlugConfig{Length: 6},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxSize: 1},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return &Server{
		cfg:         cfg,
		db:          database,
		sessions:    newSessionStore(time.Hour),
		authLimiter: httpguard.NewRateLimiter(0),
		pwGate:      httpguard.NewPasswordGate(1),
		i18n:        translator,
	}
}

func sessionCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	token, err := s.sessions.Create()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: "session", Value: token}
}

func publicMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{slug}", s.handleShortRedirect)
	mux.HandleFunc("GET /{path...}", s.handlePublicFallback)
	return mux
}

func get(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// addItem creates an item in the default room and returns its short slug.
func addItem(t *testing.T, s *Server, name string) (int64, string) {
	t.Helper()
	item, err := s.db.CreateItem(&db.Item{
		Name:       name,
		Quantity:   1,
		Status:     "In Storage",
		LocationID: sql.NullInt64{Int64: 1, Valid: true},
	}, nil)
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	s.syncItemShortlink(item.ID, 1)
	link, err := s.db.GetLinkByItemID(item.ID)
	if err != nil {
		t.Fatalf("item link: %v", err)
	}
	return item.ID, link.Slug
}

// The path-lowercasing middleware must never answer with a Location a browser
// would resolve to another host.
func TestLowercasePathNoOpenRedirect(t *testing.T) {
	h := lowercasePath(http.NotFoundHandler())
	for _, target := range []string{"//EVIL.example/X", "/%2FEVIL.example/X", "/ABC"} {
		req := httptest.NewRequest(http.MethodGet, "http://solis.example/", nil)
		req.URL = &url.URL{Path: target}
		if p, err := url.PathUnescape(target); err == nil {
			req.URL.Path = p
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		loc := rec.Header().Get("Location")
		if strings.HasPrefix(loc, "//") || strings.Contains(loc, "://") {
			t.Errorf("%q: open redirect via Location %q", target, loc)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		// same-site paths are kept
		"/default-room":          "/default-room",
		"/default-room/ab12?x=1": "/default-room/ab12?x=1",
		"/s/abc123":              "/s/abc123",
		"/x?next=//evil.example": "/x?next=//evil.example",
		// anything a browser could resolve to another host is dropped
		"":                       "",
		"//evil.example":         "",
		"///evil.example":        "",
		"/\\evil.example":        "",
		"/ok/\\evil":             "",
		"/\t/evil.example":       "",
		"/ok\r\nSet-Cookie: a=b": "",
		"https://evil.example/":  "",
		"javascript:alert(1)":    "",
		"evil.example":           "",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// With both guest switches off, a visitor who is not logged in is sent to the
// login page — identically for pages that exist and pages that do not — and
// comes back to the page after logging in.
func TestGuestPagesOffRequireLogin(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) {
		c.Server.PublicItems = false
		c.Server.PublicLocations = false
	})
	_, slug := addItem(t, s, "Passport folder")
	mux := publicMux(s)

	for _, path := range []string{"/default-room", "/default-room/" + slug, "/no-such-room", "/default-room/nope", "/s/" + slug} {
		rec := get(mux, path)
		wantLoc := "/admin/login?next=" + url.QueryEscape(path)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != wantLoc {
			t.Errorf("guest GET %s: got %d %q, want 303 %q", path, rec.Code, rec.Header().Get("Location"), wantLoc)
		}
		if strings.Contains(rec.Body.String(), "Passport folder") {
			t.Errorf("guest GET %s leaked the item name", path)
		}
	}

	// Logged in, the same pages work.
	cookie := sessionCookie(t, s)
	if rec := get(mux, "/default-room/"+slug, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Passport folder") {
		t.Errorf("admin GET item page: got %d", rec.Code)
	}
	if rec := get(mux, "/s/"+slug, cookie); rec.Code != http.StatusMovedPermanently {
		t.Errorf("admin GET /s/slug: got %d, want 301", rec.Code)
	}
}

// Items public, locations private: someone who scans a lost item's label sees
// the item (and nothing that links into the location tree), while location
// pages still ask for a login.
func TestPublicItemsWithPrivateLocations(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.PublicLocations = false })
	_, slug := addItem(t, s, "Blue umbrella")
	mux := publicMux(s)

	if rec := get(mux, "/s/"+slug); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/default-room/"+slug {
		t.Errorf("guest GET /s/slug: got %d %q, want 301 to the item page", rec.Code, rec.Header().Get("Location"))
	}
	rec := get(mux, "/default-room/"+slug)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Blue umbrella") {
		t.Fatalf("guest GET item page: got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `href="/default-room"`) {
		t.Error("guest item page links to a location page that requires a login")
	}
	for _, path := range []string{"/default-room", "/no-such-room"} {
		if rec := get(mux, path); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login?next=") {
			t.Errorf("guest GET %s: got %d %q, want 303 to login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// With public_items on (the default) a guest can open an item page, but not
// one whose item was removed.
func TestRemovedItemHiddenFromGuests(t *testing.T) {
	s := webTestServer(t, nil)
	id, slug := addItem(t, s, "Camping stove")
	mux := publicMux(s)

	if rec := get(mux, "/default-room/"+slug); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Camping stove") {
		t.Fatalf("guest GET item page: got %d", rec.Code)
	}
	if err := s.db.SoftRemoveItem(id); err != nil {
		t.Fatal(err)
	}
	if rec := get(mux, "/default-room/"+slug); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Camping stove") {
		t.Errorf("guest GET removed item: got %d, want 404 without the item name", rec.Code)
	}
	if rec := get(mux, "/default-room/"+slug, sessionCookie(t, s)); rec.Code != http.StatusOK {
		t.Errorf("admin GET removed item: got %d, want 200", rec.Code)
	}
}

func loginForm(t *testing.T, s *Server, password, next string) *httptest.ResponseRecorder {
	t.Helper()
	const csrf = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	form := url.Values{"csrf_token": {csrf}, "username": {"admin"}, "password": {password}}
	if next != "" {
		form.Set("next", next)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	rec := httptest.NewRecorder()
	s.handleLogin(rec, req)
	return rec
}

func TestLoginReturnsToNextButNeverOffSite(t *testing.T) {
	s := webTestServer(t, nil)

	if rec := loginForm(t, s, testPassword, "/default-room/ab12"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/default-room/ab12" {
		t.Errorf("valid next: got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, evil := range []string{"//evil.example", "https://evil.example", "/\\evil.example"} {
		if rec := loginForm(t, s, testPassword, evil); rec.Header().Get("Location") != "/admin" {
			t.Errorf("next=%q: redirected to %q, want /admin", evil, rec.Header().Get("Location"))
		}
	}
	if rec := loginForm(t, s, "wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d, want 401", rec.Code)
	}
}

func TestLoginAttemptsAreThrottledPerIP(t *testing.T) {
	s := webTestServer(t, nil)
	s.authLimiter = httpguard.NewRateLimiter(time.Minute)
	if rec := loginForm(t, s, "wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first attempt: got %d, want 401", rec.Code)
	}
	if rec := loginForm(t, s, testPassword, ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second attempt inside the window: got %d, want 429", rec.Code)
	}
}

func mcpBasic(s *Server, ip, password string) int {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.RemoteAddr = ip + ":1234"
	req.SetBasicAuth("admin", password)
	rec := httptest.NewRecorder()
	s.handleMCP(rec, req)
	return rec.Code
}

// Wrong admin passwords on /mcp throttle the guessing IP; a client with the
// right password is never slowed down.
func TestMCPBasicAuthFailuresThrottled(t *testing.T) {
	s := webTestServer(t, nil)
	s.authLimiter = httpguard.NewRateLimiter(time.Minute)

	if got := mcpBasic(s, "203.0.113.5", "guess-1"); got != http.StatusUnauthorized {
		t.Fatalf("first wrong guess: got %d, want 401", got)
	}
	if got := mcpBasic(s, "203.0.113.5", testPassword); got != http.StatusTooManyRequests {
		t.Errorf("guess inside the window: got %d, want 429", got)
	}
	for i := range 3 {
		if got := mcpBasic(s, "198.51.100.7", testPassword); got != http.StatusOK {
			t.Errorf("correct password, request %d: got %d, want 200", i+1, got)
		}
	}
}

// The upload size cap must apply before the CSRF check parses the form.
func TestUploadCapAppliesBeforeFormParse(t *testing.T) {
	s := webTestServer(t, nil) // Upload.MaxSize = 1 MB
	reached := false
	h := s.limitUpload(s.requireCSRF(func(w http.ResponseWriter, r *http.Request) { reached = true }))

	const csrf = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	post := func(fileBytes int) int {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("csrf_token", csrf)
		fw, _ := mw.CreateFormFile("image", "photo.jpg")
		_, _ = fw.Write(bytes.Repeat([]byte{0xff}, fileBytes))
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/admin/items/new", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}

	if got := post(4 << 20); got != http.StatusRequestEntityTooLarge || reached {
		t.Errorf("4 MB upload with a 1 MB cap: got %d (handler reached: %v), want 413", got, reached)
	}
	if got := post(64 << 10); got != http.StatusOK || !reached {
		t.Errorf("small upload: got %d (handler reached: %v), want 200", got, reached)
	}
}

// Uploaded files are named by content type, never by the client's filename.
func TestUploadExtensionComesFromContent(t *testing.T) {
	s := webTestServer(t, nil)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "evil.html")
	_, _ = fw.Write([]byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64)))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/admin/items/new", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	name, ok := s.saveUploadedFile(req, "image", true)
	if !ok || !strings.HasSuffix(name, ".png") {
		t.Errorf("saved as %q (ok=%v), want a .png name", name, ok)
	}
}
