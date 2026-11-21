package db

// ItemShortURL records the external short link (for example one made by
// Glimmer) that an item's QR code points to.
type ItemShortURL struct {
	ItemID     int64
	RemoteSlug string // the link's slug at the external service
	ShortURL   string // what the QR code encodes
	TargetURL  string // where the external link redirects (this server's /s/<slug>)
}

func (d *DB) GetItemShortURL(itemID int64) (*ItemShortURL, error) {
	var s ItemShortURL
	err := d.Conn.QueryRow("SELECT item_id, remote_slug, short_url, target_url FROM item_short_urls WHERE item_id = ?", itemID).
		Scan(&s.ItemID, &s.RemoteSlug, &s.ShortURL, &s.TargetURL)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) SetItemShortURL(s *ItemShortURL) error {
	_, err := d.Conn.Exec(`
		INSERT INTO item_short_urls (item_id, remote_slug, short_url, target_url) VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET remote_slug = excluded.remote_slug, short_url = excluded.short_url,
			target_url = excluded.target_url, updated_at = datetime('now')`,
		s.ItemID, s.RemoteSlug, s.ShortURL, s.TargetURL)
	return err
}

func (d *DB) DeleteItemShortURL(itemID int64) error {
	_, err := d.Conn.Exec("DELETE FROM item_short_urls WHERE item_id = ?", itemID)
	return err
}
