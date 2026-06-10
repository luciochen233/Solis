package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"Solis/internal/db"

	"golang.org/x/crypto/bcrypt"
)

const mcpProtocolVersion = "2025-06-18"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !s.validMCPOrigin(r) {
		writeMCPHTTPError(w, nil, http.StatusForbidden, -32000, "Forbidden origin")
		return
	}
	// Rate limit credential guessing: IPs with a recent failed auth attempt
	// are blocked before credentials are even checked. Successful requests
	// are never throttled.
	ip := clientIP(r)
	if s.limiter.Blocked(ip) {
		writeMCPHTTPError(w, nil, http.StatusTooManyRequests, -32000, "Too many failed authentication attempts. Please wait.")
		return
	}
	if !s.authorizeMCP(r) {
		s.limiter.Record(ip)
		w.Header().Set("WWW-Authenticate", `Bearer realm="Solis MCP", Basic realm="Solis MCP"`)
		writeMCPHTTPError(w, nil, http.StatusUnauthorized, -32000, "Unauthorized")
		return
	}

	var req mcpRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeMCPHTTPError(w, nil, http.StatusBadRequest, -32700, "Parse error")
		return
	}

	if req.JSONRPC != "2.0" {
		writeMCPJSON(w, http.StatusBadRequest, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32600, Message: "Invalid Request"},
		})
		return
	}

	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeMCPResult(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "solis",
				"version": "0.1.0",
			},
		})
	case "tools/list":
		writeMCPResult(w, req.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		s.handleMCPToolCall(w, req)
	default:
		writeMCPJSON(w, http.StatusNotFound, mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &mcpError{Code: -32601, Message: "Method not found"},
		})
	}
}

func (s *Server) authorizeMCP(r *http.Request) bool {
	if s.cfg.MCP.APIKey != "" {
		if token := bearerToken(r.Header.Get("Authorization")); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
			return true
		}
		if token := r.Header.Get("X-API-Key"); token != "" && constantTimeEqual(token, s.cfg.MCP.APIKey) {
			return true
		}
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	// Constant-time username check and unconditional bcrypt comparison so
	// response timing does not reveal whether the username was valid.
	usernameOK := constantTimeEqual(username, s.cfg.Admin.Username)
	passwordOK := bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)) == nil
	return usernameOK && passwordOK
}

func (s *Server) validMCPOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	baseURL, err := url.Parse(s.cfg.Server.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(originURL.Scheme, baseURL.Scheme) && strings.EqualFold(originURL.Host, baseURL.Host)
}

func (s *Server) handleMCPToolCall(w http.ResponseWriter, req mcpRequest) {
	var params mcpToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeMCPError(w, req.ID, -32602, "Invalid tool call parameters")
		return
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	var result any
	var err error
	switch params.Name {
	case "list_items":
		result, err = s.mcpListItems(params.Arguments)
	case "get_item":
		result, err = s.mcpGetItem(params.Arguments)
	case "create_item":
		result, err = s.mcpCreateItem(params.Arguments)
	case "update_item":
		result, err = s.mcpUpdateItem(params.Arguments)
	case "delete_item":
		result, err = s.mcpDeleteItem(params.Arguments)
	case "list_locations":
		result, err = s.mcpListLocations(params.Arguments)
	case "create_location":
		result, err = s.mcpCreateLocation(params.Arguments)
	case "update_location":
		result, err = s.mcpUpdateLocation(params.Arguments)
	case "delete_location":
		result, err = s.mcpDeleteLocation(params.Arguments)
	case "list_tags":
		result, err = s.mcpListTags(params.Arguments)
	case "create_tag":
		result, err = s.mcpCreateTag(params.Arguments)
	case "update_tag":
		result, err = s.mcpUpdateTag(params.Arguments)
	case "delete_tag":
		result, err = s.mcpDeleteTag(params.Arguments)
	default:
		writeMCPError(w, req.ID, -32602, "Unknown tool: "+params.Name)
		return
	}

	if err != nil {
		writeMCPResult(w, req.ID, map[string]any{
			"content": []map[string]string{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}

	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		writeMCPError(w, req.ID, -32603, "Failed to encode tool result")
		return
	}
	writeMCPResult(w, req.ID, map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(payload)}},
		"isError": false,
	})
}

func (s *Server) mcpListItems(args map[string]any) (any, error) {
	showRemoved := boolArg(args, "show_removed", false)
	items, err := s.db.ListItems(showRemoved)
	if err != nil {
		return nil, fmt.Errorf("failed to list items: %w", err)
	}

	query := strings.ToLower(strings.TrimSpace(stringArg(args, "query", "")))
	locationID, filterLocation := int64Arg(args, "location_id")
	response := make([]mcpItem, 0, len(items))
	for _, item := range items {
		if filterLocation && (!item.LocationID.Valid || item.LocationID.Int64 != locationID) {
			continue
		}
		converted := s.toMCPItem(item)
		if query != "" && !mcpItemMatches(converted, query) {
			continue
		}
		response = append(response, converted)
	}
	return response, nil
}

func (s *Server) mcpGetItem(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	item, err := s.db.GetItem(id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("item not found")
		}
		return nil, fmt.Errorf("failed to get item: %w", err)
	}
	return s.toMCPItem(*item), nil
}

func (s *Server) mcpCreateItem(args map[string]any) (any, error) {
	item, tagIDs, err := s.itemFromArgs(args, nil)
	if err != nil {
		return nil, err
	}
	created, err := s.db.CreateItem(item, tagIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to create item: %w", err)
	}
	if created.LocationID.Valid {
		s.syncItemShortlink(created.ID, created.LocationID.Int64)
	}
	return s.mcpGetItem(map[string]any{"id": created.ID})
}

func (s *Server) mcpUpdateItem(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	existing, err := s.db.GetItem(id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("item not found")
		}
		return nil, fmt.Errorf("failed to get item: %w", err)
	}

	item, tagIDs, err := s.itemFromArgs(args, existing)
	if err != nil {
		return nil, err
	}
	item.ID = id
	if err := s.db.UpdateItem(item, tagIDs); err != nil {
		return nil, fmt.Errorf("failed to update item: %w", err)
	}
	if item.LocationID.Valid {
		s.syncItemShortlink(item.ID, item.LocationID.Int64)
	}
	return s.mcpGetItem(map[string]any{"id": id})
}

func (s *Server) mcpDeleteItem(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action", "remove")))
	var err error
	switch action {
	case "remove", "soft_remove":
		err = s.db.SoftRemoveItem(id)
	case "archive":
		err = s.db.ArchiveItem(id)
	case "restore":
		err = s.db.RestoreItem(id)
	case "permanent", "delete":
		err = s.db.DeleteItem(id)
	default:
		return nil, fmt.Errorf("unsupported action %q; use remove, archive, restore, or permanent", action)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to %s item: %w", action, err)
	}
	return map[string]any{"id": id, "action": action, "ok": true}, nil
}

func (s *Server) mcpListLocations(args map[string]any) (any, error) {
	locations, err := s.db.ListLocations(boolArg(args, "show_removed", false))
	if err != nil {
		return nil, fmt.Errorf("failed to list locations: %w", err)
	}
	response := make([]mcpLocation, 0, len(locations))
	for _, loc := range locations {
		response = append(response, toMCPLocation(loc))
	}
	return response, nil
}

func (s *Server) mcpCreateLocation(args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name", ""))
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	parentID, err := nullableInt64FromArg(args, "parent_id", sql.NullInt64{})
	if err != nil {
		return nil, err
	}
	loc, err := s.db.CreateLocation(name, stringArg(args, "description", ""), parentID, stringArg(args, "image_path", ""))
	if err != nil {
		return nil, fmt.Errorf("failed to create location: %w", err)
	}
	return toMCPLocation(*loc), nil
}

func (s *Server) mcpUpdateLocation(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	loc, err := s.db.GetLocation(id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("location not found")
		}
		return nil, fmt.Errorf("failed to get location: %w", err)
	}
	name := stringArg(args, "name", loc.Name)
	description := stringArg(args, "description", loc.Description)
	imagePath := stringArg(args, "image_path", loc.ImagePath)
	parentID, err := nullableInt64FromArg(args, "parent_id", loc.ParentID)
	if err != nil {
		return nil, err
	}
	if parentID.Valid && s.isDescendantOf(parentID.Int64, id) {
		return nil, fmt.Errorf("parent_id would create a location cycle")
	}
	if err := s.db.UpdateLocation(id, name, description, parentID, imagePath); err != nil {
		return nil, fmt.Errorf("failed to update location: %w", err)
	}
	updated, err := s.db.GetLocation(id)
	if err != nil {
		return nil, fmt.Errorf("failed to reload location: %w", err)
	}
	return toMCPLocation(*updated), nil
}

func (s *Server) mcpDeleteLocation(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action", "remove")))
	var err error
	switch action {
	case "remove", "soft_remove":
		err = s.db.SoftRemoveLocation(id)
	case "archive":
		err = s.db.ArchiveLocation(id)
	case "restore":
		err = s.db.RestoreLocation(id)
	case "permanent", "delete":
		err = s.db.DeleteLocation(id)
	default:
		return nil, fmt.Errorf("unsupported action %q; use remove, archive, restore, or permanent", action)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to %s location: %w", action, err)
	}
	return map[string]any{"id": id, "action": action, "ok": true}, nil
}

func (s *Server) mcpListTags(args map[string]any) (any, error) {
	tags, err := s.db.ListTags(boolArg(args, "show_removed", false))
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}
	response := make([]mcpTag, 0, len(tags))
	for _, tag := range tags {
		response = append(response, toMCPTag(tag))
	}
	return response, nil
}

func (s *Server) mcpCreateTag(args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name", ""))
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	tag, err := s.db.CreateTag(name, stringArg(args, "color", ""), stringArg(args, "description", ""))
	if err != nil {
		return nil, fmt.Errorf("failed to create tag: %w", err)
	}
	return toMCPTag(*tag), nil
}

func (s *Server) mcpUpdateTag(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	tag, err := s.db.GetTag(id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("tag not found")
		}
		return nil, fmt.Errorf("failed to get tag: %w", err)
	}
	name := stringArg(args, "name", tag.Name)
	color := stringArg(args, "color", tag.Color)
	description := stringArg(args, "description", tag.Description)
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name cannot be empty")
	}
	if err := s.db.UpdateTag(id, name, color, description); err != nil {
		return nil, fmt.Errorf("failed to update tag: %w", err)
	}
	updated, err := s.db.GetTag(id)
	if err != nil {
		return nil, fmt.Errorf("failed to reload tag: %w", err)
	}
	return toMCPTag(*updated), nil
}

func (s *Server) mcpDeleteTag(args map[string]any) (any, error) {
	id, ok := int64Arg(args, "id")
	if !ok || id <= 0 {
		return nil, fmt.Errorf("id is required")
	}
	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action", "remove")))
	var err error
	switch action {
	case "remove", "soft_remove":
		err = s.db.SoftRemoveTag(id)
	case "archive":
		err = s.db.ArchiveTag(id)
	case "restore":
		err = s.db.RestoreTag(id)
	case "permanent", "delete":
		err = s.db.DeleteTag(id)
	default:
		return nil, fmt.Errorf("unsupported action %q; use remove, archive, restore, or permanent", action)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to %s tag: %w", action, err)
	}
	return map[string]any{"id": id, "action": action, "ok": true}, nil
}

func (s *Server) itemFromArgs(args map[string]any, existing *db.Item) (*db.Item, []int64, error) {
	item := &db.Item{
		Quantity:   1,
		Status:     "In Storage",
		LocationID: sql.NullInt64{Int64: 1, Valid: true},
	}
	tagIDs := []int64{}
	if existing != nil {
		copied := *existing
		item = &copied
		for _, tag := range existing.Tags {
			tagIDs = append(tagIDs, tag.ID)
		}
	}

	if value, ok := args["name"]; ok {
		item.Name = strings.TrimSpace(fmt.Sprint(value))
	}
	if item.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	item.Description = stringArg(args, "description", item.Description)
	item.ModelNumber = stringArg(args, "model_number", item.ModelNumber)
	item.SerialNumber = stringArg(args, "serial_number", item.SerialNumber)
	item.Status = stringArg(args, "status", item.Status)
	item.Supplier = stringArg(args, "supplier", item.Supplier)
	item.ImagePath = stringArg(args, "image_path", item.ImagePath)
	item.ReceiptPath = stringArg(args, "receipt_path", item.ReceiptPath)

	if quantity, ok := int64Arg(args, "quantity"); ok {
		if quantity < 0 {
			return nil, nil, fmt.Errorf("quantity cannot be negative")
		}
		item.Quantity = int(quantity)
	}

	var err error
	item.LocationID, err = nullableInt64FromArg(args, "location_id", item.LocationID)
	if err != nil {
		return nil, nil, err
	}
	item.PurchasePrice, err = nullableFloat64FromArg(args, "purchase_price", item.PurchasePrice)
	if err != nil {
		return nil, nil, err
	}
	item.PurchaseDate, err = nullableStringFromArg(args, "purchase_date", item.PurchaseDate)
	if err != nil {
		return nil, nil, err
	}
	item.WarrantyMonths, err = nullableInt64FromArg(args, "warranty_months", item.WarrantyMonths)
	if err != nil {
		return nil, nil, err
	}
	if customFields, ok, err := customFieldsArg(args, "custom_fields"); err != nil {
		return nil, nil, err
	} else if ok {
		item.CustomFields = customFields
	}

	if rawTagIDs, ok := args["tag_ids"]; ok {
		tagIDs, err = int64SliceArg(rawTagIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("tag_ids: %w", err)
		}
	}
	if item.LocationID.Valid {
		tagIDs = s.resolveRoomTag(item.LocationID.Int64, tagIDs)
	}
	return item, tagIDs, nil
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_items",
			"title":       "List inventory items",
			"description": "List Solis inventory items. Optional filters: query text, location_id, and show_removed.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":        map[string]any{"type": "string", "description": "Case-insensitive search over item name, description, model, serial, supplier, status, location, and tag names."},
					"location_id":  map[string]any{"type": "integer", "description": "Only return items assigned to this location ID."},
					"show_removed": map[string]any{"type": "boolean", "description": "Include removed items."},
				},
			},
		},
		{
			"name":        "get_item",
			"title":       "Get inventory item",
			"description": "Get a single Solis inventory item by ID.",
			"inputSchema": map[string]any{
				"type":       "object",
				"required":   []string{"id"},
				"properties": map[string]any{"id": map[string]any{"type": "integer", "description": "Solis item ID."}},
			},
		},
		{
			"name":        "create_item",
			"title":       "Create inventory item",
			"description": "Create a Solis inventory item. Generates the item's shortlink when a location is assigned.",
			"inputSchema": itemWriteSchema(true),
		},
		{
			"name":        "update_item",
			"title":       "Update inventory item",
			"description": "Patch a Solis inventory item by ID. Omitted fields are left unchanged.",
			"inputSchema": itemWriteSchema(false),
		},
		{
			"name":        "delete_item",
			"title":       "Change item lifecycle",
			"description": "Remove, archive, restore, or permanently delete a Solis item.",
			"inputSchema": lifecycleSchema("item ID"),
		},
		{
			"name":        "list_locations",
			"title":       "List locations",
			"description": "List Solis locations.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"show_removed": map[string]any{"type": "boolean", "description": "Include removed locations."}},
			},
		},
		{
			"name":        "create_location",
			"title":       "Create location",
			"description": "Create a Solis location.",
			"inputSchema": locationWriteSchema(true),
		},
		{
			"name":        "update_location",
			"title":       "Update location",
			"description": "Patch a Solis location by ID. Omitted fields are left unchanged.",
			"inputSchema": locationWriteSchema(false),
		},
		{
			"name":        "delete_location",
			"title":       "Change location lifecycle",
			"description": "Remove, archive, restore, or permanently delete a Solis location.",
			"inputSchema": lifecycleSchema("location ID"),
		},
		{
			"name":        "list_tags",
			"title":       "List tags",
			"description": "List Solis tags.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"show_removed": map[string]any{"type": "boolean", "description": "Include removed tags."}},
			},
		},
		{
			"name":        "create_tag",
			"title":       "Create tag",
			"description": "Create a Solis tag.",
			"inputSchema": tagWriteSchema(true),
		},
		{
			"name":        "update_tag",
			"title":       "Update tag",
			"description": "Patch a Solis tag by ID. Omitted fields are left unchanged.",
			"inputSchema": tagWriteSchema(false),
		},
		{
			"name":        "delete_tag",
			"title":       "Change tag lifecycle",
			"description": "Remove, archive, restore, or permanently delete a Solis tag.",
			"inputSchema": lifecycleSchema("tag ID"),
		},
	}
}

func itemWriteSchema(create bool) map[string]any {
	required := []string{}
	if create {
		required = append(required, "name")
	} else {
		required = append(required, "id")
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"id":              map[string]any{"type": "integer"},
			"name":            map[string]any{"type": "string"},
			"description":     map[string]any{"type": "string"},
			"quantity":        map[string]any{"type": "integer"},
			"model_number":    map[string]any{"type": "string"},
			"serial_number":   map[string]any{"type": "string"},
			"status":          map[string]any{"type": "string"},
			"location_id":     map[string]any{"type": []string{"integer", "null"}},
			"purchase_price":  map[string]any{"type": []string{"number", "null"}},
			"purchase_date":   map[string]any{"type": []string{"string", "null"}, "description": "YYYY-MM-DD"},
			"warranty_months": map[string]any{"type": []string{"integer", "null"}},
			"supplier":        map[string]any{"type": "string"},
			"custom_fields":   map[string]any{"type": []string{"object", "string", "null"}},
			"image_path":      map[string]any{"type": "string"},
			"receipt_path":    map[string]any{"type": "string"},
			"tag_ids":         map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
		},
	}
}

func locationWriteSchema(create bool) map[string]any {
	required := []string{}
	if create {
		required = append(required, "name")
	} else {
		required = append(required, "id")
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"id":          map[string]any{"type": "integer"},
			"name":        map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"parent_id":   map[string]any{"type": []string{"integer", "null"}},
			"image_path":  map[string]any{"type": "string"},
		},
	}
}

func tagWriteSchema(create bool) map[string]any {
	required := []string{}
	if create {
		required = append(required, "name")
	} else {
		required = append(required, "id")
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			"id":          map[string]any{"type": "integer"},
			"name":        map[string]any{"type": "string"},
			"color":       map[string]any{"type": "string", "description": "Hex color such as #3b82f6."},
			"description": map[string]any{"type": "string"},
		},
	}
}

func lifecycleSchema(idDescription string) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"id"},
		"properties": map[string]any{
			"id":     map[string]any{"type": "integer", "description": idDescription},
			"action": map[string]any{"type": "string", "enum": []string{"remove", "archive", "restore", "permanent"}, "description": "Defaults to remove."},
		},
	}
}

type mcpItem struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	Quantity       int      `json:"quantity"`
	ModelNumber    string   `json:"model_number,omitempty"`
	SerialNumber   string   `json:"serial_number,omitempty"`
	Status         string   `json:"status,omitempty"`
	LocationID     *int64   `json:"location_id,omitempty"`
	LocationName   string   `json:"location_name,omitempty"`
	PurchasePrice  *float64 `json:"purchase_price,omitempty"`
	PurchaseDate   string   `json:"purchase_date,omitempty"`
	WarrantyMonths *int64   `json:"warranty_months,omitempty"`
	Supplier       string   `json:"supplier,omitempty"`
	CustomFields   string   `json:"custom_fields,omitempty"`
	Tags           []mcpTag `json:"tags"`
	ShortSlug      string   `json:"short_slug,omitempty"`
	PublicURL      string   `json:"public_url,omitempty"`
	ShortURL       string   `json:"short_url,omitempty"`
	LifecycleState string   `json:"lifecycle_state"`
	CreatedAt      string   `json:"created_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

type mcpLocation struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Description    string `json:"description,omitempty"`
	ParentID       *int64 `json:"parent_id,omitempty"`
	ParentName     string `json:"parent_name,omitempty"`
	LifecycleState string `json:"lifecycle_state"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type mcpTag struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Color          string `json:"color,omitempty"`
	Description    string `json:"description,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

func (s *Server) toMCPItem(item db.Item) mcpItem {
	response := mcpItem{
		ID:             item.ID,
		Name:           item.Name,
		Description:    item.Description,
		Quantity:       item.Quantity,
		ModelNumber:    item.ModelNumber,
		SerialNumber:   item.SerialNumber,
		Status:         item.Status,
		Supplier:       item.Supplier,
		CustomFields:   item.CustomFields,
		ShortSlug:      item.ShortSlug,
		LifecycleState: item.LifecycleState,
		CreatedAt:      formatTime(item.CreatedAt),
		UpdatedAt:      formatTime(item.UpdatedAt),
		Tags:           make([]mcpTag, 0, len(item.Tags)),
	}
	if item.LocationID.Valid {
		response.LocationID = &item.LocationID.Int64
	}
	if item.LocationName.Valid {
		response.LocationName = item.LocationName.String
	}
	if item.PurchasePrice.Valid {
		response.PurchasePrice = &item.PurchasePrice.Float64
	}
	if item.PurchaseDate.Valid {
		response.PurchaseDate = item.PurchaseDate.String
	}
	if item.WarrantyMonths.Valid {
		response.WarrantyMonths = &item.WarrantyMonths.Int64
	}
	if item.ShortSlug != "" {
		response.ShortURL = joinBaseURL(s.cfg.Server.BaseURL, "/s/"+item.ShortSlug)
	}
	if item.LocationSlug.Valid && item.LocationSlug.String != "" && item.ShortSlug != "" {
		response.PublicURL = joinBaseURL(s.cfg.Server.BaseURL, "/"+item.LocationSlug.String+"/"+item.ShortSlug)
	}
	for _, tag := range item.Tags {
		response.Tags = append(response.Tags, toMCPTag(tag))
	}
	return response
}

func toMCPLocation(loc db.Location) mcpLocation {
	response := mcpLocation{
		ID:             loc.ID,
		Name:           loc.Name,
		Slug:           loc.Slug,
		Description:    loc.Description,
		LifecycleState: loc.LifecycleState,
		CreatedAt:      formatTime(loc.CreatedAt),
		UpdatedAt:      formatTime(loc.UpdatedAt),
	}
	if loc.ParentID.Valid {
		response.ParentID = &loc.ParentID.Int64
	}
	if loc.ParentName.Valid {
		response.ParentName = loc.ParentName.String
	}
	return response
}

func toMCPTag(tag db.Tag) mcpTag {
	return mcpTag{
		ID:             tag.ID,
		Name:           tag.Name,
		Color:          tag.Color,
		Description:    tag.Description,
		LifecycleState: tag.LifecycleState,
		CreatedAt:      formatTime(tag.CreatedAt),
	}
}

func mcpItemMatches(item mcpItem, query string) bool {
	fields := []string{
		item.Name,
		item.Description,
		item.ModelNumber,
		item.SerialNumber,
		item.Status,
		item.LocationName,
		item.Supplier,
		item.LifecycleState,
	}
	for _, tag := range item.Tags {
		fields = append(fields, tag.Name)
	}
	return strings.Contains(strings.ToLower(strings.Join(fields, " ")), query)
}

func writeMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeMCPJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func writeMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeMCPJSON(w, http.StatusOK, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeMCPHTTPError(w http.ResponseWriter, id json.RawMessage, status int, code int, message string) {
	writeMCPJSON(w, status, mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message}})
}

func writeMCPJSON(w http.ResponseWriter, status int, response mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if boolValue, ok := value.(bool); ok {
		return boolValue
	}
	if stringValue, ok := value.(string); ok {
		parsed, err := strconv.ParseBool(stringValue)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func stringArg(args map[string]any, key string, fallback string) string {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	if stringValue, ok := value.(string); ok {
		return stringValue
	}
	return fallback
}

func int64Arg(args map[string]any, key string) (int64, bool) {
	value, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func nullableInt64FromArg(args map[string]any, key string, fallback sql.NullInt64) (sql.NullInt64, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	if value == nil {
		return sql.NullInt64{}, nil
	}
	parsed, ok := int64Arg(args, key)
	if !ok {
		return fallback, fmt.Errorf("%s must be an integer or null", key)
	}
	return sql.NullInt64{Int64: parsed, Valid: true}, nil
}

func nullableFloat64FromArg(args map[string]any, key string, fallback sql.NullFloat64) (sql.NullFloat64, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	if value == nil {
		return sql.NullFloat64{}, nil
	}
	switch typed := value.(type) {
	case float64:
		return sql.NullFloat64{Float64: typed, Valid: true}, nil
	case int:
		return sql.NullFloat64{Float64: float64(typed), Valid: true}, nil
	case int64:
		return sql.NullFloat64{Float64: float64(typed), Valid: true}, nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return sql.NullFloat64{Float64: parsed, Valid: true}, nil
		}
	}
	return fallback, fmt.Errorf("%s must be a number or null", key)
}

func nullableStringFromArg(args map[string]any, key string, fallback sql.NullString) (sql.NullString, error) {
	value, ok := args[key]
	if !ok {
		return fallback, nil
	}
	if value == nil {
		return sql.NullString{}, nil
	}
	stringValue, ok := value.(string)
	if !ok {
		return fallback, fmt.Errorf("%s must be a string or null", key)
	}
	if stringValue == "" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: stringValue, Valid: true}, nil
}

func int64SliceArg(value any) ([]int64, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}
	result := make([]int64, 0, len(values))
	for _, raw := range values {
		switch typed := raw.(type) {
		case float64:
			result = append(result, int64(typed))
		case int64:
			result = append(result, typed)
		case int:
			result = append(result, int64(typed))
		case string:
			parsed, err := strconv.ParseInt(typed, 10, 64)
			if err != nil {
				return nil, err
			}
			result = append(result, parsed)
		default:
			return nil, fmt.Errorf("contains a non-integer value")
		}
	}
	return result, nil
}

func customFieldsArg(args map[string]any, key string) (string, bool, error) {
	value, ok := args[key]
	if !ok {
		return "", false, nil
	}
	if value == nil {
		return "", true, nil
	}
	if stringValue, ok := value.(string); ok {
		if strings.TrimSpace(stringValue) == "" {
			return "", true, nil
		}
		if json.Valid([]byte(stringValue)) {
			return stringValue, true, nil
		}
		return parseCustomFields(stringValue), true, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", false, fmt.Errorf("custom_fields must be a JSON object, string, or null")
	}
	return string(payload), true, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func joinBaseURL(baseURL, path string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		return path
	}
	return trimmed + path
}
