package db

import (
	"database/sql"
	"time"
)

// FoundReport is one "I found this item" report submitted from an item's
// public page. Everything the finder's request told us is stored as-is so the
// owner can follow up; nothing here is shown to other guests.
type FoundReport struct {
	ID          int64
	ItemID      sql.NullInt64 // NULL once the item has been permanently deleted
	ItemName    string        // snapshot, so the report still reads well later
	Slug        string
	IP          string
	IPCountry   string
	IPRegion    string
	IPCity      string
	IPLat       sql.NullFloat64 // coarse location derived from the IP address
	IPLon       sql.NullFloat64
	GPSLat      sql.NullFloat64 // precise location, only if the finder shared it
	GPSLon      sql.NullFloat64
	GPSAccuracy sql.NullFloat64 // metres
	Phone       string          // only if the finder entered one
	Email       string          // only if the finder entered one
	Message     string          // free text from the finder, e.g. where to meet
	UserAgent   string
	Emailed     bool
	CreatedAt   time.Time
}

func (d *DB) CreateFoundReport(r *FoundReport) (int64, error) {
	res, err := d.Conn.Exec(`
		INSERT INTO found_reports (
			item_id, item_name, slug, ip, ip_country, ip_region, ip_city, ip_lat, ip_lon,
			gps_lat, gps_lon, gps_accuracy, phone, email, message, user_agent
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ItemID, r.ItemName, r.Slug, r.IP, r.IPCountry, r.IPRegion, r.IPCity, r.IPLat, r.IPLon,
		r.GPSLat, r.GPSLon, r.GPSAccuracy, r.Phone, r.Email, r.Message, r.UserAgent,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFoundReports returns the most recent reports, newest first.
func (d *DB) ListFoundReports(limit int) ([]FoundReport, error) {
	rows, err := d.Conn.Query(`
		SELECT id, item_id, item_name, slug, ip, ip_country, ip_region, ip_city, ip_lat, ip_lon,
		       gps_lat, gps_lon, gps_accuracy, phone, email, message, user_agent, emailed, created_at
		FROM found_reports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFoundReports(rows)
}

func scanFoundReports(rows *sql.Rows) ([]FoundReport, error) {
	var out []FoundReport
	for rows.Next() {
		var r FoundReport
		var emailed int
		var created string
		if err := rows.Scan(&r.ID, &r.ItemID, &r.ItemName, &r.Slug, &r.IP, &r.IPCountry, &r.IPRegion, &r.IPCity,
			&r.IPLat, &r.IPLon, &r.GPSLat, &r.GPSLon, &r.GPSAccuracy, &r.Phone, &r.Email, &r.Message, &r.UserAgent, &emailed, &created); err != nil {
			return nil, err
		}
		r.Emailed = emailed != 0
		r.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListUnsentFoundReports returns reports created after since that have not
// been emailed yet, oldest first.
func (d *DB) ListUnsentFoundReports(since time.Time) ([]FoundReport, error) {
	rows, err := d.Conn.Query(`
		SELECT id, item_id, item_name, slug, ip, ip_country, ip_region, ip_city, ip_lat, ip_lon,
		       gps_lat, gps_lon, gps_accuracy, phone, email, message, user_agent, emailed, created_at
		FROM found_reports WHERE emailed = 0 AND created_at >= ? ORDER BY id ASC LIMIT 500`,
		since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFoundReports(rows)
}

func (d *DB) MarkFoundReportEmailed(id int64) error {
	_, err := d.Conn.Exec("UPDATE found_reports SET emailed = 1 WHERE id = ?", id)
	return err
}

func (d *DB) DeleteFoundReport(id int64) error {
	_, err := d.Conn.Exec("DELETE FROM found_reports WHERE id = ?", id)
	return err
}
