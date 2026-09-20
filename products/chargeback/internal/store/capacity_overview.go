package store

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/capacity"
)

// The capacity overview (DESIGN.md §11): what each pool holds, what is
// running on it BY CLASS, what can still be sold, which resource binds, and
// the two walls each resource is heading for with the date an order has to be
// placed by.
//
// Nothing here is entered. Consumption is the usage ledger read one way: the
// latest complete hour per cloud source, through the SHAPES, onto the pools
// the PLACEMENTS name, split by the placement's CLASS.

// CapacityThresholds are the utilisation percentages pools are coloured at.
type CapacityThresholds struct {
	WarnPct     int `json:"warn_pct"`
	CriticalPct int `json:"critical_pct"`
}

// CapacityDayPoint is one complete day's consumption.
type CapacityDayPoint struct {
	Day      string  `json:"day"`
	Consumed Decimal `json:"consumed"`
}

// CapacityGrowth fits a daily series and returns its growth in units per
// day; ok is false when the series is too short to fit. The API passes the
// explorer's run-rate arithmetic (rating.RunRate), which this package cannot
// import — rating imports store — so the store never invents a second one.
type CapacityGrowth func(days []CapacityDayPoint) (perDay float64, ok bool)

// CapacityClassSeries is one class's daily consumption of one resource,
// oldest first. The series is SPLIT BY CLASS and never blended: a blended
// line hides which class is moving, and guaranteed growth means something
// completely different from spot growth — one is a hardware order, the other
// is a reclaim.
type CapacityClassSeries struct {
	Class string             `json:"class"`
	Label string             `json:"label"`
	Days  []CapacityDayPoint `json:"days"`
	// GrowthPerDay is the fitted trend of this class, nil when the history is
	// too short to fit one.
	GrowthPerDay *float64 `json:"growth_per_day"`
}

// CapacityResourceView is one resource of one pool, fully derived.
type CapacityResourceView struct {
	Resource string `json:"resource"`
	Label    string `json:"label"`
	Unit     string `json:"unit"`

	Machines        Decimal `json:"machines"`
	PerMachine      Decimal `json:"per_machine"`
	Reserve         Decimal `json:"reserve"`
	OvercommitRatio Decimal `json:"overcommit_ratio"`
	Raw             Decimal `json:"raw"`
	Usable          Decimal `json:"usable"`
	Sellable        Decimal `json:"sellable"`

	Guaranteed        Decimal `json:"guaranteed"`
	Burstable         Decimal `json:"burstable"`
	BurstablePhysical Decimal `json:"burstable_physical"`
	Spot              Decimal `json:"spot"`
	SpotPhysical      Decimal `json:"spot_physical"`

	// GuaranteedFloor is physical capacity burstable may never be sold into
	// (0 on a pool that does not enforce burstable); FloorFree is how much of
	// it no guaranteed unit holds yet. BurstableEnvelope is every nominal
	// burstable unit the pool can sell — (usable − max(G, floor)) × ratio —
	// and BurstableRoom what is left of it.
	GuaranteedFloor   Decimal `json:"guaranteed_floor"`
	FloorFree         Decimal `json:"floor_free"`
	BurstableEnvelope Decimal `json:"burstable_envelope"`
	BurstableRoom     Decimal `json:"burstable_room"`

	SoldNominal       Decimal `json:"sold_nominal"`
	Remaining         Decimal `json:"remaining"`
	GuaranteedCeiling Decimal `json:"guaranteed_ceiling"`
	PhysicalUsed      Decimal `json:"physical_used"`
	// PhysicalFree is hardware nothing has been admitted into. When another
	// resource of the same pool binds, this is STRANDED capacity — real
	// machines that cannot be sold because every unit sold also needs the
	// resource that ran out.
	PhysicalFree Decimal `json:"physical_free"`
	Stranded     bool    `json:"stranded"`
	SpotRoom     Decimal `json:"spot_room"`
	// SpotReclaim is how much nominal spot must be freed. BSS says HOW MUCH;
	// the platform decides WHICH instances.
	SpotReclaim Decimal `json:"spot_reclaim"`

	Sized          bool     `json:"sized"`
	UtilisationPct *float64 `json:"utilisation_pct"`
	Status         string   `json:"status"`
	Overcommitted  bool     `json:"overcommitted"`
	Over           Decimal  `json:"over"`

	Series      []CapacityClassSeries `json:"series"`
	HistoryDays int                   `json:"history_days"`

	// SoftWall is when the pool reaches `sellable` — spot starts being
	// reclaimed and burstable starts throttling. HardWall is when guaranteed
	// alone reaches `usable` — buy hardware, no policy avoids it.
	SoftWallDays *float64 `json:"soft_wall_days"`
	SoftWallDate *string  `json:"soft_wall_date"`
	HardWallDays *float64 `json:"hard_wall_days"`
	HardWallDate *string  `json:"hard_wall_date"`
	// OrderBy is the nearer wall minus the pool's lead time. An alert keyed
	// on the wall itself fires too late by construction.
	OrderByDays *float64 `json:"order_by_days"`
	OrderByDate *string  `json:"order_by_date"`
	OrderByWall string   `json:"order_by_wall"`
	// Late is true when the order-by date has already passed: the lead time
	// is longer than the time left.
	Late bool `json:"late"`
}

// CapacityBasketItem is one line of a basket: units of a SKU per basket, at
// the class its placement carries.
type CapacityBasketItem struct {
	SKU   string             `json:"sku"`
	Units Decimal            `json:"units"`
	Class string             `json:"class"`
	Shape map[string]Decimal `json:"shape"`
}

// CapacityBasketResource is what one basket costs one resource, and how many
// baskets that resource alone allows.
type CapacityBasketResource struct {
	Resource  string  `json:"resource"`
	Label     string  `json:"label"`
	Unit      string  `json:"unit"`
	PerBasket Decimal `json:"per_basket"`
	Remaining Decimal `json:"remaining"`
	Units     *int64  `json:"units"`
}

// CapacityBasket is how many more of a named mix fit on a pool.
//
// It replaces the per-SKU headroom column, which was a WRONG ANSWER rather
// than a missing feature: "50 large fit" and "200 small fit" printed side by
// side are mutually exclusive, and each silently assumes the others sell
// zero. One mix, one number, one binding resource.
type CapacityBasket struct {
	Items []CapacityBasketItem `json:"items"`
	// Units is how many MORE of this basket fit. It is nil when nothing
	// could be measured, and Reason then says why in words — a basket with
	// no sized resource must SAY so rather than show a number.
	Units  *int64 `json:"units"`
	Reason string `json:"reason"`
	// Binding is the resource that produced Units.
	Binding      string                   `json:"binding_resource"`
	Resources    []CapacityBasketResource `json:"resources"`
	UnshapedSKUs []string                 `json:"unshaped_skus"`
}

// CapacityPoolView is a pool with everything derived from it.
type CapacityPoolView struct {
	CapacityPool
	Status string `json:"status"`
	// Binding is the resource whose remaining room is smallest as a fraction
	// of what it could sell — the resource this pool runs out of first, and
	// the main procurement signal.
	Binding        string                  `json:"binding_resource"`
	UtilisationPct *float64                `json:"utilisation_pct"`
	Resources      []CapacityResourceView  `json:"resources_view"`
	Placements     []CapacityPlacementView `json:"placements"`
	Basket         CapacityBasket          `json:"basket"`
	// ZoneUnknown is true when some of this pool's consumption was attributed
	// here because the resource's availability zone was not recorded.
	ZoneUnknown bool `json:"zone_unknown"`

	OrderByDays     *float64 `json:"order_by_days"`
	OrderByDate     *string  `json:"order_by_date"`
	OrderByResource string   `json:"order_by_resource"`
	OrderByWall     string   `json:"order_by_wall"`
	Late            bool     `json:"late"`
}

// CapacityPlacementView is one SKU selling out of a pool, with what it is
// consuming there now.
type CapacityPlacementView struct {
	// SKU is a SKU or a family ("ecs.m7n.*"); Family says which.
	SKU    string `json:"sku"`
	Family bool   `json:"family"`
	Class  string `json:"class"`
	// Shape is the SKU's vector; empty for a family, whose members each have
	// their own.
	Shape       map[string]Decimal `json:"shape"`
	ShapeSource string             `json:"shape_source"`
	// Units and Resources are what is running through THIS placement now, at
	// THIS class. MatchedSKUs names the metered SKUs a family took — always
	// present, empty for an exact placement.
	Units       Decimal  `json:"units"`
	Resources   int      `json:"resources"`
	MatchedSKUs []string `json:"matched_skus"`
}

// CapacityZoneView is one zone with its pools.
type CapacityZoneView struct {
	ID        string             `json:"id"`
	Code      string             `json:"code"`
	Name      string             `json:"name"`
	IsDefault bool               `json:"is_default"`
	Pools     []CapacityPoolView `json:"pools"`
	// UnplacedSKUs are metered SKUs this zone could not attribute: they have
	// a shape but no pool takes them, or the pool they are placed on does not
	// hold a resource they consume. They are LISTED BY NAME, never summed
	// away, because a SKU counted against nothing reads as spare capacity.
	UnplacedSKUs []CapacityUnplacedSKU `json:"unplaced_skus"`
	// ClassMismatches are running resources that SAY they are one class (a
	// tag or an operator's override) where their SKU is not placed at it.
	// They still count — at CountedAs — and are named so the disagreement is
	// fixed rather than absorbed. Always present, empty included.
	ClassMismatches []CapacityClassMismatch `json:"class_mismatches"`
}

// CapacityClassMismatch is usage that asked for a class its SKU is not
// placed at in this zone.
type CapacityClassMismatch struct {
	SKU       string  `json:"sku"`
	Asked     string  `json:"asked"`
	CountedAs string  `json:"counted_as"`
	Units     Decimal `json:"units"`
	Resources int     `json:"resources"`
}

// CapacityUnplacedSKU is metered usage in a zone that no pool received.
type CapacityUnplacedSKU struct {
	SKU   string  `json:"sku"`
	Units Decimal `json:"units"`
	// Reason is no-placement (nothing places this SKU in the zone) or
	// resource-unplaced (it is placed, but no pool it is placed on holds the
	// resource named in Resource).
	Reason    string `json:"reason"`
	Resource  string `json:"resource,omitempty"`
	Resources int    `json:"resources"`
}

// CapacityRegionView is one region of the overview.
type CapacityRegionView struct {
	ID              string             `json:"id"`
	Code            string             `json:"code"`
	Name            string             `json:"name"`
	CloudSourceKind string             `json:"cloud_source_kind"`
	Zones           []CapacityZoneView `json:"zones"`
}

// CapacityUnshapedSKU is a metered SKU with no shape, stored or derived, so
// nothing knows what it consumes.
type CapacityUnshapedSKU struct {
	SKU       string   `json:"sku"`
	Unit      string   `json:"unit"`
	Quantity  Decimal  `json:"quantity"`
	Resources int      `json:"resources"`
	Regions   []string `json:"regions"`
}

// CapacityUnmappedRegion is metered usage in a region the operator has not
// added (or has added without a zone), so nothing could receive it.
type CapacityUnmappedRegion struct {
	Region    string  `json:"region"`
	Reason    string  `json:"reason"` // no-region | no-zones
	SKUs      int     `json:"skus"`
	Quantity  Decimal `json:"quantity"`
	Resources int     `json:"resources"`
}

// CapacitySummary is the KPI strip.
type CapacitySummary struct {
	Regions int `json:"regions"`
	Zones   int `json:"zones"`
	Pools   int `json:"pools"`
	// PoolsSized is how many pools carry machines and a vector; PoolsWarn and
	// PoolsCritical count by status, PoolsPastThreshold is their sum.
	PoolsSized         int `json:"pools_sized"`
	PoolsWarn          int `json:"pools_warn"`
	PoolsCritical      int `json:"pools_critical"`
	PoolsPastThreshold int `json:"pools_past_threshold"`
	// PoolsToOrder is how many pools have an order-by date at all;
	// PoolsOrderLate how many of those have already passed it.
	PoolsToOrder   int `json:"pools_to_order"`
	PoolsOrderLate int `json:"pools_order_late"`
	Placements     int `json:"placements"`
	Shapes         int `json:"shapes"`
	UnplacedSKUs   int `json:"unplaced_skus"`
	UnshapedSKUs   int `json:"unshaped_skus"`
	// SpotToReclaim counts (pool, resource) pairs holding more spot than the
	// room left for it.
	SpotToReclaim int `json:"spot_to_reclaim"`
	// ClassMismatches counts (zone, sku, asked class) rows.
	ClassMismatches int `json:"class_mismatches"`
}

// CapacityOverview is GET /capacity/overview.
type CapacityOverview struct {
	// AsOf is the latest complete hour consumption was measured in; nil when
	// the ledger holds no cloud usage at all.
	AsOf *time.Time `json:"as_of"`
	// Sources is how many cloud sources contributed; LaggingSources how many
	// of them last metered more than six hours before AsOf.
	Sources        int                      `json:"sources"`
	LaggingSources int                      `json:"lagging_sources"`
	Thresholds     CapacityThresholds       `json:"thresholds"`
	Classes        []capacity.ClassDef      `json:"classes"`
	ResourceKinds  []CapacityResourceKind   `json:"resource_kinds"`
	Regions        []CapacityRegionView     `json:"regions"`
	UnshapedSKUs   []CapacityUnshapedSKU    `json:"unshaped_skus"`
	UnmappedRegion []CapacityUnmappedRegion `json:"unmapped_regions"`
	Summary        CapacitySummary          `json:"summary"`
}

// ---------------------------------------------------------------------------
// the usage read
// ---------------------------------------------------------------------------

// capacityUsageRow is one (source, day, region, az, sku) of the last metered
// hour of that source on that day.
type capacityUsageRow struct {
	sourceID  string
	day       string
	hour      time.Time
	region    string
	az        string
	sku       string
	unit      string
	quantity  *big.Rat
	resources int
	// tagClass is the resource's own lifecycle tag when it names a class,
	// overrideClass what an operator said for it; "" when neither did.
	tagClass      string
	overrideClass string
}

// capacityClassSQL is the lifecycle tag of a usage record, kept only when it
// names a class — every other value of the tag is somebody else's business
// and would only fragment the grouping.
const capacityClassSQL = `CASE WHEN lower(COALESCE(u.labels->'tags'->>'` + capacity.ClassTagKey + `', '')) IN ('guaranteed','burstable','spot')
	THEN lower(u.labels->'tags'->>'` + capacity.ClassTagKey + `') ELSE '' END`

// capacityHistoryDays is how many complete days of consumption feed the
// growth trend: the run rate's window (7) plus one so seven complete days
// precede the current one.
const capacityHistoryDays = 8

// capacityLagging is how far behind AsOf a source's last hour may be before
// it is reported as lagging.
const capacityLagging = 6 * time.Hour

// queryCapacityUsage reads, per cloud source and per UTC day inside the
// history window, the source's LAST metered hour of that day, grouped by
// region, availability zone (from the inventory row) and SKU. The last hour
// of the latest day is the current consumption; the last hour of each earlier
// day is that day's point in the growth series.
func (s *Store) queryCapacityUsage(ctx context.Context, now time.Time) ([]capacityUsageRow, error) {
	cutoff := now.UTC().Truncate(time.Hour)
	from := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -capacityHistoryDays)
	rows, err := s.db.QueryContext(ctx, `
WITH recs AS (
  SELECT u.source_id, u.resource_id, u.sku, u.unit, u.quantity, lower(u.region) AS region, u.window_start,
         (u.window_start AT TIME ZONE 'UTC')::date AS day,
         lower(COALESCE(NULLIF(i.attrs->>'availability_zone', ''), NULLIF(i.attrs->>'az', ''), '')) AS az,
         `+capacityClassSQL+` AS tag_class,
         COALESCE(oc.class, '') AS override_class
    FROM usage_records u
    JOIN cost_sources s ON s.id = u.source_id AND s.layer = '`+LayerCloud+`' AND s.status <> '`+StatusDisabled+`'
    LEFT JOIN resource_inventory i ON i.source_id = u.source_id AND i.resource_id = u.resource_id
    LEFT JOIN capacity_resource_classes oc ON oc.source_id = u.source_id AND oc.resource_id = u.resource_id
   WHERE u.window_start >= $1 AND u.window_start < $2 AND u.`+metricSKUFilter+`
),
last_hours AS (SELECT source_id, day, max(window_start) AS ws FROM recs GROUP BY source_id, day)
SELECT r.source_id, to_char(r.day, 'YYYY-MM-DD'), r.window_start, r.region, r.az, r.sku, min(r.unit), sum(r.quantity)::text, count(DISTINCT r.resource_id), r.tag_class, r.override_class
  FROM recs r JOIN last_hours l ON l.source_id = r.source_id AND l.day = r.day AND l.ws = r.window_start
 GROUP BY r.source_id, r.day, r.window_start, r.region, r.az, r.sku, r.tag_class, r.override_class
 ORDER BY 1, 2, 4, 5, 6, 10, 11`, from, cutoff)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []capacityUsageRow
	for rows.Next() {
		var r capacityUsageRow
		var qty string
		if err := rows.Scan(&r.sourceID, &r.day, &r.hour, &r.region, &r.az, &r.sku, &r.unit, &qty, &r.resources, &r.tagClass, &r.overrideClass); err != nil {
			return nil, err
		}
		r.hour = r.hour.UTC()
		r.quantity = ratOf(Decimal(qty))
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// the overview
// ---------------------------------------------------------------------------

// poolResourceKey keys per-pool, per-resource accumulators.
type poolResourceKey struct{ pool, resource string }

// poolResourceClassKey adds the class.
type poolResourceClassKey struct{ pool, resource, class string }

// poolSKUKey keys per-pool unit counts of one METERED SKU at one class.
type poolSKUKey struct{ pool, sku, class string }

// CapacityOverview derives the capacity picture at now (DESIGN.md §11).
// regionFilter narrows the regions listed (a code; "" = all); consumption is
// always attributed over every region so the unmapped lists are complete.
// growth fits each class's daily series for the walls; nil reports neither.
func (s *Store) CapacityOverview(ctx context.Context, now time.Time, regionFilter string, growth CapacityGrowth) (CapacityOverview, error) {
	out := CapacityOverview{
		Thresholds:     CapacityThresholds{WarnPct: capacity.ThresholdWarnPct, CriticalPct: capacity.ThresholdCriticalPct},
		Classes:        capacity.Classes,
		Regions:        []CapacityRegionView{},
		UnshapedSKUs:   []CapacityUnshapedSKU{},
		UnmappedRegion: []CapacityUnmappedRegion{},
	}
	regions, err := s.ListCapacityRegions(ctx)
	if err != nil {
		return out, err
	}
	kinds, err := s.ListCapacityResourceKinds(ctx)
	if err != nil {
		return out, err
	}
	out.ResourceKinds = kinds

	pools, err := s.listAllCapacityPools(ctx)
	if err != nil {
		return out, err
	}
	placements, err := s.ListCapacityPlacements(ctx)
	if err != nil {
		return out, err
	}
	stored, err := s.ListCapacityShapes(ctx)
	if err != nil {
		return out, err
	}
	usage, err := s.queryCapacityUsage(ctx, now)
	if err != nil {
		return out, err
	}

	poolsByZone := map[string][]CapacityPool{}
	poolByID := map[string]CapacityPool{}
	usable := map[poolResourceKey]*big.Rat{}
	holds := map[poolResourceKey]bool{}
	for _, p := range pools {
		poolsByZone[p.ZoneID] = append(poolsByZone[p.ZoneID], p)
		poolByID[p.ID] = p
		machines := ratOf(p.Machines)
		for _, r := range p.Resources {
			k := poolResourceKey{p.ID, r.Resource}
			holds[k] = true
			u := new(big.Rat).Sub(new(big.Rat).Mul(machines, ratOf(r.PerMachine)), ratOf(r.Reserve))
			if u.Sign() < 0 {
				u = new(big.Rat)
			}
			usable[k] = u
		}
	}
	// Placements by zone. One SKU may be placed at several classes, and a
	// placement may be a FAMILY, so which of them a usage row lands on is
	// decided per row (resolvePlacements), never looked up by SKU alone.
	placedIn := map[string][]CapacityPlacement{}
	for _, pl := range placements {
		placedIn[pl.ZoneID] = append(placedIn[pl.ZoneID], pl)
	}
	out.Summary.Placements = len(placements)

	zones := newCapacityZoneIndex(regions)

	// The shape universe: stored rows win; a metered SKU without a row takes
	// what its name implies.
	shapes := map[string]CapacityShape{}
	for _, sh := range stored {
		shapes[sh.SKU] = sh
	}
	out.Summary.Shapes = len(stored)
	shapeOf := func(sku string) (CapacityShape, bool) {
		if sh, ok := shapes[sku]; ok {
			return sh, len(sh.Resources) > 0
		}
		d := capacity.Derive(sku)
		if d == nil {
			return CapacityShape{}, false
		}
		sh := CapacityShape{SKU: sku, Resources: map[string]Decimal{}, Source: capacity.SourceDerived}
		for res, a := range d {
			sh.Resources[res] = Decimal(a)
		}
		shapes[sku] = sh
		return sh, true
	}

	// Current = each source's latest hour; the series = each (source, day)
	// last hour, summed over sources per day.
	latestBySource := map[string]time.Time{}
	for _, r := range usage {
		if r.hour.After(latestBySource[r.sourceID]) {
			latestBySource[r.sourceID] = r.hour
		}
	}
	var asOf time.Time
	for _, h := range latestBySource {
		if h.After(asOf) {
			asOf = h
		}
	}
	today := ""
	if !asOf.IsZero() {
		t := asOf
		out.AsOf = &t
		out.Sources = len(latestBySource)
		for _, h := range latestBySource {
			if asOf.Sub(h) > capacityLagging {
				out.LaggingSources++
			}
		}
		today = asOf.Format("2006-01-02")
	}

	consumed := map[poolResourceClassKey]*big.Rat{}
	series := map[poolResourceClassKey]map[string]*big.Rat{}
	zoneUnknownPool := map[string]bool{}
	skuUnits := map[poolSKUKey]*capacityUnits{}
	unplaced := map[string]map[string]*CapacityUnplacedSKU{} // zone → key → row
	mismatched := map[string]map[string]*CapacityClassMismatch{}
	unshaped := map[string]*CapacityUnshapedSKU{}
	unshapedRegions := map[string]map[string]bool{}
	unmappedRegion := map[string]*CapacityUnmappedRegion{}
	unmappedRegionSKUs := map[string]map[string]bool{}

	addRat := func(m map[poolResourceClassKey]*big.Rat, k poolResourceClassKey, v *big.Rat) {
		if m[k] == nil {
			m[k] = new(big.Rat)
		}
		m[k].Add(m[k], v)
	}
	noteUnplaced := func(zone, sku, reason, resource string, units *big.Rat, resources int) {
		if unplaced[zone] == nil {
			unplaced[zone] = map[string]*CapacityUnplacedSKU{}
		}
		key := sku + "|" + reason + "|" + resource
		u := unplaced[zone][key]
		if u == nil {
			u = &CapacityUnplacedSKU{SKU: sku, Reason: reason, Resource: resource, Units: "0"}
			unplaced[zone][key] = u
		}
		u.Units = addDec(u.Units, decOf(units))
		u.Resources += resources
	}

	for _, r := range usage {
		current := r.hour.Equal(latestBySource[r.sourceID])
		zoneID, regionKnown, unknownZone := zones.zoneFor(r.region, r.az)
		if zoneID == "" {
			if current {
				reason := "no-region"
				if regionKnown {
					reason = "no-zones"
				}
				u := unmappedRegion[r.region]
				if u == nil {
					u = &CapacityUnmappedRegion{Region: r.region, Reason: reason, Quantity: "0"}
					unmappedRegion[r.region] = u
					unmappedRegionSKUs[r.region] = map[string]bool{}
				}
				unmappedRegionSKUs[r.region][r.sku] = true
				u.Quantity = addDec(u.Quantity, decOf(r.quantity))
				u.Resources += r.resources
			}
			continue
		}
		shape, ok := shapeOf(r.sku)
		if !ok {
			if current {
				u := unshaped[r.sku]
				if u == nil {
					u = &CapacityUnshapedSKU{SKU: r.sku, Unit: r.unit, Quantity: "0"}
					unshaped[r.sku] = u
					unshapedRegions[r.sku] = map[string]bool{}
				}
				unshapedRegions[r.sku][r.region] = true
				u.Quantity = addDec(u.Quantity, decOf(r.quantity))
				u.Resources += r.resources
			}
			continue
		}
		here, via, resolved, placed := resolvePlacements(placedIn[zoneID], r.sku, r.overrideClass, r.tagClass)
		if !placed {
			if current {
				noteUnplaced(zoneID, r.sku, "no-placement", "", r.quantity, r.resources)
			}
			continue
		}
		class := resolved.Class
		if current && resolved.Asked != "" {
			if mismatched[zoneID] == nil {
				mismatched[zoneID] = map[string]*CapacityClassMismatch{}
			}
			key := r.sku + "|" + resolved.Asked
			m := mismatched[zoneID][key]
			if m == nil {
				m = &CapacityClassMismatch{SKU: r.sku, Asked: resolved.Asked, CountedAs: class, Units: "0"}
				mismatched[zoneID][key] = m
			}
			m.Units = addDec(m.Units, decOf(r.quantity))
			m.Resources += r.resources
		}
		// The unit share of a pool is the share it takes of the FIRST resource
		// of the shape it holds; with one placement (the ordinary case) that is
		// 1, and with several identical pools it is each pool's size share.
		unitShare := map[string]*big.Rat{}
		for _, res := range capacity.SortedResources(shapeToShape(shape)) {
			amount := new(big.Rat).Mul(r.quantity, ratOf(shape.Resources[res]))
			var candidates []string
			for _, pl := range here {
				if holds[poolResourceKey{pl.PoolID, res}] {
					candidates = append(candidates, pl.PoolID)
				}
			}
			if len(candidates) == 0 {
				if current {
					noteUnplaced(zoneID, r.sku, "resource-unplaced", res, amount, r.resources)
				}
				continue
			}
			for poolID, share := range shareByUsable(candidates, usable, res) {
				part := new(big.Rat).Mul(amount, share)
				k := poolResourceClassKey{poolID, res, class}
				if current {
					addRat(consumed, k, part)
					if unknownZone {
						zoneUnknownPool[poolID] = true
					}
				}
				if r.day != today {
					if series[k] == nil {
						series[k] = map[string]*big.Rat{}
					}
					if series[k][r.day] == nil {
						series[k][r.day] = new(big.Rat)
					}
					series[k][r.day].Add(series[k][r.day], part)
				}
				if _, seen := unitShare[poolID]; !seen {
					unitShare[poolID] = share
				}
			}
		}
		if current {
			for poolID, share := range unitShare {
				k := poolSKUKey{poolID, r.sku, class}
				acc := skuUnits[k]
				if acc == nil {
					acc = &capacityUnits{units: new(big.Rat), via: via}
					skuUnits[k] = acc
				}
				acc.units.Add(acc.units, new(big.Rat).Mul(r.quantity, share))
				acc.resources += r.resources
			}
		}
	}

	filter := normCode(regionFilter)
	for _, reg := range regions {
		if filter != "" && reg.Code != filter {
			continue
		}
		rv := CapacityRegionView{ID: reg.ID, Code: reg.Code, Name: reg.Name, CloudSourceKind: reg.CloudSourceKind, Zones: []CapacityZoneView{}}
		for _, z := range reg.Zones {
			zv := CapacityZoneView{ID: z.ID, Code: z.Code, Name: z.Name, IsDefault: z.IsDefault, Pools: []CapacityPoolView{}, UnplacedSKUs: []CapacityUnplacedSKU{}, ClassMismatches: []CapacityClassMismatch{}}
			for _, p := range poolsByZone[z.ID] {
				pv := s.poolView(p, placedIn[z.ID], shapes, consumed, series, skuUnits, growth, asOf)
				pv.ZoneUnknown = zoneUnknownPool[p.ID]
				out.Summary.Pools++
				if pv.Status != capacity.StatusUnset {
					out.Summary.PoolsSized++
				}
				switch pv.Status {
				case capacity.StatusWarn:
					out.Summary.PoolsWarn++
				case capacity.StatusCritical:
					out.Summary.PoolsCritical++
				}
				if pv.OrderByDays != nil {
					out.Summary.PoolsToOrder++
					if pv.Late {
						out.Summary.PoolsOrderLate++
					}
				}
				for _, r := range pv.Resources {
					if ratOf(r.SpotReclaim).Sign() > 0 {
						out.Summary.SpotToReclaim++
					}
				}
				zv.Pools = append(zv.Pools, pv)
			}
			for _, u := range unplaced[z.ID] {
				zv.UnplacedSKUs = append(zv.UnplacedSKUs, *u)
			}
			sort.Slice(zv.UnplacedSKUs, func(i, j int) bool {
				if zv.UnplacedSKUs[i].SKU != zv.UnplacedSKUs[j].SKU {
					return zv.UnplacedSKUs[i].SKU < zv.UnplacedSKUs[j].SKU
				}
				return zv.UnplacedSKUs[i].Resource < zv.UnplacedSKUs[j].Resource
			})
			out.Summary.UnplacedSKUs += len(zv.UnplacedSKUs)
			for _, m := range mismatched[z.ID] {
				zv.ClassMismatches = append(zv.ClassMismatches, *m)
			}
			sort.Slice(zv.ClassMismatches, func(i, j int) bool {
				if zv.ClassMismatches[i].SKU != zv.ClassMismatches[j].SKU {
					return zv.ClassMismatches[i].SKU < zv.ClassMismatches[j].SKU
				}
				return zv.ClassMismatches[i].Asked < zv.ClassMismatches[j].Asked
			})
			out.Summary.ClassMismatches += len(zv.ClassMismatches)
			out.Summary.Zones++
			rv.Zones = append(rv.Zones, zv)
		}
		out.Summary.Regions++
		out.Regions = append(out.Regions, rv)
	}
	out.Summary.PoolsPastThreshold = out.Summary.PoolsWarn + out.Summary.PoolsCritical

	for sku, u := range unshaped {
		for region := range unshapedRegions[sku] {
			u.Regions = append(u.Regions, region)
		}
		sort.Strings(u.Regions)
		out.UnshapedSKUs = append(out.UnshapedSKUs, *u)
	}
	sort.Slice(out.UnshapedSKUs, func(i, j int) bool { return out.UnshapedSKUs[i].SKU < out.UnshapedSKUs[j].SKU })
	out.Summary.UnshapedSKUs = len(out.UnshapedSKUs)
	for region, u := range unmappedRegion {
		u.SKUs = len(unmappedRegionSKUs[region])
		out.UnmappedRegion = append(out.UnmappedRegion, *u)
	}
	sort.Slice(out.UnmappedRegion, func(i, j int) bool { return out.UnmappedRegion[i].Region < out.UnmappedRegion[j].Region })
	return out, nil
}

// listAllCapacityPools returns every pool of every zone with its resources.
func (s *Store) listAllCapacityPools(ctx context.Context) ([]CapacityPool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+capacityPoolColumns+` FROM capacity_pools p
		JOIN capacity_zones z ON z.id = p.zone_id JOIN capacity_regions r ON r.id = z.region_id ORDER BY r.code, z.code, p.name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []CapacityPool{}
	for rows.Next() {
		p, err := scanCapacityPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadPoolResources(ctx, out)
}

// capacityZoneIndex finds the zone a usage row belongs to. It is ONE function
// because the overview and the per-resource list must agree on it to the row:
// a resource the list shows under a pool is one the overview counted there.
type capacityZoneIndex struct {
	zoneByCode  map[string]map[string]string
	defaultZone map[string]string
}

func newCapacityZoneIndex(regions []CapacityRegion) capacityZoneIndex {
	ix := capacityZoneIndex{zoneByCode: map[string]map[string]string{}, defaultZone: map[string]string{}}
	for _, r := range regions {
		ix.zoneByCode[r.Code] = map[string]string{}
		for _, z := range r.Zones {
			ix.zoneByCode[r.Code][z.Code] = z.ID
			if z.IsDefault {
				ix.defaultZone[r.Code] = z.ID
			}
		}
		if _, ok := ix.defaultZone[r.Code]; !ok && len(r.Zones) > 0 {
			ix.defaultZone[r.Code] = r.Zones[0].ID
		}
	}
	return ix
}

// zoneFor returns the zone a (region, availability zone) lands in: the named
// zone, else the region's default with unknownZone set. zoneID is "" when the
// region is not configured (regionKnown false) or has no zones.
func (ix capacityZoneIndex) zoneFor(region, az string) (zoneID string, regionKnown, unknownZone bool) {
	byCode, ok := ix.zoneByCode[region]
	if !ok {
		return "", false, false
	}
	if id, found := byCode[az]; found && az != "" {
		return id, true, false
	}
	return ix.defaultZone[region], true, true
}

// resolvePlacements decides where one usage row lands in a zone: WHICH
// placement SKU takes it (the exact one, else the longest family —
// capacity.BestMatches), and AT WHICH CLASS (the resource's override, else its
// lifecycle tag, else the most conservative class the SKU is placed at —
// capacity.ResolveClass). It returns the placements of that SKU at that
// class, the placement SKU the row arrived through, and the resolution.
// placed is false when nothing in the zone takes the SKU at all.
func resolvePlacements(zone []CapacityPlacement, sku, override, tag string) (here []CapacityPlacement, via string, res capacity.ClassResolution, placed bool) {
	if len(zone) == 0 {
		return nil, "", res, false
	}
	names := make([]string, 0, len(zone))
	for _, pl := range zone {
		names = append(names, pl.SKU)
	}
	via = capacity.BestMatches(names, sku)
	if via == "" {
		return nil, "", res, false
	}
	var matched []CapacityPlacement
	var classes []string
	for _, pl := range zone {
		if strings.EqualFold(pl.SKU, via) {
			matched = append(matched, pl)
			classes = append(classes, pl.Class)
		}
	}
	res, placed = capacity.ResolveClass(override, tag, classes)
	if !placed {
		return nil, via, res, false
	}
	for _, pl := range matched {
		if pl.Class == res.Class {
			here = append(here, pl)
		}
	}
	return here, via, res, true
}

// poolResourceState is the arithmetic's input for one resource of one pool.
// THE POOL'S CLASSES DECIDE TWO OF ITS NUMBERS: overcommit and the guaranteed
// floor are burstable's — the ratio is how far burstable is oversold, the
// floor what it is kept out of — so on a pool that does not enforce burstable
// the ratio is 1 and the floor 0 whatever the row holds. cleanPoolInput
// refuses anything else on a write; this covers the rows written before the
// rule existed.
func poolResourceState(p CapacityPool, r CapacityPoolResource, g, b, sp *big.Rat) capacity.ResourceState {
	st := capacity.ResourceState{
		Machines: ratOf(p.Machines), PerMachine: ratOf(r.PerMachine), Reserve: ratOf(r.Reserve),
		Ratio: big.NewRat(1, 1), Guaranteed: g, Burstable: b, Spot: sp,
	}
	if capacity.HasClass(p.Classes, capacity.ClassBurstable) {
		st.Ratio, st.Floor = ratOf(r.OvercommitRatio), ratOf(r.GuaranteedFloor)
	}
	return st
}

// shareByUsable splits an amount across the pools that hold a resource, in
// proportion to how much of that resource each has USABLE.
//
// BSS does not know which machine an instance landed on — the platform
// decides that, exactly as it decides which spot instance to reclaim — so
// when a SKU sells out of two pools the load is attributed by pool size,
// which is the only weighting the operator's own data supports. With ONE
// placement (the ordinary case) the share is 1 and the attribution is exact;
// with several pools that have no size yet it is an even split, because
// weighting by zero is not a weighting.
func shareByUsable(pools []string, usable map[poolResourceKey]*big.Rat, resource string) map[string]*big.Rat {
	out := map[string]*big.Rat{}
	if len(pools) == 1 {
		out[pools[0]] = big.NewRat(1, 1)
		return out
	}
	total := new(big.Rat)
	for _, p := range pools {
		if u := usable[poolResourceKey{p, resource}]; u != nil {
			total.Add(total, u)
		}
	}
	if total.Sign() == 0 {
		even := big.NewRat(1, int64(len(pools)))
		for _, p := range pools {
			out[p] = new(big.Rat).Set(even)
		}
		return out
	}
	for _, p := range pools {
		u := usable[poolResourceKey{p, resource}]
		if u == nil {
			u = new(big.Rat)
		}
		out[p] = new(big.Rat).Quo(u, total)
	}
	return out
}

// shapeToShape adapts a stored shape to the pure package's map type so the
// resource ordering is the same everywhere.
func shapeToShape(sh CapacityShape) capacity.Shape {
	out := capacity.Shape{}
	for k, v := range sh.Resources {
		out[k] = string(v)
	}
	return out
}

// poolView derives one pool's figures.
func (s *Store) poolView(
	p CapacityPool,
	placedHere []CapacityPlacement,
	shapes map[string]CapacityShape,
	consumed map[poolResourceClassKey]*big.Rat,
	series map[poolResourceClassKey]map[string]*big.Rat,
	skuUnits map[poolSKUKey]*capacityUnits,
	growth CapacityGrowth,
	asOf time.Time,
) CapacityPoolView {
	pv := CapacityPoolView{CapacityPool: p, Resources: []CapacityResourceView{}, Placements: []CapacityPlacementView{}}
	maths := map[string]capacity.ResourceMath{}
	statuses := make([]string, 0, len(p.Resources))

	for _, r := range p.Resources {
		g := consumed[poolResourceClassKey{p.ID, r.Resource, capacity.ClassGuaranteed}]
		b := consumed[poolResourceClassKey{p.ID, r.Resource, capacity.ClassBurstable}]
		sp := consumed[poolResourceClassKey{p.ID, r.Resource, capacity.ClassSpot}]
		m := capacity.Compute(poolResourceState(p, r, g, b, sp))
		maths[r.Resource] = m
		statuses = append(statuses, m.Status)

		rv := CapacityResourceView{
			Resource: r.Resource, Label: r.Label, Unit: r.Unit,
			// The ratio and the floor are the ones the arithmetic USED: 1 and 0
			// on a pool that does not enforce burstable, whatever is stored.
			Machines: p.Machines, PerMachine: r.PerMachine, Reserve: r.Reserve, OvercommitRatio: decOf(m.Ratio),
			GuaranteedFloor: decOf(m.Floor), FloorFree: decOf(m.FloorFree),
			BurstableEnvelope: decOf(m.BurstableEnvelope), BurstableRoom: decOf(m.BurstableRoom),
			Raw: decOf(m.Raw), Usable: decOf(m.Usable), Sellable: decOf(m.Sellable),
			Guaranteed: decOf(m.Guaranteed), Burstable: decOf(m.Burstable), BurstablePhysical: decOf(m.BurstablePhysical),
			Spot: decOf(m.Spot), SpotPhysical: decOf(m.SpotPhysical),
			SoldNominal: decOf(m.SoldNominal), Remaining: decOf(m.Remaining), GuaranteedCeiling: decOf(m.GuaranteedCeiling),
			PhysicalUsed: decOf(m.PhysicalUsed), PhysicalFree: decOf(m.PhysicalFree),
			SpotRoom: decOf(m.SpotRoom), SpotReclaim: decOf(m.SpotReclaim),
			Sized: m.Sized, UtilisationPct: m.UtilisationPct, Status: m.Status,
			Overcommitted: m.Overcommitted, Over: decOf(m.Over),
			Series: []CapacityClassSeries{},
		}

		// The series, split by class, and the trend of each.
		growthByClass := map[string]*float64{}
		for _, c := range capacity.Classes {
			days := series[poolResourceClassKey{p.ID, r.Resource, c.Key}]
			if len(days) == 0 {
				continue
			}
			keys := make([]string, 0, len(days))
			for d := range days {
				keys = append(keys, d)
			}
			sort.Strings(keys)
			cs := CapacityClassSeries{Class: c.Key, Label: c.Label, Days: make([]CapacityDayPoint, 0, len(keys))}
			for _, d := range keys {
				cs.Days = append(cs.Days, CapacityDayPoint{Day: d, Consumed: decOf(days[d])})
			}
			if len(cs.Days) > rv.HistoryDays {
				rv.HistoryDays = len(cs.Days)
			}
			if growth != nil {
				if trend, ok := growth(cs.Days); ok {
					t := trend
					cs.GrowthPerDay = &t
					growthByClass[c.Key] = &t
				}
			}
			rv.Series = append(rv.Series, cs)
		}

		walls := capacity.Project(m, growthByClass[capacity.ClassGuaranteed], growthByClass[capacity.ClassBurstable], p.LeadTimeDays)
		rv.SoftWallDays, rv.SoftWallDate = walls.Soft, dateAfter(asOf, walls.Soft)
		rv.HardWallDays, rv.HardWallDate = walls.Hard, dateAfter(asOf, walls.Hard)
		rv.OrderByDays, rv.OrderByDate, rv.OrderByWall = walls.OrderBy, dateAfter(asOf, walls.OrderBy), walls.Wall
		if walls.OrderBy != nil && *walls.OrderBy <= 0 {
			rv.Late = true
		}
		pv.Resources = append(pv.Resources, rv)
	}

	pv.Binding = capacity.Binding(maths)
	pv.Status = capacity.WorstStatus(statuses)
	for i, rv := range pv.Resources {
		// Free hardware on a resource that is NOT the one binding is
		// stranded: it cannot be sold, because every unit sold also needs the
		// resource that ran out.
		if pv.Binding != "" && rv.Resource != pv.Binding && ratOf(rv.PhysicalFree).Sign() > 0 {
			if bm, ok := maths[pv.Binding]; ok && bm.Remaining.Sign() == 0 {
				pv.Resources[i].Stranded = true
			}
		}
		if rv.Resource == pv.Binding {
			pv.UtilisationPct = rv.UtilisationPct
		}
		if rv.OrderByDays != nil && (pv.OrderByDays == nil || *rv.OrderByDays < *pv.OrderByDays) {
			pv.OrderByDays, pv.OrderByDate, pv.OrderByResource, pv.OrderByWall = rv.OrderByDays, rv.OrderByDate, rv.Resource, rv.OrderByWall
			pv.Late = rv.Late
		}
	}

	// The placements, with what is running through each of them now. A
	// family's row carries the metered SKUs it took; what is SELLING is kept
	// per metered SKU and class, because that — not the placement row — is
	// what a basket is a mix of.
	selling := []CapacityPlacementView{}
	for k, acc := range skuUnits {
		if k.pool != p.ID {
			continue
		}
		sh := shapes[k.sku]
		selling = append(selling, CapacityPlacementView{SKU: k.sku, Class: k.class, Shape: sh.Resources, ShapeSource: sh.Source, Units: decOf(acc.units), Resources: acc.resources})
	}
	sort.Slice(selling, func(i, j int) bool {
		if selling[i].SKU != selling[j].SKU {
			return selling[i].SKU < selling[j].SKU
		}
		return selling[i].Class < selling[j].Class
	})
	for _, pl := range placedHere {
		if pl.PoolID != p.ID {
			continue
		}
		pvw := CapacityPlacementView{SKU: pl.SKU, Family: capacity.IsFamily(pl.SKU), Class: pl.Class, Shape: map[string]Decimal{}, Units: "0", MatchedSKUs: []string{}}
		if !pvw.Family {
			sh := shapes[pl.SKU]
			if sh.Resources != nil {
				pvw.Shape = sh.Resources
			}
			pvw.ShapeSource = sh.Source
		}
		for k, acc := range skuUnits {
			if k.pool != p.ID || k.class != pl.Class || !strings.EqualFold(acc.via, pl.SKU) {
				continue
			}
			pvw.Units = addDec(pvw.Units, decOf(acc.units))
			pvw.Resources += acc.resources
			if pvw.Family {
				pvw.MatchedSKUs = append(pvw.MatchedSKUs, k.sku)
			}
		}
		sort.Strings(pvw.MatchedSKUs)
		pv.Placements = append(pv.Placements, pvw)
	}

	pv.Basket = basketFit(maths, defaultBasket(selling), shapes)
	return pv
}

// capacityUnits accumulates the units of one SKU attributed to one pool.
type capacityUnits struct {
	units     *big.Rat
	resources int
	// via is the placement SKU the usage arrived through: the SKU itself, or
	// the family that took it.
	via string
}

// defaultBasket is THE MIX CURRENTLY SELLING on a pool, scaled so the largest
// line is one unit. A basket is a proportion, not a total: asking "how many
// more of everything I have already sold fit" answers 0 on any pool that is
// more than half full, which tells an operator nothing. Scaled, the answer
// reads "N more of this mix", which is the question procurement asks.
func defaultBasket(placements []CapacityPlacementView) []CapacityBasketItem {
	largest := new(big.Rat)
	for _, pl := range placements {
		if u := ratOf(pl.Units); u.Cmp(largest) > 0 {
			largest = u
		}
	}
	if largest.Sign() == 0 {
		// An empty basket is an EMPTY LIST, never an absent one. Returning nil
		// here marshals as JSON null, and a reader that maps over it crashes
		// rather than rendering "nothing is selling yet" — which is exactly
		// what a brand-new pool, or one with no placements, always looks like.
		return []CapacityBasketItem{}
	}
	out := []CapacityBasketItem{}
	for _, pl := range placements {
		u := ratOf(pl.Units)
		if u.Sign() <= 0 {
			continue
		}
		out = append(out, CapacityBasketItem{SKU: pl.SKU, Units: decOf(new(big.Rat).Quo(u, largest)), Class: pl.Class, Shape: pl.Shape})
	}
	return out
}

// basketFit turns a basket into how many more of it fit, with the binding
// resource and the per-resource working.
func basketFit(maths map[string]capacity.ResourceMath, items []CapacityBasketItem, shapes map[string]CapacityShape) CapacityBasket {
	out := CapacityBasket{Items: items, Resources: []CapacityBasketResource{}, UnshapedSKUs: []string{}}
	if len(items) == 0 {
		out.Reason = "nothing is selling on this pool yet, so there is no mix to project; name one to see how many fit"
		return out
	}
	demand := map[string]capacity.BasketDemand{}
	perBasket := map[string]*big.Rat{}
	for _, it := range items {
		shape := it.Shape
		if len(shape) == 0 {
			shape = shapes[it.SKU].Resources
		}
		if len(shape) == 0 {
			out.UnshapedSKUs = append(out.UnshapedSKUs, it.SKU)
			continue
		}
		units := ratOf(it.Units)
		for res, amount := range shape {
			add := new(big.Rat).Mul(units, ratOf(amount))
			d := demand[res]
			switch it.Class {
			case capacity.ClassBurstable:
				d.Burstable = addRatP(d.Burstable, add)
			case capacity.ClassSpot:
				d.Spot = addRatP(d.Spot, add)
			default:
				d.Guaranteed = addRatP(d.Guaranteed, add)
			}
			demand[res] = d
			perBasket[res] = addRatP(perBasket[res], add)
		}
	}
	if len(demand) == 0 {
		out.Reason = "no SKU in this mix has a shape, so nothing says what a basket consumes"
		return out
	}
	fit := capacity.Fit(maths, demand)
	keys := make([]string, 0, len(demand))
	for k := range demand {
		keys = append(keys, k)
	}
	capacity.SortResources(keys)
	sized := 0
	for _, res := range keys {
		m, held := maths[res]
		kind := capacity.KindOf(res)
		br := CapacityBasketResource{Resource: res, Label: kind.Label, Unit: kind.Unit, PerBasket: decOf(perBasket[res]), Remaining: "0"}
		if held {
			br.Remaining = decOf(m.Remaining)
			if m.Sized {
				sized++
			}
		}
		if u, ok := fit.Per[res]; ok {
			n := u.Int64()
			br.Units = &n
		}
		out.Resources = append(out.Resources, br)
	}
	if fit.Units == nil {
		switch {
		case sized == 0:
			out.Reason = "no resource this mix consumes is sized on this pool: enter the machines and the per-machine vector first"
		default:
			out.Reason = "this mix consumes nothing measurable on this pool"
		}
		return out
	}
	n := fit.Units.Int64()
	out.Units, out.Binding = &n, fit.Binding
	return out
}

func addRatP(a *big.Rat, b *big.Rat) *big.Rat {
	if a == nil {
		return new(big.Rat).Set(b)
	}
	return new(big.Rat).Add(a, b)
}

// dateAfter is the UTC date `days` days after the measurement hour, or nil.
// A negative day count (an order-by that has already passed) still renders a
// date: "you should have ordered on the 3rd" is the useful message.
func dateAfter(asOf time.Time, days *float64) *string {
	if days == nil || asOf.IsZero() {
		return nil
	}
	d := asOf.UTC().AddDate(0, 0, int(math.Round(*days))).Format("2006-01-02")
	return &d
}

// ---------------------------------------------------------------------------
// basket headroom on demand
// ---------------------------------------------------------------------------

// CapacityBasketRequest is one line of a mix the operator names.
type CapacityBasketRequest struct {
	SKU   string  `json:"sku"`
	Units Decimal `json:"units"`
	// Class is the class the line is sold at; "" takes the most conservative
	// class the SKU is placed at on the pool, or guaranteed when it is not
	// placed there yet.
	Class string `json:"class"`
}

// ParseBasket reads a basket from the query form
// "sku:units[:class],sku:units[:class]". An entry without a count is one
// unit; one without a class takes the pool's default for that SKU.
func ParseBasket(s string) []CapacityBasketRequest {
	out := []CapacityBasketRequest{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		sku := strings.TrimSpace(fields[0])
		if sku == "" {
			continue
		}
		u, class := "1", ""
		if len(fields) > 1 {
			if v := strings.TrimSpace(fields[1]); v != "" {
				u = v
			}
		}
		if len(fields) > 2 {
			class = strings.ToLower(strings.TrimSpace(fields[2]))
		}
		out = append(out, CapacityBasketRequest{SKU: sku, Units: Decimal(u), Class: class})
	}
	return out
}

// CapacityPoolBasket answers "how many more of this mix fit on this pool" for
// a mix the operator names. An empty mix falls back to the one currently
// selling, which is what the overview shows.
func (s *Store) CapacityPoolBasket(ctx context.Context, poolID string, now time.Time, req []CapacityBasketRequest, growth CapacityGrowth) (CapacityPoolView, error) {
	pool, err := s.GetCapacityPool(ctx, poolID)
	if err != nil {
		return CapacityPoolView{}, err
	}
	pv, err := s.capacityPoolView(ctx, pool.ID, now, growth)
	if err != nil {
		return CapacityPoolView{}, err
	}
	if len(req) == 0 {
		return pv, nil
	}
	maths := map[string]capacity.ResourceMath{}
	for _, r := range pool.Resources {
		var g, b, sp *big.Rat
		for _, rv := range pv.Resources {
			if rv.Resource == r.Resource {
				g, b, sp = ratOf(rv.Guaranteed), ratOf(rv.Burstable), ratOf(rv.Spot)
			}
		}
		maths[r.Resource] = capacity.Compute(poolResourceState(pool, r, g, b, sp))
	}
	shapeMap := map[string]CapacityShape{}
	// The classes a SKU is placed at HERE, through the exact placement or the
	// family that takes it — the same match the attribution uses.
	placedSKUs := make([]string, 0, len(pv.Placements))
	for _, pl := range pv.Placements {
		placedSKUs = append(placedSKUs, pl.SKU)
	}
	classesOf := func(sku string) []string {
		via := capacity.BestMatches(placedSKUs, sku)
		var out []string
		for _, pl := range pv.Placements {
			if via != "" && strings.EqualFold(pl.SKU, via) {
				out = append(out, pl.Class)
			}
		}
		return out
	}
	items := make([]CapacityBasketItem, 0, len(req))
	for _, r := range req {
		sh, err := s.shapeOf(ctx, r.SKU)
		if err != nil {
			return CapacityPoolView{}, err
		}
		shapeMap[r.SKU] = sh
		class := strings.ToLower(strings.TrimSpace(r.Class))
		if class == "" {
			// No class stated: the most conservative one the SKU is placed at
			// here, and GUARANTEED for a SKU the operator is only considering
			// — it is the class that must be physically backed, so it is the
			// honest default for a "would this fit?" question.
			class = capacity.ClassGuaranteed
			if res, ok := capacity.ResolveClass("", "", classesOf(r.SKU)); ok {
				class = res.Class
			}
		}
		if !capacity.ValidClass(class) {
			return CapacityPoolView{}, fmt.Errorf("%w: class must be one of %s", ErrInvalid, strings.Join(capacity.ClassKeys(), ", "))
		}
		if !capacity.HasClass(pool.Classes, class) {
			return CapacityPoolView{}, fmt.Errorf("%w: pool %s does not enforce %s, so nothing sold at it can fit here", ErrInvalid, pool.Name, class)
		}
		units := strings.TrimSpace(string(r.Units))
		if !validNonNegativeDecimal(units) {
			return CapacityPoolView{}, ErrInvalid
		}
		items = append(items, CapacityBasketItem{SKU: r.SKU, Units: Decimal(units), Class: class, Shape: sh.Resources})
	}
	pv.Basket = basketFit(maths, items, shapeMap)
	return pv, nil
}
