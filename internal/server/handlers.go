package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"Solis/internal/db"
	"Solis/internal/slug"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Global View Data context
type PageData struct {
	CSRFToken        string
	ActiveNav        string
	Toast            string
	ToastType        string
	Error            string
	Items            []db.Item
	Item             *db.Item
	Locations        []db.Location
	Tags             []db.Tag
	Links            []db.Link
	EditingLocation  *db.Location
	EditingTag       *db.Tag
	EditingLink      *db.Link
	EditingItem      *db.Item
	FormMode         bool
	ViewMode         bool
	CustomFieldsRaw  string
	CustomFieldsMap  map[string]string
	SearchQuery      string
	SelectedStatus   string
	SelectedLocation int64
	CatalogJSON      string
	SlugLength       int
	ShowRemoved      bool
}

// ==========================================
// 1. PUBLIC SHORT URL REDIRECT HANDLER
// ==========================================

func (s *Server) handleShortRedirect(w http.ResponseWriter, r *http.Request) {
	slugVal := r.PathValue("slug")
	if slugVal == "" {
		http.NotFound(w, r)
		return
	}
	slugVal = strings.ToLower(slugVal)

	// Fetch redirect target by slug
	link, err := s.db.GetLinkBySlug(slugVal)
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Increment analytics click counter
	_ = s.db.IncrementLinkClicks(link.Slug)

	// Perform redirect
	http.Redirect(w, r, link.URL, http.StatusMovedPermanently)
}

// ==========================================
// 2. AUTHENTICATION & LOGIN HANDLERS
// ==========================================

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to Admin dashboard
	if cookie, err := r.Cookie("session"); err == nil && s.sessions.Valid(cookie.Value) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	data := PageData{
		CSRFToken: csrfToken(w, r),
	}
	renderTemplate(w, "login.html", data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		data := PageData{
			CSRFToken: csrfToken(w, r),
			Error:     "Too many login attempts. Please wait.",
		}
		w.WriteHeader(http.StatusTooManyRequests)
		renderTemplate(w, "login.html", data)
		return
	}

	// Validate CSRF
	if !verifyCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// Verify Admin Credentials using Bcrypt
	if username == s.cfg.Admin.Username && bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)) == nil {
		token, err := s.sessions.Create()
		if err != nil {
			http.Error(w, "Session creation failed", http.StatusInternalServerError)
			return
		}

		// Set highly secure session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(time.Duration(s.cfg.Admin.SessionHours) * time.Hour / time.Second),
		})

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	data := PageData{
		CSRFToken: csrfToken(w, r),
		Error:     "Invalid username or password.",
	}
	renderTemplate(w, "login.html", data)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		s.sessions.Delete(cookie.Value)
	}

	// Expire session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ==========================================
// 3. ASSET INVENTORY ITEMS CRUD HANDLERS
// ==========================================

func (s *Server) handleAdminItems(w http.ResponseWriter, r *http.Request) {
	csrf := csrfToken(w, r)
	showRemoved := r.URL.Query().Get("show_removed") == "1"
	locations, _ := s.db.ListLocations(showRemoved)
	tags, _ := s.db.ListTags(showRemoved)

	// 1. DETAIL VIEW MODE
	if viewIDStr := r.URL.Query().Get("view"); viewIDStr != "" {
		id, err := strconv.ParseInt(viewIDStr, 10, 64)
		if err == nil {
			item, err := s.db.GetItem(id)
			if err == nil {
				// Decode JSON custom fields
				var cfMap map[string]string
				_ = json.Unmarshal([]byte(item.CustomFields), &cfMap)

				data := PageData{
					CSRFToken:       csrf,
					ActiveNav:       "items",
					Item:            item,
					ViewMode:        true,
					CustomFieldsMap: cfMap,
					Locations:       locations,
					Tags:            tags,
					ShowRemoved:     showRemoved,
					Toast:           r.URL.Query().Get("toast"),
					ToastType:       r.URL.Query().Get("toast_type"),
				}
				renderTemplate(w, "items.html", data)
				return
			}
		}
	}

	// 2. LIST ALL ITEMS MODE (With filter / search criteria)
	searchQuery := r.URL.Query().Get("search")
	statusQuery := r.URL.Query().Get("status")
	locationQuery := r.URL.Query().Get("location")

	allItems, err := s.db.ListItems(showRemoved)
	if err != nil {
		http.Error(w, "Failed to load assets", http.StatusInternalServerError)
		return
	}

	// Apply filtering in memory (simple and extremely fast on SQLite dataset)
	var filteredItems []db.Item
	var locFilterID int64
	if locationQuery != "" {
		locFilterID, _ = strconv.ParseInt(locationQuery, 10, 64)
	}

	for _, item := range allItems {
		matchesSearch := searchQuery == "" || 
			strings.Contains(strings.ToLower(item.Name), strings.ToLower(searchQuery)) ||
			strings.Contains(strings.ToLower(item.ModelNumber), strings.ToLower(searchQuery)) ||
			strings.Contains(strings.ToLower(item.SerialNumber), strings.ToLower(searchQuery))

		matchesStatus := statusQuery == "" || item.Status == statusQuery
		
		matchesLocation := locationQuery == "" || (item.LocationID.Valid && item.LocationID.Int64 == locFilterID)

		if matchesSearch && matchesStatus && matchesLocation {
			filteredItems = append(filteredItems, item)
		}
	}

	data := PageData{
		CSRFToken:        csrf,
		ActiveNav:        "items",
		Items:            filteredItems,
		Locations:        locations,
		Tags:             tags,
		SearchQuery:      searchQuery,
		SelectedStatus:   statusQuery,
		SelectedLocation: locFilterID,
		ShowRemoved:      showRemoved,
		Toast:            r.URL.Query().Get("toast"),
		ToastType:        r.URL.Query().Get("toast_type"),
	}

	renderTemplate(w, "items.html", data)
}

func (s *Server) handleItemNew(w http.ResponseWriter, r *http.Request) {
	locations, _ := s.db.ListLocations(false)
	tags, _ := s.db.ListTags(false)

	catalogJSON, err := json.Marshal(s.catalog)
	if err != nil {
		catalogJSON = []byte("[]")
	}

	data := PageData{
		CSRFToken:   csrfToken(w, r),
		ActiveNav:   "items",
		Locations:   locations,
		Tags:        tags,
		FormMode:    true,
		CatalogJSON: string(catalogJSON),
	}
	renderTemplate(w, "items.html", data)
}

func (s *Server) handleItemCreate(w http.ResponseWriter, r *http.Request) {
	// Limit file uploads to prevent resource exhaustion attacks
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Upload.MaxSize*1024*1024)
	if err := r.ParseMultipartForm(s.cfg.Upload.MaxSize * 1024 * 1024); err != nil {
		http.Error(w, "Upload exceeds maximum limit", http.StatusBadRequest)
		return
	}

	if r.FormValue("wizard_flow") == "true" {
		category := r.FormValue("category")
		itemVal := r.FormValue("item")
		brand := r.FormValue("brand")

		// 1. Determine the last used location ID
		lastLocID, err := s.db.GetLastUsedLocationID()
		if err != nil {
			lastLocID = 1 // Default Room
		}
		locID := sql.NullInt64{Int64: lastLocID, Valid: true}

		// 2. Format a beautiful name
		var name string
		if brand != "" && brand != "Other" && itemVal != "" && itemVal != "Other" {
			name = brand + " " + itemVal
		} else if itemVal != "" && itemVal != "Other" {
			name = itemVal
		} else if category != "" && category != "Other" {
			// Find category name from ID
			catName := category
			for _, cat := range s.catalog {
				if cat.ID == category {
					catName = cat.Name
					break
				}
			}
			name = catName + " Asset"
		} else {
			name = "New Asset"
		}

		// 3. Resolve the room tag automatically
		var tagIDs []int64
		tagIDs = s.resolveRoomTag(lastLocID, tagIDs)

		// 4. Construct category description
		var desc string
		catLabel := category
		for _, cat := range s.catalog {
			if cat.ID == category {
				catLabel = cat.Name
				break
			}
		}
		desc = fmt.Sprintf("Automatically cataloged via Wizard: %s > %s > %s", catLabel, itemVal, brand)

		item := &db.Item{
			Name:        name,
			Description: desc,
			Quantity:    1,
			Status:      "In Storage",
			LocationID:  locID,
		}

		// 5. Create the item record
		createdItem, err := s.db.CreateItem(item, tagIDs)
		if err != nil {
			log.Printf("Error creating wizard item: %v", err)
			http.Error(w, "Failed to create item", http.StatusInternalServerError)
			return
		}

		// 6. Generate collision-resistant short slug (using Glimmer SHA-256 slug engine)
		slugVal, err := slug.GenerateItemSlug(createdItem.ID, s.cfg.Slugs.Length, s.db.Conn)
		if err == nil && slugVal != "" {
			targetURL := fmt.Sprintf("/admin?view=%d", createdItem.ID)
			_, _ = s.db.CreateLink(slugVal, targetURL, "admin", sql.NullInt64{Int64: createdItem.ID, Valid: true})
		}

		// 7. Redirect straight to the Edit page so the user can modify any further details
		http.Redirect(w, r, fmt.Sprintf("/admin/items/edit/%d?toast=Asset+created+successfully!+You+can+refine+its+details+below.&toast_type=success", createdItem.ID), http.StatusSeeOther)
		return
	}

	name := r.FormValue("name")
	qty, _ := strconv.Atoi(r.FormValue("quantity"))
	desc := r.FormValue("description")
	model := r.FormValue("model_number")
	serial := r.FormValue("serial_number")
	status := r.FormValue("status")
	supplier := r.FormValue("supplier")

	var locID sql.NullInt64
	if locVal := r.FormValue("location_id"); locVal != "" {
		id, _ := strconv.ParseInt(locVal, 10, 64)
		locID = sql.NullInt64{Int64: id, Valid: true}
	} else {
		locID = sql.NullInt64{Int64: 1, Valid: true}
	}

	var price sql.NullFloat64
	if priceVal := r.FormValue("purchase_price"); priceVal != "" {
		p, _ := strconv.ParseFloat(priceVal, 64)
		price = sql.NullFloat64{Float64: p, Valid: true}
	}

	var date sql.NullString
	if dateVal := r.FormValue("purchase_date"); dateVal != "" {
		date = sql.NullString{String: dateVal, Valid: true}
	}

	var warranty sql.NullInt64
	if warrVal := r.FormValue("warranty_months"); warrVal != "" {
		w, _ := strconv.ParseInt(warrVal, 10, 64)
		warranty = sql.NullInt64{Int64: w, Valid: true}
	}

	// Parse tags checkboxes
	var tagIDs []int64
	for _, idStr := range r.MultipartForm.Value["tag_ids"] {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		tagIDs = append(tagIDs, id)
	}

	// Resolve the room tag, prepend it, and deduplicate
	tagIDs = s.resolveRoomTag(locID.Int64, tagIDs)

	// Handle secure file uploads (photo and receipt)
	imagePath, _ := s.saveUploadedFile(r, "image", true)
	receiptPath, _ := s.saveUploadedFile(r, "receipt", false)

	// Format custom fields input to SQLite JSON string
	customFieldsJSON := parseCustomFields(r.FormValue("custom_fields"))

	item := &db.Item{
		Name:           name,
		Description:    desc,
		Quantity:       qty,
		ModelNumber:    model,
		SerialNumber:   serial,
		Status:         status,
		LocationID:     locID,
		PurchasePrice:  price,
		PurchaseDate:   date,
		WarrantyMonths: warranty,
		Supplier:       supplier,
		CustomFields:   customFieldsJSON,
		ImagePath:      imagePath,
		ReceiptPath:    receiptPath,
	}

	// Insert item into database
	createdItem, err := s.db.CreateItem(item, tagIDs)
	if err != nil {
		log.Printf("Error creating item: %v", err)
		http.Error(w, "Failed to create item", http.StatusInternalServerError)
		return
	}

	// AUTOMATICALLY GENERATE A SHORT LINK (Glimmer SHA-256 collision-resistant port)
	slugVal, err := slug.GenerateItemSlug(createdItem.ID, s.cfg.Slugs.Length, s.db.Conn)
	if err == nil && slugVal != "" {
		// Target is the internal details view of this asset
		targetURL := fmt.Sprintf("/admin?view=%d", createdItem.ID)
		_, _ = s.db.CreateLink(slugVal, targetURL, "admin", sql.NullInt64{Int64: createdItem.ID, Valid: true})
	}

	http.Redirect(w, r, "/admin?toast=Asset+created+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	item, err := s.db.GetItem(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	locations, _ := s.db.ListLocations(false)
	tags, _ := s.db.ListTags(false)

	// Translate custom JSON string back to raw lines for edit comfort
	customFieldsRaw := formatCustomFields(item.CustomFields)

	data := PageData{
		CSRFToken:       csrfToken(w, r),
		ActiveNav:       "items",
		Locations:       locations,
		Tags:            tags,
		EditingItem:     item,
		CustomFieldsRaw: customFieldsRaw,
		FormMode:        true,
	}
	renderTemplate(w, "items.html", data)
}

func (s *Server) handleItemSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	item, err := s.db.GetItem(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Upload.MaxSize*1024*1024)
	if err := r.ParseMultipartForm(s.cfg.Upload.MaxSize * 1024 * 1024); err != nil {
		http.Error(w, "Upload size exceeded", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	qty, _ := strconv.Atoi(r.FormValue("quantity"))
	desc := r.FormValue("description")
	model := r.FormValue("model_number")
	serial := r.FormValue("serial_number")
	status := r.FormValue("status")
	supplier := r.FormValue("supplier")

	var locID sql.NullInt64
	if locVal := r.FormValue("location_id"); locVal != "" {
		lid, _ := strconv.ParseInt(locVal, 10, 64)
		locID = sql.NullInt64{Int64: lid, Valid: true}
	} else {
		locID = sql.NullInt64{Int64: 1, Valid: true}
	}

	var price sql.NullFloat64
	if priceVal := r.FormValue("purchase_price"); priceVal != "" {
		p, _ := strconv.ParseFloat(priceVal, 64)
		price = sql.NullFloat64{Float64: p, Valid: true}
	}

	var date sql.NullString
	if dateVal := r.FormValue("purchase_date"); dateVal != "" {
		date = sql.NullString{String: dateVal, Valid: true}
	}

	var warranty sql.NullInt64
	if warrVal := r.FormValue("warranty_months"); warrVal != "" {
		w, _ := strconv.ParseInt(warrVal, 10, 64)
		warranty = sql.NullInt64{Int64: w, Valid: true}
	}

	// Parse tags checkboxes
	var tagIDs []int64
	for _, idStr := range r.MultipartForm.Value["tag_ids"] {
		tid, _ := strconv.ParseInt(idStr, 10, 64)
		tagIDs = append(tagIDs, tid)
	}

	// Resolve the room tag, prepend it, and deduplicate
	tagIDs = s.resolveRoomTag(locID.Int64, tagIDs)

	// Securely upload new files and purge old replaced files
	newImage, hasNewImg := s.saveUploadedFile(r, "image", true)
	if hasNewImg && item.ImagePath != "" {
		_ = os.Remove(filepath.Join(s.cfg.Upload.Dir, item.ImagePath))
	}
	if hasNewImg {
		item.ImagePath = newImage
	}

	newReceipt, hasNewRec := s.saveUploadedFile(r, "receipt", false)
	if hasNewRec && item.ReceiptPath != "" {
		_ = os.Remove(filepath.Join(s.cfg.Upload.Dir, item.ReceiptPath))
	}
	if hasNewRec {
		item.ReceiptPath = newReceipt
	}

	customFieldsJSON := parseCustomFields(r.FormValue("custom_fields"))

	// Update record
	item.Name = name
	item.Description = desc
	item.Quantity = qty
	item.ModelNumber = model
	item.SerialNumber = serial
	item.Status = status
	item.LocationID = locID
	item.PurchasePrice = price
	item.PurchaseDate = date
	item.WarrantyMonths = warranty
	item.Supplier = supplier
	item.CustomFields = customFieldsJSON

	err = s.db.UpdateItem(item, tagIDs)
	if err != nil {
		http.Error(w, "Failed to update item", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin?view=%d&toast=Asset+updated+successfully&toast_type=success", item.ID), http.StatusSeeOther)
}

func (s *Server) handleItemRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.SoftRemoveItem(id)
	http.Redirect(w, r, "/admin?toast=Asset+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.ArchiveItem(id)
	http.Redirect(w, r, "/admin?toast=Asset+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.RestoreItem(id)
	http.Redirect(w, r, "/admin?show_removed=1&toast=Asset+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Load item details first to delete uploaded file media from disk
	item, err := s.db.GetItem(id)
	if err == nil {
		if item.ImagePath != "" {
			_ = os.Remove(filepath.Join(s.cfg.Upload.Dir, item.ImagePath))
		}
		if item.ReceiptPath != "" {
			_ = os.Remove(filepath.Join(s.cfg.Upload.Dir, item.ReceiptPath))
		}
	}

	_ = s.db.DeleteItem(id)
	http.Redirect(w, r, "/admin?show_removed=1&toast=Asset+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 4. LOCATIONS MANAGEMENT HANDLERS
// ==========================================

func (s *Server) handleAdminLocations(w http.ResponseWriter, r *http.Request) {
	csrf := csrfToken(w, r)
	showRemoved := r.URL.Query().Get("show_removed") == "1"
	locations, _ := s.db.ListLocations(showRemoved)

	var editLoc *db.Location
	if editIDStr := r.URL.Query().Get("edit"); editIDStr != "" {
		id, err := strconv.ParseInt(editIDStr, 10, 64)
		if err == nil {
			editLoc, _ = s.db.GetLocation(id)
		}
	}

	data := PageData{
		CSRFToken:       csrf,
		ActiveNav:       "locations",
		Locations:       locations,
		EditingLocation: editLoc,
		ShowRemoved:     showRemoved,
		Toast:           r.URL.Query().Get("toast"),
		ToastType:       r.URL.Query().Get("toast_type"),
	}
	renderTemplate(w, "locations.html", data)
}

func (s *Server) handleLocationCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	desc := r.FormValue("description")
	
	var parentID sql.NullInt64
	if parentVal := r.FormValue("parent_id"); parentVal != "" {
		id, _ := strconv.ParseInt(parentVal, 10, 64)
		parentID = sql.NullInt64{Int64: id, Valid: true}
	}

	_, err := s.db.CreateLocation(name, desc, parentID, "")
	if err != nil {
		http.Error(w, "Failed to create location", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/locations?toast=Location+created+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")
	desc := r.FormValue("description")
	
	var parentID sql.NullInt64
	if parentVal := r.FormValue("parent_id"); parentVal != "" {
		pid, _ := strconv.ParseInt(parentVal, 10, 64)
		parentID = sql.NullInt64{Int64: pid, Valid: true}
	}

	_ = s.db.UpdateLocation(id, name, desc, parentID, "")
	http.Redirect(w, r, "/admin/locations?toast=Location+updated+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.SoftRemoveLocation(id)
	http.Redirect(w, r, "/admin/locations?toast=Location+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.ArchiveLocation(id)
	http.Redirect(w, r, "/admin/locations?toast=Location+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.RestoreLocation(id)
	http.Redirect(w, r, "/admin/locations?show_removed=1&toast=Location+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.DeleteLocation(id)
	http.Redirect(w, r, "/admin/locations?show_removed=1&toast=Location+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 5. TAGS MANAGEMENT HANDLERS
// ==========================================

func (s *Server) handleAdminTags(w http.ResponseWriter, r *http.Request) {
	csrf := csrfToken(w, r)
	showRemoved := r.URL.Query().Get("show_removed") == "1"
	tags, _ := s.db.ListTags(showRemoved)

	var editTag *db.Tag
	if editIDStr := r.URL.Query().Get("edit"); editIDStr != "" {
		id, err := strconv.ParseInt(editIDStr, 10, 64)
		if err == nil {
			editTag, _ = s.db.GetTag(id)
		}
	}

	data := PageData{
		CSRFToken:   csrf,
		ActiveNav:   "tags",
		Tags:        tags,
		EditingTag:  editTag,
		ShowRemoved: showRemoved,
		Toast:       r.URL.Query().Get("toast"),
		ToastType:   r.URL.Query().Get("toast_type"),
	}
	renderTemplate(w, "tags.html", data)
}

func (s *Server) handleTagCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	color := r.FormValue("color")
	desc := r.FormValue("description")

	_, err := s.db.CreateTag(name, color, desc)
	if err != nil {
		http.Error(w, "Failed to create tag", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/tags?toast=Tag+created+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")
	color := r.FormValue("color")
	desc := r.FormValue("description")

	_ = s.db.UpdateTag(id, name, color, desc)
	http.Redirect(w, r, "/admin/tags?toast=Tag+updated+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.SoftRemoveTag(id)
	http.Redirect(w, r, "/admin/tags?toast=Tag+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.ArchiveTag(id)
	http.Redirect(w, r, "/admin/tags?toast=Tag+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.RestoreTag(id)
	http.Redirect(w, r, "/admin/tags?show_removed=1&toast=Tag+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.DeleteTag(id)
	http.Redirect(w, r, "/admin/tags?show_removed=1&toast=Tag+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 6. SHORTENER / LINK REDIRECT HANDLERS
// ==========================================

func (s *Server) handleAdminShortener(w http.ResponseWriter, r *http.Request) {
	csrf := csrfToken(w, r)
	showRemoved := r.URL.Query().Get("show_removed") == "1"
	links, _ := s.db.ListLinks(showRemoved)

	var editLink *db.Link
	if editIDStr := r.URL.Query().Get("edit"); editIDStr != "" {
		id, err := strconv.ParseInt(editIDStr, 10, 64)
		if err == nil {
			row := s.db.Conn.QueryRow("SELECT id, slug, url, item_id, created_by, clicks, lifecycle_state, created_at FROM links WHERE id = ?", id)
			var l db.Link
			var itemID sql.NullInt64
			var ts string
			if row.Scan(&l.ID, &l.Slug, &l.URL, &itemID, &l.CreatedBy, &l.Clicks, &l.LifecycleState, &ts) == nil {
				l.ItemID = itemID
				l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
				editLink = &l
			}
		}
	}

	data := PageData{
		CSRFToken:   csrf,
		ActiveNav:   "shortener",
		Links:       links,
		EditingLink: editLink,
		SlugLength:  s.cfg.Slugs.Length,
		ShowRemoved: showRemoved,
		Toast:       r.URL.Query().Get("toast"),
		ToastType:   r.URL.Query().Get("toast_type"),
	}
	renderTemplate(w, "shortener.html", data)
}

func (s *Server) handleLinkCreate(w http.ResponseWriter, r *http.Request) {
	slugVal := strings.TrimSpace(r.FormValue("slug"))
	urlVal := strings.TrimSpace(r.FormValue("url"))

	if urlVal == "" {
		http.Redirect(w, r, "/admin/shortener?toast=Destination+URL+is+required&toast_type=error", http.StatusSeeOther)
		return
	}

	// Auto generate slug if left blank
	if slugVal == "" {
		for i := 0; i < 10; i++ {
			temp := slug.Generate(s.cfg.Slugs.Length)
			exists, _ := s.db.SlugExists(temp)
			if !exists {
				slugVal = temp
				break
			}
		}
	}

	// Validate slug syntax using glimmer validator
	if err := slug.Validate(slugVal); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/shortener?toast=%s&toast_type=error", err.Error()), http.StatusSeeOther)
		return
	}

	// Validate duplicate slug
	exists, _ := s.db.SlugExists(slugVal)
	if exists {
		http.Redirect(w, r, "/admin/shortener?toast=Short+slug+already+exists&toast_type=error", http.StatusSeeOther)
		return
	}

	_, err := s.db.CreateLink(slugVal, urlVal, "admin", sql.NullInt64{Valid: false})
	if err != nil {
		http.Error(w, "Failed to create short link", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/shortener?toast=Custom+shortlink+added+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	slugVal := strings.TrimSpace(r.FormValue("slug"))
	urlVal := strings.TrimSpace(r.FormValue("url"))

	// Validate slug
	if err := slug.Validate(slugVal); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/shortener?toast=%s&toast_type=error", err.Error()), http.StatusSeeOther)
		return
	}

	// Check if this slug is taken by ANOTHER link ID
	var existingID int64
	err = s.db.Conn.QueryRow("SELECT id FROM links WHERE slug = ? AND id != ?", slugVal, id).Scan(&existingID)
	if err == nil {
		http.Redirect(w, r, "/admin/shortener?toast=Short+slug+already+taken&toast_type=error", http.StatusSeeOther)
		return
	}

	_ = s.db.UpdateLink(id, slugVal, urlVal)
	http.Redirect(w, r, "/admin/shortener?toast=Shortlink+updated+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.SoftRemoveLink(id)
	http.Redirect(w, r, "/admin/shortener?toast=Shortlink+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.ArchiveLink(id)
	http.Redirect(w, r, "/admin/shortener?toast=Shortlink+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.RestoreLink(id)
	http.Redirect(w, r, "/admin/shortener?show_removed=1&toast=Shortlink+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	_ = s.db.DeleteLink(id)
	http.Redirect(w, r, "/admin/shortener?show_removed=1&toast=Shortlink+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleSlugLengthUpdate(w http.ResponseWriter, r *http.Request) {
	lengthStr := strings.TrimSpace(r.FormValue("slug_length"))
	length, err := strconv.Atoi(lengthStr)
	if err != nil || length < 3 || length > 16 {
		http.Redirect(w, r, "/admin/shortener?toast=Slug+length+must+be+between+3+and+16&toast_type=error", http.StatusSeeOther)
		return
	}

	// Update in-memory config
	s.cfg.Slugs.Length = length

	// Persist to config.toml
	configPath := "config.toml"
	data, err := os.ReadFile(configPath)
	if err == nil {
		content := string(data)
		// Replace the length line in the [slugs] section
		re := regexp.MustCompile(`(?m)^(\s*length\s*=\s*)\d+`)
		content = re.ReplaceAllString(content, "${1}"+strconv.Itoa(length))
		_ = os.WriteFile(configPath, []byte(content), 0644)
	}

	http.Redirect(w, r, "/admin/shortener?toast=Slug+length+updated+to+"+strconv.Itoa(length)+"+characters&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 7. LABELS & PRINT ENGINE HANDLER
// ==========================================

func (s *Server) handlePrintLabels(w http.ResponseWriter, r *http.Request) {
	csrf := csrfToken(w, r)
	items, err := s.db.ListItems(false)
	if err != nil {
		http.Error(w, "Failed to load labels", http.StatusInternalServerError)
		return
	}

	data := PageData{
		CSRFToken: csrf,
		ActiveNav: "print",
		Items:     items,
	}
	renderTemplate(w, "print_labels.html", data)
}

// ==========================================
// 8. SECURITY PRIVATE MEDIA UPLOAD HELPERS
// ==========================================

func (s *Server) saveUploadedFile(r *http.Request, fieldName string, isImage bool) (string, bool) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		return "", false // No file uploaded or empty
	}
	defer file.Close()

	// Sniff MIME type using standard 512-byte detection
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	mimeType := http.DetectContentType(buffer[:n])
	file.Seek(0, io.SeekStart)

	// Validate content types
	if isImage {
		switch mimeType {
		case "image/jpeg", "image/png", "image/webp":
			// Allow
		default:
			log.Printf("Rejected image upload. Invalid MIME type: %s", mimeType)
			return "", false
		}
	} else {
		switch mimeType {
		case "image/jpeg", "image/png", "image/webp", "application/pdf":
			// Allow
		default:
			log.Printf("Rejected receipt document. Invalid MIME type: %s", mimeType)
			return "", false
		}
	}

	// Generate safe, randomized filename (UUID) keeping original extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		// Fallback clean extensions
		if mimeType == "application/pdf" {
			ext = ".pdf"
		} else if mimeType == "image/png" {
			ext = ".png"
		} else {
			ext = ".jpg"
		}
	}
	
	uniqueFilename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
	outPath := filepath.Join(s.cfg.Upload.Dir, uniqueFilename)

	outFile, err := os.Create(outPath)
	if err != nil {
		log.Printf("Error creating upload output file: %v", err)
		return "", false
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, file)
	if err != nil {
		log.Printf("Error writing file contents: %v", err)
		return "", false
	}

	return uniqueFilename, true
}

// Custom Fields Textarea parser: translates key: value lines to SQLite JSON
func parseCustomFields(raw string) string {
	lines := strings.Split(raw, "\n")
	m := make(map[string]string)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if key != "" && val != "" {
				m[key] = val
			}
		}
	}

	b, _ := json.Marshal(m)
	return string(b)
}

// Translates SQLite JSON string back to Key: Value lines for easy text form editing
func formatCustomFields(jsonStr string) string {
	if jsonStr == "" || jsonStr == "{}" {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	var sb strings.Builder
	for k, v := range m {
		sb.WriteString(fmt.Sprintf("%s: %s\n", k, v))
	}
	return sb.String()
}

func (s *Server) resolveRoomTag(locationID int64, tagIDs []int64) []int64 {
	var roomName string
	err := s.db.Conn.QueryRow("SELECT name FROM locations WHERE id = ?", locationID).Scan(&roomName)
	if err != nil {
		roomName = "Default Room"
	}
	var roomTagID int64
	err = s.db.Conn.QueryRow("SELECT id FROM tags WHERE name = ?", roomName).Scan(&roomTagID)
	if err != nil {
		// Try falling back to Default Room tag
		_ = s.db.Conn.QueryRow("SELECT id FROM tags WHERE name = 'Default Room'").Scan(&roomTagID)
	}

	// If we still don't have a valid tag ID, return the original tagIDs unchanged
	if roomTagID == 0 {
		return tagIDs
	}

	// Build a deduplicated list with roomTagID first
	dedupTagIDs := []int64{roomTagID}
	for _, tid := range tagIDs {
		if tid != roomTagID && tid != 0 {
			dedupTagIDs = append(dedupTagIDs, tid)
		}
	}
	return dedupTagIDs
}
