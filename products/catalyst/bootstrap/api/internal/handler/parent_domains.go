// parent_domains.go — admin-console "add another parent domain" flow +
// live DNS propagation status panel (issue #829, parent epic #825).
//
// Background — multi-domain Sovereign (epic #825)
// ------------------------------------------------
// A franchised Sovereign supports N parent domains, not 1. The operator
// brings:
//   - the primary domain serving console.<primary>, api.<primary>, etc.
//   - zero-or-more "org-pool" domains offered to SME tenants for free
//     subdomain allocation
// A post-handover surface in the Sovereign Console lets the operator add
// MORE parent domains over time (e.g. acquired a new portfolio domain).
//
// This file owns the four endpoints for that admin surface:
//
//   GET    /api/v1/sovereign/parent-domains              — list current pool
//   POST   /api/v1/sovereign/parent-domains              — add a new domain
//   DELETE /api/v1/sovereign/parent-domains/{name}       — remove a domain
//   GET    /api/v1/sovereign/parent-domains/{name}/propagation
//                                                        — per-resolver
//                                                          NS-flip propagation
//
// Persistence (issue #837)
// ------------------------
// Sister tickets #826 (PR #835) and #829 (PR #834) merged on top of each
// other: #826 introduced the canonical `Deployment.parentDomains[]` data
// model + reusable `provisioner.ProvisionParentDomain` per-domain
// pipeline; #829 shipped the admin-console add-domain handler against an
// in-memory `parentDomainStore` placeholder, with a comment that the
// store would swap to the persistent record once #826 merged.
//
// THIS file is that swap. Reads and writes are now backed by the
// adopted Deployment's `Request.ParentDomains` slice — survives
// catalyst-api Pod restarts, image rolls, OOM kills. The wire shape
// returned to the admin UI is unchanged: the handler-layer
// `ParentDomain` struct (Name, Role, FlipStatus, FlipMessage,
// AddedAt, FlippedAt) is still what the JSON response carries, but
// the durable state behind it is the canonical
// `provisioner.ParentDomain` (Name, Role, RegistrarKind,
// RegistrarCredsRef, AddedAt) on the on-disk record.
//
// FlipStatus semantics in the persistent world:
//   - Entries on the persistent record represent SUCCESSFUL adds (the
//     full pipeline ran: NS-flip → zone-create → cert-issue). They
//     surface as FlipStatusReady on subsequent GETs.
//   - In-flight pipeline progress is reported synchronously in the
//     POST response only. A failed pipeline does NOT persist a
//     "failed" row — per the wipe-and-restart Catalyst-Zero rule, the
//     operator retries the add; nothing lingers in the pool.
//
// Implementation note for the propagation panel:
// Go's net.Resolver supports custom Dial that lets us route NS lookups
// through a SPECIFIC resolver IP (8.8.8.8, 1.1.1.1, etc) rather than
// the system resolv.conf. We spin one goroutine per resolver, run
// LookupNS with a 5s deadline, and aggregate the results. The polling
// rate-limit lives client-side: the UI polls this endpoint every 60s,
// which is plenty given DNS gTLD TTL is 48h. The endpoint itself is
// stateless — every request fans out fresh.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #1 (waterfall, target-state shape): the wire shape this file emits is
//       the final shape — `parentDomains[]` with role + flipStatus +
//       perResolverPropagation. Persistence shape is the canonical
//       provisioner.ParentDomain on the deployment record.
//   #4 (never hardcode): the resolver list lives in `defaultResolvers`,
//       overridable via `CATALYST_DNS_PROPAGATION_RESOLVERS` env. The
//       per-query timeout + poll-rate are also env-tunable.
//   #10 (credential hygiene): registrar API credentials submitted in the
//       AddParentDomain POST are forwarded byte-for-byte to PDM via the
//       existing /set-ns proxy seam in registrar.go — they never enter a
//       struct that gets logged AND never get persisted onto the
//       deployment record (only the non-secret RegistrarKind +
//       RegistrarCredsRef fields are durable).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ParentDomainRole names the two purposes a parent domain can serve in
// the Sovereign's pool. Mirrors the canonical shape from #826.
type ParentDomainRole string

const (
	// RolePrimary — the operator's own domain (console.<name>,
	// api.<name>, marketplace.<name>). Exactly one per Sovereign.
	RolePrimary ParentDomainRole = "primary"
	// RoleOrgPool — offered to SME tenants for free-subdomain
	// allocation (e.g. console.acme.<name>). Zero-or-more per Sovereign.
	RoleOrgPool ParentDomainRole = "org-pool"
)

// FlipStatus — high-level state of the NS-flip + zone-create + cert
// pipeline for a single parent domain. Consumed by the admin UI to
// render per-row badges.
type FlipStatus string

const (
	FlipStatusQueued     FlipStatus = "queued"
	FlipStatusFlipping   FlipStatus = "flipping"
	FlipStatusFlipped    FlipStatus = "flipped"
	FlipStatusFailed     FlipStatus = "failed"
	FlipStatusZoneCreate FlipStatus = "zone-creating"
	FlipStatusCertIssue  FlipStatus = "cert-issuing"
	FlipStatusReady      FlipStatus = "ready"
)

// ParentDomain is the wire-shape entry the admin surface renders + that
// AddParentDomain accepts (minus the credentials, which travel separately).
//
// Wire-shape only. Persistent state lives on the canonical
// `provisioner.ParentDomain` (Name, Role, RegistrarKind,
// RegistrarCredsRef, AddedAt) on the adopted Deployment's Request. The
// FlipStatus + FlipMessage + FlippedAt fields are derived in the
// response: persisted entries surface as FlipStatusReady; in-flight
// adds surface their pipeline state synchronously in the POST response
// before the row is committed.
type ParentDomain struct {
	Name              string           `json:"name"`
	Role              ParentDomainRole `json:"role"`
	RegistrarKind     string           `json:"registrarKind,omitempty"`
	RegistrarCredsRef string           `json:"registrarCredsRef,omitempty"`
	FlipStatus        FlipStatus       `json:"flipStatus"`
	FlipMessage       string           `json:"flipMessage,omitempty"`
	AddedAt           time.Time        `json:"addedAt"`
	FlippedAt         *time.Time       `json:"flippedAt,omitempty"`
}

// fromProvisioner converts a canonical provisioner.ParentDomain (the
// on-disk persistent shape) into the handler's wire shape used by the
// admin UI. Persisted entries always surface as FlipStatusReady — the
// presence of the row IS the proof the pipeline succeeded; failed
// adds are not persisted (see the file-level "Persistence" comment).
func fromProvisioner(p provisioner.ParentDomain) ParentDomain {
	role := RoleOrgPool
	if p.Role == provisioner.ParentDomainRolePrimary {
		role = RolePrimary
	}
	return ParentDomain{
		Name:              p.Name,
		Role:              role,
		RegistrarKind:     p.RegistrarKind,
		RegistrarCredsRef: p.RegistrarCredsRef,
		FlipStatus:        FlipStatusReady,
		AddedAt:           p.AddedAt,
	}
}

// addParentDomainRequest — POST body shape. RegistrarToken is the
// secret-bearing field; like registrar.go we never log it.
type addParentDomainRequest struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	RegistrarKind  string `json:"registrarKind"`
	RegistrarToken string `json:"registrarToken"`
}

// validateDomainName rejects obvious malformed inputs early. Production
// guards live PDM-side; this is a quick FE-feedback layer only.
func validateDomainName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("missing-name")
	}
	if len(name) > 253 {
		return fmt.Errorf("name-too-long")
	}
	if strings.Contains(name, "/") || strings.Contains(name, " ") {
		return fmt.Errorf("invalid-name")
	}
	if !strings.Contains(name, ".") {
		return fmt.Errorf("not-fqdn")
	}
	return nil
}

// activeDeployment returns the adopted Deployment whose
// Request.ParentDomains is the durable parent-domain pool for this
// Sovereign. The catalyst-api running on a Sovereign post-handover has
// exactly one adopted deployment (the operator's own); the lookup
// below picks it deterministically.
//
// On Catalyst-Zero (no adopted deployment) the function returns nil —
// the admin parent-domains panel only makes sense on an adopted
// Sovereign, so callers respond with an empty pool when this is nil
// (matching the legacy behaviour where the in-memory store was simply
// empty).
//
// When multiple adopted deployments exist (rare — typically only the
// home Sovereign reaches that state), returns the lexically-first by
// SovereignFQDN for determinism. This mirrors the same selection
// policy lookupPrimaryDomain has used since #829 shipped.
func (h *Handler) activeDeployment() *Deployment {
	type candidate struct {
		fqdn string
		dep  *Deployment
	}
	var candidates []candidate
	h.deployments.Range(func(_, v any) bool {
		dep, ok := v.(*Deployment)
		if !ok {
			return true
		}
		dep.mu.Lock()
		fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
		adopted := dep.AdoptedAt != nil
		dep.mu.Unlock()
		if fqdn != "" && adopted {
			candidates = append(candidates, candidate{fqdn: fqdn, dep: dep})
		}
		return true
	})
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].fqdn < candidates[j].fqdn })
	return candidates[0].dep
}

// listParentDomainsFromActive snapshots the adopted Deployment's
// Request.ParentDomains slice into a sorted []ParentDomain in wire
// shape. Holds dep.mu only long enough to copy the slice.
func listParentDomainsFromActive(dep *Deployment) []ParentDomain {
	if dep == nil {
		return []ParentDomain{}
	}
	dep.mu.Lock()
	pds := append([]provisioner.ParentDomain(nil), dep.Request.ParentDomains...)
	dep.mu.Unlock()

	out := make([]ParentDomain, 0, len(pds))
	for _, p := range pds {
		out = append(out, fromProvisioner(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findParentDomain returns the canonical provisioner.ParentDomain for
// the given lower-cased name on the adopted Deployment, or false when
// not present. Used by AddParentDomain (idempotency check) and
// DeleteParentDomain (removal).
func findParentDomain(dep *Deployment, name string) (provisioner.ParentDomain, bool) {
	if dep == nil {
		return provisioner.ParentDomain{}, false
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	for _, pd := range dep.Request.ParentDomains {
		if strings.EqualFold(pd.Name, name) {
			return pd, true
		}
	}
	return provisioner.ParentDomain{}, false
}

// appendParentDomain commits a successful add to the adopted
// Deployment's persistent record. Holds dep.mu while mutating the
// slice; caller invokes h.persistDeployment afterwards so the on-disk
// record reflects the new pool.
//
// The append uses the canonical provisioner.ParentDomain shape so a
// catalyst-api Pod restart re-reads the same row via fromRecord →
// rec.Request.ToProvisionerRequest, and the next ListParentDomains
// surfaces it via fromProvisioner.
func appendParentDomain(dep *Deployment, pd provisioner.ParentDomain) {
	if dep == nil {
		return
	}
	dep.mu.Lock()
	dep.Request.ParentDomains = append(dep.Request.ParentDomains, pd)
	dep.mu.Unlock()
}

// removeParentDomainByName drops the entry whose Name (case-insensitive)
// matches name from the adopted Deployment's slice. Returns true when
// a row was actually removed. Caller invokes h.persistDeployment
// afterwards.
func removeParentDomainByName(dep *Deployment, name string) bool {
	if dep == nil {
		return false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	dep.mu.Lock()
	defer dep.mu.Unlock()
	for i, pd := range dep.Request.ParentDomains {
		if strings.EqualFold(pd.Name, name) {
			dep.Request.ParentDomains = append(dep.Request.ParentDomains[:i], dep.Request.ParentDomains[i+1:]...)
			return true
		}
	}
	return false
}

// ListParentDomains handles GET /api/v1/sovereign/parent-domains.
//
// The shape is `{"items": [...]}` to match the rest of the catalyst-api
// list endpoints (UserAccess, deployments, etc).
//
// Persistence: reads from the adopted Deployment's Request.ParentDomains
// slice (issue #837 swap from the in-memory placeholder). When no
// deployment has been adopted yet (Catalyst-Zero / fresh wizard run)
// the function falls back to the env-var-based primary synthesis path
// so single-Sovereign sandboxes + tests keep rendering the implicit
// primary row.
func (h *Handler) ListParentDomains(w http.ResponseWriter, r *http.Request) {
	dep := h.activeDeployment()

	items := listParentDomainsFromActive(dep)

	// Synthesise the implicit "primary" row when the active deployment's
	// slice is empty (legacy single-FQDN record migrated lazily — see
	// provisioner.Validate's migration path) or no adopted deployment
	// exists yet (Catalyst-Zero / test sandbox using
	// CATALYST_PRIMARY_DOMAIN). Idempotent against an already-listed
	// primary entry.
	primaryName := h.lookupPrimaryDomain()
	if primaryName != "" {
		alreadyListed := false
		for _, it := range items {
			if strings.EqualFold(it.Name, primaryName) {
				alreadyListed = true
				break
			}
		}
		if !alreadyListed {
			items = append([]ParentDomain{{
				Name:       primaryName,
				Role:       RolePrimary,
				FlipStatus: FlipStatusReady,
				AddedAt:    time.Time{},
			}}, items...)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

// lookupPrimaryDomain returns the SovereignFQDN of any adopted deployment
// (i.e. the operator has finalised handover). For the Catalyst-Zero case
// where no deployment has been adopted yet, returns the empty string.
//
// Best-effort: when multiple adopted deployments exist (rare — typically
// only the home Sovereign reaches that state), returns the lexically
// first one for determinism.
func (h *Handler) lookupPrimaryDomain() string {
	var candidates []string
	h.deployments.Range(func(_, v any) bool {
		dep, ok := v.(*Deployment)
		if !ok {
			return true
		}
		dep.mu.Lock()
		fqdn := strings.TrimSpace(dep.Request.SovereignFQDN)
		adopted := dep.AdoptedAt != nil
		dep.mu.Unlock()
		if fqdn != "" && adopted {
			candidates = append(candidates, fqdn)
		}
		return true
	})
	if len(candidates) == 0 {
		// Fallback chain (issue #879 Bug 4):
		//   1. CATALYST_PRIMARY_DOMAIN — explicit override for tests /
		//      single-Sovereign sandboxes.
		//   2. SOVEREIGN_FQDN — the Sovereign's public FQDN, already
		//      wired into every catalyst-api Pod via the sovereign-fqdn
		//      ConfigMap (api-deployment.yaml). On a post-handover
		//      Sovereign no Deployment record is persisted (handover is
		//      JWT-only — no wizard-run on the Sovereign-side that
		//      writes one), so without this fallback GET
		//      /parent-domains returns {"items":[]} and the propagation
		//      panel shows expectedNs:null. Caught live on otech103,
		//      2026-05-05. Reading the ALREADY-wired SOVEREIGN_FQDN env
		//      makes the implicit primary visible without touching
		//      anything else.
		if v := strings.TrimSpace(os.Getenv("CATALYST_PRIMARY_DOMAIN")); v != "" {
			return v
		}
		if v := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")); v != "" {
			return v
		}
		return ""
	}
	sort.Strings(candidates)
	return candidates[0]
}

// AddParentDomain handles POST /api/v1/sovereign/parent-domains.
//
// Pipeline (sequential, so the UI can render a per-step progress bar):
//   1. Validate the request body (name shape, role enum, creds present).
//   2. Reject duplicates against the adopted Deployment's persistent
//      Request.ParentDomains[] (idempotency at the durable boundary,
//      not just in-memory — issue #837).
//   3. Run the per-domain step pipeline through
//      provisioner.ProvisionParentDomain (#826's reusable contract).
//      Today three steps are wired: registrar-flip → powerdns-zone-create
//      → cert-manager-cert. Day-1 wizard signup runs the same step list
//      from inside cloud-init; Day-2 add-domain runs it in-process.
//   4. Append the canonical provisioner.ParentDomain to the active
//      deployment's persistent record + persist the deployment so a
//      Pod restart re-reads the pool intact.
//
// All external calls are bounded by a per-call context with the
// request context as parent so a client cancel propagates.
func (h *Handler) AddParentDomain(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<14))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "request-too-large",
			"detail": "body must be under 16KB",
		})
		return
	}
	var req addParentDomainRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "body must be JSON {name, role, registrarKind, registrarToken}",
		})
		return
	}
	if err := validateDomainName(req.Name); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-name",
			"detail": err.Error(),
		})
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if role == "" {
		role = string(RoleOrgPool)
	}
	if role != string(RolePrimary) && role != string(RoleOrgPool) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-role",
			"detail": "role must be 'primary' or 'org-pool'",
		})
		return
	}
	if strings.TrimSpace(req.RegistrarKind) == "" {
		req.RegistrarKind = "dynadot"
	}
	if _, ok := supportedRegistrars[strings.ToLower(req.RegistrarKind)]; !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "unsupported-registrar",
			"detail": fmt.Sprintf("registrar %q not supported", req.RegistrarKind),
		})
		return
	}
	if strings.TrimSpace(req.RegistrarToken) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "missing-token",
			"detail": "registrarToken is required",
		})
		return
	}

	dep := h.activeDeployment()
	name := strings.ToLower(strings.TrimSpace(req.Name))

	// Idempotency: a second POST for the same name returns 409 instead
	// of double-flipping (which would cost real $$$ at the registrar
	// per-call billing tier of some adapters). Checked against the
	// PERSISTENT pool — the post-#837 store-of-truth.
	if _, exists := findParentDomain(dep, name); exists {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "already-exists",
			"detail": "parent domain already in pool",
		})
		return
	}
	// Refuse to add the operator's primary as an additional row; the
	// primary is implicit from Deployment.SovereignFQDN.
	if primary := h.lookupPrimaryDomain(); primary != "" && strings.EqualFold(primary, name) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "primary-already-set",
			"detail": "domain is the Sovereign's primary; already in pool",
		})
		return
	}

	now := time.Now().UTC()
	pd := provisioner.ParentDomain{
		Name:          name,
		Role:          provisioner.ParentDomainRoleOrgPool,
		RegistrarKind: strings.ToLower(req.RegistrarKind),
		AddedAt:       now,
	}
	if role == string(RolePrimary) {
		pd.Role = provisioner.ParentDomainRolePrimary
	}

	// Run the per-domain pipeline through provisioner.ProvisionParentDomain
	// (#826's reusable contract) so Day-1 wizard signup and Day-2 admin
	// add-domain converge on the same step list. The steps are
	// catalyst-api-side closures bound to the registrar token captured
	// in this handler's request — the token NEVER lands on the
	// persistent record, only the non-secret RegistrarKind +
	// RegistrarCredsRef fields do.
	steps := []provisioner.ParentDomainStep{
		&registrarFlipStep{h: h, registrarKind: req.RegistrarKind, registrarToken: req.RegistrarToken, ctx: r.Context()},
		&powerdnsZoneStep{h: h, ctx: r.Context()},
		&certManagerStep{h: h, ctx: r.Context()},
	}

	if err := provisioner.ProvisionParentDomain(r.Context(), pd, "", steps, nil); err != nil {
		// Per the file-level Persistence comment + the
		// FAILURE = WIPE + RESTART rule: failed adds do NOT persist.
		// The operator retries; nothing lingers in the pool.
		h.log.Info("parent-domain pipeline failed",
			"registrar", req.RegistrarKind,
			"domain", req.Name,
			"err", err.Error(),
		)
		// Surface the failed-step name + detail as the wire-shape
		// FlipStatusFailed row for the wizard's progress UI. The
		// failure is informational only — no row exists in the pool.
		failedAt := time.Now().UTC()
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "pipeline-failed",
			"detail": err.Error(),
			"item": ParentDomain{
				Name:          name,
				Role:          ParentDomainRole(role),
				RegistrarKind: strings.ToLower(req.RegistrarKind),
				FlipStatus:    FlipStatusFailed,
				FlipMessage:   err.Error(),
				AddedAt:       now,
				FlippedAt:     &failedAt,
			},
		})
		return
	}

	// Pipeline succeeded — append to the adopted Deployment's
	// Request.ParentDomains slice + persist so the row survives a
	// catalyst-api Pod restart. activeDeployment() returns nil on
	// Catalyst-Zero (no adopted deployment); in that case persistence
	// is a no-op and the row exists only in this response — that
	// matches the legacy in-memory behaviour for sandboxes/tests.
	if dep != nil {
		appendParentDomain(dep, pd)
		h.persistDeployment(dep)
	}

	flippedAt := time.Now().UTC()
	h.log.Info("parent-domain added",
		"registrar", req.RegistrarKind,
		"domain", req.Name,
		"role", role,
	)
	// Day-2 idempotent re-trigger note (issue #1772 — TBD-D30b).
	// Persisting the new entry to dep.Request.ParentDomains makes
	// the next provisioner.Provision call (Retry/Repair) re-write
	// tofu.auto.tfvars.json with the updated parent_domains_yaml,
	// which renders the additional Cilium Gateway listener pair on
	// next-prov. For an ALREADY-RUNNING Sovereign, the Hetzner
	// hcloud_server resource has no `ignore_changes = [user_data]`
	// so a `tofu apply` from changed cloud-init would request a
	// destructive server recreate.
	//
	// Closes #2118 (TBD-V48) changed the Day-2 patch target. The
	// listener YAML was historically inlined into cloud-init's
	// .spec.postBuild.substitute.PARENT_DOMAINS_LISTENERS_YAML on
	// the sovereign-tls Kustomization, so operators patched that
	// field on the live Sovereign. The chart now renders the
	// listener YAML into ConfigMap/sovereign-tls-vars in flux-system
	// and the Kustomization reads via postBuild.substituteFrom; the
	// live-Sovereign Day-2 patch target is therefore the ConfigMap's
	// data.PARENT_DOMAINS_LISTENERS_YAML key, NOT the Kustomization's
	// inline substitute map. Long-term: add a Day-2 ConfigMap-patch
	// step here that reaches into the Sovereign apiserver via the
	// persisted kubeconfig (out of scope for the #1772 ship).
	h.log.Info("parent-domain post-add: operator must patch live Sovereign ConfigMap to surface listener for the new zone",
		"domain", req.Name,
		"target", "ConfigMap/sovereign-tls-vars in flux-system on Sovereign",
		"field", ".data.PARENT_DOMAINS_LISTENERS_YAML",
		"reason", "hcloud_server user_data is not ignored — tofu apply would recreate the server. Fresh provs already render the listener via the chart.",
	)
	writeJSON(w, http.StatusCreated, ParentDomain{
		Name:          name,
		Role:          ParentDomainRole(role),
		RegistrarKind: strings.ToLower(req.RegistrarKind),
		FlipStatus:    FlipStatusReady,
		AddedAt:       now,
		FlippedAt:     &flippedAt,
	})
}

// DeleteParentDomain handles DELETE /api/v1/sovereign/parent-domains/{name}.
//
// The handler removes the row from the pool but does NOT un-flip the
// registrar NS records — that's a destructive operation an operator
// should perform deliberately at their registrar UI. The intent here is
// "stop offering this domain to SMEs"; the gTLD NS delegation can stay.
//
// Persistence: removes from the adopted Deployment's
// Request.ParentDomains slice + persists so the deletion survives a
// Pod restart (issue #837).
func (h *Handler) DeleteParentDomain(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "name")))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-name"})
		return
	}
	if primary := h.lookupPrimaryDomain(); primary != "" && strings.EqualFold(primary, name) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "primary-locked",
			"detail": "cannot remove the Sovereign's primary domain",
		})
		return
	}

	dep := h.activeDeployment()
	if !removeParentDomainByName(dep, name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not-found"})
		return
	}
	if dep != nil {
		h.persistDeployment(dep)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── ParentDomainStep adapters (Day-2 add-domain pipeline) ────────────────
//
// These bind the catalyst-api's existing PDM proxy helpers
// (h.pdmFlipNS, h.pdmCreatePowerDNSZone, h.createWildcardCert) to the
// reusable provisioner.ParentDomainStep interface from #826. Day-1
// wizard signup runs the same step list against the primary domain
// inside cloud-init (templated OpenTofu); Day-2 admin add-domain runs
// it in-process here.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10: registrarToken lives only on
// the registrarFlipStep value (a request-scoped closure that doesn't
// outlive the HTTP handler). It never enters the deployment record,
// the SSE buffer, or any logged struct.

type registrarFlipStep struct {
	h              *Handler
	ctx            context.Context
	registrarKind  string
	registrarToken string
}

func (s *registrarFlipStep) Name() string { return "registrar-flip" }

func (s *registrarFlipStep) Apply(_ context.Context, pd provisioner.ParentDomain, _ string) error {
	return s.h.pdmFlipNS(s.ctx, s.registrarKind, pd.Name, s.registrarToken)
}

type powerdnsZoneStep struct {
	h   *Handler
	ctx context.Context
}

func (s *powerdnsZoneStep) Name() string { return "powerdns-zone-create" }

func (s *powerdnsZoneStep) Apply(_ context.Context, pd provisioner.ParentDomain, _ string) error {
	return s.h.pdmCreatePowerDNSZone(s.ctx, pd.Name)
}

type certManagerStep struct {
	h   *Handler
	ctx context.Context
}

func (s *certManagerStep) Name() string { return "cert-manager-cert" }

func (s *certManagerStep) Apply(_ context.Context, pd provisioner.ParentDomain, _ string) error {
	return s.h.createWildcardCert(s.ctx, pd.Name)
}

// ── DNS propagation panel ───────────────────────────────────────────────

// defaultResolvers — public DNS resolvers we query the parent zone's NS
// records against. The five chosen here cover the major recursive-resolver
// providers + a geographic spread:
//
//   - 8.8.8.8       Google US
//   - 1.1.1.1       Cloudflare global anycast
//   - 9.9.9.9       Quad9 IBM/PCH (Switzerland-anchored)
//   - 208.67.222.222 OpenDNS / Cisco US
//   - 4.2.2.1       Level 3 / CenturyLink US (Asia presence)
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 this list is overridable via the
// `CATALYST_DNS_PROPAGATION_RESOLVERS` env var (comma-separated IPs).
// An air-gapped operator can swap to internal resolvers if their security
// posture forbids egress to public DNS.
var defaultResolvers = []resolverSpec{
	{Name: "Google", IP: "8.8.8.8", Geo: "US"},
	{Name: "Cloudflare", IP: "1.1.1.1", Geo: "Global"},
	{Name: "Quad9", IP: "9.9.9.9", Geo: "EU"},
	{Name: "OpenDNS", IP: "208.67.222.222", Geo: "US"},
	{Name: "Level3", IP: "4.2.2.1", Geo: "US"},
}

type resolverSpec struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Geo  string `json:"geo"`
}

// PropagationState — per-resolver result that the UI renders as a
// green/yellow/red pill plus a tooltip.
type PropagationState struct {
	Resolver  resolverSpec `json:"resolver"`
	Status    string       `json:"status"` // converged | diverged | error
	NS        []string     `json:"ns"`     // NS records returned (sorted)
	QueriedAt time.Time    `json:"queriedAt"`
	LatencyMs int64        `json:"latencyMs"`
	Err       string       `json:"error,omitempty"`
}

// PropagationResponse — full payload returned by GET /propagation.
type PropagationResponse struct {
	Domain      string             `json:"domain"`
	ExpectedNS  []string           `json:"expectedNs"`
	Resolvers   []PropagationState `json:"resolvers"`
	Converged   int                `json:"converged"`
	Total       int                `json:"total"`
	Percentage  int                `json:"percentage"`
	GeneratedAt time.Time          `json:"generatedAt"`
}

// resolversFromEnv parses CATALYST_DNS_PROPAGATION_RESOLVERS into a
// resolverSpec slice. Empty / unset → defaultResolvers.
func resolversFromEnv() []resolverSpec {
	raw := strings.TrimSpace(os.Getenv("CATALYST_DNS_PROPAGATION_RESOLVERS"))
	if raw == "" {
		return defaultResolvers
	}
	out := []resolverSpec{}
	for _, part := range strings.Split(raw, ",") {
		ip := strings.TrimSpace(part)
		if ip == "" {
			continue
		}
		out = append(out, resolverSpec{Name: ip, IP: ip, Geo: "Custom"})
	}
	if len(out) == 0 {
		return defaultResolvers
	}
	return out
}

// resolveQueryTimeout — bound on a single LookupNS call. Generous enough
// to absorb a slow public resolver, tight enough that a stuck resolver
// doesn't pin the whole panel.
const resolveQueryTimeout = 5 * time.Second

// lookupNSAt issues an authoritative-zone NS lookup against a SPECIFIC
// resolver IP using a custom net.Resolver Dial that ignores the system
// /etc/resolv.conf. Returns the sorted NS list or an error.
//
// Inviolable principle #4 satisfied: no resolver IP hardcoded — caller
// passes the IP from defaultResolvers / env override.
func lookupNSAt(ctx context.Context, resolverIP, domain string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: resolveQueryTimeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(resolverIP, "53"))
		},
	}
	queryCtx, cancel := context.WithTimeout(ctx, resolveQueryTimeout)
	defer cancel()
	nsRecs, err := r.LookupNS(queryCtx, domain)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(nsRecs))
	for _, ns := range nsRecs {
		out = append(out, strings.ToLower(strings.TrimSuffix(ns.Host, ".")))
	}
	sort.Strings(out)
	return out, nil
}

// expectedNSFor returns the canonical Sovereign PowerDNS NS records for
// a given parent domain. The Sovereign's PowerDNS exposes itself as
// `ns1.<sovereign-fqdn>` + `ns2.<sovereign-fqdn>`; an operator with a
// non-default deployment can override via `CATALYST_EXPECTED_NS`
// (comma-separated host list) per inviolable principle #4.
func (h *Handler) expectedNSFor(_ string) []string {
	if raw := strings.TrimSpace(os.Getenv("CATALYST_EXPECTED_NS")); raw != "" {
		out := []string{}
		for _, part := range strings.Split(raw, ",") {
			s := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(part), "."))
			if s != "" {
				out = append(out, s)
			}
		}
		sort.Strings(out)
		return out
	}
	primary := h.lookupPrimaryDomain()
	if primary == "" {
		return nil
	}
	out := []string{
		"ns1." + primary,
		"ns2." + primary,
	}
	sort.Strings(out)
	return out
}

// nsSetsMatch returns true when `got` contains at least one entry from
// `expected`. The match is intentionally weak — most public resolvers
// will return the SOA/NS list verbatim from the parent zone, but some
// glue records may add CNAME-flattened intermediaries; a single
// expected NS landing in the result is sufficient evidence of flip
// convergence.
func nsSetsMatch(got, expected []string) bool {
	if len(expected) == 0 {
		return false
	}
	seen := map[string]struct{}{}
	for _, g := range got {
		seen[strings.ToLower(strings.TrimSuffix(g, "."))] = struct{}{}
	}
	for _, e := range expected {
		if _, ok := seen[strings.ToLower(strings.TrimSuffix(e, "."))]; ok {
			return true
		}
	}
	return false
}

// GetPropagation handles GET /api/v1/sovereign/parent-domains/{name}/propagation.
//
// Fans out one goroutine per configured resolver, aggregates results,
// returns the PropagationResponse. Total wall-clock time is bounded by
// `resolveQueryTimeout` (the slowest resolver), not by the sum.
func (h *Handler) GetPropagation(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "name")))
	if err := validateDomainName(name); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-name",
			"detail": err.Error(),
		})
		return
	}

	resolvers := resolversFromEnv()
	expected := h.expectedNSFor(name)

	results := make([]PropagationState, len(resolvers))
	var wg sync.WaitGroup
	for i, spec := range resolvers {
		wg.Add(1)
		go func(idx int, s resolverSpec) {
			defer wg.Done()
			start := time.Now()
			ns, err := lookupNSAt(r.Context(), s.IP, name)
			latency := time.Since(start).Milliseconds()
			ps := PropagationState{
				Resolver:  s,
				NS:        ns,
				QueriedAt: time.Now().UTC(),
				LatencyMs: latency,
			}
			switch {
			case err != nil:
				ps.Status = "error"
				ps.Err = err.Error()
			case nsSetsMatch(ns, expected):
				ps.Status = "converged"
			default:
				ps.Status = "diverged"
			}
			results[idx] = ps
		}(i, spec)
	}
	wg.Wait()

	converged := 0
	for _, r := range results {
		if r.Status == "converged" {
			converged++
		}
	}
	total := len(results)
	pct := 0
	if total > 0 {
		pct = converged * 100 / total
	}

	writeJSON(w, http.StatusOK, PropagationResponse{
		Domain:      name,
		ExpectedNS:  expected,
		Resolvers:   results,
		Converged:   converged,
		Total:       total,
		Percentage:  pct,
		GeneratedAt: time.Now().UTC(),
	})
}

// ── PDM proxy helpers (registrar NS-flip + zone create) ─────────────────

// pdmFlipNS forwards the registrar credentials to PDM's /set-ns endpoint.
// Same wire shape as registrar.go's SetNSRegistrar; we re-implement the
// call here (rather than re-using SetNSRegistrar's HTTP handler) so the
// AddParentDomain pipeline can examine the response and update the store
// atomically. Token never enters a logged struct.
//
// Issue #879 (otech103, 2026-05-05) wired three previously-missing pieces
// to make this work end-to-end on a Sovereign:
//
//   - Bug 2 — Basic auth: PDM is exposed via the public ingress
//     `pool.openova.io` (clusters/contabo-mkt/apps/pool-domain-manager/
//     ingress.yaml) which is gated by Traefik basicAuth. Calls without
//     `Authorization: Basic …` get 401. Credentials are loaded from
//     the Pod env (CATALYST_PDM_BASIC_AUTH_USER / _PASS, sourced from
//     the `pdm-basicauth` Secret per api-deployment.yaml). When unset
//     (Catalyst-Zero in-cluster path / older Sovereigns) we omit the
//     header — the in-cluster Service URL is unauthenticated and PDM
//     responds normally. Per Inviolable Principle #10 the credentials
//     are only read at call time and never enter a logged struct.
//
//   - Bug 3 — `nameservers` field: PDM's SetNSRequest schema
//     (core/pool-domain-manager/internal/handler/registrar.go:149)
//     requires a non-empty `nameservers` array. Without it, PDM
//     returns 422 `missing-nameservers`. We populate it from
//     `expectedNSFor(domain)` which already computes
//     `[ns1.<primary>, ns2.<primary>]` from the Sovereign's primary
//     FQDN — same nameserver pair PDM's gTLD-side write needs to
//     point the new domain at the Sovereign's PowerDNS.
func (h *Handler) pdmFlipNS(ctx context.Context, registrarKind, domain, token string) error {
	pdmBase := pdmBaseURL()
	if pdmBase == "" {
		return fmt.Errorf("pdm-unavailable")
	}
	// PDM's SetNSRequest schema requires `nameservers` (a non-empty array).
	// Compute from the Sovereign's primary FQDN — same `[ns1.<primary>,
	// ns2.<primary>]` pair PDM's gTLD-side write uses for the existing
	// primary domain.
	nameservers := h.expectedNSFor(domain)
	if len(nameservers) == 0 {
		return fmt.Errorf("expected-ns-unavailable: cannot compute nameservers (primary domain unknown)")
	}
	// D30 short-circuit (caught on t134 2026-05-17): when the domain's
	// current NS records already match the expected nameservers, the
	// PDM registrar-flip step is a no-op — there's nothing to flip.
	// Skipping avoids burning the registrar API credit + sidesteps the
	// Dynadot pdm-status-401 blocker for org-pool domains already
	// delegated to OpenOva (e.g. omani.homes which already points at
	// ns1/2/3.openova.io). When the lookup fails or returns a different
	// NS set, fall through to the original pipeline so PDM does the
	// flip via the registrar API.
	if h.nsAlreadyMatches(ctx, domain, nameservers) {
		h.log.Info("pdm-flip-ns: NS already matches expected, skipping registrar-flip",
			"domain", domain, "registrarKind", registrarKind)
		return nil
	}
	// glueIP — Sovereign's load-balancer public IPv4 (issue #900). When
	// non-empty PDM pre-registers each NS hostname against the customer's
	// account before the set_ns flip, unblocking Dynadot's "needs to be
	// registered with an ip address" rejection. Source order:
	//
	//   1. SOVEREIGN_LB_IP env var (api-deployment.yaml mounts this from
	//      the sovereign-fqdn ConfigMap key `lbIP`, which the chart
	//      renders from `global.sovereignLBIP`).
	//   2. The adopted Deployment's Result.LoadBalancerIP (when running
	//      on Catalyst-Zero / contabo where SOVEREIGN_LB_IP is empty
	//      but a deployment record exists with the result captured).
	//   3. Empty — non-Dynadot adapters ignore the field; Dynadot
	//      adapter falls through and set_ns runs as-is. This preserves
	//      backward compat with pre-#900 deployments while making the
	//      glue path the default on Sovereigns.
	glueIP := h.lookupSovereignLBIP()
	payload := map[string]any{
		"domain":      domain,
		"token":       token,
		"nameservers": nameservers,
	}
	if glueIP != "" {
		payload["glueIP"] = glueIP
	}
	body, _ := json.Marshal(payload)
	target := fmt.Sprintf("%s/api/v1/registrar/%s/set-ns",
		strings.TrimRight(pdmBase, "/"),
		strings.ToLower(registrarKind),
	)
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build-request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Basic auth for the public PDM ingress. Skipped when the Pod env
	// is unset — covers the in-cluster-Service path on Catalyst-Zero
	// and CI / local dev, where PDM is unauthenticated.
	if user, pass := pdmBasicAuth(); user != "" {
		httpReq.SetBasicAuth(user, pass)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("pdm-unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pdm-status-%d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// lookupSovereignLBIP resolves the Sovereign's load-balancer public
// IPv4 for the multi-domain Day-2 add-domain flow's glue-record
// pre-registration step (issue #900). Source order:
//
//  1. SOVEREIGN_LB_IP env var — wired on Sovereign Pods from the
//     `sovereign-fqdn` ConfigMap key `lbIP` (chart renders from
//     `global.sovereignLBIP`). This is the post-handover canonical
//     source.
//  2. Any adopted deployment's Result.LoadBalancerIP — covers the
//     Catalyst-Zero (contabo) path where SOVEREIGN_LB_IP is unset
//     but a finalised deployment record exists with the LB captured.
//  3. "" — no Sovereign LB IP available. Day-2 add-domain still
//     proceeds (legacy plain set_ns); Dynadot will reject only if
//     the host hasn't been registered manually first.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the env var name is the only
// runtime-configurable knob; nothing about the IP itself is hardcoded.
func (h *Handler) lookupSovereignLBIP() string {
	if v := strings.TrimSpace(os.Getenv("SOVEREIGN_LB_IP")); v != "" {
		return v
	}
	var picked string
	h.deployments.Range(func(_, v any) bool {
		dep, ok := v.(*Deployment)
		if !ok {
			return true
		}
		dep.mu.Lock()
		adopted := dep.AdoptedAt != nil
		var lb string
		if dep.Result != nil {
			lb = strings.TrimSpace(dep.Result.LoadBalancerIP)
		}
		dep.mu.Unlock()
		if adopted && lb != "" {
			picked = lb
			return false
		}
		return true
	})
	return picked
}

// nsAlreadyMatches returns true when domain's current NS records (per
// the public DNS) already include EVERY name in expected. Case-
// insensitive, trailing-dot tolerant. Used by pdmFlipNS to short-circuit
// the registrar-flip step for org-pool domains already delegated to
// OpenOva's PowerDNS (D30 — t134 2026-05-17). Returns false on lookup
// error or partial match so the original PDM pipeline still runs.
func (h *Handler) nsAlreadyMatches(ctx context.Context, domain string, expected []string) bool {
	if len(expected) == 0 {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resolver := net.DefaultResolver
	got, err := resolver.LookupNS(lookupCtx, domain)
	if err != nil {
		return false
	}
	have := make(map[string]struct{}, len(got))
	for _, ns := range got {
		have[strings.ToLower(strings.TrimSuffix(ns.Host, "."))] = struct{}{}
	}
	for _, want := range expected {
		w := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(want), "."))
		if w == "" {
			continue
		}
		if _, ok := have[w]; !ok {
			return false
		}
	}
	return true
}

// pdmBasicAuth reads the basic-auth credentials for the PDM public
// ingress out of the Pod env. Returns ("", "") when unset — callers
// then skip SetBasicAuth and rely on the in-cluster Service path
// (unauthenticated). Read every call so a Secret rotation propagates
// without a Pod restart (Reloader handles the env reload).
//
// Per Inviolable Principle #10, this is the ONE call site that touches
// the credentials — they never enter a struct that gets logged.
func pdmBasicAuth() (string, string) {
	user := strings.TrimSpace(os.Getenv("CATALYST_PDM_BASIC_AUTH_USER"))
	pass := os.Getenv("CATALYST_PDM_BASIC_AUTH_PASS")
	return user, pass
}

// pdmCreatePowerDNSZone — runtime PowerDNS zone-create for the
// admin-console "Add another parent domain" flow.
//
// As of issue #827 this function uses the typed
// internal/powerdns.Client (CreateZone) when the catalyst-api Pod is
// configured with the in-cluster PowerDNS API endpoint via
// SetPowerDNSZoneClient (wired in main.go from
// CATALYST_POWERDNS_API_URL + CATALYST_POWERDNS_API_KEY). The call is
// idempotent on HTTP 409 — re-runs after a failed pipeline step never
// break the orchestrator.
//
// Backward-compat fallback when the typed client is NOT wired: we keep
// the legacy CATALYST_PDM_ZONES_ENABLED env-trigger path that POSTs to
// PDM's /api/v1/zones. Catalyst-Zero (contabo) leaves both unset and
// the function returns nil + an audit log line so the UI surfaces
// "zone-creating" → "ready" without a real zone create — same shape as
// before #827.
func (h *Handler) pdmCreatePowerDNSZone(ctx context.Context, domain string) error {
	// Preferred path (issue #827): typed PowerDNS client wired to the
	// Sovereign's own PowerDNS REST API. Idempotent on 409.
	if h.powerdnsZoneClient != nil {
		err := h.powerdnsZoneClient.CreateZone(ctx, powerdns.ZoneSpec{
			Name:       domain,
			Kind:       "Native",
			DNSSEC:     true,
			APIRectify: true,
		})
		switch {
		case err == nil:
			h.log.Info("parent-domain zone-create: PowerDNS 201 Created",
				"domain", domain,
			)
			return nil
		case errors.Is(err, powerdns.ErrZoneAlreadyExists):
			h.log.Info("parent-domain zone-create: PowerDNS 409 (idempotent)",
				"domain", domain,
			)
			return nil
		default:
			return fmt.Errorf("powerdns-create-zone: %w", err)
		}
	}

	// Legacy fallback path (pre-#827). Honours the
	// CATALYST_PDM_ZONES_ENABLED env-trigger contract documented in
	// #829's original implementation: when unset, the function returns
	// nil + a stub-log so the UI flow doesn't block in environments
	// (CI, local dev, contabo) that have no PowerDNS to reach.
	if os.Getenv("CATALYST_PDM_ZONES_ENABLED") != "true" {
		h.log.Info("parent-domain zone-create: stub (no powerdns client wired and CATALYST_PDM_ZONES_ENABLED not set)",
			"domain", domain,
		)
		return nil
	}
	pdmBase := pdmBaseURL()
	if pdmBase == "" {
		return fmt.Errorf("pdm-unavailable")
	}
	body, _ := json.Marshal(map[string]string{
		"name": domain,
		"kind": "Native",
	})
	target := fmt.Sprintf("%s/api/v1/zones",
		strings.TrimRight(pdmBase, "/"),
	)
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build-request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Same basic-auth treatment as pdmFlipNS — the public PDM ingress
	// requires the Authorization header. Issue #879 Bug 2 follow-up.
	if user, pass := pdmBasicAuth(); user != "" {
		httpReq.SetBasicAuth(user, pass)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(httpReq)
	if err != nil {
		return fmt.Errorf("pdm-unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		// 409 == idempotent "already exists", treat as success.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("pdm-status-%d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// createWildcardCert — stub for #827. When #827 lands this writes a
// `cert-manager.io/v1` Certificate to the home cluster requesting
// `*.<domain>` + `<domain>` via the existing PowerDNS-webhook DNS-01
// solver. Until then we return nil so AddParentDomain proceeds.
func (h *Handler) createWildcardCert(_ context.Context, domain string) error {
	if os.Getenv("CATALYST_CERTIFICATE_AUTO_CREATE") != "true" {
		h.log.Info("parent-domain cert-issue: stub (CATALYST_CERTIFICATE_AUTO_CREATE not set)",
			"domain", domain,
		)
		return nil
	}
	// Real implementation deferred to #827 sister PR.
	return nil
}
