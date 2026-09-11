// Package html renders a view.Model through Go html/template: a shared
// layout plus one content template per document kind, under
// docrender/templates/. Every label resolves through the i18n `t` / `tf`
// functions the model carries, so a template never holds a literal label and
// a new language is a new locales/<lang>.json.
//
// The HTML rendition is the print stylesheet of the same document the PDF
// draws — the operator console can show it inline, and it is what a future
// e-invoice attachment embeds as the human-readable rendition.
package html

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"strings"
	"sync"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

// Set is the parsed template set: the layout plus one template per kind.
type Set struct {
	mu   sync.RWMutex
	tmpl map[string]*template.Template
}

// Load parses templates/_layout.html together with each
// templates/<kind>.html from fsys. Every document kind must have one, so a
// missing template is a start-up failure rather than a 500 on the first
// request that needs it.
func Load(fsys fs.FS) (*Set, error) {
	s := &Set{tmpl: map[string]*template.Template{}}
	layout, err := fs.ReadFile(fsys, "templates/_layout.html")
	if err != nil {
		return nil, fmt.Errorf("templates/_layout.html: %w", err)
	}
	for _, kind := range document.Templates {
		body, err := fs.ReadFile(fsys, "templates/"+kind+".html")
		if err != nil {
			return nil, fmt.Errorf("templates/%s.html: %w", kind, err)
		}
		t := template.New(kind).Funcs(funcs(nil))
		if _, err := t.Parse(string(layout)); err != nil {
			return nil, fmt.Errorf("templates/_layout.html: %w", err)
		}
		if _, err := t.Parse(string(body)); err != nil {
			return nil, fmt.Errorf("templates/%s.html: %w", kind, err)
		}
		if t.Lookup("content") == nil {
			return nil, fmt.Errorf(`templates/%s.html does not define {{define "content"}}`, kind)
		}
		s.tmpl[kind] = t
	}
	return s, nil
}

// Kinds lists the parsed document kinds.
func (s *Set) Kinds() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.tmpl))
	for k := range s.tmpl {
		out = append(out, k)
	}
	return out
}

// Render executes the model's template and returns the HTML document.
func (s *Set) Render(m *view.Model) ([]byte, error) {
	s.mu.RLock()
	t, ok := s.tmpl[m.Template]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no template for %q", m.Template)
	}
	// The func map is rebound per render so `t` and `tf` resolve against THIS
	// document's catalog rather than a package-level one.
	bound, err := t.Clone()
	if err != nil {
		return nil, err
	}
	bound = bound.Funcs(funcs(m))
	var buf bytes.Buffer
	if err := bound.ExecuteTemplate(&buf, "layout", m); err != nil {
		return nil, fmt.Errorf("render %s: %w", m.Template, err)
	}
	return buf.Bytes(), nil
}

func funcs(m *view.Model) template.FuncMap {
	return template.FuncMap{
		// t resolves a label key. Templates carry keys, never labels.
		"t": func(key string) string {
			if m == nil {
				return key
			}
			return m.T(key)
		},
		// tf resolves a label key with {placeholder} values, given as
		// alternating name/value arguments.
		"tf": func(key string, kv ...string) string {
			if m == nil {
				return key
			}
			vars := map[string]string{}
			for i := 0; i+1 < len(kv); i += 2 {
				vars[kv[i]] = kv[i+1]
			}
			return m.Tf(key, vars)
		},
		// label resolves one waterfall / meta row's own label.
		"label": func(r view.Row) string {
			if m == nil {
				return r.Key
			}
			return m.Label(r)
		},
		// dict builds the argument map a partial takes.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd argument count")
			}
			out := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is not a string", i)
				}
				out[k] = kv[i+1]
			}
			return out, nil
		},
		// logoURI re-encodes the decoded seller logo as a data URI.
		// html/template's URL filter rejects `data:` on an img src, so the
		// value is typed template.URL — safe because the bytes came from
		// document.DecodeLogo, which admits only PNG and JPEG.
		"logoURI": func(l *view.Logo) template.URL {
			if l == nil {
				return ""
			}
			return template.URL("data:image/" + l.Format + ";base64," + base64.StdEncoding.EncodeToString(l.Bytes))
		},
		"upper": strings.ToUpper,
	}
}
