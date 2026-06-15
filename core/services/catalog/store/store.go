package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// App represents a deployable application in the catalog.
type App struct {
	ID              string    `bson:"_id" json:"id"`
	Slug            string    `bson:"slug" json:"slug"`
	Name            string    `bson:"name" json:"name"`
	Tagline         string    `bson:"tagline" json:"tagline"`
	Description     string    `bson:"description" json:"description"`
	Category        string    `bson:"category" json:"category"`
	Tags            []string  `bson:"tags" json:"tags"`
	Icon            string    `bson:"icon" json:"icon"`
	// IconLight / IconDark are theme-specific icon overrides (#3602,
	// EPIC #3597). The console renders IconLight in the light theme and
	// IconDark in the dark theme; each may be a URL or an inline data:
	// URI (the admin-edit form supports both upload and URL). Both are
	// optional and ADDITIVE over the legacy single `Icon`: an empty
	// theme-icon falls back to `Icon`, so existing catalog rows that only
	// carry `Icon` keep rendering unchanged. `Icon` stays the canonical
	// default — these two are kept alongside it for back-compat.
	IconLight       string    `bson:"icon_light,omitempty" json:"icon_light,omitempty"`
	IconDark        string    `bson:"icon_dark,omitempty" json:"icon_dark,omitempty"`
	IconBg          string    `bson:"icon_bg" json:"icon_bg"`
	// SupportedTopologies is the admin-editable set of placement modes
	// this catalog entry advertises (e.g. ["single-region",
	// "active-active", "active-hotstandby"]) — #3602, EPIC #3597. The
	// canonical topology declaration still lives on the Blueprint
	// (spec.topology.supported); this field lets the sovereign-admin
	// curate which of those modes the catalog surfaces per entry without
	// editing the Blueprint. Empty = no admin override; the catalog read
	// API keeps the Blueprint/seed's own placement set. Additive — never
	// overwrites the seed when unset.
	SupportedTopologies []string `bson:"supported_topologies,omitempty" json:"supported_topologies,omitempty"`
	MinimumSize     string    `bson:"minimum_size" json:"minimum_size"`
	RecommendedSize string    `bson:"recommended_size" json:"recommended_size"`
	Website         string    `bson:"website" json:"website"`
	License         string    `bson:"license" json:"license"`
	Featured        bool      `bson:"featured" json:"featured"`
	Popular         bool      `bson:"popular" json:"popular"`
	Free            bool      `bson:"free" json:"free"`
	Features        []string  `bson:"features" json:"features"`
	RelatedApps     []string  `bson:"related_apps" json:"related_apps"`
	// Dependencies is a list of app slugs that must be provisioned alongside
	// this app (e.g., wordpress → ["mysql"]). The provisioning service treats
	// each dependency as a full first-class app deploy.
	Dependencies    []string  `bson:"dependencies" json:"dependencies"`
	// System marks infrastructure apps (mysql, postgres, redis) that are
	// selectable as dependencies in the admin UI but hidden from the public
	// marketplace.
	System          bool      `bson:"system" json:"system"`
	// Kind classifies how the app appears in the console: "business" shows
	// up as a first-class card, "service" is a backing service (database,
	// cache, queue) that renders in the muted Backing services section.
	// Defaults to "business" when empty.
	Kind            string    `bson:"kind,omitempty" json:"kind,omitempty"`
	// Shareable=true allows more than one business app to reuse a single
	// instance of this app as a dependency (e.g., one MySQL shared by
	// WordPress and Matomo). The default (false) means each dependent gets
	// its own dedicated instance.
	Shareable       bool      `bson:"shareable,omitempty" json:"shareable,omitempty"`
	// ConfigSchema declares the tunables that the console renders on the
	// app detail page (e.g., CNPG replicas, disk size). Empty = no tunables.
	ConfigSchema    []ConfigField `bson:"config_schema,omitempty" json:"config_schema,omitempty"`
	// Deployable=false means the catalog listing is visible but day-2 installs
	// will be rejected with a clear error. The marketplace UI surfaces these
	// as "Coming soon". Defaults to false for safety — apps must be explicitly
	// enabled once the provisioning template (KnownApps in
	// services/provisioning/gitops/apps.go) can deploy them end-to-end.
	// See issue #102 — before this flag, unknown slugs silently deployed an
	// nginx placeholder that the UI reported as "installed".
	Deployable      bool      `bson:"deployable,omitempty" json:"deployable"`
	// Published=true means marketplace storefront customers see this app on
	// `marketplace.<sov>`. The Sovereign-console operator owns this flag —
	// flipping it to false hides the app from marketplace customers while
	// existing tenant deployments keep running unaffected (per founder rule
	// 2026-05-04: unpublish is a marketplace-visibility toggle, not a
	// deployment-lifecycle action). Backing services (System=true) and apps
	// with Deployable=false are NEVER shown in marketplace regardless of
	// this flag — Published is the operator-facing curation knob, not the
	// internal-eligibility gate.
	//
	// Default true on existing rows via seed.go's published-migration so the
	// flag introduction doesn't silently hide every app on the day Catalyst
	// 1.3.x ships. Operators opt OUT of marketplace visibility per app, not
	// IN — matches how a SaaS team curates a real storefront.
	Published       bool      `bson:"published,omitempty" json:"published"`
	RamMB           int       `bson:"ram_mb" json:"ram_mb"`
	CpuMilli        int       `bson:"cpu_milli" json:"cpu_milli"`
	DiskGB          int       `bson:"disk_gb" json:"disk_gb"`
	HelmChart       string    `bson:"helm_chart" json:"helm_chart"`
	HelmRepo        string    `bson:"helm_repo" json:"helm_repo"`
	CreatedAt       time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt       time.Time `bson:"updated_at" json:"updated_at"`
}

// ConfigField declares one tunable on an app. The console renders a matching
// input widget per Type ("int" | "string" | "bool" | "enum" | "size").
// Advanced fields live behind an "Advanced" toggle on the detail page.
type ConfigField struct {
	Key         string   `bson:"key" json:"key"`
	Label       string   `bson:"label" json:"label"`
	Type        string   `bson:"type" json:"type"`
	Default     any      `bson:"default,omitempty" json:"default,omitempty"`
	Min         *int     `bson:"min,omitempty" json:"min,omitempty"`
	Max         *int     `bson:"max,omitempty" json:"max,omitempty"`
	Options     []string `bson:"options,omitempty" json:"options,omitempty"`
	Description string   `bson:"description,omitempty" json:"description,omitempty"`
	Advanced    bool     `bson:"advanced,omitempty" json:"advanced,omitempty"`
}

// Industry represents a vertical market segment.
type Industry struct {
	ID            string   `bson:"_id" json:"id"`
	Slug          string   `bson:"slug" json:"slug"`
	Name          string   `bson:"name" json:"name"`
	Emoji         string   `bson:"emoji" json:"emoji"`
	Description   string   `bson:"description" json:"description"`
	DisplayOrder  int      `bson:"display_order" json:"display_order"`
	SuggestedApps []string `bson:"suggested_apps" json:"suggested_apps"`
	BundleID      string   `bson:"bundle_id" json:"bundle_id"`
}

// Bundle represents a curated collection of apps.
type Bundle struct {
	ID              string   `bson:"_id" json:"id"`
	Slug            string   `bson:"slug" json:"slug"`
	Name            string   `bson:"name" json:"name"`
	Tagline         string   `bson:"tagline" json:"tagline"`
	Apps            []string `bson:"apps" json:"apps"`
	Discount        int      `bson:"discount" json:"discount"`
	RecommendedSize string   `bson:"recommended_size" json:"recommended_size"`
}

// Plan represents a hosting plan with resource limits and pricing.
//
// ProductSlug scopes the plan to a specific product/app. Empty = generic
// compute tier (S/M/L/XL/Flexi) selectable for any tenant. Non-empty =
// product-specific tier (e.g. ProductSlug="sandbox" for Sandbox
// Free/Pro/Ent) that is only valid when the cart contains that app.
// IncludedQuotas carries product-specific quota knobs (sessions, agents,
// storage_gb, byos) the orchestrator stamps onto the per-tenant CR.
type Plan struct {
	ID             string            `bson:"_id" json:"id"`
	Slug           string            `bson:"slug" json:"slug"`
	Name           string            `bson:"name" json:"name"`
	Description    string            `bson:"description" json:"description"`
	CPU            string            `bson:"cpu" json:"cpu"`
	Memory         string            `bson:"memory" json:"memory"`
	Storage        string            `bson:"storage" json:"storage"`
	PriceOMR       int               `bson:"price_omr" json:"price_omr"`
	Popular        bool              `bson:"popular" json:"popular"`
	SortOrder      int               `bson:"sort_order" json:"sort_order"`
	Features       []string          `bson:"features" json:"features"`
	StripePriceID  string            `bson:"stripe_price_id,omitempty" json:"stripe_price_id,omitempty"`
	ProductSlug    string            `bson:"product_slug,omitempty" json:"product_slug,omitempty"`
	IncludedQuotas map[string]string `bson:"included_quotas,omitempty" json:"included_quotas,omitempty"`
}

// AddOn represents an optional add-on service.
type AddOn struct {
	ID          string `bson:"_id" json:"id"`
	Slug        string `bson:"slug" json:"slug"`
	Name        string `bson:"name" json:"name"`
	Description string `bson:"description" json:"description"`
	PriceOMR    int    `bson:"price_omr" json:"price_omr"`
	Included    bool   `bson:"included" json:"included"`
	Category    string `bson:"category" json:"category"`
}

// Store provides CRUD operations against a FerretDB (MongoDB wire protocol) database.
type Store struct {
	db *mongo.Database
}

// New creates a Store backed by the given database.
func New(client *mongo.Client, dbName string) *Store {
	return &Store{db: client.Database(dbName)}
}

// ---------------------------------------------------------------------------
// Apps
// ---------------------------------------------------------------------------

func (s *Store) apps() *mongo.Collection { return s.db.Collection("apps") }

// ListApps returns all apps sorted by name. The catalog is the SINGLE
// SOURCE OF TRUTH (#710) — both the Sovereign-console operator view AND
// the marketplace storefront read from this store. Filtering for the
// marketplace-customer subset is layered on top of this raw list, see
// ListPublishedApps.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.apps().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list apps: %w", err)
	}
	var apps []App
	if err := cursor.All(ctx, &apps); err != nil {
		return nil, fmt.Errorf("store: decode apps: %w", err)
	}
	if apps == nil {
		apps = []App{}
	}
	return apps, nil
}

// ListPublishedApps returns the marketplace-storefront subset:
// Published=true AND System=false AND Deployable=true. The three
// predicates compose:
//
//   - Published — the operator's curation knob (#710): "this app may be
//     advertised to marketplace customers"
//   - System    — internal exclude for backing services (mysql,
//     postgres, redis) that are dependencies, never first-class apps
//   - Deployable — the day-2 install gate (#102): if false, the
//     provisioning template can't actually create a tenant of this app,
//     so showing it on the storefront would lie to the customer
//
// Operators control Published via the Sovereign console; System and
// Deployable are catalog-team-controlled. A marketplace storefront fetch
// does NOT have to know about all three — it just calls
// ListPublishedApps and renders what comes back.
func (s *Store) ListPublishedApps(ctx context.Context) ([]App, error) {
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	filter := bson.D{
		{Key: "published", Value: true},
		// `system: false` and `system: <missing>` both qualify (legacy
		// rows without the field). $ne false would exclude legacy
		// rows on some Mongo versions; the OR-with-missing pattern is safer.
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "system", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "system", Value: false}},
		}},
		{Key: "deployable", Value: true},
	}
	cursor, err := s.apps().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list published apps: %w", err)
	}
	var apps []App
	if err := cursor.All(ctx, &apps); err != nil {
		return nil, fmt.Errorf("store: decode published apps: %w", err)
	}
	if apps == nil {
		apps = []App{}
	}
	return apps, nil
}

// GetApp returns a single app by slug.
func (s *Store) GetApp(ctx context.Context, slug string) (*App, error) {
	var app App
	err := s.apps().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&app)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get app %s: %w", slug, err)
	}
	return &app, nil
}

// CreateApp inserts a new app. If ID is empty, a UUID is generated.
func (s *Store) CreateApp(ctx context.Context, app *App) error {
	if app.ID == "" {
		app.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	app.CreatedAt = now
	app.UpdatedAt = now
	_, err := s.apps().InsertOne(ctx, app)
	if err != nil {
		return fmt.Errorf("store: create app: %w", err)
	}
	return nil
}

// UpdateApp updates an app by _id, setting updated_at.
func (s *Store) UpdateApp(ctx context.Context, id string, app *App) error {
	app.ID = id
	app.UpdatedAt = time.Now().UTC()
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "slug", Value: app.Slug},
		{Key: "name", Value: app.Name},
		{Key: "tagline", Value: app.Tagline},
		{Key: "description", Value: app.Description},
		{Key: "category", Value: app.Category},
		{Key: "icon", Value: app.Icon},
		// #3602 — theme icons + admin-curated topology set. Persisted on
		// every UpdateApp so an admin edit survives a service restart
		// (FerretDB is durable). Empty values are written too, so the
		// admin can clear a theme-icon override and fall back to `Icon`.
		{Key: "icon_light", Value: app.IconLight},
		{Key: "icon_dark", Value: app.IconDark},
		{Key: "supported_topologies", Value: app.SupportedTopologies},
		{Key: "icon_bg", Value: app.IconBg},
		{Key: "free", Value: app.Free},
		{Key: "featured", Value: app.Featured},
		{Key: "popular", Value: app.Popular},
		{Key: "features", Value: app.Features},
		{Key: "website", Value: app.Website},
		{Key: "license", Value: app.License},
		{Key: "dependencies", Value: app.Dependencies},
		{Key: "system", Value: app.System},
		{Key: "kind", Value: app.Kind},
		{Key: "shareable", Value: app.Shareable},
		{Key: "config_schema", Value: app.ConfigSchema},
		{Key: "deployable", Value: app.Deployable}, // #102
		{Key: "published", Value: app.Published},   // #710 wave 2
		{Key: "updated_at", Value: app.UpdatedAt},
	}}}
	res, err := s.apps().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update app %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: app %s not found", id)
	}
	return nil
}

// SetAppPublished flips the Published flag on a single app by slug — the
// hot path the Sovereign console hits when an operator toggles
// "Publish on marketplace" for an app row. Cheaper than a full UpdateApp
// and avoids the operator UI needing to round-trip the entire App
// document for a one-bit change.
//
// Caller is the Sovereign-console operator (per #710); the marketplace
// storefront NEVER hits this path. Returns nil with MatchedCount=0 mapped
// to a "not found" error to keep handler error-mapping uniform with
// UpdateApp.
func (s *Store) SetAppPublished(ctx context.Context, slug string, published bool) error {
	now := time.Now().UTC()
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "published", Value: published},
		{Key: "updated_at", Value: now},
	}}}
	res, err := s.apps().UpdateOne(ctx, bson.D{{Key: "slug", Value: slug}}, update)
	if err != nil {
		return fmt.Errorf("store: set app %s published: %w", slug, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: app %s not found", slug)
	}
	return nil
}

// DeleteApp removes an app by _id.
func (s *Store) DeleteApp(ctx context.Context, id string) error {
	res, err := s.apps().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete app %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: app %s not found", id)
	}
	return nil
}

// SearchApps finds apps matching a text query on name/tagline/description,
// with an optional category filter.
func (s *Store) SearchApps(ctx context.Context, query string, category string) ([]App, error) {
	filter := bson.D{}

	if query != "" {
		q := strings.ToLower(query)
		// FerretDB does not support $text indexes; use $or with $regex instead.
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: q}, {Key: "$options", Value: "i"}}}},
			bson.D{{Key: "tagline", Value: bson.D{{Key: "$regex", Value: q}, {Key: "$options", Value: "i"}}}},
			bson.D{{Key: "description", Value: bson.D{{Key: "$regex", Value: q}, {Key: "$options", Value: "i"}}}},
		}})
	}

	if category != "" {
		filter = append(filter, bson.E{Key: "category", Value: category})
	}

	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.apps().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: search apps: %w", err)
	}
	var apps []App
	if err := cursor.All(ctx, &apps); err != nil {
		return nil, fmt.Errorf("store: decode search results: %w", err)
	}
	if apps == nil {
		apps = []App{}
	}
	return apps, nil
}

// GetAppsByIDs returns apps whose slug is in the given list.
func (s *Store) GetAppsByIDs(ctx context.Context, ids []string) ([]App, error) {
	filter := bson.D{{Key: "slug", Value: bson.D{{Key: "$in", Value: ids}}}}
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.apps().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: get apps by ids: %w", err)
	}
	var apps []App
	if err := cursor.All(ctx, &apps); err != nil {
		return nil, fmt.Errorf("store: decode apps by ids: %w", err)
	}
	if apps == nil {
		apps = []App{}
	}
	return apps, nil
}

// ---------------------------------------------------------------------------
// Industries
// ---------------------------------------------------------------------------

func (s *Store) industries() *mongo.Collection { return s.db.Collection("industries") }

// ListIndustries returns all industries sorted by display_order.
func (s *Store) ListIndustries(ctx context.Context) ([]Industry, error) {
	opts := options.Find().SetSort(bson.D{{Key: "display_order", Value: 1}})
	cursor, err := s.industries().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list industries: %w", err)
	}
	var industries []Industry
	if err := cursor.All(ctx, &industries); err != nil {
		return nil, fmt.Errorf("store: decode industries: %w", err)
	}
	if industries == nil {
		industries = []Industry{}
	}
	return industries, nil
}

// GetIndustry returns a single industry by slug.
func (s *Store) GetIndustry(ctx context.Context, slug string) (*Industry, error) {
	var ind Industry
	err := s.industries().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&ind)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get industry %s: %w", slug, err)
	}
	return &ind, nil
}

// CreateIndustry inserts a new industry.
func (s *Store) CreateIndustry(ctx context.Context, ind *Industry) error {
	if ind.ID == "" {
		ind.ID = uuid.New().String()
	}
	_, err := s.industries().InsertOne(ctx, ind)
	if err != nil {
		return fmt.Errorf("store: create industry: %w", err)
	}
	return nil
}

// UpdateIndustry updates an industry by _id.
func (s *Store) UpdateIndustry(ctx context.Context, id string, ind *Industry) error {
	ind.ID = id
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "slug", Value: ind.Slug},
		{Key: "name", Value: ind.Name},
		{Key: "emoji", Value: ind.Emoji},
		{Key: "description", Value: ind.Description},
		{Key: "display_order", Value: ind.DisplayOrder},
		{Key: "suggested_apps", Value: ind.SuggestedApps},
		{Key: "bundle_id", Value: ind.BundleID},
	}}}
	res, err := s.industries().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update industry %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: industry %s not found", id)
	}
	return nil
}

// DeleteIndustry removes an industry by _id.
func (s *Store) DeleteIndustry(ctx context.Context, id string) error {
	res, err := s.industries().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete industry %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: industry %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Bundles
// ---------------------------------------------------------------------------

func (s *Store) bundles() *mongo.Collection { return s.db.Collection("bundles") }

// ListBundles returns all bundles sorted by name.
func (s *Store) ListBundles(ctx context.Context) ([]Bundle, error) {
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.bundles().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list bundles: %w", err)
	}
	var bundles []Bundle
	if err := cursor.All(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("store: decode bundles: %w", err)
	}
	if bundles == nil {
		bundles = []Bundle{}
	}
	return bundles, nil
}

// GetBundle returns a single bundle by slug.
func (s *Store) GetBundle(ctx context.Context, slug string) (*Bundle, error) {
	var b Bundle
	err := s.bundles().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&b)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get bundle %s: %w", slug, err)
	}
	return &b, nil
}

// CreateBundle inserts a new bundle.
func (s *Store) CreateBundle(ctx context.Context, b *Bundle) error {
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	_, err := s.bundles().InsertOne(ctx, b)
	if err != nil {
		return fmt.Errorf("store: create bundle: %w", err)
	}
	return nil
}

// UpdateBundle updates a bundle by _id.
func (s *Store) UpdateBundle(ctx context.Context, id string, b *Bundle) error {
	update := bson.D{{Key: "$set", Value: b}}
	res, err := s.bundles().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update bundle %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: bundle %s not found", id)
	}
	return nil
}

// DeleteBundle removes a bundle by _id.
func (s *Store) DeleteBundle(ctx context.Context, id string) error {
	res, err := s.bundles().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete bundle %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: bundle %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (s *Store) plans() *mongo.Collection { return s.db.Collection("plans") }

// ListPlans returns all plans sorted by sort_order.
func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	opts := options.Find().SetSort(bson.D{{Key: "sort_order", Value: 1}})
	cursor, err := s.plans().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list plans: %w", err)
	}
	var plans []Plan
	if err := cursor.All(ctx, &plans); err != nil {
		return nil, fmt.Errorf("store: decode plans: %w", err)
	}
	if plans == nil {
		plans = []Plan{}
	}
	return plans, nil
}

// GetPlan returns a single plan by slug.
func (s *Store) GetPlan(ctx context.Context, slug string) (*Plan, error) {
	var p Plan
	err := s.plans().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&p)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get plan %s: %w", slug, err)
	}
	return &p, nil
}

// CreatePlan inserts a new plan.
func (s *Store) CreatePlan(ctx context.Context, p *Plan) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	_, err := s.plans().InsertOne(ctx, p)
	if err != nil {
		return fmt.Errorf("store: create plan: %w", err)
	}
	return nil
}

// UpdatePlan updates a plan by _id.
func (s *Store) UpdatePlan(ctx context.Context, id string, p *Plan) error {
	p.ID = id // ensure _id matches the filter
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "slug", Value: p.Slug},
		{Key: "name", Value: p.Name},
		{Key: "description", Value: p.Description},
		{Key: "cpu", Value: p.CPU},
		{Key: "memory", Value: p.Memory},
		{Key: "storage", Value: p.Storage},
		{Key: "price_omr", Value: p.PriceOMR},
		{Key: "popular", Value: p.Popular},
		{Key: "sort_order", Value: p.SortOrder},
		{Key: "features", Value: p.Features},
		{Key: "stripe_price_id", Value: p.StripePriceID},
		{Key: "product_slug", Value: p.ProductSlug},
		{Key: "included_quotas", Value: p.IncludedQuotas},
	}}}
	res, err := s.plans().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update plan %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: plan %s not found", id)
	}
	return nil
}

// DeletePlan removes a plan by _id.
func (s *Store) DeletePlan(ctx context.Context, id string) error {
	res, err := s.plans().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete plan %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: plan %s not found", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// AddOns
// ---------------------------------------------------------------------------

func (s *Store) addons() *mongo.Collection { return s.db.Collection("addons") }

// ListAddOns returns all add-ons sorted by name.
func (s *Store) ListAddOns(ctx context.Context) ([]AddOn, error) {
	opts := options.Find().SetSort(bson.D{{Key: "name", Value: 1}})
	cursor, err := s.addons().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list addons: %w", err)
	}
	var addons []AddOn
	if err := cursor.All(ctx, &addons); err != nil {
		return nil, fmt.Errorf("store: decode addons: %w", err)
	}
	if addons == nil {
		addons = []AddOn{}
	}
	return addons, nil
}

// GetAddOn returns a single add-on by slug.
func (s *Store) GetAddOn(ctx context.Context, slug string) (*AddOn, error) {
	var a AddOn
	err := s.addons().FindOne(ctx, bson.D{{Key: "slug", Value: slug}}).Decode(&a)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get addon %s: %w", slug, err)
	}
	return &a, nil
}

// CreateAddOn inserts a new add-on.
func (s *Store) CreateAddOn(ctx context.Context, a *AddOn) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	_, err := s.addons().InsertOne(ctx, a)
	if err != nil {
		return fmt.Errorf("store: create addon: %w", err)
	}
	return nil
}

// UpdateAddOn updates an add-on by _id.
func (s *Store) UpdateAddOn(ctx context.Context, id string, a *AddOn) error {
	a.ID = id
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "slug", Value: a.Slug},
		{Key: "name", Value: a.Name},
		{Key: "description", Value: a.Description},
		{Key: "price_omr", Value: a.PriceOMR},
		{Key: "included", Value: a.Included},
		{Key: "category", Value: a.Category},
	}}}
	res, err := s.addons().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update addon %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: addon %s not found", id)
	}
	return nil
}

// DeleteAddOn removes an add-on by _id.
func (s *Store) DeleteAddOn(ctx context.Context, id string) error {
	res, err := s.addons().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete addon %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: addon %s not found", id)
	}
	return nil
}
