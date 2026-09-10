// Package synth generates a deterministic, realistic usage history for
// showcase customers of the chargeback product (EPIC #6867, founder direction
// 2026-09-08): three cloud-layer customers on the National Cloud list and
// three platform-layer Organizations on the "OpenOva plans" book, every one of
// them decommissioned before the real data begins.
//
// The package is pure: it knows nothing about HTTP or Postgres. It turns a
// Scenario into hourly Records and the inventory Resources they imply, and
// offers the arithmetic the seeding command and its tests need (monthly and
// daily cost at a price list, budget crossings, CSV bytes, purge selectors).
//
// Every quantity is a function of (seed, customer, resource, hour) alone —
// never of the order resources are generated in or of the window asked for —
// so two runs with the same seed produce the same bytes, and a re-run over a
// narrower or wider window agrees with the first on every shared hour. That is
// what lets the seeding command upsert in place instead of ever duplicating a
// row.
package synth

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

// Markers that make every synthetic row identifiable (and purgeable).
const (
	// SlugPrefix prefixes every synthetic customer slug, org slug and source
	// name. The product has no free-text note field on a customer, so the
	// slug carries the mark (the founder's fallback rule).
	SlugPrefix = "demo-"
	// NamePrefix prefixes every synthetic discount, campaign and budget name.
	NamePrefix = "demo: "
	// LabelKey / LabelValue are set on every synthetic usage record's labels
	// and on every synthetic inventory row's attrs.
	LabelKey   = "synthetic"
	LabelValue = "true"
)

// Layers of a source (the target model of the concurrent ownership rebuild).
const (
	LayerCloud    = "cloud"
	LayerPlatform = "platform"
)

// Source kinds as the product stores them today.
const (
	SourceKindFile = "file"        // cloud layer, imported usage
	SourceKindOrg  = "openova-org" // platform layer
)

// Regions: the Sovereign's two National Cloud regions.
const (
	RegionA = "me-east-215-a"
	RegionB = "me-east-215-b"
)

// Window is the half-open hour range [From, To) to generate. Both ends are
// whole UTC hours.
type Window struct {
	From, To time.Time
}

// DefaultWindow is the founder's showcase window: 1 June 2026 up to (not
// including) 1 September 2026 00:00 UTC — the real data begins on 2 September
// and must stay untouched.
func DefaultWindow() Window {
	return Window{
		From: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// Validate checks the window is hour-aligned and non-empty.
func (w Window) Validate() error {
	if !w.From.Equal(w.From.Truncate(time.Hour)) || !w.To.Equal(w.To.Truncate(time.Hour)) {
		return fmt.Errorf("window bounds must be whole hours: %s .. %s", w.From.Format(time.RFC3339), w.To.Format(time.RFC3339))
	}
	if !w.To.After(w.From) {
		return fmt.Errorf("window end %s must be after start %s", w.To.Format(time.RFC3339), w.From.Format(time.RFC3339))
	}
	return nil
}

// Hours is the number of hours in the window.
func (w Window) Hours() int { return int(w.To.Sub(w.From) / time.Hour) }

// Months lists the YYYY-MM periods the window touches, ascending.
func (w Window) Months() []string {
	var out []string
	t := time.Date(w.From.Year(), w.From.Month(), 1, 0, 0, 0, 0, time.UTC)
	for t.Before(w.To) {
		out = append(out, t.Format("2006-01"))
		t = t.AddDate(0, 1, 0)
	}
	return out
}

// LastFullDay is the start of the last whole UTC day inside the window — the
// last day the window can be priced against a real day of the same length.
// ok is false when the window holds no whole day.
func (w Window) LastFullDay() (time.Time, bool) {
	day := w.To.UTC().Add(-time.Nanosecond).Truncate(24 * time.Hour)
	for !day.Before(w.From) {
		if !day.Add(24 * time.Hour).After(w.To) {
			return day, true
		}
		day = day.Add(-24 * time.Hour)
	}
	return time.Time{}, false
}

// MonthBounds is [first day, first day of next month) of a YYYY-MM period.
func MonthBounds(period string) (time.Time, time.Time, error) {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("period must be YYYY-MM: %w", err)
	}
	start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0), nil
}

// Record is one hourly usage fact, the shape of a usage_records row.
type Record struct {
	ResourceID   string
	ResourceKind string
	SKU          string
	Unit         string
	Quantity     float64 // rounded to the 6 decimals the ledger stores
	Start        time.Time
	End          time.Time
	Region       string
	Labels       map[string]string
}

// Resource is the inventory row a set of records implies.
type Resource struct {
	ID        string
	Kind      string
	Name      string
	Region    string
	Attrs     map[string]any
	FirstSeen time.Time
	LastSeen  time.Time
	DeletedAt time.Time
}

// Line is one billable meter of a resource for one hour.
type Line struct {
	SKU      string
	Unit     string
	Quantity float64
	// Labels are merged over the resource's labels for this record.
	Labels map[string]string
}

// Jitter returns a deterministic multiplicative factor in [1-pct, 1+pct] for
// the hour and resource it was built for (see Scenario.jitter). Successive
// calls draw successive values from the same stream.
type Jitter func(pct float64) float64

// ResourceSpec describes one synthetic resource and how it meters.
type ResourceSpec struct {
	ID     string
	Kind   string
	Name   string
	Region string
	Attrs  map[string]any
	Labels map[string]string
	// From/To bound the resource's life [From, To). Zero From = the
	// customer's Joined; zero To = the customer's Left.
	From, To time.Time
	// Meter returns the lines for hour t. nil means the resource produced
	// nothing that hour (scaled away, suspended, batch window closed).
	Meter func(t time.Time, j Jitter) []Line
}

// Discount is a percentage or fixed reduction the seeding command creates.
type Discount struct {
	Name     string
	Kind     string // percent | fixed
	Value    float64
	SKU      string // "" = whole bill
	StartsAt time.Time
	EndsAt   time.Time // zero = open-ended
}

// Budget is a monthly cap with alert thresholds.
type Budget struct {
	Name       string
	Amount     float64
	Thresholds []int
}

// Source is the customer's single synthetic source.
type Source struct {
	Name   string // project_id today; the source name in the target model
	Kind   string // file | openova-org
	Layer  string // cloud | platform
	Region string
	Book   string // price book name to assign
}

// Customer is one showcase customer and everything to be created for it.
type Customer struct {
	Slug        string
	Name        string
	AdminEmail  string
	Layer       string
	Kind        string // external | organization
	OrgSlug     string
	BillingMode string
	Currency    string
	PlanSlug    string // the plan in force at the end (platform layer)
	Source      Source
	// Joined/Left bound the customer's usage [Joined, Left).
	Joined, Left time.Time
	// DecommissionNote is written to the customer's audit trail at Left.
	DecommissionNote string
	Discounts        []Discount
	Budgets          []Budget
	Resources        []ResourceSpec
	// PlanSwitches lists the catalog plans in force, ascending by At; the
	// first entry takes effect at Joined. Empty for cloud customers.
	PlanSwitches []PlanSwitch
	// Backfill marks a customer that is NOT a showcase creation but an
	// EXISTING one being given a synthetic past that hands over to a live
	// collection (the landlord backfill, landlord.go). Two things follow:
	// the seeding command never creates, edits, decommissions or bills such a
	// customer, and a resource still metering at the end of the window is
	// left ALIVE in the inventory — it did not go away, the real source took
	// it over.
	Backfill bool
}

// PlanSwitch is a catalog plan taking effect at a time.
type PlanSwitch struct {
	At   time.Time
	Slug string
}

// Scenario is the whole showcase.
type Scenario struct {
	Window          Window
	Seed            uint64
	CloudBookName   string
	PlanBookName    string
	GlobalDiscounts []Discount
	Customers       []*Customer
}

// Customer returns the customer with the slug (with or without the demo-
// prefix), or nil.
func (s *Scenario) Customer(slug string) *Customer {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !strings.HasPrefix(slug, SlugPrefix) {
		slug = SlugPrefix + slug
	}
	for _, c := range s.Customers {
		if c.Slug == slug {
			return c
		}
	}
	return nil
}

// Output is one customer's generated history.
type Output struct {
	Customer  *Customer
	Records   []Record   // ascending by Start, ResourceID, SKU
	Resources []Resource // ascending by Kind, Name, ID
}

// Generate produces the customer's records inside the scenario window and the
// inventory rows they imply.
func (s *Scenario) Generate(c *Customer) Output {
	out := Output{Customer: c}
	from := laterOf(s.Window.From, c.Joined)
	to := earlierOf(s.Window.To, c.Left)
	type rstate struct {
		spec  *ResourceSpec
		first time.Time
		last  time.Time
		seen  bool
	}
	states := make([]*rstate, len(c.Resources))
	for i := range c.Resources {
		states[i] = &rstate{spec: &c.Resources[i]}
	}
	for t := from; t.Before(to); t = t.Add(time.Hour) {
		for _, st := range states {
			r := st.spec
			rf := r.From
			if rf.IsZero() {
				rf = c.Joined
			}
			rt := r.To
			if rt.IsZero() {
				rt = c.Left
			}
			if t.Before(rf) || !t.Before(rt) {
				continue
			}
			lines := r.Meter(t, s.jitter(c.Slug, r.ID, t))
			for _, l := range lines {
				q := Round6(l.Quantity)
				if q <= 0 {
					continue
				}
				labels := map[string]string{LabelKey: LabelValue}
				if r.Name != "" {
					labels["name"] = r.Name
				}
				for k, v := range r.Labels {
					labels[k] = v
				}
				for k, v := range l.Labels {
					labels[k] = v
				}
				out.Records = append(out.Records, Record{
					ResourceID:   r.ID,
					ResourceKind: r.Kind,
					SKU:          l.SKU,
					Unit:         l.Unit,
					Quantity:     q,
					Start:        t,
					End:          t.Add(time.Hour),
					Region:       r.Region,
					Labels:       labels,
				})
				if !st.seen {
					st.first, st.seen = t, true
				}
				st.last = t.Add(time.Hour)
			}
		}
	}
	sort.SliceStable(out.Records, func(i, j int) bool {
		a, b := out.Records[i], out.Records[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		return a.SKU < b.SKU
	})
	for _, st := range states {
		if !st.seen {
			continue
		}
		r := st.spec
		attrs := map[string]any{LabelKey: LabelValue}
		for k, v := range r.Attrs {
			attrs[k] = v
		}
		// Every showcase resource is gone by the end of its customer's life:
		// a resource retired earlier (a bounded To, e.g. the generation a
		// migration replaced) was deleted then; everything else — autoscaled
		// nodes included, they belong to the pool until the end — at
		// decommission.
		deleted := c.Left
		if !r.To.IsZero() && r.To.Before(c.Left) {
			deleted = r.To
		} else if c.Backfill {
			// A backfill hands over to a live collection instead of ending:
			// a resource still metering in the last hour is still there, so
			// marking it deleted at the cut would be a false fact.
			deleted = time.Time{}
		}
		out.Resources = append(out.Resources, Resource{
			ID: r.ID, Kind: r.Kind, Name: r.Name, Region: r.Region, Attrs: attrs,
			FirstSeen: st.first, LastSeen: st.last, DeletedAt: deleted,
		})
	}
	sort.SliceStable(out.Resources, func(i, j int) bool {
		a, b := out.Resources[i], out.Resources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out
}

// Months lists the YYYY-MM periods in which the customer has usage inside the
// scenario window — the periods to run and issue statements for.
func (s *Scenario) Months(c *Customer) []string {
	from := laterOf(s.Window.From, c.Joined)
	to := earlierOf(s.Window.To, c.Left)
	if !to.After(from) {
		return nil
	}
	return Window{From: from, To: to}.Months()
}

// jitter builds the deterministic jitter for (seed, customer, resource, hour):
// a PCG stream keyed by the FNV-1a hash of those four, so a value depends on
// nothing else — not on generation order, not on the window asked for.
func (s *Scenario) jitter(slug, resourceID string, t time.Time) Jitter {
	k := Hash64(s.Seed, slug, resourceID, fmt.Sprint(t.Unix()))
	rng := rand.New(rand.NewPCG(k, k^0x9e3779b97f4a7c15))
	return func(pct float64) float64 {
		if pct <= 0 {
			return 1
		}
		return 1 + (rng.Float64()*2-1)*pct
	}
}

// Hash64 is the stable 64-bit FNV-1a hash the scenario derives identifiers
// and per-day randomness from.
func Hash64(seed uint64, parts ...string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	h.Write(b[:])
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

// Round6 rounds to the 6 decimals usage_records.quantity carries; anything
// not positive and finite becomes 0 (a quantity is never negative).
func Round6(f float64) float64 {
	if f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*1e6) / 1e6
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earlierOf(a, b time.Time) time.Time {
	if b.IsZero() {
		return a
	}
	if b.Before(a) {
		return b
	}
	return a
}
