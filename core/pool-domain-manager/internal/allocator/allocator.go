// Package allocator wires the persistence layer (store) to the DNS writer
// (PowerDNS) and exposes the four lifecycle operations PDM's HTTP handlers
// need: Check, Reserve, Commit, Release.
//
// The state machine the allocator implements is:
//
//	NULL ─reserve→ RESERVED ─commit→ ACTIVE
//	                  │              │
//	                  expire/        release/
//	                  release        destroy
//	                  ↓              ↓
//	                NULL          NULL
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 every transition is committed to the
// CNPG row before the corresponding side-effect (PowerDNS write/delete) is
// invoked, so a crash between the two leaves the system in a recoverable
// state: at worst we have a row claiming state='active' with stale or
// missing DNS records, which an operator can reconcile by calling Release
// then re-running the wizard.
//
// Per docs/PLATFORM-POWERDNS.md the DNS contract is:
//
//   - Every Sovereign — pool or BYO — gets its own PowerDNS zone (the
//     "child zone"). For pool tenancy the pool zone (e.g. `omani.works`)
//     is the parent and PDM writes an NS-delegation RRset into it pointing
//     at the OpenOva NS endpoints. For BYO the operator's registrar handles
//     delegation and PDM only owns the child zone — no parent touch.
//
//   - Each child zone publishes the canonical 6-record set: apex A,
//     wildcard A, plus console/api/gitea/harbor A — all pointing at the
//     regional Load Balancer IP. Wildcard TLS is therefore scoped per
//     Sovereign (`*.<sub>.<pool>`), satisfying the per-Sovereign isolation
//     requirement in the issue body for #168.
//
//   - DNSSEC is mandatory. Every child zone is signed with a fresh KSK+ZSK
//     pair (algorithm 13, ECDSAP256SHA256). Per-Sovereign keys allow
//     independent rotation and clean termination on Release.
package allocator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/pdns"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/reserved"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

// DNSWriter is the abstraction the allocator depends on for authoritative
// DNS writes. The production implementation is *pdns.Client; tests inject
// an in-memory fake. This boundary keeps the allocator free of HTTP plumbing
// and lets the unit tests run without an httptest server.
type DNSWriter interface {
	// CreateZone provisions an authoritative zone (idempotent on conflict).
	CreateZone(ctx context.Context, name string, kind pdns.ZoneKind, nameservers []string) error
	// DeleteZone drops a zone, its records, and DNSSEC keys (idempotent on 404).
	DeleteZone(ctx context.Context, name string) error
	// EnsureZone creates the zone if missing — used by parent-zone bootstrap.
	EnsureZone(ctx context.Context, name string, kind pdns.ZoneKind, nameservers []string) error
	// AddARecord upserts a single A record inside a zone.
	AddARecord(ctx context.Context, zone, name, ipv4 string, ttl int) error
	// PatchRRSets is the lower-level batch primitive the allocator uses for
	// the canonical 6-record set in a single round-trip.
	PatchRRSets(ctx context.Context, zone string, rrsets []pdns.RRSet) error
	// AddNSDelegation upserts the NS delegation RRset inside the parent zone.
	AddNSDelegation(ctx context.Context, parentZone, childName string, nameservers []string, ttl int) error
	// RemoveNSDelegation drops the delegation RRset (idempotent).
	RemoveNSDelegation(ctx context.Context, parentZone, childName string) error
	// RemoveWildcardRecords deletes any "*.<zone>" A/AAAA RRset (idempotent) —
	// used by the parent-zone bootstrap to reap a stale/foreign parent wildcard
	// that would shadow every delegated child (#5007).
	RemoveWildcardRecords(ctx context.Context, zone string) error
	// EnableDNSSEC turns on DNSSEC for the child zone and generates KSK+ZSK.
	EnableDNSSEC(ctx context.Context, zone string) error
}

// Compile-time assertion that *pdns.Client satisfies the DNSWriter contract.
var _ DNSWriter = (*pdns.Client)(nil)

// Allocator owns the state-machine logic. It is concurrency-safe — every
// operation maps to a single Postgres transaction in store; there is no
// in-memory mutable state on the Allocator itself.
type Allocator struct {
	store *store.Store
	dns   DNSWriter
	log   *slog.Logger

	// nameservers — the canonical OpenOva NS endpoints PDM uses both for
	// the apex NS RRset of every child zone AND for the parent's NS
	// delegation RRset. Per docs/PLATFORM-POWERDNS.md these are
	// ns1/ns2/ns3.openova.io anycast Floating IPs (Phase-0 stand-in is a
	// Service of type=LoadBalancer). The list is configuration-driven so
	// adding a fourth NS endpoint is a Secret edit, not a rebuild.
	nameservers []string

	// reservationTTL — how long a /reserve holds the name before the
	// sweeper reclaims it. Per the issue body this is 10 minutes.
	reservationTTL time.Duration

	// reapStaleParentWildcards — when true (default), BootstrapParentZones
	// reaps any foreign `*.<pool>` A/AAAA wildcard from each managed parent
	// pool zone so a wiped env can never leave a wildcard shadowing sibling
	// child names (#5007). pdm never writes a parent-level wildcard, so this
	// is always safe within pdm's delegation model; the flag is an operator
	// escape hatch for a deployment that intentionally serves a flat parent
	// wildcard managed by another system.
	reapStaleParentWildcards bool
}

// Config bundles the runtime allocator configuration.
type Config struct {
	// Nameservers — FQDN form (e.g. "ns1.openova.io"). Required.
	Nameservers []string
	// ReservationTTL — see Allocator.reservationTTL.
	ReservationTTL time.Duration
	// ReapStaleParentWildcards — see Allocator.reapStaleParentWildcards.
	// Default true (wired from PDM_REAP_STALE_PARENT_WILDCARDS in main).
	ReapStaleParentWildcards bool
}

// New constructs an Allocator. cfg.Nameservers must contain at least one
// NS host; production passes the three openova.io NS FQDNs.
func New(s *store.Store, d DNSWriter, log *slog.Logger, cfg Config) *Allocator {
	return &Allocator{
		store:                    s,
		dns:                      d,
		log:                      log,
		nameservers:              cfg.Nameservers,
		reservationTTL:           cfg.ReservationTTL,
		reapStaleParentWildcards: cfg.ReapStaleParentWildcards,
	}
}

// CheckResult is the wire shape for /api/v1/pool/{domain}/check?sub=X.
type CheckResult struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	FQDN      string `json:"fqdn,omitempty"`
}

// Check returns whether the (poolDomain, subdomain) pair is free, with a
// machine-readable reason when it is not.
func (a *Allocator) Check(ctx context.Context, poolDomain, subdomain string) (*CheckResult, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return &CheckResult{
			Available: false,
			Reason:    "unsupported-pool",
			Detail:    fmt.Sprintf("pool domain %s is not managed by OpenOva — pick a different pool or use BYO", poolDomain),
		}, nil
	}
	if reserved.IsReserved(subdomain) {
		return &CheckResult{
			Available: false,
			Reason:    "reserved",
			Detail:    "this subdomain is reserved for the Sovereign control plane — pick a different name",
		}, nil
	}

	available, err := a.store.IsAvailable(ctx, poolDomain, subdomain)
	if err != nil {
		return nil, fmt.Errorf("check availability: %w", err)
	}
	fqdn := subdomain + "." + poolDomain
	if available {
		return &CheckResult{Available: true, FQDN: fqdn}, nil
	}

	row, err := a.store.Get(ctx, poolDomain, subdomain)
	if err != nil {
		return nil, fmt.Errorf("read existing allocation: %w", err)
	}
	switch row.State {
	case store.StateReserved:
		return &CheckResult{
			Available: false,
			Reason:    "reserved-state",
			Detail:    "this subdomain has been reserved by another deployment in progress — try again in a few minutes",
			FQDN:      fqdn,
		}, nil
	case store.StateActive:
		return &CheckResult{
			Available: false,
			Reason:    "active-state",
			Detail:    "this subdomain is already taken by a live Sovereign — pick a different name",
			FQDN:      fqdn,
		}, nil
	default:
		return &CheckResult{
			Available: false,
			Reason:    "unknown-state",
			Detail:    "allocation exists in an unrecognised state — contact platform operators",
			FQDN:      fqdn,
		}, nil
	}
}

// ReserveInput carries the optional createdBy attribution. Defaults to
// "catalyst-api" when empty.
type ReserveInput struct {
	CreatedBy string
}

// Reserve transitions NULL → RESERVED for the (poolDomain, subdomain) pair,
// then materialises the per-Sovereign DNS shape:
//
//  1. Insert the pdm-pg row in state='reserved' (10-min TTL by default).
//  2. Create the empty child zone in PowerDNS with apex NS RRset pointing
//     at the OpenOva NS endpoints (ns1/2/3.openova.io).
//  3. Add the NS-delegation RRset for the child into the parent pool zone
//     (e.g. omani.works), so external resolvers know to follow the
//     delegation when they query "*.omantel.omani.works".
//  4. Enable DNSSEC on the child zone — the KSK + ZSK are minted now so
//     RRSIGs are emitted from the very first A record /commit writes.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 the DB row is written first; if any
// PowerDNS step fails we surface the error to the caller AFTER attempting a
// best-effort rollback of the row. The state machine guarantees that a
// successful Reserve return implies all four steps landed.
//
// Errors:
//
//	store.ErrConflict          — the row exists in any non-expired state
//	dynadot.ErrUnmanagedDomain — pool domain is not managed
func (a *Allocator) Reserve(ctx context.Context, poolDomain, subdomain string, in ReserveInput) (*store.Allocation, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return nil, dynadot.ErrUnmanagedDomain
	}
	if reserved.IsReserved(subdomain) {
		return nil, fmt.Errorf("subdomain %q is reserved", subdomain)
	}
	if len(a.nameservers) == 0 {
		return nil, errors.New("allocator misconfigured: no nameservers set")
	}

	createdBy := in.CreatedBy
	if createdBy == "" {
		createdBy = "catalyst-api"
	}

	alloc, err := a.store.Reserve(ctx, poolDomain, subdomain, a.reservationTTL, createdBy)
	if err != nil {
		return nil, err
	}

	childZone := childZoneName(poolDomain, subdomain)

	// Step 2: child zone with apex NS RRset.
	if err := a.dns.CreateZone(ctx, childZone, pdns.ZoneKindNative, a.nameservers); err != nil {
		a.log.Error("PowerDNS create child zone failed",
			"poolDomain", poolDomain, "subdomain", subdomain, "childZone", childZone, "err", err)
		// Best-effort rollback: free the reservation so the caller can retry
		// from a clean slate without waiting for the TTL sweeper.
		if rerr := a.rollbackReserve(ctx, poolDomain, subdomain); rerr != nil {
			a.log.Warn("rollback after CreateZone failure also failed",
				"poolDomain", poolDomain, "subdomain", subdomain, "rollbackErr", rerr)
		}
		return nil, fmt.Errorf("create child zone: %w", err)
	}

	// Step 3: parent NS delegation. (Pool tenancy. BYO has no parent.)
	if err := a.dns.AddNSDelegation(ctx, poolDomain, childZone, a.nameservers, pdns.DefaultParentNSDelegationTTL); err != nil {
		a.log.Error("PowerDNS parent NS delegation failed",
			"poolDomain", poolDomain, "subdomain", subdomain, "childZone", childZone, "err", err)
		// Best-effort cleanup: drop the child zone we just created and
		// roll back the reservation so the wizard's retry works.
		if derr := a.dns.DeleteZone(ctx, childZone); derr != nil {
			a.log.Warn("cleanup child zone after delegation failure also failed",
				"childZone", childZone, "err", derr)
		}
		if rerr := a.rollbackReserve(ctx, poolDomain, subdomain); rerr != nil {
			a.log.Warn("rollback after delegation failure also failed",
				"poolDomain", poolDomain, "subdomain", subdomain, "rollbackErr", rerr)
		}
		return nil, fmt.Errorf("add NS delegation: %w", err)
	}

	// Step 4: DNSSEC on the child zone (per docs/PLATFORM-POWERDNS.md
	// DNSSEC is mandatory).
	if err := a.dns.EnableDNSSEC(ctx, childZone); err != nil {
		a.log.Error("PowerDNS enable DNSSEC failed",
			"childZone", childZone, "err", err)
		// Don't roll back — the zone + delegation are valid, only signing
		// is missing. Operator can rectify by calling Release + re-Reserve.
		return alloc, fmt.Errorf("enable DNSSEC on %s: %w", childZone, err)
	}

	a.log.Info("pool-domain reserved",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
		"childZone", childZone,
		"ttl", a.reservationTTL.String(),
		"createdBy", createdBy,
		"expiresAt", alloc.ExpiresAt.Format(time.RFC3339),
	)
	return alloc, nil
}

// CommitInput carries the data /commit needs to flip RESERVED → ACTIVE.
type CommitInput struct {
	ReservationToken string
	SovereignFQDN    string
	LoadBalancerIP   string
	// ConsoleLoadBalancerIP — #4053. When non-empty, the `console` and `api`
	// A-records point HERE (the dedicated console LB → cilium-gateway-console)
	// instead of at LoadBalancerIP. This isolates the Sovereign console onto
	// its own Cilium Gateway / CiliumEnvoyConfig so a poisoned shared-gateway
	// CEC (one unresolvable app backend, cilium#35728) can never 404 the
	// console. Empty = pre-#4053 behaviour: every record points at
	// LoadBalancerIP (byte-identical to the legacy single-LB record set).
	ConsoleLoadBalancerIP string
}

// Commit flips a reservation to ACTIVE and writes the canonical 6-record set
// (apex, wildcard, console, api, gitea, harbor) into the CHILD zone. All
// records point at the supplied LB IP and use TTL 300 for fast failover.
//
// Order of operations:
//  1. Verify the row + reservation token (single row-locked Postgres tx).
//  2. Update row to state='active' (same tx).
//  3. Commit the tx.
//  4. PATCH the 6 RRsets into the child zone in a single PowerDNS PATCH
//     (atomic at the PowerDNS API layer).
//
// If step 4 fails we LEAVE the row in state='active' and surface the error
// — the wizard's retry button calls Commit again with the same token + IP
// (idempotent: PowerDNS PATCH replaces existing RRsets in place).
func (a *Allocator) Commit(ctx context.Context, poolDomain, subdomain string, in CommitInput) (*store.Allocation, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return nil, dynadot.ErrUnmanagedDomain
	}
	alloc, err := a.store.Commit(ctx, poolDomain, subdomain, store.CommitInput{
		ReservationToken: in.ReservationToken,
		SovereignFQDN:    in.SovereignFQDN,
		LoadBalancerIP:   in.LoadBalancerIP,
	})
	if err != nil {
		return nil, err
	}

	childZone := childZoneName(poolDomain, subdomain)
	rrsets := canonicalRecordSet(childZone, in.LoadBalancerIP, in.ConsoleLoadBalancerIP)

	if err := a.dns.PatchRRSets(ctx, childZone, rrsets); err != nil {
		a.log.Error("PowerDNS write canonical record set failed",
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"childZone", childZone,
			"loadBalancerIP", in.LoadBalancerIP,
			"err", err,
		)
		return alloc, fmt.Errorf("powerdns write: %w", err)
	}

	a.log.Info("pool-domain committed",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
		"childZone", childZone,
		"sovereignFQDN", in.SovereignFQDN,
		"loadBalancerIP", in.LoadBalancerIP,
		"rrsetCount", len(rrsets),
	)
	return alloc, nil
}

// Release deletes the row regardless of state and tears down the per-
// Sovereign DNS shape:
//
//  1. Drop the child zone in PowerDNS (records + DNSSEC keys gone).
//  2. Remove the NS delegation RRset from the parent pool zone.
//
// Reserved-but-not-yet-active rows still own a child zone (we create it in
// Reserve) so we always run both steps — they're idempotent at the
// PowerDNS layer (404 → no-op).
//
// Returns the freed Allocation (so the caller can log what was removed) or
// store.ErrNotFound when there was nothing to release.
func (a *Allocator) Release(ctx context.Context, poolDomain, subdomain string) (*store.Allocation, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return nil, dynadot.ErrUnmanagedDomain
	}
	alloc, err := a.store.Release(ctx, poolDomain, subdomain)
	if err != nil {
		return nil, err
	}

	childZone := childZoneName(poolDomain, subdomain)

	// Step 1: drop the child zone (records + DNSSEC keys retire together).
	if dnsErr := a.dns.DeleteZone(ctx, childZone); dnsErr != nil {
		a.log.Error("PowerDNS delete child zone failed",
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"childZone", childZone,
			"err", dnsErr,
		)
		return alloc, fmt.Errorf("powerdns delete zone: %w", dnsErr)
	}

	// Step 2: remove parent NS delegation.
	if dnsErr := a.dns.RemoveNSDelegation(ctx, poolDomain, childZone); dnsErr != nil {
		a.log.Error("PowerDNS remove NS delegation failed",
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"childZone", childZone,
			"err", dnsErr,
		)
		return alloc, fmt.Errorf("powerdns remove delegation: %w", dnsErr)
	}

	a.log.Info("pool-domain released",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
		"childZone", childZone,
		"previousState", string(alloc.State),
	)
	return alloc, nil
}

// List returns every allocation under the given pool domain.
func (a *Allocator) List(ctx context.Context, poolDomain string) ([]store.Allocation, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return nil, dynadot.ErrUnmanagedDomain
	}
	return a.store.List(ctx, poolDomain)
}

// RunSweeper starts a background loop that periodically deletes expired
// reservations. Each expired row also gets its DNS shape torn down — the
// child zone is dropped and the parent NS delegation is removed. This
// keeps PowerDNS in sync with pdm-pg even when a wizard click never
// reaches /commit.
//
// Cancel the parent context to stop the sweeper.
func (a *Allocator) RunSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.log.Info("sweeper shutdown")
			return
		case <-ticker.C:
			expired, err := a.store.ListExpiredReservations(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.log.Error("sweeper list-expired failed", "err", err)
				continue
			}
			for _, row := range expired {
				childZone := childZoneName(row.PoolDomain, row.Subdomain)
				if dnsErr := a.dns.DeleteZone(ctx, childZone); dnsErr != nil {
					a.log.Warn("sweeper child-zone delete failed",
						"childZone", childZone, "err", dnsErr)
				}
				if dnsErr := a.dns.RemoveNSDelegation(ctx, row.PoolDomain, childZone); dnsErr != nil {
					a.log.Warn("sweeper parent NS delegation remove failed",
						"poolDomain", row.PoolDomain, "childZone", childZone, "err", dnsErr)
				}
			}
			if len(expired) > 0 {
				deleted, err := a.store.ExpireReservations(ctx)
				if err != nil {
					a.log.Error("sweeper expire failed", "err", err)
					continue
				}
				if deleted > 0 {
					a.log.Info("sweeper expired reservations", "count", deleted)
				}
			}
		}
	}
}

// BootstrapParentZones ensures every managed pool domain exists as a
// PowerDNS zone before any /reserve is allowed. Idempotent — run once at
// PDM startup. The parent zone holds the NS-delegation RRsets that point
// child Sovereign zones at the OpenOva NS endpoints, so it MUST exist
// before Reserve can succeed.
//
// Each parent zone is created with apex NS records pointing at the same
// ns1/2/3.openova.io endpoints used for delegations. Pool zones are
// signed (DNSSEC mandatory per docs/PLATFORM-POWERDNS.md) so the chain of
// trust extends from the registrar's DS through the pool zone's KSK to
// each Sovereign child zone's KSK.
func (a *Allocator) BootstrapParentZones(ctx context.Context, poolDomains []string) error {
	if len(poolDomains) == 0 {
		return nil
	}
	if len(a.nameservers) == 0 {
		return errors.New("BootstrapParentZones: no nameservers configured")
	}
	for _, parent := range poolDomains {
		parent = strings.ToLower(strings.TrimSpace(parent))
		if parent == "" {
			continue
		}
		if err := a.dns.EnsureZone(ctx, parent, pdns.ZoneKindNative, a.nameservers); err != nil {
			return fmt.Errorf("ensure parent zone %s: %w", parent, err)
		}
		if err := a.dns.EnableDNSSEC(ctx, parent); err != nil {
			// Don't hard-fail bootstrap if the zone already exists with
			// DNSSEC enabled — EnableDNSSEC is idempotent. Log and continue.
			a.log.Warn("DNSSEC enable for parent zone returned error (may be harmless if already on)",
				"parent", parent, "err", err)
		}
		// #5007 — assert the pdm-intended parent-zone shape: delegations + apex
		// only, NEVER a parent-level `*.<pool>` A/AAAA wildcard. pdm never writes
		// one (the per-Sovereign wildcard lives in the delegated child zone), so a
		// `*.<pool>` record is a FOREIGN leftover — e.g. the dead `*.omantel.biz A
		// 49.12.16.160` a wiped Hetzner-era env left, which SHADOWED every child
		// name on the node resolver and black-holed image pulls (hw241 #5007).
		// Reap it so a wiped env can never shadow its siblings. Best-effort +
		// idempotent (DELETE of an absent RRset is a no-op): a reap failure never
		// blocks bootstrap. Gated (default on) for a deployment that intentionally
		// serves a flat parent wildcard managed elsewhere.
		if a.reapStaleParentWildcards {
			if err := a.dns.RemoveWildcardRecords(ctx, parent); err != nil {
				a.log.Warn("reap of stale parent-zone wildcard returned error (best-effort; may be harmless if none existed)",
					"parent", parent, "err", err)
			} else {
				a.log.Info("parent pool zone wildcard reaped (asserted delegations-only shape)", "parent", parent)
			}
		}
		a.log.Info("parent pool zone ensured", "parent", parent, "nameservers", a.nameservers)
	}
	return nil
}

// rollbackReserve best-effort deletes the pdm-pg row inserted by Reserve
// so a failed CreateZone doesn't leave a zombie reservation. Errors here
// are logged by the caller, never propagated — the original PowerDNS
// failure is the relevant signal.
func (a *Allocator) rollbackReserve(ctx context.Context, poolDomain, subdomain string) error {
	_, err := a.store.Release(ctx, poolDomain, subdomain)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// childZoneName derives the child zone FQDN from (poolDomain, subdomain).
// The convention is `<subdomain>.<poolDomain>` — verbatim as the issue
// body specifies. Lowercase, trimmed, no trailing dot (the PowerDNS client
// canonicalises on write).
func childZoneName(poolDomain, subdomain string) string {
	return strings.ToLower(strings.TrimSpace(subdomain)) + "." + strings.ToLower(strings.TrimSpace(poolDomain))
}

// canonicalRecordSet returns the 7-RRset payload PowerDNS PATCH expects to
// publish a fresh Sovereign:
//
//	@            A   <lb>          (apex of the child zone)
//	*            A   <lb>          (wildcard for ad-hoc subdomains)
//	console      A   <consoleLB>   (#4053 — isolated console gateway)
//	api          A   <consoleLB>   (#4053 — isolated console gateway)
//	marketplace  A   <consoleLB>   (#4053/#5034 — isolated console gateway)
//	gitea        A   <lb>
//	harbor       A   <lb>
//
// #4053 — the console-gateway names point at consoleLBIP (the dedicated console
// LB → cilium-gateway-console) so a poisoned SHARED-gateway CEC can never 404
// the console front doors. When consoleLBIP is empty (a provisioner or a
// deployment record that pre-dates #4053), they fall back to lbIP too — making
// the record set byte-identical to the legacy single-LB payload.
//
// #5034 — the console-gateway set is console + api + MARKETPLACE. Marketplace's
// HTTPRoute parents cilium-gateway-console (products/catalyst/chart/templates/
// org-services/marketplace-routes.yaml inherits the console-gateway parentRef
// default) exactly like console/api, so its A-record MUST ride consoleLBIP too.
// This mirrors catalyst-api's authoritative ConsoleGatewaySubdomains =
// {console, api, marketplace} in sovereign_dns_records.go — the two DNS writers
// (catalyst-api's parent-zone upsert + PDM's delegated child zone) MUST agree,
// and for a pool Sovereign the delegated child zone is the AUTHORITATIVE answer
// (it occludes the parent-zone records below the zone cut). Before this, PDM
// left marketplace on lbIP → marketplace.<fqdn> resolved the SHARED gateway and
// 404'd on every console-isolated prov, and (with the pre-#4054 image that
// dropped consoleLBIP entirely) console/api/marketplace ALL collapsed onto lbIP,
// occluding the correct parent-zone records and failing the Phase-1 console
// readiness gate so handover never fired. Every remaining name (apex, wildcard,
// gitea, harbor) always points at lbIP (the shared gateway) regardless.
//
// Per docs/PLATFORM-POWERDNS.md these names mirror the canonical-record
// contract and use TTL 300 for fast failover.
func canonicalRecordSet(childZone, lbIP, consoleLBIP string) []pdns.RRSet {
	if consoleLBIP == "" {
		consoleLBIP = lbIP
	}
	// Per-prefix target: the console-gateway front-door names (console/api/
	// marketplace) ride the console LB; everything else rides the shared LB.
	prefixes := []struct {
		name string
		ip   string
	}{
		{"", lbIP},
		{"*", lbIP},
		{"console", consoleLBIP},
		{"api", consoleLBIP},
		{"marketplace", consoleLBIP},
		{"gitea", lbIP},
		{"harbor", lbIP},
	}
	rrsets := make([]pdns.RRSet, 0, len(prefixes))
	for _, p := range prefixes {
		var name string
		if p.name == "" {
			name = childZone
		} else {
			name = p.name + "." + childZone
		}
		rrsets = append(rrsets, pdns.RRSet{
			Name:       name,
			Type:       "A",
			TTL:        pdns.DefaultChildRecordTTL,
			ChangeType: "REPLACE",
			Records:    []pdns.Record{{Content: p.ip}},
		})
	}
	return rrsets
}
