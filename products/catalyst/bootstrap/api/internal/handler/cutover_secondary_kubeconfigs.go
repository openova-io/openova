// cutover_secondary_kubeconfigs.go — #5359 secondary-region credential
// bridge for the self-sovereignty cutover.
//
// Root cause (#5359, live-proven on hw288 dep 027f07559af1f9f7): on a
// 2-region Sovereign the 11-step cutover chain runs its Jobs in the
// CONTROL-PLANE (region-A) cluster only. Steps 04/05/06/08 pivoted only
// region-A; region-B's Flux GitRepository stayed on github.com, all 64
// HelmRepositories on ghcr.io, node images on quay.io — yet
// cutoverComplete=true was earned because the step-08 deny-egress proof
// never touched region-B. cc=true was a FALSE POSITIVE for Pillar-5.
//
// The chart-side fix (bp-self-sovereign-cutover 0.1.151) teaches steps
// 05/06/08 to run a region-B leg with `kubectl --kubeconfig` against each
// secondary region. The Jobs run in region-A, so they need the secondary
// regions' kubeconfigs — which the platform ALREADY holds: the chroot
// catalyst-api's PVC carries `<kubeconfigsDir>/<depID>-<regionKey>.yaml`
// per secondary CP (deposited by the secondary cloud-init via the
// mothership → forwarded at handover via POST /sovereign/secondary-
// kubeconfig, #3991; server host healed to the VPC-routable private IP,
// #4000). That store is the SAME mechanism ClusterMesh establish
// (buildRegionSlots), the #5244 gateway-ELB reconciler and the #5261
// stalled-HR force-reconcile use to reach region-B — no new credential
// contract is invented here.
//
// This file bridges that store to the step Jobs: at every cutover run
// start (fresh fire, internal auto-trigger, AND the resume path — all
// funnel through runCutover) the engine materializes the secondary
// kubeconfigs into ONE Secret in the cutover namespace:
//
//	Secret <cutover-ns>/cutover-secondary-kubeconfigs
//	  data: "<regionKey>.yaml" -> kubeconfig bytes   (one key per region)
//
// The chart mounts it `optional: true` at /secondary-kubeconfigs; a step
// leg iterates the mounted files. Contract:
//
//   - SINGLE-REGION Sovereign → no secondary kubeconfigs exist → any
//     stale Secret is deleted and the mount is empty → every region-B
//     leg no-ops → behavior EXACTLY as before #5359.
//   - MULTI-REGION Sovereign → the Secret MUST carry every expected
//     secondary region. If the deployment record says N-1 secondaries
//     exist but fewer kubeconfigs can be resolved/read, the run FAILS
//     LOUDLY before step 1 — a cutover that would silently skip
//     region-B is the exact false-positive #5359 exists to kill.
//
// Idempotent: create-or-update with fully-replaced data (stale region
// keys from a prior topology do not linger).
package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// envCutoverSecondaryKubeconfigsSecret overrides the Secret name (runtime-
// overridable per Inviolable-Principle #4). Must match the chart value
// `secondaryRegions.kubeconfigSecretName`.
const envCutoverSecondaryKubeconfigsSecret = "CATALYST_CUTOVER_SECONDARY_KUBECONFIGS_SECRET"

// defaultCutoverSecondaryKubeconfigsSecret is the canonical Secret name the
// bp-self-sovereign-cutover chart mounts (values.yaml secondaryRegions.
// kubeconfigSecretName).
const defaultCutoverSecondaryKubeconfigsSecret = "cutover-secondary-kubeconfigs"

func cutoverSecondaryKubeconfigsSecretName() string {
	if v := strings.TrimSpace(os.Getenv(envCutoverSecondaryKubeconfigsSecret)); v != "" {
		return v
	}
	return defaultCutoverSecondaryKubeconfigsSecret
}

// resolveCutoverDeployment returns the Deployment THIS catalyst-api serves
// as an in-cluster chroot (SOVEREIGN_FQDN set + matching the record), or
// nil when no record matches. The cutover engine only ever runs on a
// chroot, and a chroot serves exactly one deployment (#5131
// chrootServesDeployment discriminator — reused verbatim).
func (h *Handler) resolveCutoverDeployment() *Deployment {
	var found *Deployment
	h.deployments.Range(func(_, value any) bool {
		dep, ok := value.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		if h.chrootServesDeployment(dep) {
			found = dep
			return false
		}
		return true
	})
	return found
}

// secondaryKubeconfigsForCutover returns regionKey → on-disk path for every
// secondary region of dep, as the union of the in-memory
// dep.secondaryKubeconfigPaths map and the on-disk `<depID>-<regionKey>.yaml`
// files — the SAME union discoverSecondaryNodeInternalIPs (#5244) trusts,
// immune to in-memory loss across catalyst-api restarts (#4000). Also
// returns the number of secondaries the deployment SPEC expects, for the
// fail-loud completeness check.
func secondaryKubeconfigsForCutover(dep *Deployment) (map[string]string, int) {
	dep.mu.Lock()
	expected := 0
	if len(dep.Request.Regions) > 1 {
		expected = len(dep.Request.Regions) - 1
	}
	paths := make(map[string]string, len(dep.secondaryKubeconfigPaths))
	for k, v := range dep.secondaryKubeconfigPaths {
		paths[k] = v
	}
	depID := dep.ID
	dep.mu.Unlock()

	dir := secondaryKubeconfigsDir()
	for _, key := range onDiskSecondaryKubeconfigKeys(dir, depID) {
		if _, ok := paths[key]; !ok {
			paths[key] = filepath.Join(dir, depID+"-"+key+".yaml")
		}
	}
	return paths, expected
}

// sanitizeSecondaryRegionKey maps a regionKey to a ConfigMap/Secret-safe
// data key stem ([-._a-zA-Z0-9]). Region keys are provider region ids
// (e.g. "me-east-215-b", "nbg1-1") and are already safe in practice; any
// other rune is replaced with '-' so a hostile/odd key can never wedge the
// Secret write.
func sanitizeSecondaryRegionKey(key string) string {
	out := make([]rune, 0, len(key))
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// materializeSecondaryKubeconfigsSecret creates/updates (or deletes, when
// no secondary regions exist) the cutover-secondary-kubeconfigs Secret in
// the cutover namespace. Returns the number of secondary regions
// materialized.
//
// Fail-loud contract (#5359): on a deployment whose spec expects
// secondaries, an unreadable/missing kubeconfig or a failed Secret write
// returns an error — runCutover aborts BEFORE step 1 rather than running
// a chain whose region-B legs would silently no-op into a false-positive
// cc=true.
func (h *Handler) materializeSecondaryKubeconfigsSecret(ctx context.Context, deps *cutoverDeps) (int, error) {
	name := cutoverSecondaryKubeconfigsSecretName()

	dep := h.resolveCutoverDeployment()
	if dep == nil {
		// No deployment record on this process (mothership never runs the
		// cutover; a record-less chroot cannot know its region topology).
		// Do NOT delete an existing Secret — a prior run with a live record
		// may have materialized it and the files it points at are still the
		// truth. Loud in the log so a record-less 2-region chroot is
		// diagnosable rather than silently single-region.
		h.log.Warn("cutover: no deployment record matches SOVEREIGN_FQDN — secondary-region detection unavailable; proceeding with any previously-materialized secondary kubeconfigs (#5359)",
			"secret", name)
		return 0, nil
	}

	paths, expected := secondaryKubeconfigsForCutover(dep)

	data := make(map[string][]byte, len(paths))
	var missing []string
	for key, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s)", key, path))
			continue
		}
		data[sanitizeSecondaryRegionKey(key)+".yaml"] = raw
	}

	if expected > len(data) {
		sort.Strings(missing)
		return 0, fmt.Errorf(
			"cutover: deployment %s expects %d secondary region(s) but only %d kubeconfig(s) are readable (missing/unreadable: %s) — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
			dep.ID, expected, len(data), strings.Join(missing, ", "))
	}

	if len(data) == 0 {
		// Single-region: guarantee the mount stays empty so the chart legs
		// no-op — delete any stale Secret from a prior multi-region life.
		err := deps.core.CoreV1().Secrets(deps.ns).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, fmt.Errorf("cutover: delete stale %s/%s: %w", deps.ns, name, err)
		}
		return 0, nil
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: deps.ns,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":    cutoverStepPartOfValue,
				"app.kubernetes.io/managed-by": "catalyst-api",
				"app.kubernetes.io/component":  "cutover-secondary-kubeconfigs",
			},
			Annotations: map[string]string{
				"catalyst.openova.io/deployment-id": dep.ID,
				"catalyst.openova.io/materialized":  time.Now().UTC().Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	existing, err := deps.core.CoreV1().Secrets(deps.ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, cerr := deps.core.CoreV1().Secrets(deps.ns).Create(ctx, desired, metav1.CreateOptions{}); cerr != nil {
			return 0, fmt.Errorf("cutover: create %s/%s: %w", deps.ns, name, cerr)
		}
	case err != nil:
		return 0, fmt.Errorf("cutover: get %s/%s: %w", deps.ns, name, err)
	default:
		// Full data replace (stale region keys must not linger); keep the
		// live object's resourceVersion for a conflict-safe update.
		updated := existing.DeepCopy()
		updated.Labels = desired.Labels
		updated.Annotations = desired.Annotations
		updated.Type = desired.Type
		updated.Data = desired.Data
		updated.StringData = nil
		if _, uerr := deps.core.CoreV1().Secrets(deps.ns).Update(ctx, updated, metav1.UpdateOptions{}); uerr != nil {
			return 0, fmt.Errorf("cutover: update %s/%s: %w", deps.ns, name, uerr)
		}
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, strings.TrimSuffix(k, ".yaml"))
	}
	sort.Strings(keys)
	h.log.Info("cutover: secondary-region kubeconfigs materialized for the step Jobs (#5359)",
		"secret", deps.ns+"/"+name,
		"regions", strings.Join(keys, ","),
		"deploymentID", dep.ID)
	return len(data), nil
}

// reapSecondaryCutoverEgressPolicies is the #5014 backstop's region-B
// counterpart: on EVERY exit of the egress-block-test step, delete the
// cutover-egress-block CCNP from every secondary region cluster too. The
// step Job's TERM/EXIT trap covers clean exits; this covers SIGKILL /
// watch-loss, where a leaked default-deny egress policy on region-B would
// freeze its CSI attaches exactly as it did on region-A (hw242 3x).
// Best-effort per region: an unreachable secondary (possibly mid region
// outage) is logged and skipped — the next step-08 leg deletes-before-
// apply so a stale policy also self-heals on re-run.
func (h *Handler) reapSecondaryCutoverEgressPolicies(ctx context.Context) {
	dep := h.resolveCutoverDeployment()
	if dep == nil {
		return
	}
	paths, _ := secondaryKubeconfigsForCutover(dep)
	for key, path := range paths {
		cs, err := h.clientsetFromKubeconfigPath(path)
		if err != nil {
			h.log.Warn("cutover: secondary deny-egress backstop reap: clientset build failed (#5359)",
				"region", key, "err", err)
			continue
		}
		rc := cs.Discovery().RESTClient()
		if rc == nil {
			continue // fake clientsets (unit tests) expose no REST client
		}
		if err := rc.Delete().AbsPath(cutoverEgressBlockPolicyAbsPath).Do(ctx).Error(); err != nil && !apierrors.IsNotFound(err) {
			h.log.Warn("cutover: secondary deny-egress backstop reap FAILED — manual `kubectl --kubeconfig <region> delete ccnp cutover-egress-block` may be required (#5359)",
				"region", key, "err", err)
			continue
		}
	}
}
