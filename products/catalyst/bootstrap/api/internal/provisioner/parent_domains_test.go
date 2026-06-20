// Tests for the multi-domain Sovereign data model + provisioning
// abstraction introduced by issue #826 (sub-1 of epic #825).
//
// The DoD covered here:
//
//  1. Validate() synthesises a single primary ParentDomain when the
//     legacy single-FQDN payload arrives without a parentDomains
//     slice — the migration step on every read.
//  2. Validate() rejects malformed pools: empty, two primaries, no
//     primary, unrecognised role, duplicate names, bad FQDN.
//  3. ProvisionParentDomain runs every step against the supplied
//     domain in order, stops on first error, emits SSE events the
//     wizard surface (and #829's add-domain panel) can render
//     per-domain.
//  4. writeTfvars emits parent_domains as a JSON array (never null)
//     so the OpenTofu module's `for pd in var.parent_domains`
//     validator accepts the input on every payload — same nil-trap
//     fix the regions slice already carries.
//
// The wizard UI is NOT exercised here — per the issue's SCOPE
// CORRECTION, the wizard stays single-FQDN, and the multi-domain
// pool is purely a backend representation that #829 (admin add-
// domain) appends to post-handover.
package provisioner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// validBaseWithSecrets seeds every required field so the parent-
// domains code paths exercise their behaviour without tripping the
// downstream Harbor / GHCR / object-storage validators. Mirrors
// validBase in provisioner_test.go but with the credentials that
// Validate() also requires.
func validBaseWithSecrets() Request {
	return Request{
		OrgName:                "ACME",
		OrgEmail:               "ops@acme.io",
		SovereignFQDN:          "acme.openova.io",
		HetznerToken:           "TEST-TOKEN-NOT-REAL",
		HetznerProjectID:       "test-project",
		Region:                 "fsn1",
		SSHPublicKey:           "ssh-ed25519 AAAA test-not-a-real-key",
		HarborRobotToken:       "harbor-test-token",
		ObjectStorageRegion:    "fsn1",
		ObjectStorageAccessKey: "TESTACCESSKEY1234567",
		ObjectStorageSecretKey: "TESTSECRETKEY1234567890123456789012345678",
		ObjectStorageBucket:    "catalyst-acme-openova-io",
	}
}

// TestValidate_SynthesisesPrimaryFromSovereignFQDN proves the
// migration step: a legacy payload with NO parentDomains slice gets
// a single primary entry synthesised from SovereignFQDN (BYO mode)
// or SovereignPoolDomain (pool mode). This is the core back-compat
// guarantee on issue #826's DoD: existing single-FQDN records read
// cleanly + persist back as the array shape on next Save().
func TestValidate_SynthesisesPrimaryFromSovereignFQDN(t *testing.T) {
	r := validBaseWithSecrets()
	r.SovereignFQDN = "acme.openova.io"
	// SovereignPoolDomain intentionally empty — BYO-or-legacy fallback.
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate should pass on legacy single-FQDN payload: %v", err)
	}
	if len(r.ParentDomains) != 1 {
		t.Fatalf("ParentDomains should be synthesised with 1 entry, got %d", len(r.ParentDomains))
	}
	pd := r.ParentDomains[0]
	if pd.Name != "acme.openova.io" {
		t.Errorf("synthesised Name = %q, want acme.openova.io (lowercased SovereignFQDN)", pd.Name)
	}
	if pd.Role != ParentDomainRolePrimary {
		t.Errorf("synthesised Role = %q, want %q", pd.Role, ParentDomainRolePrimary)
	}
	if pd.RegistrarKind != defaultRegistrarKind {
		t.Errorf("synthesised RegistrarKind = %q, want %q (defaultRegistrarKind)", pd.RegistrarKind, defaultRegistrarKind)
	}
	// AddedAt must be zero on synthesis — the on-disk record's zero
	// timestamp lets the admin console distinguish migration-derived
	// entries from explicit add-domain entries (which carry the
	// admin's add-time per issue #829).
	if !pd.AddedAt.IsZero() {
		t.Errorf("synthesised AddedAt should be zero (migration source), got %s", pd.AddedAt)
	}
}

// TestValidate_SynthesisesPrimaryFromSovereignPoolDomain proves the
// pool-mode synthesis: when SovereignPoolDomain is set, the primary
// entry's Name is the pool domain (the registered parent zone),
// NOT the per-Sovereign sub-FQDN.
func TestValidate_SynthesisesPrimaryFromSovereignPoolDomain(t *testing.T) {
	r := validBaseWithSecrets()
	r.SovereignFQDN = "omantel.omani.works"
	r.SovereignPoolDomain = "omani.works"
	r.SovereignDomainMode = "pool"
	r.GHCRPullToken = "ghcr-test-token" // pool mode requires it
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate should pass on pool-mode legacy payload: %v", err)
	}
	if len(r.ParentDomains) != 1 {
		t.Fatalf("ParentDomains should be synthesised with 1 entry, got %d", len(r.ParentDomains))
	}
	pd := r.ParentDomains[0]
	if pd.Name != "omani.works" {
		t.Errorf("pool-mode synthesised Name = %q, want omani.works (the registered parent zone, not the sub-FQDN)", pd.Name)
	}
	if pd.Role != ParentDomainRolePrimary {
		t.Errorf("Role = %q, want %q", pd.Role, ParentDomainRolePrimary)
	}
}

// TestValidate_PreservesSuppliedParentDomains proves a payload that
// already carries a parentDomains slice (e.g. #829's add-domain flow
// appending an entry, then running Validate() on the round-trip)
// keeps the slice verbatim — no double-synthesis.
func TestValidate_PreservesSuppliedParentDomains(t *testing.T) {
	r := validBaseWithSecrets()
	r.SovereignFQDN = "omantel.omani.works"
	addedAt := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	r.ParentDomains = []ParentDomain{
		{Name: "omani.works", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot", AddedAt: addedAt},
		{Name: "omani.trade", Role: ParentDomainRoleOrgPool, RegistrarKind: "dynadot", AddedAt: addedAt.Add(time.Hour)},
	}

	if err := r.Validate(); err != nil {
		t.Fatalf("Validate should accept a well-formed multi-domain pool: %v", err)
	}
	if len(r.ParentDomains) != 2 {
		t.Fatalf("ParentDomains should round-trip 2 entries, got %d", len(r.ParentDomains))
	}
	if r.ParentDomains[0].Name != "omani.works" {
		t.Errorf("primary Name was clobbered: %q", r.ParentDomains[0].Name)
	}
	if r.ParentDomains[1].Role != ParentDomainRoleOrgPool {
		t.Errorf("org-pool Role was clobbered: %q", r.ParentDomains[1].Role)
	}
	if !r.ParentDomains[0].AddedAt.Equal(addedAt) {
		t.Errorf("primary AddedAt was clobbered: %s", r.ParentDomains[0].AddedAt)
	}
}

// TestValidate_RejectsTwoPrimaries proves the "exactly one primary"
// invariant: a pool with two primary entries fails Validate() with
// a clear message naming the count. Day-2 add-domain (#829) MUST
// reject a payload that would create a second primary.
func TestValidate_RejectsTwoPrimaries(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "omani.works", Role: ParentDomainRolePrimary},
		{Name: "omani.trade", Role: ParentDomainRolePrimary},
	}
	err := r.Validate()
	if err == nil {
		t.Fatalf("two-primary pool should be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one entry") {
		t.Errorf("error should name the invariant, got %q", err.Error())
	}
}

// TestValidate_RejectsZeroPrimaries proves the "exactly one primary"
// invariant in the other direction: a pool of all org-pool entries
// is rejected.
func TestValidate_RejectsZeroPrimaries(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "omani.trade", Role: ParentDomainRoleOrgPool},
	}
	err := r.Validate()
	if err == nil {
		t.Fatalf("zero-primary pool should be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one entry") {
		t.Errorf("error should name the invariant, got %q", err.Error())
	}
}

// TestValidate_RejectsUnknownRole guards the role enum.
func TestValidate_RejectsUnknownRole(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "omani.works", Role: "marketplace-only"},
	}
	err := r.Validate()
	if err == nil {
		t.Fatalf("unknown role should be rejected")
	}
	if !strings.Contains(err.Error(), "marketplace-only") {
		t.Errorf("error should name the offending role, got %q", err.Error())
	}
}

// TestValidate_RejectsEmptyRole proves an entry with no role at all
// is rejected with a message naming the valid roles.
func TestValidate_RejectsEmptyRole(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "omani.works"},
	}
	err := r.Validate()
	if err == nil {
		t.Fatalf("empty role should be rejected")
	}
	if !strings.Contains(err.Error(), "role is required") {
		t.Errorf("error should name the required-field violation, got %q", err.Error())
	}
}

// TestValidate_RejectsBadFQDN guards against malformed names.
// Inviolable Principle #1 (target-state shape) — we don't accept a
// "looks like" domain that the registrar would later reject. Note
// uppercase names are NOT rejected — the validator normalises to
// lowercase in place; that contract is exercised by
// TestValidate_NormalisesUppercaseFQDNToLower below.
func TestValidate_RejectsBadFQDN(t *testing.T) {
	cases := []string{
		"omani",         // single label
		"-bad.works",    // leading dash
		"omani..works",  // double dot
		"omani.works.",  // trailing dot
		"omani works",   // whitespace
		"omani.works/x", // slash
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			r := validBaseWithSecrets()
			r.ParentDomains = []ParentDomain{
				{Name: name, Role: ParentDomainRolePrimary},
			}
			err := r.Validate()
			if err == nil {
				t.Fatalf("malformed FQDN %q should be rejected", name)
			}
		})
	}
}

// TestValidate_NormalisesUppercaseFQDNToLower proves the validator
// lower-cases the Name in place — uppercase user input is accepted
// but stored canonical so downstream consumers (registrar adapter,
// CRD projection, SME signup dropdown) see the same string.
func TestValidate_NormalisesUppercaseFQDNToLower(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "OMANI.WORKS", Role: ParentDomainRolePrimary},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate should accept uppercase + normalise: %v", err)
	}
	if r.ParentDomains[0].Name != "omani.works" {
		t.Errorf("Name was not normalised: got %q, want omani.works", r.ParentDomains[0].Name)
	}
}

// TestValidate_RejectsDuplicateNames proves the dedupe check: an
// admin trying to add the same domain twice via #829 surfaces a
// clear 400 instead of a half-applied NS-flip.
func TestValidate_RejectsDuplicateNames(t *testing.T) {
	r := validBaseWithSecrets()
	r.ParentDomains = []ParentDomain{
		{Name: "omani.works", Role: ParentDomainRolePrimary},
		{Name: "omani.works", Role: ParentDomainRoleOrgPool},
	}
	err := r.Validate()
	if err == nil {
		t.Fatalf("duplicate Name should be rejected")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should name the duplicate violation, got %q", err.Error())
	}
}

// TestPrimaryParentDomain_LookupHelper proves the lookup helper
// returns the unique primary entry. Used by the catalyst-api's
// runProvisioning to read the primary's Name without re-iterating
// the slice at every call site.
func TestPrimaryParentDomain_LookupHelper(t *testing.T) {
	r := Request{
		ParentDomains: []ParentDomain{
			{Name: "omani.trade", Role: ParentDomainRoleOrgPool},
			{Name: "omani.works", Role: ParentDomainRolePrimary},
			{Name: "omani.shop", Role: ParentDomainRoleOrgPool},
		},
	}
	pd := r.PrimaryParentDomain()
	if pd == nil {
		t.Fatalf("PrimaryParentDomain returned nil with a primary present")
	}
	if pd.Name != "omani.works" {
		t.Errorf("primary Name = %q, want omani.works", pd.Name)
	}
}

// TestPrimaryParentDomain_NoneReturnsNil — defensive case for
// callers that haven't yet validated.
func TestPrimaryParentDomain_NoneReturnsNil(t *testing.T) {
	r := Request{
		ParentDomains: []ParentDomain{
			{Name: "omani.trade", Role: ParentDomainRoleOrgPool},
		},
	}
	if pd := r.PrimaryParentDomain(); pd != nil {
		t.Errorf("PrimaryParentDomain should return nil with no primary, got %+v", pd)
	}
}

// TestOrgPoolParentDomains_FiltersToRole proves the slice filter
// returns only the org-pool entries, in their original order. The
// SME signup wizard's parent-pool dropdown reads this for the
// per-Sovereign list of free-subdomain options.
func TestOrgPoolParentDomains_FiltersToRole(t *testing.T) {
	r := Request{
		ParentDomains: []ParentDomain{
			{Name: "omani.works", Role: ParentDomainRolePrimary},
			{Name: "omani.trade", Role: ParentDomainRoleOrgPool},
			{Name: "omani.shop", Role: ParentDomainRoleOrgPool},
		},
	}
	pool := r.OrgPoolParentDomains()
	if len(pool) != 2 {
		t.Fatalf("OrgPoolParentDomains returned %d entries, want 2", len(pool))
	}
	if pool[0].Name != "omani.trade" {
		t.Errorf("pool[0].Name = %q, want omani.trade (original order preserved)", pool[0].Name)
	}
	if pool[1].Name != "omani.shop" {
		t.Errorf("pool[1].Name = %q, want omani.shop", pool[1].Name)
	}
}

// stepStub is a test-only ParentDomainStep that records every
// invocation. Used to prove ProvisionParentDomain runs the steps in
// order + threads the right (pd, lbIP) into Apply.
type stepStub struct {
	name    string
	calls   []ParentDomain
	failOn  string
	failErr error
}

func (s *stepStub) Name() string { return s.name }

func (s *stepStub) Apply(_ context.Context, pd ParentDomain, _ string) error {
	s.calls = append(s.calls, pd)
	if s.failOn == pd.Name {
		return s.failErr
	}
	return nil
}

// TestProvisionParentDomain_RunsStepsInOrder proves the per-domain
// abstraction iterates the steps as supplied. This is the core
// re-usability contract the issue's DoD calls out: #829 (admin add-
// domain) MUST be able to call ProvisionParentDomain with the same
// step list the catalyst-api wires for Day-1.
func TestProvisionParentDomain_RunsStepsInOrder(t *testing.T) {
	flip := &stepStub{name: "registrar-flip"}
	zone := &stepStub{name: "powerdns-zone-create"}
	cert := &stepStub{name: "cert-manager-cert"}

	pd := ParentDomain{Name: "omani.works", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot"}
	var emitted []string
	emit := func(phase, _, msg string) {
		emitted = append(emitted, phase+":"+msg)
	}
	if err := ProvisionParentDomain(context.Background(), pd, "10.0.0.1", []ParentDomainStep{flip, zone, cert}, emit); err != nil {
		t.Fatalf("ProvisionParentDomain: %v", err)
	}
	for _, s := range []*stepStub{flip, zone, cert} {
		if len(s.calls) != 1 {
			t.Errorf("step %q should have run once, got %d", s.name, len(s.calls))
		} else if s.calls[0].Name != "omani.works" {
			t.Errorf("step %q got pd.Name = %q, want omani.works", s.name, s.calls[0].Name)
		}
	}
	// Per-step events must include the step name + the domain name so
	// the SSE stream surfaces the per-domain progress unambiguously.
	joined := strings.Join(emitted, "\n")
	for _, want := range []string{"registrar-flip", "powerdns-zone-create", "cert-manager-cert", "omani.works"} {
		if !strings.Contains(joined, want) {
			t.Errorf("emitted events should mention %q, got:\n%s", want, joined)
		}
	}
}

// TestProvisionParentDomain_StopsOnFirstError proves a step that
// fails halts the per-domain pipeline before subsequent steps run.
// The wipe + restart loop relies on this: a partial state is the
// operator's signal to wipe; we MUST NOT mask a failure by
// continuing.
func TestProvisionParentDomain_StopsOnFirstError(t *testing.T) {
	flip := &stepStub{name: "registrar-flip", failOn: "omani.works", failErr: errors.New("dynadot 500")}
	zone := &stepStub{name: "powerdns-zone-create"}
	cert := &stepStub{name: "cert-manager-cert"}

	pd := ParentDomain{Name: "omani.works", Role: ParentDomainRolePrimary}
	err := ProvisionParentDomain(context.Background(), pd, "10.0.0.1", []ParentDomainStep{flip, zone, cert}, nil)
	if err == nil {
		t.Fatalf("ProvisionParentDomain should propagate the step error")
	}
	if !strings.Contains(err.Error(), "registrar-flip") || !strings.Contains(err.Error(), "omani.works") {
		t.Errorf("wrapped error should name the step + domain, got %q", err.Error())
	}
	if len(flip.calls) != 1 {
		t.Errorf("registrar-flip should have run once, got %d", len(flip.calls))
	}
	if len(zone.calls) != 0 || len(cert.calls) != 0 {
		t.Errorf("subsequent steps should be skipped on failure: zone=%d cert=%d", len(zone.calls), len(cert.calls))
	}
}

// TestProvisionParentDomains_IteratesEveryDomain proves the slice
// flavour applies every step against every domain. Day-2 (#829)
// uses this when an admin adds N domains in one batch.
func TestProvisionParentDomains_IteratesEveryDomain(t *testing.T) {
	flip := &stepStub{name: "registrar-flip"}
	pds := []ParentDomain{
		{Name: "omani.works", Role: ParentDomainRolePrimary},
		{Name: "omani.trade", Role: ParentDomainRoleOrgPool},
		{Name: "omani.shop", Role: ParentDomainRoleOrgPool},
	}
	if err := ProvisionParentDomains(context.Background(), pds, "10.0.0.1", []ParentDomainStep{flip}, nil); err != nil {
		t.Fatalf("ProvisionParentDomains: %v", err)
	}
	if len(flip.calls) != 3 {
		t.Fatalf("step should run once per domain (3), got %d", len(flip.calls))
	}
	for i, pd := range pds {
		if flip.calls[i].Name != pd.Name {
			t.Errorf("flip.calls[%d].Name = %q, want %q", i, flip.calls[i].Name, pd.Name)
		}
	}
}

// TestWriteTfvars_EmitsParentDomainsAsArrayNotNull is the parent-
// domain analogue of TestWriteTfvars_EmitsRegionsAsEmptyArrayNotNull.
// Same nil-trap fix: a future OpenTofu module's `for pd in
// var.parent_domains` validator would fail on JSON null.
func TestWriteTfvars_EmitsParentDomainsAsArrayNotNull(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-pd-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "otech87.omani.works",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		// ParentDomains intentionally nil — the typical Day-1 path
		// before Validate() has run synthesis.
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), `"parent_domains": null`) {
		t.Fatalf("parent_domains must serialise as [] (not null). Got:\n%s", string(raw))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	pds, ok := parsed["parent_domains"].([]any)
	if !ok {
		t.Fatalf("parent_domains must be a JSON array, got %T", parsed["parent_domains"])
	}
	if len(pds) != 0 {
		t.Fatalf("parent_domains should be empty when request has none, got %d entries", len(pds))
	}
}

// TestWriteTfvars_EmitsParentDomainsRoundTripsEntries proves the
// supplied slice round-trips through the JSON shape — Name + Role +
// RegistrarKind reach the OpenTofu module verbatim. The
// RegistrarCredsRef field is preserved when set; AddedAt is dropped
// when zero (omitempty).
func TestWriteTfvars_EmitsParentDomainsRoundTripsEntries(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-pd-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "otech88.omani.works",
		OrgName:          "Acme",
		OrgEmail:         "ops@acme.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		ParentDomains: []ParentDomain{
			{Name: "omani.works", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot"},
			{Name: "omani.trade", Role: ParentDomainRoleOrgPool, RegistrarKind: "dynadot", RegistrarCredsRef: "dynadot-omani-trade"},
		},
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	pdsRaw, ok := parsed["parent_domains"].([]any)
	if !ok {
		t.Fatalf("parent_domains must be a JSON array, got %T", parsed["parent_domains"])
	}
	if len(pdsRaw) != 2 {
		t.Fatalf("parent_domains should round-trip 2 entries, got %d", len(pdsRaw))
	}
	first, _ := pdsRaw[0].(map[string]any)
	if first["name"] != "omani.works" {
		t.Errorf("first.name = %v, want omani.works", first["name"])
	}
	if first["role"] != ParentDomainRolePrimary {
		t.Errorf("first.role = %v, want %q", first["role"], ParentDomainRolePrimary)
	}
	second, _ := pdsRaw[1].(map[string]any)
	if second["registrarCredsRef"] != "dynadot-omani-trade" {
		t.Errorf("second.registrarCredsRef = %v, want dynadot-omani-trade", second["registrarCredsRef"])
	}
}

// TestWriteTfvars_EmitsParentDomainsYAMLForOrgPool is the regression
// guard for issue #1772 (TBD-D30b — Cilium Gateway missing
// *.omani.homes listener on t22). Before this fix, catalyst-api wrote
// the structural `parent_domains` JSON array but never set
// `parent_domains_yaml` — the YAML-string variable the OpenTofu
// module actually reads to derive the per-zone Cilium Gateway
// listeners. The module's single-zone fallback then silently
// dropped every org-pool entry the operator added.
//
// This test asserts that a multi-domain Request renders
// parent_domains_yaml as a JSON-flow array carrying BOTH the
// primary AND every org-pool entry, with name + role fields. The
// downstream terraform local `yamldecode(parent_domains_yaml)`
// produces a list with len == #zones, and the listener-rendering
// local emits one HTTPS/HTTP pair per zone.
func TestWriteTfvars_EmitsParentDomainsYAMLForOrgPool(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-pdyaml-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "t22.omantel.biz",
		OrgName:          "Omantel",
		OrgEmail:         "ops@omantel.biz",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		ParentDomains: []ParentDomain{
			{Name: "omantel.biz", Role: ParentDomainRolePrimary, RegistrarKind: "dynadot"},
			{Name: "omani.homes", Role: ParentDomainRoleOrgPool, RegistrarKind: "dynadot"},
		},
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, ok := parsed["parent_domains_yaml"].(string)
	if !ok {
		t.Fatalf("parent_domains_yaml must be a string, got %T: %v",
			parsed["parent_domains_yaml"], parsed["parent_domains_yaml"])
	}
	if got == "" {
		t.Fatalf("parent_domains_yaml MUST be non-empty when ParentDomains is populated. Empty falls through to single-zone fallback and drops every org-pool entry. Got: %q", got)
	}
	// The literal must be a JSON-flow array (yamldecode-compatible) so
	// the OpenTofu module's `yamldecode(var.parent_domains_yaml)` parses
	// it as a list of objects. Re-parse here as the module would.
	var entries []map[string]any
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("parent_domains_yaml must be JSON-flow / YAML-decodable: %v\nRaw: %s", err, got)
	}
	if len(entries) != 2 {
		t.Fatalf("parent_domains_yaml should carry 2 entries (primary + org-pool), got %d. Raw: %s", len(entries), got)
	}
	// The primary entry must be present and labelled.
	wantNames := map[string]string{
		"omantel.biz": ParentDomainRolePrimary,
		"omani.homes": ParentDomainRoleOrgPool,
	}
	for _, e := range entries {
		name, _ := e["name"].(string)
		role, _ := e["role"].(string)
		want, ok := wantNames[name]
		if !ok {
			t.Errorf("unexpected parent_domains_yaml entry name=%q", name)
			continue
		}
		if role != want {
			t.Errorf("entry name=%q role=%q want %q", name, role, want)
		}
		delete(wantNames, name)
	}
	if len(wantNames) != 0 {
		t.Errorf("parent_domains_yaml missing entries: %v\nRaw: %s", wantNames, got)
	}
}

// TestWriteTfvars_EmitsParentDomainsYAMLEmptyOnSingleZone verifies the
// fall-through path: a Request with NO ParentDomains slice (the legacy
// single-FQDN payload that hasn't been migrated yet) must emit
// parent_domains_yaml == "" so the terraform module's
// `coalesce(var.parent_domains_yaml, "<single-zone fallback>")` picks
// the fallback derived from sovereign_fqdn. If we emitted "[]" here,
// yamldecode would produce an empty list and the listener-rendering
// local would emit ZERO listeners — even worse than the current bug.
func TestWriteTfvars_EmitsParentDomainsYAMLEmptyOnSingleZone(t *testing.T) {
	dir, err := os.MkdirTemp("", "writeTfvars-pdyaml-single-*")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer os.RemoveAll(dir)

	req := Request{
		SovereignFQDN:    "solo.example.io",
		OrgName:          "Solo",
		OrgEmail:         "ops@example.io",
		HetznerToken:     "tok",
		HetznerProjectID: "p1",
		Region:           "fsn1",
		WorkerCount:      2,
		// ParentDomains intentionally nil — legacy single-zone payload.
	}
	if err := writeTfvars(dir, req); err != nil {
		t.Fatalf("writeTfvars: %v", err)
	}
	raw, err := os.ReadFile(dir + "/tofu.auto.tfvars.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, ok := parsed["parent_domains_yaml"].(string)
	if !ok {
		t.Fatalf("parent_domains_yaml must be a string, got %T", parsed["parent_domains_yaml"])
	}
	if got != "" {
		t.Errorf("parent_domains_yaml should be empty on single-zone payload (lets terraform module fall through to sovereign_fqdn fallback). Got: %q", got)
	}
}

// TestParentDomainsYAMLLiteral_RoundTripsCleanly is a focused unit test
// for the helper independent of writeTfvars. Verifies (a) zone names
// are lowercased + trimmed (consistent with the rest of the codebase),
// (b) missing Role defaults to "primary", (c) empty input → empty
// string (not "[]") to preserve the terraform single-zone fallback.
func TestParentDomainsYAMLLiteral_RoundTripsCleanly(t *testing.T) {
	tests := []struct {
		name string
		in   []ParentDomain
		want string
	}{
		{
			name: "empty slice → empty string",
			in:   nil,
			want: "",
		},
		{
			name: "single primary",
			in: []ParentDomain{
				{Name: "omantel.biz", Role: ParentDomainRolePrimary},
			},
			want: `[{"name":"omantel.biz","role":"primary"}]`,
		},
		{
			name: "primary + org-pool (t22 scenario)",
			in: []ParentDomain{
				{Name: "omantel.biz", Role: ParentDomainRolePrimary},
				{Name: "omani.homes", Role: ParentDomainRoleOrgPool},
			},
			want: `[{"name":"omantel.biz","role":"primary"},{"name":"omani.homes","role":"org-pool"}]`,
		},
		{
			name: "uppercase name is lowercased",
			in: []ParentDomain{
				{Name: "  OMANTEL.BIZ  ", Role: ParentDomainRolePrimary},
			},
			want: `[{"name":"omantel.biz","role":"primary"}]`,
		},
		{
			name: "missing role defaults to primary",
			in: []ParentDomain{
				{Name: "lonely.zone"},
			},
			want: `[{"name":"lonely.zone","role":"primary"}]`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parentDomainsYAMLLiteral(tc.in)
			if got != tc.want {
				t.Errorf("parentDomainsYAMLLiteral mismatch:\n  got  %q\n  want %q", got, tc.want)
			}
		})
	}
}

// TestDefaultRegistrarKindFromEnv proves Inviolable Principle #4 ──
// the default registrar adapter id is overridable via env at runtime.
// An operator running on a non-Dynadot registrar sets
// CATALYST_DEFAULT_REGISTRAR_KIND on the catalyst-api Pod and the
// Day-1 synthesis path picks it up.
func TestDefaultRegistrarKindFromEnv(t *testing.T) {
	t.Setenv("CATALYST_DEFAULT_REGISTRAR_KIND", "namecheap")
	if got := defaultRegistrarKindFromEnv(); got != "namecheap" {
		t.Errorf("env override not honoured: got %q, want namecheap", got)
	}
	t.Setenv("CATALYST_DEFAULT_REGISTRAR_KIND", "")
	if got := defaultRegistrarKindFromEnv(); got != defaultRegistrarKind {
		t.Errorf("empty env should fall back to default: got %q, want %q", got, defaultRegistrarKind)
	}
}
