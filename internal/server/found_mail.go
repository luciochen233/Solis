package server

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"Solis/internal/db"
)

// Found-report notifications.
//
// Every stored report must reach the owner, but the report endpoint is public,
// so the number of emails has to stay bounded. Reports are therefore never
// dropped; they are delivered by a small "pump":
//
//   - a report is emailed straight away if its item has not produced an email
//     within foundMailEvery;
//   - otherwise it waits, and when the window ends one email carries every
//     report for that item that arrived in the meantime;
//   - at most foundMailDailyCap emails go out per day; after that reports
//     still accumulate under Found Reports and are sent when the day rolls
//     over (or are visible in the admin UI regardless).
//
// The queue is simply the reports with emailed = 0 from the last 24 hours, so
// a restart loses nothing.
type foundMailState struct {
	mu      sync.Mutex
	running bool
	again   bool
	timer   *time.Timer
	nextOK  map[string]time.Time // item key -> earliest time for its next email
	day     string
	sentDay int
}

const (
	defaultFoundMailEvery    = 10 * time.Minute
	defaultFoundMailDailyCap = 200
	foundMailRetryAfter      = 15 * time.Minute // after a delivery failure
)

// kickFoundMail makes sure pending reports get emailed. It returns at once;
// the work happens in the background and only one pump runs at a time.
func (s *Server) kickFoundMail() {
	if s.cfg.Found.NotifyTo == "" || !s.mailer.Configured() {
		return
	}
	st := &s.foundMail
	st.mu.Lock()
	if st.running {
		st.again = true
		st.mu.Unlock()
		return
	}
	st.running = true
	st.mu.Unlock()

	go func() {
		for {
			wait := s.flushFoundMail()
			st.mu.Lock()
			if st.again {
				st.again = false
				st.mu.Unlock()
				continue
			}
			st.running = false
			if st.timer != nil {
				st.timer.Stop()
				st.timer = nil
			}
			if wait > 0 {
				st.timer = time.AfterFunc(wait, s.kickFoundMail)
			}
			st.mu.Unlock()
			return
		}
	}()
}

// flushFoundMail sends what is due and returns how long until the next
// deferred email is due (0 when nothing is waiting).
func (s *Server) flushFoundMail() time.Duration {
	reports, err := s.db.ListUnsentFoundReports(time.Now().Add(-24 * time.Hour))
	if err != nil {
		log.Printf("found mail: listing pending reports failed: %v", err)
		return foundMailRetryAfter
	}

	// Group by item, keeping the order in which items first appear.
	var order []string
	groups := map[string][]db.FoundReport{}
	for _, r := range reports {
		key := r.Slug
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}

	every := s.foundMailEvery
	if every <= 0 {
		every = defaultFoundMailEvery
	}
	dailyCap := s.foundMailDailyCap
	if dailyCap <= 0 {
		dailyCap = defaultFoundMailDailyCap
	}

	st := &s.foundMail
	var wait time.Duration
	deferUntil := func(d time.Duration) {
		if d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}

	for _, key := range order {
		group := groups[key]
		now := time.Now()

		st.mu.Lock()
		if st.nextOK == nil {
			st.nextOK = map[string]time.Time{}
		}
		if today := now.UTC().Format("2006-01-02"); st.day != today {
			st.day, st.sentDay = today, 0
		}
		due := st.nextOK[key]
		overCap := st.sentDay >= dailyCap
		st.mu.Unlock()

		if now.Before(due) {
			deferUntil(due.Sub(now))
			continue
		}
		if overCap {
			log.Printf("found mail: daily limit of %d emails reached; %d report(s) for %s wait under Found Reports", dailyCap, len(group), key)
			deferUntil(time.Hour)
			continue
		}

		subject, body, replyTo := foundEmail(group, s.cfg.Server.BaseURL)
		if err := s.mailer.SendWithReplyTo(s.cfg.Found.NotifyTo, replyTo, subject, body); err != nil {
			log.Printf("found mail: sending %d report(s) for %s failed: %v (retrying in %s)", len(group), key, err, foundMailRetryAfter)
			st.mu.Lock()
			st.nextOK[key] = time.Now().Add(foundMailRetryAfter)
			st.mu.Unlock()
			deferUntil(foundMailRetryAfter)
			continue
		}
		for _, r := range group {
			if err := s.db.MarkFoundReportEmailed(r.ID); err != nil {
				log.Printf("found report #%d: marking emailed failed: %v", r.ID, err)
			}
		}
		log.Printf("found mail: sent %d report(s) for %s", len(group), key)
		st.mu.Lock()
		st.nextOK[key] = time.Now().Add(every)
		st.sentDay++
		st.mu.Unlock()
	}
	return wait
}

// foundEmail renders one notification for all pending reports of an item
// (normally one). Reply-To is the most recent finder who left an address.
func foundEmail(reports []db.FoundReport, baseURL string) (subject, body, replyTo string) {
	base := strings.TrimRight(baseURL, "/")
	first := reports[0]
	ref := strings.ToUpper(first.Slug)

	var b strings.Builder
	if len(reports) == 1 {
		subject = fmt.Sprintf("Found: %s (%s)", first.ItemName, ref)
		fmt.Fprintf(&b, "Someone reported finding %q (label %s).\n\n", first.ItemName, ref)
	} else {
		subject = fmt.Sprintf("Found: %s (%s) — %d reports", first.ItemName, ref, len(reports))
		fmt.Fprintf(&b, "%d reports about %q (label %s), oldest first.\n\n", len(reports), first.ItemName, ref)
	}
	for i := range reports {
		r := &reports[i]
		if len(reports) > 1 {
			fmt.Fprintf(&b, "--- Report %d of %d ---\n", i+1, len(reports))
		}
		writeFoundReport(&b, r)
		b.WriteString("\n")
		if r.Email != "" {
			replyTo = r.Email
		}
	}

	// Links back into Solis. The item page shows the full record once you are
	// logged in (a guest sees only the found-item card there).
	fmt.Fprintf(&b, "Item page:    %s/s/%s\n", base, first.Slug)
	if first.ItemID.Valid {
		fmt.Fprintf(&b, "Edit item:    %s/admin/items/edit/%d\n", base, first.ItemID.Int64)
	}
	fmt.Fprintf(&b, "All reports:  %s/admin/found\n", base)
	return subject, b.String(), replyTo
}

func writeFoundReport(b *strings.Builder, r *db.FoundReport) {
	fmt.Fprintf(b, "Time:         %s UTC\n", r.CreatedAt.Format("2006-01-02 15:04:05"))
	if r.Message != "" {
		fmt.Fprintf(b, "Message from the finder:\n%s\n\n", indent(r.Message, "    "))
	}
	fmt.Fprintf(b, "Phone:        %s\n", orDash(r.Phone, "not provided"))
	fmt.Fprintf(b, "Email:        %s\n", orDash(r.Email, "not provided"))
	if r.GPSLat.Valid {
		fmt.Fprintf(b, "GPS location: %s\n", formatCoords(r.GPSLat, r.GPSLon, r.GPSAccuracy))
		fmt.Fprintf(b, "Map:          %s\n", MapURL(r.GPSLat.Float64, r.GPSLon.Float64))
	} else {
		b.WriteString("GPS location: not shared\n")
	}
	fmt.Fprintf(b, "IP address:   %s\n", r.IP)
	fmt.Fprintf(b, "IP location:  %s\n", orDash(IPPlace(r), "unknown"))
	if r.IPLat.Valid {
		fmt.Fprintf(b, "IP map:       %s (approximate)\n", MapURL(r.IPLat.Float64, r.IPLon.Float64))
	}
	fmt.Fprintf(b, "Browser:      %s\n", orDash(r.UserAgent, "unknown"))
}
