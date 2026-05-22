package slug

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
)

func Slugify(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	// Remove non-alphanumeric chars (except - and _)
	reg := regexp.MustCompile(`[^a-z0-9_-]+`)
	name = reg.ReplaceAllString(name, "")
	// Collapse multiple hyphens
	reg2 := regexp.MustCompile(`-+`)
	name = reg2.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		name = "location"
	}
	return name
}

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

var reservedSlugs = map[string]bool{
	"admin":     true,
	"static":    true,
	"api":       true,
	"health":    true,
	"items":     true,
	"locations": true,
	"tags":      true,
	"s":         true,
	"uploads":   true,
	"login":     true,
	"logout":    true,
}

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func Generate(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

func Validate(slug string) error {
	if len(slug) == 0 {
		return fmt.Errorf("slug cannot be empty")
	}
	if len(slug) > 64 {
		return fmt.Errorf("slug too long (max 64 characters)")
	}
	if !validSlugRe.MatchString(slug) {
		return fmt.Errorf("slug can only contain letters, numbers, hyphens, and underscores")
	}
	if reservedSlugs[strings.ToLower(slug)] {
		return fmt.Errorf("slug %q is reserved", slug)
	}
	return nil
}

func GenerateItemSlug(itemID int64, length int, dbConn *sql.DB) (string, error) {
	if length <= 0 {
		length = 8
	}

	baseInput := fmt.Sprintf("item%d", itemID)
	for {
		h := sha256.New()
		h.Write([]byte(baseInput))
		hashBytes := h.Sum(nil)
		hashHex := hex.EncodeToString(hashBytes)

		slugStr := hashHex
		if length < len(hashHex) {
			slugStr = hashHex[:length]
		}

		if reservedSlugs[strings.ToLower(slugStr)] {
			baseInput = fmt.Sprintf("item%d-%d", itemID, rand.IntN(900000)+100000)
			continue
		}

		var exists int
		err := dbConn.QueryRow("SELECT COUNT(*) FROM links WHERE slug = ?", slugStr).Scan(&exists)
		if err != nil {
			return "", err
		}

		if exists == 0 {
			return slugStr, nil
		}

		baseInput = fmt.Sprintf("item%d-%d", itemID, rand.IntN(900000)+100000)
	}
}
