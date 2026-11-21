package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"Solis/internal/db"
	"Solis/internal/shortlink"

	"github.com/skip2/go-qrcode"
)

// GenerateQRCodePNG encodes a given string (e.g. short link URL) into a 256x256 PNG image
func GenerateQRCodePNG(data string) ([]byte, error) {
	png, err := qrcode.Encode(data, qrcode.Low, 256)
	if err != nil {
		return nil, fmt.Errorf("encoding qr code: %w", err)
	}
	return png, nil
}

// handleQR serves a PNG image containing a QR code for a given short slug
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "Missing slug", http.StatusBadRequest)
		return
	}

	link, err := s.db.GetLinkBySlug(slug)
	if err != nil {
		http.Error(w, "Slug not found", http.StatusNotFound)
		return
	}

	// Generate QR code PNG in all caps for minimum redundancy alphanumeric mode
	pngData, err := GenerateQRCodePNG(strings.ToUpper(s.qrTarget(r.Context(), link)))
	if err != nil {
		http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
		return
	}

	// Write PNG to response
	w.Header().Set("Content-Type", "image/png")
	// private: this is an authenticated route, so shared caches must not store it
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(pngData)
}

// builtinShortURL is this server's own short URL for a slug.
func (s *Server) builtinShortURL(slug string) string {
	return fmt.Sprintf("%s/s/%s", strings.TrimRight(s.cfg.Server.BaseURL, "/"), slug)
}

// qrTarget returns the URL a link's QR code should encode. For item links
// with the Glimmer provider that is a Glimmer short link pointing at the
// built-in /s/<slug> address (created on first use and remembered); in every
// other case, and whenever Glimmer cannot be reached, it is the built-in
// address itself — a label must always be printable.
func (s *Server) qrTarget(ctx context.Context, link *db.Link) string {
	builtin := s.builtinShortURL(link.Slug)
	if s.glimmer == nil || !link.ItemID.Valid {
		return builtin
	}
	itemID := link.ItemID.Int64

	known, err := s.db.GetItemShortURL(itemID)
	if err == nil && known.TargetURL == builtin {
		return known.ShortURL
	}

	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var remote *shortlink.Link
	if err == nil {
		// The item's own address changed (new slug or base_url): repoint the
		// existing short link so labels already printed keep working.
		remote, err = s.glimmer.Update(ctx, known.RemoteSlug, builtin)
		if e, ok := err.(*shortlink.StatusError); ok && e.Code == http.StatusNotFound {
			remote, err = s.glimmer.Create(ctx, builtin)
		}
	} else {
		remote, err = s.glimmer.Create(ctx, builtin)
	}
	if err != nil {
		log.Printf("glimmer short link for item %d: %v (using built-in link)", itemID, err)
		return builtin
	}
	if err := s.db.SetItemShortURL(&db.ItemShortURL{ItemID: itemID, RemoteSlug: remote.Slug, ShortURL: remote.ShortURL, TargetURL: builtin}); err != nil {
		log.Printf("glimmer short link for item %d: storing failed: %v", itemID, err)
	}
	return remote.ShortURL
}

// dropExternalShortLink removes an item's Glimmer short link, best effort,
// before the item itself is deleted for good.
func (s *Server) dropExternalShortLink(itemID int64) {
	if s.glimmer == nil {
		return
	}
	known, err := s.db.GetItemShortURL(itemID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := s.glimmer.Delete(ctx, known.RemoteSlug); err != nil {
		log.Printf("glimmer short link for item %d: delete failed: %v", itemID, err)
	}
	_ = s.db.DeleteItemShortURL(itemID)
}
