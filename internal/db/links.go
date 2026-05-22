package db

import (
	"database/sql"
	"time"
)

type Link struct {
	ID             int64
	Slug           string
	URL            string
	ItemID         sql.NullInt64
	CreatedBy      string
	Clicks         int64
	LifecycleState string // 'active', 'archived', 'removed'
	CreatedAt      time.Time
}

func (d *DB) CreateLink(slug, url, createdBy string, itemID sql.NullInt64) (*Link, error) {
	res, err := d.Conn.Exec(
		"INSERT INTO links (slug, url, created_by, item_id) VALUES (?, ?, ?, ?)",
		slug, url, createdBy, itemID,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Link{
		ID:        id,
		Slug:      slug,
		URL:       url,
		ItemID:    itemID,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
	}, nil
}

func (d *DB) GetLinkBySlug(slug string) (*Link, error) {
	row := d.Conn.QueryRow("SELECT id, slug, url, item_id, created_by, clicks, lifecycle_state, created_at FROM links WHERE slug = ?", slug)
	return scanLink(row)
}

func (d *DB) GetLinkByItemID(itemID int64) (*Link, error) {
	row := d.Conn.QueryRow("SELECT id, slug, url, item_id, created_by, clicks, lifecycle_state, created_at FROM links WHERE item_id = ?", itemID)
	return scanLink(row)
}

func (d *DB) IncrementLinkClicks(slug string) error {
	_, err := d.Conn.Exec("UPDATE links SET clicks = clicks + 1 WHERE slug = ?", slug)
	return err
}

func (d *DB) SoftRemoveLink(id int64) error {
	_, err := d.Conn.Exec("UPDATE links SET lifecycle_state = 'removed' WHERE id = ?", id)
	return err
}

func (d *DB) ArchiveLink(id int64) error {
	_, err := d.Conn.Exec("UPDATE links SET lifecycle_state = 'archived' WHERE id = ?", id)
	return err
}

func (d *DB) RestoreLink(id int64) error {
	_, err := d.Conn.Exec("UPDATE links SET lifecycle_state = 'active' WHERE id = ?", id)
	return err
}

func (d *DB) ListLinks(showRemoved bool) ([]Link, error) {
	query := "SELECT id, slug, url, item_id, created_by, clicks, lifecycle_state, created_at FROM links"
	if !showRemoved {
		query += " WHERE lifecycle_state != 'removed'"
	}
	query += " ORDER BY CASE lifecycle_state WHEN 'archived' THEN 1 WHEN 'removed' THEN 2 ELSE 0 END, id DESC"

	rows, err := d.Conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var l Link
		var itemID sql.NullInt64
		var ts string
		if err := rows.Scan(&l.ID, &l.Slug, &l.URL, &itemID, &l.CreatedBy, &l.Clicks, &l.LifecycleState, &ts); err != nil {
			return nil, err
		}
		l.ItemID = itemID
		l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		links = append(links, l)
	}
	return links, rows.Err()
}

func (d *DB) DeleteLink(id int64) error {
	_, err := d.Conn.Exec("DELETE FROM links WHERE id = ?", id)
	return err
}

func (d *DB) UpdateLink(id int64, slug, url string) error {
	_, err := d.Conn.Exec("UPDATE links SET slug = ?, url = ? WHERE id = ?", slug, url, id)
	return err
}

func (d *DB) SlugExists(slug string) (bool, error) {
	var count int
	err := d.Conn.QueryRow("SELECT COUNT(*) FROM links WHERE slug = ?", slug).Scan(&count)
	return count > 0, err
}

func scanLink(row interface {
	Scan(dest ...any) error
}) (*Link, error) {
	var l Link
	var itemID sql.NullInt64
	var ts string
	if err := row.Scan(&l.ID, &l.Slug, &l.URL, &itemID, &l.CreatedBy, &l.Clicks, &l.LifecycleState, &ts); err != nil {
		return nil, err
	}
	l.ItemID = itemID
	l.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
	return &l, nil
}
