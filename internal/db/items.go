package db

import (
	"database/sql"
	"fmt"
	"time"
)

type Item struct {
	ID             int64
	Name           string
	Description    string
	Quantity       int
	ModelNumber    string
	SerialNumber   string
	Status         string // 'In Use', 'In Storage', 'Borrowed', 'Lost', 'Disposed'
	LocationID     sql.NullInt64
	LocationName   sql.NullString
	PurchasePrice  sql.NullFloat64
	PurchaseDate   sql.NullString // ISO8601 YYYY-MM-DD
	WarrantyMonths sql.NullInt64
	Supplier       string
	CustomFields   string // JSON text
	ImagePath      string
	ReceiptPath    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Tags           []Tag // Loaded on demand
	ShortSlug      string // Generated or loaded short URL slug
}

func (d *DB) CreateItem(item *Item, tagIDs []int64) (*Item, error) {
	// Wrap in a SQLite transaction for safe consistency
	tx, err := d.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO items (
			name, description, quantity, model_number, serial_number, status, 
			location_id, purchase_price, purchase_date, warranty_months, supplier, 
			custom_fields, image_path, receipt_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Name, item.Description, item.Quantity, item.ModelNumber, item.SerialNumber, item.Status,
		item.LocationID, item.PurchasePrice, item.PurchaseDate, item.WarrantyMonths, item.Supplier,
		item.CustomFields, item.ImagePath, item.ReceiptPath,
	)
	if err != nil {
		return nil, err
	}

	itemID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	item.ID = itemID

	// Insert tag associations
	if len(tagIDs) > 0 {
		stmt, err := tx.Prepare("INSERT INTO item_tags (item_id, tag_id) VALUES (?, ?)")
		if err != nil {
			return nil, err
		}
		defer stmt.Close()

		for _, tagID := range tagIDs {
			_, err = stmt.Exec(item.ID, tagID)
			if err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return item, nil
}

func (d *DB) GetItem(id int64) (*Item, error) {
	row := d.Conn.QueryRow(`
		SELECT 
			i.id, i.name, i.description, i.quantity, i.model_number, i.serial_number, i.status, 
			i.location_id, l.name AS location_name, i.purchase_price, i.purchase_date, i.warranty_months, 
			i.supplier, i.custom_fields, i.image_path, i.receipt_path, i.created_at, i.updated_at,
			COALESCE(lk.slug, '') AS short_slug
		FROM items i
		LEFT JOIN locations l ON i.location_id = l.id
		LEFT JOIN links lk ON lk.item_id = i.id
		WHERE i.id = ?`,
		id,
	)

	item, err := scanItem(row)
	if err != nil {
		return nil, err
	}

	// Load tags for this item
	tags, err := d.getItemTags(item.ID)
	if err != nil {
		return nil, err
	}
	item.Tags = d.arrangeTags(item.LocationName.String, tags)

	return item, nil
}

func (d *DB) UpdateItem(item *Item, tagIDs []int64) error {
	tx, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		UPDATE items SET 
			name = ?, description = ?, quantity = ?, model_number = ?, serial_number = ?, status = ?, 
			location_id = ?, purchase_price = ?, purchase_date = ?, warranty_months = ?, supplier = ?, 
			custom_fields = ?, image_path = ?, receipt_path = ?, updated_at = datetime('now') 
		WHERE id = ?`,
		item.Name, item.Description, item.Quantity, item.ModelNumber, item.SerialNumber, item.Status,
		item.LocationID, item.PurchasePrice, item.PurchaseDate, item.WarrantyMonths, item.Supplier,
		item.CustomFields, item.ImagePath, item.ReceiptPath, item.ID,
	)
	if err != nil {
		return err
	}

	// Rebuild tag associations
	_, err = tx.Exec("DELETE FROM item_tags WHERE item_id = ?", item.ID)
	if err != nil {
		return err
	}

	if len(tagIDs) > 0 {
		stmt, err := tx.Prepare("INSERT INTO item_tags (item_id, tag_id) VALUES (?, ?)")
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, tagID := range tagIDs {
			_, err = stmt.Exec(item.ID, tagID)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func (d *DB) DeleteItem(id int64) error {
	// Schema cascading rules handle tag mappings and short links automatically
	_, err := d.Conn.Exec("DELETE FROM items WHERE id = ?", id)
	return err
}

func (d *DB) ListItems() ([]Item, error) {
	rows, err := d.Conn.Query(`
		SELECT 
			i.id, i.name, i.description, i.quantity, i.model_number, i.serial_number, i.status, 
			i.location_id, l.name AS location_name, i.purchase_price, i.purchase_date, i.warranty_months, 
			i.supplier, i.custom_fields, i.image_path, i.receipt_path, i.created_at, i.updated_at,
			COALESCE(lk.slug, '') AS short_slug
		FROM items i
		LEFT JOIN locations l ON i.location_id = l.id
		LEFT JOIN links lk ON lk.item_id = i.id
		ORDER BY i.id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	var itemIDs []int64
	for rows.Next() {
		var i Item
		var created, updated string
		var locID sql.NullInt64
		var locName sql.NullString
		var price sql.NullFloat64
		var date sql.NullString
		var warranty sql.NullInt64

		err := rows.Scan(
			&i.ID, &i.Name, &i.Description, &i.Quantity, &i.ModelNumber, &i.SerialNumber, &i.Status,
			&locID, &locName, &price, &date, &warranty, &i.Supplier, &i.CustomFields, &i.ImagePath,
			&i.ReceiptPath, &created, &updated, &i.ShortSlug,
		)
		if err != nil {
			return nil, err
		}

		i.LocationID = locID
		i.LocationName = locName
		i.PurchasePrice = price
		i.PurchaseDate = date
		i.WarrantyMonths = warranty
		i.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		i.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)

		items = append(items, i)
		itemIDs = append(itemIDs, i.ID)
	}

	if len(items) == 0 {
		return items, nil
	}

	// Bulk load tags to avoid N+1 queries
	tagMap, err := d.getBulkItemTags(itemIDs)
	if err != nil {
		return nil, err
	}

	// Map tags to items
	for idx, item := range items {
		locName := ""
		if item.LocationName.Valid {
			locName = item.LocationName.String
		}
		if tags, exists := tagMap[item.ID]; exists {
			items[idx].Tags = d.arrangeTags(locName, tags)
		} else {
			items[idx].Tags = d.arrangeTags(locName, []Tag{})
		}
	}

	return items, nil
}

func (d *DB) getItemTags(itemID int64) ([]Tag, error) {
	rows, err := d.Conn.Query(`
		SELECT t.id, t.name, t.color, t.description 
		FROM item_tags it
		JOIN tags t ON it.tag_id = t.id
		WHERE it.item_id = ?`,
		itemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (d *DB) getBulkItemTags(itemIDs []int64) (map[int64][]Tag, error) {
	tagMap := make(map[int64][]Tag)
	if len(itemIDs) == 0 {
		return tagMap, nil
	}

	// Dynamic IN clause building for pure SQL safely (itemIDs are system-generated int64 values)
	inClause := ""
	for i, id := range itemIDs {
		if i > 0 {
			inClause += ","
		}
		inClause += fmt.Sprintf("%d", id)
	}

	query := fmt.Sprintf(`
		SELECT it.item_id, t.id, t.name, t.color, t.description 
		FROM item_tags it
		JOIN tags t ON it.tag_id = t.id
		WHERE it.item_id IN (%s)`, inClause)

	rows, err := d.Conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var itemID int64
		var t Tag
		if err := rows.Scan(&itemID, &t.ID, &t.Name, &t.Color, &t.Description); err != nil {
			return nil, err
		}
		tagMap[itemID] = append(tagMap[itemID], t)
	}

	return tagMap, rows.Err()
}

func scanItem(row interface {
	Scan(dest ...any) error
}) (*Item, error) {
	var i Item
	var created, updated string
	var locID sql.NullInt64
	var locName sql.NullString
	var price sql.NullFloat64
	var date sql.NullString
	var warranty sql.NullInt64

	err := row.Scan(
		&i.ID, &i.Name, &i.Description, &i.Quantity, &i.ModelNumber, &i.SerialNumber, &i.Status,
		&locID, &locName, &price, &date, &warranty, &i.Supplier, &i.CustomFields, &i.ImagePath,
		&i.ReceiptPath, &created, &updated, &i.ShortSlug,
	)
	if err != nil {
		return nil, err
	}

	i.LocationID = locID
	i.LocationName = locName
	i.PurchasePrice = price
	i.PurchaseDate = date
	i.WarrantyMonths = warranty
	i.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	i.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)

	return &i, nil
}

func (d *DB) arrangeTags(roomName string, tags []Tag) []Tag {
	if roomName == "" {
		return tags
	}

	var roomTag *Tag
	var otherTags []Tag

	for i, t := range tags {
		if t.Name == roomName {
			roomTag = &tags[i]
		} else {
			otherTags = append(otherTags, t)
		}
	}

	if roomTag == nil {
		// Room tag was not in the loaded tags, let's fetch it from db
		row := d.Conn.QueryRow("SELECT id, name, color, description FROM tags WHERE name = ?", roomName)
		var t Tag
		if err := row.Scan(&t.ID, &t.Name, &t.Color, &t.Description); err == nil {
			roomTag = &t
		} else {
			// fallback: construct a temporary Tag
			roomTag = &Tag{
				Name:  roomName,
				Color: "#f59e0b",
			}
		}
	}

	// Prepend roomTag
	return append([]Tag{*roomTag}, otherTags...)
}

// GetLastUsedLocationID returns the location ID of the most recently created/added item.
// If no items exist or none have a location assigned, it defaults to 1 (Default Room).
func (d *DB) GetLastUsedLocationID() (int64, error) {
	var locID int64
	err := d.Conn.QueryRow("SELECT location_id FROM items WHERE location_id IS NOT NULL ORDER BY id DESC LIMIT 1").Scan(&locID)
	if err != nil {
		if err == sql.ErrNoRows {
			return 1, nil // Fallback to Default Room
		}
		return 0, err
	}
	return locID, nil
}
