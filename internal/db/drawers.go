package db

import (
	"database/sql"
	"fmt"

	"Solis/internal/slug"
)

const MaxGridSize = 20

// SetLocationGrid turns a location into a container array (or resizes/disables one).
// Shrinking is rejected while drawers occupy cells outside the new bounds.
func (d *DB) SetLocationGrid(id int64, rows, cols int64) error {
	if rows < 0 || cols < 0 || rows > MaxGridSize || cols > MaxGridSize {
		return fmt.Errorf("grid size must be between 0 and %d", MaxGridSize)
	}
	// A container array needs both dimensions; treat a half-set grid as disabled
	if rows == 0 || cols == 0 {
		rows, cols = 0, 0
	}

	var occupied int
	err := d.Conn.QueryRow(
		"SELECT COUNT(*) FROM locations WHERE parent_id = ? AND grid_row IS NOT NULL AND (grid_row >= ? OR grid_col >= ?)",
		id, rows, cols,
	).Scan(&occupied)
	if err != nil {
		return err
	}
	if occupied > 0 {
		return fmt.Errorf("cannot shrink container array: %d drawer(s) sit outside the new size", occupied)
	}

	_, err = d.Conn.Exec("UPDATE locations SET grid_rows = ?, grid_cols = ?, updated_at = datetime('now') WHERE id = ?", rows, cols, id)
	return err
}

// ListDrawers returns the active drawers of a container array with a content summary.
func (d *DB) ListDrawers(arrayID int64) ([]Drawer, error) {
	rows, err := d.Conn.Query(`
		SELECT l.id, l.name, l.slug, l.color, l.grid_row, l.grid_col,
		       COUNT(i.id) AS item_count,
		       COALESCE(GROUP_CONCAT(i.name, ', '), '') AS item_names
		FROM locations l
		LEFT JOIN items i ON i.location_id = l.id AND i.lifecycle_state = 'active'
		WHERE l.parent_id = ? AND l.grid_row IS NOT NULL AND l.grid_col IS NOT NULL AND l.lifecycle_state = 'active'
		GROUP BY l.id
		ORDER BY l.grid_row, l.grid_col`,
		arrayID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var drawers []Drawer
	for rows.Next() {
		var dr Drawer
		if err := rows.Scan(&dr.ID, &dr.Name, &dr.Slug, &dr.Color, &dr.Row, &dr.Col, &dr.ItemCount, &dr.ItemNames); err != nil {
			return nil, err
		}
		drawers = append(drawers, dr)
	}
	return drawers, rows.Err()
}

// CreateDrawer creates a child location occupying a cell of a container array.
func (d *DB) CreateDrawer(arrayID int64, row, col int64, name, color string) (*Location, error) {
	array, err := d.GetLocation(arrayID)
	if err != nil {
		return nil, err
	}
	if array.GridRows == 0 || array.GridCols == 0 {
		return nil, fmt.Errorf("location is not a container array")
	}
	if row < 0 || col < 0 || row >= array.GridRows || col >= array.GridCols {
		return nil, fmt.Errorf("drawer position out of bounds")
	}

	tx, err := d.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var occupied int
	err = tx.QueryRow(
		"SELECT COUNT(*) FROM locations WHERE parent_id = ? AND grid_row = ? AND grid_col = ? AND lifecycle_state = 'active'",
		arrayID, row, col,
	).Scan(&occupied)
	if err != nil {
		return nil, err
	}
	if occupied > 0 {
		return nil, fmt.Errorf("drawer slot is already occupied")
	}

	slugVal := slug.Slugify(name)
	uniqueSlug := slugVal
	index := 1
	for {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM locations WHERE slug = ?", uniqueSlug).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			break
		}
		uniqueSlug = fmt.Sprintf("%s-%d", slugVal, index)
		index++
	}

	res, err := tx.Exec(
		"INSERT INTO locations (name, slug, description, parent_id, grid_row, grid_col, color) VALUES (?, ?, '', ?, ?, ?, ?)",
		name, uniqueSlug, arrayID, row, col, color,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &Location{
		ID:       id,
		Name:     name,
		Slug:     uniqueSlug,
		ParentID: sql.NullInt64{Int64: arrayID, Valid: true},
		GridRow:  sql.NullInt64{Int64: row, Valid: true},
		GridCol:  sql.NullInt64{Int64: col, Valid: true},
		Color:    color,
	}, nil
}

// UpdateDrawer renames and recolors a drawer, optionally moving it to another
// parent location (which frees its slot in the container array).
func (d *DB) UpdateDrawer(id int64, name, color string, parentID sql.NullInt64) error {
	tx, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldParentID sql.NullInt64
	if err := tx.QueryRow("SELECT parent_id FROM locations WHERE id = ?", id).Scan(&oldParentID); err != nil {
		return err
	}

	slugVal := slug.Slugify(name)
	uniqueSlug := slugVal
	index := 1
	for {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM locations WHERE slug = ? AND id != ?", uniqueSlug, id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			break
		}
		uniqueSlug = fmt.Sprintf("%s-%d", slugVal, index)
		index++
	}

	_, err = tx.Exec(
		"UPDATE locations SET name = ?, slug = ?, color = ?, parent_id = ?, updated_at = datetime('now') WHERE id = ?",
		name, uniqueSlug, color, parentID, id,
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

// MoveDrawer moves a drawer to a target cell of its container array. If the
// target cell is occupied, the two drawers swap positions.
func (d *DB) MoveDrawer(drawerID, toRow, toCol int64) error {
	drawer, err := d.GetLocation(drawerID)
	if err != nil {
		return err
	}
	if !drawer.ParentID.Valid || !drawer.GridRow.Valid || !drawer.GridCol.Valid {
		return fmt.Errorf("location is not a drawer in a container array")
	}

	array, err := d.GetLocation(drawer.ParentID.Int64)
	if err != nil {
		return err
	}
	if toRow < 0 || toCol < 0 || toRow >= array.GridRows || toCol >= array.GridCols {
		return fmt.Errorf("drawer position out of bounds")
	}

	tx, err := d.Conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Move any occupant of the target cell into the source cell (swap)
	_, err = tx.Exec(
		"UPDATE locations SET grid_row = ?, grid_col = ?, updated_at = datetime('now') WHERE parent_id = ? AND grid_row = ? AND grid_col = ? AND lifecycle_state = 'active' AND id != ?",
		drawer.GridRow.Int64, drawer.GridCol.Int64, array.ID, toRow, toCol, drawerID,
	)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		"UPDATE locations SET grid_row = ?, grid_col = ?, updated_at = datetime('now') WHERE id = ?",
		toRow, toCol, drawerID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
