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
}

var _ Repository = (*store.Store)(nil)

// Verifier performs the activation check for a declared huawei-project cost
// source; the huawei collector implements it. Optional — nil leaves declared
// sources pending until they are verified through the API.
type Verifier interface {
	VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error
}
