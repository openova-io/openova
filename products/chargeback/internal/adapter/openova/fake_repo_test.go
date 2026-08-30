package openova

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// fakeRepo is an in-memory Repository so the adapter's sync and platform
// collector are provable without Postgres — the huawei collector's
// isolation-test pattern.
type fakeRepo struct {
	mu        sync.Mutex
	seq       int
	customers map[string]*store.Customer   // by id
	bySlug    map[string]string            // slug → id
	sources   map[string]*store.CostSource // by id
	creds     map[string]store.Credential  // by id
	credEnc   map[string][]byte
	rotated   map[string]bool
	usage     map[string]store.UsageRecord // source|resource|sku|window_start
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		customers: map[string]*store.Customer{}, bySlug: map[string]string{},
		sources: map[string]*store.CostSource{}, creds: map[string]store.Credential{},
		credEnc: map[string][]byte{}, rotated: map[string]bool{}, usage: map[string]store.UsageRecord{},
	}
}

func (f *fakeRepo) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *fakeRepo) GetCustomerBySlug(_ context.Context, slug string) (store.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.bySlug[strings.ToLower(strings.TrimSpace(slug))]
	if !ok {
		return store.Customer{}, store.ErrNotFound
	}
	return *f.customers[id], nil
}

func (f *fakeRepo) CreateCustomer(_ context.Context, in store.CustomerInput) (store.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	if _, exists := f.bySlug[slug]; exists {
		return store.Customer{}, store.ErrConflict
	}
	id := f.nextID("cust")
	c := &store.Customer{ID: id, Slug: slug, Name: in.Name, AdminEmail: strings.ToLower(in.AdminEmail), Kind: in.Kind, BillingMode: in.BillingMode, Status: "pending"}
	if in.OrgSlug != "" {
		v := in.OrgSlug
		c.OrgSlug = &v
	}
	f.customers[id] = c
	f.bySlug[slug] = id
	return *c, nil
}

func (f *fakeRepo) UpdateCustomer(_ context.Context, id string, p store.CustomerPatch) (store.Customer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.customers[id]
	if !ok {
		return store.Customer{}, store.ErrNotFound
	}
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.AdminEmail != nil {
		c.AdminEmail = strings.ToLower(*p.AdminEmail)
	}
	if p.BillingMode != nil {
		c.BillingMode = *p.BillingMode
	}
	if p.Status != nil {
		c.Status = *p.Status
	}
	if p.OrgSlug != nil {
		v := *p.OrgSlug
		c.OrgSlug = &v
	}
	return *c, nil
}

func (f *fakeRepo) SetCustomerStatus(ctx context.Context, id, status string) error {
	_, err := f.UpdateCustomer(ctx, id, store.CustomerPatch{Status: &status})
	return err
}

func (f *fakeRepo) UpsertSource(_ context.Context, customerID, kind, region, projectID string) (store.CostSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sources {
		if s.CustomerID == customerID && s.Kind == kind && s.Region == region && s.ProjectID == projectID {
			return *s, false, nil
		}
	}
	id := f.nextID("src")
	s := &store.CostSource{ID: id, CustomerID: customerID, Kind: kind, Region: region, ProjectID: projectID, Status: "pending"}
	f.sources[id] = s
	return *s, true, nil
}

func (f *fakeRepo) ListSources(_ context.Context, scope store.Scope, customerID string) ([]store.CostSource, error) {
	if !scope.Allows(customerID) {
		return nil, store.ErrNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.CostSource
	for _, s := range f.sources {
		if s.CustomerID == customerID {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f *fakeRepo) CreateCredential(_ context.Context, customerID, accessKey string, secretEnc []byte) (store.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID("cred")
	c := store.Credential{ID: id, CustomerID: customerID, Kind: "aksk", AccessKey: accessKey}
	f.creds[id] = c
	f.credEnc[id] = secretEnc
	return c, nil
}

func (f *fakeRepo) MarkCredentialRotated(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotated[id] = true
	return nil
}

func (f *fakeRepo) SetSourceCredential(_ context.Context, sourceID, credentialID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sources[sourceID]
	if !ok {
		return store.ErrNotFound
	}
	cid := credentialID
	s.CredentialID = &cid
	s.Status = "pending"
	s.AccessKey = f.creds[credentialID].AccessKey
	return nil
}

func (f *fakeRepo) SetSourceVerified(_ context.Context, sourceID string, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sources[sourceID]
	if !ok {
		return store.ErrNotFound
	}
	s.Status = "verified"
	s.LastError = nil
	return nil
}

func (f *fakeRepo) SetSourceFailed(_ context.Context, sourceID, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sources[sourceID]
	if !ok {
		return store.ErrNotFound
	}
	s.Status = "failed"
	msg := lastError
	s.LastError = &msg
	return nil
}

func (f *fakeRepo) SetSourceCollected(_ context.Context, sourceID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sources[sourceID]
	if !ok {
		return store.ErrNotFound
	}
	t := at
	s.LastCollectedAt = &t
	return nil
}

func (f *fakeRepo) UpsertUsage(_ context.Context, recs []store.UsageRecord) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		f.usage[r.SourceID+"|"+r.ResourceID+"|"+r.SKU+"|"+r.WindowStart.UTC().Format(time.RFC3339)] = r
	}
	return len(recs), nil
}

// ---- test-side accessors ------------------------------------------------

func (f *fakeRepo) customerBySlug(slug string) (store.Customer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.bySlug[slug]
	if !ok {
		return store.Customer{}, false
	}
	return *f.customers[id], true
}

func (f *fakeRepo) sourcesOf(customerID string) []store.CostSource {
	out, _ := f.ListSources(context.Background(), store.OperatorScope, customerID)
	return out
}

func (f *fakeRepo) usageRecords(sourceID string) []store.UsageRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.UsageRecord
	for _, r := range f.usage {
		if r.SourceID == sourceID {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeRepo) addActiveCustomer(slug string) store.Customer {
	c, _ := f.CreateCustomer(context.Background(), store.CustomerInput{Slug: slug, Name: slug, Kind: "organization", OrgSlug: slug, BillingMode: "chargeback"})
	_ = f.SetCustomerStatus(context.Background(), c.ID, "active")
	got, _ := f.customerBySlug(slug)
	return got
}
