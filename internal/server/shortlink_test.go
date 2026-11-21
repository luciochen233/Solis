package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"Solis/internal/config"
	"Solis/internal/shortlink"
)

// fakeGlimmer is a stand-in for Glimmer's /api/links that records calls.
type fakeGlimmer struct {
	mu      sync.Mutex
	calls   []string          // "METHOD path"
	links   map[string]string // slug -> target
	fail    bool
	baseURL string
}

func newFakeGlimmer(t *testing.T) (*fakeGlimmer, *shortlink.Glimmer) {
	t.Helper()
	f := &fakeGlimmer{links: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-app-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		slug := strings.TrimPrefix(r.URL.Path, "/api/links/")
		switch {
		case r.Method == http.MethodPost:
			slug = "g" + string(rune('a'+len(f.links)))
			f.links[slug] = body.URL
		case r.Method == http.MethodPatch:
			if _, ok := f.links[slug]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.links[slug] = body.URL
		case r.Method == http.MethodDelete:
			if _, ok := f.links[slug]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.links, slug)
			_ = json.NewEncoder(w).Encode(map[string]any{"deleted": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"slug": slug, "url": f.links[slug], "short_url": "https://g.example/" + slug})
	}))
	t.Cleanup(srv.Close)
	f.baseURL = srv.URL
	return f, shortlink.NewGlimmer(srv.URL, "test-app-key")
}

func (f *fakeGlimmer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// With the Glimmer provider an item's QR code points at a Glimmer short link
// that leads back to this server's own /s/<slug>. The link is created once.
func TestQRTargetUsesGlimmerForItems(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.BaseURL = "https://solis.example" })
	fake, client := newFakeGlimmer(t)
	s.glimmer = client
	_, slug := addItem(t, s, "Tripod")
	link, _ := s.db.GetLinkBySlug(slug)

	got := s.qrTarget(context.Background(), link)
	if got != "https://g.example/ga" {
		t.Fatalf("qr target = %q, want the Glimmer short URL", got)
	}
	if fake.links["ga"] != "https://solis.example/s/"+slug {
		t.Errorf("Glimmer link points at %q, want this server's /s/ address", fake.links["ga"])
	}
	if again := s.qrTarget(context.Background(), link); again != got || fake.callCount() != 1 {
		t.Errorf("second lookup: %q after %d API calls, want the stored URL and no new call", again, fake.callCount())
	}

}

func TestQRTargetBuiltinWhenNoProviderOrNotAnItem(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.BaseURL = "https://solis.example/" })
	_, slug := addItem(t, s, "Lens cap")
	link, _ := s.db.GetLinkBySlug(slug)
	if got := s.qrTarget(context.Background(), link); got != "https://solis.example/s/"+slug {
		t.Errorf("no provider: %q, want the built-in short URL", got)
	}

	fake, client := newFakeGlimmer(t)
	s.glimmer = client
	link.ItemID.Valid = false // a custom short link, not an item
	if got := s.qrTarget(context.Background(), link); got != "https://solis.example/s/"+slug || fake.callCount() != 0 {
		t.Errorf("custom link: %q after %d API calls, want built-in and none", got, fake.callCount())
	}
}

// A label must always be printable: if Glimmer is down or refuses, the QR
// code falls back to the built-in link, and a later attempt tries again.
func TestQRTargetFallsBackWhenGlimmerFails(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.BaseURL = "https://solis.example" })
	fake, client := newFakeGlimmer(t)
	s.glimmer = client
	_, slug := addItem(t, s, "Flash")
	link, _ := s.db.GetLinkBySlug(slug)

	fake.fail = true
	if got := s.qrTarget(context.Background(), link); got != "https://solis.example/s/"+slug {
		t.Errorf("while Glimmer fails: %q, want the built-in short URL", got)
	}
	if _, err := s.db.GetItemShortURL(link.ItemID.Int64); err == nil {
		t.Error("a failed attempt must not be remembered")
	}
	fake.fail = false
	if got := s.qrTarget(context.Background(), link); got != "https://g.example/ga" {
		t.Errorf("after recovery: %q, want the Glimmer short URL", got)
	}

	// Wrong key: also falls back.
	s.glimmer = shortlink.NewGlimmer(fake.baseURL, "wrong-key")
	_, slug2 := addItem(t, s, "Strap")
	link2, _ := s.db.GetLinkBySlug(slug2)
	if got := s.qrTarget(context.Background(), link2); got != "https://solis.example/s/"+slug2 {
		t.Errorf("with a rejected key: %q, want the built-in short URL", got)
	}
}

// If this server's address for the item changes, the existing Glimmer link is
// repointed instead of replaced, so printed labels keep working.
func TestQRTargetRepointsWhenBaseURLChanges(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.BaseURL = "https://old.example" })
	fake, client := newFakeGlimmer(t)
	s.glimmer = client
	_, slug := addItem(t, s, "Battery")
	link, _ := s.db.GetLinkBySlug(slug)
	first := s.qrTarget(context.Background(), link)

	s.cfg.Server.BaseURL = "https://new.example"
	if got := s.qrTarget(context.Background(), link); got != first {
		t.Errorf("short URL changed from %q to %q; printed labels would break", first, got)
	}
	if fake.links["ga"] != "https://new.example/s/"+slug || len(fake.links) != 1 {
		t.Errorf("Glimmer links: %v, want the one link repointed", fake.links)
	}
}

func TestPermanentDeleteRemovesGlimmerLink(t *testing.T) {
	s := webTestServer(t, func(c *config.Config) { c.Server.BaseURL = "https://solis.example" })
	fake, client := newFakeGlimmer(t)
	s.glimmer = client
	id, slug := addItem(t, s, "Old charger")
	link, _ := s.db.GetLinkBySlug(slug)
	s.qrTarget(context.Background(), link)
	if len(fake.links) != 1 {
		t.Fatalf("setup: %v", fake.links)
	}

	s.dropExternalShortLink(id)
	if len(fake.links) != 0 {
		t.Errorf("Glimmer link survived the item's deletion: %v", fake.links)
	}
	if _, err := s.db.GetItemShortURL(id); err == nil {
		t.Error("stored short URL survived the item's deletion")
	}
	s.dropExternalShortLink(id) // nothing left: must be a no-op, not an error
}
