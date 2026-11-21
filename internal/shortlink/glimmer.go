// Package shortlink talks to an external short-link service (Glimmer's REST
// link API) so item QR codes can use its shorter URLs.
package shortlink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Link is a short link as the service reports it.
type Link struct {
	Slug     string `json:"slug"`
	URL      string `json:"url"`
	ShortURL string `json:"short_url"`
}

// Glimmer is a client for Glimmer's /api/links.
type Glimmer struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewGlimmer returns a client for the API at baseURL (for example
// "https://s.example.com", or "http://127.0.0.1:8888" when Glimmer runs on
// the same machine).
func NewGlimmer(baseURL, apiKey string) *Glimmer {
	return &Glimmer{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 5 * time.Second,
			// The API never redirects; following one could send the key elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Create makes (or returns the existing) short link pointing at target.
func (g *Glimmer) Create(ctx context.Context, target string) (*Link, error) {
	return g.call(ctx, http.MethodPost, "/api/links", map[string]string{"url": target})
}

// Update points an existing short link at a new target.
func (g *Glimmer) Update(ctx context.Context, slug, target string) (*Link, error) {
	return g.call(ctx, http.MethodPatch, "/api/links/"+url.PathEscape(slug), map[string]string{"url": target})
}

// Delete removes a short link. A link that is already gone is not an error.
func (g *Glimmer) Delete(ctx context.Context, slug string) error {
	_, err := g.call(ctx, http.MethodDelete, "/api/links/"+url.PathEscape(slug), nil)
	if e, ok := err.(*StatusError); ok && e.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// StatusError is a non-2xx answer from the service.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("short-link service answered %d: %s", e.Code, e.Message)
}

func (g *Glimmer) call(ctx context.Context, method, path string, body any) (*Link, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("short-link service unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &StatusError{Code: resp.StatusCode, Message: e.Error}
	}
	if method == http.MethodDelete {
		return nil, nil
	}
	var link Link
	if err := json.Unmarshal(raw, &link); err != nil {
		return nil, fmt.Errorf("short-link service sent an unreadable answer: %w", err)
	}
	// The short URL ends up inside QR codes, so insist on a plain web URL.
	u, err := url.Parse(link.ShortURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || link.Slug == "" {
		return nil, fmt.Errorf("short-link service sent an invalid link (%q)", link.ShortURL)
	}
	return &link, nil
}
