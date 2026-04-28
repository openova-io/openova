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

// AllowedTLDs lists the TLDs available for free subdomains.
var AllowedTLDs = []string{
	"omani.rest",
	"omani.works",
	"omani.trade",
	"omani.homes",
}

// IsAllowedTLD returns true if the TLD is in the allowed list.
func IsAllowedTLD(tld string) bool {
	for _, t := range AllowedTLDs {
		if t == tld {
			return true
		}
	}
	return false
}

// Domain represents a registered subdomain or custom (BYOD) domain.
type Domain struct {
	ID        string    `bson:"_id" json:"id"`
	TenantID  string    `bson:"tenant_id" json:"tenant_id"`
	Domain    string    `bson:"domain" json:"domain"`       // full domain: myco.omani.rest or custom.com
	Type      string    `bson:"type" json:"type"`           // subdomain, byod
	TLD       string    `bson:"tld" json:"tld"`             // omani.rest, omani.works, etc.
	Subdomain string    `bson:"subdomain" json:"subdomain"` // the part before the TLD
	Registrar string    `bson:"registrar" json:"registrar"` // detected registrar for BYOD
	DNSStatus string    `bson:"dns_status" json:"dns_status"` // pending, verified, failed
	TLSReady  bool      `bson:"tls_ready" json:"tls_ready"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// Store provides CRUD operations against a FerretDB (MongoDB wire protocol) database.
type Store struct {
	db *mongo.Database
}

// New creates a Store backed by the given database.
func New(client *mongo.Client, dbName string) *Store {
	return &Store{db: client.Database(dbName)}
}

func (s *Store) domains() *mongo.Collection { return s.db.Collection("domains") }

// CreateDomain inserts a new domain. If ID is empty, a UUID is generated.
func (s *Store) CreateDomain(ctx context.Context, d *Domain) error {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	_, err := s.domains().InsertOne(ctx, d)
	if err != nil {
		return fmt.Errorf("store: create domain: %w", err)
	}
	return nil
}

// GetDomain returns a single domain by ID.
func (s *Store) GetDomain(ctx context.Context, id string) (*Domain, error) {
	var d Domain
	err := s.domains().FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get domain %s: %w", id, err)
	}
	return &d, nil
}

// ListDomainsByTenant returns all domains for a tenant, sorted by created_at descending.
func (s *Store) ListDomainsByTenant(ctx context.Context, tenantID string) ([]Domain, error) {
	filter := bson.D{{Key: "tenant_id", Value: tenantID}}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cursor, err := s.domains().Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("store: list domains for tenant %s: %w", tenantID, err)
	}
	var domains []Domain
	if err := cursor.All(ctx, &domains); err != nil {
		return nil, fmt.Errorf("store: decode domains: %w", err)
	}
	if domains == nil {
		domains = []Domain{}
	}
	return domains, nil
}

// UpdateDomain updates a domain by ID, setting updated_at.
func (s *Store) UpdateDomain(ctx context.Context, id string, d *Domain) error {
	d.UpdatedAt = time.Now().UTC()
	update := bson.D{{Key: "$set", Value: d}}
	res, err := s.domains().UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	if err != nil {
		return fmt.Errorf("store: update domain %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("store: domain %s not found", id)
	}
	return nil
}

// DeleteDomainsByTenant removes every domain record owned by the given
// tenant. Used by the tenant.deleted cascade (issue #95) so subdomain
// records don't linger and block a future customer from registering the same
// name. Returns the number of records deleted.
//
// Idempotent: if the tenant has no domains (already cascaded, or never had
// any) the function returns (0, nil).
func (s *Store) DeleteDomainsByTenant(ctx context.Context, tenantID string) (int64, error) {
	if tenantID == "" {
		return 0, nil
	}
	res, err := s.domains().DeleteMany(ctx, bson.D{{Key: "tenant_id", Value: tenantID}})
	if err != nil {
		return 0, fmt.Errorf("store: delete domains for tenant %s: %w", tenantID, err)
	}
	return res.DeletedCount, nil
}

// DeleteDomain removes a domain by ID.
func (s *Store) DeleteDomain(ctx context.Context, id string) error {
	res, err := s.domains().DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("store: delete domain %s: %w", id, err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("store: domain %s not found", id)
	}
	return nil
}

// CheckSubdomainAvailable returns true if the subdomain.tld combination is not yet taken.
func (s *Store) CheckSubdomainAvailable(ctx context.Context, subdomain, tld string) (bool, error) {
	fullDomain := subdomain + "." + tld
	count, err := s.domains().CountDocuments(ctx, bson.D{{Key: "domain", Value: fullDomain}})
	if err != nil {
		return false, fmt.Errorf("store: check subdomain availability: %w", err)
	}
	return count == 0, nil
}

// FindDomainByName returns a domain by exact match on the domain field.
func (s *Store) FindDomainByName(ctx context.Context, domain string) (*Domain, error) {
	var d Domain
	err := s.domains().FindOne(ctx, bson.D{{Key: "domain", Value: domain}}).Decode(&d)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("store: find domain %s: %w", domain, err)
	}
	return &d, nil
}
