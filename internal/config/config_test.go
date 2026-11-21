package config

import "testing"

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
