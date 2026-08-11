// sovereign_seed_reconciler.go — #4877. LEVEL-TRIGGERED self-heal for the
// three OpenBao seed seams (newapi-admin-token / anthropic / mcp-bearer).
//
// THE GAP this file closes:
//
//	seedNewapiAdminToken, seedAnthropicToken and seedMCPBearer are each fired
//	from EXACTLY ONE place — runOrganizationPipeline
//	(organization_provisioning.go, Step 1) — once per HTTP Org-create, with NO
//	retry and NO reconcile. If that single write misses (catalyst-api OOM
//	mid-pipeline #4811, or a prereq — the NewAPI bridge Secret, the Anthropic
//	env cred, the handover signer — not yet reflected at that instant), the
//	OpenBao paths
//	  - secret/catalyst/newapi/admin-token
//	  - secret/catalyst/anthropic/token
//	  - secret/catalyst/agenity/<slug>/mcp-bearer
//	are NEVER written. The ExternalSecrets that read them then stay
//	READY=False / SecretSyncedError FOREVER (unified-rbac 401s; the agenity
//	agent never gets its Anthropic cred or its Catalyst MCP bearer), because
//	nothing re-fires the producer — the Org is already "created".
//
// THE FIX (this file):
//
//	Re-run the seeds on catalyst-api STARTUP and on a periodic reconcile so a
//	missed write self-heals. The seeds are already idempotent (PutKVv2
//	overwrites) and non-fatal (a missing prereq is a logged skip, never an
//	error), so a reconcile loop is safe. STARTUP is the primary recovery path:
//	after the OOM that dropped the write, the catalyst-api Pod restarts and the
//	startup pass immediately re-seeds every absent path. The periodic loop is
//	the secondary safety net for a prereq that lands after startup (e.g. NewAPI
//	finishing its install).
//
// READ-BEFORE-WRITE (why we don't blindly re-PutKVv2 every pass):
//
//	seedMCPBearer MINTS A FRESH bearer (new jti + new exp) on every call, so a
//	blind re-seed every interval would churn the OpenBao value → ExternalSecret
//	→ per-Org agenity-mcp-bearer Secret on every tick, growing KV version
//	history and re-writing a healthy Secret needlessly. So each pass first
//	READS the target path (GetKVv2) and seeds ONLY when it is absent or its
//	expected property is empty — i.e. exactly the #4877 gap ("never written").
//	A path that already holds a valid value is a true no-op (zero writes, zero
//	log noise). The mcp-bearer's own doc comment frames periodic re-mint as an
//	expiry-refresh feature, but the minted bearer is 1-year TTL and env-
//	projected into the agenity StatefulSet (no hot-reload, no reloader on the
//	chart), so a healthy bearer never needs refreshing between reconciles —
//	self-heal of an ABSENT path is the whole job here.
//
// Global vs per-Org scope:
//
//	newapi-admin-token + anthropic are Sovereign-GLOBAL (one cluster-shared
//	path each) → checked/seeded unconditionally once per pass. mcp-bearer is
//	PER-ORG (the path carries the Org slug) → we enumerate the Organizations
//	the Sovereign knows about the SAME way the console directory + the
//	tenant-registry reconcile do (orgResponsesFromCRs → list every
//	orgs.openova.io CR via the in-cluster dynamic client) and check/seed each.
//	Out-of-cluster / CI (no dynamic client) yields zero Orgs and the per-Org
//	leg is a no-op, matching every other dynamic-client path in this package.
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): this never logs any token,
// bearer, key or PEM — only outcome classes + byte-free identifiers.

package handler

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

const (
	// defaultSeedReconcileInterval — steady-state cadence of the self-heal
	// loop. Generous (10m): the #4877 fault is a PERSISTENT SecretSyncedError,
	// not a fast-moving one, and the startup pass already heals the common
	// post-OOM case immediately; the periodic loop only needs to catch a
	// prereq that lands after startup. Overridable per docs/PRINCIPLES.md #4.
	defaultSeedReconcileInterval = 10 * time.Minute
	// seedReconcileWarmup delays the first pass so the Org-create pipeline +
	// the OpenBao/handover-signer/org-deps wiring settle first — a redundant
	// early pass is harmless (read-before-write no-ops), this just keeps the
	// startup logs clean.
	seedReconcileWarmup = 45 * time.Second
)

// StartSovereignSeedReconciler launches the #4877 seed self-heal loop in a
// background goroutine. Returns immediately; ctx cancellation stops the loop.
// Run from main.go AFTER the OpenBao client, handover signer and Organization
// deps are wired (all three are seed prerequisites).
//
// Dormant (returns without spawning) when:
//   - CATALYST_SEED_RECONCILER_ENABLED=false (kill switch), or
//   - h.openbao is nil — the Catalyst-Zero orchestrator has no in-cluster
//     OpenBao client and never hosts these secrets, so there is nothing to
//     self-heal (mirrors every seed's own nil-OpenBao skip).
func (h *Handler) StartSovereignSeedReconciler(ctx context.Context) {
	if h == nil {
		return
	}
	if os.Getenv("CATALYST_SEED_RECONCILER_ENABLED") == "false" {
		if h.log != nil {
			h.log.Info("[SEED-RECONCILE] disabled via CATALYST_SEED_RECONCILER_ENABLED=false")
		}
		return
	}
	if h.openbao == nil {
		if h.log != nil {
			h.log.Info("[SEED-RECONCILE] dormant: no OpenBao client (orchestrator / Catalyst-Zero) — nothing to self-heal")
		}
		return
	}

	interval := durationEnv("CATALYST_SEED_RECONCILE_INTERVAL", defaultSeedReconcileInterval)
	if h.log != nil {
		h.log.Info("[SEED-RECONCILE] starting (self-heal for newapi-admin-token / anthropic / per-Org mcp-bearer seeds, #4877)",
			"intervalSec", int(interval.Seconds()),
			"warmupSec", int(seedReconcileWarmup.Seconds()),
		)
	}

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(seedReconcileWarmup):
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			h.runSeedReconcilePass(ctx)
			select {
			case <-ctx.Done():
				if h.log != nil {
					h.log.Info("[SEED-RECONCILE] stopped")
				}
				return
			case <-t.C:
			}
		}
	}()
}

// runSeedReconcilePass performs one idempotent, best-effort self-heal pass:
// for each of the three OpenBao seed paths it READS the current value and
// re-runs the producing seed ONLY when the path is absent / empty (the #4877
// gap). Exposed (unexported-but-package-visible) for the reconciler test.
//
// Never fatal: every seed already folds its own failures to a logged skip, and
// a panic anywhere in the pass is recovered so a self-heal attempt can NEVER
// crash catalyst-api. A read/list error skips just that leg and retries next
// tick.
func (h *Handler) runSeedReconcilePass(ctx context.Context) {
	if h == nil || h.openbao == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil && h.log != nil {
			h.log.Error("[SEED-RECONCILE] pass panicked; recovered — catalyst-api unaffected, retry next tick",
				"panic", r)
		}
	}()

	// (1) newapi admin-token — Sovereign-GLOBAL. Seed only when the path lacks
	// a non-empty ADMIN_API_TOKEN. seedNewapiAdminToken itself no-ops (loud
	// skip) if NewAPI's bridge Secret hasn't rendered yet, so a still-absent
	// path simply retries next tick once the chart lands.
	h.reconcileGlobalSeed(ctx,
		"newapi-admin-token",
		newapiAdminTokenSeedMountPath, newapiAdminTokenSeedSecretPath,
		[]string{newapiAdminTokenSeedKVProperty},
		func() { _ = h.seedNewapiAdminToken(ctx) },
	)

	// (2) anthropic token — Sovereign-GLOBAL. Seed only when the path lacks a
	// non-empty apiKey AND credentialsJson. seedAnthropicToken loud-skips when
	// the founder cred env is unset, so on a credless Sovereign the path stays
	// absent and the loop keeps (usefully) surfacing that gap.
	h.reconcileGlobalSeed(ctx,
		"anthropic",
		anthropicSeedMountPath, anthropicSeedSecretPath,
		[]string{"apiKey", "credentialsJson"},
		func() { _ = h.seedAnthropicToken(ctx) },
	)

	// (3) per-Org mcp-bearer. Enumerate every Organization CR (the single
	// source of truth both the BSS door and the marketplace funnel write) and
	// self-heal each Org's per-slug bearer path. Global seeds above already
	// ran, so a list failure here never starves them.
	orgs, err := h.orgResponsesFromCRs(ctx)
	if err != nil {
		if h.log != nil {
			h.log.Warn("[SEED-RECONCILE] list Organizations failed; per-Org mcp-bearer self-heal skipped this pass — retry next tick",
				"err", err)
		}
		return
	}
	healed := 0
	for i := range orgs {
		slug := strings.TrimSpace(orgs[i].Subdomain)
		if slug == "" {
			continue
		}
		present, perr := h.openbaoPathHasProperty(ctx,
			mcpBearerMountPath, mcpBearerSecretPath(slug), mcpBearerKVBearerProperty)
		if perr != nil {
			if h.log != nil {
				h.log.Warn("[SEED-RECONCILE] mcp-bearer presence check failed; skipping Org this pass",
					"slug", slug, "err", perr)
			}
			continue
		}
		if present {
			continue // healthy — no churn.
		}
		if h.log != nil {
			h.log.Info("[SEED-RECONCILE] mcp-bearer OpenBao path absent for Org — self-healing (#4877)",
				"slug", slug)
		}
		if out := h.seedMCPBearer(ctx, slug, orgs[i].AdminEmail); out == MCPBearerSeedOutcomeSeeded {
			healed++
		}
	}
	if healed > 0 && h.log != nil {
		h.log.Info("[SEED-RECONCILE] per-Org mcp-bearer self-heal complete",
			"orgs", len(orgs), "healed", healed)
	}
}

// SeedHealOutcome — the VERIFIED result of one reconcileGlobalSeed attempt.
//
// Every value except SeedHealUnverified is backed by a read of the OpenBao path
// performed AFTER the seed ran. That is the whole point of the type: the
// reconciler is not allowed to report an outcome it did not observe (#4277).
type SeedHealOutcome string

const (
	// SeedHealHealthy — the path already held a non-empty expected property on
	// entry. Nothing was written, nothing was logged (no churn, no noise).
	SeedHealHealthy SeedHealOutcome = "healthy"
	// SeedHealHealed — the path was absent/hollow on entry and a re-read AFTER
	// the seed shows it now holds a value. The only outcome that may be
	// described as a heal.
	SeedHealHealed SeedHealOutcome = "healed"
	// SeedHealUnhealed — the path was absent/hollow on entry and is STILL
	// absent/hollow after the seed ran: the producer could not produce (its
	// source credential is unset, revoked, or its write failed). This is the
	// #4277 live state and it is reported LOUDLY.
	SeedHealUnhealed SeedHealOutcome = "unhealed"
	// SeedHealUnverified — a transport error on the read left the pass with no
	// evidence either way. Never reported as healed; the next tick retries.
	SeedHealUnverified SeedHealOutcome = "unverified"
)

// seedRemediation maps a reconcileGlobalSeed label to the ONE concrete action
// that closes its gap. It is carried on the unhealed error so the line an
// sovereign-admin actually finds in the log tells them what to populate rather
// than only that something is wrong.
//
// Per docs/PRINCIPLES.md #10 these name the ENV VAR and the Secret to fill —
// never a credential value.
var seedRemediation = map[string]string{
	"anthropic": "set CATALYST_ANTHROPIC_CREDENTIALS_JSON (and optionally CATALYST_ANTHROPIC_API_KEY) on catalyst-api " +
		"— chart values sovereign.anthropic.*, or the operator-rotatable Secret catalyst-system/sovereign-anthropic-credentials (#4277). " +
		"Until then every Organization's agenity-anthropic-token ExternalSecret stays SecretSyncedError and no Agenity workspace agent can authenticate.",
	"newapi-admin-token": "NewAPI's bridge Secret has not rendered its ADMIN_SECRET yet — check the bp-newapi HelmRelease (#4477). " +
		"Until then the catalyst-newapi-admin-token ExternalSecret stays SecretSyncedError and unified-rbac's admin-API calls 401.",
}

// reconcileGlobalSeed checks one Sovereign-global OpenBao path, invokes its
// producing seed when the path is absent / its expected property empty, and
// then RE-READS the path to establish whether the heal actually happened.
//
// Why the re-read (#4277). This function used to log
//
//	"[SEED-RECONCILE] OpenBao path absent — self-healing (#4877)"
//
// and then call seed() and return. That line announces an outcome the function
// never observed. Every producer it drives is deliberately non-fatal — each one
// folds a missing prerequisite into a logged skip and returns normally — so
// "self-healing" was emitted identically whether the write landed or the
// producer did nothing at all. On a Sovereign holding no Anthropic credential
// that is exactly what happened: the same reassuring INFO line every 10 minutes
// for the life of the cluster, while `secret/catalyst/anthropic/token` stayed
// absent and every Organization's agenity ExternalSecret stayed
// SecretSyncedError. The gap was not unreported — it was reported as progress.
//
// A verdict now requires positive evidence: the pass says "healed" only after
// reading the value back, and says so LOUDLY, with the remediation, when the
// path is still empty. A transport error yields SeedHealUnverified rather than
// either verdict.
//
// Returns the outcome so a caller/test can assert on a value instead of on log
// text. Callers may ignore it — the loud line is the operator-facing surface.
func (h *Handler) reconcileGlobalSeed(ctx context.Context, label, mount, path string, props []string, seed func()) SeedHealOutcome {
	present, err := h.openbaoPathHasProperty(ctx, mount, path, props...)
	if err != nil {
		if h.log != nil {
			h.log.Warn("[SEED-RECONCILE] presence check failed; skipping this pass — retry next tick",
				"seed", label, "err", err)
		}
		return SeedHealUnverified
	}
	if present {
		return SeedHealHealthy // healthy — no churn.
	}
	if h.log != nil {
		// Deliberately phrased as an ATTEMPT. The verdict is below, after the
		// re-read; this line must never be mistakable for a completed heal.
		h.log.Info("[SEED-RECONCILE] OpenBao path absent — attempting self-heal (#4877)",
			"seed", label, "openbaoPath", mount+"/"+path)
	}
	seed()

	healed, verr := h.openbaoPathHasProperty(ctx, mount, path, props...)
	if verr != nil {
		if h.log != nil {
			h.log.Warn("[SEED-RECONCILE] self-heal verification read failed — outcome UNKNOWN this pass, retry next tick",
				"seed", label, "openbaoPath", mount+"/"+path, "err", verr)
		}
		return SeedHealUnverified
	}
	if healed {
		if h.log != nil {
			h.log.Info("[SEED-RECONCILE] self-heal complete — path verified non-empty by read-back",
				"seed", label, "openbaoPath", mount+"/"+path)
		}
		return SeedHealHealed
	}
	if h.log != nil {
		h.log.Error("[SEED-RECONCILE] 🛑 self-heal did NOT take — the OpenBao path is STILL empty after the producer ran; every ExternalSecret reading it stays SecretSyncedError (#4277)",
			"seed", label,
			"openbaoPath", mount+"/"+path,
			"expectedProperties", strings.Join(props, ","),
			"remediation", seedRemediation[label],
		)
	}
	return SeedHealUnhealed
}

// openbaoPathHasProperty reports whether the KV-v2 path exists AND at least one
// of the named properties holds a non-empty string.
//
//   - NotFound / empty body  → (false, nil): the #4877 gap ("never written") —
//     the caller seeds.
//   - present, a prop non-empty → (true, nil): healthy — the caller no-ops.
//   - present, all props empty  → (false, nil): a hollow / empty-seeded path —
//     the caller re-seeds to fill it.
//   - transport error           → (false, err): the caller skips this pass and
//     retries next tick (a write would fail against the same blip).
func (h *Handler) openbaoPathHasProperty(ctx context.Context, mount, path string, props ...string) (bool, error) {
	data, err := h.openbao.GetKVv2(ctx, mount, path)
	if err != nil {
		if errors.Is(err, openbao.ErrSecretNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, p := range props {
		if s, ok := data[p].(string); ok && strings.TrimSpace(s) != "" {
			return true, nil
		}
	}
	return false, nil
}
