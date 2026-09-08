package openova

import (
	"context"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Repository is the persistence the adapter needs; *store.Store implements
// it, and tests substitute an in-memory fake (the huawei collector's
// Repository pattern).
type Repository interface {
	GetCustomerBySlug(ctx context.Context, slug string) (store.Customer, error)
	CreateCustomer(ctx context.Context, in store.CustomerInput) (store.Customer, error)
	UpdateCustomer(ctx context.Context, id string, p store.CustomerPatch) (store.Customer, error)
	SetCustomerStatus(ctx context.Context, id, status string) error
	UpsertSource(ctx context.Context, customerID, kind, region, projectID string) (store.CostSource, bool, error)
	ListSources(ctx context.Context, scope store.Scope, customerID string) ([]store.CostSource, error)
	CreateCredential(ctx context.Context, customerID, accessKey string, secretEnc []byte) (store.Credential, error)
	MarkCredentialRotated(ctx context.Context, id string) error
	SetSourceCredential(ctx context.Context, sourceID, credentialID string) error
	SetSourceVerified(ctx context.Context, sourceID string, domainID string) error
	SetSourceFailed(ctx context.Context, sourceID, lastError string) error
	SetSourceCollected(ctx context.Context, sourceID string, at time.Time) error
	UpsertUsage(ctx context.Context, recs []store.UsageRecord) (int, error)
	// EnsurePlanBook returns the "OpenOva plans" rate card, creating it when
	// absent (DESIGN.md §2.8 "Plan revenue"); created reports a fresh book.
	EnsurePlanBook(ctx context.Context) (pb store.PriceBook, created bool, err error)
	// SetSourcePriceBook assigns a book to a source (DESIGN.md §2: the book
	// is a property of the source, scope-checked against its layer).
	SetSourcePriceBook(ctx context.Context, sourceID, bookID string) error
	// EnsureInternalSource returns the Sovereign's own internal platform
	// source for the internal Organization's slug, creating it when absent.
	// It has no customer; the platform collector writes the overhead
	// records to it and only Allocation reads them.
	EnsureInternalSource(ctx context.Context, projectID string) (src store.CostSource, created bool, err error)
	// RetireOrganizationCustomer converts the customer an earlier version
	// synced the Sovereign's own Organization as into a plain external
	// customer and its openova-org source into the internal source.
	RetireOrganizationCustomer(ctx context.Context, customerID string) error
}

var _ Repository = (*store.Store)(nil)

// Verifier performs the activation check for a declared huawei-project cost
// source; the huawei collector implements it. Optional — nil leaves declared
// sources pending until they are verified through the API.
type Verifier interface {
	VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error
}
