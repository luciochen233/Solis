package server

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"Solis/internal/db"
)

// The public "found item" flow. A visitor who is not logged in and opens an
// item URL (typically by scanning the QR label on a lost item) gets a card
// with the item's name, the owner's contact line and a button that reports
// the find. Nothing else from the item record is shown.

// renderFoundCard renders the guest view of an item.
func (s *Server) renderFoundCard(w http.ResponseWriter, r *http.Request, item *db.Item, slug string) {
	q := r.URL.Query()
	s.render(w, r, "found.html", PageData{
		CSRFToken:     s.csrfToken(w, r),
		Item:          item,
		FoundSlug:     slug,
		FoundRef:      strings.ToUpper(slug),
		FoundContact:  s.cfg.Found.Contact,
		FoundReported: q.Get("reported") == "1",
		FoundLimited:  q.Get("reported") == "wait",
	})
}

// handleFoundReport records an "I found this" report and notifies the owner.
// It is public by design, so everything about it is bounded: the CSRF token
// and cross-origin guard keep other sites from submitting it, per-IP and
// global windows cap how many reports can be stored, and a per-item window
// caps how many emails one item can generate.
func (s *Server) handleFoundReport(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Server.PublicItems && !s.isAuthenticated(r) {
		s.render404(w, r)
		return
	}
	slugVal := strings.ToLower(r.PathValue("slug"))
	link, err := s.db.GetLinkBySlug(slugVal)
	if err != nil || !link.ItemID.Valid || link.LifecycleState == "removed" {
		s.render404(w, r)
		return
	}
	item, err := s.db.GetItem(link.ItemID.Int64)
	if err != nil || item.LifecycleState == "removed" {
		s.render404(w, r)
		return
	}
	itemURL := "/" + locationSlugOf(item) + "/" + link.Slug

	ip := s.clientIP(r)
	if !s.foundIPLimiter.Allow(ip) || !s.foundAllLimiter.Allow("all") {
		http.Redirect(w, r, itemURL+"?reported=wait", http.StatusSeeOther)
		return
	}

	report := &db.FoundReport{
		ItemID:    sql.NullInt64{Int64: item.ID, Valid: true},
		ItemName:  item.Name,
		Slug:      link.Slug,
		IP:        ip,
		Phone:     cleanPhone(r.FormValue("phone")),
		UserAgent: cleanText(r.UserAgent(), 300),
	}
	// Precise location, present only if the finder's browser shared it.
	if lat, lon, ok := parseLatLon(r.FormValue("gps_lat"), r.FormValue("gps_lon")); ok {
		report.GPSLat = sql.NullFloat64{Float64: lat, Valid: true}
		report.GPSLon = sql.NullFloat64{Float64: lon, Valid: true}
		if acc, err := strconv.ParseFloat(r.FormValue("gps_accuracy"), 64); err == nil && acc >= 0 && acc < 1e7 {
			report.GPSAccuracy = sql.NullFloat64{Float64: acc, Valid: true}
		}
	}
	// Coarse location from the IP address. Cloudflare adds these headers at
	// its edge (overwriting anything the client sent), so they are only read
	// when the server is configured to trust its proxies.
	if s.cfg.Server.TrustProxy {
		report.IPCountry = cleanText(r.Header.Get("CF-IPCountry"), 64)
		report.IPRegion = cleanText(r.Header.Get("CF-Region"), 64)
		report.IPCity = cleanText(r.Header.Get("CF-IPCity"), 64)
		if lat, lon, ok := parseLatLon(r.Header.Get("CF-IPLatitude"), r.Header.Get("CF-IPLongitude")); ok {
			report.IPLat = sql.NullFloat64{Float64: lat, Valid: true}
			report.IPLon = sql.NullFloat64{Float64: lon, Valid: true}
		}
	}

	id, err := s.db.CreateFoundReport(report)
	if err != nil {
		log.Printf("found report for item %d: storing failed: %v", item.ID, err)
		http.Error(w, "Could not record the report", http.StatusInternalServerError)
		return
	}
	report.ID = id
	report.CreatedAt = time.Now().UTC()
	log.Printf("found report #%d: item %d (%s) from %s (gps shared: %v, phone given: %v)",
		id, item.ID, link.Slug, ip, report.GPSLat.Valid, report.Phone != "")

	s.notifyFound(report, itemURL)

	http.Redirect(w, r, itemURL+"?reported=1", http.StatusSeeOther)
}

// notifyFound emails the owner about a report, in the background so a slow
// mail server cannot hold up the finder's request. At most one email per item
// per window; throttled reports are still stored and listed in the admin UI.
func (s *Server) notifyFound(report *db.FoundReport, itemURL string) {
	to := s.cfg.Found.NotifyTo
	if to == "" || !s.mailer.Configured() {
		return
	}
	if !s.foundMailLimiter.Allow("item:" + strconv.FormatInt(report.ItemID.Int64, 10)) {
		log.Printf("found report #%d: email skipped (one per item per %s)", report.ID, foundMailWindow)
		return
	}
	subject := fmt.Sprintf("Found: %s (%s)", report.ItemName, strings.ToUpper(report.Slug))
	body := foundEmailBody(report, s.cfg.Server.BaseURL, itemURL)
	go func() {
		if err := s.mailer.Send(to, subject, body); err != nil {
			log.Printf("found report #%d: email failed: %v", report.ID, err)
			return
		}
		if err := s.db.MarkFoundReportEmailed(report.ID); err != nil {
			log.Printf("found report #%d: marking emailed failed: %v", report.ID, err)
		}
	}()
}

const foundMailWindow = 10 * time.Minute

func foundEmailBody(r *db.FoundReport, baseURL, itemURL string) string {
	base := strings.TrimRight(baseURL, "/")
	var b strings.Builder
	fmt.Fprintf(&b, "Someone reported finding %q (label %s).\n\n", r.ItemName, strings.ToUpper(r.Slug))
	fmt.Fprintf(&b, "Time:         %s UTC\n", r.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Phone:        %s\n", orDash(r.Phone, "not provided"))
	if r.GPSLat.Valid {
		fmt.Fprintf(&b, "GPS location: %s\n", formatCoords(r.GPSLat, r.GPSLon, r.GPSAccuracy))
		fmt.Fprintf(&b, "Map:          %s\n", MapURL(r.GPSLat.Float64, r.GPSLon.Float64))
	} else {
		b.WriteString("GPS location: not shared\n")
	}
	fmt.Fprintf(&b, "IP address:   %s\n", r.IP)
	fmt.Fprintf(&b, "IP location:  %s\n", orDash(IPPlace(r), "unknown"))
	if r.IPLat.Valid {
		fmt.Fprintf(&b, "IP map:       %s (approximate)\n", MapURL(r.IPLat.Float64, r.IPLon.Float64))
	}
	fmt.Fprintf(&b, "Browser:      %s\n\n", orDash(r.UserAgent, "unknown"))
	fmt.Fprintf(&b, "Item:         %s%s\n", base, itemURL)
	fmt.Fprintf(&b, "All reports:  %s/admin/found\n", base)
	return b.String()
}

// ---- Admin ----

func (s *Server) handleAdminFound(w http.ResponseWriter, r *http.Request) {
	reports, err := s.db.ListFoundReports(200)
	if err != nil {
		http.Error(w, "Failed to load reports", http.StatusInternalServerError)
		return
	}
	s.render(w, r, "found_reports.html", PageData{
		CSRFToken:    s.csrfToken(w, r),
		ActiveNav:    "found",
		FoundReports: reports,
		MailReady:    s.cfg.Found.NotifyTo != "" && s.mailer.Configured(),
		Toast:        r.URL.Query().Get("toast"),
		ToastType:    r.URL.Query().Get("toast_type"),
	})
}

func (s *Server) handleAdminFoundDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}
	_ = s.db.DeleteFoundReport(id)
	http.Redirect(w, r, "/admin/found?toast=Report+deleted&toast_type=success", http.StatusSeeOther)
}

// ---- Helpers ----

func locationSlugOf(item *db.Item) string {
	if item.LocationSlug.Valid && item.LocationSlug.String != "" {
		return item.LocationSlug.String
	}
	return "default-room"
}

// cleanPhone keeps only what a phone number can contain, so the value is safe
// to store, show and put in an email.
func cleanPhone(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= '0' && c <= '9') || c == '+' || c == '-' || c == ' ' || c == '(' || c == ')' || c == '.' {
			b.WriteRune(c)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// cleanText drops control characters and truncates to max runes.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, c := range s {
		if unicode.IsControl(c) {
			continue
		}
		b.WriteRune(c)
		if n++; n >= max {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func parseLatLon(latStr, lonStr string) (lat, lon float64, ok bool) {
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(latStr), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(lonStr), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// MapURL links a coordinate pair to a map.
func MapURL(lat, lon float64) string {
	return fmt.Sprintf("https://www.google.com/maps?q=%.6f,%.6f", lat, lon)
}

func formatCoords(lat, lon, acc sql.NullFloat64) string {
	s := fmt.Sprintf("%.6f, %.6f", lat.Float64, lon.Float64)
	if acc.Valid {
		s += fmt.Sprintf(" (±%.0f m)", acc.Float64)
	}
	return s
}

// IPPlace renders the coarse IP location as "City, Region, Country".
func IPPlace(r *db.FoundReport) string {
	var parts []string
	for _, p := range []string{r.IPCity, r.IPRegion, r.IPCountry} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func orDash(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
