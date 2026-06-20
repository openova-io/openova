// Tests for the parent-domain pool persistence introduced by issue
// #826 (sub-1 of epic #825).
//
// Two surfaces:
//
//  1. Redact() round-trips ParentDomains verbatim. The fields are
//     non-secret (Name + Role + RegistrarKind + RegistrarCredsRef +
//     AddedAt); RegistrarCredsRef points at a SealedSecret name in
//     catalyst-system, the plaintext credentials never live on the
//     deployment record.
//  2. A record persisted under the LEGACY single-FQDN shape (no
//     parentDomains key in JSON) deserializes cleanly + the
//     provisioner.Validate() migration path re-synthesises the
//     primary entry on the next read. This is the backward-compat
//     guarantee on the issue's DoD: "existing records with
//     sovereign_fqdn = X get translated to [{Name: X, Role: 'primary',
//     ...}] on read; persist back on next save".
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestRedact_RoundTripsParentDomains proves the non-secret pool
// metadata reaches disk verbatim — the admin add-domain panel (#829)
// reads this from the persisted record after a Pod restart and the
// values must match what the operator configured at signup.
func TestRedact_RoundTripsParentDomains(t *testing.T) {
	addedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	req := provisioner.Request{
		OrgName:       "Omantel",
		SovereignFQDN: "omantel.omani.works",
		HetznerToken:  "leaked-if-broken",
		ParentDomains: []provisioner.ParentDomain{
			{Name: "omani.works", Role: provisioner.ParentDomainRolePrimary, RegistrarKind: "dynadot", AddedAt: addedAt},
			{Name: "omani.trade", Role: provisioner.ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "dynadot-omani-trade", AddedAt: addedAt.Add(time.Hour)},
		},
	}
	out := Redact(req)

	if len(out.ParentDomains) != 2 {
		t.Fatalf("RedactedRequest.ParentDomains should round-trip 2 entries, got %d", len(out.ParentDomains))
	}
	if out.ParentDomains[0].Name != "omani.works" {
		t.Errorf("first.Name = %q, want omani.works", out.ParentDomains[0].Name)
	}
	if out.ParentDomains[0].Role != provisioner.ParentDomainRolePrimary {
		t.Errorf("first.Role = %q, want %q", out.ParentDomains[0].Role, provisioner.ParentDomainRolePrimary)
	}
	if out.ParentDomains[1].RegistrarCredsRef != "dynadot-omani-trade" {
		t.Errorf("second.RegistrarCredsRef = %q, want dynadot-omani-trade", out.ParentDomains[1].RegistrarCredsRef)
	}
	if !out.ParentDomains[0].AddedAt.Equal(addedAt) {
		t.Errorf("first.AddedAt = %v, want %v", out.ParentDomains[0].AddedAt, addedAt)
	}

	// Serialised form must NOT contain any pretend-secret value.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "leaked-if-broken") {
		t.Errorf("serialised form leaked credential: %s", raw)
	}
	// The serialised parentDomains array MUST appear under the canonical
	// JSON key — admin tooling consumes this verbatim.
	if !strings.Contains(string(raw), `"parentDomains"`) {
		t.Errorf("serialised RedactedRequest missing parentDomains key: %s", raw)
	}
}

// TestRedact_EmptyParentDomainsStaysEmpty proves a request without a
// pool slice doesn't get an empty slice rewritten into the redacted
// form. omitempty would otherwise drop nothing — but we want the
// serialised form to skip the field entirely, not emit `[]`.
func TestRedact_EmptyParentDomainsStaysEmpty(t *testing.T) {
	req := provisioner.Request{
		OrgName:       "Acme",
		SovereignFQDN: "acme.openova.io",
		HetznerToken:  "tok",
		// ParentDomains intentionally nil.
	}
	out := Redact(req)
	if out.ParentDomains != nil {
		t.Errorf("nil ParentDomains should round-trip nil, got %v", out.ParentDomains)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), `"parentDomains"`) {
		t.Errorf("nil ParentDomains should be omitted from JSON, got: %s", raw)
	}
}

// TestSaveLoad_RoundTripsParentDomains proves a record with the
// pool slice populated round-trips the slice through the store —
// Save → on-disk file → LoadAll → in-memory record. The admin
// add-domain panel (#829) relies on this surviving Pod restarts.
func TestSaveLoad_RoundTripsParentDomains(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	addedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	rec := Record{
		ID:     "deadbeefcafef00d",
		Status: "ready",
		Request: Redact(provisioner.Request{
			OrgName:       "Omantel",
			SovereignFQDN: "omantel.omani.works",
			HetznerToken:  "tok",
			ParentDomains: []provisioner.ParentDomain{
				{Name: "omani.works", Role: provisioner.ParentDomainRolePrimary, RegistrarKind: "dynadot", AddedAt: addedAt},
				{Name: "omani.trade", Role: provisioner.ParentDomainRoleOrgPool, RegistrarKind: "dynadot", AddedAt: addedAt.Add(time.Hour)},
			},
		}),
		StartedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.LoadAll(nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LoadAll returned %d records, want 1", len(got))
	}
	pds := got[0].Request.ParentDomains
	if len(pds) != 2 {
		t.Fatalf("ParentDomains round-trip: len=%d, want 2", len(pds))
	}
	if pds[0].Name != "omani.works" || pds[0].Role != provisioner.ParentDomainRolePrimary {
		t.Errorf("primary entry round-trip failed: %+v", pds[0])
	}
	if pds[1].Name != "omani.trade" || pds[1].Role != provisioner.ParentDomainRoleOrgPool {
		t.Errorf("org-pool entry round-trip failed: %+v", pds[1])
	}
}

// TestLegacyRecord_NoParentDomainsKey_LoadsCleanly is the load-bearing
// backward-compat test. Drops a legacy on-disk record (single-FQDN
// shape, no parentDomains key) into the store directory and proves:
//
//   - LoadAll returns the record without error
//   - the rehydrated provisioner.Request has empty ParentDomains
//   - calling Validate() on the rehydrated Request synthesises the
//     primary entry from SovereignPoolDomain (or SovereignFQDN)
//   - persisting back via Save() emits the array form on the next
//     read — the migration is transparent across a single Pod boot
func TestLegacyRecord_NoParentDomainsKey_LoadsCleanly(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Hand-craft a JSON file that mirrors the pre-#826 on-disk shape:
	// no parentDomains key. We write it directly with os.WriteFile so
	// we exercise the deserialiser the way a real legacy record would.
	legacyJSON := `{
  "id": "legacy0001legacy",
  "status": "ready",
  "request": {
    "orgName": "Omantel",
    "orgEmail": "ops@omantel.om",
    "sovereignFQDN": "omantel.omani.works",
    "sovereignDomainMode": "pool",
    "sovereignPoolDomain": "omani.works",
    "sovereignSubdomain": "omantel",
    "region": "fsn1",
    "controlPlaneSize": "cpx22",
    "workerSize": "cpx32",
    "workerCount": 2,
    "haEnabled": false,
    "sshPublicKey": "ssh-ed25519 AAAA legacy",
    "hetznerToken": "<redacted>",
    "dynadotKey": "<redacted>",
    "dynadotSecret": "<redacted>"
  },
  "startedAt": "2026-04-30T12:00:00Z",
  "events": []
}
`
	path := filepath.Join(dir, "legacy0001legacy.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	// Load it back through the store.
	got, err := s.LoadAll(nil)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LoadAll returned %d records, want 1", len(got))
	}
	rec := got[0]
	if rec.Request.SovereignFQDN != "omantel.omani.works" {
		t.Errorf("non-secret context lost on legacy load: %+v", rec.Request)
	}
	// Pre-migration: the rehydrated slice is empty.
	if len(rec.Request.ParentDomains) != 0 {
		t.Fatalf("legacy record should rehydrate with empty ParentDomains, got %d entries", len(rec.Request.ParentDomains))
	}

	// Now simulate the on-restart re-validation path: rehydrate +
	// run Validate(). The migration synthesises the primary entry.
	// Validate() requires every secret field to be present — for a
	// legacy record the credentials are <redacted>; we only exercise
	// the parent-domains synthesis, so we patch the placeholders in
	// to make the validator pass.
	preq := rec.Request.ToProvisionerRequest()
	preq.HetznerToken = "harness-token"
	preq.HetznerProjectID = "harness-project"
	preq.HarborRobotToken = "harness-harbor"
	preq.GHCRPullToken = "harness-ghcr"
	preq.ObjectStorageRegion = "fsn1"
	preq.ObjectStorageAccessKey = "TESTACCESSKEY"
	preq.ObjectStorageSecretKey = "TESTSECRETKEY1234567890123456789012345"
	preq.ObjectStorageBucket = "catalyst-omantel-omani-works"
	if err := preq.Validate(); err != nil {
		t.Fatalf("Validate on rehydrated legacy request: %v", err)
	}
	if len(preq.ParentDomains) != 1 {
		t.Fatalf("migration should synthesise 1 primary entry, got %d", len(preq.ParentDomains))
	}
	pd := preq.ParentDomains[0]
	if pd.Name != "omani.works" {
		t.Errorf("synthesised Name = %q, want omani.works (the SovereignPoolDomain — the registered parent zone)", pd.Name)
	}
	if pd.Role != provisioner.ParentDomainRolePrimary {
		t.Errorf("synthesised Role = %q, want %q", pd.Role, provisioner.ParentDomainRolePrimary)
	}

	// Persist back — the next on-disk file MUST emit the array form.
	rec.Request = Redact(preq)
	if err := s.Save(rec); err != nil {
		t.Fatalf("Save migrated record: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"parentDomains"`) {
		t.Errorf("migrated on-disk record should contain parentDomains key:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"omani.works"`) {
		t.Errorf("migrated on-disk record should contain primary domain:\n%s", raw)
	}
	// Re-load again and confirm the slice is now populated on the
	// next round-trip.
	got2, err := s.LoadAll(nil)
	if err != nil {
		t.Fatalf("LoadAll after migration: %v", err)
	}
	if len(got2[0].Request.ParentDomains) != 1 {
		t.Errorf("after migration save, LoadAll should rehydrate 1 entry, got %d", len(got2[0].Request.ParentDomains))
	}
}
