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
//
// #5488 restart-recovery addendum: a catalyst-api restart wipes the
// process-local dep.secondaryKubeconfigPaths map and can leave the
// pre-flight resolving a degraded record whose ID is empty — while the
// Secret a prior run materialized (the only artifact the chart mounts)
// is still present and correct. When the process cannot re-derive the
// paths, the pre-flight FIRST accepts an already-satisfying Secret
// (count >= expected, non-empty values, not annotated for a different
// deployment); only then does it abort, with a message that names the
// ACTUAL condition (empty record id / ambiguous on-disk prefixes / no
// candidate paths resolved / genuinely missing files) instead of
// folding everything into "0 readable".
//
// #6027 addendum — the Secret stopped being a cutover-only artifact.
//
// The organization-controller reads THIS Secret to write the per-Org console
// listener pair into every region (tenant_console_tls_regions.go). While
// runCutover was the only caller, that credential existed only on a Sovereign
// that had already fired the operator-gated cutover — so on hw293 the Secret
// was NotFound in both regions and half of every customer TLS connection to
// `console.<slug>.<parent>` reset. Three things changed here; the write itself
// did not move, so there is still exactly one producer:
//
//   - A level-triggered caller (secondary_kubeconfig_secret_materializer.go)
//     runs the same function on an interval and on delivery.
//   - USABILITY replaced presence. A file, or a Secret value, that cannot
//     build a client counts as absent — the 95-byte hw293 stub satisfied every
//     non-empty check and could not authenticate.
//   - The region COUNT is max(record, CATALYST_CONFIGURED_REGIONS) and the
//     record may be nil. hw293's chroot restored 0 deployment records, so a
//     record-only denominator made the completeness check unable to fail on
//     the one Sovereign that needed it.
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
//
// #5488 — prefer a record with a NON-EMPTY ID. chrootServesDeployment
// discriminates on SovereignFQDN alone, so a degraded record synthesized
// with an empty ID (the historical chrootEnsureDeployment("") mint, now
// guarded at the source) matches exactly as well as the real imported
// record. sync.Map.Range order is nondeterministic, so before this
// preference the pre-flight sometimes resolved the empty-ID record and
// could not derive its own on-disk kubeconfig paths (hw291 dep
// 2c2d746b578c636b). A fully-populated match now always wins; the
// empty-ID record is returned only when it is the ONLY match, and every
// caller that builds paths from the record diagnoses that state
// explicitly instead of concatenating a blank.
func (h *Handler) resolveCutoverDeployment() *Deployment {
	var found *Deployment
	h.deployments.Range(func(_, value any) bool {
		dep, ok := value.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		if !h.chrootServesDeployment(dep) {
			return true
		}
		dep.mu.Lock()
		id := strings.TrimSpace(dep.ID)
		dep.mu.Unlock()
		if id != "" {
			found = dep
			return false // fully-populated match — stop scanning.
		}
		if found == nil {
			found = dep // degraded (empty-ID) match — keep scanning for better.
		}
		return true
	})
	return found
}

// secondaryKubeconfigResolution is the outcome of resolving regionKey →
// on-disk kubeconfig path for a deployment's secondary regions, with
// enough diagnostic context to tell an operator WHY resolution came up
// short (#5488): the current message-shape contract distinguishes "the
// file is unreadable" from "we never had a path to look at" from "the
// record cannot name itself".
type secondaryKubeconfigResolution struct {
	// paths maps regionKey → on-disk kubeconfig path.
	paths map[string]string
	// expected is the number of secondaries the deployment SPEC expects
	// (len(Regions)-1), for the fail-loud completeness check.
	expected int
	// depID is the trimmed dep.ID at resolution time. Empty = the
	// resolved record is degraded (#5488) and could not key the on-disk
	// `<depID>-<regionKey>.yaml` fallback directly.
	depID string
	// derivedPrefix is the on-disk deployment prefix used for the
	// fallback when depID was empty and EXACTLY ONE candidate prefix
	// existed on disk (unambiguous — safe to adopt).
	derivedPrefix string
	// ambiguousPrefixes carries the sorted candidate prefixes when depID
	// was empty and MORE THAN ONE deployment prefix existed on disk —
	// resolution refuses to guess between them (silently picking one
	// could feed a FOREIGN cluster's kubeconfig to the pivot legs).
	ambiguousPrefixes []string
	// regionsDeclared is len(dep.Request.Regions) — the SPEC's own count,
	// kept separately from `expected` because zero declared regions and
	// one declared region both yield expected == 0 and mean opposite
	// things (see specAuthoritative).
	regionsDeclared int
}

// specAuthoritative reports whether SOMETHING credible declared how many
// regions this Sovereign has — either the deployment record's own list or
// the chart-baked CATALYST_CONFIGURED_REGIONS. When neither speaks, an
// expected count of 0 means "nobody knows", not "this Sovereign is
// single-region", and acting on that zero would reap a correct Secret a
// prior pass materialized (the #5488 failure mode inverted).
func (r secondaryKubeconfigResolution) specAuthoritative() bool {
	return r.regionsDeclared > 0
}

// secondaryKubeconfigsForCutover resolves regionKey → on-disk path for
// every secondary region of dep, as the union of the in-memory
// dep.secondaryKubeconfigPaths map and the on-disk `<depID>-<regionKey>.yaml`
// files — the SAME union discoverSecondaryNodeInternalIPs (#5244) trusts,
// immune to in-memory loss across catalyst-api restarts (#4000).
//
// #5488 — when dep.ID is empty (a degraded record synthesized after a
// catalyst-api restart), the on-disk fallback derives the deployment
// prefix from the files actually present instead of concatenating a
// blank (`<dir>/-<key>.yaml`, which matched nothing on hw291). Exactly
// one candidate prefix → adopt it; multiple → record the ambiguity and
// resolve nothing (the caller prefers the already-materialized Secret
// and otherwise fails naming the candidates).
// #6027 — the region COUNT is the max of the record's own list and the
// chart-baked CATALYST_CONFIGURED_REGIONS, and `dep` may be nil.
//
// Measured on hw293 dep a0077ba47e3720e5: the chroot's catalyst-api logs
// `restored deployments from PVC count=0` — handover never fired, so no
// record was ever imported and resolveCutoverDeployment returns nil. Taking
// the record as the sole authority there hands the completeness check a
// denominator of zero, i.e. a guard that cannot fail: a 2-region Sovereign
// reads as single-region and the producer decides, correctly by its own
// arithmetic, that there is nothing to materialize.
//
// `sovereign-fqdn`'s `configuredRegions` is written by the IaC at provision
// time and is independent of both the deployment store and the k8scache — on
// hw293 it reads `hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod` while
// the store held nothing. It is the same witness placementDeploymentIdentity
// (#6015) and the org-controller's own region resolver (#6097) take the max
// against, so no new contract is invented.
//
// It supplies a COUNT only. Its region names are cloud regions
// (`hw-me-east-215-b-rtz-prod`) while the kubeconfig files are keyed by the
// secondary CP's own regionKey (`me-east-215-b-1`), so the KEYS still come
// from disk — the count says how many must be found, the disk says which.
func secondaryKubeconfigsForCutover(dep *Deployment) secondaryKubeconfigResolution {
	envDeclared := len(regionsFromEnv())
	expected, regionsDeclared, depID := 0, envDeclared, ""
	paths := map[string]string{}
	if envDeclared > 1 {
		expected = envDeclared - 1
	}
	if dep != nil {
		dep.mu.Lock()
		if n := len(dep.Request.Regions); n > 1 && n-1 > expected {
			expected = n - 1
		}
		if n := len(dep.Request.Regions); n > regionsDeclared {
			regionsDeclared = n
		}
		paths = make(map[string]string, len(dep.secondaryKubeconfigPaths))
		for k, v := range dep.secondaryKubeconfigPaths {
			paths[k] = v
		}
		depID = strings.TrimSpace(dep.ID)
		dep.mu.Unlock()
	}

	res := secondaryKubeconfigResolution{
		paths:           paths,
		expected:        expected,
		depID:           depID,
		regionsDeclared: regionsDeclared,
	}
	dir := secondaryKubeconfigsDir()

	prefix := depID
	if depID == "" {
		candidates := onDiskSecondaryKubeconfigPrefixes(dir)
		switch len(candidates) {
		case 0:
			return res // nothing derivable — caller diagnoses.
		case 1:
			for p := range candidates {
				prefix = p
			}
			res.derivedPrefix = prefix
		default:
			for p := range candidates {
				res.ambiguousPrefixes = append(res.ambiguousPrefixes, p)
			}
			sort.Strings(res.ambiguousPrefixes)
			return res // refuse to guess — caller diagnoses.
		}
	}

	for _, key := range onDiskSecondaryKubeconfigKeys(dir, prefix) {
		if _, ok := paths[key]; !ok {
			paths[key] = filepath.Join(dir, prefix+"-"+key+".yaml")
		}
	}
	return res
}

// onDiskSecondaryKubeconfigPrefixes derives candidate deployment-ID
// prefixes from the kubeconfig files present in dir, for the #5488
// empty-depID fallback: a stem P is a candidate iff the PRIMARY
// kubeconfig `P.yaml` exists AND at least one secondary `P-<key>.yaml`
// sibling exists (on a chroot #5131 always materializes the primary at
// `<kubeconfigsDir>/<id>.yaml`, so both are present after handover).
// Returns prefix → secondary region keys. A dir where only orphan
// `X-Y.yaml` files exist yields no candidates — there is no reliable
// way to split an arbitrary stem into prefix+regionKey, and guessing
// wrong would target a foreign cluster.
func onDiskSecondaryKubeconfigPrefixes(dir string) map[string][]string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	stems := make([]string, 0, len(entries))
	seen := map[string]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		stem := strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		if stem == "" {
			continue
		}
		if _, dup := seen[stem]; dup {
			continue
		}
		seen[stem] = struct{}{}
		stems = append(stems, stem)
	}
	out := map[string][]string{}
	for _, p := range stems {
		for _, q := range stems {
			if q == p || !strings.HasPrefix(q, p+"-") {
				continue
			}
			key := strings.TrimPrefix(q, p+"-")
			if key == "" {
				continue
			}
			out[p] = append(out[p], key)
		}
	}
	return out
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

	// A record-less process is NOT a single-region one (#6027). The mothership
	// has no record because it never runs the cutover; a chroot can have none
	// because handover never fired — hw293's catalyst-api logs `restored
	// deployments from PVC count=0` while its `sovereign-fqdn` ConfigMap
	// declares two regions. Returning 0 here, as this function used to, meant
	// the exact Sovereign that most needs the peer-region credential is the one
	// guaranteed not to get it. secondaryKubeconfigsForCutover now tolerates a
	// nil record and falls back to CATALYST_CONFIGURED_REGIONS for the count
	// and to the on-disk `<prefix>-<regionKey>.yaml` files for the keys; when
	// NEITHER speaks, specAuthoritative is false below and nothing is touched.
	dep := h.resolveCutoverDeployment()

	res := secondaryKubeconfigsForCutover(dep)
	paths, expected := res.paths, res.expected

	// annotationDepID identifies which deployment the Secret was materialized
	// for. Empty when the record is absent or degraded AND no single on-disk
	// prefix could be derived — the annotation is then omitted rather than
	// written blank, so acceptMaterializedSecondaryKubeconfigsSecret's
	// foreign-Secret check keeps a meaningful "" == unknown reading.
	annotationDepID := res.depID
	if annotationDepID == "" {
		annotationDepID = res.derivedPrefix
	}

	// THE SPEC DECIDES SINGLE-REGION, NOT THE DISK (#6027).
	//
	// secondaryKubeconfigsForCutover unions the in-memory path map with every
	// `<depID>-<regionKey>.yaml` it finds on disk, and that union is not
	// bounded by the declared region count. So a ONE-region deployment that
	// still carried a file from a prior topology fell straight past the
	// `expected > len(data)` check (0 > 1 is false) and materialized a Secret
	// naming a region the Sovereign does not have. Under the cutover's single
	// call that was a narrow window; under a level-triggered loop it would be
	// the steady state, and the control that a single-region Sovereign produces
	// no Secret — the one control that distinguishes this fix from a producer
	// that always writes — would be false.
	//
	// The `expected == 0` arm is split in two because zero has two meanings:
	//   - a record that DECLARES one region is authoritative; there are no
	//     secondaries, so any Secret is stale and is reaped;
	//   - a DEGRADED record (#5488: empty id, no declared regions) knows
	//     nothing. Reaping on that would delete the correct Secret a prior
	//     pass materialized, which is the #5488 failure mode inverted. Leave
	//     the object exactly as it is and say so.
	if expected == 0 {
		if !res.specAuthoritative() {
			h.log.Warn("secondary-kubeconfigs: NOBODY declares a region count — no deployment record and CATALYST_CONFIGURED_REGIONS empty; leaving any already-materialized Secret untouched rather than reaping it on an unknown topology (#5488 inverted). Recovery: check the `sovereign-fqdn` ConfigMap key `configuredRegions`, or re-import the record (POST /api/v1/internal/deployments/import)",
				"secret", deps.ns+"/"+name)
			return 0, nil
		}
		// Single-region: guarantee the mount stays empty so the chart legs
		// no-op — reap a stale Secret from a prior multi-region life.
		//
		// Get-before-Delete: this function is now also driven by a
		// level-triggered loop, so an unconditional Delete would issue one
		// apiserver write every tick, forever, to remove an object that has
		// never existed. Reading first makes the steady state a single cheap
		// Get and keeps the reap.
		if _, gerr := deps.core.CoreV1().Secrets(deps.ns).Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(gerr) {
			return 0, nil
		}
		if derr := deps.core.CoreV1().Secrets(deps.ns).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return 0, fmt.Errorf("cutover: delete stale %s/%s: %w", deps.ns, name, derr)
		}
		return 0, nil
	}

	data := make(map[string][]byte, len(paths))
	var missing []string
	for key, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			missing = append(missing, fmt.Sprintf("%s (%s: unreadable or empty)", key, path))
			continue
		}
		// USABILITY, not presence (#6027). Until this check existed the only
		// bar a file had to clear was len(raw) != 0, so the 95-byte hw293
		// stub — clusters+server and nothing else — was materialized into
		// the Secret and returned n=1. Every consumer then read region B as
		// CREDENTIALLED: the org-controller's newRegionClient failed at
		// clientcmd, logged the region as Unreachable, and the per-Org
		// console listener was never written there while the Secret sat in
		// the namespace looking complete. A key whose value cannot build a
		// client is worth exactly as much as an absent key, so it is counted
		// as one — the same fail-loud arm, naming the byte count and the
		// missing sections. See secondary_kubeconfig_completeness.go for the
		// measured artefact; this is the READ-side twin of the #6054
		// write-side gate, and it is what disqualifies a stub that landed on
		// disk BEFORE that gate shipped.
		if defects := secondaryKubeconfigDefects(string(raw)); len(defects) > 0 {
			missing = append(missing, fmt.Sprintf("%s (%s: %d bytes, cannot build a client — missing %s)",
				key, path, len(raw), strings.Join(defects, "+")))
			continue
		}
		data[sanitizeSecondaryRegionKey(key)+".yaml"] = raw
	}

	if expected > len(data) {
		// #5488 recovery check FIRST: an earlier run may already have
		// materialized the Secret correctly — a catalyst-api restart wipes
		// only the process-local path map, not the Secret. If the Secret
		// already carries every expected key, the materialization this
		// function exists to guarantee is DONE and the pre-flight passes.
		// The #5359 fail-loud contract is untouched: acceptance requires
		// the Secret to actually satisfy the spec's secondary count (with
		// non-empty values, and not annotated for a different deployment)
		// — a chain whose region-B legs would silently no-op still aborts
		// below.
		if n, ok := h.acceptMaterializedSecondaryKubeconfigsSecret(ctx, deps, name, expected, res.depID); ok {
			return n, nil
		}

		sort.Strings(missing)
		secretRef := deps.ns + "/" + name
		switch {
		case res.depID == "" && len(res.ambiguousPrefixes) > 0:
			// #5488 task 3 — ambiguous on-disk fallback: never silently
			// pick one of several deployment prefixes.
			return 0, fmt.Errorf(
				"cutover: the resolved deployment record carries an EMPTY deployment id (degraded record after a catalyst-api restart, #5488) and %d distinct deployment prefixes exist on disk in %s (%s) — refusing to guess which one is this Sovereign's; expected %d secondary region(s) and no already-materialized %s Secret satisfies them. Recovery: re-import the deployment record (POST /api/v1/internal/deployments/import from the mothership) so the record can name its own id, then re-fire the cutover (#5359 fail-loud contract preserved)",
				len(res.ambiguousPrefixes), secondaryKubeconfigsDir(), strings.Join(res.ambiguousPrefixes, ", "), expected, secretRef)
		case res.depID == "":
			// #5488 task 1 — the empty-ID record is its own diagnosable
			// condition, not "0 readable".
			return 0, fmt.Errorf(
				"cutover: the resolved deployment record carries an EMPTY deployment id (degraded record after a catalyst-api restart, #5488) while its spec expects %d secondary region(s) — the on-disk <depID>-<regionKey>.yaml kubeconfig paths in %s cannot be derived from a blank id, and no already-materialized %s Secret satisfies the expected count. Recovery: re-import the deployment record (POST /api/v1/internal/deployments/import from the mothership) or re-POST the secondary kubeconfig(s) via /sovereign/secondary-kubeconfig, then re-fire the cutover (#5359 fail-loud contract preserved)",
				expected, secondaryKubeconfigsDir(), secretRef)
		case len(paths) == 0:
			// #5488 task 2 — an empty candidate map is "we never looked",
			// not "the files were unreadable". Say so instead of printing
			// an empty missing list.
			return 0, fmt.Errorf(
				"cutover: deployment %s expects %d secondary region(s) but no candidate kubeconfig paths resolved (the in-memory secondary-kubeconfig map is empty — typical after a catalyst-api restart — and no %s-<regionKey>.yaml files were found in %s), and no already-materialized %s Secret satisfies the expected count — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
				res.depID, expected, res.depID, secondaryKubeconfigsDir(), secretRef)
		default:
			// Original #5359 contract: candidate paths existed but the
			// files are genuinely missing/unreadable/unusable — name each.
			return 0, fmt.Errorf(
				"cutover: deployment %s expects %d secondary region(s) but only %d kubeconfig(s) are USABLE (rejected: %s) — refusing to start a cutover whose region-B pivot legs would silently no-op (#5359)",
				res.depID, expected, len(data), strings.Join(missing, ", "))
		}
	}

	annotations := map[string]string{
		"catalyst.openova.io/materialized": time.Now().UTC().Format(time.RFC3339),
	}
	if annotationDepID != "" {
		annotations["catalyst.openova.io/deployment-id"] = annotationDepID
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
			Annotations: annotations,
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
		// Converged? Then write NOTHING (#6027). The `materialized`
		// annotation is a timestamp, so an unconditional Update rewrites the
		// object on every pass and bumps its resourceVersion. That is not
		// merely wasteful: the org-controller keys its per-region client
		// cache on exactly this Secret's resourceVersion
		// (tenant_console_tls_regions.go regionClientCache), so a churning
		// object throws away every secondary-region client and re-runs
		// discovery against the peer apiserver once per Org per tick. Under
		// the cutover's single call this was invisible; under a
		// level-triggered loop it would be a standing load. Measured on the
		// fake clientset: 3 identical passes produced 3 writes before this
		// check, 1 after.
		if secretDataEqualBytes(existing.Data, desired.Data) &&
			existing.Type == desired.Type &&
			len(existing.StringData) == 0 &&
			existing.Labels["app.kubernetes.io/component"] == desired.Labels["app.kubernetes.io/component"] &&
			existing.Annotations["catalyst.openova.io/deployment-id"] == desired.Annotations["catalyst.openova.io/deployment-id"] {
			return len(data), nil
		}
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
		"deploymentID", annotationDepID)
	return len(data), nil
}

// acceptMaterializedSecondaryKubeconfigsSecret reports whether the
// cutover-secondary-kubeconfigs Secret ALREADY satisfies the deployment
// spec's secondary-region count — the #5488 primary recovery path. A
// catalyst-api restart wipes the process-local dep.secondaryKubeconfig-
// Paths map (and can leave the pre-flight holding a degraded record that
// cannot re-derive its on-disk paths), but the Secret a prior run
// materialized is durable and is the ONLY artifact the chart's region-B
// legs actually mount. If it carries >= expected non-empty keys, the
// materialization contract is met and the pre-flight passes.
//
// Guard rails (the #5359 fail-loud contract is not weakened):
//   - acceptance NEVER fires when the Secret is absent, unreadable, or
//     carries fewer non-empty keys than the spec expects — those still
//     abort in the caller;
//   - when the resolving record has a non-empty depID AND the Secret's
//     deployment-id annotation names a DIFFERENT deployment, the Secret
//     is foreign and is NOT accepted.
//
// Returns (key count, true) on acceptance.
func (h *Handler) acceptMaterializedSecondaryKubeconfigsSecret(ctx context.Context, deps *cutoverDeps, name string, expected int, depID string) (int, bool) {
	if expected <= 0 {
		return 0, false // single-region never needs recovery.
	}
	existing, err := deps.core.CoreV1().Secrets(deps.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, false
	}
	if depID != "" {
		if ann := strings.TrimSpace(existing.Annotations["catalyst.openova.io/deployment-id"]); ann != "" && ann != depID {
			return 0, false // materialized for a different deployment.
		}
	}
	// USABILITY, not presence (#6027). `len(v) > 0` accepted the 95-byte
	// hw293 stub here exactly as it did on the read path — and this arm is
	// the one that runs when the process cannot re-derive its own on-disk
	// paths, i.e. precisely when nothing else would catch it. A key that
	// cannot build a client does not count toward the expected total.
	n := 0
	for _, v := range existing.Data {
		if len(v) > 0 && secondaryKubeconfigUsable(string(v)) {
			n++
		}
	}
	if n < expected {
		return 0, false
	}
	h.log.Info("cutover: secondary kubeconfigs Secret already materialized by a prior run — accepting as-is (#5488 recovery: process-local paths unresolvable after a catalyst-api restart)",
		"secret", deps.ns+"/"+name,
		"keys", n,
		"expected", expected,
		"deploymentID", depID)
	return n, true
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
	paths := secondaryKubeconfigsForCutover(dep).paths
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
