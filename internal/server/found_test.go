package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"Solis/internal/config"
	"Solis/internal/db"
	"Solis/internal/httpguard"
	"Solis/internal/notify"
	"Solis/internal/notify/smtptest"
)

const testCSRF = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func foundMux(s *Server) *http.ServeMux {
	mux := publicMux(s)
	mux.HandleFunc("POST /found/{slug}", s.requireCSRF(s.handleFoundReport))
	return mux
}

func postFound(h http.Handler, slug, ip string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	form.Set("csrf_token", testCSRF)
	req := httptest.NewRequest(http.MethodPost, "/found/"+slug, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf", Value: testCSRF})
	req.RemoteAddr = ip + ":4321"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A guest sees the item's name, the owner's note and contact, and the report
// form — and nothing else from the record.
func TestGuestSeesFoundCardOnly(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Found.Contact = "If found, call the front desk" })
	item, err := s.db.CreateItem(&db.Item{
		Name:         "Trail camera",
		Description:  "Bought for 480 at the outlet",
		SerialNumber: "SN-SECRET-1",
		Supplier:     "Outlet Co",
		Quantity:     1,
		Status:       "In Storage",
		LocationID:   sql.NullInt64{Int64: 1, Valid: true},
		PublicNote:   "Reward if returned",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.syncItemShortlink(item.ID, 1)
	link, _ := s.db.GetLinkByItemID(item.ID)

	body := get(publicMux(s), "/default-room/"+link.Slug).Body.String()
	for _, want := range []string{"Trail camera", "Reward if returned", "If found, call the front desk", `action="/found/` + link.Slug + `"`, strings.ToUpper(link.Slug)} {
		if !strings.Contains(body, want) {
			t.Errorf("guest card lacks %q", want)
		}
	}
	for _, secret := range []string{"Bought for 480", "SN-SECRET-1", "Outlet Co", "In Storage", "/admin/items/edit"} {
		if strings.Contains(body, secret) {
			t.Errorf("guest card leaks %q", secret)
		}
	}

	// The owner still gets the full detail page.
	if full := get(publicMux(s), "/default-room/"+link.Slug, sessionCookie(t, s)).Body.String(); !strings.Contains(full, "SN-SECRET-1") {
		t.Error("logged-in view lost the item details")
	}
}

func TestFoundReportIsStoredWithCleanedInput(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.TrustProxy = true })
	id, slug := addItem(t, s, "Blue umbrella")
	mux := foundMux(s)

	rec := postFound(mux, slug, "10.0.0.9", url.Values{
		"phone":        {"+1 (555) 123-4567<script>alert(1)</script>"},
		"email":        {"  finder@example.test "},
		"message":      {"Left it at the front desk.\r\nAsk for Sam.\x00\x1b[31m\n\n\n\nThanks"},
		"gps_lat":      {"37.774900"},
		"gps_lon":      {"-122.419400"},
		"gps_accuracy": {"12.5"},
	}, map[string]string{
		"X-Forwarded-For": "6.6.6.6, 203.0.113.9",
		"CF-IPCountry":    "US",
		"CF-IPCity":       "San Francisco",
		"CF-Region":       "California",
		"CF-IPLatitude":   "37.7",
		"CF-IPLongitude":  "-122.4",
		"User-Agent":      "TestBrowser/1.0",
	})
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/"+slug+"?reported=1") {
		t.Fatalf("report: got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	reports, err := s.db.ListFoundReports(10)
	if err != nil || len(reports) != 1 {
		t.Fatalf("stored reports: %d (err %v), want 1", len(reports), err)
	}
	r := reports[0]
	if r.ItemID.Int64 != id || r.ItemName != "Blue umbrella" || r.Slug != slug {
		t.Errorf("item fields: %+v", r)
	}
	if r.IP != "203.0.113.9" {
		t.Errorf("IP = %q, want the proxy-reported client, not the forged prefix", r.IP)
	}
	if r.Phone != "+1 (555) 123-4567(1)" {
		t.Errorf("phone = %q, want only phone characters kept", r.Phone)
	}
	if r.Email != "finder@example.test" {
		t.Errorf("email = %q", r.Email)
	}
	if r.Message != "Left it at the front desk.\nAsk for Sam.[31m\n\nThanks" {
		t.Errorf("message = %q, want line breaks kept and control characters dropped", r.Message)
	}
	if !r.GPSLat.Valid || r.GPSLat.Float64 != 37.7749 || r.GPSLon.Float64 != -122.4194 || r.GPSAccuracy.Float64 != 12.5 {
		t.Errorf("gps: %+v %+v %+v", r.GPSLat, r.GPSLon, r.GPSAccuracy)
	}
	if IPPlace(&r) != "San Francisco, California, US" || !r.IPLat.Valid {
		t.Errorf("ip geo: %q %+v", IPPlace(&r), r.IPLat)
	}
	if r.UserAgent != "TestBrowser/1.0" || r.Emailed {
		t.Errorf("ua %q emailed %v", r.UserAgent, r.Emailed)
	}

	// The thank-you state replaces the form.
	if body := get(mux, "/default-room/"+slug+"?reported=1").Body.String(); !strings.Contains(body, "found-thanks") || strings.Contains(body, "found-form") {
		t.Error("after reporting, the page should thank the finder and hide the form")
	}
}

func TestFoundReportDropsImpossibleCoordinatesAndUntrustedGeoHeaders(t *testing.T) {
	s := webTestServer(t, nil) // TrustProxy off
	_, slug := addItem(t, s, "Key ring")
	postFound(foundMux(s), slug, "10.0.0.9", url.Values{"gps_lat": {"123.0"}, "gps_lon": {"10"}}, map[string]string{
		"CF-IPCountry": "ZZ", "X-Forwarded-For": "6.6.6.6",
	})
	reports, _ := s.db.ListFoundReports(10)
	if len(reports) != 1 {
		t.Fatalf("reports: %d", len(reports))
	}
	if r := reports[0]; r.GPSLat.Valid || r.IPCountry != "" || r.IP != "10.0.0.9" || r.Phone != "" {
		t.Errorf("stored unvalidated data: %+v", r)
	}
}

// The endpoint is public, so one address cannot file report after report.
func TestFoundReportThrottledPerIP(t *testing.T) {
	s := webTestServer(t, nil)
	s.foundIPLimiter = httpguard.NewRateLimiter(time.Minute)
	_, slug := addItem(t, s, "Backpack")
	mux := foundMux(s)

	if rec := postFound(mux, slug, "10.0.0.9", url.Values{}, nil); !strings.HasSuffix(rec.Header().Get("Location"), "?reported=1") {
		t.Fatalf("first report: %q", rec.Header().Get("Location"))
	}
	if rec := postFound(mux, slug, "10.0.0.9", url.Values{}, nil); !strings.HasSuffix(rec.Header().Get("Location"), "?reported=wait") {
		t.Errorf("second report from the same address: %q, want the wait notice", rec.Header().Get("Location"))
	}
	if rec := postFound(mux, slug, "10.0.0.10", url.Values{}, nil); !strings.HasSuffix(rec.Header().Get("Location"), "?reported=1") {
		t.Errorf("another address should not be throttled: %q", rec.Header().Get("Location"))
	}
	if reports, _ := s.db.ListFoundReports(10); len(reports) != 2 {
		t.Errorf("stored %d reports, want 2", len(reports))
	}
}

func TestFoundReportRejectsWhatIsNotAnActiveItem(t *testing.T) {
	s := webTestServer(t, nil)
	id, slug := addItem(t, s, "Old lamp")
	if _, err := s.db.CreateLink("docs", "https://example.org/", "admin", sql.NullInt64{}); err != nil {
		t.Fatal(err)
	}
	mux := foundMux(s)

	if rec := postFound(mux, "nope00", "10.0.0.1", url.Values{}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: %d, want 404", rec.Code)
	}
	if rec := postFound(mux, "docs", "10.0.0.2", url.Values{}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("custom short link: %d, want 404", rec.Code)
	}
	_ = s.db.SoftRemoveItem(id)
	if rec := postFound(mux, slug, "10.0.0.3", url.Values{}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("removed item: %d, want 404", rec.Code)
	}

	// No CSRF token: refused before anything is stored.
	req := httptest.NewRequest(http.MethodPost, "/found/"+slug, strings.NewReader("phone=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("missing CSRF token: %d, want 403", rec.Code)
	}
	if reports, _ := s.db.ListFoundReports(10); len(reports) != 0 {
		t.Errorf("stored %d reports, want none", len(reports))
	}

	// With item pages private, guests cannot file reports either.
	s.cfg.Server.PublicItems = false
	_, slug2 := addItem(t, s, "New lamp")
	if rec := postFound(mux, slug2, "10.0.0.4", url.Values{}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("public_items off: %d, want 404", rec.Code)
	}
}

// A stored report triggers one email per item per window.
func TestFoundReportEmailsOwnerOncePerWindow(t *testing.T) {
	host, port, got := smtptest.Start(t)
	s := webTestServer(t, func(c *config.Config) {
		c.Found.NotifyTo = "owner@example.test"
		c.Server.BaseURL = "https://solis.example"
	})
	s.mailer = notify.Mailer{Host: host, Port: port, From: "solis@example.test"}
	s.foundMailLimiter = httpguard.NewRateLimiter(time.Minute)
	_, slug := addItem(t, s, "Field recorder")
	mux := foundMux(s)

	postFound(mux, slug, "10.0.0.9", url.Values{"phone": {"555 0100"}, "email": {"finder@example.test"}, "message": {"Meet at the cafe on Main St\nafter 6 pm"}, "gps_lat": {"48.8584"}, "gps_lon": {"2.2945"}}, nil)
	select {
	case msg := <-got:
		for _, want := range []string{"Field recorder", strings.ToUpper(slug), "555 0100", "Reply-To: finder@example.test\r\n", "Email:        finder@example.test", "    Meet at the cafe on Main St\r\n    after 6 pm", "48.858400, 2.294500", "10.0.0.9", "Item page:    https://solis.example/default-room/" + slug, "Edit item:    https://solis.example/admin/items/edit/", "https://solis.example/admin/found"} {
			if !strings.Contains(msg.Data, want) {
				t.Errorf("email lacks %q:\n%s", want, msg.Data)
			}
		}
		if msg.To != "owner@example.test" {
			t.Errorf("recipient %q", msg.To)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no email sent")
	}
	// The emailed flag is set once delivery finished.
	deadline := time.Now().Add(3 * time.Second)
	for {
		reports, _ := s.db.ListFoundReports(1)
		if len(reports) == 1 && reports[0].Emailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("report was not marked as emailed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A second report for the same item is stored but does not send again.
	postFound(mux, slug, "10.0.0.10", url.Values{}, nil)
	select {
	case msg := <-got:
		t.Fatalf("second email inside the window: %s", msg.Data)
	case <-time.After(300 * time.Millisecond):
	}
	if reports, _ := s.db.ListFoundReports(10); len(reports) != 2 {
		t.Errorf("stored %d reports, want 2", len(reports))
	}
}

// Only a single plain address is accepted: it is stored, shown and used as
// the Reply-To header of the notification.
func TestCleanEmailAndMessage(t *testing.T) {
	for in, want := range map[string]string{
		"finder@example.test":                     "finder@example.test",
		" a.b+tag@mail.example.co ":               "a.b+tag@mail.example.co",
		"":                                        "",
		"not-an-email":                            "",
		"a@b":                                     "",
		"a@example.test, b@example.test":          "",
		"Name <a@example.test>":                   "",
		"a@example.test\r\nBcc: x@example.test":   "",
		"a@example.test\nBcc: x@example.test":     "",
		"a b@example.test":                        "",
		strings.Repeat("a", 65) + "@example.test": "",
	} {
		if got := cleanEmail(in); got != want {
			t.Errorf("cleanEmail(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanMessage(strings.Repeat("x", 700), 600); len(got) != 600 {
		t.Errorf("message not capped: %d characters", len(got))
	}
	if got := cleanMessage("  \n\n hello \n", 600); got != "hello" {
		t.Errorf("message not trimmed: %q", got)
	}
}

// The guest card offers the message box, both contact fields and the location
// button, and does not ask for a location on its own.
func TestFoundCardHasMessageContactsAndLocationButton(t *testing.T) {
	s := webTestServer(t, nil)
	_, slug := addItem(t, s, "Water bottle")
	body := get(publicMux(s), "/default-room/"+slug).Body.String()
	for _, want := range []string{`name="message"`, `name="phone"`, `name="email"`, `id="found-locate"`, `id="found-submit"`, `name="gps_lat"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %s", want)
		}
	}
	if strings.Contains(body, " required") {
		t.Error("every field on the card must be optional")
	}
}
