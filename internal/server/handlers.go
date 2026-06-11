package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"Solis/internal/config"
	"Solis/internal/db"
	"Solis/internal/slug"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Global View Data context
type PageData struct {
	Lang             string
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
	SelectedTag      int64
	CatalogJSON      string
	SlugLength       int
	ShowRemoved      bool
	IsAuthenticated  bool
	CurrentLocation  *db.Location
	Breadcrumbs      []db.Location
	ChildLocations   []db.Location
	DrawerGrid       [][]DrawerCell
	LocationTree     []LocationTreeRow
}

// DrawerCell is one slot of a container array grid; Drawer is nil for empty slots.
type DrawerCell struct {
	Row    int64
	Col    int64
	Drawer *db.Drawer
}

// LocationTreeRow is a location flattened from the hierarchy in pre-order,
// carrying its nesting depth for the collapsible Locations table.
type LocationTreeRow struct {
	db.Location
	Depth       int
	Indent      int // indentation in px for the name cell
	HasChildren bool
}

// buildLocationTree flattens locations into pre-order rows. Locations whose
// parent is absent from the list (e.g. hidden removed parents) surface at the
// top level so they stay reachable.
func buildLocationTree(locations []db.Location) []LocationTreeRow {
	inList := make(map[int64]bool, len(locations))
	for _, l := range locations {
		inList[l.ID] = true
	}

	childrenOf := map[int64][]db.Location{}
	var roots []db.Location
	for _, l := range locations {
		if l.ParentID.Valid && l.ParentID.Int64 != l.ID && inList[l.ParentID.Int64] {
			childrenOf[l.ParentID.Int64] = append(childrenOf[l.ParentID.Int64], l)
		} else {
			roots = append(roots, l)
		}
	}

	rows := make([]LocationTreeRow, 0, len(locations))
	visited := map[int64]bool{}
	var walk func(l db.Location, depth int)
	walk = func(l db.Location, depth int) {
		if visited[l.ID] {
			return
		}
		visited[l.ID] = true
		kids := childrenOf[l.ID]
		rows = append(rows, LocationTreeRow{Location: l, Depth: depth, Indent: depth * 22, HasChildren: len(kids) > 0})
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return rows
}


// ==========================================
// 1. PUBLIC SHORT URL REDIRECT HANDLER
// ==========================================

func (s *Server) handleShortRedirect(w http.ResponseWriter, r *http.Request) {
	slugVal := r.PathValue("slug")
	if slugVal == "" {
		s.render404(w, r)
		return
	}
	slugVal = strings.ToLower(slugVal)

	// Fetch redirect target by slug
	link, err := s.db.GetLinkBySlug(slugVal)
	if err != nil {
		if err == sql.ErrNoRows {
			s.render404(w, r)
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
		CSRFToken: s.csrfToken(w, r),
	}
	s.render(w, r, "login.html", data)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		data := PageData{
			CSRFToken: s.csrfToken(w, r),
			Error:     "Too many login attempts. Please wait.",
		}
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, r, "login.html", data)
		return
	}

	// Validate CSRF
	if !verifyCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	// Verify Admin Credentials using Bcrypt. Compare the username in constant
	// time and always run the bcrypt check so response timing does not reveal
	// whether the username was valid.
	usernameOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.Admin.Username)) == 1
	passwordOK := bcrypt.CompareHashAndPassword([]byte(s.cfg.Admin.PasswordHash), []byte(password)) == nil
	if usernameOK && passwordOK {
		token, err := s.sessions.Create()
		if err != nil {
			http.Error(w, "Session creation failed", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   s.isHTTPS(),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(time.Duration(s.cfg.Admin.SessionHours) * time.Hour / time.Second),
		})

		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	data := PageData{
		CSRFToken: s.csrfToken(w, r),
		Error:     "Invalid username or password.",
	}
	s.render(w, r, "login.html", data)
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
	csrf := s.csrfToken(w, r)
	showRemoved := r.URL.Query().Get("show_removed") == "1"
	locations, _ := s.db.ListLocations(showRemoved)
	tags, _ := s.db.ListTags(showRemoved)

	// 1. DETAIL VIEW MODE (Upgrade / Redirect to clean URL format)
	if viewIDStr := r.URL.Query().Get("view"); viewIDStr != "" {
		id, err := strconv.ParseInt(viewIDStr, 10, 64)
		if err == nil {
			item, err := s.db.GetItem(id)
			if err == nil {
				var locSlug string
				if item.LocationID.Valid {
					loc, err := s.db.GetLocation(item.LocationID.Int64)
					if err == nil {
						locSlug = loc.Slug
					}
				}
				if locSlug == "" {
					locSlug = "default-room"
				}

				var itemSlug string
				link, err := s.db.GetLinkByItemID(item.ID)
				if err == nil {
					itemSlug = link.Slug
				} else {
					itemSlug, _ = slug.GenerateItemSlug(item.ID, s.cfg.Slugs.Length, s.db.Conn)
					if itemSlug != "" {
						newURL := fmt.Sprintf("/%s/%s", locSlug, itemSlug)
						_, _ = s.db.CreateLink(itemSlug, newURL, "admin", sql.NullInt64{Int64: item.ID, Valid: true})
					}
				}

				if itemSlug != "" {
					http.Redirect(w, r, fmt.Sprintf("/%s/%s?%s", locSlug, itemSlug, r.URL.RawQuery), http.StatusMovedPermanently)
					return
				}
			}
		}
	}

	// 2. LIST ALL ITEMS MODE (With filter / search criteria)
	searchQuery := r.URL.Query().Get("search")
	statusQuery := r.URL.Query().Get("status")
	locationQuery := r.URL.Query().Get("location")
	tagQuery := r.URL.Query().Get("tag")

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

	var tagFilterID int64
	if tagQuery != "" {
		tagFilterID, _ = strconv.ParseInt(tagQuery, 10, 64)
	}

	for _, item := range allItems {
		matchesSearch := searchQuery == "" || 
			strings.Contains(strings.ToLower(item.Name), strings.ToLower(searchQuery)) ||
			strings.Contains(strings.ToLower(item.ModelNumber), strings.ToLower(searchQuery)) ||
			strings.Contains(strings.ToLower(item.SerialNumber), strings.ToLower(searchQuery))

		matchesStatus := statusQuery == "" || item.Status == statusQuery
		
		matchesLocation := locationQuery == "" || (item.LocationID.Valid && item.LocationID.Int64 == locFilterID)

		matchesTag := true
		if tagFilterID > 0 {
			matchesTag = false
			for _, t := range item.Tags {
				if t.ID == tagFilterID {
					matchesTag = true
					break
				}
			}
		}

		if matchesSearch && matchesStatus && matchesLocation && matchesTag {
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
		SelectedTag:      tagFilterID,
		ShowRemoved:      showRemoved,
		Toast:            r.URL.Query().Get("toast"),
		ToastType:        r.URL.Query().Get("toast_type"),
	}

	s.render(w, r, "items.html", data)
}

func (s *Server) handleItemNew(w http.ResponseWriter, r *http.Request) {
	locations, _ := s.db.ListLocations(false)
	tags, _ := s.db.ListTags(false)

	catalogJSON, err := json.Marshal(s.localizedCatalog())
	if err != nil {
		catalogJSON = []byte("[]")
	}

	data := PageData{
		CSRFToken:   s.csrfToken(w, r),
		ActiveNav:   "items",
		Locations:   locations,
		Tags:        tags,
		FormMode:    true,
		CatalogJSON: string(catalogJSON),
	}
	s.render(w, r, "items.html", data)
}

func (s *Server) localizedCatalog() []config.CatalogCategory {
	result := make([]config.CatalogCategory, len(s.catalog))
	for i, cat := range s.catalog {
		result[i] = config.CatalogCategory{
			ID:    cat.ID,
			Name:  s.i18n.T(cat.Name),
			Color: cat.Color,
			Items: make([]config.CatalogItem, len(cat.Items)),
		}
		for j, item := range cat.Items {
			result[i].Items[j] = config.CatalogItem{
				Name:   s.i18n.T(item.Name),
				Brands: item.Brands,
			}
		}
	}
	return result
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
			catName := category
			for _, cat := range s.catalog {
				if cat.ID == category {
					catName = s.i18n.T(cat.Name)
					break
				}
			}
			name = catName + " " + s.i18n.T("wizard.asset_suffix")
		} else {
			name = s.i18n.T("wizard.new_asset")
		}

		// 3. Auto-tag with the selected catalog category
		var tagIDs []int64
		for _, cat := range s.catalog {
			if cat.ID == category {
				tagName := s.i18n.T(cat.Name)
				tag, err := s.db.GetTagByName(tagName)
				if err != nil {
					tag, err = s.db.CreateTag(tagName, cat.Color, s.i18n.T("wizard.auto_tag_desc"))
				}
				if err == nil {
					tagIDs = append(tagIDs, tag.ID)
				}
				break
			}
		}
		tagIDs = s.resolveRoomTag(lastLocID, tagIDs)

		// 4. Construct category description
		var desc string
		catLabel := category
		for _, cat := range s.catalog {
			if cat.ID == category {
				catLabel = s.i18n.T(cat.Name)
				break
			}
		}
		desc = s.i18n.TF("wizard.auto_desc", catLabel, itemVal, brand)

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

		// 6. Automatically generate and sync short link target as /{location_slug}/{item_slug}
		s.syncItemShortlink(createdItem.ID, lastLocID)

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

	// AUTOMATICALLY GENERATE & SYNC A SHORT LINK
	s.syncItemShortlink(createdItem.ID, locID.Int64)

	http.Redirect(w, r, "/admin?toast=Asset+created+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	item, err := s.db.GetItem(id)
	if err != nil {
		s.render404(w, r)
		return
	}

	locations, _ := s.db.ListLocations(false)
	tags, _ := s.db.ListTags(false)

	// Translate custom JSON string back to raw lines for edit comfort
	customFieldsRaw := formatCustomFields(item.CustomFields)

	data := PageData{
		CSRFToken:       s.csrfToken(w, r),
		ActiveNav:       "items",
		Locations:       locations,
		Tags:            tags,
		EditingItem:     item,
		CustomFieldsRaw: customFieldsRaw,
		FormMode:        true,
	}
	s.render(w, r, "items.html", data)
}

func (s *Server) handleItemSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	item, err := s.db.GetItem(id)
	if err != nil {
		s.render404(w, r)
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

	s.syncItemShortlink(item.ID, item.LocationID.Int64)

	var locSlug string
	if item.LocationID.Valid {
		loc, err := s.db.GetLocation(item.LocationID.Int64)
		if err == nil {
			locSlug = loc.Slug
		}
	}
	if locSlug == "" {
		locSlug = "default-room"
	}
	var itemSlug string
	link, err := s.db.GetLinkByItemID(item.ID)
	if err == nil {
		itemSlug = link.Slug
	} else {
		itemSlug = fmt.Sprintf("%d", item.ID)
	}

	http.Redirect(w, r, fmt.Sprintf("/%s/%s?toast=Asset+updated+successfully&toast_type=success", locSlug, itemSlug), http.StatusSeeOther)
}

func (s *Server) handleItemRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.SoftRemoveItem(id)
	http.Redirect(w, r, "/admin?toast=Asset+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.ArchiveItem(id)
	http.Redirect(w, r, "/admin?toast=Asset+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.RestoreItem(id)
	http.Redirect(w, r, "/admin?show_removed=1&toast=Asset+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleItemPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
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
	csrf := s.csrfToken(w, r)
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
		LocationTree:    buildLocationTree(locations),
		EditingLocation: editLoc,
		ShowRemoved:     showRemoved,
		Toast:           r.URL.Query().Get("toast"),
		ToastType:       r.URL.Query().Get("toast_type"),
	}
	s.render(w, r, "locations.html", data)
}

func (s *Server) handleLocationCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	desc := r.FormValue("description")
	
	var parentID sql.NullInt64
	if parentVal := r.FormValue("parent_id"); parentVal != "" {
		id, _ := strconv.ParseInt(parentVal, 10, 64)
		parentID = sql.NullInt64{Int64: id, Valid: true}
	}

	loc, err := s.db.CreateLocation(name, desc, parentID, "")
	if err != nil {
		http.Error(w, "Failed to create location", http.StatusInternalServerError)
		return
	}

	if rows, cols := parseGridSize(r); rows > 0 && cols > 0 {
		_ = s.db.SetLocationGrid(loc.ID, rows, cols)
	}

	http.Redirect(w, r, "/admin/locations?toast=Location+created+successfully&toast_type=success", http.StatusSeeOther)
}

// parseGridSize reads the container array dimensions from the location form.
// Returns 0,0 when the container array option is unchecked or values are invalid.
func parseGridSize(r *http.Request) (int64, int64) {
	if r.FormValue("is_array") == "" {
		return 0, 0
	}
	rows, _ := strconv.ParseInt(r.FormValue("grid_rows"), 10, 64)
	cols, _ := strconv.ParseInt(r.FormValue("grid_cols"), 10, 64)
	if rows < 1 || cols < 1 || rows > db.MaxGridSize || cols > db.MaxGridSize {
		return 0, 0
	}
	return rows, cols
}

// handleLocationEditPage serves the GET links used around the app (e.g. the
// folder view's "Edit Location" button) by opening the edit form on the
// Locations page.
func (s *Server) handleLocationEditPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}
	http.Redirect(w, r, "/admin/locations?edit="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handleLocationEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
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

	rows, cols := parseGridSize(r)
	if err := s.db.SetLocationGrid(id, rows, cols); err != nil {
		http.Redirect(w, r, "/admin/locations?toast="+url.QueryEscape(err.Error())+"&toast_type=error", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/locations?toast=Location+updated+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.SoftRemoveLocation(id)
	http.Redirect(w, r, "/admin/locations?toast=Location+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.ArchiveLocation(id)
	http.Redirect(w, r, "/admin/locations?toast=Location+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.RestoreLocation(id)
	http.Redirect(w, r, "/admin/locations?show_removed=1&toast=Location+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLocationPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.DeleteLocation(id)
	http.Redirect(w, r, "/admin/locations?show_removed=1&toast=Location+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 4b. CONTAINER ARRAY DRAWER HANDLERS
// ==========================================

// redirectToArray sends the user back to the container array folder view.
func (s *Server) redirectToArray(w http.ResponseWriter, r *http.Request, arrayID int64, toast string) {
	target := "/admin/locations"
	if array, err := s.db.GetLocation(arrayID); err == nil && array.Slug != "" {
		target = "/" + array.Slug
	}
	http.Redirect(w, r, target+"?toast="+url.QueryEscape(toast)+"&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleDrawerCreate(w http.ResponseWriter, r *http.Request) {
	arrayID, err := strconv.ParseInt(r.FormValue("array_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid array_id", http.StatusBadRequest)
		return
	}
	row, err1 := strconv.ParseInt(r.FormValue("row"), 10, 64)
	col, err2 := strconv.ParseInt(r.FormValue("col"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "Invalid drawer position", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = fmt.Sprintf("Drawer %c%d", 'A'+rune(row), col+1)
	}
	color := r.FormValue("color")

	if _, err := s.db.CreateDrawer(arrayID, row, col, name, color); err != nil {
		http.Redirect(w, r, "/admin/locations?toast="+url.QueryEscape(err.Error())+"&toast_type=error", http.StatusSeeOther)
		return
	}

	s.redirectToArray(w, r, arrayID, "Drawer created successfully")
}

func (s *Server) handleDrawerEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	drawer, err := s.db.GetLocation(id)
	if err != nil {
		s.render404(w, r)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = drawer.Name
	}
	color := r.FormValue("color")

	parentID := drawer.ParentID
	if parentVal := r.FormValue("parent_id"); parentVal != "" {
		pid, perr := strconv.ParseInt(parentVal, 10, 64)
		if perr == nil && pid != id {
			parentID = sql.NullInt64{Int64: pid, Valid: true}
		}
	}

	if err := s.db.UpdateDrawer(id, name, color, parentID); err != nil {
		http.Error(w, "Failed to update drawer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return to the array the drawer lived in before any reparenting
	if drawer.ParentID.Valid {
		s.redirectToArray(w, r, drawer.ParentID.Int64, "Drawer updated successfully")
		return
	}
	http.Redirect(w, r, "/admin/locations?toast=Drawer+updated+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleDrawerRemove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	drawer, err := s.db.GetLocation(id)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.SoftRemoveLocation(id)

	if drawer.ParentID.Valid {
		s.redirectToArray(w, r, drawer.ParentID.Int64, "Drawer removed; slot is now empty")
		return
	}
	http.Redirect(w, r, "/admin/locations?toast=Drawer+removed&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleDrawerMove(w http.ResponseWriter, r *http.Request) {
	drawerID, err := strconv.ParseInt(r.FormValue("drawer_id"), 10, 64)
	row, err1 := strconv.ParseInt(r.FormValue("row"), 10, 64)
	col, err2 := strconv.ParseInt(r.FormValue("col"), 10, 64)
	if err != nil || err1 != nil || err2 != nil {
		http.Error(w, "Invalid drawer_id or position", http.StatusBadRequest)
		return
	}

	if err := s.db.MoveDrawer(drawerID, row, col); err != nil {
		http.Error(w, "Failed to move drawer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success","message":"Drawer moved successfully"}`))
}

// ==========================================
// 5. TAGS MANAGEMENT HANDLERS
// ==========================================

func (s *Server) handleAdminTags(w http.ResponseWriter, r *http.Request) {
	csrf := s.csrfToken(w, r)
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
	s.render(w, r, "tags.html", data)
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
		s.render404(w, r)
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
		s.render404(w, r)
		return
	}

	_ = s.db.SoftRemoveTag(id)
	http.Redirect(w, r, "/admin/tags?toast=Tag+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.ArchiveTag(id)
	http.Redirect(w, r, "/admin/tags?toast=Tag+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.RestoreTag(id)
	http.Redirect(w, r, "/admin/tags?show_removed=1&toast=Tag+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleTagPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.DeleteTag(id)
	http.Redirect(w, r, "/admin/tags?show_removed=1&toast=Tag+deleted+forever+successfully&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 6. SHORTENER / LINK REDIRECT HANDLERS
// ==========================================

func (s *Server) handleAdminShortener(w http.ResponseWriter, r *http.Request) {
	csrf := s.csrfToken(w, r)
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
	s.render(w, r, "shortener.html", data)
}

func (s *Server) handleLinkCreate(w http.ResponseWriter, r *http.Request) {
	slugVal := strings.TrimSpace(r.FormValue("slug"))
	urlVal := strings.TrimSpace(r.FormValue("url"))

	if urlVal == "" {
		http.Redirect(w, r, "/admin/shortener?toast=Destination+URL+is+required&toast_type=error", http.StatusSeeOther)
		return
	}

	if !isValidRedirectURL(urlVal) {
		http.Redirect(w, r, "/admin/shortener?toast=URL+must+use+http,+https,+or+be+a+relative+path&toast_type=error", http.StatusSeeOther)
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
		s.render404(w, r)
		return
	}

	slugVal := strings.TrimSpace(r.FormValue("slug"))
	urlVal := strings.TrimSpace(r.FormValue("url"))

	// Validate slug
	if err := slug.Validate(slugVal); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/shortener?toast=%s&toast_type=error", err.Error()), http.StatusSeeOther)
		return
	}

	if !isValidRedirectURL(urlVal) {
		http.Redirect(w, r, "/admin/shortener?toast=URL+must+use+http,+https,+or+be+a+relative+path&toast_type=error", http.StatusSeeOther)
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
		s.render404(w, r)
		return
	}

	_ = s.db.SoftRemoveLink(id)
	http.Redirect(w, r, "/admin/shortener?toast=Shortlink+moved+to+soon-to-be-deleted+queue&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkArchive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.ArchiveLink(id)
	http.Redirect(w, r, "/admin/shortener?toast=Shortlink+archived+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
		return
	}

	_ = s.db.RestoreLink(id)
	http.Redirect(w, r, "/admin/shortener?show_removed=1&toast=Shortlink+restored+successfully&toast_type=success", http.StatusSeeOther)
}

func (s *Server) handleLinkPermanentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.render404(w, r)
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

	s.cfg.Slugs.Length = length

	http.Redirect(w, r, "/admin/shortener?toast=Slug+length+updated+to+"+strconv.Itoa(length)+"+characters&toast_type=success", http.StatusSeeOther)
}

// ==========================================
// 7. LABELS & PRINT ENGINE HANDLER
// ==========================================

func (s *Server) handlePrintLabels(w http.ResponseWriter, r *http.Request) {
	csrf := s.csrfToken(w, r)
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
	s.render(w, r, "print_labels.html", data)
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

func isValidRedirectURL(u string) bool {
	return strings.HasPrefix(u, "/") || strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func (s *Server) resolveRoomTag(locationID int64, tagIDs []int64) []int64 {
	return tagIDs
}

// render automatically injects IsAuthenticated based on the session token
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data PageData) {
	data.Lang = s.i18n.Lang()
	cookie, err := r.Cookie("session")
	data.IsAuthenticated = (err == nil && s.sessions.Valid(cookie.Value))
	if err := renderTemplate(w, name, data); err != nil {
		log.Printf("Template execution error for %s: %v", name, err)
		http.Error(w, "Template execution error", http.StatusInternalServerError)
	}
}

// render404 serves the styled 404 page that links (and auto-redirects) back home.
func (s *Server) render404(w http.ResponseWriter, r *http.Request) {
	csrf := s.csrfToken(w, r) // sets the CSRF cookie, so it must run before WriteHeader
	w.WriteHeader(http.StatusNotFound)
	s.render(w, r, "404.html", PageData{
		CSRFToken: csrf,
	})
}

// syncItemShortlink automatically synchronizes the shortlink URL target as /{location_slug}/{item_slug}
func (s *Server) syncItemShortlink(itemID int64, locID int64) {
	var locSlug string
	loc, err := s.db.GetLocation(locID)
	if err == nil {
		locSlug = loc.Slug
	} else {
		locSlug = "default-room"
	}

	link, err := s.db.GetLinkByItemID(itemID)
	if err != nil {
		if err == sql.ErrNoRows {
			// Generate clean, collision-resistant slug
			slugVal, err := slug.GenerateItemSlug(itemID, s.cfg.Slugs.Length, s.db.Conn)
			if err == nil && slugVal != "" {
				newURL := fmt.Sprintf("/%s/%s", locSlug, slugVal)
				_, _ = s.db.CreateLink(slugVal, newURL, "admin", sql.NullInt64{Int64: itemID, Valid: true})
			}
		}
		return
	}

	newURL := fmt.Sprintf("/%s/%s", locSlug, link.Slug)
	_ = s.db.UpdateLink(link.ID, link.Slug, newURL)
}

// isDescendantOf recursively checks if a location is a child/descendant of another to prevent cyclic nesting loops
func (s *Server) isDescendantOf(parentID, childID int64) bool {
	if parentID == childID {
		return true
	}
	child, err := s.db.GetLocation(childID)
	if err != nil || !child.ParentID.Valid {
		return false
	}
	return s.isDescendantOf(parentID, child.ParentID.Int64)
}

func (s *Server) handlePublicFallback(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		r.SetPathValue("location_slug", parts[0])
		s.handleFolderView(w, r)
		return
	} else if len(parts) == 2 {
		r.SetPathValue("location_slug", parts[0])
		r.SetPathValue("item_slug", parts[1])
		s.handlePublicItemView(w, r)
		return
	}

	s.render404(w, r)
}

func (s *Server) handleFolderView(w http.ResponseWriter, r *http.Request) {
	slugVal := r.PathValue("location_slug")
	if slugVal == "" {
		s.render404(w, r)
		return
	}
	slugVal = strings.ToLower(slugVal)

	loc, err := s.db.GetLocationBySlug(slugVal)
	if err != nil {
		if err == sql.ErrNoRows {
			s.render404(w, r)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	cookie, err := r.Cookie("session")
	isAuth := (err == nil && s.sessions.Valid(cookie.Value))

	var childLocs []db.Location
	var items []db.Item
	var breadcrumbs []db.Location
	var showRemoved bool
	var allLocations []db.Location

	var drawerGrid [][]DrawerCell

	if isAuth {
		showRemoved = r.URL.Query().Get("show_removed") == "1"

		childLocs, err = s.db.ListChildLocations(loc.ID, showRemoved)
		if err != nil {
			childLocs = []db.Location{}
		}

		// Container array: build the drawer grid and keep drawers out of the folder list
		if loc.GridRows > 0 && loc.GridCols > 0 {
			drawers, derr := s.db.ListDrawers(loc.ID)
			if derr != nil {
				drawers = []db.Drawer{}
			}
			byCell := make(map[[2]int64]*db.Drawer, len(drawers))
			for i := range drawers {
				byCell[[2]int64{drawers[i].Row, drawers[i].Col}] = &drawers[i]
			}
			drawerGrid = make([][]DrawerCell, loc.GridRows)
			for row := int64(0); row < loc.GridRows; row++ {
				cells := make([]DrawerCell, loc.GridCols)
				for col := int64(0); col < loc.GridCols; col++ {
					cells[col] = DrawerCell{Row: row, Col: col, Drawer: byCell[[2]int64{row, col}]}
				}
				drawerGrid[row] = cells
			}

			filtered := childLocs[:0]
			for _, c := range childLocs {
				if !c.GridRow.Valid {
					filtered = append(filtered, c)
				}
			}
			childLocs = filtered
		}

		items, err = s.db.ListItemsByLocation(loc.ID, showRemoved)
		if err != nil {
			items = []db.Item{}
		}

		// Build breadcrumbs recursively to the root (parentID = null)
		current := loc
		for {
			breadcrumbs = append([]db.Location{*current}, breadcrumbs...)
			if !current.ParentID.Valid {
				break
			}
			parent, err := s.db.GetLocation(current.ParentID.Int64)
			if err != nil {
				break
			}
			current = parent
		}

		allLocations, _ = s.db.ListLocations(false)
	} else {
		// Guest user: return empty lists, hide folder structures and breadcrumbs
		childLocs = []db.Location{}
		items = []db.Item{}
		breadcrumbs = []db.Location{}
		allLocations = []db.Location{}
		showRemoved = false
	}

	data := PageData{
		CSRFToken:       s.csrfToken(w, r),
		ActiveNav:       "locations",
		CurrentLocation: loc,
		ChildLocations:  childLocs,
		Items:           items,
		Breadcrumbs:     breadcrumbs,
		Locations:       allLocations,
		ShowRemoved:     showRemoved,
		DrawerGrid:      drawerGrid,
	}

	s.render(w, r, "location_folder.html", data)
}

func (s *Server) handlePublicItemView(w http.ResponseWriter, r *http.Request) {
	locationSlug := r.PathValue("location_slug")
	itemSlug := r.PathValue("item_slug")

	link, err := s.db.GetLinkBySlug(itemSlug)
	if err != nil {
		if err == sql.ErrNoRows {
			s.render404(w, r)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if !link.ItemID.Valid {
		s.render404(w, r)
		return
	}

	item, err := s.db.GetItem(link.ItemID.Int64)
	if err != nil {
		s.render404(w, r)
		return
	}

	var correctLocationSlug string
	if item.LocationID.Valid {
		loc, err := s.db.GetLocation(item.LocationID.Int64)
		if err == nil {
			correctLocationSlug = loc.Slug
		}
	}
	if correctLocationSlug == "" {
		correctLocationSlug = "default-room"
	}

	if correctLocationSlug != locationSlug {
		http.Redirect(w, r, fmt.Sprintf("/%s/%s", correctLocationSlug, itemSlug), http.StatusMovedPermanently)
		return
	}

	// Resolve the parent location details if available for breadcrumb fallback
	var currentLoc *db.Location
	if item.LocationID.Valid {
		currentLoc, _ = s.db.GetLocation(item.LocationID.Int64)
	}

	var cfMap map[string]string
	_ = json.Unmarshal([]byte(item.CustomFields), &cfMap)

	locations, _ := s.db.ListLocations(false)
	tags, _ := s.db.ListTags(false)

	data := PageData{
		CSRFToken:       s.csrfToken(w, r),
		ActiveNav:       "items",
		Item:            item,
		ViewMode:        true,
		CustomFieldsMap: cfMap,
		Locations:       locations,
		Tags:            tags,
		CurrentLocation: currentLoc,
	}

	s.render(w, r, "items.html", data)
}

func (s *Server) handleItemMove(w http.ResponseWriter, r *http.Request) {
	itemIDStr := r.FormValue("item_id")
	locIDStr := r.FormValue("location_id")

	itemID, err1 := strconv.ParseInt(itemIDStr, 10, 64)
	locID, err2 := strconv.ParseInt(locIDStr, 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "Invalid item_id or location_id", http.StatusBadRequest)
		return
	}

	err := s.db.MoveItemLocation(itemID, locID)
	if err != nil {
		http.Error(w, "Failed to move item: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.syncItemShortlink(itemID, locID)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success","message":"Item moved successfully"}`))
}

func (s *Server) handleLocationMove(w http.ResponseWriter, r *http.Request) {
	locIDStr := r.FormValue("location_id")
	parentIDStr := r.FormValue("parent_id")

	locID, err1 := strconv.ParseInt(locIDStr, 10, 64)
	parentID, err2 := strconv.ParseInt(parentIDStr, 10, 64)
	if err1 != nil {
		http.Error(w, "Invalid location_id", http.StatusBadRequest)
		return
	}

	if err2 != nil {
		parentID = 0
	}

	if locID == parentID {
		http.Error(w, "A location cannot be a parent of itself", http.StatusBadRequest)
		return
	}

	if parentID > 0 {
		if s.isDescendantOf(locID, parentID) {
			http.Error(w, "Cycle detected: cannot move a location inside its own sub-folder", http.StatusBadRequest)
			return
		}
	}

	err := s.db.MoveLocationParent(locID, parentID)
	if err != nil {
		http.Error(w, "Failed to move location: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success","message":"Location moved successfully"}`))
}

