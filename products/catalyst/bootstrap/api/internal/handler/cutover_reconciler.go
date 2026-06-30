package handler

import (
	"context"
	"os"
	"time"
)

// #4635 — LEVEL-TRIGGERED sovereignty-cutover reconciler.
//
// The cutover used to be driven ONLY by edge-triggered events: the
// bp-self-sovereign-cutover Helm post-install hook (source="internal",
// fires once at chart-install) and the handover-seal path (handover.go's
// ReceiveTofuArchive fires the engine once, source="handover"). Both are
// fire-once. If either loses its timing race or the engine crashes/wedges
// mid-run, NOTHING re-fires it — the Sovereign sits sealed-but-uncut for
// hours until an unrelated chart roll happens to re-fire the upgrade hook
// (observed live on dep 8fd457a8: trigger 425'd, seal written 2 min later,
// cutover stuck at 0% for hours). That is non-deterministic.
//
// This reconciler closes the gap: it continuously, idempotently ensures the
// cutover engine is running whenever the cause (the handover seal) exists
// and the effect (the durable cutover-complete seal) does not — with NO
// give-up deadline. It is the level-triggered half that makes
// cutoverComplete a function of state, not of a one-shot event landing at
// exactly the right moment.
//
// Safety: spawnCutoverEngine is fully idempotent — it no-ops when the
// durable cutover-complete seal exists (#3667), when a run is already in
// flight on this Pod, and (for source="internal") when handover is not yet
// sealed. We fire with source="reconcile", which is NOT the internal arm,
// so the handover-gate is skipped — but runCutoverReconcilePass only fires
// AFTER confirming the seal exists itself, so the engine never half-pivots
// the registry pre-handover. The net effect of a redundant pass is a cheap
// status read + an "already running"/"already complete" no-op.

const (
	defaultCutoverReconcileInterval = 2 * time.Minute
	// Warmup so a freshly-(re)started Pod gives the handover-seal path in
	// ReceiveTofuArchive (which also fires the engine) a moment to run first
	// — avoiding a redundant double-fire on the very first tick. The
	// "already running" guard makes a double-fire harmless either way; this
	// just keeps the logs clean.
	cutoverReconcileWarmup = 90 * time.Second
)

// StartCutoverReconciler launches the level-triggered cutover reconciler in
// a background goroutine. Returns immediately; ctx cancellation stops the
// loop. Run from main.go after the handler + OpenBao client are wired.
//
// Gated to Sovereigns configured to auto-cut-over: when
// CATALYST_FIRE_CUTOVER_ON_HANDOVER=false (the #4061 default on a PERMANENT
// customer Sovereign, where the cutover is a deliberate operator action via
// the BSS CTA) the reconciler stays dormant — it would otherwise re-fire a
// cutover the operator has not chosen to start. Disable entirely with
// CATALYST_CUTOVER_RECONCILER_ENABLED=false.
func (h *Handler) StartCutoverReconciler(ctx context.Context) {
	if os.Getenv("CATALYST_CUTOVER_RECONCILER_ENABLED") == "false" {
		h.log.Info("[CUTOVER-RECONCILE] disabled via CATALYST_CUTOVER_RECONCILER_ENABLED=false")
		return
	}
	if !fireCutoverOnHandover() {
		h.log.Info("[CUTOVER-RECONCILE] dormant: fireCutoverOnHandover=false (operator-gated Sovereign; cutover fires via the BSS CTA, #4061)")
		return
	}

	interval := durationEnv("CATALYST_CUTOVER_RECONCILE_INTERVAL", defaultCutoverReconcileInterval)
	h.log.Info("[CUTOVER-RECONCILE] starting (level-triggered, no give-up)",
		"intervalSec", int(interval.Seconds()),
		"warmupSec", int(cutoverReconcileWarmup.Seconds()),
	)

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cutoverReconcileWarmup):
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			h.runCutoverReconcilePass(ctx)
			select {
			case <-ctx.Done():
				h.log.Info("[CUTOVER-RECONCILE] stopped")
				return
			case <-t.C:
			}
		}
	}()
}

// runCutoverReconcilePass performs one idempotent reconcile pass. Exposed
// for tests. The decision is pure state, never timing:
//
//	seal exists (handover done) && cutover-complete seal absent
//	    => ensure the engine is running (idempotent fire).
//
// Any read error fails closed (logs + returns) so a transient OpenBao blip
// never half-fires a cutover; the next tick retries — there is no give-up.
func (h *Handler) runCutoverReconcilePass(ctx context.Context) {
	// Cause: the Sovereign must have received the Phase-0 archive (handover
	// sealed). Pre-handover there is nothing to reconcile.
	sealed, err := h.sovereignHandoverComplete(ctx)
	if err != nil {
		h.log.Info("[CUTOVER-RECONCILE] handover-seal check failed; will retry next tick", "err", err.Error())
		return
	}
	if !sealed {
		return
	}

	// Effect: if the durable, revert-immune cutover-complete fact (#3667) is
	// already sealed, sovereignty is done — nothing to do.
	complete, err := h.sovereignCutoverComplete(ctx)
	if err != nil {
		h.log.Info("[CUTOVER-RECONCILE] cutover-complete check failed; will retry next tick", "err", err.Error())
		return
	}
	if complete {
		return
	}

	// Sealed-but-uncut: ensure the engine is running. cutoverDepsFor errors
	// on a Sovereign without the cutover chart installed — benign, skip.
	deps, derr := h.cutoverDepsFor()
	if derr != nil {
		return
	}
	res, ferr := h.fireCutoverEngine(ctx, deps, "reconcile")
	if ferr != nil {
		h.log.Error("[CUTOVER-RECONCILE] engine fire failed; will retry next tick", "err", ferr)
		return
	}
	// Only the spawn-started outcome means this pass actually (re)launched
	// the engine; already-running / already-complete are expected steady-state
	// no-ops and are not worth a log line every tick.
	if res.outcome == cutoverSpawnStarted {
		h.log.Info("[CUTOVER-RECONCILE] sealed-but-uncut → cutover engine (re)started", "outcome", res.outcome)
	}
}
