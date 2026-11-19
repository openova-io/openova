// Package store implements a flat-file JSON persistence layer for Catalyst
// deployments.
//
// Why flat files (not a database):
//
//   - Catalyst-Zero provisions sovereigns on the order of "a few thousand
//     per year per franchise" — not OLTP traffic. A row count that fits in
//     a single ext4 directory with one file per deployment is sufficient.
//   - Per docs/INVIOLABLE-PRINCIPLES.md #3 we install zero new external
//     services without architectural review. A SQLite or Postgres
//     dependency would expand the catalyst-api Pod's blast radius (init
//     containers, migration scripts, sidecar, backup story) for a
//     workload that doesn't need ACID joins or query planning.
//   - The on-disk shape is a `*os.File` rewrite per change with `fsync`
//     after the write, which on a Persistent Volume backed by Hetzner
//     Cloud Volumes survives Pod evictions, image rolls, and node
//     reboots. The Deployment as a whole is the unit of consistency —
//     events are appended into the in-memory slice, then the entire
//     deployment is rewritten atomically via temp-file + rename. There
//     is no partial-write window.
//
// The store is indexed by deployment ID (a 16-char hex string from
// crypto/rand). Each deployment is one file: <dir>/<id>.json. Walking the
// directory at startup loads every deployment back into memory, so a
// catalyst-api Pod restart preserves the `/api/v1/deployments/<id>` and
// `/api/v1/deployments/<id>/events` surfaces — the user-reported regression
// where a 6-times-restarted Pod returned 404 for an in-progress wizard.
//
// Secrets are redacted before serialization. See RedactedRequest below;
// the redaction list is tracked in tests so a future field addition that
// looks like a credential lands as a test failure rather than a leak.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// redactedMarker is the literal value substituted for any field that the
// redaction list flags as a credential. Tests assert exact equality on
// this marker so a regex change here shows up as a test diff, not a leak.
const redactedMarker = provisioner.RedactedMarker

// Record is the on-disk shape of a single deployment. The catalyst-api
// handler.Deployment type carries goroutine-coordination fields
// (eventsCh, done, mu, sync.Map containment) that are NOT persisted —
// those are runtime-only. This Record is the JSON-safe projection.
//
// Field order intentionally mirrors the Deployment struct so a diff
// between a live State() snapshot and an on-disk record is mechanical.
type Record struct {
	ID         string                  `json:"id"`
	Status     string                  `json:"status"`
	Request    RedactedRequest         `json:"request"`
	Result     *provisioner.Result     `json:"result,omitempty"`
	Error      string                  `json:"error,omitempty"`
	StartedAt  time.Time               `json:"startedAt"`
	FinishedAt time.Time               `json:"finishedAt,omitempty"`
	Events     []provisioner.Event     `json:"events"`

	// PDM reservation fields. The reservation token is a per-deployment
	// opaque string, NOT a credential — it identifies the reservation
	// PDM holds for this deployment's subdomain so the commit/release
	// path knows which row to mutate. Persisting it across Pod restarts
	// is required so a restarted catalyst-api can still call /commit on
	// PDM after `tofu apply` returns the LB IP.
	PDMReservationToken string `json:"pdmReservationToken,omitempty"`
	PDMPoolDomain       string `json:"pdmPoolDomain,omitempty"`
	PDMSubdomain        string `json:"pdmSubdomain,omitempty"`

	// KubeconfigBearerHash — hex-encoded SHA-256 of the 32-byte bearer
	// token issued at CreateDeployment time and templated into the
	// new Sovereign's cloud-init (issue #183, Option D). The PUT
	// /api/v1/deployments/{id}/kubeconfig handler computes SHA-256 of
	// the received Authorization: Bearer token and constant-time
	// compares to this value before accepting the kubeconfig.
	//
	// Persisting the HASH (not the plaintext) survives a Pod restart
	// without exposing the bearer to anyone with read access to the
	// PVC. The hash on its own is not sufficient to authorise a PUT;
	// the cloud-init runcmd is the only holder of the plaintext, and
	// it is consumed in a single PUT then discarded.
	KubeconfigBearerHash string `json:"kubeconfigBearerHash,omitempty"`

	// AdoptedAt — handover-finalisation timestamp (issue #317 writes;
	// issue #319 reads). Once set, the Sovereign is operationally self-
	// sufficient; console.openova.io/sovereign/<id> redirects to the
	// customer's own console.<sovereign-fqdn>. Persisted across Pod
	// restarts so a redirect that should fire still fires after a
	// catalyst-api roll.
	AdoptedAt *time.Time `json:"adoptedAt,omitempty"`

	// OwnerEmail — the operator who created this deployment (issue #689).
	// Stamped at CreateDeployment time from the authenticated session
	// (X-User-Email injected by auth.RequireSession middleware). Every
	// read/mutate handler on /deployments/{id}* enforces ownership by
	// comparing the request's session email against this value; a
	// mismatch yields 404 (NOT 403 — the 404 distinction is deliberate
	// per issue #689 so the existence of someone else's deployment is
	// never leaked).
	//
	// Legacy compatibility: deployments persisted before the field was
	// added will deserialize with OwnerEmail == "". The ownership check
	// MUST treat that as "legacy — skip the check, log a warning" so a
	// catalyst-api upgrade doesn't lock operators out of their own
	// pre-existing deployments. New deployments always populate it.
	OwnerEmail string `json:"ownerEmail,omitempty"`

	// RetryCalls lists every accepted call of a route whose effect is "run
	// this install step again" (gate check FND-G02, Zero touch), oldest
	// first. The hand-over carries it to the Sovereign with the rest of the
	// record, and the Sovereign appends its own calls to the same list.
	//
	// nil and empty differ on purpose. A deployment created by an engine
	// that records retry calls starts with an empty list, written as [];
	// nil (written as null, or absent in a record of an older engine) means
	// the calls were never recorded, so "no retry call" was not measured.
	// No omitempty: an empty list must stay [] across a save and a load.
	RetryCalls []RetryCall `json:"retryCalls"`

	// RetryCallsSent is how many entries of RetryCalls the Sovereign confirmed
	// it holds, as the parent counts them: the parent sends the list again
	// until the Sovereign confirms them all, after a restart too
	// (retrycall.Log.Deliver). A Sovereign does not read it.
	RetryCallsSent int `json:"retryCallsSent,omitempty"`
}

// RetryCall is one accepted retry call of a deployment.
type RetryCall struct {
	At     time.Time `json:"at"`     // when the engine accepted the call, UTC
	Route  string    `json:"route"`  // method and route pattern, e.g. "POST /api/v1/deployments/{id}/phases/{phase}/retry"
	Target string    `json:"target"` // what was run again, e.g. "phase tofu-apply"
	By     string    `json:"by"`     // the session's email or subject, "operator" when the request carried neither
}

// RedactedRequest is the on-disk projection of provisioner.Request with
// every credential replaced by redactedMarker. The struct intentionally
// mirrors provisioner.Request field-for-field (minus json:"-" tagged
// fields, which are restored on deserialization since they aren't in
// the serialized form anyway) so the wizard's error display can render
// the non-secret context — region, FQDN, control-plane size, regions
// list — without ever seeing the customer's Hetzner token.
//
// Field tags use the same JSON keys as provisioner.Request so a manual
// `cat /var/lib/catalyst/deployments/<id>.json` reads naturally.
type RedactedRequest struct {
	OrgName  string `json:"orgName,omitempty"`
	OrgEmail string `json:"orgEmail,omitempty"`

	SovereignFQDN       string `json:"sovereignFQDN,omitempty"`
	SovereignDomainMode string `json:"sovereignDomainMode,omitempty"`
	SovereignPoolDomain string `json:"sovereignPoolDomain,omitempty"`
	SovereignSubdomain  string `json:"sovereignSubdomain,omitempty"`

	// ParentDomains — multi-domain pool persisted across the
	// catalyst-api Pod restart boundary (issue #826, sub-1 of epic
	// #825). The slice carries per-domain metadata only; per-domain
	// registrar credentials are NEVER persisted on the deployment
	// record — RegistrarCredsRef points at a SealedSecret in
	// catalyst-system whose plaintext lives only in K8s, never on
	// the catalyst-api PVC. See provisioner.ParentDomain for field
	// semantics.
	//
	// Backward compatibility: a record persisted under the legacy
	// single-FQDN shape (no `parentDomains` key in JSON) deserializes
	// with the slice empty; ToProvisionerRequest re-runs the
	// provisioner.Validate() migration path on the next read so the
	// rehydrated request carries the synthesised primary entry. The
	// next Save() that writes this record back to disk emits the
	// array form — the migration is transparent to operators.
	ParentDomains []provisioner.ParentDomain `json:"parentDomains,omitempty"`

	// Wave 5.131 (hw30 fix-forward 2026-05-27): persist top-level
	// Provider so reload after Pod restart correctly dispatches the
	// validator branch. Pre-Wave-5.131, the field was silently dropped
	// at Save() time; on reload the rehydrated request had Provider=""
	// → validator branched to hetzner → "hetzner token is required"
	// regardless of provider. The RegionSpec carries provider too but
	// the validator's top-level switch keys off req.Provider.
	Provider string `json:"provider,omitempty"`

	// Installation — the installation document the request named (CLD-103),
	// non-secret, persisted verbatim.
	Installation string `json:"installation,omitempty"`

	// Credentials — the request's credential map (CLD-103) with every key
	// kept and every non-empty value replaced by the redaction marker, so a
	// reader sees which credentials were attached and never their values;
	// the value of a key that the cloud profile marks not secret is kept
	// (provisioner.StoredCredentialKeys).
	Credentials map[string]string `json:"credentials,omitempty"`

	Region           string `json:"region,omitempty"`
	ControlPlaneSize string `json:"controlPlaneSize,omitempty"`
	WorkerSize       string `json:"workerSize,omitempty"`
	WorkerCount      int    `json:"workerCount,omitempty"`

	// StorageClass — the operator-chosen default StorageClass (#4057),
	// non-secret. Persisted so a reload after a Pod restart preserves the
	// wizard's storage choice for the Settings page + FailureCard
	// diagnostic context (the same rationale Provider/ControlPlaneSize are
	// persisted). Empty means the operator accepted the per-provider CSI
	// default; the effective value is re-derived by deriveStorageClass at
	// writeTfvars time.
	StorageClass string `json:"storageClass,omitempty"`

	HAEnabled bool `json:"haEnabled,omitempty"`

	Regions []provisioner.RegionSpec `json:"regions,omitempty"`

	SSHPublicKey string `json:"sshPublicKey,omitempty"`

	// Dynadot credentials are persisted ONLY as the redaction marker.
	// We preserve the JSON field so a reader can see "yes, credentials
	// were attached to this deployment" without seeing their value.
	DynadotAPIKey    string `json:"dynadotKey,omitempty"`
	DynadotAPISecret string `json:"dynadotSecret,omitempty"`

	// RegistrarToken — for BYO Flow B (issue #169) the wizard captures
	// the customer's registrar API token (Dynadot, Namecheap, Cloudflare,
	// ...) so catalyst-api can flip nameservers from the registrar's
	// default to the Sovereign's NS records. Same redaction policy:
	// presence yes, value no.
	RegistrarToken string `json:"registrarToken,omitempty"`

	// Hetzner Object Storage (issue #371). Region + bucket are NOT
	// secret — preserved verbatim so the deployment record reads
	// naturally on disk and the rehydrate path can reconstruct the
	// non-secret context for the wizard's failure card. Access + secret
	// keys ARE redacted with the marker; presence is preserved so a
	// reader can distinguish "Sovereign was provisioned with Object
	// Storage credentials" from "no credentials were ever attached".
	ObjectStorageRegion    string `json:"objectStorageRegion,omitempty"`
	ObjectStorageBucket    string `json:"objectStorageBucket,omitempty"`
	ObjectStorageAccessKey string `json:"objectStorageAccessKey,omitempty"`
	ObjectStorageSecretKey string `json:"objectStorageSecretKey,omitempty"`

	// MarketplaceEnabled — PR P (2026-05-17 t144 founder bug #5 deep fix):
	// preserve the prov-body flag through persistence + export so the
	// chroot's GET /api/v1/sovereigns/{id}/marketplace endpoint
	// (PR J #1590) returns the actual value instead of false. Without
	// this field on the RedactedRequest, every export-then-load cycle
	// stripped the bit and /settings/marketplace toggle defaulted to
	// disabled even on a marketplace-enabled Sovereign.
	MarketplaceEnabled bool `json:"marketplaceEnabled,omitempty"`
}

// Redact returns a RedactedRequest derived from req with every
// credential field replaced by redactedMarker (when present). A field
// that was empty in the original request stays empty in the redacted
// form — we don't want a wizard that omitted DynadotAPIKey to look
// like it had one redacted away.
func Redact(req provisioner.Request) RedactedRequest {
	out := RedactedRequest{
		OrgName:             req.OrgName,
		OrgEmail:            req.OrgEmail,
		SovereignFQDN:       req.SovereignFQDN,
		SovereignDomainMode: req.SovereignDomainMode,
		SovereignPoolDomain: req.SovereignPoolDomain,
		SovereignSubdomain:  req.SovereignSubdomain,
		Provider:            req.Provider, // Wave 5.131 — persist provider routing
		Region:              req.Region,
		ControlPlaneSize:    req.ControlPlaneSize,
		WorkerSize:          req.WorkerSize,
		WorkerCount:         req.WorkerCount,
		StorageClass:        req.StorageClass, // #4057 — persist the operator's storage choice (non-secret)
		HAEnabled:           req.HAEnabled,
		Regions:             req.Regions,
		SSHPublicKey:        req.SSHPublicKey,
		// Multi-domain pool (issue #826). Persisted verbatim — the
		// fields on ParentDomain are non-secret (Name + Role +
		// RegistrarKind + RegistrarCredsRef + AddedAt). The
		// RegistrarCredsRef points at a SealedSecret name; the
		// plaintext registrar credentials are NEVER on this record.
		ParentDomains: req.ParentDomains,
		// Object Storage non-secret context — region + bucket are public
		// in the sense that they appear in tofu outputs and the cluster's
		// Secret stringData on every reconciliation. Persisted verbatim.
		ObjectStorageRegion: req.ObjectStorageRegion,
		ObjectStorageBucket: req.ObjectStorageBucket,
		// PR P (2026-05-17): MarketplaceEnabled preserved through redact path.
		MarketplaceEnabled: req.MarketplaceEnabled,
	}
	// Credentials: present-and-non-empty → redactedMarker; empty → empty.
	// This is the test-load-bearing branch for TestRedact_OmitsAllSecrets.
	// The credentials map keeps its keys; every value is redacted except
	// that of a key the cloud profile marks not secret, which a reloaded
	// record still needs (TestRedact_KeepsTheNonSecretCredentials).
	out.Installation = req.Installation
	if len(req.Credentials) > 0 {
		plain := provisioner.StoredCredentialKeys(provisioner.ResolveProvider(&req))
		out.Credentials = make(map[string]string, len(req.Credentials))
		for k, v := range req.Credentials {
			if strings.TrimSpace(v) != "" && !plain[k] {
				v = redactedMarker
			}
			out.Credentials[k] = v
		}
	}
	if strings.TrimSpace(req.DynadotAPIKey) != "" {
		out.DynadotAPIKey = redactedMarker
	}
	if strings.TrimSpace(req.DynadotAPISecret) != "" {
		out.DynadotAPISecret = redactedMarker
	}
	// Object Storage credentials (issue #371) — same policy: presence
	// preserved as the redacted marker, plaintext NEVER on disk.
	if strings.TrimSpace(req.ObjectStorageAccessKey) != "" {
		out.ObjectStorageAccessKey = redactedMarker
	}
	if strings.TrimSpace(req.ObjectStorageSecretKey) != "" {
		out.ObjectStorageSecretKey = redactedMarker
	}
	return out
}

// UnmarshalJSON decodes a stored request. A record written before the
// credentials map carried every credential holds the value of a non-secret
// one (provisioner.StoredCredentialKeys) in a legacy field that the adapter
// of its provider names (providers.LegacyAliaser); that value is copied into
// Credentials when the map holds the key empty, redacted or not at all, so a
// reload and the next Save keep it.
func (r *RedactedRequest) UnmarshalJSON(data []byte) error {
	type plain RedactedRequest
	if err := json.Unmarshal(data, (*plain)(r)); err != nil {
		return err
	}
	name := provisioner.ResolveProvider(&provisioner.Request{Provider: r.Provider, Regions: r.Regions})
	aliaser, ok, err := providers.As[providers.LegacyAliaser](name)
	if err != nil || !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	keep := provisioner.StoredCredentialKeys(name)
	for legacy, key := range aliaser.LegacyCredentialAliases() {
		var v string
		if !keep[key] || json.Unmarshal(fields[legacy], &v) != nil || strings.TrimSpace(v) == "" || v == redactedMarker {
			continue
		}
		if cur := r.Credentials[key]; strings.TrimSpace(cur) != "" && cur != redactedMarker {
			continue
		}
		if r.Credentials == nil {
			r.Credentials = map[string]string{}
		}
		r.Credentials[key] = v
	}
	return nil
}

// ToProvisionerRequest reconstructs a provisioner.Request from the
// redacted on-disk projection. The credential fields come back as
// the redacted marker (or empty if they were empty originally) — no
// re-running of OpenTofu is possible from this struct alone, which
// is intentional. The point of rehydrating Request after a Pod
// restart is to preserve the wizard's diagnostic context (FQDN,
// region, sizes, regions list) for the FailureCard, not to resume
// the apply.
func (r RedactedRequest) ToProvisionerRequest() provisioner.Request {
	return provisioner.Request{
		OrgName:             r.OrgName,
		OrgEmail:            r.OrgEmail,
		SovereignFQDN:       r.SovereignFQDN,
		SovereignDomainMode: r.SovereignDomainMode,
		SovereignPoolDomain: r.SovereignPoolDomain,
		SovereignSubdomain:  r.SovereignSubdomain,
		// Multi-domain pool rehydrate (issue #826). Records persisted
		// before the field was added deserialize with this empty;
		// provisioner.Request.Validate() then synthesises the primary
		// entry on the next call — the migration is transparent.
		ParentDomains:    r.ParentDomains,
		Provider:         r.Provider, // Wave 5.131 — restore provider routing
		Installation:     r.Installation,
		Credentials:      maps.Clone(r.Credentials), // <redacted>, "" or a non-secret value
		Region:           r.Region,
		ControlPlaneSize: r.ControlPlaneSize,
		WorkerSize:       r.WorkerSize,
		WorkerCount:      r.WorkerCount,
		StorageClass:     r.StorageClass, // #4057 — restore the operator's storage choice
		HAEnabled:        r.HAEnabled,
		Regions:          r.Regions,
		SSHPublicKey:     r.SSHPublicKey,
		DynadotAPIKey:    r.DynadotAPIKey,    // <redacted> or ""
		DynadotAPISecret: r.DynadotAPISecret, // <redacted> or ""
		// Object Storage rehydrate (issue #371) — region + bucket are
		// the verbatim values; access + secret come back as the redacted
		// marker so a Pod restart can render the wizard's failure card
		// with full context but cannot resume `tofu apply` (the keys
		// would have to be re-supplied by the operator).
		ObjectStorageRegion:    r.ObjectStorageRegion,
		ObjectStorageBucket:    r.ObjectStorageBucket,
		ObjectStorageAccessKey: r.ObjectStorageAccessKey,
		ObjectStorageSecretKey: r.ObjectStorageSecretKey,
		MarketplaceEnabled:     r.MarketplaceEnabled,
	}
}

// Store is the directory-backed persistence layer.
//
// Concurrency: every public method takes a per-store mutex before
// touching the filesystem. Deployment-level callers already serialize
// writes to a given ID via dep.mu; the store's mutex protects the
// directory walk in Load and the temp-file rename in Save against
// each other.
type Store struct {
	dir string
	mu  sync.Mutex
}

// New returns a Store rooted at dir. The directory is created (with
// 0o700 perms) if it doesn't exist; an error is returned only if the
// directory cannot be created or is not writable.
//
// 0o700 is intentional: the deployment files contain orchestration
// state that, while redacted of credentials, still leaks org names,
// email addresses, and FQDNs for every Sovereign the catalyst-api has
// ever provisioned. Restricting to the catalyst-api process's UID is
// the correct posture.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: directory path is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create directory %q: %w", dir, err)
	}
	// Probe writability — a PVC mounted with the wrong UID surfaces
	// here, not at the first deployment-create.
	probe := filepath.Join(dir, ".write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: write-probe %q: %w (PVC mount may be read-only or wrong UID)", probe, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return &Store{dir: dir}, nil
}

// Dir returns the directory the store is rooted at. Used by tests and
// by the manual-proof harness in cmd/api smoke tests.
func (s *Store) Dir() string { return s.dir }

// path returns the canonical on-disk path for a given deployment id.
// IDs are hex-encoded so they don't contain path separators; we still
// reject any caller-supplied id that does, defending against a future
// caller passing a slash-bearing identifier.
func (s *Store) path(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return "", fmt.Errorf("store: invalid deployment id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Save serializes rec and writes it atomically to <dir>/<id>.json.
//
// Atomicity: write to a temp file in the same directory, fsync, then
// os.Rename onto the final name. ext4 + most modern filesystems make
// rename within a single directory atomic from the reader's
// perspective. A reader that races with this write either sees the
// pre-update file or the post-update file, never a half-written one.
//
// fsync is required because a Pod kill in the middle of a write would
// otherwise lose the data even though the rename had been issued — the
// kernel buffers the page cache write, and the kill happens before
// flush. fsync guarantees the bytes are on the volume.
func (s *Store) Save(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	final, err := s.path(rec.ID)
	if err != nil {
		return err
	}

	// Encoder rather than MarshalIndent — we disable HTML escaping so
	// the redaction marker `<redacted>` lands literally on disk
	// instead of as `<redacted>`. The file is not embedded
	// in HTML; it's a flat JSON record consumed only by catalyst-api
	// itself, so HTML-escaping has no callers but breaks readability
	// and grep ergonomics.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("store: marshal deployment %q: %w", rec.ID, err)
	}
	data := buf.Bytes()

	tmp, err := os.CreateTemp(s.dir, "."+rec.ID+".*.json.tmp")
	if err != nil {
		return fmt.Errorf("store: create temp file for %q: %w", rec.ID, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup of the temp file on any error path. If rename
	// succeeds, the temp file is gone and Remove is a no-op.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temp file for %q: %w", rec.ID, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: fsync temp file for %q: %w", rec.ID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp file for %q: %w", rec.ID, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("store: rename %q to %q: %w", tmpName, final, err)
	}
	return nil
}

// LoadAll walks the store directory and returns every deployment
// record that successfully decoded. Files that fail to decode (a
// half-written record from a pre-fsync regression, manual editing,
// disk corruption) are reported via the per-file error callback so
// the caller can log them, but do not abort the whole load — a single
// corrupt file must not prevent all OTHER deployments from being
// recovered.
//
// The onErr callback is invoked synchronously per-file. Pass nil to
// silently skip corrupted files (used in tests that intentionally
// drop garbage in the directory).
func (s *Store) LoadAll(onErr func(path string, err error)) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("store: read dir %q: %w", s.dir, err)
	}

	out := make([]Record, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		// Ignore temp files (pre-rename crashes leave them behind),
		// hidden files, and anything not matching the .json suffix.
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		full := filepath.Join(s.dir, name)
		raw, err := os.ReadFile(full)
		if err != nil {
			if onErr != nil {
				onErr(full, err)
			}
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			if onErr != nil {
				onErr(full, fmt.Errorf("decode: %w", err))
			}
			continue
		}
		// A file with no ID is unusable — we key the in-memory map by
		// it. Drop with onErr.
		if rec.ID == "" {
			if onErr != nil {
				onErr(full, errors.New("record has empty ID"))
			}
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Load returns the record for a single deployment, or os.ErrNotExist
// if no file exists for that id. Used by tests and by ad-hoc tooling;
// the handler primarily uses LoadAll at startup.
func (s *Store) Load(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	final, err := s.path(id)
	if err != nil {
		return Record{}, err
	}
	raw, err := os.ReadFile(final)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("store: decode %q: %w", final, err)
	}
	return rec, nil
}

// Delete removes the on-disk file for id. Idempotent — a missing file
// returns nil (so a delete-twice doesn't panic the caller).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	final, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(final); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: remove %q: %w", final, err)
	}
	return nil
}
