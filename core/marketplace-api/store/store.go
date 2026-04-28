package store

import (
	"sync"
	"time"
)

type ProvisionStatus string

const (
	StatusPending      ProvisionStatus = "pending"
	StatusProvisioning ProvisionStatus = "provisioning"
	StatusCompleted    ProvisionStatus = "completed"
	StatusFailed       ProvisionStatus = "failed"
)

type ProvisionStep struct {
	Name      string          `json:"name"`
	Status    ProvisionStatus `json:"status"`
	Message   string          `json:"message,omitempty"`
	StartedAt *time.Time      `json:"startedAt,omitempty"`
	DoneAt    *time.Time      `json:"doneAt,omitempty"`
}

type Provision struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenantId"`
	CompanyName string          `json:"companyName"`
	Email       string          `json:"email"`
	Subdomain   string          `json:"subdomain"`
	Size        string          `json:"size"`
	Apps        []string        `json:"apps"`
	AddOns      []string        `json:"addOns"`
	Status      ProvisionStatus `json:"status"`
	Steps       []ProvisionStep `json:"steps"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	JWTToken    string          `json:"jwtToken,omitempty"`
}

type Tenant struct {
	ID             string    `json:"id"`
	CompanyName    string    `json:"companyName"`
	Email          string    `json:"email"`
	Subdomain      string    `json:"subdomain"`
	VClusterName   string    `json:"vclusterName"`
	VClusterStatus string    `json:"vclusterStatus"`
	Size           string    `json:"size"`
	SizeLabel      string    `json:"sizeLabel"`
	Apps           []App     `json:"apps"`
	Domains        []Domain  `json:"domains"`
	CreatedAt      time.Time `json:"createdAt"`
}

type App struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Icon       string `json:"icon"`
	Color      string `json:"color"`
	Status     string `json:"status"`
	URL        string `json:"url"`
	Version    string `json:"version"`
	DeployedAt string `json:"deployedAt"`
	Healthy    bool   `json:"healthy"`
}

type Domain struct {
	Domain    string `json:"domain"`
	Type      string `json:"type"`
	TLSReady  bool   `json:"tlsReady"`
	CreatedAt string `json:"createdAt"`
}

// MemoryStore is an in-memory store for development/demo purposes.
// Production will use K8s CRDs (MarketplaceProvision, MarketplaceTenant).
type MemoryStore struct {
	mu         sync.RWMutex
	provisions map[string]*Provision
	tenants    map[string]*Tenant
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		provisions: make(map[string]*Provision),
		tenants:    make(map[string]*Tenant),
	}
}

func (s *MemoryStore) CreateProvision(p *Provision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provisions[p.ID] = p
}

func (s *MemoryStore) GetProvision(id string) *Provision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provisions[id]
}

func (s *MemoryStore) UpdateProvision(p *Provision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provisions[p.ID] = p
}

func (s *MemoryStore) CreateTenant(t *Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.ID] = t
}

func (s *MemoryStore) GetTenant(id string) *Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tenants[id]
}

func (s *MemoryStore) UpdateTenant(t *Tenant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenants[t.ID] = t
}

func (s *MemoryStore) DeleteTenant(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tenants, id)
}
