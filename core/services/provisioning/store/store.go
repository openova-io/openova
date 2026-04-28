package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ProvisionStep tracks the status of a single provisioning step.
type ProvisionStep struct {
	Name      string    `bson:"name" json:"name"`
	Status    string    `bson:"status" json:"status"` // pending, running, completed, failed
	Message   string    `bson:"message" json:"message"`
	StartedAt time.Time `bson:"started_at,omitempty" json:"started_at,omitempty"`
	DoneAt    time.Time `bson:"done_at,omitempty" json:"done_at,omitempty"`
}

// Provision represents a tenant environment provisioning record.
type Provision struct {
	ID        string          `bson:"_id" json:"id"`
	TenantID  string          `bson:"tenant_id" json:"tenant_id"`
	OrderID   string          `bson:"order_id" json:"order_id"`
	PlanID    string          `bson:"plan_id" json:"plan_id"`
	Apps      []string        `bson:"apps" json:"apps"`
	Subdomain string          `bson:"subdomain" json:"subdomain"`
	Status    string          `bson:"status" json:"status"` // pending, provisioning, completed, failed
	Steps     []ProvisionStep `bson:"steps" json:"steps"`
	Progress  int             `bson:"progress" json:"progress"` // 0-100
	CreatedAt time.Time       `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time       `bson:"updated_at" json:"updated_at"`
}

// Store provides CRUD operations against a FerretDB (MongoDB wire protocol) database.
type Store struct {
	db *mongo.Database
}

// New creates a Store backed by the given database.
func New(client *mongo.Client, dbName string) *Store {
	return &Store{db: client.Database(dbName)}
}

func (s *Store) provisions() *mongo.Collection { return s.db.Collection("provisions") }

// CreateProvision inserts a new provision record. If ID is empty, a UUID is generated.
func (s *Store) CreateProvision(ctx context.Context, p *Provision) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	_, err := s.provisions().InsertOne(ctx, p)
	if err != nil {
		return fmt.Errorf("store: create provision: %w", err)
	}
	return nil
}

// GetProvision returns a provision by ID.
func (s *Store) GetProvision(ctx context.Context, id string) (*Provision, error) {
	var p Provision
	err := s.provisions().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&p)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get provision %s: %w", id, err)
	}
	return &p, nil
}

// GetProvisionByTenant returns the provision record for a given tenant.
func (s *Store) GetProvisionByTenant(ctx context.Context, tenantID string) (*Provision, error) {
	var p Provision
	err := s.provisions().FindOne(ctx, bson.D{{Key: "tenant_id", Value: tenantID}}).Decode(&p)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get provision by tenant %s: %w", tenantID, err)
	}
	return &p, nil
}

// UpdateProvision replaces the provision document by ID.
func (s *Store) UpdateProvision(ctx context.Context, id string, p *Provision) error {
	p.UpdatedAt = time.Now().UTC()
	update := bson.D{{Key: "$set", Value: p}}
	res, err := s.provisions().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update provision %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: provision %s not found", id)
	}
	return nil
}

// UpdateStep updates a single step within a provision record by index.
func (s *Store) UpdateStep(ctx context.Context, provisionID string, stepIndex int, step ProvisionStep) error {
	stepKey := fmt.Sprintf("steps.%d", stepIndex)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: stepKey, Value: step},
			{Key: "updated_at", Value: time.Now().UTC()},
		}},
	}
	res, err := s.provisions().UpdateOne(ctx, bson.D{{Key: "_id", Value: provisionID}}, update)
	if err != nil {
		return fmt.Errorf("store: update step %d for provision %s: %w", stepIndex, provisionID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: provision %s not found", provisionID)
	}
	return nil
}

// ListProvisions returns provisions with pagination, sorted by created_at descending.
func (s *Store) ListProvisions(ctx context.Context, offset, limit int) ([]Provision, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(int64(offset)).
		SetLimit(int64(limit))
	cursor, err := s.provisions().Find(ctx, bson.D{}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list provisions: %w", err)
	}
	var provisions []Provision
	if err := cursor.All(ctx, &provisions); err != nil {
		return nil, fmt.Errorf("store: decode provisions: %w", err)
	}
	if provisions == nil {
		provisions = []Provision{}
	}
	return provisions, nil
}

// ---------------------------------------------------------------------------
// Day-2 jobs — install / uninstall records the Jobs page consumes. Mirrors
// the Provision shape so the UI can render both with the same component.
// ---------------------------------------------------------------------------

// JobStep tracks a single stage of a day-2 Job. Same shape as ProvisionStep.
type JobStep struct {
	Name      string    `bson:"name" json:"name"`
	Status    string    `bson:"status" json:"status"` // pending, running, completed, failed
	Message   string    `bson:"message" json:"message"`
	StartedAt time.Time `bson:"started_at,omitempty" json:"started_at,omitempty"`
	DoneAt    time.Time `bson:"done_at,omitempty" json:"done_at,omitempty"`
}

// Job is a day-2 install or uninstall record. Purged services lists the
// backing-service slugs whose data was dropped (for uninstall jobs only).
// RetainedServices is the inverse — shared with another installed app.
//
// IdempotencyKey is the opaque string the tenant service generates per user
// click. It's indexed UNIQUE so the first writer wins and the second writer
// (whichever transport arrives second — HTTP vs event bus) gets a duplicate
// key error from Mongo. CreateJobIfAbsent translates that into a
// "job already exists" signal so callers can skip. See issue #71.
type Job struct {
	ID               string    `bson:"_id" json:"id"`
	TenantID         string    `bson:"tenant_id" json:"tenant_id"`
	TenantSlug       string    `bson:"tenant_slug" json:"tenant_slug"`
	Kind             string    `bson:"kind" json:"kind"` // "install" | "uninstall"
	AppSlug          string    `bson:"app_slug" json:"app_slug"`
	AppID            string    `bson:"app_id" json:"app_id"`
	AppName          string    `bson:"app_name" json:"app_name"`
	IdempotencyKey   string    `bson:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
	Status           string    `bson:"status" json:"status"` // pending, running, succeeded, failed
	Steps            []JobStep `bson:"steps" json:"steps"`
	Progress         int       `bson:"progress" json:"progress"`
	PurgedServices   []string  `bson:"purged_services,omitempty" json:"purged_services,omitempty"`
	RetainedServices []string  `bson:"retained_services,omitempty" json:"retained_services,omitempty"`
	CreatedAt        time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt        time.Time `bson:"updated_at" json:"updated_at"`
}

func (s *Store) jobs() *mongo.Collection { return s.db.Collection("jobs") }

// EnsureJobIndexes creates the unique index on IdempotencyKey that backs the
// dedup guarantee in CreateJobIfAbsent. Safe to call at startup every time —
// index creation is idempotent. Partial index so historical rows (written
// before the fix) without a key don't trip the unique constraint.
func (s *Store) EnsureJobIndexes(ctx context.Context) error {
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_idempotency_key").SetSparse(true),
	}
	_, err := s.jobs().Indexes().CreateOne(ctx, model)
	if err != nil {
		return fmt.Errorf("store: ensure job indexes: %w", err)
	}
	return nil
}

// CreateJob inserts a new day-2 Job record.
func (s *Store) CreateJob(ctx context.Context, j *Job) error {
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	j.CreatedAt = now
	j.UpdatedAt = now
	if j.Steps == nil {
		j.Steps = []JobStep{}
	}
	_, err := s.jobs().InsertOne(ctx, j)
	if err != nil {
		return fmt.Errorf("store: create job: %w", err)
	}
	return nil
}

// ErrJobExists is returned by CreateJobIfAbsent when a Job with the given
// IdempotencyKey already exists. Callers should treat this as "work is
// already in flight (or complete) — skip this duplicate dispatch". See #71.
var ErrJobExists = fmt.Errorf("store: job already exists")

// CreateJobIfAbsent inserts a Job iff no row with the same IdempotencyKey
// exists. Returns ErrJobExists when a duplicate key is detected (the first
// writer wins). Empty IdempotencyKey is rejected — callers must supply one;
// otherwise the whole dedup guarantee collapses.
//
// Implementation notes: we rely on the unique sparse index created by
// EnsureJobIndexes. Mongo surfaces E11000 for duplicate-key violations; we
// check with mongo.IsDuplicateKeyError to stay driver-version-independent.
func (s *Store) CreateJobIfAbsent(ctx context.Context, j *Job) error {
	if j.IdempotencyKey == "" {
		return fmt.Errorf("store: CreateJobIfAbsent requires IdempotencyKey")
	}
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	j.CreatedAt = now
	j.UpdatedAt = now
	if j.Steps == nil {
		j.Steps = []JobStep{}
	}
	_, err := s.jobs().InsertOne(ctx, j)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrJobExists
		}
		return fmt.Errorf("store: create job if absent: %w", err)
	}
	return nil
}

// GetJobByIdempotencyKey returns the existing Job for the given key, or
// (nil, nil) if none exists. Used by the dedup path to surface the already-
// in-flight job to callers after a CreateJobIfAbsent collision.
func (s *Store) GetJobByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	if key == "" {
		return nil, nil
	}
	var j Job
	err := s.jobs().FindOne(ctx, bson.D{{Key: "idempotency_key", Value: key}}).Decode(&j)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get job by idempotency key: %w", err)
	}
	return &j, nil
}

// GetJob returns a Job by ID.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	var j Job
	err := s.jobs().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&j)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get job %s: %w", id, err)
	}
	return &j, nil
}

// UpdateJob replaces the Job document by ID.
func (s *Store) UpdateJob(ctx context.Context, id string, j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	res, err := s.jobs().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: j}},
	)
	if err != nil {
		return fmt.Errorf("store: update job %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: job %s not found", id)
	}
	return nil
}

// UpdateJobStep updates a single step within a Job record by index.
func (s *Store) UpdateJobStep(ctx context.Context, jobID string, stepIndex int, step JobStep) error {
	stepKey := fmt.Sprintf("steps.%d", stepIndex)
	res, err := s.jobs().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: jobID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: stepKey, Value: step},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("store: update job step %d for %s: %w", stepIndex, jobID, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: job %s not found", jobID)
	}
	return nil
}

// ListJobsByTenant returns Jobs for a tenant, newest first. limit defaults to 50.
func (s *Store) ListJobsByTenant(ctx context.Context, tenantID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))
	cursor, err := s.jobs().Find(ctx, bson.D{{Key: "tenant_id", Value: tenantID}}, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs by tenant: %w", err)
	}
	var jobs []Job
	if err := cursor.All(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("store: decode jobs: %w", err)
	}
	if jobs == nil {
		jobs = []Job{}
	}
	return jobs, nil
}
