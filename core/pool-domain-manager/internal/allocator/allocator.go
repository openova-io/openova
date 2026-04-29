// Package allocator wires the persistence layer (store) to the DNS writer
// (dynadot) and exposes the four lifecycle operations PDM's HTTP handlers
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
// CNPG row before the corresponding side-effect (Dynadot write/delete) is
// invoked, so a crash between the two leaves the system in a recoverable
// state: at worst we have a row claiming state='active' with stale or
// missing DNS records, which an operator can reconcile by calling Release
// then re-running the wizard.
package allocator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/dynadot"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/reserved"
	"github.com/openova-io/openova/core/pool-domain-manager/internal/store"
)

// Allocator owns the state-machine logic. It is concurrency-safe — every
// operation maps to a single Postgres transaction in store; there is no
// in-memory mutable state on the Allocator itself.
type Allocator struct {
	store   *store.Store
	dynadot *dynadot.Client
	log     *slog.Logger

	// reservationTTL — how long a /reserve holds the name before the
	// sweeper reclaims it. Per the issue body this is 10 minutes.
	reservationTTL time.Duration
}

// New constructs an Allocator. ttl is the reservation TTL; pass
// 10*time.Minute for production.
func New(s *store.Store, d *dynadot.Client, log *slog.Logger, ttl time.Duration) *Allocator {
	return &Allocator{
		store:          s,
		dynadot:        d,
		log:            log,
		reservationTTL: ttl,
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
// machine-readable reason when it is not. Failure modes are:
//
//	"unsupported-pool" — poolDomain is not in DYNADOT_MANAGED_DOMAINS
//	"reserved"         — subdomain is in the reserved-name list
//	"reserved-state"   — somebody has reserved (TTL not expired) this name
//	"active-state"     — somebody has committed this name as a live Sovereign
//
// Note: NO net.LookupHost is invoked anywhere in this code path. PDM is the
// authoritative allocation source — DNS-wildcard parking records can never
// cause a false positive here. (This is the entire point of the service.)
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

	// Disambiguate the unavailable reason for the wizard's UX.
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
// holding the name for the configured TTL. Returns the Allocation, including
// the reservation token the caller MUST pass back to Commit.
//
// Errors:
//
//	store.ErrConflict — the row exists in any non-expired state
//	dynadot.ErrUnmanagedDomain — pool domain is not managed
func (a *Allocator) Reserve(ctx context.Context, poolDomain, subdomain string, in ReserveInput) (*store.Allocation, error) {
	if !dynadot.IsManagedDomain(poolDomain) {
		return nil, dynadot.ErrUnmanagedDomain
	}
	if reserved.IsReserved(subdomain) {
		return nil, fmt.Errorf("subdomain %q is reserved", subdomain)
	}
	createdBy := in.CreatedBy
	if createdBy == "" {
		createdBy = "catalyst-api"
	}
	alloc, err := a.store.Reserve(ctx, poolDomain, subdomain, a.reservationTTL, createdBy)
	if err != nil {
		return nil, err
	}
	a.log.Info("pool-domain reserved",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
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
}

// Commit flips a reservation to ACTIVE and writes the Dynadot DNS records
// (wildcard + canonical control-plane prefixes) for the new Sovereign.
//
// Order of operations is deliberate:
//  1. Verify the row exists and the reservation token matches (in a single
//     row-locked Postgres transaction so a concurrent Release can't race).
//  2. Update the row to state='active' (still in the same transaction).
//  3. Commit the transaction.
//  4. Write the Dynadot records. If this fails we LEAVE the row in
//     state='active' and surface the error to the caller — the operator
//     decides whether to Release (which will fix DNS) or retry.
//
// Per the auto-memory `feedback_dynadot_dns.md`: Dynadot writes are
// idempotent with add_dns_to_current_setting=yes, so step 4 is safe to
// retry from outside (the wizard's retry button calls Commit again with
// the same token and the same LB IP).
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

	if err := a.dynadot.AddSovereignRecords(ctx, poolDomain, subdomain, in.LoadBalancerIP); err != nil {
		// Row is already state='active'; do not roll it back. The caller can
		// Release if they want a clean slate. Surface the DNS error so the
		// wizard's UX shows the partial-failure path.
		a.log.Error("dynadot write after commit failed",
			"poolDomain", poolDomain,
			"subdomain", subdomain,
			"loadBalancerIP", in.LoadBalancerIP,
			"err", err,
		)
		return alloc, fmt.Errorf("dynadot write: %w", err)
	}
	a.log.Info("pool-domain committed",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
		"sovereignFQDN", in.SovereignFQDN,
		"loadBalancerIP", in.LoadBalancerIP,
	)
	return alloc, nil
}

// Release deletes the row regardless of state, then (if the row was active)
// removes the Dynadot DNS records. Reserved rows have no DNS side-effect to
// clean up.
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
	if alloc.State == store.StateActive {
		if dnsErr := a.dynadot.DeleteSubdomainRecords(ctx, poolDomain, subdomain); dnsErr != nil {
			a.log.Error("dynadot delete after release failed",
				"poolDomain", poolDomain,
				"subdomain", subdomain,
				"err", dnsErr,
			)
			return alloc, fmt.Errorf("dynadot delete: %w", dnsErr)
		}
	}
	a.log.Info("pool-domain released",
		"poolDomain", poolDomain,
		"subdomain", subdomain,
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
// reservations. Cancel the parent context to stop the sweeper. Should run as
// a goroutine off cmd/pdm/main.
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
			deleted, err := a.store.ExpireReservations(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				a.log.Error("sweeper expire failed", "err", err)
				continue
			}
			if deleted > 0 {
				a.log.Info("sweeper expired reservations", "count", deleted)
			}
		}
	}
}
