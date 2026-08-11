// secondary_kubeconfig_secret_materializer.go — give the secondary-region
// credential Secret a producer that does not require a cutover.
//
// THE DEFECT (measured on hw293, dep a0077ba47e3720e5, both regions)
// ------------------------------------------------------------------
// The per-Org console surface answered roughly half of all fresh TCP
// connections. `console.<slug>.<parent>` resolves to one shared EIP whose
// ELB round-robins every region's envoy, so a host only region A can serve
// resets whatever share the pool sends to region B. Region B's
// `kube-system/cilium-gateway-console` carried ZERO per-Org listeners against
// five pairs in region A.
//
// The organization-controller writes those listeners into every region it can
// reach, and it reaches a secondary region through exactly one artefact:
//
//	Secret catalyst/cutover-secondary-kubeconfigs
//	  data: "<regionKey>.yaml" -> kubeconfig bytes
//
// On hw293 that Secret was NotFound in both regions. The controls prove the
// query was live: `catalyst` held 11 other Secrets, `kube-system` held the
// whole `clustermesh-apiserver-*-cert` family. It was that one object, absent.
//
// WHY IT WAS ABSENT — a structural property, not a failure
// --------------------------------------------------------
// materializeSecondaryKubeconfigsSecret had exactly ONE production call site:
// cutover.go, inside runCutover. Counted, not assumed — one, in one file. So
// the Secret came into existence only on a Sovereign that had already run the
// self-sovereignty cutover, and bp-self-sovereign-cutover installs DORMANT at
// bootstrap-kit slot 06a and is operator-gated by design. A 2-region Sovereign
// that has never cut over — which is every Sovereign, until an operator
// decides otherwise — therefore could not hold the credential for its own peer
// region. Nothing was broken; nothing had ever been asked to write it.
//
// That is the same shape as #6015 one layer down. There, both producers of the
// chroot's on-disk kubeconfigs were gated behind `finalStatus == "ready"`, so
// one dormant HelmRelease blinded the Sovereign to region B. #6015 gave
// DELIVERY its own level-triggered loop. This file does the same for the step
// that turns a delivered file into the in-cluster credential the controllers
// consume: a Sovereign that has a peer region gets a usable Secret for it as
// soon as the file lands, and keeps having one.
//
// ONE PRODUCER, TWO TRIGGERS
// --------------------------
// The Secret is still written by exactly one function — materializeSecondary-
// KubeconfigsSecret. Two producers of one object generate drift by
// construction, so nothing here re-implements the write: runCutover keeps its
// pre-flight call (with its fail-loud abort), and this loop calls the same
// function on an interval plus on a kick from the delivery endpoint. Every
// write stays idempotent and, since #6027's convergence check, a no-op once
// the object matches.
//
// WHAT SINGLE-REGION DOES
// -----------------------
// Nothing, loudly enough to be checkable. When the declared region count is 1,
// the shared function takes its `expected == 0` arm BEFORE it reads anything
// off disk: it reaps a stale Secret if one exists and otherwise writes nothing
// and returns (0, nil). Reading the spec first rather than the disk is what
// makes that arm reachable — a stray kubeconfig left over from a prior
// topology used to walk straight past the completeness check and materialize a
// region the Sovereign does not have. The loop reports "no secondary regions"
// and keeps ticking. A fix that always writes a Secret would be
// indistinguishable from the defect it claims to fix, so this is pinned by its
// own test.
//
// Refs #6015 #6027.
package handler

import (
	"context"
	"strconv"
	"time"
)

// secondaryKubeconfigSecretIntervalDefault — cadence of the level-triggered
// materializer. Faster than the 5-minute delivery loop because this side is a
// local read plus (in the converged case) a single Get: the cost of a pass is
// negligible and the reward is that a Sovereign converges soon after the
// delivery loop lands a file, even if the kick is missed across a restart.
const secondaryKubeconfigSecretIntervalDefault = 60 * time.Second

// secondaryKubeconfigSecretKickChan returns the coalescing kick channel,
// allocating it on first use. Capacity 1: a burst of deliveries collapses into
// one extra pass instead of queueing one pass per POST.
func (h *Handler) secondaryKubeconfigSecretKickChan() chan struct{} {
	h.secondaryKubeconfigSecretKickOnce.Do(func() {
		h.secondaryKubeconfigSecretKick = make(chan struct{}, 1)
	})
	return h.secondaryKubeconfigSecretKick
}

// kickSecondaryKubeconfigSecret asks the materializer for an immediate pass.
// Non-blocking and safe to call when no loop is running (the buffered slot
// simply stays full until one starts).
func (h *Handler) kickSecondaryKubeconfigSecret() {
	select {
	case h.secondaryKubeconfigSecretKickChan() <- struct{}{}:
	default:
	}
}

// RunSecondaryKubeconfigSecretMaterializer is the level-triggered producer of
// the secondary-region credential Secret. It runs on the chroot Sovereign only
// — the mothership holds no cutover namespace and serves no deployment record
// of its own — and returns when ctx is cancelled.
//
// Every pass delegates to materializeSecondaryKubeconfigsSecret, so the
// contract is that function's: a complete set of USABLE secondary kubeconfigs
// produces the Secret, an incomplete set produces nothing and says which
// region fell short, and a single-region Sovereign produces nothing at all.
// The difference here is only WHEN it is asked — every tick and on delivery,
// rather than once, if and when an operator fires a cutover.
//
// A shortfall is logged and retried, never fatal: the region-B file may simply
// not have been delivered yet, and this loop is the thing that notices when it
// arrives. runCutover keeps the hard abort, because starting the 11-step chain
// against a half-known topology is the #5359 false positive.
func (h *Handler) RunSecondaryKubeconfigSecretMaterializer(ctx context.Context) {
	if !isChroot() {
		h.log.Debug("secondary-kubeconfig-secret: materializer not started — SOVEREIGN_FQDN unset (mothership mode)")
		return
	}
	interval := h.secondaryKubeconfigSecretInterval
	if interval <= 0 {
		interval = secondaryKubeconfigSecretIntervalDefault
	}
	kick := h.secondaryKubeconfigSecretKickChan()

	h.log.Info("secondary-kubeconfig-secret: level-triggered materializer started — the per-Org console credential for a peer region no longer waits on a cutover (#6027)",
		"secret", cutoverSecondaryKubeconfigsSecretName(),
		"interval", interval.String(),
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		h.reconcileSecondaryKubeconfigsSecret(ctx)
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-ticker.C:
		}
	}
}

// reconcileSecondaryKubeconfigsSecret runs one pass. Exported only to the
// package so a test can drive a single pass deterministically instead of
// racing the loop's timer.
func (h *Handler) reconcileSecondaryKubeconfigsSecret(ctx context.Context) {
	deps, err := h.cutoverDepsFor()
	if err != nil || deps == nil {
		h.logSecondaryKubeconfigSecretState("deps-unavailable",
			func() {
				h.log.Warn("secondary-kubeconfig-secret: cannot build an in-cluster client this pass; retrying", "err", err)
			})
		return
	}
	n, merr := h.materializeSecondaryKubeconfigsSecret(ctx, deps)
	switch {
	case merr != nil:
		// Not fatal here. The most common cause is the one this loop exists
		// to wait out: the peer region's kubeconfig has not been delivered
		// (or is the credential-less shell #6054 now refuses at the door and
		// the read path now refuses off disk). Say which, once per change of
		// state, and try again next tick.
		h.logSecondaryKubeconfigSecretState("incomplete:"+merr.Error(),
			func() {
				h.log.Warn("secondary-kubeconfig-secret: the peer region(s) have no usable kubeconfig yet — the per-Org console listener cannot be written there until one lands; retrying",
					"secret", deps.ns+"/"+cutoverSecondaryKubeconfigsSecretName(),
					"detail", merr.Error())
			})
	case n == 0:
		h.logSecondaryKubeconfigSecretState("single-region",
			func() {
				h.log.Info("secondary-kubeconfig-secret: no secondary regions — no Secret is produced (single-region Sovereign; behaviour identical to before this loop existed)",
					"secret", deps.ns+"/"+cutoverSecondaryKubeconfigsSecretName())
			})
	default:
		h.logSecondaryKubeconfigSecretState("materialized:"+strconv.Itoa(n),
			func() {
				h.log.Info("secondary-kubeconfig-secret: peer-region credential materialized — the organization-controller can now write the per-Org console listener pair into every region (#6027)",
					"secret", deps.ns+"/"+cutoverSecondaryKubeconfigsSecretName(),
					"regions", n)
			})
	}
}

// logSecondaryKubeconfigSecretState emits `emit` only when the outcome differs
// from the previous pass. A converged Sovereign is quiet; a state change is
// always on the record. Without this a 60-second loop would print the same
// line 1440 times a day and bury the transition that matters.
func (h *Handler) logSecondaryKubeconfigSecretState(state string, emit func()) {
	h.secondaryKubeconfigSecretStateMu.Lock()
	changed := h.secondaryKubeconfigSecretState != state
	h.secondaryKubeconfigSecretState = state
	h.secondaryKubeconfigSecretStateMu.Unlock()
	if changed {
		emit()
	}
}

// secretDataEqualBytes compares two Secret data maps byte-for-byte. Used by
// the convergence check that keeps a level-triggered pass from rewriting an
// already-correct object.
func secretDataEqualBytes(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || string(av) != string(bv) {
			return false
		}
	}
	return true
}
