package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
)

// What is RUNNING on a pool, resource by resource (DESIGN.md §11).
//
// The overview says how much of a pool each class holds. This says WHICH
// resources those are, which class each one counts at and why — and, when the
// pool holds more spot than the room left for it, which of them to reclaim.
//
// It is the same read as the overview (the same zone lookup, the same
// placement match, the same class resolution — resolvePlacements), one row
// per resource instead of one per SKU, so a resource listed here is one the
// overview counted here.

// CapacityRunningResource is one resource running on a pool now.
type CapacityRunningResource struct {
	SourceID   string `json:"source_id"`
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	SKU        string `json:"sku"`
	// Via is the placement the resource arrived through: its SKU, or the
	// family that took it.
	Via   string  `json:"via"`
	Units Decimal `json:"units"`
	Unit  string  `json:"unit"`
	// Class is what it counts at; ClassSource is override / tag / default.
	Class       string `json:"class"`
	ClassSource string `json:"class_source"`
	// OverrideClass and TagClass are what was SAID, whether or not it was
	// followed; Asked is set when one of them named a class the SKU is not
	// placed at here.
	OverrideClass string `json:"override_class"`
	TagClass      string `json:"tag_class"`
	Asked         string `json:"asked"`
	// PlacedClasses is what the operator may choose between for this
	// resource: the classes its SKU is placed at on this pool's zone.
	PlacedClasses []string `json:"placed_classes"`
	// Consumes is units × the SKU's shape: what this one resource holds.
	Consumes  map[string]Decimal `json:"consumes"`
	FirstSeen *time.Time         `json:"first_seen"`
	// SharedWith names the OTHER pools the same SKU sells out of at this
	// class. BSS does not know which machine a resource landed on, so a
	// resource listed under two pools is on one of them.
	SharedWith []string `json:"shared_with"`
	// Reclaim is true when this resource is on the reclaim list below.
	Reclaim bool `json:"reclaim"`
}

// CapacityReclaim is how much spot one resource kind must give back, and how
// much the listed resources cover.
type CapacityReclaim struct {
	Resource string  `json:"resource"`
	Label    string  `json:"label"`
	Unit     string  `json:"unit"`
	Needed   Decimal `json:"needed"`
	Covered  Decimal `json:"covered"`
	// Resources is how many running resources were marked for it.
	Resources int `json:"resources"`
	// Short is true when every spot resource on the pool together does not
	// cover what is needed — the rest has to come from somewhere else.
	Short bool `json:"short"`
}

// CapacityPoolRunning is GET /capacity/pools/{id}/resources.
type CapacityPoolRunning struct {
	PoolID   string     `json:"pool_id"`
	PoolName string     `json:"pool_name"`
	AsOf     *time.Time `json:"as_of"`
	// Resources is always present, empty included.
	Resources []CapacityRunningResource `json:"resources"`
	// Reclaim is what spot has to give back, per resource kind; empty when
	// spot fits the room it has. BSS says HOW MUCH and proposes WHICH, newest
	// first; the platform does the deleting.
	Reclaim []CapacityReclaim `json:"reclaim"`
}

// CapacityPoolResources lists what is running on a pool at `now`.
func (s *Store) CapacityPoolResources(ctx context.Context, poolID string, now time.Time) (CapacityPoolRunning, error) {
	pool, err := s.GetCapacityPool(ctx, poolID)
	if err != nil {
		return CapacityPoolRunning{}, err
	}
	out := CapacityPoolRunning{PoolID: pool.ID, PoolName: pool.Name, Resources: []CapacityRunningResource{}, Reclaim: []CapacityReclaim{}}

	regions, err := s.ListCapacityRegions(ctx)
	if err != nil {
		return out, err
	}
	zones := newCapacityZoneIndex(regions)
	placements, err := s.ListCapacityPlacements(ctx)
	if err != nil {
		return out, err
	}
	var zonePlacements []CapacityPlacement
	for _, pl := range placements {
		if pl.ZoneID == pool.ZoneID {
			zonePlacements = append(zonePlacements, pl)
		}
	}
	stored, err := s.ListCapacityShapes(ctx)
	if err != nil {
		return out, err
	}
	shapes := map[string]CapacityShape{}
	for _, sh := range stored {
		shapes[sh.SKU] = sh
	}
	shapeOf := func(sku string) map[string]Decimal {
		if sh, ok := shapes[sku]; ok && len(sh.Resources) > 0 {
			return sh.Resources
		}
		res := map[string]Decimal{}
		for k, v := range capacity.Derive(sku) {
			res[k] = Decimal(v)
		}
		return res
	}

	cutoff := now.UTC().Truncate(time.Hour)
	from := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -capacityHistoryDays)
	rows, err := s.db.QueryContext(ctx, `
WITH recs AS (
  SELECT u.source_id, u.resource_id, u.sku, u.unit, u.quantity, lower(u.region) AS region, u.window_start,
         lower(COALESCE(NULLIF(i.attrs->>'availability_zone', ''), NULLIF(i.attrs->>'az', ''), '')) AS az,
         `+capacityClassSQL+` AS tag_class,
         COALESCE(oc.class, '') AS override_class,
         COALESCE(i.name, '') AS name, i.first_seen
    FROM usage_records u
    JOIN cost_sources s ON s.id = u.source_id AND s.layer = '`+LayerCloud+`' AND s.status <> '`+StatusDisabled+`'
    LEFT JOIN resource_inventory i ON i.source_id = u.source_id AND i.resource_id = u.resource_id
    LEFT JOIN capacity_resource_classes oc ON oc.source_id = u.source_id AND oc.resource_id = u.resource_id
   WHERE u.window_start >= $1 AND u.window_start < $2 AND u.`+metricSKUFilter+`
),
last_hours AS (SELECT source_id, max(window_start) AS ws FROM recs GROUP BY source_id)
SELECT r.source_id, r.resource_id, max(r.name), r.region, r.az, r.sku, min(r.unit), sum(r.quantity)::text, r.tag_class, r.override_class, min(r.first_seen), r.window_start
  FROM recs r JOIN last_hours l ON l.source_id = r.source_id AND l.ws = r.window_start
 GROUP BY r.source_id, r.resource_id, r.region, r.az, r.sku, r.tag_class, r.override_class, r.window_start
 ORDER BY r.sku, r.resource_id`, from, cutoff)
	if err != nil {
		return out, mapErr(err)
	}
	defer rows.Close()
	poolNames := map[string]string{}
	for _, pl := range zonePlacements {
		poolNames[pl.PoolID] = pl.PoolName
	}
	for rows.Next() {
		var rr CapacityRunningResource
		var region, az, qty string
		var firstSeen sql.NullTime
		var hour time.Time
		if err := rows.Scan(&rr.SourceID, &rr.ResourceID, &rr.Name, &region, &az, &rr.SKU, &rr.Unit, &qty, &rr.TagClass, &rr.OverrideClass, &firstSeen, &hour); err != nil {
			return out, err
		}
		hour = hour.UTC()
		if out.AsOf == nil || hour.After(*out.AsOf) {
			h := hour
			out.AsOf = &h
		}
		zoneID, _, _ := zones.zoneFor(region, az)
		if zoneID != pool.ZoneID {
			continue
		}
		here, via, resolved, placed := resolvePlacements(zonePlacements, rr.SKU, rr.OverrideClass, rr.TagClass)
		if !placed {
			continue
		}
		onPool := false
		rr.SharedWith = []string{}
		for _, pl := range here {
			if pl.PoolID == pool.ID {
				onPool = true
			} else {
				rr.SharedWith = append(rr.SharedWith, poolNames[pl.PoolID])
			}
		}
		if !onPool {
			continue
		}
		sort.Strings(rr.SharedWith)
		rr.Via, rr.Class, rr.ClassSource, rr.Asked = via, resolved.Class, resolved.Source, resolved.Asked
		rr.Units = Decimal(qty)
		rr.PlacedClasses = []string{}
		for _, c := range capacity.ClassKeys() {
			for _, pl := range zonePlacements {
				if pl.Class == c && strings.EqualFold(pl.SKU, via) {
					rr.PlacedClasses = append(rr.PlacedClasses, c)
					break
				}
			}
		}
		rr.Consumes = map[string]Decimal{}
		for res, per := range shapeOf(rr.SKU) {
			rr.Consumes[res] = decOf(new(big.Rat).Mul(ratOf(Decimal(qty)), ratOf(per)))
		}
		if firstSeen.Valid {
			t := firstSeen.Time.UTC()
			rr.FirstSeen = &t
		}
		out.Resources = append(out.Resources, rr)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	// The reclaim list. How much each resource kind must give back comes from
	// the overview's own arithmetic, so the figure here is the figure there.
	pv, err := s.capacityPoolView(ctx, pool.ID, now, nil)
	if err != nil {
		return out, err
	}
	// Newest first: the resource that has run the shortest loses the least.
	spot := []int{}
	for i, rr := range out.Resources {
		if rr.Class == capacity.ClassSpot {
			spot = append(spot, i)
		}
	}
	sort.SliceStable(spot, func(a, b int) bool {
		x, y := out.Resources[spot[a]].FirstSeen, out.Resources[spot[b]].FirstSeen
		switch {
		case x == nil || y == nil:
			return y == nil && x != nil
		case !x.Equal(*y):
			return x.After(*y)
		}
		return out.Resources[spot[a]].ResourceID < out.Resources[spot[b]].ResourceID
	})
	for _, rv := range pv.Resources {
		need := ratOf(rv.SpotReclaim)
		if need.Sign() <= 0 {
			continue
		}
		rc := CapacityReclaim{Resource: rv.Resource, Label: rv.Label, Unit: rv.Unit, Needed: rv.SpotReclaim}
		covered := new(big.Rat)
		for _, i := range spot {
			if covered.Cmp(need) >= 0 {
				break
			}
			amount := ratOf(out.Resources[i].Consumes[rv.Resource])
			if amount.Sign() <= 0 {
				continue
			}
			covered.Add(covered, amount)
			if !out.Resources[i].Reclaim {
				out.Resources[i].Reclaim = true
			}
			rc.Resources++
		}
		rc.Covered = decOf(covered)
		rc.Short = covered.Cmp(need) < 0
		out.Reclaim = append(out.Reclaim, rc)
	}
	return out, nil
}

// capacityPoolView is one pool of the overview.
func (s *Store) capacityPoolView(ctx context.Context, poolID string, now time.Time, growth CapacityGrowth) (CapacityPoolView, error) {
	ov, err := s.CapacityOverview(ctx, now, "", growth)
	if err != nil {
		return CapacityPoolView{}, err
	}
	for _, r := range ov.Regions {
		for _, z := range r.Zones {
			for _, p := range z.Pools {
				if p.ID == poolID {
					return p, nil
				}
			}
		}
	}
	return CapacityPoolView{}, ErrNotFound
}

// ---------------------------------------------------------------------------
// the per-resource class override
// ---------------------------------------------------------------------------

// CapacityResourceClass is an operator's word on which class one running
// resource was sold at.
type CapacityResourceClass struct {
	SourceID   string    `json:"source_id"`
	ResourceID string    `json:"resource_id"`
	Class      string    `json:"class"`
	SetBy      string    `json:"set_by"`
	SetAt      time.Time `json:"set_at"`
}

// SetCapacityResourceClass records the class of one resource. It outranks the
// resource's lifecycle tag. The resource must be one the ledger knows: an
// override on an id nobody meters would sit there matching nothing.
func (s *Store) SetCapacityResourceClass(ctx context.Context, sourceID, resourceID, class, actor string) (CapacityResourceClass, error) {
	resourceID = strings.TrimSpace(resourceID)
	class = strings.ToLower(strings.TrimSpace(class))
	if sourceID == "" || resourceID == "" {
		return CapacityResourceClass{}, fmt.Errorf("%w: source_id and resource_id are required", ErrInvalid)
	}
	if !capacity.ValidClass(class) {
		return CapacityResourceClass{}, fmt.Errorf("%w: class must be one of %s", ErrInvalid, strings.Join(capacity.ClassKeys(), ", "))
	}
	var known bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM resource_inventory WHERE source_id = $1 AND resource_id = $2)
		OR EXISTS (SELECT 1 FROM usage_records WHERE source_id = $1 AND resource_id = $2)`, sourceID, resourceID).Scan(&known); err != nil {
		return CapacityResourceClass{}, mapErr(err)
	}
	if !known {
		return CapacityResourceClass{}, ErrNotFound
	}
	var out CapacityResourceClass
	err := s.db.QueryRowContext(ctx, `INSERT INTO capacity_resource_classes (source_id, resource_id, class, set_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_id, resource_id) DO UPDATE SET class = EXCLUDED.class, set_by = EXCLUDED.set_by, set_at = now()
		RETURNING source_id, resource_id, class, set_by, set_at`, sourceID, resourceID, class, actor).Scan(&out.SourceID, &out.ResourceID, &out.Class, &out.SetBy, &out.SetAt)
	if err != nil {
		return CapacityResourceClass{}, mapErr(err)
	}
	out.SetAt = out.SetAt.UTC()
	return out, nil
}

// ClearCapacityResourceClass removes an override, so the resource falls back
// to its tag, then to the default. ErrNotFound when there was none.
func (s *Store) ClearCapacityResourceClass(ctx context.Context, sourceID, resourceID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM capacity_resource_classes WHERE source_id = $1 AND resource_id = $2`, sourceID, strings.TrimSpace(resourceID))
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// the SKUs an operator can pick from
// ---------------------------------------------------------------------------

// CapacitySKUOption is one SKU the console offers wherever a SKU is chosen.
// Nothing here is typed by hand: the list is every SKU the product already
// knows, from the three places it can know one.
type CapacitySKUOption struct {
	SKU string `json:"sku"`
	// InPriceBook: some price book prices it. Metered: the ledger has usage
	// of it in the capacity window. HasShape: a stored row or a derivable
	// name says what one unit consumes — without it the SKU cannot be placed.
	InPriceBook bool               `json:"in_price_book"`
	Metered     bool               `json:"metered"`
	HasShape    bool               `json:"has_shape"`
	Shape       map[string]Decimal `json:"shape"`
	ShapeSource string             `json:"shape_source"`
	Description string             `json:"description"`
}

// CapacitySKUOptions is GET /capacity/skus.
type CapacitySKUOptions struct {
	SKUs     []CapacitySKUOption `json:"skus"`
	Families []capacity.Family   `json:"families"`
}

// ListCapacitySKUOptions returns every SKU worth offering: price-book items ∪
// metered cloud SKUs ∪ stored shapes, with the families they form.
func (s *Store) ListCapacitySKUOptions(ctx context.Context, now time.Time) (CapacitySKUOptions, error) {
	out := CapacitySKUOptions{SKUs: []CapacitySKUOption{}, Families: []capacity.Family{}}
	opts := map[string]*CapacitySKUOption{}
	get := func(sku string) *CapacitySKUOption {
		o := opts[sku]
		if o == nil {
			o = &CapacitySKUOption{SKU: sku, Shape: map[string]Decimal{}}
			opts[sku] = o
		}
		return o
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sku, max(description) FROM price_items GROUP BY sku`)
	if err != nil {
		return out, mapErr(err)
	}
	for rows.Next() {
		var sku, desc string
		if err := rows.Scan(&sku, &desc); err != nil {
			rows.Close()
			return out, err
		}
		o := get(sku)
		o.InPriceBook, o.Description = true, desc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	cutoff := now.UTC().Truncate(time.Hour)
	from := cutoff.AddDate(0, 0, -capacityHistoryDays)
	mrows, err := s.db.QueryContext(ctx, `SELECT DISTINCT u.sku FROM usage_records u
		JOIN cost_sources s ON s.id = u.source_id AND s.layer = '`+LayerCloud+`' AND s.status <> '`+StatusDisabled+`'
		WHERE u.window_start >= $1 AND u.window_start < $2 AND u.`+metricSKUFilter, from, cutoff)
	if err != nil {
		return out, mapErr(err)
	}
	for mrows.Next() {
		var sku string
		if err := mrows.Scan(&sku); err != nil {
			mrows.Close()
			return out, err
		}
		get(sku).Metered = true
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return out, err
	}
	stored, err := s.ListCapacityShapes(ctx)
	if err != nil {
		return out, err
	}
	for _, sh := range stored {
		o := get(sh.SKU)
		o.HasShape, o.Shape, o.ShapeSource = len(sh.Resources) > 0, sh.Resources, sh.Source
	}
	names := make([]string, 0, len(opts))
	for sku, o := range opts {
		if !o.HasShape {
			if d := capacity.Derive(sku); d != nil {
				o.HasShape, o.ShapeSource = true, capacity.SourceDerived
				for res, a := range d {
					o.Shape[res] = Decimal(a)
				}
			}
		}
		names = append(names, sku)
	}
	sort.Strings(names)
	for _, sku := range names {
		out.SKUs = append(out.SKUs, *opts[sku])
	}
	out.Families = capacity.Families(names)
	return out, nil
}
