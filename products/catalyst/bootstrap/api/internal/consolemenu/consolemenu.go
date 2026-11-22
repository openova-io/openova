// Package consolemenu holds what the candidates of the Sovereign console's
// left menu share: the wire shape of one candidate (Entry), the names of the
// three sources a candidate comes from, the limits of a label and an order,
// and the list of console pages that are candidates of the source page.
//
// Settings, Menu lists every candidate. The sovereign-admin switches a
// candidate on, renames it, gives it a route and an order, and nests it
// under a fixed menu item; internal/handler/console_ui.go merges that stored
// choice over the candidates. The three sources:
//
//   - blueprint: a Blueprint with a spec.consoleUI block;
//   - application: an installed Application whose Blueprint has a user
//     interface;
//   - page: a console page that no Blueprint delivers, written in the list
//     below. Its identifier is page:<name>. It is off until the
//     sovereign-admin switches it on, and an Organization console never
//     shows it.
//
// The list holds one class view of Resources per Blueprint class (SVC-180):
// Resources filtered to the installed Blueprints of that class. A task that
// adds a console page which no Blueprint delivers adds one line to it;
// TestPages_TheListIsValid checks
// every line.
package consolemenu

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Entry is the wire shape of one merged menu entry, as
// GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries returns it.
//
// The first five fields are the contract the console's left menu already
// consumes ({id,label,route,order,icon}); the rest are the mapping layer of
// Settings → Menu. Default* echo the un-overridden values so its table can
// show provenance and offer a per-row reset without a second request.
type Entry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Route string `json:"route"`
	Order int    `json:"order"`
	Icon  string `json:"icon,omitempty"`

	// Source — SourceBlueprint, SourceApplication or SourcePage.
	Source string `json:"source"`
	// Enabled — false entries are candidates the console does not render.
	Enabled bool `json:"enabled"`
	// Parent — a fixed menu id when the entry renders as a sub-item under
	// that item; empty = top-level.
	Parent string `json:"parent,omitempty"`
	// Overridden — true when a stored override touched this entry.
	Overridden bool `json:"overridden"`

	DefaultLabel   string `json:"defaultLabel"`
	DefaultRoute   string `json:"defaultRoute"`
	DefaultOrder   int    `json:"defaultOrder"`
	DefaultEnabled bool   `json:"defaultEnabled"`
}

// The sources of a candidate, and the prefix of a page identifier.
const (
	SourceBlueprint   = "blueprint"
	SourceApplication = "application"
	SourcePage        = "page"

	PageIDPrefix = "page:"
)

// The limits of a label and an order, for a page of the list and for a
// stored override alike.
const (
	LabelMaxRunes = 40
	OrderMin      = 0
	OrderMax      = 100
)

// Page is one console page that is a menu candidate of the source page.
type Page struct {
	// Name is the <name> of the identifier page:<name>: lower-case letters,
	// digits and hyphens, a letter or a digit at each end, at most 63
	// characters.
	Name string
	// Label is the text of the menu entry, at most 40 characters.
	Label string
	// Route is a console path: it starts with one "/".
	Route string
	// Order places the entry: 0 first, 100 last.
	Order int
	// Icon is the SVG path the menu draws, in the form of
	// spec.consoleUI.sidebarIcon. Empty draws the default icon.
	Icon string
}

// pages is the list of console pages that are menu candidates.
//
// The class views: identifier page:class-<class>, the plural label and the
// Resources address of resourcesClassRoute in the console's
// src/shared/constants/classes.ts (its test reads these lines and fails when
// a label or a route differs), in the console's class order.
var pages = []Page{
	{Name: "class-application", Label: "Applications", Route: "/cloud?view=list&kind=helmreleases&class=application", Order: 60},
	{Name: "class-data-service", Label: "Data services", Route: "/cloud?view=list&kind=helmreleases&class=data-service", Order: 61},
	{Name: "class-network-security-service", Label: "Network and security services", Route: "/cloud?view=list&kind=helmreleases&class=network-security-service", Order: 62},
	{Name: "class-ai-service", Label: "AI services", Route: "/cloud?view=list&kind=helmreleases&class=ai-service", Order: 63},
	{Name: "class-infrastructure", Label: "Infrastructure", Route: "/cloud?view=list&kind=helmreleases&class=infrastructure", Order: 64},
	{Name: "class-platform-component", Label: "Platform components", Route: "/cloud?view=list&kind=helmreleases&class=platform-component", Order: 65},
}

// Pages returns a copy of the list, sorted by name.
func Pages() []Page {
	out := append([]Page(nil), pages...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PageEntries projects each page of list as a candidate that is off by
// default, in the order of list.
func PageEntries(list []Page) []Entry {
	out := make([]Entry, 0, len(list))
	for _, p := range list {
		out = append(out, Entry{
			ID:             PageIDPrefix + p.Name,
			Label:          p.Label,
			Route:          p.Route,
			Order:          p.Order,
			Icon:           p.Icon,
			Source:         SourcePage,
			Enabled:        false,
			DefaultLabel:   p.Label,
			DefaultRoute:   p.Route,
			DefaultOrder:   p.Order,
			DefaultEnabled: false,
		})
	}
	return out
}

// HasPage reports whether id is page:<name> of a page in list.
func HasPage(list []Page, id string) bool {
	name, ok := strings.CutPrefix(id, PageIDPrefix)
	if !ok {
		return false
	}
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

var pageNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CheckPages returns one line per problem of list; an empty result means
// the list is valid.
func CheckPages(list []Page) []string {
	var problems []string
	seen := make(map[string]struct{}, len(list))
	for i, p := range list {
		at := fmt.Sprintf("pages[%d](%s)", i, p.Name)
		if !pageNameRe.MatchString(p.Name) {
			problems = append(problems, at+".name: lower-case letters, digits and hyphens, a letter or a digit at each end, at most 63 characters")
		}
		if _, dup := seen[p.Name]; dup {
			problems = append(problems, at+".name: duplicate name")
		}
		seen[p.Name] = struct{}{}
		label := strings.TrimSpace(p.Label)
		if label == "" {
			problems = append(problems, at+".label: required")
		} else if n := utf8.RuneCountInString(label); n > LabelMaxRunes {
			problems = append(problems, fmt.Sprintf("%s.label: at most %d characters (got %d)", at, LabelMaxRunes, n))
		}
		if !strings.HasPrefix(p.Route, "/") || strings.HasPrefix(p.Route, "//") || strings.IndexFunc(p.Route, unicode.IsSpace) >= 0 {
			problems = append(problems, at+".route: a console path that starts with one / and holds no space")
		}
		if p.Order < OrderMin || p.Order > OrderMax {
			problems = append(problems, fmt.Sprintf("%s.order: must be between %d and %d (got %d)", at, OrderMin, OrderMax, p.Order))
		}
	}
	return problems
}
