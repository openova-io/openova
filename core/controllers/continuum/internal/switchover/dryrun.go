// DryRun report — slice F-2 of EPIC-6 (#1101).
//
// `Sequencer.DryRun` walks the same 7-step plan that Execute would
// run, but without mutating any state — no CNPG annotation, no
// HTTPRoute weight flip, no PDM commit, no lease swap, no audit emit.
// The output is a `DryRunReport` U-DR-1's switchover dialog displays
// before showing the [ Confirm Switchover ] button so the operator can
// see blockers / warnings + estimated disruption window upfront.
//
// Key invariants:
//
//   - DryRun is read-only end-to-end. It MUST NOT cause any
//     externally-observable side effect (no DNS write, no annotation,
//     no audit emit). Tests assert this by counting Recorder events
//     before/after.
//   - DryRun returns a single Report even when individual step
//     pre-checks fail; per-step failures surface as `Blockers` (FATAL,
//     would-prevent-switchover) or `Warnings` (advisory). The CALLER
//     decides how to render them.
//   - PlanFingerprint is a content hash of the plan inputs, intended
//     for cache idempotency: identical inputs should yield the same
//     fingerprint so a UI can avoid re-rendering on identical
//     consecutive calls. The fingerprint covers the SwitchoverPlan
//     fields that affect the report (FromRegion, ToRegion, CNPGPair,
//     CNPGNamespace, HTTPRouteName, PDMZone, LeaseTTL, Reason,
//     InitiatedBy) — NOT timing-dependent fields.
//
// Per INVIOLABLE-PRINCIPLES #5 the dry-run endpoint is owner-tier
// gated by the API server (api/server.go); the Sequencer method
// itself is just the pure-Go computation.
package switchover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// DryRunReport is the output of Sequencer.DryRun — a single document
// the UI renders before the operator confirms a switchover.
//
// Schema is part of the cross-slice contract: U-DR-1 (UI) decodes
// against this shape. Field renames here ripple through the API
// server's handler + the UI's JSON decoder. Coordinate via the
// canonical-seam map before changing.
type DryRunReport struct {
	// PlanFingerprint is a 16-hex SHA-256 prefix of the
	// content-stable plan inputs. Identical plans produce identical
	// fingerprints — useful for client-side cache keys.
	PlanFingerprint string `json:"planFingerprint"`

	// Steps lists the per-step pre-condition evaluations in 1..7
	// order. The slice is ALWAYS exactly 7 entries — even when an
	// earlier step is a blocker, later steps still surface a "skipped
	// (depends on step N)" note rather than being omitted, so the UI's
	// progress view stays consistent.
	Steps []DryRunStep `json:"steps"`

	// Warnings are non-fatal advisories the operator should see but
	// MAY proceed despite (e.g. "Cilium HTTPRoute weight=0 drain may
	// take longer if connection backlog is high").
	Warnings []string `json:"warnings,omitempty"`

	// Blockers are FATAL conditions that would prevent the switchover
	// from succeeding. The UI MUST disable the [ Confirm ] button when
	// Blockers is non-empty.
	Blockers []string `json:"blockers,omitempty"`

	// EstimatedDurationSeconds — sum of per-step expected durations.
	// Conservative upper bound, NOT a prediction.
	EstimatedDurationSeconds int `json:"estimatedDurationSeconds"`

	// EstimatedWriteDisruptionSeconds — sum of step-2 (cordon),
	// step-3 (HTTP drain), step-5 (lease swap) durations. This is the
	// window during which application writes may fail or stall.
	EstimatedWriteDisruptionSeconds int `json:"estimatedWriteDisruptionSeconds"`

	// GeneratedAt is the wall-clock UTC stamp at report generation.
	GeneratedAt time.Time `json:"generatedAt"`
}

// DryRunStep is a single per-step entry in the report.
type DryRunStep struct {
	// Step is the 1-based step number (1..7).
	Step int `json:"step"`

	// Name is the canonical step name (matches Sequencer.steps()).
	Name string `json:"name"`

	// PreconditionsMet is true when the step would proceed cleanly,
	// false when at least one Note describes a Blocker (the UI can
	// short-circuit on this; the per-Note severity is in the
	// surrounding Report's Warnings + Blockers).
	PreconditionsMet bool `json:"preconditionsMet"`

	// EstimatedDurationSeconds — conservative upper bound for THIS
	// step in isolation.
	EstimatedDurationSeconds int `json:"estimatedDurationSeconds"`

	// Notes are short human-readable observations the dry-run
	// surfaced (e.g. "current primary lease holder verified", "WAL
	// lag 12s < 30s threshold OK"). Notes that map to Blockers /
	// Warnings are also surfaced separately at the Report level.
	Notes []string `json:"notes,omitempty"`
}

// Per-step duration estimates (seconds). These are conservative
// upper bounds derived from the design doc §9.3 timings; the Sequencer
// uses them for EstimatedDurationSeconds + EstimatedWriteDisruption.
//
// They're exported so tests + the UI can pin them and so future
// tuning is one-edit traceable.
const (
	StepValidateLeaseSeconds = 1
	StepCordonOldSeconds     = 2
	StepDrainHTTPSeconds     = 10 // matches DrainSeconds default
	StepFlipDNSSeconds       = 5
	StepSwapLeaseSeconds     = 2
	StepUncordonNewSeconds   = 3
	StepAuditEmitSeconds     = 1
)

// DryRun walks the 7 steps in dry-run mode and returns a
// DryRunReport. The plan is NOT mutated.
//
// On a missing dependency (e.g. nil Witness, nil PDMCommit) DryRun
// records a Blocker for that step rather than returning an error —
// the caller is meant to render the Report rather than handle a
// hard error. Genuine errors (ctx-cancel, plan.Validate failure)
// return (zero, error).
func (s *Sequencer) DryRun(ctx context.Context, plan SwitchoverPlan) (DryRunReport, error) {
	if err := ctx.Err(); err != nil {
		return DryRunReport{}, err
	}
	plan = plan.Defaults()
	if err := plan.Validate(); err != nil {
		return DryRunReport{}, fmt.Errorf("dry-run: %w", err)
	}

	report := DryRunReport{
		PlanFingerprint: planFingerprint(plan),
		GeneratedAt:     s.now().UTC(),
		Steps:           make([]DryRunStep, 7),
	}

	// Step 1 — validate-lease.
	report.Steps[0] = s.dryStep1ValidateLease(ctx, plan, &report)

	// Step 2 — cordon-old-primary.
	report.Steps[1] = s.dryStep2CordonOld(ctx, plan, &report)

	// Step 3 — drain-http.
	step3Seconds := plan.DrainSeconds
	if step3Seconds <= 0 {
		step3Seconds = StepDrainHTTPSeconds
	}
	report.Steps[2] = s.dryStep3DrainHTTP(ctx, plan, &report, step3Seconds)

	// Step 4 — flip-dns.
	report.Steps[3] = s.dryStep4FlipDNS(ctx, plan, &report)

	// Step 5 — swap-lease.
	report.Steps[4] = s.dryStep5SwapLease(ctx, plan, &report)

	// Step 6 — uncordon-new-primary.
	report.Steps[5] = s.dryStep6UncordonNew(ctx, plan, &report)

	// Step 7 — audit-emit.
	report.Steps[6] = s.dryStep7Audit(ctx, plan, &report)

	// Aggregate durations.
	for _, st := range report.Steps {
		report.EstimatedDurationSeconds += st.EstimatedDurationSeconds
	}
	report.EstimatedWriteDisruptionSeconds =
		report.Steps[1].EstimatedDurationSeconds + // cordon-old
			report.Steps[2].EstimatedDurationSeconds + // drain-http
			report.Steps[4].EstimatedDurationSeconds // swap-lease

	return report, nil
}

// planFingerprint hashes the content-stable plan inputs into a
// 16-char hex prefix.
func planFingerprint(plan SwitchoverPlan) string {
	h := sha256.New()
	// Concatenate fields with `|` so a hostname-with-pipe doesn't
	// alias another plan (defensive — region names won't have pipes).
	fmt.Fprintf(h, "from=%s|to=%s|pair=%s|ns=%s|hr=%s/%s|zone=%s|ttl=%s|reason=%s|by=%s",
		plan.FromRegion,
		plan.ToRegion,
		plan.CNPGPair,
		plan.CNPGNamespace,
		plan.HTTPRouteNamespace,
		plan.HTTPRouteName,
		plan.PDMZone,
		plan.LeaseTTL.String(),
		plan.Reason,
		plan.InitiatedBy,
	)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:16]
}

func (s *Sequencer) dryStep1ValidateLease(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 1, Name: "validate-lease", EstimatedDurationSeconds: StepValidateLeaseSeconds}
	if s.Witness == nil {
		st.Notes = append(st.Notes, "Witness client not configured")
		rep.Blockers = append(rep.Blockers, "step-1: Witness client is required")
		return st
	}
	wState, err := s.Witness.Read(ctx)
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf("Witness read error: %v", err))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-1: witness read failed: %v", err))
		return st
	}
	switch {
	case wState.Holder == "":
		st.Notes = append(st.Notes, "lease unheld — ToRegion will take it cold")
		rep.Warnings = append(rep.Warnings, "step-1: lease currently unheld; switchover will acquire on ToRegion as a fresh claim")
		st.PreconditionsMet = true
	case wState.Holder == plan.FromRegion:
		st.Notes = append(st.Notes, fmt.Sprintf("lease held by FromRegion %q (gen=%d) — clean handoff", wState.Holder, wState.Generation))
		st.PreconditionsMet = true
	case wState.Holder == plan.ToRegion:
		st.Notes = append(st.Notes, fmt.Sprintf("lease ALREADY held by ToRegion %q — switchover would be a no-op acquire", wState.Holder))
		rep.Warnings = append(rep.Warnings, "step-1: lease already on ToRegion; this would be a redundant switchover")
		st.PreconditionsMet = true
	default:
		st.Notes = append(st.Notes, fmt.Sprintf("lease held by THIRD-PARTY %q (expected %q)", wState.Holder, plan.FromRegion))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-1: lease holder %q != FromRegion %q (third-party split-brain risk)", wState.Holder, plan.FromRegion))
	}
	return st
}

func (s *Sequencer) dryStep2CordonOld(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 2, Name: "cordon-old-primary", EstimatedDurationSeconds: StepCordonOldSeconds}
	if s.CNPG == nil {
		st.Notes = append(st.Notes, "CNPG reader not configured")
		rep.Blockers = append(rep.Blockers, "step-2: CNPG reader is required")
		return st
	}
	primary, replica, err := s.CNPG.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf("FindPair %s/%s: %v", plan.CNPGNamespace, plan.CNPGPair, err))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-2: cannot resolve cluster-pair %q in ns %q: %v", plan.CNPGPair, plan.CNPGNamespace, err))
		return st
	}
	st.Notes = append(st.Notes, fmt.Sprintf("primary=%s replica=%s", primary.GetName(), replica.GetName()))
	// Inspect annotations — if the primary already carries a cordon
	// annotation, that's a warning (likely a stuck switchover).
	if anns := primary.GetAnnotations(); anns != nil {
		if v, ok := anns["cnpg.io/cluster.primary"]; ok && v != "" {
			st.Notes = append(st.Notes, fmt.Sprintf("primary already carries cordon annotation = %q", v))
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("step-2: primary %q already has cnpg.io/cluster.primary=%q (likely stuck prior switchover)", primary.GetName(), v))
		}
	}
	st.PreconditionsMet = true
	return st
}

func (s *Sequencer) dryStep3DrainHTTP(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport, drainSecs int) DryRunStep {
	st := DryRunStep{Step: 3, Name: "drain-http", EstimatedDurationSeconds: drainSecs}
	if s.HTTPRoute == nil || plan.HTTPRouteName == "" {
		st.Notes = append(st.Notes, "no HTTPRoute drainer (single-region degenerate or not wired) — step is a no-op")
		st.EstimatedDurationSeconds = 0
		st.PreconditionsMet = true
		return st
	}
	st.Notes = append(st.Notes, fmt.Sprintf("HTTPRoute %s/%s will be patched (weight=0 toward %s) and slept %ds", plan.HTTPRouteNamespace, plan.HTTPRouteName, plan.FromRegion, drainSecs))
	if drainSecs > 30 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("step-3: drain window %ds is unusually long", drainSecs))
	}
	st.PreconditionsMet = true
	return st
}

func (s *Sequencer) dryStep4FlipDNS(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 4, Name: "flip-dns", EstimatedDurationSeconds: StepFlipDNSSeconds}
	if s.PDMCommit == nil {
		st.Notes = append(st.Notes, "PDMCommit not configured")
		rep.Blockers = append(rep.Blockers, "step-4: PDMCommit closure is required")
		return st
	}
	// Synthesize records WITHOUT committing — surfaces shape errors early.
	params := plan.SynthParams
	params.PrimaryRegion = plan.ToRegion
	records, err := dns.Synthesize(plan.Application, params)
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf("dns.Synthesize: %v", err))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-4: lua-record synthesis failed: %v", err))
		return st
	}
	if len(records) == 0 {
		st.Notes = append(st.Notes, "Synthesize returned 0 records (no hostnames in Application)")
		rep.Warnings = append(rep.Warnings, "step-4: no lua-records will be written (Application has no public hostnames)")
		st.PreconditionsMet = true
		return st
	}
	if _, ok := params.RegionToIPs[plan.ToRegion]; !ok {
		st.Notes = append(st.Notes, fmt.Sprintf("RegionToIPs has no entry for ToRegion %q", plan.ToRegion))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-4: no IPs configured for ToRegion %q", plan.ToRegion))
		return st
	}
	st.Notes = append(st.Notes, fmt.Sprintf("would commit %d lua-record(s) to PowerDNS zone %q", len(records), plan.PDMZone))
	st.PreconditionsMet = true
	return st
}

func (s *Sequencer) dryStep5SwapLease(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 5, Name: "swap-lease", EstimatedDurationSeconds: StepSwapLeaseSeconds}
	if s.Witness == nil {
		// Already blocked at step 1 — defensive note.
		st.Notes = append(st.Notes, "Witness client not configured (see step-1)")
		return st
	}
	wState, err := s.Witness.Read(ctx)
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf("witness read for swap dry-run: %v", err))
		return st
	}
	if wState.Holder != "" && wState.Holder != plan.FromRegion && wState.Holder != plan.ToRegion {
		st.Notes = append(st.Notes, fmt.Sprintf("lease blocked by %q — Acquire would return ErrLeaseHeldByAnother", wState.Holder))
		rep.Blockers = append(rep.Blockers, fmt.Sprintf("step-5: lease held by third-party %q; Acquire on ToRegion would fail", wState.Holder))
		return st
	}
	st.Notes = append(st.Notes, fmt.Sprintf("would Release(%q) then Acquire(%q, ttl=%s)", plan.FromRegion, plan.ToRegion, plan.LeaseTTL))
	st.PreconditionsMet = true
	_ = wState // silence unused
	_ = errors.New
	return st
}

func (s *Sequencer) dryStep6UncordonNew(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 6, Name: "uncordon-new-primary", EstimatedDurationSeconds: StepUncordonNewSeconds}
	if s.CNPG == nil {
		// Already blocked at step 2 — defensive note.
		st.Notes = append(st.Notes, "CNPG reader not configured (see step-2)")
		return st
	}
	_, _, err := s.CNPG.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err != nil {
		st.Notes = append(st.Notes, fmt.Sprintf("FindPair (re-check for uncordon): %v", err))
		// Already a blocker at step 2; don't double-count.
		return st
	}
	st.Notes = append(st.Notes, "would clear cordon + flip replica.enabled on both halves")
	st.PreconditionsMet = true
	return st
}

func (s *Sequencer) dryStep7Audit(ctx context.Context, plan SwitchoverPlan, rep *DryRunReport) DryRunStep {
	st := DryRunStep{Step: 7, Name: "audit-emit", EstimatedDurationSeconds: StepAuditEmitSeconds}
	if s.Audit == nil {
		st.Notes = append(st.Notes, "Audit publisher not configured")
		rep.Blockers = append(rep.Blockers, "step-7: Audit publisher is required")
		return st
	}
	st.Notes = append(st.Notes, fmt.Sprintf("would emit %s on subject catalyst.audit", "continuum-switchover"))
	st.PreconditionsMet = true
	return st
}

// dryRunWitnessReadOK is a tiny helper used by tests. NOT exported.
//
//nolint:unused // reserved for future use; keeping the seam stable
func dryRunWitnessReadOK(s witness.State) bool {
	return s.Holder != ""
}
