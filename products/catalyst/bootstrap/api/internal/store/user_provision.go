// Package store — user_provision.go: persisted state for the
// ADR-0003 RBAC ↔ NewAPI user-create hook.
//
// ADR-0003 §3.4 specifies the canonical state shape:
//
//	user_provision_state(sme_user_uuid PK, sme_tenant_id, email,
//	                     state, kc_user_id, newapi_user_id,
//	                     retry_count, last_error, created_at, updated_at)
//
// State machine:
//
//	pending → kc_created → newapi_created → secret_applied → done
//	            │              │                   │
//	            └──────────────┴───────────────────┴─── retry idempotently
//	            ↓ (5 transient failures)
//	          failed
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 this implementation uses a flat
// JSON file indexed by sme_user_uuid (one file per user) instead of
// the Postgres table specified in the ADR. The semantic contract — the
// state column values, the idempotent re-run semantics, the retry
// counter — is identical; only the persistence engine differs. When
// the unified-rbac service is split out into its own deployable unit
// the schema migrates verbatim to Postgres without a wire-shape change.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// UserProvisionState is one of the canonical states from ADR-0003
// §3.4. Strings (not int constants) so the on-disk record is
// human-readable.
type UserProvisionState string

const (
	UPSPending        UserProvisionState = "pending"
	UPSKCCreated      UserProvisionState = "kc_created"
	UPSNewAPICreated  UserProvisionState = "newapi_created"
	UPSSecretApplied  UserProvisionState = "secret_applied"
	UPSDone           UserProvisionState = "done"
	UPSFailed         UserProvisionState = "failed"
	UPSDeleted        UserProvisionState = "deleted"
)

// UserProvisionRecord is the per-user state row described in
// ADR-0003 §3.4.
type UserProvisionRecord struct {
	SMEUserUUID  string             `json:"sme_user_uuid"`
	OrganizationID  string             `json:"sme_tenant_id"`
	Email        string             `json:"email"`
	State        UserProvisionState `json:"state"`
	KCUserID     string             `json:"kc_user_id,omitempty"`
	NewAPIUserID string             `json:"newapi_user_id,omitempty"`
	// SecretName is the name of the K8s Secret created in step 3
	// (ADR-0003 §3.3 — name pattern `newapi-key-<sme_user_uuid>`).
	SecretName string    `json:"secret_name,omitempty"`
	RetryCount int       `json:"retry_count"`
	LastError  string    `json:"last_error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// userProvisionDir is the per-tenant subdirectory layout under the
// store root: <dir>/user-provision/<tenant_id>/<uuid>.json. Per-tenant
// scoping makes a future "list users for this tenant" query an O(N)
// directory walk over only that tenant's rows, not the whole table.
const userProvisionDir = "user-provision"

// UserProvisionStore is the directory-backed user-provision-state
// implementation.
type UserProvisionStore struct {
	dir string
	mu  sync.Mutex
}

// NewUserProvisionStore returns a store rooted at dir/user-provision.
// dir must already exist; the subdirectory is created with 0o700
// perms.
func NewUserProvisionStore(dir string) (*UserProvisionStore, error) {
	if dir == "" {
		return nil, errors.New("user-provision store: directory is required")
	}
	root := filepath.Join(dir, userProvisionDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("user-provision store: mkdir %q: %w", root, err)
	}
	return &UserProvisionStore{dir: root}, nil
}

// Put upserts a record. The CreatedAt timestamp is preserved on
// upsert; UpdatedAt is bumped to now.
func (s *UserProvisionStore) Put(rec UserProvisionRecord) error {
	if strings.TrimSpace(rec.SMEUserUUID) == "" {
		return errors.New("user-provision: sme_user_uuid is required")
	}
	if strings.TrimSpace(rec.OrganizationID) == "" {
		return errors.New("user-provision: sme_tenant_id is required")
	}
	if rec.State == "" {
		rec.State = UPSPending
	}
	now := time.Now().UTC()
	rec.UpdatedAt = now
	s.mu.Lock()
	defer s.mu.Unlock()

	tenantDir := filepath.Join(s.dir, sanitizeID(rec.OrganizationID))
	if err := os.MkdirAll(tenantDir, 0o700); err != nil {
		return fmt.Errorf("user-provision: mkdir tenant %q: %w", tenantDir, err)
	}
	path := filepath.Join(tenantDir, sanitizeID(rec.SMEUserUUID)+".json")

	// Preserve CreatedAt across upserts.
	if rec.CreatedAt.IsZero() {
		if existing, err := readRec(path); err == nil {
			rec.CreatedAt = existing.CreatedAt
		} else {
			rec.CreatedAt = now
		}
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("user-provision: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("user-provision: write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// Get returns the record for the given uuid. tenantID is required so
// the lookup is O(1) without scanning every tenant's subdirectory.
func (s *UserProvisionStore) Get(tenantID, uuid string) (UserProvisionRecord, bool) {
	path := filepath.Join(s.dir, sanitizeID(tenantID), sanitizeID(uuid)+".json")
	rec, err := readRec(path)
	if err != nil {
		return UserProvisionRecord{}, false
	}
	return rec, true
}

// List returns every record for the given tenant, sorted by
// CreatedAt descending (newest first) — the order the SME admin's UI
// renders.
func (s *UserProvisionStore) List(tenantID string) []UserProvisionRecord {
	tenantDir := filepath.Join(s.dir, sanitizeID(tenantID))
	entries, err := os.ReadDir(tenantDir)
	if err != nil {
		return nil
	}
	out := make([]UserProvisionRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(tenantDir, e.Name())
		if rec, err := readRec(path); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Delete removes a record. Idempotent.
func (s *UserProvisionStore) Delete(tenantID, uuid string) error {
	path := filepath.Join(s.dir, sanitizeID(tenantID), sanitizeID(uuid)+".json")
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("user-provision: remove: %w", err)
	}
	return nil
}

func readRec(path string) (UserProvisionRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UserProvisionRecord{}, err
	}
	var rec UserProvisionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return UserProvisionRecord{}, err
	}
	return rec, nil
}

// sanitizeID strips path separators so a malicious tenant_id /
// user_uuid can't escape the store directory. The catalyst-api's
// session middleware scopes both values to the authenticated session,
// but defence-in-depth keeps this layer safe regardless of upstream
// validation.
func sanitizeID(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}
