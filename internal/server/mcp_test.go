package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"Solis/internal/config"
	"Solis/internal/db"
)

func TestMCPRequiresAuthentication(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8889"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestMCPListsToolsWithAPIKey(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8889"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"list_items"`) || !strings.Contains(body, `"get_item"`) {
		t.Fatalf("expected tool list in response, got %s", body)
	}
}

func TestMCPRejectsUnexpectedOrigin(t *testing.T) {
	srv := &Server{cfg: &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:8889"},
		MCP:    config.MCPConfig{Enabled: true, APIKey: "test-key"},
	}}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()

	srv.handleMCP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestMCPWriteToolsCreateAndUpdateInventory(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "solis.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	srv := &Server{
		cfg: &config.Config{
			Server: config.ServerConfig{BaseURL: "http://localhost:8889"},
			Slugs:  config.SlugConfig{Length: 4},
		},
		db: database,
	}

	tagResult, err := srv.mcpCreateTag(map[string]any{
		"name":        "Workbench",
		"color":       "#3b82f6",
		"description": "Garage bench items",
	})
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	tagID := tagResult.(mcpTag).ID

	locResult, err := srv.mcpCreateLocation(map[string]any{"name": "Garage"})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	locID := locResult.(mcpLocation).ID

	itemResult, err := srv.mcpCreateItem(map[string]any{
		"name":        "Torque wrench",
		"quantity":    float64(1),
		"status":      "In Storage",
		"location_id": float64(locID),
		"tag_ids":     []any{float64(tagID)},
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	item := itemResult.(mcpItem)
	if item.ID == 0 || item.ShortSlug == "" {
		t.Fatalf("expected created item with shortlink, got %#v", item)
	}

	updatedResult, err := srv.mcpUpdateItem(map[string]any{
		"id":       float64(item.ID),
		"quantity": float64(2),
		"supplier": "Local hardware store",
	})
	if err != nil {
		t.Fatalf("update item: %v", err)
	}
	updated := updatedResult.(mcpItem)
	if updated.Quantity != 2 || updated.Supplier != "Local hardware store" {
		t.Fatalf("expected patched item, got %#v", updated)
	}

	if _, err := srv.mcpUpdateLocation(map[string]any{"id": float64(locID), "description": "Main tool storage"}); err != nil {
		t.Fatalf("update location: %v", err)
	}
	if _, err := srv.mcpUpdateTag(map[string]any{"id": float64(tagID), "description": "Updated"}); err != nil {
		t.Fatalf("update tag: %v", err)
	}
}
