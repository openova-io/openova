// reconcilers.go — the lightweight ArgoCD/Flux MANAGEMENT surface for the
// Cloud view's Reconciliation lens (issue #3996).
//
// Where reconciliation_dag.go renders the BOUNDED Flux DAG (read-only, for
// the convergence graph) and jobs_retry.go re-drives a FAILED activity from
// the Jobs canvas, this file gives the operator an ArgoCD-like management
// surface over the FULL continuous-reconciler set — every Flux reconciler
// object (HelmRelease, Kustomization, GitRepository, OCIRepository,
// HelmRepository, HelmChart) — with three capabilities:
//
//   GET  /api/v1/deployments/{depId}/reconcilers
//        → list with live status + last-reconcile + message + revision +
//          suspended flag + the controller that owns each kind.
//
//   GET  /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/logs
//        → page the owning controller's logs (helm-controller /
//          kustomize-controller / source-controller) FILTERED to this
//          object, mirroring the GET /actions/executions/{id}/logs shape.
//
//   POST /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}
//        action ∈ {reconcile, suspend, resume}:
//          reconcile → annotate reconcile.fluxcd.io/requestedAt=<RFC3339>
//          suspend   → merge-patch spec.suspend=true
//          resume    → merge-patch spec.suspend=false
//        via the IN-CLUSTER DYNAMIC CLIENT only — NEVER a shell-out
//        (PRINCIPLES #61). Owner-checked (404 cross-tenant) + RBAC-gated to
//        operator tier (403 otherwise), the SAME gate jobs_retry.go uses.
package handler

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// Reconciler-log paging defaults — mirror the jobs execution-log contract
// (jobs.DefaultLogPageSize / MaxLogPageSize) so the FE reuses one viewer.
const (
	defaultReconcilerLogTail = 200
	maxReconcilerLogTail     = 5000
)

// resolveReconcilerDeployment loads + ownership-gates the deployment for a
// reconciler-management request. Returns the Deployment, or nil after
// having already written the 404/403 response. Mirrors the RetryJob lookup
// (chrootEnsureDeployment first so the Sovereign-side catalyst-api serves
// its imported deployment by id).
func (h *Handler) resolveReconcilerDeployment(w http.ResponseWriter, r *http.Request, depID string) *Deployment {
	dep := h.chrootEnsureDeployment(depID)
	if dep == nil {
		if val, ok := h.deployments.Load(depID); ok {
			dep = val.(*Deployment)
		}
	}
	if dep == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return nil
	}
	if !h.checkOwnership(w, r, dep) {
		return nil // 404 already written
	}
	return dep
}

// ListReconcilers handles GET /api/v1/deployments/{depId}/reconcilers.
//
// Returns `{ "reconcilers": [...], "reconciled": N, "total": M }` — every
// manageable Flux reconciler with its live status, last-reconcile ts,
// message, applied revision, suspended flag, and owning controller. The
// counts let the convergence %/ratio drill into the non-Reconciled rows.
func (h *Handler) ListReconcilers(w http.ResponseWriter, r *http.Request) {
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-depId"})
		return
	}
	dep := h.resolveReconcilerDeployment(w, r, depID)
	if dep == nil {
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}
	listCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rows, err := helmwatch.ListManagedReconcilers(listCtx, dyn)
	if err != nil {
		h.log.Warn("ListReconcilers: list failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "list-failed"})
		return
	}
	if rows == nil {
		rows = []helmwatch.ManagedReconciler{}
	}
	reconciled := 0
	for _, rc := range rows {
		if rc.State == helmwatch.ManageStateReconciled {
			reconciled++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reconcilers": rows,
		"reconciled":  reconciled,
		"total":       len(rows),
	})
}

// ReconcilerAction handles POST
// /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/{action}.
//
//	200 — action applied; body echoes the kind/name/action + requestedBy.
//	400 — missing path params / unknown kind / unsupported action.
//	403 — caller lacks operator RBAC.
//	404 — unknown deployment / cross-tenant.
//	502 — the in-cluster client/patch write failed.
func (h *Handler) ReconcilerAction(w http.ResponseWriter, r *http.Request) {
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	kind := strings.TrimSpace(chi.URLParam(r, "kind"))
	ns := strings.TrimSpace(chi.URLParam(r, "ns"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	action := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "action")))
	if depID == "" || kind == "" || ns == "" || name == "" || action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-path-params"})
		return
	}
	gvr, ok := helmwatch.ManagedGVRForKind(kind)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "unknown-kind",
			"detail": fmt.Sprintf("kind %q is not a manageable Flux reconciler", kind),
		})
		return
	}
	switch action {
	case "reconcile", "suspend", "resume":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "unsupported-action",
			"detail": fmt.Sprintf("action %q must be one of reconcile / suspend / resume", action),
		})
		return
	}

	dep := h.resolveReconcilerDeployment(w, r, depID)
	if dep == nil {
		return
	}
	// RBAC: mutating a reconciler is an operator action — reuse the SAME
	// operator-tier gate the jobs-retry endpoint uses (403 for viewers; nil
	// claims pass for CI/tests).
	claims := auth.ClaimsFromContext(r.Context())
	if !jobRetryCallerAuthorized(claims, dep) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"detail": "managing a reconciler requires operator tier or higher",
		})
		return
	}

	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	now := time.Now().UTC()
	var patch []byte
	switch action {
	case "reconcile":
		// The Flux-native "reconcile now" primitive (the same annotation
		// `flux reconcile` writes). Reuse the exact constant + shape
		// jobs_retry.go's triggerReconcileAnnotation uses.
		patch = []byte(fmt.Sprintf(
			`{"metadata":{"annotations":{%q:%q}}}`,
			reconcileRequestedAtAnnotation, now.Format(time.RFC3339Nano),
		))
	case "suspend":
		patch = []byte(`{"spec":{"suspend":true}}`)
	case "resume":
		patch = []byte(`{"spec":{"suspend":false}}`)
	}

	if _, err := dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		h.log.Warn("ReconcilerAction: patch failed",
			"depId", depID, "kind", kind, "ns", ns, "name", name, "action", action, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "action-failed",
			"detail": err.Error(),
		})
		return
	}

	operator := retryOperatorIdentity(claims, r)
	h.log.Info("ReconcilerAction applied",
		"depId", depID, "kind", kind, "ns", ns, "name", name,
		"action", action, "by", operator)
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":        kind,
		"namespace":   ns,
		"name":        name,
		"action":      action,
		"requestedAt": now.Format(time.RFC3339),
		"requestedBy": operator,
	})
}

// GetReconcilerLogs handles GET
// /api/v1/deployments/{depId}/reconcilers/{kind}/{ns}/{name}/logs?tailLines=N.
//
// Tails the OWNING controller's pod logs (helm-controller /
// kustomize-controller / source-controller) and FILTERS to lines mentioning
// this object's "<ns>/<name>" coordinate — Flux controllers log every
// reconcile line with the object's namespaced name, so a substring match
// yields exactly this object's reconcile output. Returns the same paginated
// shape the execution-log viewer reads: `{ "lines": [...], "total": N }`.
//
// 404 when the controller pod can't be found (Flux not installed / RBAC).
func (h *Handler) GetReconcilerLogs(w http.ResponseWriter, r *http.Request) {
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	kind := strings.TrimSpace(chi.URLParam(r, "kind"))
	ns := strings.TrimSpace(chi.URLParam(r, "ns"))
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if depID == "" || kind == "" || ns == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-path-params"})
		return
	}
	if _, ok := helmwatch.ManagedGVRForKind(kind); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown-kind"})
		return
	}
	controller := helmwatch.ControllerForKind(kind)
	if controller == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no-controller-for-kind"})
		return
	}

	dep := h.resolveReconcilerDeployment(w, r, depID)
	if dep == nil {
		return
	}

	core, err := h.sovereignCoreClient(dep)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	tail := defaultReconcilerLogTail
	if v := strings.TrimSpace(r.URL.Query().Get("tailLines")); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			tail = n
		}
	}
	if tail > maxReconcilerLogTail {
		tail = maxReconcilerLogTail
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	lines, err := tailControllerLogsForObject(ctx, core, controller, ns, name, tail)
	if err != nil {
		h.log.Warn("GetReconcilerLogs: tail failed",
			"depId", depID, "kind", kind, "controller", controller, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "logs-unavailable",
			"detail": err.Error(),
		})
		return
	}
	if lines == nil {
		lines = []reconcilerLogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"controller": controller,
		"object":     ns + "/" + name,
		"lines":      lines,
		"total":      len(lines),
	})
}

// reconcilerLogLine is one rendered controller-log line for the FE viewer.
type reconcilerLogLine struct {
	LineNumber int    `json:"lineNumber"`
	Message    string `json:"message"`
}

// fluxControllerNamespace — Flux controllers live in flux-system on every
// Catalyst Sovereign (bootstrap-kit installs them there). The pod selector
// is `app=<controller>` (the canonical Flux label, matching
// helmwatch.HelmControllerSelector's pattern).
const fluxControllerNamespace = helmwatch.FluxNamespace

// tailControllerLogsForObject finds the named Flux controller pod in
// flux-system, pulls its last `tail`×4 lines (oversample so post-filter we
// still return a useful window), and keeps only lines mentioning the
// object's "<ns>/<name>" coordinate or its bare name. Returns at most `tail`
// matched lines (newest-biased: the controller writes chronologically so the
// returned slice is the matched tail).
func tailControllerLogsForObject(ctx context.Context, core kubernetes.Interface, controller, ns, name string, tail int) ([]reconcilerLogLine, error) {
	podName, err := firstReadyControllerPod(ctx, core, controller)
	if err != nil {
		return nil, err
	}
	// Oversample raw lines so the post-filter still yields a useful window
	// (a busy controller interleaves many objects' lines).
	rawTail := int64(tail * 8)
	if rawTail > int64(maxReconcilerLogTail*8) {
		rawTail = int64(maxReconcilerLogTail * 8)
	}
	opts := &corev1.PodLogOptions{
		Follow:    false,
		TailLines: &rawTail,
	}
	stream, err := core.CoreV1().Pods(fluxControllerNamespace).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open %s/%s log stream: %w", fluxControllerNamespace, podName, err)
	}
	defer stream.Close()

	nsName := ns + "/" + name
	out := make([]reconcilerLogLine, 0, tail)
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		// Flux controllers log the object as "<ns>/<name>" in a
		// name=/namespace= or path field; match either the namespaced form
		// or the bare name (some lines log just name= with a separate
		// namespace=). The namespaced form is the precise match; the bare
		// name is a fallback that still scopes to this object on a normal
		// Sovereign (object names are unique within flux-system).
		if !logLineMentionsToken(line, nsName) && !logLineMentionsName(line, name) {
			continue
		}
		n++
		out = append(out, reconcilerLogLine{LineNumber: n, Message: line})
	}
	if err := sc.Err(); err != nil {
		// Return what we matched so far rather than failing the whole pull.
		if len(out) > 0 {
			return clampLogTail(out, tail), nil
		}
		return nil, fmt.Errorf("scan %s logs: %w", controller, err)
	}
	return clampLogTail(out, tail), nil
}

// nameCharByte reports whether b can appear INSIDE a Kubernetes object name
// (RFC 1123: lowercase alphanumerics, '-', '.'). Used to decide whether a
// substring match ended at a real boundary or in the middle of a longer name.
func nameCharByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '-' || b == '.' || b == '_'
}

// logLineMentionsToken reports whether `line` contains `token` as a WHOLE
// token — i.e. the character immediately after the match is not another
// name character.
//
// #5485: the previous code used a bare strings.Contains for the namespaced
// form, so `flux-system/bp-velero` matched inside
// `flux-system/bp-velero-hcs` and the bp-velero drill-in rendered 100%
// bp-velero-hcs log lines with nothing indicating the substitution. Only the
// LEADING boundary was guarded; the trailing one was open.
func logLineMentionsToken(line, token string) bool {
	if token == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(line[i:], token)
		if j < 0 {
			return false
		}
		end := i + j + len(token)
		if end >= len(line) || !nameCharByte(line[end]) {
			return true
		}
		i = i + j + 1
	}
}

// logLineMentionsName reports whether a controller log line references the
// object by bare name in a name= / "name": field (avoids matching a random
// substring).
//
// #5485: the quoted markers are delimited on BOTH sides and are safe as
// plain substrings. The unquoted ones (`name=<n>` and `/<n>`) are delimited
// only on the left, so they need an explicit trailing-boundary check —
// otherwise `bp-cnpg` matches `bp-cnpg-pair` and `bp-velero` matches
// `bp-velero-hcs`.
func logLineMentionsName(line, name string) bool {
	if name == "" {
		return false
	}
	for _, marker := range []string{
		`name="` + name + `"`,
		`"name":"` + name + `"`,
	} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	for _, marker := range []string{
		`name=` + name,
		`/` + name,
	} {
		if logLineMentionsToken(line, marker) {
			return true
		}
	}
	return false
}

// clampLogTail keeps the LAST `tail` lines (the newest window) and
// renumbers them 1..len so the FE viewer's line gutter is contiguous.
func clampLogTail(in []reconcilerLogLine, tail int) []reconcilerLogLine {
	if tail > 0 && len(in) > tail {
		in = in[len(in)-tail:]
	}
	for i := range in {
		in[i].LineNumber = i + 1
	}
	return in
}

// firstReadyControllerPod returns the name of a Running Flux controller pod
// matching `app=<controller>` in flux-system. Prefers a Running pod; falls
// back to the first listed when none report Running (so logs from a
// CrashLooping controller — the most useful diagnostic — still surface).
func firstReadyControllerPod(ctx context.Context, core kubernetes.Interface, controller string) (string, error) {
	pods, err := core.CoreV1().Pods(fluxControllerNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + controller,
	})
	if err != nil {
		return "", fmt.Errorf("list %s pods: %w", controller, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no %s pod found in %s", controller, fluxControllerNamespace)
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}
	return pods.Items[0].Name, nil
}

// sovereignCoreClient builds a typed kubernetes.Interface against the
// target cluster — the in-cluster ServiceAccount on a Sovereign chroot
// (SOVEREIGN_FQDN matches), else the posted-back kubeconfig on the mother.
// Mirrors sovereignDynamicClient (infrastructure.go) exactly, but returns a
// core clientset for Pod log tailing.
func (h *Handler) sovereignCoreClient(dep *Deployment) (kubernetes.Interface, error) {
	dep.mu.Lock()
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	depFQDN := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()

	if selfFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")); selfFQDN != "" && selfFQDN == depFQDN {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("chroot in-cluster config: %w", err)
		}
		return kubernetes.NewForConfig(cfg)
	}
	if kubeconfigPath == "" {
		return nil, fmt.Errorf("sovereign cluster kubeconfig not yet posted back — retry once Phase-1 ready")
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	if h.coreFactory != nil {
		return h.coreFactory(string(raw))
	}
	return helmwatch.NewKubernetesClientFromKubeconfig(string(raw))
}
