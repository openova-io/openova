// Package handler — sovereign.go: REST surface the Sovereign Console
// at console.<sov-fqdn>/console/* reads to populate its Jobs / Apps /
// Cloud / Dashboard pages with LIVE local-cluster data.
//
// Until this file landed, every page in the freshly-handed-over
// Sovereign Console rendered a "integration pending" placeholder
// because the only data path was via the mothership at
// console.openova.io/sovereign/* — useless at handover, since the
// operator just severed every visual tether to the mothership.
//
// Routes:
//
//	GET /api/v1/sovereign/status   — counts for the Dashboard cards
//	GET /api/v1/sovereign/jobs     — HelmRelease history + recent K8s Jobs
//	GET /api/v1/sovereign/apps     — installable marketplace catalog +
//	                                 currently-installed status
//	GET /api/v1/sovereign/cloud    — nodes / namespaces / ingresses /
//	                                 services (LBs) / storage classes / PVCs
//
// Architecture, per ADR-0001 §5 + INVIOLABLE-PRINCIPLES.md #3:
//
//   - The catalyst-api Pod runs on the SAME cluster the Console serves
//     after handover. It uses rest.InClusterConfig() to read its own
//     apiserver — there is NO mothership round-trip on any of these
//     endpoints, so the Console stays fully populated even after the
//     Self-Sovereignty Cutover (issue #792) severs every external link.
//
//   - The catalog endpoint embeds the same blueprints.json the wizard's
//     StepComponents reads (internal/catalog), so the Sovereign side
//     always has a Phase-1 catalog identical to the mothership — no
//     gitea-mirror dependency for first paint. The local Gitea mirror
//     established by cutover step `gitea-mirror` keeps the same
//     catalog reconciled going forward.
//
// On Catalyst-Zero (mothership) the in-cluster client still resolves
// (the catalyst-api on contabo also runs in-cluster) — the responses
// are still correct (mothership cluster's view) but the mothership
// Console serves /sovereign/jobs|apps|cloud via the existing
// per-deployment endpoints, NOT these. The new /api/v1/sovereign/*
// surface is intended for the Sovereign-side console only.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
)

// sovereignDeps bundles the Kubernetes clients the local-cluster
// endpoints read from. Production builds these once from
// rest.InClusterConfig (catalyst-api always runs IN the cluster it
// serves — both on the mothership and after handover on the
// Sovereign). Tests inject fakes via SetSovereignDepsFactory.
type sovereignDeps struct {
	core kubernetes.Interface
	dyn  dynamic.Interface
}

// SovereignDepsFactory returns a fresh sovereignDeps. nil in
// production means "build from in-cluster config"; tests inject a
// closure returning a fake clientset + dynamic client.
type SovereignDepsFactory func() (*sovereignDeps, error)

// SetSovereignDepsFactory wires a test-only deps factory.
func (h *Handler) SetSovereignDepsFactory(f SovereignDepsFactory) {
	h.sovereignDepsFactory = f
}

func (h *Handler) sovereignDepsFor() (*sovereignDeps, error) {
	if h.sovereignDepsFactory != nil {
		return h.sovereignDepsFactory()
	}
	return sovereignDepsFromEnv()
}

func sovereignDepsFromEnv() (*sovereignDeps, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("sovereign: in-cluster config unavailable: %w", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("sovereign: build core client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("sovereign: build dynamic client: %w", err)
	}
	return &sovereignDeps{core: core, dyn: dyn}, nil
}

// ── GVRs the local-cluster endpoints read ─────────────────────────────────

// helmReleaseGVR — flux v2.4 stable HelmRelease API. Mirrors the
// canonical GVR in internal/helmwatch.HelmReleaseGVR; redeclared here
// to avoid the import cycle (helmwatch already imports the handler
// package transitively via the Job bridge wiring).
var helmReleaseGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// applicationGVR — apps.openova.io/v1 Application CR. Each per-Sovereign
// workload has an Application record carrying the `spec.environmentRef`
// field that drives the AppsPage card's environment chip (qa-loop iter-7
// TC-090). Catalyst joins the catalog/HelmRelease rows with Application
// CRs by slug — when an Application matches, its environmentRef is
// surfaced on the sovereignAppItem; otherwise the row falls back to the
// defaultSovereignEnvironment chip ("dev" out of the box).
//
// Resolved via the canonical ApplicationGVR() helper in applications.go
// so the version stays aligned with the rest of the handler suite (the
// chart's qa-fixtures Application uses apps.openova.io/v1; the
// k8scache kinds.go entry still references v1alpha1 for legacy reasons,
// but the live CRD is v1).
var applicationGVR = ApplicationGVR()

// defaultSovereignEnvironment is the chip the AppsPage renders for any
// app row that has no matching Application CR with a populated
// spec.environmentRef. Single-environment Sovereigns (the common case
// today, including omantel.biz) ship with everything in "dev"; multi-
// environment Sovereigns surface the per-app environment as soon as
// their Application CRs declare it. The matrix's TC-090 expectation
// (Apps page must contain "dev") locks this default in place — the
// chip MUST always render with a usable environment label.
const defaultSovereignEnvironment = "dev"

// httpRouteGVR — Gateway API HTTPRoute. The canonical Sovereign
// install uses Cilium Gateway as the only ingress; Console / Organization /
// per-blueprint front-doors all surface as HTTPRoutes. We list both
// HTTPRoutes and Ingresses so a Sovereign that hasn't fully
// migrated to Gateway API still surfaces its operator-visible
// front-doors on the Cloud page.
var httpRouteGVR = schema.GroupVersionResource{
	Group:    "gateway.networking.k8s.io",
	Version:  "v1",
	Resource: "httproutes",
}

// ── /api/v1/sovereign/status — Dashboard summary ──────────────────────────

type sovereignStatus struct {
	HelmReleasesReady   int `json:"helmReleasesReady"`
	HelmReleasesTotal   int `json:"helmReleasesTotal"`
	PodsRunning         int `json:"podsRunning"`
	PodsTotal           int `json:"podsTotal"`
	CertExpirySoonCount int `json:"certExpirySoonCount"`
}

// HandleSovereignStatus — GET /api/v1/sovereign/status.
// Aggregate counts for the three Dashboard cards.
func (h *Handler) HandleSovereignStatus(w http.ResponseWriter, r *http.Request) {
	deps, err := h.sovereignDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "in-cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	status := sovereignStatus{}

	// HelmReleases — count Ready=True. The list is unbounded by
	// namespace because bp-catalyst-platform's chart reconciles into
	// {auth, catalyst, openbao, ...} and a flat "all bp-* HRs" view
	// is the operator's mental model.
	hrList, err := deps.dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err == nil {
		status.HelmReleasesTotal = len(hrList.Items)
		for _, hr := range hrList.Items {
			if helmReleaseReady(&hr) {
				status.HelmReleasesReady++
			}
		}
	}

	// Pods — running vs total across the cluster.
	pods, err := deps.core.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err == nil {
		status.PodsTotal = len(pods.Items)
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning {
				status.PodsRunning++
			}
		}
	}

	// Certs expiring within 30 days. We treat any cert-manager
	// Certificate whose Ready condition is False AND whose status
	// reason mentions Expir* OR whose status.notAfter is within 30d
	// as "expiring soon". Today we use the simpler signal: count
	// Certificate resources whose Ready=False — works on every
	// Sovereign that has cert-manager (every Sovereign does).
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	certs, err := deps.dyn.Resource(certGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err == nil {
		now := time.Now()
		for _, c := range certs.Items {
			if certExpirySoon(&c, now) {
				status.CertExpirySoonCount++
			}
		}
	}

	writeJSON(w, http.StatusOK, status)
}

// helmReleaseReady inspects a HelmRelease's status.conditions slice
// for type=Ready, status=True. Defensive against unset-status cases
// (a reconcile that hasn't fired yet) — those count as not-ready.
func helmReleaseReady(u *unstructured.Unstructured) bool {
	conds, ok, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if !ok || err != nil {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Ready" && m["status"] == "True" {
			return true
		}
	}
	return false
}

// certExpirySoon classifies a cert-manager Certificate as
// expiring-soon. Returns true when:
//   - Ready=False (re-issuance failed / pending); or
//   - status.notAfter is within 30 days of now.
func certExpirySoon(u *unstructured.Unstructured, now time.Time) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Ready" && m["status"] != "True" {
			return true
		}
	}
	notAfterStr, ok, _ := unstructured.NestedString(u.Object, "status", "notAfter")
	if ok && notAfterStr != "" {
		if t, err := time.Parse(time.RFC3339, notAfterStr); err == nil {
			if t.Sub(now) <= 30*24*time.Hour {
				return true
			}
		}
	}
	return false
}

// ── /api/v1/sovereign/jobs — Job history populated from local cluster ─────

// sovereignJobItem is the row shape rendered by the Console Jobs page.
// Compatible with the existing JobsTable component shape but
// stripped of the deployment-id-scoped fields the per-deployment
// endpoint serves on the mothership.
type sovereignJobItem struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

type sovereignJobsResponse struct {
	Jobs []sovereignJobItem `json:"jobs"`
}

// HandleSovereignJobs — GET /api/v1/sovereign/jobs.
//
// Three signal sources, sorted started-DESC:
//
//  1. Flux HelmReleases — every reconcile result (Ready / NotReady)
//     surfaced as a "reconcile" job row. Captures the bootstrap-kit
//     install events the operator just lived through.
//  2. K8s Jobs across all namespaces — captures Helm post-install
//     hooks (e.g. bp-keycloak-zero-bootstrap) and any operator-
//     authored CronJob / Job runs.
//  3. K8s Events — operator-visible warnings (recent Failed/Unhealthy
//     reasons). Bounded to last 100 to keep the response small.
//
// The endpoint is read-only and idempotent.
func (h *Handler) HandleSovereignJobs(w http.ResponseWriter, r *http.Request) {
	deps, err := h.sovereignDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "in-cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	out := []sovereignJobItem{}

	// FINITE-ONLY (#3896 / #3925): the Jobs surface lists FINITE work —
	// batch Jobs + warning Events. Flux HelmReleases are CONTINUOUS
	// reconcilers (they never "finish"); they belong on the Cloud
	// Reconciliation lens / reconciler-management surface (#3996), not on
	// /jobs. Listing all ~80 HRs here is exactly the "83 HelmReleases
	// mislabeled as LIFECYCLE jobs" pollution #3896 calls out — and it
	// happens through THIS legacy endpoint even though the primary
	// per-deployment /jobs path already drops them via
	// jobs.FilterFiniteJobs. Skip HRs here so the two surfaces agree —
	// HelmRelease status lives on the recon surface (ListReconcilers).

	// 1. K8s Jobs.
	if jobs, err := deps.core.BatchV1().Jobs("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, j := range jobs.Items {
			out = append(out, k8sJobToJob(&j))
		}
	}

	// 2. K8s Events (warnings only).
	if events, err := deps.core.CoreV1().Events("").List(ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
		Limit:         100,
	}); err == nil {
		for _, e := range events.Items {
			out = append(out, eventToJob(&e))
		}
	}

	// Sort by StartedAt DESC, with zero-time items bucketed last.
	sort.Slice(out, func(i, j int) bool {
		ai, bi := out[i].StartedAt, out[j].StartedAt
		if ai.IsZero() && !bi.IsZero() {
			return false
		}
		if !ai.IsZero() && bi.IsZero() {
			return true
		}
		return ai.After(bi)
	})

	writeJSON(w, http.StatusOK, sovereignJobsResponse{Jobs: out})
}

// k8sJobToJob maps a batch/v1 Job into the Console Job row shape.
func k8sJobToJob(j *batchv1.Job) sovereignJobItem {
	status := "running"
	for _, c := range j.Status.Conditions {
		switch c.Type {
		case "Complete":
			if c.Status == corev1.ConditionTrue {
				status = "succeeded"
			}
		case "Failed":
			if c.Status == corev1.ConditionTrue {
				status = "failed"
			}
		}
	}
	if status == "running" && j.Status.Active == 0 && j.Status.Succeeded == 0 && j.Status.Failed == 0 {
		status = "pending"
	}
	started := time.Time{}
	if j.Status.StartTime != nil {
		started = j.Status.StartTime.Time
	} else {
		started = j.CreationTimestamp.Time
	}
	finished := time.Time{}
	if j.Status.CompletionTime != nil {
		finished = j.Status.CompletionTime.Time
	}
	return sovereignJobItem{
		ID:         fmt.Sprintf("job/%s/%s", j.Namespace, j.Name),
		Name:       j.Name,
		Namespace:  j.Namespace,
		Kind:       "Job",
		Status:     status,
		StartedAt:  started,
		FinishedAt: finished,
	}
}

// eventToJob maps a K8s Event (warning only) into a Job row so
// operator-visible cluster anomalies surface alongside HelmReleases.
func eventToJob(e *corev1.Event) sovereignJobItem {
	involved := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
	if e.InvolvedObject.Namespace != "" && e.InvolvedObject.Namespace != e.Namespace {
		involved = e.InvolvedObject.Namespace + "/" + involved
	}
	started := e.FirstTimestamp.Time
	if started.IsZero() && !e.EventTime.Time.IsZero() {
		started = e.EventTime.Time
	}
	if started.IsZero() {
		started = e.CreationTimestamp.Time
	}
	return sovereignJobItem{
		ID:        fmt.Sprintf("event/%s/%s", e.Namespace, e.Name),
		Name:      e.Reason + ": " + involved,
		Namespace: e.Namespace,
		Kind:      "Event",
		Status:    "warning",
		Message:   e.Message,
		StartedAt: started,
	}
}

// ── /api/v1/sovereign/apps — installable catalog + installed status ───────

type sovereignAppItem struct {
	ID           string   `json:"id"`
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	Category     string   `json:"category,omitempty"`
	Tagline      string   `json:"tagline,omitempty"`
	Tags         []string `json:"tags"`
	Version      string   `json:"version,omitempty"`
	Section      string   `json:"section,omitempty"`
	Depends      []string `json:"depends"`
	Status       string   `json:"status"` // installed | available | bootstrap
	BootstrapKit bool     `json:"bootstrapKit"`

	// Environment — deployment environment this app row is associated
	// with on this Sovereign. Sourced from the matching Application
	// CR's spec.environmentRef when present (apps.openova.io/v1alpha1);
	// falls back to "dev" otherwise so single-environment Sovereigns
	// (the common case) always display a usable chip on the AppsPage
	// card. The FE renders this verbatim as the environment chip
	// alongside FREE/BOOTSTRAP. Closes qa-loop iter-7 TC-090 (Apps
	// page lost the environment chip during the Organization-publish redesign).
	Environment string `json:"environment,omitempty"`

	// MarketplacePublished — null when the slug isn't in the Organization
	// catalog (bootstrap components, or marketplace not deployed on
	// this Sovereign). When non-null, the FE renders a Publish chip
	// on the app card. The deleted /catalog page's per-row toggle has
	// moved here. Toggled via PATCH /api/v1/sovereign/apps/{slug}/publish.
	MarketplacePublished *bool `json:"marketplacePublished,omitempty"`

	// ExternalURL — front-door URL the operator clicks to open this
	// installed Application in a new browser tab (G90, 2026-06-01,
	// founder UX gap on hw86: every installed app showed INSTALLED but
	// had no clickable launch affordance). Populated by joining the
	// installed HelmRelease's (targetNamespace, releaseName) against
	// every Gateway-API HTTPRoute on the cluster; the first HTTPRoute
	// whose name OR backend Service name matches the release wins, and
	// we surface `https://<spec.hostnames[0]>`.
	//
	// Empty when:
	//   - the app has no HTTPRoute (controllers, operators, bootstrap
	//     internals that don't expose a UI surface)
	//   - the HTTPRoute lives in a namespace this lookup didn't reach
	//   - Gateway API isn't installed on the Sovereign yet
	//
	// FE renders an "Open" button on the card when non-empty; otherwise
	// the card is unchanged. Behind the URL each upstream chart is
	// already wired as a Keycloak OIDC client so the click lands the
	// operator with their existing SSO session active — no extra wiring
	// needed on the FE.
	ExternalURL string `json:"externalURL,omitempty"`

	// ── #3370 — instance cards ──────────────────────────────────────
	// Instance=true marks a row that is one RUNNING INSTANCE
	// (Application CR) rather than a blueprint/catalog row. A blueprint
	// with N instances ⇒ exactly N such rows; the FE renders one card
	// per instance (`shared-pg · PostgreSQL · singleton · ● Ready ·
	// ⛓ 3 contexts`) and suppresses the blueprint-level duplicate.
	Instance bool `json:"instance,omitempty"`
	// Blueprint — the instance's class (full bp-<name> id), for the
	// FE's catalog-metadata join (title, logo, family).
	Blueprint string `json:"blueprint,omitempty"`
	// Topology — the instance's resolved placement/topology chip.
	Topology string `json:"topology,omitempty"`
	// ContextCount — number of Contexts declared on this (shareable)
	// instance; drives the ⛓ badge that deep-links to the Contexts
	// tab. 0 renders no badge.
	ContextCount int `json:"contextCount,omitempty"`
}

type sovereignAppsResponse struct {
	Apps         []sovereignAppItem `json:"apps"`
	GeneratedAt  string             `json:"generatedAt,omitempty"`
	BootstrapKit []string           `json:"bootstrapKit"`
}

// HandleSovereignApps — GET /api/v1/sovereign/apps.
//
// Marketplace card grid for the Sovereign Console. Sources:
//
//   - Catalog: internal/catalog (embedded blueprints.json) — the
//     SAME data the wizard's StepComponents renders.
//   - Installed: HelmReleases on the local cluster, matched to
//     blueprints by name (HR name "bp-<slug>" maps directly to
//     catalog id "bp-<slug>").
//
// Each row carries a status field:
//   - "installed"  — HR present & Ready=True
//   - "installing" — HR present, Ready=False (or absent condition)
//   - "bootstrap"  — visibility=unlisted AND part of bootstrap kit
//     (these always render as installed; the operator
//     cannot uninstall the spine)
//   - "available"  — listed catalog entry, no matching HR yet
func (h *Handler) HandleSovereignApps(w http.ResponseWriter, r *http.Request) {
	listed, err := catalog.ListedBlueprints()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "catalog-load-failed",
			"detail": err.Error(),
		})
		return
	}
	bootstrap, err := catalog.BootstrapKit()
	if err != nil {
		// non-fatal: a missing bootstrap-kit entry just means
		// nothing renders as bootstrap. The catalog blueprints
		// still ship.
		bootstrap = nil
	}

	// Map HR name → ready/notReady. Best-effort: a Sovereign with
	// no in-cluster client (CI) gets "available" for every entry,
	// which is the safe, non-misleading fallback.
	installed := map[string]string{} // hr name → "installed" | "installing"
	// Map slug → environmentRef harvested from Application CRs. When
	// an app row's slug matches, the chip on the FE displays the
	// per-Application environment (e.g. "prod", "staging"); otherwise
	// the chip falls back to defaultSovereignEnvironment ("dev").
	// Best-effort: missing CR / RBAC / list error → empty map →
	// every row falls back, which is the target-state behaviour for
	// single-environment Sovereigns.
	envBySlug := map[string]string{}
	// hrTarget — per-HR (targetNamespace, releaseName) keyed by HR name.
	// Needed by the externalURL join below to point an HTTPRoute lookup
	// at the actual install location instead of guessing. G90 2026-06-01.
	type hrTarget struct {
		targetNamespace string
		releaseName     string
		// blueprint — the chart name (bp-<slug>) backing this HR, captured
		// so the externalURL projection can run the SAME user-UI gate the
		// per-app detail endpoint uses (externalURLIfUserUI →
		// blueprintHasUserUIEndpoint, #3224 / #3374). Without the gate the
		// grid Open button would render for any app that merely owns an
		// HTTPRoute, including API/protocol-only backends (the dead-button
		// shape). Empty when the HR declares no chart name.
		blueprint string
	}
	hrInfo := map[string]hrTarget{}
	// urlByHRName — front-door URL for each installed HR, populated by
	// joining each HR's (targetNamespace, releaseName) against the
	// cluster's HTTPRoute set. G90 2026-06-01 — feeds sovereignAppItem.ExternalURL
	// so the AppsPage card can render an "Open" launch button next to
	// the INSTALLED badge.
	urlByHRName := map[string]string{}
	// #3370 — instance cards. Every Application CR projects as its own
	// card row; bootstrap rows whose HR is ADOPTED by a self-registered
	// CR (spec.helmRelease) are suppressed so one running instance is
	// exactly one card.
	var appCRs []unstructured.Unstructured
	// #3370 — the full HelmRelease list is retained so bootstrap-kit
	// shareable instances (shared-pg / -b / -c on a zero-touch prov, which
	// have NO companion Application CR) can ALSO project as instance cards
	// below. Mirrors HandleListBlueprintInstances' bootstrapHRInstances
	// fallback — without it, /apps shows zero instance cards on a converged
	// prov even though /catalyst/v1/catalog/<bp>/instances renders them.
	var hrItems []unstructured.Unstructured
	instanceRows := []sovereignAppItem{}
	adoptedHRs := map[string]bool{}
	deps, depsErr := h.sovereignDepsFor()
	if depsErr == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if hrList, err := deps.dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
			hrItems = hrList.Items
			for _, hr := range hrList.Items {
				name := hr.GetName()
				if helmReleaseReady(&hr) {
					installed[name] = "installed"
				} else {
					installed[name] = "installing"
				}
				tn, _, _ := unstructured.NestedString(hr.Object, "spec", "targetNamespace")
				if tn == "" {
					if sn, _, _ := unstructured.NestedString(hr.Object, "spec", "storageNamespace"); sn != "" {
						tn = sn
					} else {
						tn = hr.GetNamespace()
					}
				}
				rn, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
				if rn == "" {
					rn = name
				}
				// Chart name backs the user-UI gate below. Flux stores it at
				// spec.chart.spec.chart; bootstrap-kit HR names are bp-<slug>
				// so we fall back to the HR name when the chart field is
				// absent.
				chart, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
				if chart == "" {
					chart = name
				}
				hrInfo[name] = hrTarget{targetNamespace: tn, releaseName: rn, blueprint: chart}
			}
		}
		// Build the HTTPRoute index: (namespace, name) → "https://<host>"
		// plus (namespace, backend-service-name) → "https://<host>" so
		// the join below matches whether the chart named its HTTPRoute
		// after the release (the common case — gitea, harbor, grafana,
		// keycloak, openbao, marketplace, guacamole on hw86) or after a
		// different sub-component (e.g. harbor's HTTPRoute named
		// `harbor` but releaseName also `harbor`). Empty hostnames are
		// skipped — a route with no hostname can't be launched.
		hrURL := map[[2]string]string{}
		if hrlist, err := deps.dyn.Resource(httpRouteGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, rt := range hrlist.Items {
				hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
				if len(hosts) == 0 || hosts[0] == "" {
					continue
				}
				url := "https://" + hosts[0]
				ns := rt.GetNamespace()
				hrURL[[2]string{ns, rt.GetName()}] = url
				// Index by backendRef name too — handles the case where
				// the HTTPRoute is named differently from the Service it
				// fronts (e.g. `<release>-http`, `<release>-public`).
				rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
				for _, rl := range rules {
					rm, isMap := rl.(map[string]interface{})
					if !isMap {
						continue
					}
					refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
					for _, ref := range refs {
						rfm, isMap := ref.(map[string]interface{})
						if !isMap {
							continue
						}
						svcName, _, _ := unstructured.NestedString(rfm, "name")
						if svcName == "" {
							continue
						}
						if _, exists := hrURL[[2]string{ns, svcName}]; !exists {
							hrURL[[2]string{ns, svcName}] = url
						}
					}
				}
			}
		}
		// #3374 — gate the grid Open button on the SAME user-UI signal the
		// AppDetail launch button uses (userUIGatePasses →
		// blueprintHasUserUIEndpoint, #3224). An HTTPRoute existing for an HR
		// is necessary but NOT sufficient: API/protocol-only backends
		// (bp-newapi, bp-openova-flow-server) also own routes, and projecting
		// their externalURL here would render a dead Open button that dumps
		// the operator on a login form / 404. The gate fails OPEN when the
		// catalog is unwired (chroot/CI) so working buttons are never
		// suppressed fleet-wide.
		//
		// Memoize the decision per BLUEPRINT so this hot list path (the FE
		// polls /apps every 5s) issues at most ONE catalog round-trip per
		// UNIQUE chart — not one per HR. Without the memo, N bootstrap HRs
		// (+ the shared-pg/-b/-c instances that share a chart) would each
		// re-fetch under the single shared ctx deadline; whichever HRs were
		// iterated last when a slow catalog exhausted the budget would
		// non-deterministically lose their Open button (Go map order is
		// random). The unique-chart set is tiny (grafana, harbor, openbao,
		// gitea, keycloak, guacamole, powerdns-admin) so this stays well
		// inside the 10s budget.
		gateMemo := map[string]bool{}
		gatePasses := func(blueprint string) bool {
			if v, seen := gateMemo[blueprint]; seen {
				return v
			}
			v := h.userUIGatePasses(ctx, blueprint)
			gateMemo[blueprint] = v
			return v
		}
		for hrName, info := range hrInfo {
			// #3931 — resolve the front-door route by (namespace, releaseName).
			// Pre-#3642 the app's HTTPRoute shared its targetNamespace, so a
			// single lookup sufficed. Post-#3642 the 7 front-door apps moved
			// INTO the mgmt vCluster and the syncer mirrors each route onto the
			// HOST under the vCluster sync namespace (`mgmt`/`dmz`/`rtz`), NOT
			// the app's in-vCluster targetNamespace (gitea/grafana/openbao/…).
			// Probe targetNamespace FIRST (the host-placed apps + the canonical
			// pre-#3642 layout), then fall back to the well-known vCluster sync
			// namespaces. Same widening — and same collision-free guarantee
			// (each app owns a distinct `<release>.<fqdn>` host) — as
			// lookupExternalURL's routeNamespaceMatchesApp. Without this the
			// AppsPage grid Open button vanishes for every mgmt-vCluster app.
			url, ok := hrURL[[2]string{info.targetNamespace, info.releaseName}]
			if !ok {
				for _, syncNS := range vClusterHostSyncNamespaces {
					if syncNS == info.targetNamespace {
						continue
					}
					if u, hit := hrURL[[2]string{syncNS, info.releaseName}]; hit {
						url, ok = u, true
						break
					}
				}
			}
			if !ok {
				continue
			}
			if !gatePasses(info.blueprint) {
				continue
			}
			urlByHRName[hrName] = url
		}
		// Application CR pass — separate List so a missing CRD or
		// RBAC denial on apps.openova.io doesn't break the HR pass
		// above (which is the load-bearing one for status).
		if appList, err := deps.dyn.Resource(applicationGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, app := range appList.Items {
				// #3370 — every Application CR is one RUNNING INSTANCE
				// and projects as its OWN card row (the canonical G117
				// class≠instance model). Built below after this pass so
				// the envBySlug join still applies first.
				appCRs = append(appCRs, app)

				env, _, _ := unstructured.NestedString(app.Object, "spec", "environmentRef")
				if env == "" {
					continue
				}
				// Application CR's name is the slug; spec.blueprintRef
				// could also drive the join but the name is the canonical
				// per-app identifier in the wizard catalog.
				slug := app.GetName()
				if existing, ok := envBySlug[slug]; ok && existing != env {
					// Multiple Applications for the same slug across
					// environments — keep the first deterministic
					// non-empty hit (alphabetical iteration over
					// namespaces). Multi-env-per-slug surfaces in a
					// later UI iteration via the cross-Sov view.
					continue
				}
				envBySlug[slug] = env
			}
		}

		// #3370 — project every Application CR as one instance card.
		// Contexts count from the instance's declared IaC values via
		// the Blueprint's contextSchema (generic, declaration-only).
		ctxSchemaByBP := map[string]*contextSchemaDecl{}
		for i := range appCRs {
			app := &appCRs[i]
			bpFull, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
			slug := strings.TrimPrefix(bpFull, "bp-")
			schema, cached := ctxSchemaByBP[bpFull]
			if !cached {
				schema = h.contextSchemaFor(ctx, bpFull)
				ctxSchemaByBP[bpFull] = schema
			}
			contextCount := 0
			if schema != nil && schema.ValuesKey != "" {
				if params, ok, _ := unstructured.NestedMap(app.Object, "spec", "parameters"); ok {
					if entries, ok := params[schema.ValuesKey].([]interface{}); ok {
						contextCount = len(entries)
					}
				}
			}
			phase, _, _ := unstructured.NestedString(app.Object, "status", "phase")
			status := "installing"
			if phase == "Ready" {
				status = "installed"
			}
			bootstrapOwned, _, _ := unstructured.NestedBool(app.Object, "spec", "bootstrap")
			externalURL := ""
			if hrName, _, _ := unstructured.NestedString(app.Object, "spec", "helmRelease", "name"); hrName != "" {
				adoptedHRs[hrName] = true
				externalURL = urlByHRName[hrName]
				// #4889 — HR-Ready overlay for adopted spine/bootstrap CRs.
				// The Application CR status.phase lags/flaps behind Flux (the
				// app-controller can sit at Provisioning/Failed while the
				// adopted HelmRelease is already Ready=True). When the adopted
				// HR (installed map, built from helmReleaseReady above) is
				// Ready, prefer it — mirrors the detail endpoint's C4-003 /
				// #4889 overlay so the grid card doesn't render FAILED/
				// installing while the HelmRelease is Ready.
				if status != "installed" && installed[hrName] == "installed" {
					status = "installed"
				}
			}
			// #4897 — spec.placement is dual-form: a legacy posture STRING or
			// the object {mode, regions, …}. readTopology handles both (the
			// object posture rides in .mode), so an active-hot-standby instance
			// created via Catalog→New-instance (which writes the OBJECT form)
			// gets its topology badge, same as string-form spine AHS apps. A
			// raw NestedString read collapsed the object to "" → no badge.
			topology := readTopology(app)
			instanceRows = append(instanceRows, sovereignAppItem{
				ID:           app.GetName(),
				Slug:         slug,
				Title:        app.GetName(),
				Status:       status,
				BootstrapKit: bootstrapOwned,
				Environment:  resolveAppEnvironment(envBySlug, app.GetName()),
				ExternalURL:  externalURL,
				Instance:     true,
				Blueprint:    bpFull,
				Topology:     topology,
				ContextCount: contextCount,
			})
		}

		// #3370 — project bootstrap-kit SHAREABLE HelmReleases as instance
		// cards too. On a zero-touch converged prov the shared-pg / -b / -c
		// instances exist ONLY as Flux HelmReleases (no companion
		// Application CR yet — the self-registered CR materialises on the
		// first post-bootstrap upgrade after the Application CRD lands), so
		// the Application-CR pass above projects zero instance rows. Without
		// this fallback /apps shows no shared-pg cards + no ⛓ Contexts badge
		// even though /catalyst/v1/catalog/<bp>/instances already renders
		// them (the bootstrapHRInstances fallback in HandleListBlueprintInstances).
		// This is the GENERIC mechanism — any shareable Blueprint (postgres,
		// valkey, kafka, seaweedfs) whose HR declares Contexts under its
		// contextSchema valuesKey projects here with zero per-service code.
		//
		// Dedup: a shareable HR ADOPTED by a self-registered Application CR
		// (adoptedHRs, keyed by spec.helmRelease.name) is already projected
		// above — skip it so one running instance is exactly one card. We
		// also skip when an Application-CR instance row already carries the
		// HR's releaseName (the instance identity the self-registered CR is
		// named after).
		seenInstanceName := map[string]bool{}
		for _, ir := range instanceRows {
			seenInstanceName[strings.ToLower(ir.ID)] = true
		}
		for i := range hrItems {
			hr := &hrItems[i]
			hrName := hr.GetName()
			if adoptedHRs[hrName] {
				continue
			}
			chart, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
			ctxSchema := h.contextSchemaFor(ctx, chart)
			if ctxSchema == nil || ctxSchema.ValuesKey == "" {
				// non-shareable chart → not an instance card (it stays a
				// regular installed/bootstrap row below).
				continue
			}
			// Instance identity = spec.releaseName (the Helm release IS the
			// instance — shared-pg). Fall back to the HR name with bp-
			// stripped (Flux default: release name = HR name).
			instName, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
			if instName == "" {
				instName = strings.TrimPrefix(hrName, "bp-")
			}
			if seenInstanceName[strings.ToLower(instName)] {
				continue
			}
			seenInstanceName[strings.ToLower(instName)] = true
			bpFull := chart
			if !strings.HasPrefix(bpFull, "bp-") {
				bpFull = "bp-" + bpFull
			}
			slug := strings.TrimPrefix(bpFull, "bp-")
			values, _, _ := unstructured.NestedMap(hr.Object, "spec", "values")
			contextCount := 0
			if entries, ok := values[ctxSchema.ValuesKey].([]interface{}); ok {
				contextCount = len(entries)
			}
			status := "installing"
			if helmReleaseReady(hr) {
				status = "installed"
			}
			instanceRows = append(instanceRows, sovereignAppItem{
				ID:           instName,
				Slug:         slug,
				Title:        instName,
				Status:       status,
				BootstrapKit: true,
				Environment:  resolveAppEnvironment(envBySlug, instName),
				ExternalURL:  urlByHRName[hrName],
				Instance:     true,
				Blueprint:    bpFull,
				Topology:     readTopologyFromValues(values),
				ContextCount: contextCount,
			})
			// Suppress the bootstrap SLOT row for this HR below — it is now
			// represented by its instance card (one physical instance = one
			// card). The slot row keys off the HR's catalog id (bp-<slug>),
			// which is the HR name for bootstrap-kit installs.
			adoptedHRs[hrName] = true
		}
	}

	bootstrapIDs := map[string]bool{}
	bootstrapList := make([]string, 0, len(bootstrap))
	for _, b := range bootstrap {
		bootstrapIDs[b.ID] = true
		bootstrapList = append(bootstrapList, b.ID)
	}

	apps := make([]sovereignAppItem, 0, len(listed)+len(bootstrap)+len(instanceRows))

	// #3370 — instance cards first: one card per RUNNING INSTANCE
	// (Application CR), of everything. A blueprint with N instances ⇒
	// exactly N cards.
	apps = append(apps, instanceRows...)

	// Bootstrap-kit always rendered first as "installed" (unlisted
	// blueprints don't appear in `listed`). The operator sees them
	// as part of the catalog, marked "bootstrap" so they can't be
	// uninstalled via UI.
	for _, b := range bootstrap {
		// #3370 — an HR adopted by a self-registered Application CR is
		// already projected as its instance card above; rendering the
		// slot row too would show one physical instance as two cards.
		if adoptedHRs[b.ID] {
			continue
		}
		apps = append(apps, sovereignAppItem{
			ID:           b.ID,
			Slug:         b.Slug,
			Title:        humanizeSlug(b.Slug),
			Summary:      "",
			Status:       "bootstrap",
			BootstrapKit: true,
			Environment:  resolveAppEnvironment(envBySlug, b.Slug),
			ExternalURL:  urlByHRName[b.ID],
		})
	}

	for _, b := range listed {
		// Skip duplicates that already appeared as bootstrap.
		if bootstrapIDs[b.ID] {
			continue
		}
		status := "available"
		if s, ok := installed[b.ID]; ok {
			status = s
		}
		apps = append(apps, sovereignAppItem{
			ID:           b.ID,
			Slug:         b.Slug,
			Title:        b.Title,
			Summary:      b.Summary,
			Category:     deref(b.Category),
			Tagline:      deref(b.Tagline),
			Tags:         b.Tags,
			Version:      deref(b.Version),
			Section:      deref(b.Section),
			Depends:      b.Depends,
			Status:       status,
			BootstrapKit: false,
			Environment:  resolveAppEnvironment(envBySlug, b.Slug),
			ExternalURL:  urlByHRName[b.ID],
		})
	}

	// Best-effort Organization catalog join. When marketplace.enabled=false on
	// this Sovereign, the Organization catalog service is not deployed; the
	// client returns (nil, nil) and every app stays with
	// MarketplacePublished=nil (the FE then suppresses the Publish
	// chip). When Organization IS deployed and reachable, decorate matching
	// slugs with the published flag.
	orgCtx, orgCancel := context.WithTimeout(r.Context(), orgCatalogProbeBudget)
	defer orgCancel()
	if pub, _ := orgCatalog().PublishedBySlug(orgCtx); pub != nil {
		for i := range apps {
			if v, ok := pub[apps[i].Slug]; ok {
				vCopy := v
				apps[i].MarketplacePublished = &vCopy
			}
		}
	}

	gen, _ := catalog.GeneratedAt()
	writeJSON(w, http.StatusOK, sovereignAppsResponse{
		Apps:         apps,
		GeneratedAt:  gen,
		BootstrapKit: bootstrapList,
	})
}

// resolveAppEnvironment returns the environment chip the FE renders on
// the AppsPage card for a given slug. Uses the per-Application CR
// environmentRef when populated; otherwise falls back to
// defaultSovereignEnvironment ("dev"). Always returns a non-empty string
// — the matrix's TC-090 contract requires every app card to display an
// environment chip.
func resolveAppEnvironment(envBySlug map[string]string, slug string) string {
	if env, ok := envBySlug[slug]; ok && env != "" {
		return env
	}
	return defaultSovereignEnvironment
}

// HandleSovereignAppPublish — PATCH /api/v1/sovereign/apps/{slug}/publish.
//
// Operator-admin toggle to publish/unpublish a SaaS app on the
// Sovereign's marketplace. Replaces the deleted /catalog page's
// per-row toggle: the chip lives on each AppsPage card now. The
// chroot's catalyst-api proxies the PATCH to the in-cluster Organization
// catalog service at http://catalog.org-services.svc.cluster.local:8082 —
// same data plane the deleted /catalog page used.
//
// 503 when the Organization catalog is not deployed on this Sovereign
// (marketplace.enabled=false in the chart values), or when the
// Organization HS256 bridge secret is not wired (org-services-secrets reflection
// not yet reconciled). 401 when the operator's session has no
// claims attached (auth middleware bypassed). 404 when the slug
// isn't in the Organization catalog. The upstream status is surfaced verbatim.
//
// Body shape: {"published": true|false}.
//
// Auth seam (Closes #1735): the upstream catalog runs JWTAuth on
// every /catalog/admin/* path and requireAdmin on this specific
// handler. Forwarding the operator's Keycloak RS256 session header
// to the catalog 401s upstream (catalog only accepts HS256 signed
// with org-services-secrets/JWT_SECRET, just like the rest of the Organization mesh).
// Mint a fresh HS256 bridge token via the same h.mintOrgBridgeToken
// helper org_billing_vouchers.go uses for the BSS Vouchers surface
// and forward THAT as the upstream Authorization header. Operators
// with `catalyst-owner` realm-role (per OrgRoleFor) get role=
// "superadmin" claimed in the bridge token, satisfying requireAdmin.
func (h *Handler) HandleSovereignAppPublish(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-slug",
		})
		return
	}
	var body struct {
		Published bool `json:"published"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "bad-body",
			"detail": err.Error(),
		})
		return
	}
	// Mint the canonical Organization bridge token so the upstream catalog's
	// JWTAuth + requireAdmin admit this hop. Pre-bridge state (no
	// Authorization header) ⇒ JWTAuth rejects with 401 "missing or
	// invalid authorization header" — that's the C4-012 / #1735
	// symptom. The bridge mirrors org_billing_vouchers.go's
	// mintOrgBridgeToken pattern: RS256 operator session → HS256
	// token signed with org-services-secrets/JWT_SECRET, role mapped via
	// sharedauth.OrgRoleFor. The upstream catalog's requireAdmin was
	// widened in the same PR to accept "sovereign-admin" so a
	// franchisee operator can manage their own Sovereign's
	// marketplace catalog without a Catalyst-Zero ("superadmin")
	// role. When the bridge is unwired (Sovereign without
	// marketplace, or stale chart) the helper returns 503 which we
	// surface verbatim so the FE renders an actionable
	// "marketplace not enabled" message.
	bearer, status, errResp := h.mintOrgBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), orgCatalogProbeBudget)
	defer cancel()
	upstreamStatus, err := orgCatalog().SetPublished(ctx, slug, body.Published, bearer)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "org-catalog-unreachable",
			"detail": err.Error(),
		})
		return
	}
	if upstreamStatus >= 200 && upstreamStatus < 300 {
		writeJSON(w, http.StatusOK, map[string]any{
			"slug":      slug,
			"published": body.Published,
		})
		return
	}
	writeJSON(w, upstreamStatus, map[string]string{
		"error":  "org-catalog-rejected",
		"detail": fmt.Sprintf("upstream returned %d", upstreamStatus),
	})
}

// ── /api/v1/sovereign/cloud — local-cluster topology surface ──────────────

type sovereignCloudResponse struct {
	Nodes          []sovereignNode      `json:"nodes"`
	Namespaces     []sovereignNamespace `json:"namespaces"`
	Ingresses      []sovereignIngress   `json:"ingresses"`
	HTTPRoutes     []sovereignIngress   `json:"httpRoutes"`
	LoadBalancers  []sovereignLB        `json:"loadBalancers"`
	StorageClasses []sovereignSC        `json:"storageClasses"`
	PVCs           []sovereignPVC       `json:"pvcs"`
}

type sovereignNode struct {
	// Cluster — multi-region fan-out tag (D16 PR D, 2026-05-17). When the
	// Sovereign Console has secondary kubeconfigs registered with the
	// chroot's k8sCache (via /api/v1/sovereign/secondary-kubeconfig posted
	// at handover), HandleSovereignCloud enumerates nodes / LBs / SCs /
	// PVCs from every registered cluster and tags each row with its
	// cluster id (e.g., "primary", "nbg1-1", "sin-2") so the operator
	// can group/filter by region. omitempty for backward compat with
	// single-cluster Sovereigns.
	Cluster        string            `json:"cluster,omitempty"`
	Name           string            `json:"name"`
	Status         string            `json:"status"`
	Roles          []string          `json:"roles"`
	KubeletVersion string            `json:"kubeletVersion"`
	OS             string            `json:"os"`
	Arch           string            `json:"arch"`
	InternalIP     string            `json:"internalIP"`
	ExternalIP     string            `json:"externalIP,omitempty"`
	CapacityCPU    string            `json:"capacityCPU"`
	CapacityMemory string            `json:"capacityMemory"`
	Labels         map[string]string `json:"labels,omitempty"`
}

type sovereignNamespace struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type sovereignIngress struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Hosts     []string `json:"hosts"`
	URL       string   `json:"url,omitempty"`
}

type sovereignLB struct {
	Cluster    string   `json:"cluster,omitempty"`
	Name       string   `json:"name"`
	Namespace  string   `json:"namespace"`
	Type       string   `json:"type"`
	ClusterIP  string   `json:"clusterIP,omitempty"`
	ExternalIP string   `json:"externalIP,omitempty"`
	Ports      []string `json:"ports"`
}

type sovereignSC struct {
	Cluster       string `json:"cluster,omitempty"`
	Name          string `json:"name"`
	Provisioner   string `json:"provisioner"`
	IsDefault     bool   `json:"isDefault"`
	ReclaimPolicy string `json:"reclaimPolicy"`
}

type sovereignPVC struct {
	Cluster      string `json:"cluster,omitempty"`
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	StorageClass string `json:"storageClass"`
	Capacity     string `json:"capacity"`
	Status       string `json:"status"`
}

// HandleSovereignCloud — GET /api/v1/sovereign/cloud.
//
// Read-only topology snapshot for the Console Cloud page. Six lists,
// every list paged in one call. The Console renders sections from
// the same response so a freshly handed-over operator sees their
// cluster's full surface at a glance.
func (h *Handler) HandleSovereignCloud(w http.ResponseWriter, r *http.Request) {
	deps, err := h.sovereignDepsFor()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "in-cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	resp := sovereignCloudResponse{
		Nodes:          []sovereignNode{},
		Namespaces:     []sovereignNamespace{},
		Ingresses:      []sovereignIngress{},
		HTTPRoutes:     []sovereignIngress{},
		LoadBalancers:  []sovereignLB{},
		StorageClasses: []sovereignSC{},
		PVCs:           []sovereignPVC{},
	}

	// D16 PR D (2026-05-17): multi-region fan-out. Founder caught on t136
	// that /cloud?view=list&kind=nodes shows 1 node for a 3-region
	// Sovereign because this handler was using only the in-cluster
	// kube client (primary cluster). When secondary kubeconfigs are
	// registered with h.k8sCache (via the chroot's POST
	// /api/v1/sovereign/secondary-kubeconfig endpoint and the
	// mothership handover-export hook), enumerate per-cluster + tag
	// each row with its cluster id so the UI can group/filter by
	// region. Single-cluster Sovereigns fall back to the deps client.
	type clientPair struct {
		id   string
		core kubernetes.Interface
		dyn  dynamic.Interface
	}
	pairs := []clientPair{}
	if h.k8sCache != nil {
		for _, cid := range h.k8sCache.Clusters() {
			cc := h.k8sCache.CoreClient(cid)
			if cc == nil {
				continue
			}
			dc, _ := h.k8sCache.DynamicClientFor(cid)
			pairs = append(pairs, clientPair{id: cid, core: cc, dyn: dc})
		}
	}
	if len(pairs) == 0 {
		// Single-cluster fallback — primary in-cluster client only.
		pairs = []clientPair{{id: "", core: deps.core, dyn: deps.dyn}}
	}

	for _, p := range pairs {
		if nodes, err := p.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err == nil {
			for _, n := range nodes.Items {
				row := mapNode(&n)
				row.Cluster = p.id
				resp.Nodes = append(resp.Nodes, row)
			}
		}
		if svcs, err := p.core.CoreV1().Services("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, svc := range svcs.Items {
				if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
					continue
				}
				ports := []string{}
				for _, port := range svc.Spec.Ports {
					ports = append(ports, fmt.Sprintf("%d/%s", port.Port, port.Protocol))
				}
				extIP := ""
				for _, ing := range svc.Status.LoadBalancer.Ingress {
					if ing.IP != "" {
						extIP = ing.IP
						break
					}
					if ing.Hostname != "" {
						extIP = ing.Hostname
						break
					}
				}
				resp.LoadBalancers = append(resp.LoadBalancers, sovereignLB{
					Cluster:    p.id,
					Name:       svc.Name,
					Namespace:  svc.Namespace,
					Type:       string(svc.Spec.Type),
					ClusterIP:  svc.Spec.ClusterIP,
					ExternalIP: extIP,
					Ports:      ports,
				})
			}
		}
		if scs, err := p.core.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{}); err == nil {
			for _, sc := range scs.Items {
				isDefault := sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"
				rp := ""
				if sc.ReclaimPolicy != nil {
					rp = string(*sc.ReclaimPolicy)
				}
				resp.StorageClasses = append(resp.StorageClasses, sovereignSC{
					Cluster:       p.id,
					Name:          sc.Name,
					Provisioner:   sc.Provisioner,
					IsDefault:     isDefault,
					ReclaimPolicy: rp,
				})
			}
		}
		if pvcs, err := p.core.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, pv := range pvcs.Items {
				cap := ""
				if v, ok := pv.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
					cap = v.String()
				}
				sc := ""
				if pv.Spec.StorageClassName != nil {
					sc = *pv.Spec.StorageClassName
				}
				resp.PVCs = append(resp.PVCs, sovereignPVC{
					Cluster:      p.id,
					Name:         pv.Name,
					Namespace:    pv.Namespace,
					StorageClass: sc,
					Capacity:     cap,
					Status:       string(pv.Status.Phase),
				})
			}
		}
	}

	// Namespaces / Ingresses / HTTPRoutes remain primary-only — they are
	// the operator-facing front-door inventory, served by the primary
	// (mothership-handed-over) cluster. Per-cluster fan-out would dup
	// the same logical hostnames across regions.

	if nss, err := deps.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err == nil {
		for _, ns := range nss.Items {
			resp.Namespaces = append(resp.Namespaces, sovereignNamespace{
				Name:      ns.Name,
				Status:    string(ns.Status.Phase),
				CreatedAt: ns.CreationTimestamp.Time,
			})
		}
	}

	if ings, err := deps.core.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, ing := range ings.Items {
			hosts := []string{}
			for _, rule := range ing.Spec.Rules {
				if rule.Host != "" {
					hosts = append(hosts, rule.Host)
				}
			}
			url := ""
			if len(hosts) > 0 {
				url = "https://" + hosts[0]
			}
			resp.Ingresses = append(resp.Ingresses, sovereignIngress{
				Name:      ing.Name,
				Namespace: ing.Namespace,
				Hosts:     hosts,
				URL:       url,
			})
		}
	}

	// HTTPRoutes via dynamic client — Gateway API may not be installed
	// on every cluster, so the not-found error is non-fatal.
	if rl, err := deps.dyn.Resource(httpRouteGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, h := range rl.Items {
			hosts, _, _ := unstructured.NestedStringSlice(h.Object, "spec", "hostnames")
			url := ""
			if len(hosts) > 0 {
				url = "https://" + hosts[0]
			}
			resp.HTTPRoutes = append(resp.HTTPRoutes, sovereignIngress{
				Name:      h.GetName(),
				Namespace: h.GetNamespace(),
				Hosts:     hosts,
				URL:       url,
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// mapNode converts a core/v1.Node to the Sovereign Console row shape.
func mapNode(n *corev1.Node) sovereignNode {
	status := "Unknown"
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
			}
			break
		}
	}
	roles := []string{}
	for k := range n.Labels {
		const p = "node-role.kubernetes.io/"
		if strings.HasPrefix(k, p) {
			roles = append(roles, strings.TrimPrefix(k, p))
		}
	}
	if len(roles) == 0 {
		roles = []string{"worker"}
	}
	internalIP := ""
	externalIP := ""
	for _, addr := range n.Status.Addresses {
		switch addr.Type {
		case corev1.NodeInternalIP:
			internalIP = addr.Address
		case corev1.NodeExternalIP:
			externalIP = addr.Address
		}
	}
	cpuCap := ""
	memCap := ""
	if v, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
		cpuCap = v.String()
	}
	if v, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
		memCap = v.String()
	}
	return sovereignNode{
		Name:           n.Name,
		Status:         status,
		Roles:          roles,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OS:             n.Status.NodeInfo.OperatingSystem,
		Arch:           n.Status.NodeInfo.Architecture,
		InternalIP:     internalIP,
		ExternalIP:     externalIP,
		CapacityCPU:    cpuCap,
		CapacityMemory: memCap,
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// humanizeSlug turns "cert-manager" into "Cert Manager".
func humanizeSlug(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
