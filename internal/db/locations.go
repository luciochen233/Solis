package db

import (
	"database/sql"
	"fmt"
	"time"

	"Solis/internal/slug"
)

type Location struct {
	ID             int64
	Name           string
	Slug           string
	Description    string
	ParentID       sql.NullInt64
	ParentName     sql.NullString // Resolved parent location name
	ImagePath      string
	LifecycleState string // 'active', 'archived', 'removed'
	GridRows       int64  // Container array height (0 = not a container array)
	GridCols       int64  // Container array width (0 = not a container array)
	GridRow        sql.NullInt64
	GridCol        sql.NullInt64
	Color          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Drawer is a child location positioned inside a parent container array,
// enriched with a summary of its contents.
type Drawer struct {
	ID        int64
	Name      string
	Slug      string
	Color     string
	Row       int64
	Col       int64
	ItemCount int64
	ItemNames string // comma-separated preview of item names
}

func (d *DB) CreateLocation(name, description string, parentID sql.NullInt64, imagePath string) (*Location, error) {
	tx, err := d.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	slugVal := slug.Slugify(name)
	uniqueSlug := slugVal
	index := 1
	for {
		var count int
		err := tx.QueryRow("SELECT COUNT(*) FROM locations WHERE slug = ?", uniqueSlug).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			break
		}
		uniqueSlug = fmt.Sprintf("%s-%d", slugVal, index)
		index++
	}

	res, err := tx.Exec(
		"INSERT INTO locations (name, slug, description, parent_id, image_path) VALUES (?, ?, ?, ?, ?)",
		name, uniqueSlug, description, parentID, imagePath,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Location{
		ID:          id,
		Name:        name,
		Slug:        uniqueSlug,
		Description: description,
		ParentID:    parentID,
		ImagePath:   imagePath,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}, nil
}

func (d *DB) GetLocation(id int64) (*Location, error) {
	row := d.Conn.QueryRow(`
		SELECT l.id, l.name, l.slug, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.grid_rows, l.grid_cols, l.grid_row, l.grid_col, l.color, l.created_at, l.updated_at
		FROM locations l 
		LEFT JOIN locations p ON l.parent_id = p.id 
		WHERE l.id = ?`,
		id,
	)
	return scanLocation(row)
}

func (d *DB) GetLocationBySlug(slug string) (*Location, error) {
	row := d.Conn.QueryRow(`
		SELECT l.id, l.name, l.slug, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.grid_rows, l.grid_cols, l.grid_row, l.grid_col, l.color, l.created_at, l.updated_at
		FROM locations l 
		LEFT JOIN locations p ON l.parent_id = p.id 
		WHERE l.slug = ?`,
		slug,
	)
	return scanLocation(row)
}

func (d *DB) UpdateLocation(id int64, name, description string, parentID sql.NullInt64, imagePath string) error {
	if id == 1 && name != "Default Room" {
		return fmt.Errorf("cannot rename the Default Room")
	}

	tx, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldName string
	var oldParentID sql.NullInt64
	err = tx.QueryRow("SELECT name, parent_id FROM locations WHERE id = ?", id).Scan(&oldName, &oldParentID)
	if err != nil {
		return err
	}

	slugVal := slug.Slugify(name)
	uniqueSlug := slugVal
	index := 1
	for {
		var count int
		err := tx.QueryRow("SELECT COUNT(*) FROM locations WHERE slug = ? AND id != ?", uniqueSlug, id).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			break
		}
		uniqueSlug = fmt.Sprintf("%s-%d", slugVal, index)
		index++
	}

	_, err = tx.Exec(
		"UPDATE locations SET name = ?, slug = ?, description = ?, parent_id = ?, image_path = ?, updated_at = datetime('now') WHERE id = ?",
		name, uniqueSlug, description, parentID, imagePath, id,
	)
	if err != nil {
		return err
	}

	// Reparenting pulls a drawer out of its container array grid, freeing the slot
	if oldParentID != parentID {
		if _, err = tx.Exec("UPDATE locations SET grid_row = NULL, grid_col = NULL WHERE id = ?", id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *DB) DeleteLocation(id int64) error {
	if id == 1 {
		return fmt.Errorf("cannot delete the Default Room")
	}

	tx, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Move items in this location to Default Room (ID = 1)
	_, err = tx.Exec("UPDATE items SET location_id = 1 WHERE location_id = ?", id)
	if err != nil {
		return err
	}

	// Set parent references to NULL
	_, err = tx.Exec("UPDATE locations SET parent_id = NULL WHERE parent_id = ?", id)
	if err != nil {
		return err
	}

	// Delete the location
	_, err = tx.Exec("DELETE FROM locations WHERE id = ?", id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (d *DB) SoftRemoveLocation(id int64) error {
	if id == 1 {
		return fmt.Errorf("cannot remove the Default Room")
	}
	// Clearing the grid position leaves an empty drawer slot behind in the parent container array
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'removed', grid_row = NULL, grid_col = NULL, updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) ArchiveLocation(id int64) error {
	if id == 1 {
		return fmt.Errorf("cannot archive the Default Room")
	}
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'archived', grid_row = NULL, grid_col = NULL, updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) RestoreLocation(id int64) error {
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'active', updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) ListLocations(showRemoved bool) ([]Location, error) {
	query := `
		SELECT l.id, l.name, l.slug, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.grid_rows, l.grid_cols, l.grid_row, l.grid_col, l.color, l.created_at, l.updated_at
		FROM locations l 
		LEFT JOIN locations p ON l.parent_id = p.id`
	if !showRemoved {
		query += " WHERE l.lifecycle_state != 'removed'"
	}
	query += " ORDER BY CASE l.lifecycle_state WHEN 'archived' THEN 1 WHEN 'removed' THEN 2 ELSE 0 END, l.name ASC"

	rows, err := d.Conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []Location
	for rows.Next() {
		var l Location
		var parentID sql.NullInt64
		var parentName sql.NullString
		var created, updated string
		if err := rows.Scan(&l.ID, &l.Name, &l.Slug, &l.Description, &parentID, &parentName, &l.ImagePath, &l.LifecycleState, &l.GridRows, &l.GridCols, &l.GridRow, &l.GridCol, &l.Color, &created, &updated); err != nil {
			return nil, err
		}
		l.ParentID = parentID
		l.ParentName = parentName
		l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		l.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		locations = append(locations, l)
	}
	return locations, rows.Err()
}

func (d *DB) ListChildLocations(parentID int64, showRemoved bool) ([]Location, error) {
	query := `
		SELECT l.id, l.name, l.slug, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.grid_rows, l.grid_cols, l.grid_row, l.grid_col, l.color, l.created_at, l.updated_at
		FROM locations l 
		LEFT JOIN locations p ON l.parent_id = p.id
		WHERE l.parent_id = ?`
	if !showRemoved {
		query += " AND l.lifecycle_state != 'removed'"
	}
	query += " ORDER BY CASE l.lifecycle_state WHEN 'archived' THEN 1 WHEN 'removed' THEN 2 ELSE 0 END, l.name ASC"

	rows, err := d.Conn.Query(query, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []Location
	for rows.Next() {
		var l Location
		var pID sql.NullInt64
		var pName sql.NullString
		var created, updated string
		if err := rows.Scan(&l.ID, &l.Name, &l.Slug, &l.Description, &pID, &pName, &l.ImagePath, &l.LifecycleState, &l.GridRows, &l.GridCols, &l.GridRow, &l.GridCol, &l.Color, &created, &updated); err != nil {
			return nil, err
		}
		l.ParentID = pID
		l.ParentName = pName
		l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		l.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		locations = append(locations, l)
	}
	return locations, nil
}

func scanLocation(row interface {
	Scan(dest ...any) error
}) (*Location, error) {
	var l Location
	var parentID sql.NullInt64
	var parentName sql.NullString
	var created, updated string
	if err := row.Scan(&l.ID, &l.Name, &l.Slug, &l.Description, &parentID, &parentName, &l.ImagePath, &l.LifecycleState, &l.GridRows, &l.GridCols, &l.GridRow, &l.GridCol, &l.Color, &created, &updated); err != nil {
		return nil, err
	}
	l.ParentID = parentID
	l.ParentName = parentName
	l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	l.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &l, nil
}

func (d *DB) MoveLocationParent(locID, newParentID int64) error {
	var pID sql.NullInt64
	if newParentID > 0 {
		pID = sql.NullInt64{Int64: newParentID, Valid: true}
	}
	// Reparenting pulls a drawer out of its container array grid, freeing the slot
	_, err := d.Conn.Exec("UPDATE locations SET parent_id = ?, grid_row = NULL, grid_col = NULL, updated_at = datetime('now') WHERE id = ?", pID, locID)
	return err
}

