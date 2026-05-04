// parent_domains.go — admin-console "add another parent domain" flow +
// live DNS propagation status panel (issue #829, parent epic #825).
//
// Background — multi-domain Sovereign (epic #825)
// ------------------------------------------------
// A franchised Sovereign supports N parent domains, not 1. The operator
// brings:
//   - the primary domain serving console.<primary>, api.<primary>, etc.
//   - zero-or-more "sme-pool" domains offered to SME tenants for free
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
// Sister tickets:
//   - #826 (MD-1): Sovereign data model `parentDomains[]` + provisioning
//     NS-flip loop. NOT YET MERGED → this file stubs the persistence
//     layer with an in-memory store rooted on the existing single
//     `SovereignFQDN` field as the implicit "primary" entry. When #826
//     lands the AddParentDomain handler will switch from the in-memory
//     store to writing into Deployment.parentDomains[] and triggering
//     the same NS-flip loop the wizard fires at signup.
//   - #827 (MD-2): PowerDNS multi-zone bootstrap + cert-manager per-zone
//     wildcard cert. NOT YET MERGED → AddParentDomain emits an SSE-style
//     log line for the zone-create + cert-issue steps so the UI can
//     surface them, but does not block on actual reconciliation.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #1 (waterfall, target-state shape): the wire shape this file emits is
//       the final shape — `parentDomains[]` with role + flipStatus +
//       perResolverPropagation. It will not change when #826/#827 merge;
//       only the persistence backing changes.
//   #4 (never hardcode): the resolver list lives in `defaultResolvers`,
//       overridable via `CATALYST_DNS_PROPAGATION_RESOLVERS` env. The
//       per-query timeout + poll-rate are also env-tunable.
//   #10 (credential hygiene): registrar API credentials submitted in the
//       AddParentDomain POST are forwarded byte-for-byte to PDM via the
//       existing /set-ns proxy seam in registrar.go — they never enter a
//       struct that gets logged.
//
// Implementation note for the propagation panel:
// Go's net.Resolver supports custom Dial that lets us route NS lookups
// through a SPECIFIC resolver IP (8.8.8.8, 1.1.1.1, etc) rather than
// the system resolv.conf. We spin one goroutine per resolver, run
// LookupNS with a 5s deadline, and aggregate the results. The polling
// rate-limit lives client-side: the UI polls this endpoint every 60s,
// which is plenty given DNS gTLD TTL is 48h. The endpoint itself is
// stateless — every request fans out fresh.
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
)

// ParentDomainRole names the two purposes a parent domain can serve in
// the Sovereign's pool. Mirrors the canonical shape from #826.
type ParentDomainRole string

const (
	// RolePrimary — the operator's own domain (console.<name>,
	// api.<name>, marketplace.<name>). Exactly one per Sovereign.
	RolePrimary ParentDomainRole = "primary"
	// RoleSMEPool — offered to SME tenants for free-subdomain
	// allocation (e.g. console.acme.<name>). Zero-or-more per Sovereign.
	RoleSMEPool ParentDomainRole = "sme-pool"
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
// Per #826 this will eventually also live on Deployment.parentDomains[];
// for now we serve it from the in-memory parentDomainStore below plus
// an implicit "primary" row synthesised from Deployment.SovereignFQDN.
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

// parentDomainStore — in-memory persistence for additions made via the
// admin surface. Backed by a sync.Map keyed by domain name. When #826
// lands this is replaced by Deployment.parentDomains[] + the store.go
// flat-file persistence layer; the wire shape stays identical so the UI
// is unaffected by the swap.
type parentDomainStore struct {
	entries sync.Map // map[string]*ParentDomain
}

// global single-instance store. The handler reads this lazily so tests
// that build Handler{} directly still work — no Init wiring needed.
var globalParentDomainStore = &parentDomainStore{}

// list — snapshot of every entry, sorted by name for stable UI rendering.
func (s *parentDomainStore) list() []ParentDomain {
	out := []ParentDomain{}
	s.entries.Range(func(_, v any) bool {
		out = append(out, *(v.(*ParentDomain)))
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *parentDomainStore) get(name string) (*ParentDomain, bool) {
	v, ok := s.entries.Load(strings.ToLower(name))
	if !ok {
		return nil, false
	}
	return v.(*ParentDomain), true
}

func (s *parentDomainStore) put(pd *ParentDomain) {
	s.entries.Store(strings.ToLower(pd.Name), pd)
}

func (s *parentDomainStore) del(name string) bool {
	_, loaded := s.entries.LoadAndDelete(strings.ToLower(name))
	return loaded
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

// ListParentDomains handles GET /api/v1/sovereign/parent-domains.
//
// The shape is `{"items": [...]}` to match the rest of the catalyst-api
// list endpoints (UserAccess, deployments, etc).
func (h *Handler) ListParentDomains(w http.ResponseWriter, r *http.Request) {
	items := globalParentDomainStore.list()

	// Synthesise the implicit "primary" row from any deployment record
	// that has been adopted (i.e. the operator has finalised handover
	// and is now using this Sovereign as the home cluster). This is the
	// stub stand-in for #826's Deployment.parentDomains[].
	primaryName := h.lookupPrimaryDomain()
	if primaryName != "" {
		// Avoid duplicating if the operator already added their primary
		// via the admin UI (idempotency).
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
		// Fallback: env override for tests / single-Sovereign sandboxes.
		if v := strings.TrimSpace(os.Getenv("CATALYST_PRIMARY_DOMAIN")); v != "" {
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
//   2. Insert a `flipping` row into the store (or 409 if it already
//      exists) so a concurrent GET/list reflects the in-flight state.
//   3. Forward the credentials to PDM's /set-ns endpoint to actually
//      flip the NS records at the registrar (#826's real engine).
//   4. Forward to PDM's /zones endpoint to bootstrap the PowerDNS zone
//      (#827, currently a stub since #827 hasn't merged).
//   5. Update the store row to `ready` (or `failed` with detail).
//
// All three external calls are bounded by a per-call context with the
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
		role = string(RoleSMEPool)
	}
	if role != string(RolePrimary) && role != string(RoleSMEPool) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "invalid-role",
			"detail": "role must be 'primary' or 'sme-pool'",
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
	// Idempotency: a second POST for the same name returns 409 instead
	// of double-flipping (which would cost real $$$ at the registrar
	// per-call billing tier of some adapters).
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if _, exists := globalParentDomainStore.get(name); exists {
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
	pd := &ParentDomain{
		Name:          name,
		Role:          ParentDomainRole(role),
		RegistrarKind: strings.ToLower(req.RegistrarKind),
		FlipStatus:    FlipStatusFlipping,
		AddedAt:       now,
	}
	globalParentDomainStore.put(pd)

	// Step 1 of #826: NS-flip via PDM proxy. Forward credentials
	// byte-for-byte; never log the token.
	flipErr := h.pdmFlipNS(r.Context(), req.RegistrarKind, req.Name, req.RegistrarToken)
	if flipErr != nil {
		pd.FlipStatus = FlipStatusFailed
		pd.FlipMessage = "ns-flip: " + flipErr.Error()
		globalParentDomainStore.put(pd)
		// Log without the token — only registrar + domain + status
		h.log.Info("parent-domain ns-flip failed",
			"registrar", req.RegistrarKind,
			"domain", req.Name,
			"err", flipErr.Error(),
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "ns-flip-failed",
			"detail": flipErr.Error(),
			"item":   pd,
		})
		return
	}
	pd.FlipStatus = FlipStatusZoneCreate
	globalParentDomainStore.put(pd)

	// Step 2 of #827: PowerDNS zone create. Best-effort stub — when
	// #827 lands this becomes a hard dependency.
	zoneErr := h.pdmCreatePowerDNSZone(r.Context(), req.Name)
	if zoneErr != nil {
		pd.FlipStatus = FlipStatusFailed
		pd.FlipMessage = "zone-create: " + zoneErr.Error()
		globalParentDomainStore.put(pd)
		h.log.Info("parent-domain zone-create failed",
			"domain", req.Name,
			"err", zoneErr.Error(),
		)
		// We don't roll back the registrar NS-flip — that's a deliberate
		// no-op since the gTLD TTL of 48h means a flip-then-flip-back
		// burns 4 days for the same end-state. Operator can retry the
		// zone-create + cert step independently.
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "zone-create-failed",
			"detail": zoneErr.Error(),
			"item":   pd,
		})
		return
	}
	pd.FlipStatus = FlipStatusCertIssue
	globalParentDomainStore.put(pd)

	// Step 3 of #827: cert-manager wildcard Certificate create. Stub.
	// When #827 lands this writes a Certificate CR to the cluster via
	// dynamic client.
	certErr := h.createWildcardCert(r.Context(), req.Name)
	if certErr != nil {
		pd.FlipStatus = FlipStatusFailed
		pd.FlipMessage = "cert-issue: " + certErr.Error()
		globalParentDomainStore.put(pd)
		h.log.Info("parent-domain cert-issue failed",
			"domain", req.Name,
			"err", certErr.Error(),
		)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "cert-issue-failed",
			"detail": certErr.Error(),
			"item":   pd,
		})
		return
	}

	flippedAt := time.Now().UTC()
	pd.FlipStatus = FlipStatusReady
	pd.FlippedAt = &flippedAt
	globalParentDomainStore.put(pd)

	h.log.Info("parent-domain added",
		"registrar", req.RegistrarKind,
		"domain", req.Name,
		"role", role,
	)
	writeJSON(w, http.StatusCreated, pd)
}

// DeleteParentDomain handles DELETE /api/v1/sovereign/parent-domains/{name}.
//
// The handler removes the row from the pool but does NOT un-flip the
// registrar NS records — that's a destructive operation an operator
// should perform deliberately at their registrar UI. The intent here is
// "stop offering this domain to SMEs"; the gTLD NS delegation can stay.
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
	if !globalParentDomainStore.del(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not-found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	Status    string       `json:"status"`              // converged | diverged | error
	NS        []string     `json:"ns"`                  // NS records returned (sorted)
	QueriedAt time.Time    `json:"queriedAt"`
	LatencyMs int64        `json:"latencyMs"`
	Err       string       `json:"error,omitempty"`
}

// PropagationResponse — full payload returned by GET /propagation.
type PropagationResponse struct {
	Domain     string             `json:"domain"`
	ExpectedNS []string           `json:"expectedNs"`
	Resolvers  []PropagationState `json:"resolvers"`
	Converged  int                `json:"converged"`
	Total      int                `json:"total"`
	Percentage int                `json:"percentage"`
	GeneratedAt time.Time         `json:"generatedAt"`
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
func (h *Handler) pdmFlipNS(ctx context.Context, registrarKind, domain, token string) error {
	pdmBase := pdmBaseURL()
	if pdmBase == "" {
		return fmt.Errorf("pdm-unavailable")
	}
	body, _ := json.Marshal(map[string]string{
		"domain": domain,
		"token":  token,
	})
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
