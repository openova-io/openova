package consolemenu

import (
	"strings"
	"testing"
)

// TestPages_TheListIsValid checks every line of the list of pages, so a
// task that adds a console page with a bad name, label, route or order
// fails here.
func TestPages_TheListIsValid(t *testing.T) {
	if problems := CheckPages(Pages()); len(problems) != 0 {
		t.Fatalf("the list of console pages is not valid: %v", problems)
	}
}

func TestCheckPages_RefusesEachRule(t *testing.T) {
	good := Page{Name: "class-database", Label: "Databases", Route: "/cloud?class=database", Order: 30, Icon: "M4 6h16"}
	if problems := CheckPages([]Page{good}); len(problems) != 0 {
		t.Fatalf("a good page was refused: %v", problems)
	}
	with := func(change func(p *Page)) []Page {
		p := good
		change(&p)
		return []Page{p}
	}
	cases := []struct {
		name string
		list []Page
		want string
	}{
		{"empty name", with(func(p *Page) { p.Name = "" }), ".name: lower-case letters"},
		{"upper-case name", with(func(p *Page) { p.Name = "Class-Database" }), ".name: lower-case letters"},
		{"name with a dot", with(func(p *Page) { p.Name = "class.database" }), ".name: lower-case letters"},
		{"name that ends in a hyphen", with(func(p *Page) { p.Name = "class-" }), ".name: lower-case letters"},
		{"name of 64 characters", with(func(p *Page) { p.Name = strings.Repeat("a", 64) }), ".name: lower-case letters"},
		{"duplicate name", []Page{good, good}, ".name: duplicate name"},
		{"empty label", with(func(p *Page) { p.Label = "  " }), ".label: required"},
		{"label of 41 characters", with(func(p *Page) { p.Label = strings.Repeat("x", 41) }), ".label: at most 40 characters"},
		{"route without a slash", with(func(p *Page) { p.Route = "cloud" }), ".route: a console path"},
		{"route with two slashes", with(func(p *Page) { p.Route = "//evil.example" }), ".route: a console path"},
		{"https route", with(func(p *Page) { p.Route = "https://grafana.example" }), ".route: a console path"},
		{"route with a space", with(func(p *Page) { p.Route = "/cloud x" }), ".route: a console path"},
		{"order below 0", with(func(p *Page) { p.Order = -1 }), ".order: must be between 0 and 100"},
		{"order above 100", with(func(p *Page) { p.Order = 101 }), ".order: must be between 0 and 100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := CheckPages(c.list)
			for _, p := range problems {
				if strings.Contains(p, c.want) {
					return
				}
			}
			t.Fatalf("no problem holds %q; got %v", c.want, problems)
		})
	}
}

func TestPages_ReturnsASortedCopy(t *testing.T) {
	saved := pages
	t.Cleanup(func() { pages = saved })
	pages = []Page{{Name: "zeta", Label: "Zeta", Route: "/z"}, {Name: "alpha", Label: "Alpha", Route: "/a"}}
	got := Pages()
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Name != "zeta" {
		t.Fatalf("Pages() = %+v, want alpha then zeta", got)
	}
	got[0].Label = "changed"
	if pages[1].Label != "Alpha" {
		t.Fatalf("changing the result of Pages() changed the list")
	}
}

func TestPageEntries_AreOffByDefault(t *testing.T) {
	got := PageEntries([]Page{{Name: "class-database", Label: "Databases", Route: "/cloud?class=database", Order: 30, Icon: "M4 6h16"}})
	want := Entry{
		ID: "page:class-database", Label: "Databases", Route: "/cloud?class=database", Order: 30, Icon: "M4 6h16",
		Source: "page", Enabled: false,
		DefaultLabel: "Databases", DefaultRoute: "/cloud?class=database", DefaultOrder: 30, DefaultEnabled: false,
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("PageEntries = %+v, want [%+v]", got, want)
	}
	if n := len(PageEntries(nil)); n != 0 {
		t.Fatalf("no page gives no entry, got %d", n)
	}
}

func TestHasPage(t *testing.T) {
	list := []Page{{Name: "class-database"}}
	for _, c := range []struct {
		id   string
		want bool
	}{
		{"page:class-database", true},
		{"page:class-network", false},
		{"page:", false},
		{"class-database", false},
		{"app:class-database", false},
	} {
		if got := HasPage(list, c.id); got != c.want {
			t.Fatalf("HasPage(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
