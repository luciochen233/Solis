package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Admin    AdminConfig    `toml:"admin"`
	MCP      MCPConfig      `toml:"mcp"`
	Database DatabaseConfig `toml:"database"`
	Slugs    SlugConfig     `toml:"slugs"`
	Upload   UploadConfig   `toml:"upload"`
	Found    FoundConfig    `toml:"found"`
	SMTP     SMTPConfig     `toml:"smtp"`
}

// FoundConfig controls the public "found item" page and its reports.
type FoundConfig struct {
	// Contact is shown to anyone who opens an item's public page, e.g.
	// "If found, please email lost@example.com". Publish only a contact you
	// are comfortable making public.
	Contact string `toml:"contact"`
	// NotifyTo receives an email for each found-item report (needs [smtp]).
	NotifyTo string `toml:"notify_to"`
}

// SMTPConfig is the outgoing mail server used for found-item notifications.
// Port 465 uses implicit TLS; any other port must offer STARTTLS (mail is
// never sent in the clear, except to a server on localhost).
type SMTPConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	From     string `toml:"from"`
}

type ServerConfig struct {
	// Host is the address to listen on. Empty (the default) listens on all
	// interfaces; set it to 127.0.0.1 when a reverse proxy on the same machine
	// is the only thing that should reach the app.
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	BaseURL      string `toml:"base_url"`
	ReadTimeout  string `toml:"read_timeout"`
	WriteTimeout string `toml:"write_timeout"`
	Language     string `toml:"language"`
	// TrustProxy controls whether X-Forwarded-For / X-Real-Ip are honoured for
	// client IP detection. Only enable behind a trusted reverse proxy;
	// otherwise a client can spoof them to bypass rate limiting.
	TrustProxy bool `toml:"trust_proxy"`
	// ProxyHops is how many trusted proxies sit in front of the server when
	// TrustProxy is on (default 1). The client IP is taken that many entries
	// from the right of X-Forwarded-For. Use 2 for Cloudflare -> nginx -> app.
	ProxyHops int `toml:"proxy_hops"`
	// MaxConcurrent caps the number of in-flight requests (503 beyond it).
	MaxConcurrent int `toml:"max_concurrent"`
	// PublicItems controls whether visitors who are not logged in can open an
	// item page by URL — e.g. someone who finds a lost item and scans its QR
	// label. They see the item's name, status and description, nothing else.
	// Item slugs are short and derived from the item ID, so these pages can
	// be enumerated: keep anything sensitive out of names and descriptions,
	// or set this to false to require a login.
	PublicItems bool `toml:"public_items"`
	// PublicLocations controls whether visitors who are not logged in can
	// open a location page (its name and description; never its contents).
	PublicLocations bool `toml:"public_locations"`
}

type AdminConfig struct {
	Username     string `toml:"username"`
	PasswordHash string `toml:"password_hash"`
	SessionHours int    `toml:"session_hours"`
}

type MCPConfig struct {
	Enabled bool   `toml:"enabled"`
	APIKey  string `toml:"api_key"`
}

type DatabaseConfig struct {
	Path string `toml:"path"`
}

type SlugConfig struct {
	Length int `toml:"length"`
}

type UploadConfig struct {
	Dir     string `toml:"dir"`
	MaxSize int64  `toml:"max_size_mb"`
}

func (c *ServerConfig) ReadTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.ReadTimeout)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

func (c *ServerConfig) WriteTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.WriteTimeout)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

// UsesDefaultPassword reports whether the admin password is still the
// well-known default ("admin") that ships in config.toml.example.
func (c *Config) UsesDefaultPassword() bool {
	return bcrypt.CompareHashAndPassword([]byte(c.Admin.PasswordHash), []byte("admin")) == nil
}

// LocalOnly reports whether base_url points at this machine (localhost or a
// loopback address), i.e. the instance is not meant to be reached by others.
func (c *Config) LocalOnly() bool {
	u, err := url.Parse(c.Server.BaseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Port:            8889,
			BaseURL:         "http://localhost:8889",
			ReadTimeout:     "5s",
			WriteTimeout:    "10s",
			Language:        "en",
			TrustProxy:      false,
			ProxyHops:       1,
			MaxConcurrent:   64,
			PublicItems:     true,
			PublicLocations: true,
		},
		Admin: AdminConfig{
			Username:     "admin",
			SessionHours: 24,
		},
		Database: DatabaseConfig{
			Path: "./data/solis.db",
		},
		Slugs: SlugConfig{
			Length: 4, // 4 characters gives plenty of short URL combinations (1.6M+) for home inventories
		},
		Upload: UploadConfig{
			Dir:     "./data/uploads",
			MaxSize: 10, // 10MB is plenty for home receipts and item photos
		},
		SMTP: SMTPConfig{Port: 587},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Admin.PasswordHash == "" {
		return nil, fmt.Errorf("admin.password_hash is required in config")
	}

	if cfg.Server.ProxyHops < 1 {
		cfg.Server.ProxyHops = 1
	}
	if cfg.Server.MaxConcurrent <= 0 {
		cfg.Server.MaxConcurrent = 64
	}
	if cfg.Upload.MaxSize <= 0 {
		cfg.Upload.MaxSize = 10
	}

	return cfg, nil
}
