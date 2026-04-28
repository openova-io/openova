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

// Tenant represents an organization on the platform.
type Tenant struct {
	ID            string    `bson:"_id" json:"id"`
	Slug          string    `bson:"slug" json:"slug"`
	Name          string    `bson:"name" json:"name"`
	OrgType       string    `bson:"org_type" json:"org_type"`
	Industry      string    `bson:"industry" json:"industry"`
	OwnerID       string    `bson:"owner_id" json:"owner_id"`
	PlanID        string    `bson:"plan_id" json:"plan_id"`
	Apps          []string  `bson:"apps" json:"apps"`
	// AppStates tracks per-app lifecycle (keyed by app ID). Values:
	// "installing" | "uninstalling" | "failed". Absent means the app is in
	// its steady state (installed when the ID is in Apps; gone otherwise).
	AppStates     map[string]string `bson:"app_states,omitempty" json:"app_states,omitempty"`
	AddOns        []string  `bson:"addons" json:"addons"`
	Subdomain     string    `bson:"subdomain" json:"subdomain"`
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
