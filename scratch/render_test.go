//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"

	"Solis/internal/config"
	"Solis/internal/db"
)

func main() {
	// 1. Load catalog
	cat, err := config.LoadCatalog("../catalog.json")
	if err != nil {
		fmt.Printf("Error loading catalog: %v\n", err)
		return
	}
	catalogJSON, _ := json.Marshal(cat)

	// 2. Define PageData struct matching internal/server/handlers.go
	type PageData struct {
		CSRFToken        string
		ActiveNav        string
		Toast            string
		ToastType        string
		Error            string
		Items            []db.Item
		Item             *db.Item
		Locations        []db.Location
		Tags             []db.Tag
		Links            []db.Link
		EditingLocation  *db.Location
		EditingTag       *db.Tag
		EditingLink      *db.Link
		EditingItem      *db.Item
		FormMode         bool
		ViewMode         bool
		CustomFieldsRaw  string
		CustomFieldsMap  map[string]string
		SearchQuery      string
		SelectedStatus   string
		SelectedLocation int64
		CatalogJSON      string
	}

	data := PageData{
		FormMode:    true,
		CatalogJSON: string(catalogJSON),
	}

	// 3. Define the template functions matching internal/server/templates.go
	templateFuncs := template.FuncMap{
		"safeJS": func(s string) template.JS {
			return template.JS(s)
		},
		"formatPrice": func(p any) string {
			return ""
		},
		"formatDate": func(d any) string {
			return ""
		},
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			return nil, nil
		},
		"hasTag": func(itemTags []db.Tag, tagID int64) bool {
			return false
		},
	}

	// 4. Parse templates
	t, err := template.New("base.html").Funcs(templateFuncs).ParseFiles(
		"../internal/server/templates/base.html",
		"../internal/server/templates/items.html",
	)
	if err != nil {
		fmt.Printf("Error parsing templates: %v\n", err)
		return
	}

	// 5. Render template
	out, err := os.Create("rendered.html")
	if err != nil {
		fmt.Printf("Error creating output file: %v\n", err)
		return
	}
	defer out.Close()

	err = t.Execute(out, data)
	if err != nil {
		fmt.Printf("Error executing template: %v\n", err)
		return
	}

	fmt.Println("Render completed successfully!")
}
