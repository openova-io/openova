package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// CAPPED OR GROW (DESIGN.md §22.11, founder direction 2026-10-10).
//
// A package is a PREPAID MINIMUM COMMITMENT: its price is the plan line, and
// its shape — vCPU, memory, disk, bandwidth — is an ALLOWANCE consumed first
// (§22.3). What happens at the allowance is the CUSTOMER's choice, per
// subscription (per platform Source):
//
//   - capped (the default): the Organization's quota is the package headline
//     and nothing is billed beyond the package — every quantity feature reads
//     as a hard cap whatever its cell's overage policy says;
//   - grow: the quota is raised to a CEILING and the usage above the
//     allowance is billed in arrears at the package's OVERAGE RATES — compute
//     (vCPU, memory) at the package's own rates, disk and bandwidth at the
//     book's flat meter prices.
//
// The package says whether grow is allowed, what its ceiling is (the XL shape
// by default) and its two compute rates (package_settings); the customer may
// choose a lower ceiling per dimension (never below the package headline) and
// a monthly spend limit. A cell may be GROW ONLY: the feature is available on
// that package only in grow mode, billed as usage — the active-passive DR
// topology on S, M and L.

// growModelMigrationSQL is appended at the END of migrations and located by
// content as MigrationGrowModel. Every constraint is NAMED (dberr.go).
const growModelMigrationSQL = `
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS overage_mode TEXT NOT NULL DEFAULT 'capped';
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS grow_ceiling_vcpu NUMERIC(12,4);
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS grow_ceiling_memory_gb NUMERIC(12,4);
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS grow_ceiling_disk_gb NUMERIC(12,4);
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS grow_ceiling_bandwidth_mbps NUMERIC(12,4);
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS spend_limit_month NUMERIC(20,3);
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_overage_mode_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_overage_mode_check CHECK (overage_mode IN ('capped','grow'));
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_grow_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_grow_check CHECK (overage_mode = 'grow' OR (grow_ceiling_vcpu IS NULL AND grow_ceiling_memory_gb IS NULL
	AND grow_ceiling_disk_gb IS NULL AND grow_ceiling_bandwidth_mbps IS NULL AND spend_limit_month IS NULL));
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_spend_limit_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_spend_limit_check CHECK (spend_limit_month IS NULL OR spend_limit_month > 0);

ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS grow_allowed BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS grow_ceiling_vcpu NUMERIC(12,4);
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS grow_ceiling_memory_gb NUMERIC(12,4);
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS grow_ceiling_disk_gb NUMERIC(12,4);
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS grow_ceiling_bandwidth_mbps NUMERIC(12,4);
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS overage_vcpu_month NUMERIC(12,4);
ALTER TABLE package_settings ADD COLUMN IF NOT EXISTS overage_mem_gb_month NUMERIC(12,4);
ALTER TABLE package_settings DROP CONSTRAINT IF EXISTS package_settings_grow_check;
ALTER TABLE package_settings ADD CONSTRAINT package_settings_grow_check CHECK (
	COALESCE(grow_ceiling_vcpu, 0) >= 0 AND COALESCE(grow_ceiling_memory_gb, 0) >= 0 AND COALESCE(grow_ceiling_disk_gb, 0) >= 0
	AND COALESCE(grow_ceiling_bandwidth_mbps, 0) >= 0 AND COALESCE(overage_vcpu_month, 0) >= 0 AND COALESCE(overage_mem_gb_month, 0) >= 0);

ALTER TABLE package_entitlements ADD COLUMN IF NOT EXISTS grow_only BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE package_entitlements DROP CONSTRAINT IF EXISTS package_entitlements_grow_only_check;
ALTER TABLE package_entitlements ADD CONSTRAINT package_entitlements_grow_only_check CHECK (NOT grow_only OR state = 'optional');
`

// MigrationGrowModel is the schema_migrations version of the grow migration,
// located by content like the others.
var MigrationGrowModel = func() int {
	for i, m := range migrations {
		if m == growModelMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// The two overage modes of a Source.
const (
	OverageModeCapped = "capped"
	OverageModeGrow   = "grow"
)

// ValidOverageMode reports whether m is capped or grow.
func ValidOverageMode(m string) bool { return m == OverageModeCapped || m == OverageModeGrow }

// GrowCeiling is a ceiling per dimension: vCPU, memory (GB), disk (GB) and
// bandwidth (Mbps). On a package it is the most grow may raise the quota to;
// on a Source, what the customer chose (never below the headline, never
// above the package's).
type GrowCeiling struct {
	VCPU          *Decimal `json:"vcpu,omitempty"`
	MemoryGB      *Decimal `json:"memory_gb,omitempty"`
	DiskGB        *Decimal `json:"disk_gb,omitempty"`
	BandwidthMbps *Decimal `json:"bandwidth_mbps,omitempty"`
}

// dims lists the four dimensions with their wire names, in order.
func (g *GrowCeiling) dims() []struct {
	name string
	v    **Decimal
} {
	return []struct {
		name string
		v    **Decimal
	}{{"vcpu", &g.VCPU}, {"memory_gb", &g.MemoryGB}, {"disk_gb", &g.DiskGB}, {"bandwidth_mbps", &g.BandwidthMbps}}
}

// trimDecimalPtr drops trailing zeros ("8.0000" → "8") so a stored ceiling
// reads as it was written.
func trimDecimalPtr(d *Decimal) *Decimal {
	if d == nil {
		return nil
	}
	s := strings.TrimSpace(string(*d))
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	out := Decimal(s)
	return &out
}

// PackageLimits is what a package says about grow, with the headline the
// ceiling is bounded below by. A nil headline value is one the package does
// not state (no shape, no cell); the lower bound is then not checked.
type PackageLimits struct {
	PlanSlug string
	Headline GrowCeiling
	// GrowAllowed is the package setting; Ceiling the package's ceiling.
	GrowAllowed bool
	Ceiling     GrowCeiling
	// OverageVCPUMonth / OverageMemGBMonth are the package's compute
	// overage rates per unit per month.
	OverageVCPUMonth  *Decimal
	OverageMemGBMonth *Decimal
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// cellQuantityOn is the included quantity of the quantity feature metered on
// sku in one package, or nil.
func cellQuantityOn(ctx context.Context, q queryRower, bookID, planSKU, sku string) (*Decimal, error) {
	var v sql.NullString
	err := q.QueryRowContext(ctx, `SELECT e.included_quantity::text FROM package_entitlements e JOIN features f ON f.id = e.feature_id
		WHERE e.price_book_id = $1 AND e.plan_sku = $2 AND f.kind = 'quantity' AND f.addon_sku = $3 AND e.state = 'included' LIMIT 1`, bookID, planSKU, sku).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapErr(err)
	}
	return trimDecimalPtr(decPtr(v)), nil
}

// packageLimitsOf reads one package's headline, grow setting and ceiling:
// the vCPU and memory headline from the settings (the catalog's constants
// otherwise), the disk from the settings (the disk cell otherwise) and the
// bandwidth from the bandwidth cell.
func packageLimitsOf(ctx context.Context, q queryRower, bookID, planSlug string) (PackageLimits, error) {
	planSKU := PlanSKU(planSlug)
	out := PackageLimits{PlanSlug: planSlug}
	if v, m, ok := PlanShape(planSlug); ok {
		vd, md := Decimal(fmt.Sprint(v)), Decimal(fmt.Sprint(m))
		out.Headline.VCPU, out.Headline.MemoryGB = &vd, &md
	}
	ps, err := scanPackageSettings(q.QueryRowContext(ctx, `SELECT `+packageSettingsColumns+` FROM package_settings WHERE price_book_id = $1 AND plan_sku = $2`, bookID, planSKU))
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return out, err
	default:
		if ps.VCPU != nil {
			out.Headline.VCPU = trimDecimalPtr(ps.VCPU)
		}
		if ps.MemoryGB != nil {
			out.Headline.MemoryGB = trimDecimalPtr(ps.MemoryGB)
		}
		out.Headline.DiskGB = trimDecimalPtr(ps.DiskGB)
		out.GrowAllowed = ps.GrowAllowed
		out.Ceiling = GrowCeiling{VCPU: ps.GrowCeilingVCPU, MemoryGB: ps.GrowCeilingMemoryGB, DiskGB: ps.GrowCeilingDiskGB, BandwidthMbps: ps.GrowCeilingBandwidthMbps}
		out.OverageVCPUMonth, out.OverageMemGBMonth = ps.OverageVCPUMonth, ps.OverageMemGBMonth
	}
	if out.Headline.DiskGB == nil {
		if out.Headline.DiskGB, err = cellQuantityOn(ctx, q, bookID, planSKU, SKUPVC); err != nil {
			return out, err
		}
	}
	if out.Headline.BandwidthMbps, err = cellQuantityOn(ctx, q, bookID, planSKU, SKUBandwidth); err != nil {
		return out, err
	}
	return out, nil
}

// PackageLimitsOf is packageLimitsOf on the store's connection — what the
// rating run reads a package's compute rates and headline from.
func (s *Store) PackageLimitsOf(ctx context.Context, bookID, planSlug string) (PackageLimits, error) {
	return packageLimitsOf(ctx, s.db, bookID, NormalizePlanSlug(planSlug))
}

// OverageInput is a Source's overage choice as the console, the adapter or
// the API writes it, whole.
type OverageInput struct {
	Mode            string
	Ceiling         *GrowCeiling
	SpendLimitMonth *Decimal
}

// ResolveGrowCeiling checks a customer's ceiling against a package and fills
// every dimension it leaves out with the package's ceiling: each value must
// be a non-negative number, at least the package headline (where the package
// states one) and at most the package's ceiling.
func ResolveGrowCeiling(lim PackageLimits, chosen *GrowCeiling) (GrowCeiling, error) {
	name := PlanName(lim.PlanSlug)
	var out GrowCeiling
	pkg := lim.Ceiling
	head := lim.Headline
	var in GrowCeiling
	if chosen != nil {
		in = *chosen
	}
	od, pd, hd, id := out.dims(), pkg.dims(), head.dims(), in.dims()
	for i := range od {
		dim := od[i].name
		top := *pd[i].v
		if top == nil {
			return out, fmt.Errorf("%w: the %s package states no grow ceiling for %s; set it on the package first", ErrInvalid, name, dim)
		}
		v := *id[i].v
		if v == nil || strings.TrimSpace(string(*v)) == "" {
			*od[i].v = trimDecimalPtr(top)
			continue
		}
		r, ok := new(big.Rat).SetString(strings.TrimSpace(string(*v)))
		if !ok || r.Sign() < 0 {
			return out, fmt.Errorf("%w: grow ceiling %s %q is not a non-negative number", ErrInvalid, dim, string(*v))
		}
		low := *hd[i].v
		if low != nil && r.Cmp(ratOf(*low)) < 0 {
			return out, fmt.Errorf("%w: grow ceiling %s %s is below the %s package's %s; choose between %s and %s", ErrInvalid, dim, string(*trimDecimalPtr(v)), name, string(*trimDecimalPtr(low)), string(*trimDecimalPtr(low)), string(*trimDecimalPtr(top)))
		}
		if r.Cmp(ratOf(*top)) > 0 {
			lowS := "0"
			if low != nil {
				lowS = string(*trimDecimalPtr(low))
			}
			return out, fmt.Errorf("%w: grow ceiling %s %s is above the %s package's ceiling %s; choose between %s and %s", ErrInvalid, dim, string(*trimDecimalPtr(v)), name, string(*trimDecimalPtr(top)), lowS, string(*trimDecimalPtr(top)))
		}
		*od[i].v = trimDecimalPtr(v)
	}
	return out, nil
}

// SetSourceOverage writes a platform Source's overage choice. capped clears
// the ceiling and the spend limit and refuses either being given; grow needs
// the Source on a sized package of its book whose settings allow grow, a
// ceiling inside [headline, the package's ceiling] per dimension (the
// package's ceiling where none is chosen), and an optional spend limit above
// zero.
func (s *Store) SetSourceOverage(ctx context.Context, sourceID string, in OverageInput) (CostSource, error) {
	src, err := s.GetSource(ctx, OperatorScope, sourceID)
	if err != nil {
		return CostSource{}, err
	}
	if src.Internal {
		return CostSource{}, fmt.Errorf("%w: the internal platform source is never billed and has no overage mode", ErrInvalid)
	}
	if src.Layer != LayerPlatform {
		return CostSource{}, fmt.Errorf("%w: the overage mode belongs to a platform source on a package; %s is a %s source", ErrInvalid, src.Label(), src.Layer)
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "" {
		mode = OverageModeCapped
	}
	if !ValidOverageMode(mode) {
		return CostSource{}, fmt.Errorf("%w: the overage mode is capped or grow", ErrInvalid)
	}
	var spend any
	if in.SpendLimitMonth != nil && strings.TrimSpace(string(*in.SpendLimitMonth)) != "" {
		r, ok := new(big.Rat).SetString(strings.TrimSpace(string(*in.SpendLimitMonth)))
		if !ok || r.Sign() <= 0 {
			return CostSource{}, fmt.Errorf("%w: a spend limit is an amount above zero", ErrInvalid)
		}
		spend = strings.TrimSpace(string(*in.SpendLimitMonth))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CostSource{}, err
	}
	defer tx.Rollback()
	var ceil GrowCeiling
	if mode == OverageModeCapped {
		if in.Ceiling != nil && (in.Ceiling.VCPU != nil || in.Ceiling.MemoryGB != nil || in.Ceiling.DiskGB != nil || in.Ceiling.BandwidthMbps != nil) {
			return CostSource{}, fmt.Errorf("%w: a grow ceiling applies in grow mode; in capped mode the quota is the package headline", ErrInvalid)
		}
		if spend != nil {
			return CostSource{}, fmt.Errorf("%w: a spend limit applies to what grow bills; in capped mode nothing is billed beyond the package", ErrInvalid)
		}
	} else {
		if src.PriceBookID == nil {
			return CostSource{}, fmt.Errorf("%w: %s has no price book, so no package offers it grow; assign the plans book first", ErrInvalid, src.Label())
		}
		var planSlug string
		if err := tx.QueryRowContext(ctx, `SELECT plan_slug FROM customers WHERE id = $1`, src.CustomerID).Scan(&planSlug); err != nil {
			return CostSource{}, mapErr(err)
		}
		planSlug = NormalizePlanSlug(planSlug)
		if !PlanBillable(planSlug) {
			return CostSource{}, fmt.Errorf("%w: %s is not on a sized package (plan %q); grow is a mode of a package", ErrInvalid, src.Label(), planSlug)
		}
		lim, err := packageLimitsOf(ctx, tx, *src.PriceBookID, planSlug)
		if err != nil {
			return CostSource{}, err
		}
		if !lim.GrowAllowed {
			return CostSource{}, fmt.Errorf("%w: the %s package does not offer grow mode", ErrInvalid, PlanName(planSlug))
		}
		if ceil, err = ResolveGrowCeiling(lim, in.Ceiling); err != nil {
			return CostSource{}, err
		}
	}
	arg := func(d *Decimal) any {
		if d == nil {
			return nil
		}
		return string(*d)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cost_sources SET overage_mode = $2, grow_ceiling_vcpu = $3::numeric, grow_ceiling_memory_gb = $4::numeric,
		grow_ceiling_disk_gb = $5::numeric, grow_ceiling_bandwidth_mbps = $6::numeric, spend_limit_month = $7::numeric WHERE id = $1`,
		src.ID, mode, arg(ceil.VCPU), arg(ceil.MemoryGB), arg(ceil.DiskGB), arg(ceil.BandwidthMbps), spend); err != nil {
		return CostSource{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return CostSource{}, err
	}
	return s.GetSource(ctx, OperatorScope, src.ID)
}

// SameOverage reports whether a Source already carries the given choice —
// the adapter's "write only when it differs" test. The ceiling compares
// resolved values, so an order that left a dimension to the package and a
// Source that stored the package's value read as the same.
func SameOverage(src CostSource, mode string, ceiling *GrowCeiling, spend *Decimal) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = OverageModeCapped
	}
	have0 := src.OverageMode
	if have0 == "" {
		have0 = OverageModeCapped
	}
	if have0 != mode {
		return false
	}
	eq := func(a, b *Decimal) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return ratOf(*a).Cmp(ratOf(*b)) == 0
	}
	if !eq(src.SpendLimitMonth, spend) {
		return false
	}
	if mode == OverageModeCapped {
		return true
	}
	have := GrowCeiling{}
	if src.GrowCeiling != nil {
		have = *src.GrowCeiling
	}
	want := GrowCeiling{}
	if ceiling != nil {
		want = *ceiling
	}
	hd, wd := have.dims(), want.dims()
	for i := range hd {
		if *wd[i].v != nil && !eq(*hd[i].v, *wd[i].v) {
			return false
		}
	}
	return true
}
