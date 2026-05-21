package config

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// CatalogCategory represents a high-level inventory category (e.g. Tech, Kitchen)
type CatalogCategory struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	Color string        `json:"color"`
	Items []CatalogItem `json:"items"`
}

// CatalogItem represents a popular item type and its associated popular brands
type CatalogItem struct {
	Name   string   `json:"name"`
	Brands []string `json:"brands"`
}

// LoadCatalog reads a catalog.json file, dynamically strips comments starting with '//' or '#',
// and decodes the cleaned JSON contents into memory.
func LoadCatalog(path string) ([]CatalogCategory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var sb strings.Builder
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		// Ignore comment lines starting with '//' or '#'
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		sb.WriteString(line)
		sb.WriteRune('\n')
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var catalog []CatalogCategory
	if err := json.Unmarshal([]byte(sb.String()), &catalog); err != nil {
		return nil, err
	}

	return catalog, nil
}
