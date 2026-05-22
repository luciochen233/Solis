package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

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

	// Check if locations count is 0
	var count int
	err = conn.QueryRow("SELECT COUNT(*) FROM locations").Scan(&count)
	if err != nil {
		return fmt.Errorf("checking locations count: %w", err)
	}

	if count == 0 {
		// Create the default room with explicit ID 1
		_, err = conn.Exec("INSERT INTO locations (id, name, description) VALUES (1, 'Default Room', 'Default Room for unassigned items')")
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
