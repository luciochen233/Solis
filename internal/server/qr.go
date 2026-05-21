package server

import (
	"fmt"
	"net/http"
	"strings"

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

// HandleQR serves a PNG image containing a QR code for a given short slug
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "Missing slug", http.StatusBadRequest)
		return
	}

	// Double check if slug exists in database
	exists, err := s.db.SlugExists(slug)
	if err != nil || !exists {
		http.Error(w, "Slug not found", http.StatusNotFound)
		return
	}

	// Build the absolute short URL
	shortURL := fmt.Sprintf("%s/s/%s", s.cfg.Server.BaseURL, slug)

	// Generate QR code PNG in all caps for minimum redundancy alphanumeric mode
	pngData, err := GenerateQRCodePNG(strings.ToUpper(shortURL))
	if err != nil {
		http.Error(w, "Failed to generate QR code", http.StatusInternalServerError)
		return
	}

	// Write PNG to response
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24h as QR codes are static
	w.Write(pngData)
}
