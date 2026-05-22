-- SQLite Database Schema for Solis
-- Enables high-fidelity asset tracking and custom URL shortening

-- 1. Locations Table (Supports hierarchical locations via parent_id)
CREATE TABLE IF NOT EXISTS locations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    parent_id       INTEGER REFERENCES locations(id) ON DELETE SET NULL,
    image_path      TEXT NOT NULL DEFAULT '',
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_locations_parent ON locations(parent_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_locations_slug ON locations(slug);

-- 2. Tags Table (Sleek labels for grouping items)
CREATE TABLE IF NOT EXISTS tags (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    color           TEXT NOT NULL DEFAULT '#e2e8f0', -- Hex code for pill color
    description     TEXT NOT NULL DEFAULT '',
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

-- 3. Items Table (Asset entries with purchase records and JSON custom fields)
CREATE TABLE IF NOT EXISTS items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    quantity        INTEGER NOT NULL DEFAULT 1,
    model_number    TEXT NOT NULL DEFAULT '',
    serial_number   TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'In Use', -- 'In Use', 'In Storage', 'Borrowed', 'Lost', 'Disposed'
    location_id     INTEGER REFERENCES locations(id) ON DELETE SET NULL,
    purchase_price  REAL,                           -- Double/Real for financial values
    purchase_date   TEXT,                           -- ISO8601 date string (YYYY-MM-DD)
    warranty_months INTEGER,
    supplier        TEXT NOT NULL DEFAULT '',
    custom_fields   TEXT NOT NULL DEFAULT '{}',     -- JSON text field containing arbitrary key-value custom metadata
    image_path      TEXT NOT NULL DEFAULT '',
    receipt_path    TEXT NOT NULL DEFAULT '',
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_items_location ON items(location_id);

-- 4. Item Tags Junction Table (Many-to-Many relationship)
CREATE TABLE IF NOT EXISTS item_tags (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, tag_id)
);

-- 5. Links Table (Ported and enhanced from Glimmer)
CREATE TABLE IF NOT EXISTS links (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    slug            TEXT NOT NULL UNIQUE,
    url             TEXT NOT NULL,                       -- Redirect target (internal, e.g., '/items/12', or external)
    item_id         INTEGER REFERENCES items(id) ON DELETE CASCADE, -- Nullable; links specific assets to short URLs
    created_by      TEXT NOT NULL DEFAULT 'admin',
    clicks          INTEGER NOT NULL DEFAULT 0,
    lifecycle_state TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_links_slug ON links(slug);
CREATE INDEX IF NOT EXISTS idx_links_item ON links(item_id);
