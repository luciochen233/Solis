package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPasswordAndLocalOnly(t *testing.T) {
	// The hash shipped in config.toml.example (password "admin").
	const exampleHash = "$2a$10$WXBoF7ZB7fIRl5smdGH9seqlNbXWiCS7ZbGNk0ONb9xjS8499gBpu"
	c := &Config{Admin: AdminConfig{PasswordHash: exampleHash}}
	if !c.UsesDefaultPassword() {
		t.Error("the example hash should be detected as the default password")
	}
	c.Admin.PasswordHash = "$2a$04$abcdefghijklmnopqrstuuJ0J8V0e0zY0Y0Y0Y0Y0Y0Y0Y0Y0Y0Y0" // not a hash of "admin"
	if c.UsesDefaultPassword() {
		t.Error("a different hash must not be reported as the default password")
	}

	for baseURL, want := range map[string]bool{
		"http://localhost:8889":    true,
		"http://127.0.0.1:8889":    true,
		"http://[::1]:8889":        true,
		"https://solis.example":    false,
		"http://192.168.1.20:8889": false,
		"":                         false,
	} {
		c.Server.BaseURL = baseURL
		if got := c.LocalOnly(); got != want {
			t.Errorf("LocalOnly(%q) = %v, want %v", baseURL, got, want)
		}
	}
}

func TestShortlinksProviderIsValidated(t *testing.T) {
	load := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("[admin]\npassword_hash = \"x\"\n"+body), 0600); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	if cfg, err := load(""); err != nil || cfg.Shortlinks.Provider != "builtin" {
		t.Errorf("default provider: %v %+v", err, cfg)
	}
	if _, err := load("[shortlinks]\nprovider = \"glimmer\"\nglimmer_url = \"http://127.0.0.1:8888\"\nglimmer_api_key = \"k\"\n"); err != nil {
		t.Errorf("valid glimmer config rejected: %v", err)
	}
	for name, body := range map[string]string{
		"unknown provider": "[shortlinks]\nprovider = \"bitly\"\n",
		"missing url":      "[shortlinks]\nprovider = \"glimmer\"\nglimmer_api_key = \"k\"\n",
		"missing key":      "[shortlinks]\nprovider = \"glimmer\"\nglimmer_url = \"http://127.0.0.1:8888\"\n",
		"non-http url":     "[shortlinks]\nprovider = \"glimmer\"\nglimmer_url = \"ftp://x\"\nglimmer_api_key = \"k\"\n",
	} {
		if _, err := load(body); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
