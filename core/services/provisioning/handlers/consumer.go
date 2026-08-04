package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// tenantSlugRE mirrors the DNS-subdomain safe range we allow on the marketplace
// side. Empty / mismatched values are rejected BEFORE we touch the generator,
// so we can never produce paths like `.../tenants//namespace.yaml` that GitHub
// rejects with HTTP 422 "tree.path contains a malformed path component".
// Issue #105.
var tenantSlugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}$`)

// validTenantSlug is true iff s is a safe, non-empty tenant slug. Used at
// every event-consumer boundary (order.placed, tenant.app_install_requested,
// tenant.app_uninstall_requested, tenant.deleted) to refuse malformed inputs
// before any git / k8s side effect.
func validTenantSlug(s string) bool {
	return tenantSlugRE.MatchString(s)
}

type orderPlacedData struct {
	TenantID  string   `json:"tenant_id"`
	OrderID   string   `json:"order_id"`
	PlanID    string   `json:"plan_id"`
	Apps      []string `json:"apps"`
	Subdomain string   `json:"subdomain"`
	// AppConfigs carries per-app configSchema values the customer
	// chose on the marketplace AppDetail surface (TBD-V18-D / PR #2038
	// — persisted on Tenant.AppConfigs by PR #2043). The billing
	// dispatcher enriches the order.placed payload by reading
	// Tenant.AppConfigs from the tenant service before publish
	// (TBD-V27 #2042). Keys are app SLUGs (e.g. "postgres"), values
	// are field-typed primitives keyed by ConfigField.Key
	// (e.g. {"replicas": 3, "disk_gb": 20, "backups_enabled": true}).
	// Nil/empty when the cart shipped no configSchema-bearing app or
	// the lookup failed — provisioning treats absence as "use
	// generator defaults" without erroring. Unknown keys (not in the
	// app's configSchema) are dropped with a warn log so a stale
	// frontend can't tunnel arbitrary YAML into the rendered manifest.
	AppConfigs map[string]map[string]any `json:"app_configs,omitempty"`
}

const topicProvisionEvents = "org.provision.events"

// staleProvisionTimeout (#3898) is how long an in-flight provision row may go
// without an updated_at refresh before the dedup collision path treats it as
// abandoned and reaps it. The workflow refreshes updated_at on every step and
// its longest single wait is 10 minutes (waitForHelmRelease / waitForVclusterApp),
// so 30 minutes leaves a comfortable margin: a healthy provision is never
// reaped, but a row stranded by a Pod crash/roll is recovered well before a
// human would notice the tenant wedged out of the funnel.
const staleProvisionTimeout = 30 * time.Minute

// StartConsumer listens for tenant + order events and routes them to the
// per-event handlers. The subscriber argument is whatever transport the
// service was wired with — on Sovereigns this is a NATS-backed
// MultiSubscriber (subjects `catalyst.tenant.created` /
// `catalyst.tenant.deleted` / `catalyst.tenant.app_{install,uninstall}_requested`
// / `catalyst.billing.order.placed`); on Catalyst-Zero it is the legacy
// Kafka *events.Consumer. Both satisfy events.BrokerSubscriber so the
// dispatch loop below is transport-agnostic.
func (h *Handler) StartConsumer(ctx context.Context, consumer events.BrokerSubscriber) error {
	slog.Info("starting provisioning event consumer")
	return consumer.Subscribe(ctx, func(event *events.Event) error {
		switch event.Type {
		case "order.placed":
			return h.handleOrderPlaced(ctx, event)
		case "tenant.created":
			// TBD-C16 (#1722): voucher-accept → Organization CR.
			// organization-controller fans out vCluster + Keycloak
			// group + Gitea org + UserAccess from the CR. Closes the
			// D29 zero-touch convergence gap that left orgs in a
			// stuck "tenant row exists, no controller artefacts" state.
			return h.handleTenantCreated(ctx, event)
		case "tenant.app_install_requested":
			return h.handleAppInstallRequested(ctx, event)
		case "tenant.app_uninstall_requested":
			return h.handleAppUninstallRequested(ctx, event)
		case "tenant.deleted":
			return h.handleTenantDeleted(ctx, event)
		default:
			slog.Debug("ignoring event", "type", event.Type)
		}
		return nil
	})
}

// day-2 events share a small shape; keeping them in one struct.
//
// IdempotencyKey is the per-click key the tenant service generates. The same
// key arrives via BOTH transports (HTTP and event bus) so provisioning dedups
// and runs the work once. See issue #71.
type appChangeData struct {
	TenantID       string            `json:"tenant_id"`
	TenantSlug     string            `json:"tenant_slug"`
	PlanID         string            `json:"plan_id"`
	AppSlug        string            `json:"app_slug"`
	AppID          string            `json:"app_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	DeployIDs      []string          `json:"deploy_ids"`
	DeploySlugs    []string          `json:"deploy_slugs"`
	DepChoices     map[string]string `json:"dep_choices"`
	Apps           []string          `json:"apps"` // final tenant.Apps IDs after the change
	// ParentDomain is the Org's chosen (already tenant-service-resolved) pool
	// zone (#4999). The per-Org apps generator renders app hosts under THIS zone
	// (via applyTenantChangePerOrg's scoped generator clone) so they match the
	// per-Org console's zone (console==apps invariant under the honored TLD).
	// Empty → the apps fall back to the Sovereign's primary apps pool, unchanged.
	ParentDomain string `json:"parent_domain"`
}

func (h *Handler) handleAppInstallRequested(ctx context.Context, event *events.Event) error {
	var data appChangeData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		slog.Error("failed to unmarshal app_install_requested data", "error", err)
		return err
	}
	if !validTenantSlug(data.TenantSlug) {
		slog.Error("app_install_requested rejected — invalid tenant_slug",
			"tenant_id", data.TenantID, "tenant_slug", data.TenantSlug)
		return nil // #105 — don't attempt git commit on blank/bad slug.
	}
	slog.Info("day-2 install requested",
		"tenant_id", data.TenantID,
		"tenant_slug", data.TenantSlug,
		"app_slug", data.AppSlug,
		"deploy_slugs", data.DeploySlugs,
	)
	return h.runInstallJob(ctx, data)
}

// runInstallJob is the shared implementation used by both the event consumer
// and the HTTP `/provisioning/apps/install` path. It creates a Job record the
// Jobs page renders, drives step 0 (git commit) synchronously so the consumer
// ACK is durable, then forks a goroutine for steps 1+ (pod-ready wait and
// terminal event publish). This keeps the event consumer from blocking up to
// 10 minutes per install — see issue #99 ("tenant.deleted stuck behind day-2
// install waits").
//
// Idempotency (issue #71): the tenant service produces ONE IdempotencyKey per
// click and carries it through both transports. newInstallJob uses
// store.CreateJobIfAbsent so whichever writer wins the race creates the Job;
// the loser gets ErrJobExists, newInstallJob returns nil, and we skip the
// work. Without this we'd double-commit the same git SHA twice.
//
// Preemption (issue #99): the async waiter registers its cancel func in
// h.day2Cancels under the tenant slug. handleTenantDeleted calls
// CancelAllFor(slug) at the top of teardown so in-flight waits for a doomed
// tenant stop immediately instead of polling a terminating namespace.
func (h *Handler) runInstallJob(ctx context.Context, data appChangeData) error {
	job := h.newInstallJob(ctx, data)
	if job == nil {
		// Duplicate dispatch — the sibling transport already claimed the job.
		slog.Info("day-2 install: duplicate dispatch ignored",
			"tenant_id", data.TenantID, "app_slug", data.AppSlug,
			"idempotency_key", data.IdempotencyKey)
		return nil
	}

	// Step 0 SYNC: commit manifests to Git. This is the actual durable state
	// change; once it returns, Flux will converge regardless of what happens
	// to this process afterwards.
	//
	// #4404 — the funnel cart dispatches this install the instant the Org CR
	// is created, which can BEAT organization-controller's per-Org Gitea
	// org/repo create. The commit then 404s ("user redirect does not exist
	// [name: <slug>]" / branch-missing). Because BOTH transports share one
	// idempotency Job, a failed first attempt poisons the key and the sibling
	// dispatch is suppressed as a duplicate — so the purchased app's manifest
	// is lost forever and the customer's console stays empty. Retry the commit
	// with bounded backoff while the failure is the transient Gitea-not-ready
	// race; any other error fails immediately as before.
	h.markJobStep(ctx, job, 0, "running", "")
	if err := h.commitInstallWithGiteaReadyRetry(ctx, data); err != nil {
		// #4404 SELF-HEAL — if the in-line budget exhausted while the per-Org
		// Gitea org/repo STILL did not exist (the transient race, just slower
		// than the in-line window), do NOT fail the Job. Failing it would drop
		// the purchased app forever: the failed attempt already claimed the
		// shared idempotency Job, so the sibling transport's dispatch is
		// suppressed as a duplicate and never retries. Park the install in the
		// pending-install registry; StartPendingInstallReconciler re-attempts
		// the commit on a cadence until the repo finally appears and the commit
		// lands — zero-touch, robust to ANY per-Org creation latency, not just a
		// bigger fixed budget. ctx-cancel (tenant deleted) is NOT a parkable
		// race; fall through to fail. Permanent errors fail immediately as before.
		if isGiteaNotReadyError(err) && ctx.Err() == nil {
			h.markJobStep(ctx, job, 0, "running",
				"per-Org Gitea repo not ready yet — awaiting org-controller (will retry automatically, #4404)")
			h.pendingInstalls.Enqueue(data, job)
			slog.Warn("day-2 install: in-line retry budget exhausted, parking for self-heal reconciler (#4404)",
				"tenant", data.TenantSlug, "app_slug", data.AppSlug,
				"idempotency_key", data.IdempotencyKey, "error", err.Error())
			return nil
		}
		h.markJobStep(ctx, job, 0, "failed", err.Error())
		h.finalizeJob(ctx, job, "failed", err.Error())
		h.publishEvent(ctx, "provision.app_failed", data.TenantID, map[string]any{
			"app_slug":   data.AppSlug,
			"app_id":     data.AppID,
			"deploy_ids": data.DeployIDs,
			"action":     "install",
			"error":      err.Error(),
		})
		// #5234 — a terminally-failed step-0 commit (e.g. a ref-race that
		// persisted through every CAS retry) means the purchased app can NEVER
		// deploy from this dispatch. Fail the Org's in-flight funnel provision
		// NOW — red "Deploying <app>" step + provision.failed → customer
		// status "failed" — instead of letting the timeline burn the full
		// 10-minute pod wait before surfacing anything.
		h.failActiveProvisionForCommitError(ctx, data, err)
		return err
	}
	h.markJobStep(ctx, job, 0, "completed", "ok")

	// Steps 1-2 ASYNC: pod-ready wait + terminal event publish.
	h.launchInstallWaitTail(data, job)
	return nil
}

// launchInstallWaitTail forks the async tail of an install (steps 1-2: pod-ready
// wait + terminal event publish) after the step-0 commit has landed. Detached
// from the caller ctx so the consumer ack can happen immediately; registered in
// day2Cancels so handleTenantDeleted can preempt. Shared by the synchronous
// runInstallJob path and the #4404 pending-install self-heal reconciler (which
// commits a parked install long after the original dispatch, then needs the same
// wait tail to drive the Job to a terminal state).
func (h *Handler) launchInstallWaitTail(data appChangeData, job *store.Job) {
	waitCtx, cancel := h.day2Cancels.Register(context.Background(), data.TenantSlug, job.ID)
	go func() {
		defer cancel()
		defer h.day2Cancels.Unregister(data.TenantSlug, job.ID)
		h.waitAndFinalizeInstall(waitCtx, data, job)
	}()
}

// #4404 — bound the in-line commit retry. The per-Org Gitea org/repo is created
// by organization-controller asynchronously after the Org CR, and the funnel
// cart dispatches the install the instant the CR exists. On a busy Sovereign the
// org-controller's Gitea-Org-creation latency can run well past the old ~25s
// budget (6 × 5s) — a live #4404 close-walk saw the repo still not-yet-queryable
// past that window — so the in-line retry exhausted and the install was dropped.
//
// Widen the in-line budget to a multi-minute exponential backoff so the common
// case (repo appears within a couple of minutes) commits without ever needing
// the durable self-heal. The backoff starts at day2GiteaReadyRetryInitialDelay,
// doubles each attempt up to day2GiteaReadyRetryMaxDelay, and stops once the
// cumulative wait would exceed day2GiteaReadyRetryBudget. The slower tail (repo
// takes even longer, or this pod restarts) is caught by the pending-install
// self-heal reconciler, so no latency drops the install. Vars (not consts) so
// tests can drive the delays to near-zero.
var (
	day2GiteaReadyRetryBudget       = 3 * time.Minute
	day2GiteaReadyRetryInitialDelay = 3 * time.Second
	day2GiteaReadyRetryMaxDelay     = 20 * time.Second
)

// commitInstallWithGiteaReadyRetry runs applyTenantChange(install) and retries
// with bounded exponential backoff while the only thing wrong is that the
// per-Org Gitea org/repo does not exist yet (#4404 — the funnel cart races the
// organization-controller's repo create). On any non-transient error, or once
// the cumulative wait budget is exhausted, it returns the last error so the
// caller can park the install for the self-heal reconciler (still-transient) or
// fail the Job (permanent). ctx cancellation aborts the wait immediately.
func (h *Handler) commitInstallWithGiteaReadyRetry(ctx context.Context, data appChangeData) error {
	var (
		err     error
		elapsed time.Duration
		delay   = day2GiteaReadyRetryInitialDelay
	)
	for attempt := 1; ; attempt++ {
		err = h.applyTenantChange(ctx, data, "install")
		if err == nil {
			return nil
		}
		// A non-transient error fails immediately, as before. A transient
		// Gitea-not-ready error retries until the cumulative wait budget is
		// spent, then returns the last error (the caller parks it for the
		// self-heal reconciler).
		if !isGiteaNotReadyError(err) || elapsed+delay > day2GiteaReadyRetryBudget {
			return err
		}
		slog.Warn("day-2 install: per-Org Gitea repo not ready yet — retrying commit (#4404)",
			"tenant", data.TenantSlug, "app_slug", data.AppSlug,
			"attempt", attempt, "elapsed", elapsed.String(),
			"next_delay", delay.String(), "budget", day2GiteaReadyRetryBudget.String(),
			"error", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		elapsed += delay
		if delay < day2GiteaReadyRetryMaxDelay {
			delay *= 2
			if delay > day2GiteaReadyRetryMaxDelay {
				delay = day2GiteaReadyRetryMaxDelay
			}
		}
	}
}

// isGiteaNotReadyError reports whether the commit error is the transient
// "per-Org Gitea org/repo does not exist yet" race (#4404) — distinct from a
// permanent failure (bad manifest, auth, malformed slug). Gitea surfaces the
// not-yet-created org as a 404 "user redirect does not exist [name: <slug>]" on
// the ref read, and getRef also reports a missing branch the same way. Both are
// safe to retry: organization-controller creates the repo seconds later.
func isGiteaNotReadyError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "user redirect does not exist") ||
		strings.Contains(s, "user does not exist") ||
		strings.Contains(s, " 404 ") ||
		strings.Contains(s, "Not Found") ||
		strings.Contains(s, "auto-create branch")
}

// waitAndFinalizeInstall runs the async tail of an install job: wait for each
// deployed pod to become Ready, then publish provision.app_ready (or
// provision.app_failed on pod-timeout). Respects waitCtx cancellation —
// when a tenant.deleted preempt fires, we log + mark the job failed without
// publishing provision.app_failed (the tenant is being torn down, a failure
// notification would be noise).
func (h *Handler) waitAndFinalizeInstall(ctx context.Context, data appChangeData, job *store.Job) {
	h.markJobStep(ctx, job, 1, "running", "")
	// #4290: org-controller-owned `<slug>` host namespace (single boundary).
	hostNS := data.TenantSlug
	// #4297 TIER GATE — vcluster tiers (M+) sync app pods up to the host ns
	// with the `-x-apps-x-vcluster` suffix; host tiers (free/S) run them with
	// native names in the host ns. Match the right shape.
	//
	// #4293 MAJOR-3 — resolve the tier AUTHORITATIVELY off the Organization CR
	// (immune to a transient catalog blip) so the pod-name matcher tracks the
	// boundary the manifests were actually committed against, not a catalog-miss
	// "s" downgrade. (This wait is for the matcher only; the manifests were
	// already committed authoritatively by applyTenantChange, which fails-closed.)
	planSlug, _ := h.resolveTenantPlanSlug(ctx, data.TenantSlug, data.PlanID)
	isVcluster := gitops.BoundaryIsVcluster(planSlug)
	waitSlugs := data.DeploySlugs
	if len(waitSlugs) == 0 {
		waitSlugs = []string{data.AppSlug}
	}
	for _, slug := range waitSlugs {
		if err := h.waitForVclusterApp(ctx, hostNS, slug, 10*time.Minute, isVcluster); err != nil {
			if ctx.Err() != nil {
				slog.Warn("day-2 install: wait canceled — tenant delete preempted",
					"tenant", data.TenantSlug, "app", slug, "job_id", job.ID)
				h.markJobStep(ctx, job, 1, "failed", "canceled: tenant deletion preempted wait")
				h.finalizeJob(ctx, job, "failed", "canceled: tenant deletion preempted wait")
				return
			}
			slog.Error("day-2 install: pod not ready", "tenant", data.TenantSlug, "app", slug, "error", err)
			h.markJobStep(ctx, job, 1, "failed", err.Error())
			h.finalizeJob(ctx, job, "failed", err.Error())
			h.publishEvent(ctx, "provision.app_failed", data.TenantID, map[string]any{
				"app_slug":   slug,
				"app_id":     data.AppID,
				"deploy_ids": data.DeployIDs,
				"action":     "install",
				"error":      err.Error(),
			})
			return
		}
	}
	h.markJobStep(ctx, job, 1, "completed", "ok")
	h.markJobStep(ctx, job, 2, "completed", "ok")
	job.Status = "succeeded"
	h.finalizeJob(ctx, job, "succeeded", "")
	h.publishEvent(ctx, "provision.app_ready", data.TenantID, map[string]any{
		"app_slug":   data.AppSlug,
		"app_id":     data.AppID,
		"deploy_ids": data.DeployIDs,
		"action":     "install",
	})
	// PR #1644 follow-up — patch Organization.spec.tenantPublic with
	// the freshly-installed product. Day-2 installs use the picked
	// app_slug; the patch overwrites any prior product binding so the
	// HTTPRoute always points at the most recently installed product.
	// hostNS computed above ("tenant-<slug>"). Best-effort.
	h.emitOrgTenantPublic(ctx, data.TenantSlug, data.AppSlug, hostNS)

	slog.Info("day-2 install completed (async)", "tenant", data.TenantSlug, "app", data.AppSlug)
}

func (h *Handler) handleAppUninstallRequested(ctx context.Context, event *events.Event) error {
	var data appChangeData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		slog.Error("failed to unmarshal app_uninstall_requested data", "error", err)
		return err
	}
	if !validTenantSlug(data.TenantSlug) {
		slog.Error("app_uninstall_requested rejected — invalid tenant_slug",
			"tenant_id", data.TenantID, "tenant_slug", data.TenantSlug)
		return nil // #105
	}
	slog.Info("day-2 uninstall requested",
		"tenant_id", data.TenantID,
		"tenant_slug", data.TenantSlug,
		"app_slug", data.AppSlug,
	)
	return h.runUninstallJob(ctx, data)
}

// runUninstallJob is the shared implementation used by both the event
// consumer and the HTTP `/provisioning/apps/uninstall` path. Same split as
// runInstallJob after issue #99: step 0 (git prune) runs synchronously so
// consumer ACK is durable, step 1 (Flux-gone wait) and step 2 (terminal
// event publish) run in a cancellable goroutine registered with day2Cancels.
//
// Same dedup contract as runInstallJob — first writer wins on
// IdempotencyKey, runner-up logs + returns without re-running. See #71.
func (h *Handler) runUninstallJob(ctx context.Context, data appChangeData) error {
	purged, retained := h.computePurgeRetention(ctx, data)
	job := h.newUninstallJob(ctx, data, purged, retained)
	if job == nil {
		slog.Info("day-2 uninstall: duplicate dispatch ignored",
			"tenant_id", data.TenantID, "app_slug", data.AppSlug,
			"idempotency_key", data.IdempotencyKey)
		return nil
	}

	// Step 0 SYNC: prune manifests from Git.
	h.markJobStep(ctx, job, 0, "running", "")
	if err := h.applyTenantChange(ctx, data, "uninstall"); err != nil {
		h.markJobStep(ctx, job, 0, "failed", err.Error())
		h.finalizeJob(ctx, job, "failed", err.Error())
		h.publishEvent(ctx, "provision.app_failed", data.TenantID, map[string]any{
			"app_slug": data.AppSlug,
			"app_id":   data.AppID,
			"action":   "uninstall",
			"error":    err.Error(),
		})
		return err
	}
	h.markJobStep(ctx, job, 0, "completed", "ok")

	// Steps 1-2 ASYNC: pod-gone wait + terminal event publish.
	waitCtx, cancel := h.day2Cancels.Register(context.Background(), data.TenantSlug, job.ID)
	go func() {
		defer cancel()
		defer h.day2Cancels.Unregister(data.TenantSlug, job.ID)
		h.waitAndFinalizeUninstall(waitCtx, data, job)
	}()
	return nil
}

// waitAndFinalizeUninstall runs the async tail of an uninstall job: wait for
// Flux to remove the pod from the tenant vcluster, then publish
// provision.app_removed. Slow-prune is not a failure — we log and still
// publish removed, matching prior behavior. Preempt (ctx.Err()) short-circuits
// without publishing anything.
func (h *Handler) waitAndFinalizeUninstall(ctx context.Context, data appChangeData, job *store.Job) {
	h.markJobStep(ctx, job, 1, "running", "")
	// #4290: org-controller-owned `<slug>` host namespace (single boundary).
	hostNS := data.TenantSlug
	// #4293 MINOR-4 — tier-gate the uninstall wait so a host-tier (native pod
	// name) Org isn't reported "gone" while its pod is still Terminating.
	// Authoritative tier off the Organization CR (immune to a catalog blip).
	planSlug, _ := h.resolveTenantPlanSlug(ctx, data.TenantSlug, data.PlanID)
	isVcluster := gitops.BoundaryIsVcluster(planSlug)
	if err := h.waitForVclusterAppGone(ctx, hostNS, data.AppSlug, 5*time.Minute, isVcluster); err != nil {
		if ctx.Err() != nil {
			slog.Warn("day-2 uninstall: wait canceled — tenant delete preempted",
				"tenant", data.TenantSlug, "app", data.AppSlug, "job_id", job.ID)
			h.markJobStep(ctx, job, 1, "failed", "canceled: tenant deletion preempted wait")
			h.finalizeJob(ctx, job, "failed", "canceled: tenant deletion preempted wait")
			return
		}
		slog.Warn("day-2 uninstall: pod still present after wait — Flux will continue reconciling",
			"tenant", data.TenantSlug, "app", data.AppSlug, "error", err)
		// Don't fail the job — Flux will continue reconciling and pods go away
		// shortly after. The UI already reflects "removed" once this step
		// completes; users don't need to block on a slow prune.
		h.markJobStep(ctx, job, 1, "completed", "still reconciling")
	} else {
		h.markJobStep(ctx, job, 1, "completed", "ok")
	}
	h.markJobStep(ctx, job, 2, "completed", "ok")
	job.Status = "succeeded"
	h.finalizeJob(ctx, job, "succeeded", "")
	h.publishEvent(ctx, "provision.app_removed", data.TenantID, map[string]any{
		"app_slug": data.AppSlug,
		"app_id":   data.AppID,
		"action":   "uninstall",
	})
	slog.Info("day-2 uninstall completed (async)", "tenant", data.TenantSlug, "app", data.AppSlug)
}

// computePurgeRetention classifies the to-be-uninstalled app's backing
// services into (purged, retained). A backing service is RETAINED if any
// other app that remains installed also depends on it. Otherwise its data
// goes away with this uninstall. Uses the catalog to walk dependencies.
//
// data.Apps is the post-uninstall final tenant.Apps list (canonical IDs —
// the target's ID was filtered out by the tenant service before emitting).
//
// #89: keyed by ID throughout. Returned slices hold dep slugs (the stable
// label for the purge-preview UI + retention events). We read the
// target's dependencies from the same response, so there's no second
// catalog round-trip.
func (h *Handler) computePurgeRetention(ctx context.Context, data appChangeData) (purged, retained []string) {
	apps, ok := h.fetchCatalogApps(ctx)
	if !ok {
		return nil, nil
	}
	byID := make(map[string]catalogAppResp, len(apps))
	var target *catalogAppResp
	for i := range apps {
		byID[apps[i].ID] = apps[i]
		if apps[i].ID == data.AppID {
			target = &apps[i]
		}
	}
	if target == nil || len(target.Dependencies) == 0 {
		return nil, nil
	}

	// Count post-uninstall dependency references per dep slug. We walk
	// Dependencies (slugs) here because the set we return is compared
	// against target.Dependencies (also slugs); staying in one namespace
	// keeps the comparison cheap.
	depCount := map[string]int{}
	for _, appID := range data.Apps {
		a, ok := byID[appID]
		if !ok {
			continue
		}
		for _, d := range a.Dependencies {
			depCount[d]++
		}
	}
	for _, dep := range target.Dependencies {
		if depCount[dep] > 0 {
			retained = append(retained, dep)
		} else {
			purged = append(purged, dep)
		}
	}
	return purged, retained
}

// applyTenantChange regenerates the tenant's manifests from the final app list
// carried on the event, preserves the DB password by reading the existing
// db-*.yaml from Git, and commits the new tree. Used by both install and
// uninstall (the tenant service already persisted the final tenant.Apps
// before emitting the event, so regeneration is symmetric).
func (h *Handler) applyTenantChange(ctx context.Context, data appChangeData, action string) error {
	if h.GitHubClient == nil {
		return fmt.Errorf("GitHub client not configured")
	}
	if !validTenantSlug(data.TenantSlug) {
		return fmt.Errorf("event has missing or malformed tenant_slug %q (issue #105)", data.TenantSlug)
	}
	if h.Generator == nil {
		return fmt.Errorf("manifest generator not configured")
	}

	// #4293 MAJOR-3 — fail-CLOSED tier resolution on the day-2 re-generation
	// path. resolvePlanSlug returns "s" on ANY transient catalog failure; if
	// that fired here for a paid (M+) Org we'd re-emit apps-sync WITHOUT
	// kubeConfig (host tier) and Flux would RE-ROUTE the apps from the vcluster
	// into the host `<slug>` ns, orphaning the in-vcluster copy. resolveTenantPlanSlug
	// reads the AUTHORITATIVE `spec.planSlug` off the Organization CR first
	// (immune to a catalog blip); ok=false means it could only guess (catalog
	// unreachable AND no CR planSlug) — in that case ABORT the day-2 change so
	// the job retries rather than silently downgrading the boundary.
	planSlug, authoritative := h.resolveTenantPlanSlug(ctx, data.TenantSlug, data.PlanID)
	if !authoritative {
		return fmt.Errorf("day-2 %s aborted: could not authoritatively resolve plan tier for tenant %q (catalog unreachable + no persisted Organization.spec.planSlug) — refusing to re-generate manifests that could re-route apps to the wrong boundary (#4293 MAJOR-3); will retry", action, data.TenantSlug)
	}
	appSlugs := h.resolveAppSlugs(ctx, data.Apps)

	branch := h.GitBranch
	if branch == "" {
		branch = "main"
	}

	// #4384 — on a Sovereign the customer's purchased Applications belong in
	// the per-Org `<slug>/catalyst-tenant` repo's `vcluster/apps/` tree (the
	// one the org-controller bootstrapped + wired a Flux Kustomization for),
	// NOT the globally-configured catalog repo (openova/openova). Retarget the
	// commit there. The legacy contabo per-tenant overlay path keeps using the
	// global repo + tenant-dir tree below.
	if h.PerOrgGitops {
		return h.applyTenantChangePerOrg(ctx, data, action, planSlug, appSlugs, branch)
	}

	// Preserve the existing DB password if a db file is present in Git.
	// Without this, regenerating the Secret mints a fresh password and the
	// running DB pods keep the old one — apps would fail to connect.
	dbPassword := h.readExistingDBPassword(ctx, data.TenantSlug)
	if dbPassword == "" {
		slog.Info("day-2: no existing DB password found in Git — generating fresh (first DB install)",
			"tenant", data.TenantSlug, "action", action)
	}

	manifests := h.Generator.GenerateAllWithPassword(data.TenantSlug, planSlug, appSlugs, dbPassword)
	if len(manifests) == 0 {
		return fmt.Errorf("manifest generation produced no files")
	}

	// Prune orphan files in the tenant dir so uninstalled apps don't linger
	// (Flux would otherwise keep reconciling `app-ghost.yaml` forever).
	tenantDir := h.Generator.TenantDir(data.TenantSlug) + "/"
	prunePrefixes := []string{tenantDir}

	// Issue #944 cross-cluster pollution guard — defence in depth on top
	// of main.go's startup validation. Refuses to commit into a foreign
	// cluster's tree even if a runtime env mutation slipped past startup.
	if err := h.VerifyCommitTargetSafe(tenantDir); err != nil {
		return fmt.Errorf("commit guard: %w", err)
	}

	commitMsg := fmt.Sprintf("day-2: %s %s on tenant %s (apps: %d)",
		action, data.AppSlug, data.TenantSlug, len(appSlugs))
	if err := h.GitHubClient.CommitFilesWithPrune(ctx, branch, commitMsg, manifests, prunePrefixes); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	slog.Info("day-2: committed tenant manifests",
		"tenant", data.TenantSlug, "action", action, "apps", len(appSlugs))
	return nil
}

// perOrgRepoName returns the per-Org Gitea repo the funnel cart install
// targets (matches the org-controller's `catalyst-tenant` constant). Operator-
// overridable via TENANT_GITOPS_REPO; defaults "catalyst-tenant".
func (h *Handler) perOrgRepoName() string {
	if strings.TrimSpace(h.PerOrgRepoName) != "" {
		return h.PerOrgRepoName
	}
	return "catalyst-tenant"
}

// perOrgBranch returns the branch the per-Org `<slug>/catalyst-tenant` repo
// commits land on. Defaults "main" (the org-controller's per-Org Flux
// GitRepository ref.branch) — explicitly NOT the global GitBranch.
func (h *Handler) perOrgBranch() string {
	if strings.TrimSpace(h.PerOrgBranch) != "" {
		return h.PerOrgBranch
	}
	return "main"
}

// applyTenantChangePerOrg is the Sovereign per-Org commit path (#4384). It
// renders ONLY the customer's app payload, re-roots it into `vcluster/apps/`,
// and commits to the per-Org `<slug>/catalyst-tenant` repo — the tree the
// org-controller's `catalyst-tenant-<slug>-apps` Flux Kustomization reconciles
// into the host `<slug>` namespace. The funnel-owned app files are pruned (so
// an uninstall removes them) WITHOUT touching the org-controller's boundary
// baseline (networkpolicy.yaml / ciliumnetworkpolicy.yaml), which is preserved
// in the merged kustomization.
//
// #5234 — the commit is a genuine per-attempt compare-and-swap. The
// org-controller writes the SAME repo concurrently (one PutFile commit per
// rendered manifest, per reconcile), so the funnel's batch commit routinely
// loses the ref CAS mid-burst. The file map — including the read-modify-write
// merges of both kustomization indexes — used to be built ONCE before the
// retry loop, so every retry re-pushed content based on a STALE base read
// (the exact #1031 defect class, now against the org-controller's #5104
// merge-writes). The build now runs inside a rebuild closure the client
// invokes at the start of EVERY attempt: each retry re-reads the remote
// head's kustomization indexes / tree listings / DB password and re-merges
// onto them before re-pushing with fresh per-file SHA preconditions.
func (h *Handler) applyTenantChangePerOrg(ctx context.Context, data appChangeData, action, planSlug string, appSlugs []string, _ string) error {
	slug := data.TenantSlug
	repoClient := h.GitHubClient.WithRepo(slug, h.perOrgRepoName())
	// The per-Org repo tracks `main`, NOT the global GitBranch (`org-tenants`
	// on a Sovereign). Commit to the branch the org-controller's per-Org Flux
	// GitRepository actually watches.
	branch := h.perOrgBranch()

	// #5387 — hold the per-branch gate for the WHOLE read-merge-commit cycle.
	// A cart with N Applications dispatches N concurrent installs (one per cart
	// entry, each in its own goroutine), and every one of them re-renders the
	// full cart and pushes it to this same branch. Un-gated they collide on the
	// branch head and Gitea rejects the losers with `PushOutOfDate`, which on
	// hw290 failed the funnel outright. Serialising them makes each commit build
	// on the head its sibling just wrote, and leaves the whole CAS-retry budget
	// for the genuinely out-of-process racer (the org-controller). The gate is
	// released before we return, on every path.
	release, err := h.perOrgCommits.acquire(ctx, slug+"/"+h.perOrgRepoName()+"@"+branch)
	if err != nil {
		return fmt.Errorf("per-Org commit gate for %s/%s@%s: %w", slug, h.perOrgRepoName(), branch, err)
	}
	defer release()

	// #4999: render the per-Org app hosts under the Org's CHOSEN (served) pool
	// zone — the SAME zone resolveOrgParentDomain stamps on the Org CR's
	// spec.tenantPublic.parentDomain (which drives the per-Org console DNS / TLS /
	// route). Without this the apps render under the Sovereign's PRIMARY apps pool
	// while the console lives on the honored TLD, so `<app>.<slug>.<primary>` has
	// no per-Org A-record and dead-IPs on a stale apex wildcard (the #4421
	// failure). Re-resolve the event's parent_domain through the SAME resolver the
	// Org CR used, then render on a scoped generator clone — ManifestGenerator is
	// read-only during render, so a shallow copy with an overridden ParentDomain
	// is safe and confined to this Org. An empty/unserved pick resolves to the
	// primary apps pool → byte-identical to pre-#4999 output (no regression).
	gen := h.Generator
	if zone := resolveOrgParentDomain(h.AppsParentDomain, h.PoolDomains, data.ParentDomain); zone != "" {
		clone := *h.Generator
		clone.ParentDomain = zone
		gen = &clone
	}

	// buildPerOrgFiles assembles the COMPLETE per-Org commit payload from the
	// remote head's CURRENT state. Called before the commit (for the
	// empty-payload early return, via the returned payload count) and again on
	// every ref-race retry (#5234) so a retry never replays a merge computed
	// against a superseded head.
	buildPerOrgFiles := func(ctx context.Context) (map[string]string, int, error) {
		// Preserve the existing DB password if the per-Org repo already carries a
		// db Secret (so a second cart install / day-2 change keeps the running DB
		// password). Read from the SAME per-Org tree we write to — re-read per
		// attempt so a concurrent install's freshly-minted password is honored.
		dbPassword := readDBPasswordFromTree(ctx, repoClient, branch, gitops.PerOrgAppsDir)
		if dbPassword == "" {
			slog.Info("day-2: no existing DB password found in per-Org repo — generating fresh (first DB install)",
				"tenant", slug, "action", action, "repo", slug+"/"+h.perOrgRepoName())
		}

		appFiles, appDocs := gen.GeneratePerOrgAppsTree(slug, planSlug, appSlugs, dbPassword)
		payloadCount := len(appFiles)
		if appFiles == nil {
			appFiles = map[string]string{}
		}

		// Merge the new app docs into the org-controller's vcluster/apps
		// kustomization (read-modify-write so the NP/CNP baseline + any prior
		// cart apps survive). On uninstall, the rebuilt list reflects ONLY the
		// surviving app docs (appDocs no longer lists the removed app) plus the
		// baseline. Empty existing → MergePerOrgAppsKustomization seeds the
		// baseline + the current docs.
		//
		// #5104 — the merge is plan-aware AND tree-derived: it force-includes the
		// org-controller baseline for this plan (networkpolicy.yaml; plus the #4992
		// vcluster target-ns namespace.yaml for the vcluster tier) and every
		// baseline-shaped doc ACTUALLY committed under vcluster/apps/ (best-effort
		// dir listing — nil on error, the plan-aware list still covers the known
		// docs). Before this, the merge knew only networkpolicy.yaml, so the funnel
		// index dropped namespace.yaml → the target ns never existed inside the
		// vcluster → the apps Flux Kustomization wedged on `namespaces "<slug>"
		// not found` and the purchased app never deployed (2/2 Orgs, hw255).
		kustPath := gitops.PerOrgAppsDir + "/kustomization.yaml"
		existingKust := ""
		if content, err := repoClient.ReadFile(ctx, branch, kustPath); err == nil {
			existingKust = content
		}
		appsTreeDocs, err := repoClient.ListDir(ctx, branch, gitops.PerOrgAppsDir)
		if err != nil {
			appsTreeDocs = nil // best-effort — plan-aware baseline still applies
		}
		if action == "uninstall" {
			// Rebuild from the baseline + the SURVIVING app docs only, so the
			// removed app is dropped from resources (a plain merge would preserve
			// it because it's still listed in the existing kustomization).
			appFiles[kustPath] = gitops.MergePerOrgAppsKustomization("", planSlug, appsTreeDocs, appDocs)
		} else {
			appFiles[kustPath] = gitops.MergePerOrgAppsKustomization(existingKust, planSlug, appsTreeDocs, appDocs)
		}

		// #4993 — for the VCLUSTER tier, also emit the HOST-NATIVE per-app HTTPRoutes
		// into vcluster/host-apps/. The app's own HTTPRoute is co-located INSIDE the
		// vcluster (apps tree, kubeConfig-targeted) but loft vcluster 0.33.4 registers
		// no httproute reflecting controller, so it never reaches the host Cilium
		// Gateway → 404 with pods Running. The host-native route binds the SYNCED
		// Service (<app>-x-<slug>-x-vcluster) on the host `<slug>` ns directly — the
		// same host-native model the per-Org console route uses. The org-controller
		// SEEDS vcluster/host-apps/kustomization.yaml once and never clobbers it, so
		// merge our route docs in (preserving the CNP + provisioning-rbac baseline),
		// exactly as the apps kustomization above. Host-tier Orgs route via the
		// co-located generateAppHTTPRoute (plain Service in the host ns), so
		// GeneratePerOrgHostAppRoutes returns nothing and this block is a no-op.
		if gitops.BoundaryIsVcluster(planSlug) {
			hostRouteFiles, hostAppDocs := gen.GeneratePerOrgHostAppRoutes(slug, planSlug, appSlugs)
			for p, c := range hostRouteFiles {
				appFiles[p] = c
			}
			// #5423 — on this tier GeneratePerOrgAppsTree has already re-rooted
			// the HelmRelease-shaped app files (openclaw / stalwart-mail /
			// newapi) into vcluster/host-apps/, because the apps Kustomization
			// is kubeConfig-targeted at the Org vcluster, which registers no
			// Flux CRDs — a HelmRelease doc there fails dry-run and Flux aborts
			// the ENTIRE Kustomization, so the customer's plain Deployment app
			// and its DB never applied either (hw290 rows 86/90/233/234).
			// Index them here so kustomize actually builds them; on uninstall
			// the rebuilt list carries only the surviving HR apps, which prunes
			// the removed one exactly like the route docs above.
			hostAppDocs = append(hostAppDocs, gitops.PerOrgHostHelmReleaseAppDocs(planSlug, appSlugs)...)
			hostKustPath := gitops.PerOrgHostAppsDir + "/kustomization.yaml"
			existingHostKust := ""
			if content, err := repoClient.ReadFile(ctx, branch, hostKustPath); err == nil {
				existingHostKust = content
			}
			// #5104 — same tree-derived baseline preservation as the apps merge
			// above: any baseline-shaped doc the org-controller committed under
			// vcluster/host-apps/ survives the merge even if this build predates it.
			hostTreeDocs, err := repoClient.ListDir(ctx, branch, gitops.PerOrgHostAppsDir)
			if err != nil {
				hostTreeDocs = nil // best-effort
			}
			if action == "uninstall" {
				// Rebuild from baseline + SURVIVING route docs only (drops the
				// removed app's route from the index).
				appFiles[hostKustPath] = gitops.MergePerOrgHostAppsKustomization("", hostTreeDocs, hostAppDocs)
			} else {
				appFiles[hostKustPath] = gitops.MergePerOrgHostAppsKustomization(existingHostKust, hostTreeDocs, hostAppDocs)
			}
		}
		return appFiles, payloadCount, nil
	}

	// First build runs BEFORE the commit loop so the empty-payload install
	// stays a clean no-commit no-op (e.g. the cart held only HR-overlay apps
	// the generic generator can't emit, or an empty cart). On an UNINSTALL we
	// still fall through to prune the stale file + rebuild the kustomization.
	// The app payload is deterministic per (slug, plan, apps), so emptiness
	// cannot change across retries — only the merge inputs can.
	firstFiles, payloadCount, err := buildPerOrgFiles(ctx)
	if err != nil {
		return err
	}
	if payloadCount == 0 && action == "install" {
		slog.Info("day-2 (per-Org): no deployable app payload to commit",
			"tenant", slug, "action", action, "apps", len(appSlugs))
		return nil
	}

	// Prune scope: ONLY the funnel-owned app docs (so uninstalled apps are
	// removed), never the whole vcluster/apps tree — pruning the org-controller
	// baseline (networkpolicy.yaml / ciliumnetworkpolicy.yaml) would tear down
	// intra-Org isolation. CommitFilesWithPrune deletes blobs under a prune
	// prefix that are NOT in the committed file set; we re-commit every funnel
	// app doc each render, so a removed app's stale file is the only thing
	// pruned. Scope the prefix to app-/db- files under the apps dir + the
	// host-native app-route files under host-apps (#4993) — the org-controller
	// baseline there (ciliumnetworkpolicy.yaml / provisioning-rbac.yaml) does NOT
	// start with `app-`, so it is never pruned.
	prunePrefixes := []string{
		gitops.PerOrgAppsDir + "/app-",
		gitops.PerOrgAppsDir + "/db-",
		gitops.PerOrgHostAppsDir + "/app-",
	}

	// Rebuild-per-attempt (#5234): attempt 1 reuses the map built above (no
	// double remote read on the happy path); every RETRY re-runs the full
	// build against the freshly-moved head before re-pushing.
	firstAttempt := true
	rebuild := func(ctx context.Context) (map[string]string, error) {
		if firstAttempt {
			firstAttempt = false
			return firstFiles, nil
		}
		files, _, buildErr := buildPerOrgFiles(ctx)
		return files, buildErr
	}

	commitMsg := fmt.Sprintf("day-2: %s %s on Org %s (apps: %d) → vcluster/apps",
		action, data.AppSlug, slug, len(appSlugs))
	if err := repoClient.CommitFilesWithPruneAndRebuild(ctx, branch, commitMsg, nil, prunePrefixes, rebuild); err != nil {
		return fmt.Errorf("commit to per-Org repo %s/%s: %w", slug, h.perOrgRepoName(), err)
	}
	slog.Info("day-2 (per-Org): committed app manifests to per-Org repo",
		"tenant", slug, "action", action, "apps", len(appSlugs),
		"repo", slug+"/"+h.perOrgRepoName(), "branch", branch)
	return nil
}

// readDBPasswordFromTree reads db-postgres.yaml / db-mysql.yaml from the given
// dir in the given repo client + branch and extracts the DB password, "" if
// neither exists (first install) or it can't be parsed.
func readDBPasswordFromTree(ctx context.Context, client *ghclient.Client, branch, dir string) string {
	for _, name := range []string{"db-postgres.yaml", "db-mysql.yaml"} {
		content, err := client.ReadFile(ctx, branch, dir+"/"+name)
		if err != nil {
			continue
		}
		if pw := gitops.ExtractDBPassword(content); pw != "" {
			return pw
		}
	}
	return ""
}

// readExistingDBPassword tries db-postgres.yaml then db-mysql.yaml from the
// tenant's apps/ dir on the configured branch (default "main"). Returns ""
// if neither file exists (first-time DB install) or the password couldn't
// be parsed.
func (h *Handler) readExistingDBPassword(ctx context.Context, tenantSlug string) string {
	dir := h.Generator.TenantDir(tenantSlug) + "/apps"
	branch := h.GitBranch
	if branch == "" {
		branch = "main"
	}
	for _, name := range []string{"db-postgres.yaml", "db-mysql.yaml"} {
		content, err := h.GitHubClient.ReadFile(ctx, branch, dir+"/"+name)
		if err != nil {
			continue
		}
		if pw := gitops.ExtractDBPassword(content); pw != "" {
			return pw
		}
	}
	return ""
}

// waitForVclusterAppGone is the inverse of waitForVclusterApp — it waits
// until no pod matching the app slug exists in the host namespace.
//
// #4293 MINOR-4 — TIER-GATED. The keystone tier-gated the install wait
// (waitForVclusterApp via appPodNameMatches) but left this uninstall wait
// hardcoded to the vcluster syncer suffix `-x-apps-x-vcluster`. For a host-tier
// (free/S) Org the app pod has a NATIVE name (no suffix), so the hardcoded
// matcher never found it → the "no matching pod" branch returned success on the
// first poll, reporting "gone" while the native pod was still Terminating. Thread
// isVcluster + reuse appPodNameMatches so both tiers wait on the right shape,
// mirroring the install side.
func (h *Handler) waitForVclusterAppGone(ctx context.Context, namespace, appSlug string, timeout time.Duration, isVcluster bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body, err := h.k8sGet(fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
		if err == nil {
			var podList struct {
				Items []struct {
					Metadata struct {
						Name string `json:"name"`
					} `json:"metadata"`
				} `json:"items"`
			}
			found := false
			if jerr := json.Unmarshal(body, &podList); jerr == nil {
				for _, pod := range podList.Items {
					if appPodNameMatches(pod.Metadata.Name, appSlug, isVcluster) {
						found = true
						break
					}
				}
			}
			if !found {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("app %s still present in %s after %s", appSlug, namespace, timeout)
}

type tenantDeletedData struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// handleTenantDeleted tears a tenant all the way down:
//
//  1. Delete the per-tenant Flux Kustomization CR (named
//     "tenant-<slug>-apps"). Since issue #97 this CR lives in `flux-system`
//     with spec.targetNamespace: tenant-<slug> — placing the CR inside the
//     tenant NS wedged GC on its finalizer. For backwards compat with
//     tenants provisioned before the fix, we also try the old in-namespace
//     location and strip finalizers there as a fallback.
//  2. Remove the tenant entry from the parent kustomization.yaml and prune
//     the entire clusters/contabo-mkt/tenants/<slug>/ directory from Git.
//  3. Wait for the namespace to be garbage-collected by Kubernetes. If it
//     stays Terminating past the timeout, strip remaining finalizers as a
//     last resort so the reset primitive (delete) can't be permanently wedged.
//  4. Clean up the flux-system kubeconfig mirror Secret so it doesn't
//     linger and collide with a same-slug re-provision.
//  5. Emit provision.tenant_removed so the tenant service can hard-delete
//     the record and the UI can react.
func (h *Handler) handleTenantDeleted(ctx context.Context, event *events.Event) error {
	var data tenantDeletedData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		slog.Error("failed to unmarshal tenant.deleted data", "error", err)
		return err
	}
	if !validTenantSlug(data.Slug) {
		slog.Warn("tenant.deleted rejected — missing or malformed slug",
			"tenant_id", data.ID, "slug", data.Slug)
		return nil // #105
	}
	slog.Info("tenant.deleted received — tearing down", "tenant_id", data.ID, "slug", data.Slug)

	// Preempt any in-flight day-2 waits for this tenant (issue #99). A wait
	// polling a terminating namespace would just burn 10 min returning its
	// own "not ready" error; canceling lets those goroutines exit right away
	// and marks their jobs as canceled rather than silently timed-out.
	if n := h.day2Cancels.CancelAllFor(data.Slug); n > 0 {
		slog.Info("teardown: preempted in-flight day-2 jobs",
			"slug", data.Slug, "canceled", n)
	}
	// #4404 — drop any parked pending-install for this doomed Org so the
	// self-heal reconciler stops re-attempting a commit against a repo that is
	// being torn down.
	if n := h.pendingInstalls.RemoveAllFor(data.Slug); n > 0 {
		slog.Info("teardown: dropped parked pending installs",
			"slug", data.Slug, "dropped", n)
	}

	if h.GitHubClient == nil || h.Generator == nil {
		return fmt.Errorf("github client or manifest generator not configured")
	}

	// #4290: the host namespace is the org-controller-owned `<slug>` (single
	// boundary). The funnel tears down ONLY what it produced — the apps-sync
	// Flux Kustomization, its `<slug>/apps` git tree, and the kubeconfig
	// mirror. The `<slug>` namespace + the `vcluster` HelmRelease are owned by
	// the org-controller and are pruned by its teardownPerOrgFlux when the
	// Organization CR is deleted; the funnel must NOT delete them (doing so
	// would rip the boundary out from under a still-live Org).
	hostNS := data.Slug
	// The apps-sync Kustomization name is still emitted as `tenant-<slug>-apps`
	// by generateAppsSyncKustomization (a flux-system CR name, not a
	// namespace); keep the literal so teardown deletes the CR the funnel wrote.
	appsKustName := "tenant-" + data.Slug + "-apps"

	// 1. Delete the per-tenant Flux Kustomization CR from flux-system (the
	// new location introduced by issue #97). Also try the legacy in-namespace
	// location for pre-#97 tenants and the "tenant-<slug>" (old parent)
	// variant that some earlier iterations wrote.
	//
	// Order matters: delete in flux-system first. That's where active tenants
	// reconcile; finalizing here releases the CR's managed resources in the
	// vCluster cleanly. The legacy deletes below are best-effort migrations.
	if err := h.k8sDelete(fmt.Sprintf(
		"/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/%s",
		appsKustName)); err != nil {
		slog.Warn("teardown: could not delete flux-system kustomization",
			"name", appsKustName, "error", err)
	}
	// Legacy location (pre-#97): CR in the tenant namespace. Keep deletion +
	// finalizer-strip so old tenants don't strand a terminating namespace.
	for _, kustName := range []string{"tenant-" + data.Slug, appsKustName} {
		legacyPath := fmt.Sprintf(
			"/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/%s/kustomizations/%s",
			hostNS, kustName)
		if err := h.k8sDelete(legacyPath); err != nil {
			slog.Warn("teardown: could not delete legacy in-ns flux kustomization",
				"ns", hostNS, "name", kustName, "error", err)
		}
	}

	// 2. Prune the tenant directory from Git and remove the entry from the
	// parent kustomization.
	tenantDir := h.Generator.TenantDir(data.Slug) + "/"
	parentPath := h.Generator.BasePath + "/kustomization.yaml"

	// Issue #944 cross-cluster pollution guard.
	if err := h.VerifyCommitTargetSafe(tenantDir); err != nil {
		return fmt.Errorf("teardown: commit guard: %w", err)
	}

	branch := h.GitBranch
	if branch == "" {
		branch = "main"
	}

	// Re-read the parent kustomization on EACH attempt so a concurrent
	// provision/teardown that beat us doesn't get its slug entry resurrected
	// when our retry replays a stale files map. Issue: live race observed
	// 2026-05-06 between bookcheck teardown and a concurrent multitest
	// provision — multitest's retry resurrected bookcheck because
	// CommitFilesWithPrune didn't re-read the parent on rebuild.
	rebuild := func(ctx context.Context) (map[string]string, error) {
		currentParent, readErr := h.GitHubClient.ReadFile(ctx, branch, parentPath)
		files := map[string]string{}
		if readErr != nil {
			// #4250 — a genuine 404 means the parent index is already gone
			// (nothing to remove from); the tenantDir prune alone is correct.
			// A TRANSIENT read error must NOT silently skip the parent
			// rewrite: that would leave this slug listed in resources: while
			// its directory is pruned, wedging Flux's org-tenants build (and
			// on the retry the stale-listing race RemoveTenantFromParent was
			// added to defend against). Fail the attempt so the retry re-reads.
			if !errors.Is(readErr, ghclient.ErrFileNotFound) {
				return nil, fmt.Errorf("read parent kustomization %s for teardown (refusing partial prune, #4250): %w", parentPath, readErr)
			}
			return files, nil
		}
		files[parentPath] = gitops.RemoveTenantFromParentKustomization(currentParent, data.Slug)
		return files, nil
	}

	commitMsg := fmt.Sprintf("teardown: delete tenant %s", data.Slug)
	if err := h.GitHubClient.CommitFilesWithPruneAndRebuild(ctx, branch, commitMsg, nil, []string{tenantDir}, rebuild); err != nil {
		slog.Error("teardown: commit/prune failed", "slug", data.Slug, "error", err)
		return err
	}
	slog.Info("teardown: pruned git", "slug", data.Slug, "dir", tenantDir)

	// 3. #4290 — DO NOT delete the `<slug>` namespace or the `vcluster`
	// HelmRelease here. They are owned by the org-controller (the single
	// boundary producer) and are pruned by its teardownPerOrgFlux when the
	// Organization CR is deleted. The funnel previously deleted both (the
	// `tenant-<slug>` stray it built itself); now that the boundary is the
	// org-controller's `<slug>`, the funnel deleting it would rip the boundary
	// out from under a still-live Org (e.g. a day-2 re-install racing the
	// tenant.deleted, or an Org whose CR has not yet been deleted). The funnel
	// has already (step 1) deleted its own apps-sync Kustomization and (step 2)
	// pruned its `<slug>/apps` git tree; Flux prune on that Kustomization
	// removes the funnel-installed Applications from inside the vCluster. The
	// org-controller's reconcile owns the rest.
	//
	// Best-effort: strip cert-manager finalizers on any Certificate the funnel
	// ingress authored in the `<slug>` namespace so a same-slug re-provision
	// starts clean. This does NOT delete the namespace — it only clears
	// finalizers on funnel-authored Certificates. Issue #86 carried forward.
	h.stripCertificateFinalizers(ctx, hostNS)

	// 4. Drop the flux-system kubeconfig mirror so it doesn't linger (and so
	// a same-slug re-provision starts from a clean slate).
	if err := h.deleteVClusterKubeconfigMirror(ctx, data.Slug); err != nil {
		slog.Warn("teardown: could not delete kubeconfig mirror secret",
			"slug", data.Slug, "error", err)
	}

	h.publishEvent(ctx, "provision.tenant_removed", data.ID, map[string]string{
		"id":   data.ID,
		"slug": data.Slug,
	})
	slog.Info("teardown: tenant removed", "slug", data.Slug)
	return nil
}

// stripCertificateFinalizers enumerates cert-manager.io/v1 Certificate CRs in
// the given namespace and strips their `.metadata.finalizers` so they can be
// garbage-collected without waiting for cert-manager to reconcile the delete.
//
// Why this exists:
//   - cert-manager attaches
//     `finalizer.cert-manager.io/certificate-secret-binding` to every
//     Certificate. When the tenant NS starts Terminating, the cert-manager
//     controller in the host cluster is still running and expects to
//     reconcile the delete — but it can't write back into a Terminating NS,
//     so the finalizer is never removed, the Certificate is never released,
//     and the NS stays Terminating forever.
//   - Same defect class as the Flux Kustomization finalizer fixed in #54/#97.
//   - Best-effort: we swallow enumeration + strip errors. The retry path
//     that calls us a second time after the 90s timeout re-tries, and
//     worst-case the legacy strip-by-timeout path still fires.
//
// We do NOT delete the Certificates — they're already slated for deletion by
// the NS terminator. We only drop the finalizer that's blocking GC. Also
// covers CertificateRequest children (the ACME challenge carriers) because
// those also carry finalizers in some cert-manager versions.
func (h *Handler) stripCertificateFinalizers(ctx context.Context, namespace string) {
	body, err := h.k8sGet(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificates", namespace))
	if err != nil {
		// 404 = CRD not installed or NS already gone — both acceptable.
		if !strings.Contains(err.Error(), "status 404") {
			slog.Debug("teardown: list certificates failed",
				"ns", namespace, "error", err)
		}
		return
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name       string   `json:"name"`
				Finalizers []string `json:"finalizers"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if jerr := json.Unmarshal(body, &list); jerr != nil {
		slog.Warn("teardown: decode certificate list",
			"ns", namespace, "error", jerr)
		return
	}
	stripped := 0
	for _, item := range list.Items {
		if len(item.Metadata.Finalizers) == 0 {
			continue
		}
		path := fmt.Sprintf(
			"/apis/cert-manager.io/v1/namespaces/%s/certificates/%s",
			namespace, item.Metadata.Name)
		if perr := h.k8sPatchRemoveFinalizers(path); perr != nil {
			slog.Warn("teardown: could not strip certificate finalizers",
				"ns", namespace, "cert", item.Metadata.Name, "error", perr)
			continue
		}
		stripped++
	}
	if stripped > 0 {
		slog.Info("teardown: stripped certificate finalizers",
			"ns", namespace, "count", stripped)
	}

	// CertificateRequests — cert-manager's ACME challenge carriers. Some
	// versions attach finalizers to these too; strip them in the same pass.
	crBody, crErr := h.k8sGet(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificaterequests", namespace))
	if crErr != nil {
		return
	}
	var crList struct {
		Items []struct {
			Metadata struct {
				Name       string   `json:"name"`
				Finalizers []string `json:"finalizers"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if jerr := json.Unmarshal(crBody, &crList); jerr != nil {
		return
	}
	for _, item := range crList.Items {
		if len(item.Metadata.Finalizers) == 0 {
			continue
		}
		path := fmt.Sprintf(
			"/apis/cert-manager.io/v1/namespaces/%s/certificaterequests/%s",
			namespace, item.Metadata.Name)
		_ = h.k8sPatchRemoveFinalizers(path)
	}
}

func (h *Handler) handleOrderPlaced(ctx context.Context, event *events.Event) error {
	var data orderPlacedData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		slog.Error("failed to unmarshal order.placed data", "error", err)
		return err
	}
	// Reject empty or malformed subdomain at the boundary. Without this guard,
	// an empty slug produces git paths like `.../tenants//namespace.yaml` and
	// GitHub's tree API rejects the whole commit with
	//   "tree.path contains a malformed path component" (HTTP 422).
	// Seen in production 2026-04-20, provision_id e13cbbd2. Issue #105.
	if !validTenantSlug(data.Subdomain) {
		slog.Error("order.placed rejected — invalid subdomain",
			"tenant_id", data.TenantID, "subdomain", data.Subdomain,
			"expected", "[a-z][a-z0-9-]{2,30}")
		h.publishEvent(ctx, "provision.tenant_failed", data.TenantID, map[string]string{
			"tenant_id": data.TenantID,
			"error":     "invalid subdomain: must match [a-z][a-z0-9-]{2,30}",
		})
		return nil // Acked; redelivery won't help a bad payload.
	}
	slog.Info("received order.placed event",
		"tenant_id", data.TenantID,
		"order_id", data.OrderID,
		"plan_id", data.PlanID,
		"apps", data.Apps,
		"app_configs_keys", appConfigsKeys(data.AppConfigs),
	)
	_, err := h.startProvisioning(ctx, data.TenantID, data.OrderID, data.PlanID, data.Apps, data.Subdomain, data.AppConfigs)
	return err
}

// appConfigsKeys returns the sorted list of app slugs present in the
// AppConfigs map. Used solely for structured logging on the order.placed
// path so operators can see which apps shipped customer-chosen values
// without dumping the full (potentially large) map at INFO level.
func appConfigsKeys(m map[string]map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// startProvisioning creates the provision record and kicks off the workflow
// goroutine. Steps are laid out up front so the UI knows what to render.
//
// appConfigs carries the customer-chosen configSchema values keyed by app
// SLUG (TBD-V27 #2042). Empty/nil means use generator defaults — the
// backing-service renderers (generatePostgres etc.) treat absence as a
// no-op so legacy paths keep working unchanged.
func (h *Handler) startProvisioning(ctx context.Context, tenantID, orderID, planID string, apps []string, subdomain string, appConfigs map[string]map[string]any) (*store.Provision, error) {
	appNames := h.resolveAppNames(ctx)
	planSlug := h.resolvePlanSlug(ctx, planID)
	appSlugs := h.resolveAppSlugs(ctx, apps)

	// Dependencies come from the catalog (admin-editable). Dedup across apps
	// (many apps share mysql/postgres), order deterministically for the UI.
	depsByApp := h.resolveAppDependencies(ctx, appSlugs)
	depSet := make(map[string]bool)
	for _, ds := range depsByApp {
		for _, d := range ds {
			depSet[d] = true
		}
	}
	depSlugs := make([]string, 0, len(depSet))
	for d := range depSet {
		depSlugs = append(depSlugs, d)
	}
	sort.Strings(depSlugs)

	steps := []store.ProvisionStep{
		{Name: "Creating Organization", Status: "pending"},
		{Name: "Committing manifests to Git", Status: "pending"},
		{Name: "Provisioning vCluster", Status: "pending"},
	}
	for _, dep := range depSlugs {
		steps = append(steps, store.ProvisionStep{
			Name:   fmt.Sprintf("Installing %s (dependency)", dep),
			Status: "pending",
		})
	}
	for _, appID := range apps {
		steps = append(steps, store.ProvisionStep{
			Name:   fmt.Sprintf("Deploying %s", appDisplayName(appNames, appID)),
			Status: "pending",
		})
	}
	steps = append(steps,
		store.ProvisionStep{Name: "Configuring TLS certificates", Status: "pending"},
		store.ProvisionStep{Name: "Running health checks", Status: "pending"},
	)

	provision := &store.Provision{
		TenantID:  tenantID,
		OrderID:   orderID,
		PlanID:    planID,
		Apps:      apps,
		Subdomain: subdomain,
		Status:    "provisioning",
		Steps:     steps,
		Progress:  0,
	}

	// #3744 — dedup the provision-create entrypoint. A credit-covered
	// marketplace checkout fires this path twice (NATS order.placed event +
	// POST /provisioning/start) by design; without a guard both inserts fork a
	// workflow and the two near-simultaneous Gitea commits race on the shared
	// org-tenants branch ("cannot lock ref"), marking the tenant failed even
	// though the Org CR + winning commit land. dedupProvisionCreate is backed by
	// a partial unique index on (tenant_id, in-flight status), so the second
	// trigger collides and we return the already-running provision without
	// forking a duplicate workflow — mirroring CreateJobIfAbsent's #71 dedup for
	// day-2 Jobs. The decision is extracted into a pure helper so the exact
	// double-fire → single-provision semantics are unit-testable without Mongo.
	// The helper also folds in the #3898 stale-in-flight reap so a crashed/rolled
	// provisioning Pod can't permanently wedge a tenant out of the #3376 funnel.
	result, err := dedupProvisionCreate(ctx, h.Store, provision, tenantID)
	if err != nil {
		slog.Error("provision-create dedup failed", "tenant_id", tenantID, "error", err)
		return nil, err
	}
	if !result.fork {
		// A concurrent trigger already owns the in-flight provision (or a prior
		// one just terminated). Return the existing record WITHOUT forking a
		// second workflow — this is the load-bearing guard that stops the
		// self-race that marked stranger redeems failed.
		slog.Info("duplicate provision dispatch — not forking a second workflow",
			"tenant_id", tenantID, "provision_id", result.provision.ID)
		return result.provision, nil
	}

	h.publishEvent(ctx, "provision.started", tenantID, map[string]string{
		"provision_id": provision.ID,
		"plan_id":      planID,
	})

	go h.runProvisioningWorkflow(provision.ID, tenantID, subdomain, planSlug, depSlugs, appSlugs, len(steps), appConfigs)

	return provision, nil
}

// provisionDedupStore is the narrow store surface dedupProvisionCreate needs.
// *store.Store satisfies it in production; tests inject a fake that simulates
// the partial-unique-index collision without a live Mongo. Keeping the surface
// to exactly the two dedup methods means the test can model the double-fire
// race deterministically.
type provisionDedupStore interface {
	CreateProvisionIfAbsent(ctx context.Context, p *store.Provision) error
	GetInFlightProvisionByTenant(ctx context.Context, tenantID string) (*store.Provision, error)
	// ReapStaleInFlightProvision (#3898) clears an abandoned in-flight row whose
	// owning workflow goroutine died with its Pod, so the partial-unique index
	// releases and a legitimate re-provision can proceed.
	ReapStaleInFlightProvision(ctx context.Context, tenantID string, staleAfter time.Duration) (int, error)
}

// dedupDecision is the outcome of the #3744 provision-create dedup. When fork is
// true the caller owns a freshly-inserted provision and MUST start exactly one
// workflow; when fork is false the provision field is the already-running (or
// just-terminated) record and the caller MUST NOT fork — returning it is the
// no-op that defeats the self-race.
type dedupDecision struct {
	provision *store.Provision
	fork      bool
}

// dedupProvisionCreate runs the #3744 create-once guard. It attempts the
// insert; on an ErrProvisionExists collision (the second of the two
// credit-covered checkout triggers) it surfaces the in-flight provision so the
// loser returns it instead of forking a duplicate workflow → duplicate Gitea
// commit → ref-lock race → failed tenant.
//
// Outcomes:
//   - insert wins  → {provision, fork:true}   (start one workflow)
//   - collision, stale row reaped + re-insert wins → {provision, fork:true}
//     (#3898 — the prior owning Pod died; this re-provision legitimately forks)
//   - collision, in-flight row found → {existing, fork:false}
//   - collision, row already terminal → {provision, fork:false} (benign: the
//     prior workflow owns the outcome; a later legitimate re-provision succeeds
//     because the partial index only collides on in-flight statuses)
func dedupProvisionCreate(ctx context.Context, st provisionDedupStore, provision *store.Provision, tenantID string) (dedupDecision, error) {
	if err := st.CreateProvisionIfAbsent(ctx, provision); err != nil {
		if errors.Is(err, store.ErrProvisionExists) {
			// #3898 — before surfacing the in-flight row, reap it if it is
			// stale. A provisioning Pod crash/roll abandons the detached
			// runProvisioningWorkflow goroutine, stranding the row at
			// "provisioning" forever; the partial-unique index then wedges this
			// tenant out of the funnel because every re-provision short-circuits
			// on the dead row. A live workflow refreshes updated_at on every
			// step, so only a genuinely abandoned row (no progress for
			// staleProvisionTimeout) is reaped. If one was, retry the insert so
			// the legitimate re-provision forks a fresh workflow.
			if reaped, reapErr := st.ReapStaleInFlightProvision(ctx, tenantID, staleProvisionTimeout); reapErr != nil {
				slog.Warn("duplicate provision dispatch: stale-provision reap failed — surfacing existing row",
					"tenant_id", tenantID, "error", reapErr)
			} else if reaped > 0 {
				slog.Info("duplicate provision dispatch: reaped stale in-flight provision — re-forking workflow",
					"tenant_id", tenantID, "reaped", reaped)
				if reErr := st.CreateProvisionIfAbsent(ctx, provision); reErr != nil {
					if !errors.Is(reErr, store.ErrProvisionExists) {
						return dedupDecision{}, reErr
					}
					// Another writer won the post-reap insert race; fall through
					// to surface whatever in-flight row now exists.
					slog.Info("duplicate provision dispatch: lost the post-reap insert race — surfacing the winner",
						"tenant_id", tenantID)
				} else {
					// Won the insert post-reap — fork as a fresh provision.
					return dedupDecision{provision: provision, fork: true}, nil
				}
			}
			existing, getErr := st.GetInFlightProvisionByTenant(ctx, tenantID)
			if getErr != nil {
				return dedupDecision{}, getErr
			}
			if existing != nil {
				return dedupDecision{provision: existing, fork: false}, nil
			}
			// Lost the in-flight row between the collision and the lookup (it
			// just reached a terminal state). Treat as benign: return the
			// provision we built without forking — the prior workflow owns the
			// outcome. A subsequent legitimate re-provision will succeed.
			return dedupDecision{provision: provision, fork: false}, nil
		}
		return dedupDecision{}, err
	}
	return dedupDecision{provision: provision, fork: true}, nil
}

// provisionParentRebuild recomputes the file map for one provision commit
// attempt: the static per-Org manifests PLUS the parent org-tenants
// kustomization.yaml with this Org's slug merged in (read-modify-write off
// the LIVE parent so a concurrent sibling write/teardown isn't clobbered).
//
// #4250 root-cause guard. UpdateParentKustomization appends one slug to
// whatever parent content it's handed. If the parent ReadFile fails and we
// fabricate an empty `resources: []`, the merged result lists ONLY this Org
// — and Flux's org-tenants Kustomization runs prune=true, so committing
// that truncated list garbage-collects every SIBLING Org's namespace (the
// chronic `org-<uuid>` teardown that coincides with concurrent sibling
// provisions). The empty seed is therefore ONLY legitimate on a genuine
// 404 (ErrFileNotFound = first Org on this Sovereign). Any other read error
// (network blip, Gitea 5xx, decode failure) returns an error so
// CommitFilesWithPruneAndRebuild fails the attempt — the provision pipeline
// retries on its own cadence instead of nuking live Orgs. #4214 fixed the
// JSON-envelope corruption of the same file; this closes the residual
// transient-error fallback.
func (h *Handler) provisionParentRebuild(ctx context.Context, branch, parentKustomPath, subdomain string, manifests map[string]string) (map[string]string, error) {
	currentParentKustom, readErr := h.GitHubClient.ReadFile(ctx, branch, parentKustomPath)
	if readErr != nil {
		if !errors.Is(readErr, ghclient.ErrFileNotFound) {
			return nil, fmt.Errorf("read parent kustomization %s (refusing empty fallback that would prune sibling Orgs, #4250): %w", parentKustomPath, readErr)
		}
		slog.Info("parent kustomization absent (first Org on this Sovereign) — seeding empty", "path", parentKustomPath)
		currentParentKustom = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
	}
	// Copy the static tenant manifests + the freshly-merged parent.
	fresh := make(map[string]string, len(manifests)+1)
	for k, v := range manifests {
		fresh[k] = v
	}
	fresh[parentKustomPath] = gitops.UpdateParentKustomization(currentParentKustom, subdomain)
	return fresh, nil
}

// runProvisioningWorkflow performs real K8s provisioning via GitOps and
// verifies each step against the host cluster (vCluster pods are visible
// in the host tenant-<slug> namespace thanks to vCluster's pod sync).
//
// appConfigs threads customer-chosen configSchema values through to the
// manifest generator (TBD-V27 #2042). Nil/empty preserves the legacy
// "use defaults" behavior.
func (h *Handler) runProvisioningWorkflow(provisionID, tenantID, subdomain, planSlug string, depSlugs, appSlugs []string, totalSteps int, appConfigs map[string]map[string]any) {
	ctx := context.Background()

	prov, err := h.Store.GetProvision(ctx, provisionID)
	if err != nil || prov == nil {
		slog.Error("failed to load provision for workflow", "provision_id", provisionID, "error", err)
		return
	}

	// Workstream A (#4290 / EPIC #4293): the per-Organization host namespace is
	// the org-controller-owned `<slug>` (the SINGLE boundary), not a stray
	// `tenant-<slug>`. The vCluster pods sync up into `<slug>`; every wait /
	// pod-status / TLS poll below targets it.
	hostNS := subdomain
	// #4297 TIER GATE (keystone of EPIC #4293) — paid M+ Orgs get a dedicated
	// vCluster (the apps are reconciled INTO it via the apps-sync Kustomization's
	// kubeConfig); free/S Orgs have NO vcluster, so the apps land directly in the
	// host `<slug>` ns. The vcluster-HR wait, the DNS-sync kick, and the
	// kubeconfig mirror below are ALL vcluster-only — running them for a host-tier
	// Org would block forever (no `vcluster` HelmRelease, no `vc-vcluster` secret
	// ever appears). Gate every vcluster-specific step on this predicate.
	isVcluster := gitops.BoundaryIsVcluster(planSlug)
	stepIdx := 0

	// --- Step: Generate manifests ---
	h.markStep(ctx, provisionID, stepIdx, prov.Steps[stepIdx].Name, "running")

	// #4827 — On a Sovereign (PerOrgGitops) the customer's Applications are
	// authored ONLY into the per-Org `<slug>/catalyst-tenant` repo: the
	// org-controller bootstraps that repo's `vcluster/apps` tree + its
	// `catalyst-tenant-<slug>-apps` Flux Kustomization, and each cart app's
	// tenant.app_install_requested event drives applyTenantChangePerOrg to
	// commit there (branch `main`, DB password read-preserved from that same
	// tree). Authoring the SAME apps ALSO into the global `org-tenants` catalog
	// tree here (the legacy contabo path) mints a SECOND, divergent apps tree
	// with an independently-generated random DB password; that tree's
	// `tenant-<slug>-apps` Kustomization AND the per-Org
	// `catalyst-tenant-<slug>-apps` Kustomization then both reconcile the SAME
	// WordPress Deployment INTO the vCluster (identical name/namespace, both
	// prune=true) with two different *plaintext* WORDPRESS_DB_PASSWORD values,
	// so the WordPress pod template flip-flops on every reconcile → endless
	// rollout churn → intermittent HTTP 500. (The MySQL pod is immune: it
	// consumes creds via `envFrom secretRef: mysql-credentials`, a
	// template-stable reference by NAME, so its template is byte-identical
	// across both trees and it never rolls.) The PerOrgGitops contract is
	// explicit — "the global repo is the WRONG target for per-Org apps on a
	// Sovereign" — and everything the org-tenants overlay carried lives in the
	// per-Org tree (the #4785 in-vCluster HTTPRoute datapath, the networkpolicy
	// baseline, the vCluster CR; the org-tenants overlay's only extras were a
	// legacy `kind: Ingress` + per-tenant RBAC, both inert on a Gateway-API
	// Sovereign). So on a Sovereign this legacy monolithic workflow authors
	// NOTHING to git — it completes the generate + commit timeline steps as a
	// no-op and proceeds to OBSERVE the per-Org-delivered vCluster pods for the
	// funnel progress UI.
	if h.PerOrgGitops {
		h.completeStepWithMessage(ctx, provisionID, tenantID, stepIdx, prov.Steps[stepIdx].Name, totalSteps,
			"per-Org Sovereign — Applications authored in the Org's catalyst-tenant repo (not the global org-tenants tree)")
		stepIdx++

		// --- Step: Commit to Git (owned by the per-Org app-install path) ---
		h.markStep(ctx, provisionID, stepIdx, prov.Steps[stepIdx].Name, "running")
		h.completeStepWithMessage(ctx, provisionID, tenantID, stepIdx, prov.Steps[stepIdx].Name, totalSteps,
			"per-Org Sovereign — manifests committed to the Org's catalyst-tenant repo by the app-install path")
		stepIdx++
	} else {
		manifests := h.Generator.GenerateAllWithAppConfigs(subdomain, planSlug, appSlugs, "", appConfigs)
		if len(manifests) == 0 {
			h.failProvision(ctx, provisionID, tenantID, stepIdx, "failed to generate manifests")
			return
		}
		slog.Info("generated manifests",
			"provision_id", provisionID,
			"tenant", subdomain,
			"files", len(manifests),
		)
		h.completeStep(ctx, provisionID, tenantID, stepIdx, prov.Steps[stepIdx].Name, totalSteps)
		stepIdx++

		// --- Step: Commit to Git ---
		h.markStep(ctx, provisionID, stepIdx, prov.Steps[stepIdx].Name, "running")

		if h.GitHubClient == nil {
			h.failProvision(ctx, provisionID, tenantID, stepIdx, "GitHub client not configured")
			return
		}

		parentKustomPath := h.Generator.BasePath + "/kustomization.yaml"

		// Issue #944 cross-cluster pollution guard. Validate the parent
		// kustomization path AND the per-tenant subdir live under the
		// configured GIT_BASE_PATH before any commit lands.
		if err := h.VerifyCommitTargetSafe(parentKustomPath); err != nil {
			slog.Error("commit guard rejected provision target", "provision_id", provisionID, "path", parentKustomPath, "error", err)
			h.failProvision(ctx, provisionID, tenantID, stepIdx, fmt.Sprintf("commit guard: %s", err))
			return
		}
		if err := h.VerifyCommitTargetSafe(h.Generator.TenantDir(subdomain) + "/"); err != nil {
			slog.Error("commit guard rejected tenant target", "provision_id", provisionID, "tenant", subdomain, "error", err)
			h.failProvision(ctx, provisionID, tenantID, stepIdx, fmt.Sprintf("commit guard: %s", err))
			return
		}

		branch := h.GitBranch
		if branch == "" {
			branch = "main"
		}

		// Re-read the parent kustomization on EACH commit attempt so a concurrent
		// teardown's removal of another slug doesn't get reverted by our retry
		// replaying a stale files map (live race observed 2026-05-06: multitest
		// provision's retry resurrected the just-deleted bookcheck slug). The
		// #4250 sibling-prune guard lives in provisionParentRebuild.
		rebuild := func(ctx context.Context) (map[string]string, error) {
			return h.provisionParentRebuild(ctx, branch, parentKustomPath, subdomain, manifests)
		}

		commitMsg := fmt.Sprintf("provision: deploy tenant %s (plan: %s, apps: %d)", subdomain, planSlug, len(appSlugs))
		if err := h.GitHubClient.CommitFilesWithPruneAndRebuild(ctx, branch, commitMsg, nil, nil, rebuild); err != nil {
			slog.Error("failed to commit manifests", "provision_id", provisionID, "error", err)
			h.failProvision(ctx, provisionID, tenantID, stepIdx, fmt.Sprintf("git commit failed: %s", err))
			return
		}
		slog.Info("committed manifests to GitHub", "provision_id", provisionID, "tenant", subdomain)
		h.completeStep(ctx, provisionID, tenantID, stepIdx, prov.Steps[stepIdx].Name, totalSteps)
		stepIdx++
	}

	// --- Step: Wait for vCluster HelmRelease Ready ---
	// #4297 TIER GATE — vcluster-only. The "Provisioning vCluster" step stays in
	// the timeline for BOTH tiers (step indices must stay stable), but for the
	// HOST tier (free/S — no vcluster) there is no `vcluster` HelmRelease, no
	// syncer DNS to kick, and no `vc-vcluster` kubeconfig to mirror. Running any
	// of those waits would block the host-tier provision forever. So host-tier
	// completes this step immediately with an explanatory message and lets the
	// apps-sync Kustomization reconcile the apps straight into the host `<slug>`
	// ns (no kubeConfig — see generateAppsSyncKustomization).
	vcStepIdx := stepIdx
	h.markStep(ctx, provisionID, vcStepIdx, prov.Steps[vcStepIdx].Name, "running")
	if isVcluster {
		if err := h.waitForHelmRelease(ctx, hostNS, "vcluster", 10*time.Minute); err != nil {
			h.failStep(ctx, provisionID, tenantID, vcStepIdx, prov.Steps[vcStepIdx].Name, err.Error())
			h.failProvision(ctx, provisionID, tenantID, vcStepIdx, fmt.Sprintf("vcluster not ready: %s", err))
			return
		}
		// HelmRelease Ready only guarantees vcluster-0 is up; the syncer's initial
		// DNS reconciliation is racy and sometimes leaves kube-dns-x-kube-system-x-
		// vcluster absent from the host NS. Without that service, app pods inside
		// the vcluster can't resolve DNS and stay Pending for their full 10-min
		// wait. Issue #103. Observed on tenant e2e90689b today — gitea + vaultwarden
		// timed out as a side effect. Gate the next step on DNS being synced and
		// bounce vcluster-0 once if it doesn't appear, before letting app installs
		// dispatch.
		if err := h.waitForVclusterDNSOrKick(ctx, hostNS); err != nil {
			slog.Warn("vcluster DNS did not sync after kick — proceeding anyway",
				"ns", hostNS, "error", err)
			// Don't fail provisioning: apps might still come up if DNS syncs late,
			// and a hard fail here would strand the tenant. We've done what we can.
		}
		// Mirror the vCluster kubeconfig into flux-system so the per-tenant Flux
		// Kustomization CR (which now lives in flux-system — see issue #97) can
		// resolve its spec.kubeConfig.secretRef. Without this mirror the CR
		// reconciles into "secret not found" and tenant apps never deploy.
		if err := h.mirrorVClusterKubeconfig(ctx, subdomain); err != nil {
			slog.Error("failed to mirror vcluster kubeconfig", "tenant", subdomain, "error", err)
			h.failStep(ctx, provisionID, tenantID, vcStepIdx, prov.Steps[vcStepIdx].Name, err.Error())
			h.failProvision(ctx, provisionID, tenantID, vcStepIdx,
				fmt.Sprintf("mirror kubeconfig to flux-system: %s", err))
			return
		}
		h.completeStep(ctx, provisionID, tenantID, vcStepIdx, prov.Steps[vcStepIdx].Name, totalSteps)
	} else {
		slog.Info("host-tier Org (no vCluster) — skipping vcluster HR wait / DNS kick / kubeconfig mirror; apps reconcile into the host namespace",
			"tenant", subdomain, "plan", planSlug, "ns", hostNS)
		h.completeStepWithMessage(ctx, provisionID, tenantID, vcStepIdx, prov.Steps[vcStepIdx].Name, totalSteps,
			"host-tier plan — apps run directly in the Org namespace (no dedicated vCluster)")
	}
	stepIdx++

	// --- Steps: Parallel dependency installs ---
	// DBs (postgres/mysql/redis) come up faster than apps, so we show them as
	// separate visible steps and wait on them first. K8s starts them in
	// parallel with apps regardless, but surfacing them in the timeline is
	// what the user asked for.
	if len(depSlugs) > 0 {
		var depWG sync.WaitGroup
		var depFailed atomic.Bool
		depStartIdx := stepIdx
		for i, dep := range depSlugs {
			idx := depStartIdx + i
			stepName := prov.Steps[idx].Name
			h.markStep(ctx, provisionID, idx, stepName, "running")
			depWG.Add(1)
			go func(depSlug, stepName string, stepIdx int) {
				defer depWG.Done()
				if err := h.waitForVclusterApp(ctx, hostNS, depSlug, 10*time.Minute, isVcluster); err != nil {
					depFailed.Store(true)
					h.failStep(ctx, provisionID, tenantID, stepIdx, stepName, err.Error())
					return
				}
				h.completeStep(ctx, provisionID, tenantID, stepIdx, stepName, totalSteps)
			}(dep, stepName, idx)
		}
		depWG.Wait()
		stepIdx += len(depSlugs)

		if depFailed.Load() {
			h.failProvision(ctx, provisionID, tenantID, depStartIdx, "one or more dependencies failed to become ready")
			return
		}
	}

	// --- Steps: Parallel app deploys ---
	// All apps start their wait at the same time. Each goroutine updates its
	// own step index; the main waits for the group.
	var wg sync.WaitGroup
	var failed atomic.Bool
	appStartIdx := stepIdx
	for i, appSlug := range appSlugs {
		idx := appStartIdx + i
		stepName := prov.Steps[idx].Name
		h.markStep(ctx, provisionID, idx, stepName, "running")
		wg.Add(1)
		go func(appSlug, stepName string, stepIdx int) {
			defer wg.Done()
			if err := h.waitForVclusterApp(ctx, hostNS, appSlug, 10*time.Minute, isVcluster); err != nil {
				failed.Store(true)
				h.failStep(ctx, provisionID, tenantID, stepIdx, stepName, err.Error())
				return
			}
			h.completeStep(ctx, provisionID, tenantID, stepIdx, stepName, totalSteps)
		}(appSlug, stepName, idx)
	}
	wg.Wait()
	stepIdx += len(appSlugs)

	if failed.Load() {
		h.failProvision(ctx, provisionID, tenantID, appStartIdx, "one or more apps failed to become ready")
		return
	}

	// --- Step: TLS ---
	tlsStepIdx := stepIdx
	h.markStep(ctx, provisionID, tlsStepIdx, prov.Steps[tlsStepIdx].Name, "running")
	// cert-manager typically issues the first cert within 30-90s. Don't fail
	// the whole provision if it's still issuing — the ingress works on HTTP
	// while the challenge completes.
	if err := h.waitForCertificate(ctx, hostNS, fmt.Sprintf("tenant-%s-tls", subdomain), 3*time.Minute); err != nil {
		h.completeStepWithMessage(ctx, provisionID, tenantID, tlsStepIdx, prov.Steps[tlsStepIdx].Name, totalSteps, "cert still issuing — will be ready shortly")
	} else {
		h.completeStep(ctx, provisionID, tenantID, tlsStepIdx, prov.Steps[tlsStepIdx].Name, totalSteps)
	}
	stepIdx++

	// --- Step: Final health check ---
	healthStepIdx := stepIdx
	h.markStep(ctx, provisionID, healthStepIdx, prov.Steps[healthStepIdx].Name, "running")
	if err := h.waitForAnyPod(ctx, hostNS, 2*time.Minute); err != nil {
		h.failStep(ctx, provisionID, tenantID, healthStepIdx, prov.Steps[healthStepIdx].Name, err.Error())
		h.failProvision(ctx, provisionID, tenantID, healthStepIdx, "no running pods after provisioning")
		return
	}
	h.completeStep(ctx, provisionID, tenantID, healthStepIdx, prov.Steps[healthStepIdx].Name, totalSteps)

	p, err := h.Store.GetProvision(ctx, provisionID)
	if err != nil || p == nil {
		slog.Error("failed to get provision for completion", "error", err)
		return
	}
	p.Status = "completed"
	p.Progress = 100
	if err := h.Store.UpdateProvision(ctx, provisionID, p); err != nil {
		slog.Error("failed to mark provision as completed", "error", err)
		return
	}

	h.publishEvent(ctx, "provision.completed", tenantID, map[string]string{
		"provision_id": provisionID,
	})

	// PR #1644 follow-up — patch Organization.spec.tenantPublic so the
	// per-tenant HTTPRoute reconciler renders `<slug>.<parentDomain>`
	// against the just-deployed product. Uses the first app slug as the
	// "primary product" since the HTTPRoute reconciler emits one route
	// per Organization (last-write-wins on subsequent installs). The
	// host namespace is "tenant-<slug>" — matches generateHostIngress's
	// syncedName() in gitops/gitops.go. Best-effort: failures don't
	// fail the provision (tenant stays reachable via the Sovereign-wide
	// tenant-wildcard route).
	primaryProduct := ""
	if len(appSlugs) > 0 {
		primaryProduct = appSlugs[0]
	}
	h.emitOrgTenantPublic(ctx, subdomain, primaryProduct, hostNS)

	slog.Info("provisioning completed", "provision_id", provisionID, "tenant", subdomain)
}

// --- step tracking helpers ---

func (h *Handler) markStep(ctx context.Context, provisionID string, idx int, name, status string) {
	step := store.ProvisionStep{
		Name:      name,
		Status:    status,
		StartedAt: time.Now().UTC(),
	}
	if err := h.Store.UpdateStep(ctx, provisionID, idx, step); err != nil {
		slog.Error("failed to update step", "step", idx, "status", status, "error", err)
	}
}

func (h *Handler) completeStep(ctx context.Context, provisionID, tenantID string, idx int, name string, totalSteps int) {
	h.completeStepWithMessage(ctx, provisionID, tenantID, idx, name, totalSteps, "ok")
}

func (h *Handler) completeStepWithMessage(ctx context.Context, provisionID, tenantID string, idx int, name string, totalSteps int, message string) {
	step := store.ProvisionStep{
		Name:    name,
		Status:  "completed",
		Message: message,
		DoneAt:  time.Now().UTC(),
	}
	if err := h.Store.UpdateStep(ctx, provisionID, idx, step); err != nil {
		slog.Error("failed to complete step", "step", idx, "error", err)
	}

	// Compute progress as completed-steps / total, so parallel completions
	// roll up correctly.
	p, err := h.Store.GetProvision(ctx, provisionID)
	if err == nil && p != nil {
		completed := 0
		for _, s := range p.Steps {
			if s.Status == "completed" {
				completed++
			}
		}
		progress := (completed * 100) / totalSteps
		h.updateProgress(ctx, provisionID, progress)

		h.publishEvent(ctx, "provision.step", tenantID, map[string]string{
			"provision_id": provisionID,
			"step_index":   fmt.Sprintf("%d", idx),
			"step_name":    name,
			"progress":     fmt.Sprintf("%d", progress),
		})

		slog.Info("provisioning step completed",
			"provision_id", provisionID,
			"step", name,
			"progress", progress,
		)
	}
}

// failStep marks a single step as failed without failing the whole provision.
// Used by sibling parallel workers so the UI sees which app broke.
func (h *Handler) failStep(ctx context.Context, provisionID, tenantID string, idx int, name, message string) {
	step := store.ProvisionStep{
		Name:    name,
		Status:  "failed",
		Message: message,
		DoneAt:  time.Now().UTC(),
	}
	if err := h.Store.UpdateStep(ctx, provisionID, idx, step); err != nil {
		slog.Error("failed to mark step as failed", "step", idx, "error", err)
	}
	slog.Error("provisioning step failed", "provision_id", provisionID, "step", name, "error", message)
}

// failProvision marks a provision and its current step as failed, publishes
// provision.failed so downstream services can react.
func (h *Handler) failProvision(ctx context.Context, provisionID, tenantID string, stepIndex int, message string) {
	p, err := h.Store.GetProvision(ctx, provisionID)
	if err != nil || p == nil {
		slog.Error("failed to get provision for failure", "error", err)
		return
	}

	if stepIndex < len(p.Steps) && p.Steps[stepIndex].Status != "failed" {
		p.Steps[stepIndex].Status = "failed"
		p.Steps[stepIndex].Message = message
		p.Steps[stepIndex].DoneAt = time.Now().UTC()
	}

	p.Status = "failed"
	// #5646 — progress must never read 100 on a run that failed. A concurrent
	// pod-truth reconcile (or an earlier step-completion roll-up) can have left
	// Progress at 100 just before this failure lands; without recomputing it the
	// customer sees a FULL progress bar sitting above "Provisioning didn't
	// finish". Recompute from the steps that ACTUALLY completed — the failed /
	// still-pending steps are excluded, so on a failed run this is always < 100.
	// The invariant the funnel UI relies on: progress == 100 ⟺ status == completed.
	p.Progress = completedStepProgress(p.Steps)
	if err := h.Store.UpdateProvision(ctx, provisionID, p); err != nil {
		slog.Error("failed to mark provision as failed", "error", err)
	}

	h.publishEvent(ctx, "provision.failed", tenantID, map[string]string{
		"provision_id": provisionID,
		"error":        message,
	})

	slog.Error("provisioning failed", "provision_id", provisionID, "step", stepIndex, "error", message)
}

// completedStepProgress returns the percent of steps that are in the terminal
// "completed" state. Because a failing (or still-running) provision always has
// at least one non-completed step, the result is guaranteed < 100 — which is
// exactly the property #5646's failProvision relies on to enforce the
// progress == 100 ⟺ status == "completed" invariant. Empty step list → 0.
func completedStepProgress(steps []store.ProvisionStep) int {
	if len(steps) == 0 {
		return 0
	}
	completed := 0
	for _, s := range steps {
		if s.Status == "completed" {
			completed++
		}
	}
	return (completed * 100) / len(steps)
}

func (h *Handler) updateProgress(ctx context.Context, provisionID string, progress int) {
	p, err := h.Store.GetProvision(ctx, provisionID)
	if err != nil || p == nil {
		return
	}
	p.Progress = progress
	_ = h.Store.UpdateProvision(ctx, provisionID, p)
}

func (h *Handler) publishEvent(_ context.Context, eventType, tenantID string, data any) {
	event, err := events.NewEvent(eventType, "provisioning", tenantID, data)
	if err != nil {
		slog.Error("failed to create event", "type", eventType, "error", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Producer.Publish(pubCtx, topicProvisionEvents, event); err != nil {
		slog.Error("failed to publish event", "type", eventType, "error", err)
	}
}
