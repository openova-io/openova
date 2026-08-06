package main

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// defaultDrainBudget caps the HTTP drain.
//
// Kubernetes' default terminationGracePeriodSeconds is 30s. Overrunning it does
// not buy a cleaner exit — the kubelet just SIGKILLs, which is the abrupt kill
// this whole change exists to remove, only later. 25s leaves headroom for the
// orphan-release join that follows the drain.
const defaultDrainBudget = 25 * time.Second

// httpDrainer is the http.Server surface drainOnSignal needs. Narrowed to one
// method so a test can substitute a fake without standing up a real listener.
type httpDrainer interface {
	Shutdown(ctx context.Context) error
}

// orphanReleaseJoiner is the handler surface drainOnSignal needs.
type orphanReleaseJoiner interface {
	WaitOrphanReleases()
}

// drainOnSignal blocks until sigCh delivers, then shuts the HTTP server down
// and joins the in-flight orphan-release goroutines — in that order (#5767).
//
// The order is the substance of this function, not an implementation detail.
// Draining first and joining second is what makes the join terminate: past
// Shutdown no new request can arrive, so no new orphan-release work can be
// spawned. Joining first would race — a request landing mid-join spawns a
// goroutine the join has already passed, and we would exit while it is still
// writing to the store.
//
// Extracted from main() so the ordering is testable without delivering a real
// signal to the test process.
func drainOnSignal(
	sigCh <-chan os.Signal,
	srv httpDrainer,
	joiner orphanReleaseJoiner,
	budget time.Duration,
	log *slog.Logger,
) {
	sig := <-sigCh
	log.Info("shutdown signal received; draining", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		// Deliberately not fatal, and deliberately still followed by the
		// join. A drain that overran its budget leaves in-flight requests
		// cut, but an orphan-release goroutine killed mid-persistDeployment
		// is the worse outcome (#489 subdomain lock), so we still wait for
		// it rather than returning early on this error.
		log.Warn("http drain did not finish inside the budget; continuing to the orphan-release join",
			"err", err, "budget", budget.String())
	}

	joiner.WaitOrphanReleases()
	log.Info("drain complete — orphan PDM releases joined, no persist left in flight")
}
