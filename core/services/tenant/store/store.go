package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openova-io/openova/core/services/shared/events"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Tenant represents an organization on the platform.
type Tenant struct {
	ID       string `bson:"_id" json:"id"`
	Slug     string `bson:"slug" json:"slug"`
	Name     string `bson:"name" json:"name"`
	OrgType  string `bson:"org_type" json:"org_type"`
	Industry string `bson:"industry" json:"industry"`
	OwnerID  string `bson:"owner_id" json:"owner_id"`
	// OwnerEmail is the owner's email captured at CreateOrg from the caller's
	// JWT claim. It is persisted so the DEFERRED-launch path (#4956) can emit
	// the `tenant.created` event — which the provisioning consumer requires an
	// owner_email on to mint the Organization CR — long after the original HTTP
	// request's claims are gone (the launch is triggered server-to-server by
	// billing on settlement, with no user context). Empty on legacy records /
	// callers whose JWT lacked the claim (the immediate path still derives it
	// from claims inline).
	OwnerEmail string   `bson:"owner_email,omitempty" json:"owner_email,omitempty"`
	PlanID     string   `bson:"plan_id" json:"plan_id"`
	Apps       []string `bson:"apps" json:"apps"`
	// Agents carries the Sandbox coding-agent picks from the marketplace
	// AppDetail surface. Persisted (#4956) so the deferred-launch path can emit
	// `tenant.sandbox_requested` with the chosen catalogue on settlement — the
	// request-body Agents list is otherwise gone by launch time. Only acted on
	// when Apps contains "sandbox". Tolerated empty.
	Agents []string `bson:"agents,omitempty" json:"agents,omitempty"`
	// AppStates tracks per-app lifecycle (keyed by app ID). Values:
	// "installing" | "uninstalling" | "failed". Absent means the app is in
	// its steady state (installed when the ID is in Apps; gone otherwise).
	AppStates map[string]string `bson:"app_states,omitempty" json:"app_states,omitempty"`
	// AppConfigs carries per-instance configSchema values chosen by
	// the customer on the marketplace AppDetail surface, keyed by app
	// SLUG (e.g. "postgres" or "wordpress"). The inner map keys are
	// `ConfigField.Key` names (e.g. "replicas", "disk_gb",
	// "backups_enabled") and values are the field-typed primitives
	// (int / string / bool). Empty when no app in the cart shipped a
	// configSchema (Ghost / Nextcloud today) or when the cart predates
	// TBD-V18-D (#2026 follow-up to PR #2038). Down-stream consumers
	// (provisioning, blueprint-controller) read this when rendering
	// HelmRelease values — the actual binding lands behind the
	// TBD-V26 (#2040) Path A/B decision; this field threads the
	// SHAPE end-to-end so the binding lights up without a second
	// upstream change.
	AppConfigs map[string]map[string]any `bson:"app_configs,omitempty" json:"app_configs,omitempty"`
	// AddOns is the cart's ONE add-on list exactly as the storefront sent it
	// (core/marketplace/src/lib/cart.ts): catalog add-on ids, or BSS add-on
	// SKUs such as "addon.backup" when the Sovereign's BSS published the
	// package document (#6971). Billing's settlement launch merges the priced
	// `addon.*` SKUs into it (SetCommerce). The `tenant.created` emitter
	// filters it through events.BSSAddonSKUs so the Organization CR carries
	// BSS SKUs only.
	AddOns    []string `bson:"addons" json:"addons"`
	Subdomain string   `bson:"subdomain" json:"subdomain"`

	// Commerce provenance — #6971 item 8. What was bought and where the
	// price came from, so the Organization CR minted from `tenant.created`
	// carries `spec.commerce` and the chargeback adapter can attach the
	// Organization's platform Source to its package + add-on lines.
	//
	// PackageSKU is the BSS package ("plan.m") the storefront sends beside
	// `plan_id` on POST /tenant/orgs; PlanID stays the catalog plan id the
	// provisioning consumer resolves to spec.planSlug. PriceSource ("catalog"
	// | "bss:<price_book>@<prices_as_of>") and OrderID (billing `orders.id`)
	// arrive from billing's settlement launch body (#4956 deferred path) and
	// WIN over whatever the create request carried — the order is the source.
	//
	// Migration: FerretDB is schemaless, so the `omitempty` bson tags ARE the
	// migration — a record written before these fields decodes with empty
	// values and the emitter leaves `spec.commerce` absent (legacy behaviour
	// byte-for-byte).
	PackageSKU  string `bson:"package_sku,omitempty" json:"package_sku,omitempty"`
	PriceSource string `bson:"price_source,omitempty" json:"price_source,omitempty"`
	OrderID     string `bson:"order_id,omitempty" json:"order_id,omitempty"`
	// Overage (founder model 2026-10-10). OverageMode is "capped" or "grow";
	// GrowCeiling is the resolved quota ceiling in grow mode; SpendLimitMonth
	// the optional monthly overage spend limit ("25.000"). Billing's
	// settlement launch body is the source and wins over create-time values.
	// omitempty — absent on every record written before them.
	OverageMode     string              `bson:"overage_mode,omitempty" json:"overage_mode,omitempty"`
	GrowCeiling     *events.GrowCeiling `bson:"grow_ceiling,omitempty" json:"grow_ceiling,omitempty"`
	SpendLimitMonth string              `bson:"spend_limit_month,omitempty" json:"spend_limit_month,omitempty"`
	// ParentDomain — the org-pool parent apex the customer chose at the
	// /addons step (e.g. "omani.works"). #4176/#4179: the per-Org console
	// lives at `console.<subdomain>.<parent_domain>`. On a Sovereign whose
	// marketplace runs on the Sovereign domain (marketplace.omantel.biz)
	// while Orgs provision on a SEPARATE pool domain (omani.works), this is
	// the ONLY field that carries the chosen pool apex — without it the
	// console host is mis-derived to console.<slug>.omantel.biz (unreachable)
	// so every org-create lands on a dead host. Empty on legacy records
	// (callers fall back to the Sovereign FQDN, preserving back-compat).
	ParentDomain  string    `bson:"parent_domain,omitempty" json:"parent_domain,omitempty"`
	CustomDomains []string  `bson:"custom_domains" json:"custom_domains"`
	Status        string    `bson:"status" json:"status"` // active, suspended, provisioning, deleted
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at" json:"updated_at"`
}

// Member represents a user's membership in a tenant.
type Member struct {
	ID       string    `bson:"_id" json:"id"`
	TenantID string    `bson:"tenant_id" json:"tenant_id"`
	UserID   string    `bson:"user_id" json:"user_id"`
	Email    string    `bson:"email" json:"email"`
	Role     string    `bson:"role" json:"role"` // owner, admin, member, viewer
	JoinedAt time.Time `bson:"joined_at" json:"joined_at"`
}

// Store provides CRUD operations against a FerretDB (MongoDB wire protocol) database.
type Store struct {
	db *mongo.Database
}

// New creates a Store backed by the given database.
func New(client *mongo.Client, dbName string) *Store {
	return &Store{db: client.Database(dbName)}
}

func (s *Store) tenants() *mongo.Collection { return s.db.Collection("tenants") }
func (s *Store) members() *mongo.Collection { return s.db.Collection("members") }

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

// CreateTenant inserts a new tenant. If ID is empty, a UUID is generated.
func (s *Store) CreateTenant(ctx context.Context, t *Tenant) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Apps == nil {
		t.Apps = []string{}
	}
	if t.AddOns == nil {
		t.AddOns = []string{}
	}
	if t.CustomDomains == nil {
		t.CustomDomains = []string{}
	}
	_, err := s.tenants().InsertOne(ctx, t)
	if err != nil {
		return fmt.Errorf("store: create tenant: %w", err)
	}
	return nil
}

// GetTenant returns a single tenant by ID.
func (s *Store) GetTenant(ctx context.Context, id string) (*Tenant, error) {
	var t Tenant
	err := s.tenants().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&t)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get tenant %s: %w", id, err)
	}
	return &t, nil
}

// GetTenantBySlug returns a single tenant by slug.
func (s *Store) GetTenantBySlug(ctx context.Context, slug string) (*Tenant, error) {
	var t Tenant
	err := s.tenants().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&t)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get tenant by slug %s: %w", slug, err)
	}
	return &t, nil
}

// UpdateTenantStatus updates only the status field for a tenant.
// Used by the provision event consumer to reflect lifecycle state.
func (s *Store) UpdateTenantStatus(ctx context.Context, id, status string) error {
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: status},
		{Key: "updated_at", Value: time.Now().UTC()},
	}}}
	res, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update tenant status %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: tenant %s not found", id)
	}
	return nil
}

// TryTransitionTenantStatus atomically flips a tenant's status from `from` to
// `to`, but ONLY if it currently equals `from`. It returns true iff the row was
// matched-and-updated (i.e. this caller won the transition), false if the
// tenant's status was not `from` (already transitioned by a concurrent caller,
// or never in that state). This is the idempotency + race guard for the #4956
// deferred launch: the settlement trigger flips `pending_payment → provisioning`
// exactly once even if billing dispatches order.placed more than once (credit +
// Stripe-retry), so the Org CR / cart-install fire a single time.
func (s *Store) TryTransitionTenantStatus(ctx context.Context, id, from, to string) (bool, error) {
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "status", Value: to},
		{Key: "updated_at", Value: time.Now().UTC()},
	}}}
	res, err := s.tenants().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "status", Value: from}}, update)
	if err != nil {
		return false, fmt.Errorf("store: transition tenant status %s (%s→%s): %w", id, from, to, err)
	}
	return res.ModifiedCount > 0, nil
}

// Commerce is the purchase billing hands this service at settlement
// (#6971 item 8): the fields SetCommerce writes onto a Tenant record. Empty scalars
// are left untouched (never cleared); Addons are ADDED to the cart list, never
// removed, so a re-delivered settlement is a no-op.
type Commerce struct {
	PackageSKU  string
	PriceSource string
	OrderID     string
	Addons      []string
	// OverageMode / GrowCeiling / SpendLimitMonth are $set when non-empty.
	// ClearGrow $unsets grow_ceiling and spend_limit_month: the order says
	// capped, so a create-time grow ceiling or spend limit no longer holds.
	OverageMode     string
	GrowCeiling     *events.GrowCeiling
	SpendLimitMonth string
	ClearGrow       bool
}

// IsZero reports whether the Commerce carries nothing to write.
func (c Commerce) IsZero() bool {
	return c.PackageSKU == "" && c.PriceSource == "" && c.OrderID == "" && len(c.Addons) == 0 &&
		c.OverageMode == "" && c.GrowCeiling.IsZero() && c.SpendLimitMonth == "" && !c.ClearGrow
}

// commerceUpdate is the targeted update SetCommerce sends. Pure, so the
// update shape is unit-tested without a database.
func commerceUpdate(c Commerce, now time.Time) bson.D {
	setFields := bson.D{{Key: "updated_at", Value: now}}
	if c.PackageSKU != "" {
		setFields = append(setFields, bson.E{Key: "package_sku", Value: c.PackageSKU})
	}
	if c.PriceSource != "" {
		setFields = append(setFields, bson.E{Key: "price_source", Value: c.PriceSource})
	}
	if c.OrderID != "" {
		setFields = append(setFields, bson.E{Key: "order_id", Value: c.OrderID})
	}
	if c.OverageMode != "" {
		setFields = append(setFields, bson.E{Key: "overage_mode", Value: c.OverageMode})
	}
	if !c.GrowCeiling.IsZero() {
		setFields = append(setFields, bson.E{Key: "grow_ceiling", Value: *c.GrowCeiling})
	}
	if c.SpendLimitMonth != "" {
		setFields = append(setFields, bson.E{Key: "spend_limit_month", Value: c.SpendLimitMonth})
	}
	update := bson.D{{Key: "$set", Value: setFields}}
	if c.ClearGrow {
		unset := bson.D{}
		if c.GrowCeiling.IsZero() {
			unset = append(unset, bson.E{Key: "grow_ceiling", Value: ""})
		}
		if c.SpendLimitMonth == "" {
			unset = append(unset, bson.E{Key: "spend_limit_month", Value: ""})
		}
		if len(unset) > 0 {
			update = append(update, bson.E{Key: "$unset", Value: unset})
		}
	}
	if len(c.Addons) > 0 {
		update = append(update, bson.E{Key: "$addToSet", Value: bson.D{
			{Key: "addons", Value: bson.D{{Key: "$each", Value: c.Addons}}},
		}})
	}
	return update
}

// SetCommerce stamps the purchase onto a Tenant record in ONE targeted update:
// `$set` for each non-empty scalar (package_sku / price_source / order_id /
// overage_mode / grow_ceiling / spend_limit_month), `$unset` of the grow
// fields when the order is capped (ClearGrow), and `$addToSet $each` for the
// add-on SKUs. Targeted rather than the
// whole-document UpdateTenant so a concurrent day-2 AtomicAppendApps on the
// same record is not overwritten (the lost-update class dod-chaos scenario7
// found). Idempotent: $addToSet ignores SKUs already present and re-setting
// the same scalar is a no-op, so billing's redelivered settlement (credit +
// Stripe-retry, #4956) leaves the record unchanged. A zero Commerce returns
// nil without a round-trip.
func (s *Store) SetCommerce(ctx context.Context, id string, c Commerce) error {
	if id == "" || c.IsZero() {
		return nil
	}
	update := commerceUpdate(c, time.Now().UTC())
	res, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: set commerce %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: set commerce: organization %s not found", id)
	}
	return nil
}

// SetAppState sets AppStates[appID] = state on the given tenant.
func (s *Store) SetAppState(ctx context.Context, tenantID, appID, state string) error {
	if tenantID == "" || appID == "" {
		return nil
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "app_states." + appID, Value: state},
		{Key: "updated_at", Value: time.Now().UTC()},
	}}}
	_, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: tenantID}}, update)
	if err != nil {
		return fmt.Errorf("store: set app state %s/%s: %w", tenantID, appID, err)
	}
	return nil
}

// ClearAppState removes AppStates[appID] from the given tenant.
func (s *Store) ClearAppState(ctx context.Context, tenantID, appID string) error {
	if tenantID == "" || appID == "" {
		return nil
	}
	update := bson.D{
		{Key: "$unset", Value: bson.D{{Key: "app_states." + appID, Value: ""}}},
		{Key: "$set", Value: bson.D{{Key: "updated_at", Value: time.Now().UTC()}}},
	}
	_, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: tenantID}}, update)
	if err != nil {
		return fmt.Errorf("store: clear app state %s/%s: %w", tenantID, appID, err)
	}
	return nil
}

// RemoveAppFromTenant pulls appID from the apps array and clears its AppStates entry.
func (s *Store) RemoveAppFromTenant(ctx context.Context, tenantID, appID string) error {
	if tenantID == "" || appID == "" {
		return nil
	}
	update := bson.D{
		{Key: "$pull", Value: bson.D{{Key: "apps", Value: appID}}},
		{Key: "$unset", Value: bson.D{{Key: "app_states." + appID, Value: ""}}},
		{Key: "$set", Value: bson.D{{Key: "updated_at", Value: time.Now().UTC()}}},
	}
	_, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: tenantID}}, update)
	if err != nil {
		return fmt.Errorf("store: remove app %s/%s: %w", tenantID, appID, err)
	}
	return nil
}

// UpdateTenant updates a tenant by _id, setting updated_at.
func (s *Store) UpdateTenant(ctx context.Context, id string, t *Tenant) error {
	t.UpdatedAt = time.Now().UTC()
	update := bson.D{{Key: "$set", Value: t}}
	res, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update tenant %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: tenant %s not found", id)
	}
	return nil
}

// AtomicAppendApps atomically adds newAppIDs to tenant.apps via $addToSet and
// merges app_states entries via $set on dotted keys. Fixes the lost-update
// race where 3 concurrent InstallApp calls on the same tenant all read the
// same tenant.apps, append their own id, and overwrite each other — net
// result: only one or two of the three apps end up recorded. Found by
// dod-chaos scenario7 (concurrent-day2). Issue discovered 2026-04-20.
//
// $addToSet is idempotent so the 'already installed' fast-path in the
// handler remains correct even if two concurrent callers race past it.
func (s *Store) AtomicAppendApps(ctx context.Context, id string, newAppIDs []string, appStates map[string]string) error {
	setFields := bson.D{{Key: "updated_at", Value: time.Now().UTC()}}
	for k, v := range appStates {
		setFields = append(setFields, bson.E{Key: "app_states." + k, Value: v})
	}
	update := bson.D{
		{Key: "$addToSet", Value: bson.D{
			{Key: "apps", Value: bson.D{{Key: "$each", Value: newAppIDs}}},
		}},
		{Key: "$set", Value: setFields},
	}
	res, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: atomic append apps %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: tenant %s not found", id)
	}
	return nil
}

// AtomicRemoveApp is the uninstall counterpart: $pull removes the app id from
// tenant.apps (idempotent — noop if already removed) and $set writes the
// target app's app_states entry. Mirrors AtomicAppendApps for day-2 uninstall.
func (s *Store) AtomicRemoveApp(ctx context.Context, id, appID, appState string) error {
	update := bson.D{
		{Key: "$pull", Value: bson.D{{Key: "apps", Value: appID}}},
		{Key: "$set", Value: bson.D{
			{Key: "updated_at", Value: time.Now().UTC()},
			{Key: "app_states." + appID, Value: appState},
		}},
	}
	res, err := s.tenants().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: atomic remove app %s/%s: %w", id, appID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: tenant %s not found", id)
	}
	return nil
}

// ListTenantsByOwner returns all tenants where the given user is a member.
func (s *Store) ListTenantsByOwner(ctx context.Context, ownerID string) ([]Tenant, error) {
	// First, find all tenant IDs where this user is a member.
	cursor, err := s.members().Find(ctx, bson.D{{Key: "user_id", Value: ownerID}})
	if err != nil {
		return nil, fmt.Errorf("store: list memberships for %s: %w", ownerID, err)
	}
	var memberships []Member
	if err := cursor.All(ctx, &memberships); err != nil {
		return nil, fmt.Errorf("store: decode memberships: %w", err)
	}

	if len(memberships) == 0 {
		return []Tenant{}, nil
	}

	tenantIDs := make([]string, len(memberships))
	for i, m := range memberships {
		tenantIDs[i] = m.TenantID
	}

	// Fetch tenants by IDs, excluding deleted.
	filter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: tenantIDs}}},
		{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}},
	}
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	tCursor, err := s.tenants().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list tenants by owner: %w", err)
	}
	var tenants []Tenant
	if err := tCursor.All(ctx, &tenants); err != nil {
		return nil, fmt.Errorf("store: decode tenants: %w", err)
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	return tenants, nil
}

// DeleteTenant removes a tenant by _id AND every member row attached to it
// (hard delete, cascade). Before issue #96 this only removed the tenant
// document — the members collection kept rows pointing at a tenant that no
// longer existed, which (a) let stale membership checks drift if a slug were
// reused and (b) left operator tooling showing ghost members.
//
// Semantics:
//   - Members are deleted first so an error midway can't leave the tenant
//     gone but members still orphaned.
//   - "Tenant not found" is still returned (and the member delete is a
//     no-op) so callers that relied on the sentinel behavior keep working.
//   - Redeliveries of `provision.tenant_removed` hit this path after a
//     prior successful run: tenant absent → we now treat that as success so
//     the consumer commits the offset. DeleteMany on an empty set is a
//     no-op, which is the entire cascade — idempotent by construction.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	if _, err := s.members().DeleteMany(ctx, bson.D{{Key: "tenant_id", Value: id}}); err != nil {
		return fmt.Errorf("store: delete members for tenant %s: %w", id, err)
	}

	res, err := s.tenants().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete tenant %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		// Idempotent: a redelivered provision.tenant_removed event after a
		// successful cascade is a no-op, not an error. Members were already
		// purged above (second DeleteMany is also a no-op). Returning nil
		// lets the consumer commit the offset rather than re-queuing
		// forever.
		return nil
	}
	return nil
}

// CheckSlugAvailable returns true if no tenant uses the given slug.
func (s *Store) CheckSlugAvailable(ctx context.Context, slug string) (bool, error) {
	count, err := s.tenants().CountDocuments(ctx, bson.D{{Key: "slug", Value: slug}})
	if err != nil {
		return false, fmt.Errorf("store: check slug %s: %w", slug, err)
	}
	return count == 0, nil
}

// ListAllTenants returns a paginated list of all tenants and the total count (admin).
func (s *Store) ListAllTenants(ctx context.Context, offset, limit int) ([]Tenant, int64, error) {
	total, err := s.tenants().CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, 0, fmt.Errorf("store: count tenants: %w", err)
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64(offset)).
		SetLimit(int64(limit))
	cursor, err := s.tenants().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list all tenants: %w", err)
	}
	var tenants []Tenant
	if err := cursor.All(ctx, &tenants); err != nil {
		return nil, 0, fmt.Errorf("store: decode all tenants: %w", err)
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	return tenants, total, nil
}

// SearchTenants searches tenants by name or slug (admin).
func (s *Store) SearchTenants(ctx context.Context, query string) ([]Tenant, error) {
	q := strings.ToLower(query)
	// FerretDB does not support $text indexes; use $or with $regex.
	filter := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: q}, {Key: "$options", Value: "i"}}}},
		bson.D{{Key: "slug", Value: bson.D{{Key: "$regex", Value: q}, {Key: "$options", Value: "i"}}}},
	}}}
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.tenants().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: search tenants: %w", err)
	}
	var tenants []Tenant
	if err := cursor.All(ctx, &tenants); err != nil {
		return nil, fmt.Errorf("store: decode search results: %w", err)
	}
	if tenants == nil {
		tenants = []Tenant{}
	}
	return tenants, nil
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

// AddMember inserts a new member. If ID is empty, a UUID is generated.
func (s *Store) AddMember(ctx context.Context, m *Member) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.JoinedAt.IsZero() {
		m.JoinedAt = time.Now().UTC()
	}
	_, err := s.members().InsertOne(ctx, m)
	if err != nil {
		return fmt.Errorf("store: add member: %w", err)
	}
	return nil
}

// ListMembers returns all members for a tenant.
func (s *Store) ListMembers(ctx context.Context, tenantID string) ([]Member, error) {
	cursor, err := s.members().Find(ctx, bson.D{{Key: "tenant_id", Value: tenantID}})
	if err != nil {
		return nil, fmt.Errorf("store: list members for %s: %w", tenantID, err)
	}
	var members []Member
	if err := cursor.All(ctx, &members); err != nil {
		return nil, fmt.Errorf("store: decode members: %w", err)
	}
	if members == nil {
		members = []Member{}
	}
	return members, nil
}

// RemoveMember removes a member by tenant ID and user ID.
func (s *Store) RemoveMember(ctx context.Context, tenantID, userID string) error {
	filter := bson.D{
		{Key: "tenant_id", Value: tenantID},
		{Key: "user_id", Value: userID},
	}
	res, err := s.members().DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("store: remove member %s from %s: %w", userID, tenantID, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: member %s not found in tenant %s", userID, tenantID)
	}
	return nil
}

// DeleteMembersByTenant removes every member row whose tenant_id matches.
// Used by the members-cleanup consumer (issue #96) to purge membership
// state as soon as a tenant is soft-deleted, so authz checks run against a
// pre-cleaned members collection for the rest of the teardown window.
//
// Idempotent: an already-empty match set returns (0, nil).
func (s *Store) DeleteMembersByTenant(ctx context.Context, tenantID string) (int64, error) {
	if tenantID == "" {
		return 0, nil
	}
	res, err := s.members().DeleteMany(ctx, bson.D{{Key: "tenant_id", Value: tenantID}})
	if err != nil {
		return 0, fmt.Errorf("store: delete members for tenant %s: %w", tenantID, err)
	}
	return res.DeletedCount, nil
}

// GetMemberRole returns the role for a user in a tenant, or empty string if not a member.
func (s *Store) GetMemberRole(ctx context.Context, tenantID, userID string) (string, error) {
	filter := bson.D{
		{Key: "tenant_id", Value: tenantID},
		{Key: "user_id", Value: userID},
	}
	var m Member
	err := s.members().FindOne(ctx, filter).Decode(&m)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", nil
		}
		return "", fmt.Errorf("store: get member role: %w", err)
	}
	return m.Role, nil
}

// GetMemberEmail returns the email recorded on the member row, or
// empty string when the member does not exist. Used by the sandbox
// orchestrator (handlers/sandbox_consumer.go) to enrich the
// tenant.sandbox_requested event with the owner's email when the
// publisher did not inline it. Members inserted via CreateOrg do not
// currently set Email (only UserID), so this returns "" for the
// owner-just-created path — the orchestrator then falls back to
// owner_id. The method exists for the eventual InviteMember path where
// Email is populated up-front.
func (s *Store) GetMemberEmail(ctx context.Context, tenantID, userID string) (string, error) {
	filter := bson.D{
		{Key: "tenant_id", Value: tenantID},
		{Key: "user_id", Value: userID},
	}
	var m Member
	err := s.members().FindOne(ctx, filter).Decode(&m)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", nil
		}
		return "", fmt.Errorf("store: get member email: %w", err)
	}
	return m.Email, nil
}
