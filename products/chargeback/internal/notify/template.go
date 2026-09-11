package notify

import (
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Templates (DESIGN.md §21.3). One template per (event, LOCALE); each is a
// subject and a body as text/template source over the event's declared
// payload.
//
// The locale key is what makes a second language ADDITIVE: a new locale is
// one new file that calls RegisterLocale with the same event keys, and not
// one line of this file, of the catalogue, of the notifier or of any call
// site changes. Only ENGLISH is shipped (locale_en.go) — by direction; no
// translation is invented here.
//
// Two events — statement.issued and report.scheduled — carry a `document`
// field their template renders whole, because those two documents are built
// by internal/report: a money waterfall, a ranked line table and a
// column-aligned summary are a renderer, not a mail template. Their template
// is therefore `{{.document}}`, and that is a REAL limit, stated rather than
// hidden: an Arabic locale can translate the wrapper around those two and
// translating the documents themselves is a change to internal/report. Every
// other event's prose is in its template, where a translator can reach it.

// DefaultLocale is the locale a recipient with no preference is rendered in,
// and the fallback when a requested locale has no template for an event.
const DefaultLocale = "en"

// Template is one rendered form of one event.
type Template struct {
	// Event is the event key.
	Event string `json:"event"`
	// Locale is the language tag, e.g. "en".
	Locale string `json:"locale"`
	// Subject and Body are text/template sources over the event's payload.
	Subject string `json:"subject"`
	Body    string `json:"body"`

	subject *template.Template
	body    *template.Template
}

// funcs are the only helpers a template may use. Deliberately few: a
// template that can compute is a second place money is formatted.
var funcs = template.FuncMap{
	// plural renders the English plural suffix of a count.
	"plural": func(n int) string {
		if n == 1 || n == -1 {
			return ""
		}
		return "s"
	},
	// abs is |n|, for prose that says "in 3 days" from days = -3.
	"abs": func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	},
}

// registry holds every registered template, event key → locale → template.
var registry = map[string]map[string]*Template{}

// locales is every locale that has registered at least one template.
var locales []string

// RegisterLocale adds one locale's templates. It PANICS on a malformed
// template or an unknown event key: both are programming errors that must
// fail at start-up rather than at the moment a customer was owed a mail. A
// new language is exactly one call to this from one new file.
func RegisterLocale(locale string, templates map[string]Template) {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" {
		panic("notify: RegisterLocale with an empty locale")
	}
	keys := make([]string, 0, len(templates))
	for key := range templates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t := templates[key]
		if _, ok := Lookup(key); !ok {
			panic(fmt.Sprintf("notify: locale %q registers a template for unknown event %q", locale, key))
		}
		t.Event, t.Locale = key, locale
		subject, err := template.New(key + ".subject").Funcs(funcs).Option("missingkey=zero").Parse(t.Subject)
		if err != nil {
			panic(fmt.Sprintf("notify: locale %q, event %q, subject: %v", locale, key, err))
		}
		body, err := template.New(key + ".body").Funcs(funcs).Option("missingkey=zero").Parse(t.Body)
		if err != nil {
			panic(fmt.Sprintf("notify: locale %q, event %q, body: %v", locale, key, err))
		}
		t.subject, t.body = subject, body
		if registry[key] == nil {
			registry[key] = map[string]*Template{}
		}
		registry[key][locale] = &t
	}
	for _, l := range locales {
		if l == locale {
			return
		}
	}
	locales = append(locales, locale)
	sort.Strings(locales)
}

// Locales lists every registered locale, sorted.
func Locales() []string {
	out := make([]string, len(locales))
	copy(out, locales)
	return out
}

// TemplateFor returns the template for an event in a locale, falling back to
// DefaultLocale. The second return is the locale actually used, which is
// what the delivery log records — "we had no Arabic for this one" is a fact
// worth being able to read back.
func TemplateFor(event, locale string) (*Template, string, bool) {
	byLocale := registry[event]
	if len(byLocale) == 0 {
		return nil, "", false
	}
	locale = strings.ToLower(strings.TrimSpace(locale))
	if t, ok := byLocale[locale]; ok {
		return t, locale, true
	}
	if t, ok := byLocale[DefaultLocale]; ok {
		return t, DefaultLocale, true
	}
	return nil, "", false
}

// Message is a rendered notification.
type Message struct {
	Subject string
	Body    string
	Locale  string
}

// Render renders one event for one locale from a payload.
func Render(event, locale string, payload map[string]any) (Message, error) {
	t, used, ok := TemplateFor(event, locale)
	if !ok {
		return Message{}, fmt.Errorf("no template for event %q in any locale", event)
	}
	var subject, body strings.Builder
	if err := t.subject.Execute(&subject, payload); err != nil {
		return Message{}, fmt.Errorf("render %s subject (%s): %w", event, used, err)
	}
	if err := t.body.Execute(&body, payload); err != nil {
		return Message{}, fmt.Errorf("render %s body (%s): %w", event, used, err)
	}
	// A subject is a HEADER: a newline in one is a header injection, and a
	// template that grew a stray line break would put it there. Collapsed
	// rather than rejected — a mail that goes out with a flattened subject
	// beats an invoice that does not go out at all.
	return Message{Subject: collapse(subject.String()), Body: body.String(), Locale: used}, nil
}

// collapse turns every run of whitespace containing a line break into one
// space and trims the ends.
func collapse(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// TemplateDoc is what the console reads: the source of a template, without
// the parsed form.
type TemplateDoc struct {
	Event   string `json:"event"`
	Locale  string `json:"locale"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// TemplateDocs lists every registered template, by event then locale.
func TemplateDocs() []TemplateDoc {
	out := []TemplateDoc{}
	for _, e := range catalogue {
		byLocale := registry[e.Key]
		names := make([]string, 0, len(byLocale))
		for l := range byLocale {
			names = append(names, l)
		}
		sort.Strings(names)
		for _, l := range names {
			t := byLocale[l]
			out = append(out, TemplateDoc{Event: t.Event, Locale: t.Locale, Subject: t.Subject, Body: t.Body})
		}
	}
	return out
}

// referencedFields lists the top-level payload fields a template source
// names, e.g. `{{.due_date}}` → "due_date". It is deliberately a scan of the
// SOURCE rather than a walk of the parse tree: it has one job, which is to
// hold the templates and the catalogue's declared payload together in a
// test, and a scan that over-reports would fail that test loudly rather than
// pass it quietly.
func referencedFields(src string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i+1 < len(src); i++ {
		if src[i] != '.' {
			continue
		}
		// A field reference is preceded by a delimiter, whitespace, '(' or
		// '$' — never by an identifier character, which would make it a
		// method call or a nested field.
		if i > 0 {
			prev := src[i-1]
			if prev == '.' || isIdentByte(prev) {
				continue
			}
		}
		j := i + 1
		for j < len(src) && isIdentByte(src[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		name := src[i+1 : j]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
