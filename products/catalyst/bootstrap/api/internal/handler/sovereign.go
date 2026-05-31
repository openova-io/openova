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
// install uses Cilium Gateway as the only ingress; Console / SME /
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

	// 1. HelmReleases.
	if hrList, err := deps.dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, hr := range hrList.Items {
			item := helmReleaseToJob(&hr)
			out = append(out, item)
		}
	}

	// 2. K8s Jobs.
	if jobs, err := deps.core.BatchV1().Jobs("").List(ctx, metav1.ListOptions{}); err == nil {
		for _, j := range jobs.Items {
			out = append(out, k8sJobToJob(&j))
		}
	}

	// 3. K8s Events (warnings only).
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

// helmReleaseToJob maps a Flux HelmRelease unstructured into the
// Sovereign Console's Job row shape. Status derives from
// status.conditions[type=Ready].
func helmReleaseToJob(u *unstructured.Unstructured) sovereignJobItem {
	name := u.GetName()
	ns := u.GetNamespace()
	id := fmt.Sprintf("hr/%s/%s", ns, name)
	status := "running"
	message := ""
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] != "Ready" {
			continue
		}
		switch m["status"] {
		case "True":
			status = "succeeded"
		case "False":
			status = "failed"
		default:
			status = "running"
		}
		if msg, ok := m["message"].(string); ok {
			message = msg
		}
	}
	started := u.GetCreationTimestamp().Time
	finished := time.Time{}
	if status == "succeeded" || status == "failed" {
		// Use lastTransitionTime as a proxy for finish.
		for _, c := range conds {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if m["type"] == "Ready" {
				if t, ok := m["lastTransitionTime"].(string); ok {
					if parsed, err := time.Parse(time.RFC3339, t); err == nil {
						finished = parsed
					}
				}
			}
		}
	}
	return sovereignJobItem{
		ID:         id,
		Name:       name,
		Namespace:  ns,
		Kind:       "HelmRelease",
		Status:     status,
		Message:    message,
		StartedAt:  started,
		FinishedAt: finished,
	}
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
	// page lost the environment chip during the SME-publish redesign).
	Environment string `json:"environment,omitempty"`

	// MarketplacePublished — null when the slug isn't in the SME
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
//                     (these always render as installed; the operator
//                      cannot uninstall the spine)
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
	}
	hrInfo := map[string]hrTarget{}
	// urlByHRName — front-door URL for each installed HR, populated by
	// joining each HR's (targetNamespace, releaseName) against the
	// cluster's HTTPRoute set. G90 2026-06-01 — feeds sovereignAppItem.ExternalURL
	// so the AppsPage card can render an "Open" launch button next to
	// the INSTALLED badge.
	urlByHRName := map[string]string{}
	deps, depsErr := h.sovereignDepsFor()
	if depsErr == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if hrList, err := deps.dyn.Resource(helmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
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
				hrInfo[name] = hrTarget{targetNamespace: tn, releaseName: rn}
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
		for hrName, info := range hrInfo {
			if url, ok := hrURL[[2]string{info.targetNamespace, info.releaseName}]; ok {
				urlByHRName[hrName] = url
			}
		}
		// Application CR pass — separate List so a missing CRD or
		// RBAC denial on apps.openova.io doesn't break the HR pass
		// above (which is the load-bearing one for status).
		if appList, err := deps.dyn.Resource(applicationGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, app := range appList.Items {
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
	}

	bootstrapIDs := map[string]bool{}
	bootstrapList := make([]string, 0, len(bootstrap))
	for _, b := range bootstrap {
		bootstrapIDs[b.ID] = true
		bootstrapList = append(bootstrapList, b.ID)
	}

	apps := make([]sovereignAppItem, 0, len(listed)+len(bootstrap))

	// Bootstrap-kit always rendered first as "installed" (unlisted
	// blueprints don't appear in `listed`). The operator sees them
	// as part of the catalog, marked "bootstrap" so they can't be
	// uninstalled via UI.
	for _, b := range bootstrap {
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

	// Best-effort SME catalog join. When marketplace.enabled=false on
	// this Sovereign, the SME catalog service is not deployed; the
	// client returns (nil, nil) and every app stays with
	// MarketplacePublished=nil (the FE then suppresses the Publish
	// chip). When SME IS deployed and reachable, decorate matching
	// slugs with the published flag.
	smeCtx, smeCancel := context.WithTimeout(r.Context(), smeCatalogProbeBudget)
	defer smeCancel()
	if pub, _ := smeCatalog().PublishedBySlug(smeCtx); pub != nil {
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
// chroot's catalyst-api proxies the PATCH to the in-cluster SME
// catalog service at http://catalog.sme.svc.cluster.local:8082 —
// same data plane the deleted /catalog page used.
//
// 503 when the SME catalog is not deployed on this Sovereign
// (marketplace.enabled=false in the chart values), or when the
// SME HS256 bridge secret is not wired (sme-secrets reflection
// not yet reconciled). 401 when the operator's session has no
// claims attached (auth middleware bypassed). 404 when the slug
// isn't in the SME catalog. The upstream status is surfaced verbatim.
//
// Body shape: {"published": true|false}.
//
// Auth seam (Closes #1735): the upstream catalog runs JWTAuth on
// every /catalog/admin/* path and requireAdmin on this specific
// handler. Forwarding the operator's Keycloak RS256 session header
// to the catalog 401s upstream (catalog only accepts HS256 signed
// with sme-secrets/JWT_SECRET, just like the rest of the SME mesh).
// Mint a fresh HS256 bridge token via the same h.mintSMEBridgeToken
// helper sme_billing_vouchers.go uses for the BSS Vouchers surface
// and forward THAT as the upstream Authorization header. Operators
// with `catalyst-owner` realm-role (per SMERoleFor) get role=
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
	// Mint the canonical SME bridge token so the upstream catalog's
	// JWTAuth + requireAdmin admit this hop. Pre-bridge state (no
	// Authorization header) ⇒ JWTAuth rejects with 401 "missing or
	// invalid authorization header" — that's the C4-012 / #1735
	// symptom. The bridge mirrors sme_billing_vouchers.go's
	// mintSMEBridgeToken pattern: RS256 operator session → HS256
	// token signed with sme-secrets/JWT_SECRET, role mapped via
	// sharedauth.SMERoleFor. The upstream catalog's requireAdmin was
	// widened in the same PR to accept "sovereign-admin" so a
	// franchisee operator can manage their own Sovereign's
	// marketplace catalog without a Catalyst-Zero ("superadmin")
	// role. When the bridge is unwired (Sovereign without
	// marketplace, or stale chart) the helper returns 503 which we
	// surface verbatim so the FE renders an actionable
	// "marketplace not enabled" message.
	bearer, status, errResp := h.mintSMEBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), smeCatalogProbeBudget)
	defer cancel()
	upstreamStatus, err := smeCatalog().SetPublished(ctx, slug, body.Published, bearer)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-catalog-unreachable",
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
		"error":  "sme-catalog-rejected",
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
	Cluster        string `json:"cluster,omitempty"`
	Name           string `json:"name"`
	Provisioner    string `json:"provisioner"`
	IsDefault      bool   `json:"isDefault"`
	ReclaimPolicy  string `json:"reclaimPolicy"`
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

