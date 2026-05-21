package server

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io"
	"time"

	"Solis/internal/db"
)

//go:embed all:templates
var templateFS embed.FS

//go:embed all:static
var staticFS embed.FS

var templates *template.Template

var templateMap map[string]*template.Template

var templateFuncs = template.FuncMap{
	"safeJS": func(s string) template.JS {
		return template.JS(s)
	},
	"formatPrice": func(p any) string {
		switch v := p.(type) {
		case float64:
			return fmt.Sprintf("$%.2f", v)
		case sql.NullFloat64:
			if v.Valid {
				return fmt.Sprintf("$%.2f", v.Float64)
			}
		}
		return ""
	},
	"formatDate": func(d any) string {
		if d == nil {
			return ""
		}
		switch v := d.(type) {
		case string:
			if v == "" {
				return ""
			}
			t, err := time.Parse("2006-01-02 15:04:05", v)
			if err == nil {
				return t.Format("Jan 02, 2006")
			}
			t, err = time.Parse("2006-01-02", v)
			if err == nil {
				return t.Format("Jan 02, 2006")
			}
			return v
		case time.Time:
			if v.IsZero() {
				return ""
			}
			return v.Format("Jan 02, 2006")
		case sql.NullString:
			if v.Valid && v.String != "" {
				t, err := time.Parse("2006-01-02", v.String)
				if err == nil {
					return t.Format("Jan 02, 2006")
				}
				return v.String
			}
		}
		return ""
	},
	"dict": func(values ...interface{}) (map[string]interface{}, error) {
		if len(values)%2 != 0 {
			return nil, fmt.Errorf("invalid dict call")
		}
		dict := make(map[string]interface{}, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings")
			}
			dict[key] = values[i+1]
		}
		return dict, nil
	},
	"hasTag": func(itemTags []db.Tag, tagID int64) bool {
		for _, t := range itemTags {
			if t.ID == tagID {
				return true
			}
		}
		return false
	},
}

func initTemplates() {
	templateMap = make(map[string]*template.Template)

	// Pages that use the base layout — each gets its own template set
	// so their {{define "content"}} blocks don't collide.
	layoutPages := []string{
		"items.html",
		"locations.html",
		"tags.html",
		"shortener.html",
		"print_labels.html",
	}

	for _, page := range layoutPages {
		t := template.Must(
			template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/base.html", "templates/"+page),
		)
		templateMap[page] = t
	}

	// Standalone pages (no base layout wrapper)
	templateMap["login.html"] = template.Must(
		template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/login.html"),
	)
}

func renderTemplate(w io.Writer, name string, data any) error {
	t, ok := templateMap[name]
	if !ok {
		return fmt.Errorf("template %q not found", name)
	}
	// Standalone pages render by their own name; layout pages render via base.html
	if name == "login.html" {
		return t.ExecuteTemplate(w, name, data)
	}
	return t.ExecuteTemplate(w, "base.html", data)
}
