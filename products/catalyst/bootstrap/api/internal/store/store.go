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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// redactedMarker is the literal value substituted for any field that the
// redaction list flags as a credential. Tests assert exact equality on
// this marker so a regex change here shows up as a test diff, not a leak.
const redactedMarker = "<redacted>"

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

	HetznerToken     string `json:"hetznerToken,omitempty"`
	HetznerProjectID string `json:"hetznerProjectID,omitempty"`

	Region           string `json:"region,omitempty"`
	ControlPlaneSize string `json:"controlPlaneSize,omitempty"`
	WorkerSize       string `json:"workerSize,omitempty"`
	WorkerCount      int    `json:"workerCount,omitempty"`

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
		HetznerProjectID:    req.HetznerProjectID,
		Region:              req.Region,
		ControlPlaneSize:    req.ControlPlaneSize,
		WorkerSize:          req.WorkerSize,
		WorkerCount:         req.WorkerCount,
		HAEnabled:           req.HAEnabled,
		Regions:             req.Regions,
		SSHPublicKey:        req.SSHPublicKey,
	}
	// Credentials: present-and-non-empty → redactedMarker; empty → empty.
	// This is the test-load-bearing branch for TestRedact_OmitsAllSecrets.
	if strings.TrimSpace(req.HetznerToken) != "" {
		out.HetznerToken = redactedMarker
	}
	if strings.TrimSpace(req.DynadotAPIKey) != "" {
		out.DynadotAPIKey = redactedMarker
	}
	if strings.TrimSpace(req.DynadotAPISecret) != "" {
		out.DynadotAPISecret = redactedMarker
	}
	return out
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
		HetznerToken:        r.HetznerToken, // <redacted> or ""
		HetznerProjectID:    r.HetznerProjectID,
		Region:              r.Region,
		ControlPlaneSize:    r.ControlPlaneSize,
		WorkerSize:          r.WorkerSize,
		WorkerCount:         r.WorkerCount,
		HAEnabled:           r.HAEnabled,
		Regions:             r.Regions,
		SSHPublicKey:        r.SSHPublicKey,
		DynadotAPIKey:       r.DynadotAPIKey,    // <redacted> or ""
		DynadotAPISecret:    r.DynadotAPISecret, // <redacted> or ""
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
