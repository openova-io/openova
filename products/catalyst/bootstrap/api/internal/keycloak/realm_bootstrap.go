package keycloak

// realm_bootstrap.go — EPIC-3 slice T2 (#1098) Keycloak composite
// realm-role bootstrap.
//
// At catalyst-api startup (when KEYCLOAK_BOOTSTRAP_TIER_ROLES=true), a
// goroutine in cmd/api/main.go calls EnsureTierRealmRoles to materialize
// the 5 catalog-tier realm-roles + their composite chain in the
// Sovereign Keycloak realm. Re-runs are no-ops once the chain is in
// place.
//
// Architectural rules (locked in by the master brief + canon):
//
//   1. ADR-0001 §2.7 — tier ClusterRoles are the on-cluster RBAC truth;
//      these Keycloak realm-roles are a UX layer (groups → tiers →
//      composites) that mirror but do NOT replace.
//
//   2. INVIOLABLE-PRINCIPLES #4 — the tier names + composite chain live
//      in this file (Go-source) NOT in template strings or env vars.
//      The `TierBootstrapPlan` slice IS the configuration; operators
//      who want a different chain author a different plan.
//
//   3. Idempotency anchor — every step calls Ensure* against Keycloak's
//      current state and short-circuits on the no-op path. A re-run on
//      a populated realm performs 5 GETs + 4 GETs (composite-list
//      reads) and ZERO writes.
//
//   4. Non-blocking — main.go's wire-up runs this in a goroutine. A
//      Keycloak that's slow to come up does NOT block catalyst-api's
//      HTTP listener. Errors are logged + the goroutine exits; the
//      next catalyst-api restart will pick up the bootstrap again.
//
// Per docs/EPICS-1-6-unified-design.md §6.2 the canonical chain is:
//
//     catalyst-viewer    (level 10, leaf, composite=false)
//     catalyst-developer (level 20) → composes catalyst-viewer
//     catalyst-operator  (level 30) → composes catalyst-developer
//     catalyst-admin     (level 40) → composes catalyst-operator
//     catalyst-owner     (level 50) → composes catalyst-admin

import (
	"context"
	"fmt"
)

// TierBootstrapStep is one row of the canonical catalog-tier chain.
// The Parent name is the realm role to ensure; ComposeChild is the
// realm role that should be attached as a composite (empty for the leaf
// `catalyst-viewer` tier). Level mirrors the ClusterRole's
// `catalyst.openova.io/tier-level` label so the access-matrix UI can
// sort tiers without a hardcoded order.
type TierBootstrapStep struct {
	Name         string
	Description  string
	Level        int
	ComposeChild string // empty when this tier is the leaf
}

// CatalogTierBootstrapPlan is the canonical 5-tier plan per
// docs/EPICS-1-6-unified-design.md §6.2. Order matters: viewer must be
// created BEFORE developer (so the EnsureCompositeRealmRole call for
// developer can resolve viewer's UUID), and so on up the chain.
//
// This slice is the configuration. INVIOLABLE-PRINCIPLES #4 forbids
// hardcoding values in templates / env vars; this file IS the
// canonical source. The Sovereign Keycloak realm name is supplied by
// the caller (cfg.Keycloak.Realm — env-driven), but the tier names
// themselves are fixed by the catalog tier system contract.
var CatalogTierBootstrapPlan = []TierBootstrapStep{
	{
		Name:         "catalyst-viewer",
		Description:  "Read-only access to in-scope resources (catalog tier 10).",
		Level:        10,
		ComposeChild: "",
	},
	{
		Name:         "catalyst-developer",
		Description:  "Viewer + workload exec/console + ticket update (catalog tier 20).",
		Level:        20,
		ComposeChild: "catalyst-viewer",
	},
	{
		Name:         "catalyst-operator",
		Description:  "Developer + console connect admin + SAM/patch/ticket-accept (catalog tier 30).",
		Level:        30,
		ComposeChild: "catalyst-developer",
	},
	{
		Name:         "catalyst-admin",
		Description:  "Operator + compute/credentials/applications/actions/networks/sessions admin (catalog tier 40).",
		Level:        40,
		ComposeChild: "catalyst-operator",
	},
	{
		Name:         "catalyst-owner",
		Description:  "Admin + RBAC + organization (catalog tier 50).",
		Level:        50,
		ComposeChild: "catalyst-admin",
	},
}

// EnsureTierRealmRoles is idempotent: re-runs are no-ops when the 5
// catalog-tier realm-roles already exist with the correct composite
// chain.
//
// Logic (per master brief 04-T2-keycloak-realm-role-bootstrap.md):
//
//   1. For each tier viewer → owner:
//      EnsureRealmRole with composite=true for non-viewer tiers and a
//      `tier-level` attribute encoding the integer ordering.
//
//   2. For each tier ≠ viewer:
//      EnsureCompositeRealmRole(parent, child) reads the parent's
//      current composites first; only POSTs the missing child.
//
//   3. On error, return — the caller (the goroutine in main.go) logs
//      the error and exits without retrying within the process. The
//      next catalyst-api restart picks up where this run failed.
//
// Note: the `realm` parameter is informational here — the underlying
// `Client` was constructed with its realm at New() time, so all
// HTTP requests are already scoped to the right realm. The parameter
// is kept on the API for symmetry with the master brief signature
// (`func (c *Client) EnsureTierRealmRoles(ctx, realm) error`) and so
// future refactors that route a single Client across multiple
// Sovereign realms have a clear extension point.
func (c *Client) EnsureTierRealmRoles(ctx context.Context, realm string) error {
	if realm != "" && realm != c.realm {
		return fmt.Errorf("keycloak.EnsureTierRealmRoles: realm mismatch: caller=%q client=%q (Client is constructed with a fixed realm; per-call realm override not supported in this slice)",
			realm, c.realm)
	}

	// Phase 1 — ensure the 5 realm roles exist.
	for _, step := range CatalogTierBootstrapPlan {
		composite := step.ComposeChild != ""
		rr := RealmRole{
			Name:        step.Name,
			Description: step.Description,
			Composite:   composite,
			Attributes: map[string][]string{
				// Encoded as a string array per Keycloak v24 schema.
				"tier-level": {fmt.Sprintf("%d", step.Level)},
			},
		}
		if _, err := c.EnsureRealmRole(ctx, rr); err != nil {
			return fmt.Errorf("ensure realm role %q: %w", step.Name, err)
		}
	}

	// Phase 2 — wire the composite chain. This MUST run after phase 1
	// because EnsureCompositeRealmRole calls getRealmRole on the child
	// to resolve its UUID; that GET needs the role to exist.
	for _, step := range CatalogTierBootstrapPlan {
		if step.ComposeChild == "" {
			continue // viewer is the leaf
		}
		if err := c.EnsureCompositeRealmRole(ctx, step.Name, step.ComposeChild); err != nil {
			return fmt.Errorf("ensure composite %q→%q: %w", step.ComposeChild, step.Name, err)
		}
	}
	return nil
}
