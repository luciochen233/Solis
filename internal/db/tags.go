package db

import (
	"fmt"
	"time"
)

type Tag struct {
	ID             int64
	Name           string
	Color          string // Hex representation, e.g. "#3b82f6"
	Description    string
	LifecycleState string // 'active', 'archived', 'removed'
	CreatedAt      time.Time
}

func (d *DB) CreateTag(name, color, description string) (*Tag, error) {
	if color == "" {
		color = "#e2e8f0" // default soft light gray
	}
	res, err := d.Conn.Exec(
		"INSERT INTO tags (name, color, description) VALUES (?, ?, ?)",
		name, color, description,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Tag{
		ID:          id,
		Name:        name,
		Color:       color,
		Description: description,
		CreatedAt:   time.Now(),
	}, nil
}

func (d *DB) GetTag(id int64) (*Tag, error) {
	row := d.Conn.QueryRow("SELECT id, name, color, description, lifecycle_state, created_at FROM tags WHERE id = ?", id)
	return scanTag(row)
}

func (d *DB) GetTagByName(name string) (*Tag, error) {
	row := d.Conn.QueryRow("SELECT id, name, color, description, lifecycle_state, created_at FROM tags WHERE name = ?", name)
	return scanTag(row)
}

func (d *DB) UpdateTag(id int64, name, color, description string) error {
	if color == "" {
		color = "#e2e8f0"
	}

	var oldName string
	err := d.Conn.QueryRow("SELECT name FROM tags WHERE id = ?", id).Scan(&oldName)
	if err == nil && oldName == "Default Room" && name != "Default Room" {
		return fmt.Errorf("cannot rename the Default Room tag")
	}

	_, err = d.Conn.Exec(
		"UPDATE tags SET name = ?, color = ?, description = ? WHERE id = ?",
		name, color, description, id,
	)
	return err
}

func (d *DB) DeleteTag(id int64) error {
	var name string
	err := d.Conn.QueryRow("SELECT name FROM tags WHERE id = ?", id).Scan(&name)
	if err == nil && name == "Default Room" {
		return fmt.Errorf("cannot delete the Default Room tag")
	}

	// Foreign key constraints on item_tags handle cascade delete
	_, err = d.Conn.Exec("DELETE FROM tags WHERE id = ?", id)
	return err
}

func (d *DB) SoftRemoveTag(id int64) error {
	var name string
	err := d.Conn.QueryRow("SELECT name FROM tags WHERE id = ?", id).Scan(&name)
	if err == nil && name == "Default Room" {
		return fmt.Errorf("cannot remove the Default Room tag")
	}
	_, err = d.Conn.Exec("UPDATE tags SET lifecycle_state = 'removed' WHERE id = ?", id)
	return err
}

func (d *DB) ArchiveTag(id int64) error {
	var name string
	err := d.Conn.QueryRow("SELECT name FROM tags WHERE id = ?", id).Scan(&name)
	if err == nil && name == "Default Room" {
		return fmt.Errorf("cannot archive the Default Room tag")
	}
	_, err = d.Conn.Exec("UPDATE tags SET lifecycle_state = 'archived' WHERE id = ?", id)
	return err
}

func (d *DB) RestoreTag(id int64) error {
	_, err := d.Conn.Exec("UPDATE tags SET lifecycle_state = 'active' WHERE id = ?", id)
	return err
}

func (d *DB) ListTags(showRemoved bool) ([]Tag, error) {
	query := "SELECT id, name, color, description, lifecycle_state, created_at FROM tags"
	if !showRemoved {
		query += " WHERE lifecycle_state != 'removed'"
	}
	query += " ORDER BY CASE lifecycle_state WHEN 'archived' THEN 1 WHEN 'removed' THEN 2 ELSE 0 END, name ASC"

	rows, err := d.Conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		var created string
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.LifecycleState, &created); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func scanTag(row interface {
	Scan(dest ...any) error
}) (*Tag, error) {
	var t Tag
	var created string
	if err := row.Scan(&t.ID, &t.Name, &t.Color, &t.Description, &t.LifecycleState, &created); err != nil {
		return nil, err
	}
	t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	return &t, nil
}
