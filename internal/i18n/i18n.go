package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed locales
var localesFS embed.FS

type Translator struct {
	lang     string
	messages map[string]string
	mu       sync.RWMutex
}

func New(lang string) (*Translator, error) {
	if lang == "" {
		lang = "en"
	}

	t := &Translator{
		lang:     lang,
		messages: make(map[string]string),
	}

	data, err := localesFS.ReadFile(fmt.Sprintf("locales/%s.json", lang))
	if err != nil {
		return nil, fmt.Errorf("loading locale %s: %w", lang, err)
	}

	if err := json.Unmarshal(data, &t.messages); err != nil {
		return nil, fmt.Errorf("parsing locale %s: %w", lang, err)
	}

	return t, nil
}

func (t *Translator) T(key string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if msg, ok := t.messages[key]; ok {
		return msg
	}
	return key
}

func (t *Translator) TF(key string, args ...string) string {
	msg := t.T(key)
	for _, arg := range args {
		msg = strings.Replace(msg, "%s", arg, 1)
	}
	return msg
}

func (t *Translator) Lang() string {
	return t.lang
}
