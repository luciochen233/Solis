package db

import (
	"database/sql"
	"fmt"
	"time"
)

type Location struct {
	ID             int64
	Name           string
	Description    string
	ParentID       sql.NullInt64
	ParentName     sql.NullString // Resolved parent location name
	ImagePath      string
	LifecycleState string // 'active', 'archived', 'removed'
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (d *DB) CreateLocation(name, description string, parentID sql.NullInt64, imagePath string) (*Location, error) {
	tx, err := d.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"INSERT INTO locations (name, description, parent_id, image_path) VALUES (?, ?, ?, ?)",
		name, description, parentID, imagePath,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	// Automatically create a corresponding tag for the new location if it doesn't exist
	var tagID int64
	err = tx.QueryRow("SELECT id FROM tags WHERE name = ?", name).Scan(&tagID)
	if err != nil {
		_, err = tx.Exec(
			"INSERT INTO tags (name, color, description) VALUES (?, '#f59e0b', ?)",
			name, "Tag representing location: "+name,
		)
		if err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Location{
		ID:          id,
		Name:        name,
		Description: description,
		ParentID:    parentID,
		ImagePath:   imagePath,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}, nil
}

func (d *DB) GetLocation(id int64) (*Location, error) {
	row := d.Conn.QueryRow(`
		SELECT l.id, l.name, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.created_at, l.updated_at 
		FROM locations l 
		LEFT JOIN locations p ON l.parent_id = p.id 
		WHERE l.id = ?`,
		id,
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
	err = tx.QueryRow("SELECT name FROM locations WHERE id = ?", id).Scan(&oldName)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		"UPDATE locations SET name = ?, description = ?, parent_id = ?, image_path = ?, updated_at = datetime('now') WHERE id = ?",
		name, description, parentID, imagePath, id,
	)
	if err != nil {
		return err
	}

	// Update the tag name as well
	_, _ = tx.Exec("UPDATE tags SET name = ? WHERE name = ?", name, oldName)

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

	// Get location name to delete the tag as well
	var name string
	err = tx.QueryRow("SELECT name FROM locations WHERE id = ?", id).Scan(&name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // already deleted
		}
		return err
	}

	// Move items in this location to Default Room (ID = 1)
	_, err = tx.Exec("UPDATE items SET location_id = 1 WHERE location_id = ?", id)
	if err != nil {
		return err
	}

	// Get the Default Room tag ID
	var defaultTagID int64
	_ = tx.QueryRow("SELECT id FROM tags WHERE name = 'Default Room'").Scan(&defaultTagID)

	if defaultTagID > 0 {
		// Fetch all items that were just moved to Default Room (ID = 1) and insert Default Room tag reference
		rows, err := tx.Query("SELECT id FROM items WHERE location_id = 1")
		if err == nil {
			var movedItemIDs []int64
			for rows.Next() {
				var mid int64
				if err := rows.Scan(&mid); err == nil {
					movedItemIDs = append(movedItemIDs, mid)
				}
			}
			rows.Close()
			for _, mid := range movedItemIDs {
				_, _ = tx.Exec("INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)", mid, defaultTagID)
			}
		}
	}

	// Delete the tag representing this location
	var tagID int64
	err = tx.QueryRow("SELECT id FROM tags WHERE name = ?", name).Scan(&tagID)
	if err == nil && name != "Default Room" {
		_, _ = tx.Exec("DELETE FROM tags WHERE id = ?", tagID)
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
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'removed', updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) ArchiveLocation(id int64) error {
	if id == 1 {
		return fmt.Errorf("cannot archive the Default Room")
	}
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'archived', updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) RestoreLocation(id int64) error {
	_, err := d.Conn.Exec("UPDATE locations SET lifecycle_state = 'active', updated_at = datetime('now') WHERE id = ?", id)
	return err
}

func (d *DB) ListLocations(showRemoved bool) ([]Location, error) {
	query := `
		SELECT l.id, l.name, l.description, l.parent_id, p.name AS parent_name, l.image_path, l.lifecycle_state, l.created_at, l.updated_at 
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
		if err := rows.Scan(&l.ID, &l.Name, &l.Description, &parentID, &parentName, &l.ImagePath, &l.LifecycleState, &created, &updated); err != nil {
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

func scanLocation(row interface {
	Scan(dest ...any) error
}) (*Location, error) {
	var l Location
	var parentID sql.NullInt64
	var parentName sql.NullString
	var created, updated string
	if err := row.Scan(&l.ID, &l.Name, &l.Description, &parentID, &parentName, &l.ImagePath, &l.LifecycleState, &created, &updated); err != nil {
		return nil, err
	}
	l.ParentID = parentID
	l.ParentName = parentName
	l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	l.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &l, nil
}
