// Package i18n is the label catalog every document label goes through. A
// catalog is one flat JSON file per locale (locales/<lang>.json in the module
// root): key → label, with `{name}` placeholders substituted by Tf. Keys that
// start with an underscore are locale metadata (number style, date layout),
// not labels.
//
// English ships. Adding a language is adding a file — the layouts never carry
// a literal label — plus, for a non-Latin script, a UTF-8 font for the PDF
// (see pdf.Options.FontFile).
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// Catalog is one locale's labels.
type Catalog struct {
	locale string
	name   string
	msgs   map[string]string
	// Thousands and Decimal are the locale's digit grouping; DateLayout is
	// its Go time layout for dates.
	Thousands  string
	Decimal    string
	DateLayout string

	mu      sync.Mutex
	missing map[string]bool
}

// Bundle is every catalog the renderer knows.
type Bundle struct {
	catalogs map[string]*Catalog
}

// Load reads every locales/<lang>.json in fsys. `en` must be present.
func Load(fsys fs.FS) (*Bundle, error) {
	entries, err := fs.ReadDir(fsys, "locales")
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}
	b := &Bundle{catalogs: map[string]*Catalog{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(fsys, "locales/"+e.Name())
		if err != nil {
			return nil, err
		}
		var msgs map[string]string
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, fmt.Errorf("locales/%s: %w", e.Name(), err)
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		if declared := msgs["_locale"]; declared != "" && declared != locale {
			return nil, fmt.Errorf("locales/%s declares _locale %q", e.Name(), declared)
		}
		c := &Catalog{
			locale:     locale,
			name:       msgs["_name"],
			msgs:       msgs,
			Thousands:  msgs["_number.thousands"],
			Decimal:    msgs["_number.decimal"],
			DateLayout: msgs["_date.layout"],
			missing:    map[string]bool{},
		}
		if c.Decimal == "" {
			c.Decimal = "."
		}
		if c.DateLayout == "" {
			c.DateLayout = "2006-01-02"
		}
		b.catalogs[locale] = c
	}
	if _, ok := b.catalogs["en"]; !ok {
		return nil, fmt.Errorf("locales/en.json is required")
	}
	return b, nil
}

// Locales lists the loaded locale codes, sorted.
func (b *Bundle) Locales() []string {
	out := make([]string, 0, len(b.catalogs))
	for k := range b.catalogs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Catalog returns the locale's catalog; ok=false when it is not loaded. The
// lookup is case-insensitive and accepts a region suffix ("en-GB" → "en").
func (b *Bundle) Catalog(locale string) (*Catalog, bool) {
	l := strings.ToLower(strings.TrimSpace(locale))
	if c, ok := b.catalogs[l]; ok {
		return c, true
	}
	if base, _, ok := strings.Cut(l, "-"); ok {
		if c, ok := b.catalogs[base]; ok {
			return c, true
		}
	}
	return nil, false
}

// Locale is the catalog's code ("en").
func (c *Catalog) Locale() string { return c.locale }

// T returns the label for key. A missing key returns the key itself — a
// visible, greppable gap rather than a blank — and is recorded for Missing.
func (c *Catalog) T(key string) string {
	if v, ok := c.msgs[key]; ok && !strings.HasPrefix(key, "_") {
		return v
	}
	c.mu.Lock()
	c.missing[key] = true
	c.mu.Unlock()
	return key
}

// Tf is T with `{name}` placeholders replaced from vars.
func (c *Catalog) Tf(key string, vars map[string]string) string {
	s := c.T(key)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// Has reports whether the key is in the catalog.
func (c *Catalog) Has(key string) bool {
	_, ok := c.msgs[key]
	return ok && !strings.HasPrefix(key, "_")
}

// Missing lists every key asked for that the catalog did not have, sorted.
// Tests render every fixture and assert this is empty.
func (c *Catalog) Missing() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.missing))
	for k := range c.missing {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Keys lists the label keys (metadata excluded), sorted.
func (c *Catalog) Keys() []string {
	out := make([]string, 0, len(c.msgs))
	for k := range c.msgs {
		if !strings.HasPrefix(k, "_") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
