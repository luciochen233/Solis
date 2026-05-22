package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"Solis/internal/slug"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	Conn *sql.DB
}

func Open(path string) (*DB, error) {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	// Open CGO-free SQLite connection with high-concurrency WAL mode and busy timeout
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000&_fk=true")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite operates best in Go with max open connections limited to 1, avoiding concurrent write lockouts
	conn.SetMaxOpenConns(1)

	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}

	return &DB{Conn: conn}, nil
}

func migrate(conn *sql.DB) error {
	// Execute schema migrations
	_, err := conn.Exec(schemaSQL)
	if err != nil {
		return fmt.Errorf("running schema migrations: %w", err)
	}

	// Ensure lifecycle_state column exists in items, locations, tags, links (for existing databases)
	for _, tableName := range []string{"items", "locations", "tags", "links"} {
		rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
		if err != nil {
			return fmt.Errorf("pragma table_info for %s: %w", tableName, err)
		}
		
		hasLifecycleState := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("scanning table_info for %s: %w", tableName, err)
			}
			if name == "lifecycle_state" {
				hasLifecycleState = true
			}
		}
		rows.Close()

		if !hasLifecycleState {
			alterQuery := fmt.Sprintf("ALTER TABLE %s ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active'", tableName)
			if _, err := conn.Exec(alterQuery); err != nil {
				return fmt.Errorf("adding lifecycle_state column to %s: %w", tableName, err)
			}
		}
	}

	// Ensure slug column exists in locations table (for existing databases)
	{
		rows, err := conn.Query("PRAGMA table_info(locations)")
		if err != nil {
			return fmt.Errorf("pragma table_info for locations: %w", err)
		}
		hasSlug := false
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("scanning table_info for locations: %w", err)
			}
			if name == "slug" {
				hasSlug = true
			}
		}
		rows.Close()

		if !hasSlug {
			if _, err := conn.Exec("ALTER TABLE locations ADD COLUMN slug TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("adding slug column to locations: %w", err)
			}
			if _, err := conn.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_slug ON locations(slug)"); err != nil {
				return fmt.Errorf("creating index idx_locations_slug: %w", err)
			}
		}
	}

	// Populate empty location slugs
	{
		rows, err := conn.Query("SELECT id, name FROM locations WHERE slug = '' OR slug IS NULL")
		if err != nil {
			return fmt.Errorf("querying locations with empty slugs: %w", err)
		}
		type locSlugUpdate struct {
			id   int64
			name string
		}
		var updates []locSlugUpdate
		for rows.Next() {
			var u locSlugUpdate
			if err := rows.Scan(&u.id, &u.name); err == nil {
				updates = append(updates, u)
			}
		}
		rows.Close()

		for _, u := range updates {
			slugVal := slug.Slugify(u.name)
			uniqueSlug := slugVal
			index := 1
			for {
				var count int
				err := conn.QueryRow("SELECT COUNT(*) FROM locations WHERE slug = ? AND id != ?", uniqueSlug, u.id).Scan(&count)
				if err != nil {
					return fmt.Errorf("checking slug uniqueness in migration: %w", err)
				}
				if count == 0 {
					break
				}
				uniqueSlug = fmt.Sprintf("%s-%d", slugVal, index)
				index++
			}
			_, err = conn.Exec("UPDATE locations SET slug = ? WHERE id = ?", uniqueSlug, u.id)
			if err != nil {
				return fmt.Errorf("updating location slug in migration: %w", err)
			}
		}
	}

	// Upgrade existing shortlinks with target `/admin?view=X` to `/{location_slug}/{item_slug}`
	{
		linkRows, err := conn.Query("SELECT id, url, item_id, slug FROM links WHERE url LIKE '/admin?view=%' AND item_id IS NOT NULL")
		if err == nil {
			type linkUpgrade struct {
				id     int64
				slug   string
				itemID int64
			}
			var linkUpdates []linkUpgrade
			for linkRows.Next() {
				var l linkUpgrade
				var rawURL string
				if err := linkRows.Scan(&l.id, &rawURL, &l.itemID, &l.slug); err == nil {
					linkUpdates = append(linkUpdates, l)
				}
			}
			linkRows.Close()

			for _, l := range linkUpdates {
				var locSlug string
				err = conn.QueryRow(`
					SELECT COALESCE(l.slug, '') 
					FROM items i 
					LEFT JOIN locations l ON i.location_id = l.id 
					WHERE i.id = ?`, l.itemID).Scan(&locSlug)
				if err != nil || locSlug == "" {
					locSlug = "default-room"
				}
				newURL := fmt.Sprintf("/%s/%s", locSlug, l.slug)
				_, _ = conn.Exec("UPDATE links SET url = ? WHERE id = ?", newURL, l.id)
			}
		}
	}

	// Check if locations count is 0
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM locations").Scan(&count)
	if err != nil {
		return fmt.Errorf("checking locations count: %w", err)
	}

	if count == 0 {
		// Create the default room with explicit ID 1 and slug 'default-room'
		_, err = conn.Exec("INSERT INTO locations (id, name, slug, description) VALUES (1, 'Default Room', 'default-room', 'Default Room for unassigned items')")
		if err != nil {
			return fmt.Errorf("inserting default room: %w", err)
		}

		// Create the corresponding tag for Default Room
		_, err = conn.Exec("INSERT INTO tags (name, color, description) VALUES ('Default Room', '#f59e0b', 'Default tag for items in Default Room')")
		if err != nil {
			return fmt.Errorf("inserting default room tag: %w", err)
		}
	}

	return nil
}

func (d *DB) Close() error {
	return d.Conn.Close()
}
