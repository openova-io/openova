// tenant_console_tls_regions.go — #5246. The per-Org console listener pair is
// written into EVERY region the Sovereign serves, not just the one this
// controller happens to run in.
//
// THE DEFECT
// ----------
// A 2-region Sovereign is TWO separate k3s clusters joined by Cilium
// ClusterMesh (see internal/clusterregistry's package doc). The
// organization-controller Deployment exists in the region-A cluster only —
// measured on hw292 dep 1c56518035a83e03, `catalyst-system` carries the four
// Catalyst controllers in region A and NOTHING in region B. Every write in
// tenant_console_tls.go goes through the manager's client, which is bound to
// the local apiserver, so the per-Org `console-https-<slug>` /
// `console-http-<slug>` listener pair could only ever land in region A. Not
// "usually" — structurally.
//
// The public console EIP does not share that limitation. #5246 made both
// front-door ELB backend pools span every region's nodes so the EIPs survive a
// region-kill, so customer TLS for `console.<slug>.<parent>` arrives at
// whichever region the pool picks. A region with no per-Org listener answers
// the handshake with a reset. Measured on hw292 with fresh-TCP sampling:
// `console.uatco.omani.homes` 5/10 HTTP 200 against a 10/10 control on
// `console.hw292.omani.works` — same VIP, same ELB, same port. Region B's
// `kube-system/cilium-gateway-console` carried listeners for `r17probe`, an Org
// deleted days earlier, and nothing for either live Org.
//
// THE SEAM — no new credential contract
// -------------------------------------
// The secondary regions' kubeconfigs are ALREADY materialized in-cluster by
// catalyst-api's #5359 bridge:
//
//	Secret <cutover-ns>/cutover-secondary-kubeconfigs
//	  data: "<regionKey>.yaml" -> kubeconfig bytes   (one key per secondary)
//
// That is the same store ClusterMesh establish, the #5246 gateway-ELB member
// reconciler and the #5261 stalled-HR force-reconcile already reach region-B
// through. This file reads it and builds one controller-runtime client per
// secondary region. The org-controller ClusterRole already grants
// `secrets: get,list,watch` cluster-wide, so nothing new is granted.
//
// WHY IT CANNOT DEGRADE SILENTLY
// ------------------------------
// "No kubeconfig Secret" must not mean "no secondary regions" — that would be a
// guard whose scope is decided by the same step that loses the region, exactly
// the shape #5246 found in catalyst-api (a dropped region never entered
// `targets`, so the admission read-back never looked at it and the emitter
// logged "admitted in every region" over a one-region list).
//
// So the region set is cross-checked against an INDEPENDENT in-cluster witness:
// the Cilium ClusterMesh config Secret `kube-system/cilium-clustermesh`, whose
// non-certificate keys name every REMOTE cluster this region is meshed with
// (hw292 region A: `hw292-me-east-b`). A Sovereign whose mesh declares N remote
// clusters while the kubeconfig Secret resolves fewer reports the shortfall as
// UNWIRED — verifyProvisioned turns that into a NOT-provisioned Organization
// naming the count, never a green Org over an unwritten region. A single-region
// Sovereign has no ClusterMesh Secret, declares zero remotes, and behaves
// EXACTLY as before this file existed.
//
// Refs #5246 · #5511 (the port + admission twin) · #5359 (the kubeconfig
// bridge) · #5930 (the catalyst-api twin of this fix).

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Console region fan-out defaults. All four are overridable through the
// Reconciler's env-configured fields per Inviolable Principle #4.
const (
	// consoleSecondaryKubeconfigSecretDefaultName / …Namespace name the
	// #5359 bridge Secret. The namespace default matches the cutover
	// namespace the chart installs bp-self-sovereign-cutover into.
	consoleSecondaryKubeconfigSecretDefaultName      = "cutover-secondary-kubeconfigs"
	consoleSecondaryKubeconfigSecretDefaultNamespace = "catalyst"

	// consoleClusterMeshSecretDefaultName / …Namespace name the Cilium
	// ClusterMesh config Secret used as the independent witness of how many
	// OTHER regions this Sovereign has.
	consoleClusterMeshSecretDefaultName      = "cilium-clustermesh"
	consoleClusterMeshSecretDefaultNamespace = "kube-system"

	// consoleHostRegionKey labels the region this controller runs in. It is
	// never derived from a kubeconfig — it IS the manager's own client.
	consoleHostRegionKey = "local"

	// consoleRegionClientTimeout bounds every call made through a secondary
	// region's client. A wedged peer apiserver must degrade this Org's
	// reconcile into a requeue, never hang the work queue.
	consoleRegionClientTimeout = 20 * time.Second
)

// consoleRegionTarget is one cluster the per-Org console surface is written to.
// The host region always leads the slice so the Certificate (which only ever
// exists in the issuing region) is handled before any mirror.
type consoleRegionTarget struct {
	// Region is the region key — consoleHostRegionKey for the local cluster,
	// otherwise the `<regionKey>` half of the bridge Secret's data key.
	Region string
	// Host reports whether this target is the cluster the controller runs in.
	Host bool
	// Client writes into that region's apiserver.
	Client client.Client
}

// consoleRegionResolution is one pass of region discovery.
type consoleRegionResolution struct {
	// Targets — every region a write can actually be made to, host first.
	Targets []consoleRegionTarget
	// Unwired — regions the ClusterMesh witness proves exist but for which
	// no kubeconfig is wired at all. STRUCTURAL: the listener can never be
	// written there until an operator supplies the credential, so it is
	// reported as a missing artifact, not as a transient.
	Unwired []string
	// Unreachable — regions whose kubeconfig IS wired but whose client could
	// not be built or used this pass. TRANSIENT: an apiserver blip must not
	// red-flag every Organization on the Sovereign, so it only requeues.
	Unreachable []string
}

// regionClientCache memoizes the per-region clients between reconciles, keyed
// on the bridge Secret's resourceVersion. Building a client runs discovery
// against the remote apiserver; doing that per Org per 30s pass would put a
// steady discovery load on the peer region for no new information.
type regionClientCache struct {
	mu              sync.Mutex
	resourceVersion string
	clients         map[string]client.Client
}

// regionCacheInitMu guards the lazy allocation of Reconciler.regionClients.
// It is package-level rather than a Reconciler field so the Reconciler struct
// stays copy-safe (`go vet` copylocks) for the literal construction in
// cmd/main.go and the tests.
var regionCacheInitMu sync.Mutex

func (r *Reconciler) regionClientCache() *regionClientCache {
	regionCacheInitMu.Lock()
	defer regionCacheInitMu.Unlock()
	if r.regionClients == nil {
		r.regionClients = &regionClientCache{clients: map[string]client.Client{}}
	}
	return r.regionClients
}

func (r *Reconciler) secondaryKubeconfigSecretName() string {
	if v := strings.TrimSpace(r.SecondaryRegionKubeconfigSecretName); v != "" {
		return v
	}
	return consoleSecondaryKubeconfigSecretDefaultName
}

func (r *Reconciler) secondaryKubeconfigSecretNamespace() string {
	if v := strings.TrimSpace(r.SecondaryRegionKubeconfigSecretNamespace); v != "" {
		return v
	}
	return consoleSecondaryKubeconfigSecretDefaultNamespace
}

func (r *Reconciler) clusterMeshSecretName() string {
	if v := strings.TrimSpace(r.ClusterMeshSecretName); v != "" {
		return v
	}
	return consoleClusterMeshSecretDefaultName
}

func (r *Reconciler) clusterMeshSecretNamespace() string {
	if v := strings.TrimSpace(r.ClusterMeshSecretNamespace); v != "" {
		return v
	}
	return consoleClusterMeshSecretDefaultNamespace
}

// consoleRegionKeyFromSecretKey turns a bridge-Secret data key
// (`me-east-215-b.yaml`) into its region key (`me-east-215-b`). Keys that are
// not kubeconfig files are rejected by returning "".
func consoleRegionKeyFromSecretKey(k string) string {
	k = strings.TrimSpace(k)
	switch {
	case strings.HasSuffix(k, ".yaml"):
		return strings.TrimSuffix(k, ".yaml")
	case strings.HasSuffix(k, ".yml"):
		return strings.TrimSuffix(k, ".yml")
	}
	return ""
}

// clusterMeshRemoteClusters extracts the REMOTE cluster names from a Cilium
// ClusterMesh config Secret. The Secret carries, per remote cluster, one
// `<name>` config key plus `<name>-ca.crt` / `<name>.crt` / `<name>.key`
// material; only the config key names a cluster. The local cluster is never
// present. Returns a sorted, de-duplicated slice.
func clusterMeshRemoteClusters(sec *corev1.Secret) []string {
	seen := map[string]bool{}
	for k := range sec.Data {
		k = strings.TrimSpace(k)
		if k == "" || strings.HasSuffix(k, ".crt") || strings.HasSuffix(k, ".key") {
			continue
		}
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// newRegionClient builds a controller-runtime client from kubeconfig bytes.
// The RegionClientBuilder seam lets tests inject fakes without a real
// apiserver; production leaves it nil.
func (r *Reconciler) newRegionClient(kubeconfig []byte) (client.Client, error) {
	if r.RegionClientBuilder != nil {
		return r.RegionClientBuilder(kubeconfig)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	cfg.Timeout = consoleRegionClientTimeout
	c, err := client.New(cfg, client.Options{Scheme: r.Scheme()})
	if err != nil {
		return nil, fmt.Errorf("build client: %w", err)
	}
	return c, nil
}

// consoleRegionTargets resolves every region cluster the per-Org console
// surface must be written to, plus the regions it could not reach and why.
//
// The host region always leads. Secondary regions come from the #5359 bridge
// Secret; the ClusterMesh witness decides whether that set is COMPLETE. A
// resolution that lost a region says so — it never returns a short list that a
// downstream "verified in every region" check would then read as success.
func (r *Reconciler) consoleRegionTargets(ctx context.Context) consoleRegionResolution {
	res := consoleRegionResolution{
		Targets: []consoleRegionTarget{{Region: consoleHostRegionKey, Host: true, Client: r.Client}},
	}

	// The independent witness first: how many OTHER regions does this
	// Sovereign's mesh declare? Absent Secret == single-region Sovereign.
	meshRemotes := 0
	mesh := &corev1.Secret{}
	switch err := r.Get(ctx, client.ObjectKey{
		Namespace: r.clusterMeshSecretNamespace(),
		Name:      r.clusterMeshSecretName(),
	}, mesh); {
	case err == nil:
		meshRemotes = len(clusterMeshRemoteClusters(mesh))
	case apierrors.IsNotFound(err):
		// Single-region Sovereign (or ClusterMesh not yet established).
		// Zero expected secondaries; behaviour identical to pre-#5246.
	default:
		// Cannot decide completeness this pass. Report it as transient so
		// the Org requeues rather than being declared complete on an
		// unverified region set.
		res.Unreachable = append(res.Unreachable, fmt.Sprintf(
			"ClusterMesh witness %s/%s unreadable, so the expected region count is unknown: %s",
			r.clusterMeshSecretNamespace(), r.clusterMeshSecretName(), err))
	}

	bridgeNS, bridgeName := r.secondaryKubeconfigSecretNamespace(), r.secondaryKubeconfigSecretName()
	bridge := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: bridgeNS, Name: bridgeName}, bridge)
	if err != nil && !apierrors.IsNotFound(err) {
		res.Unreachable = append(res.Unreachable, fmt.Sprintf(
			"secondary-region kubeconfig Secret %s/%s unreadable: %s", bridgeNS, bridgeName, err))
		return res
	}

	regions := make([]string, 0, len(bridge.Data))
	kubeconfigFor := make(map[string][]byte, len(bridge.Data))
	if err == nil {
		for k, v := range bridge.Data {
			region := consoleRegionKeyFromSecretKey(k)
			if region == "" || len(v) == 0 {
				continue
			}
			regions = append(regions, region)
			kubeconfigFor[region] = v
		}
	}
	sort.Strings(regions)

	// The shortfall check is the whole anti-vacuity point: a mesh that
	// declares 1 remote cluster while 0 kubeconfigs are wired means every
	// per-Org listener this controller writes reaches exactly half the
	// regions the console ELB forwards to.
	if meshRemotes > len(regions) {
		res.Unwired = append(res.Unwired, fmt.Sprintf(
			"%d of %d secondary region(s) have no kubeconfig in %s/%s (ClusterMesh declares %d remote cluster(s); wired region keys: %s)",
			meshRemotes-len(regions), meshRemotes, bridgeNS, bridgeName, meshRemotes, describeRegionKeys(regions)))
	}

	cache := r.regionClientCache()
	cache.mu.Lock()
	if cache.resourceVersion != bridge.GetResourceVersion() {
		cache.resourceVersion = bridge.GetResourceVersion()
		cache.clients = map[string]client.Client{}
	}
	for _, region := range regions {
		c, ok := cache.clients[region]
		if !ok {
			built, buildErr := r.newRegionClient(kubeconfigFor[region])
			if buildErr != nil || built == nil {
				res.Unreachable = append(res.Unreachable, fmt.Sprintf(
					"region %q kubeconfig in %s/%s did not yield a client: %v", region, bridgeNS, bridgeName, buildErr))
				continue
			}
			cache.clients[region] = built
			c = built
		}
		res.Targets = append(res.Targets, consoleRegionTarget{Region: region, Client: c})
	}
	cache.mu.Unlock()

	return res
}

// describeRegionKeys renders a region-key slice for an operator-facing message,
// naming the empty case explicitly rather than rendering "[]".
func describeRegionKeys(regions []string) string {
	if len(regions) == 0 {
		return "none"
	}
	return strings.Join(regions, ",")
}

// mirrorConsoleOrgCertSecret copies the ISSUED per-Org TLS Secret from the host
// region into a secondary region, so that region's `console-https-<slug>`
// listener has material to terminate on.
//
// A second cert-manager Certificate in the peer region is deliberately NOT
// created: both would solve the same DNS-01 challenge on the same pool zone and
// burn two Let's-Encrypt issuances per Org for one SAN. Mirroring the issued
// Secret is what the catalyst-api twin does (#5511 mirrorOrgConsoleCertSecret)
// and keeps the issuance count at one per Org.
//
// Returns (changed, err). "The host Secret is not issued yet" is an error, not
// a silent skip: it is the state in which the peer region's listener is present
// but ResolvedRefs=False, and the caller must keep requeueing until it clears.
func (r *Reconciler) mirrorConsoleOrgCertSecret(ctx context.Context, dst client.Client, names orgConsoleTLSNames) (bool, error) {
	ns := r.consoleTLSCertNamespace()

	src := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.CertName}, src); err != nil {
		return false, fmt.Errorf("read host TLS Secret %s/%s to mirror: %w", ns, names.CertName, err)
	}
	if len(src.Data["tls.crt"]) == 0 || len(src.Data["tls.key"]) == 0 {
		return false, fmt.Errorf("host TLS Secret %s/%s carries no issued keypair yet", ns, names.CertName)
	}

	desired := &corev1.Secret{}
	desired.SetNamespace(ns)
	desired.SetName(names.CertName)
	desired.Type = src.Type
	desired.Data = map[string][]byte{}
	for k, v := range src.Data {
		desired.Data[k] = v
	}
	desired.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by":      "catalyst",
		"openova.io/managed-by":             "organization-controller",
		"catalyst.openova.io/component":     "cilium-gateway-console",
		"catalyst.openova.io/parent-zone":   names.ParentDomain,
		"catalyst.openova.io/org-subdomain": names.Slug,
		"catalyst.openova.io/mirrored-from": consoleHostRegionKey,
	})

	current := &corev1.Secret{}
	switch err := dst.Get(ctx, client.ObjectKey{Namespace: ns, Name: names.CertName}, current); {
	case apierrors.IsNotFound(err):
		if createErr := dst.Create(ctx, desired); createErr != nil {
			if apierrors.IsAlreadyExists(createErr) {
				return true, nil
			}
			return false, fmt.Errorf("mirror TLS Secret %s/%s: %w", ns, names.CertName, createErr)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("read mirrored TLS Secret %s/%s: %w", ns, names.CertName, err)
	}

	if secretDataEqual(current.Data, desired.Data) {
		return false, nil
	}
	current.Data = desired.Data
	current.Type = desired.Type
	current.SetLabels(desired.GetLabels())
	if err := dst.Update(ctx, current); err != nil {
		return false, fmt.Errorf("update mirrored TLS Secret %s/%s: %w", ns, names.CertName, err)
	}
	return true, nil
}

// deleteMirroredConsoleOrgCertSecret removes a mirrored per-Org TLS Secret from
// a secondary region. Absent-as-success, matching every other teardown helper.
func (r *Reconciler) deleteMirroredConsoleOrgCertSecret(ctx context.Context, dst client.Client, names orgConsoleTLSNames) (bool, error) {
	ns := r.consoleTLSCertNamespace()
	sec := &corev1.Secret{}
	sec.SetNamespace(ns)
	sec.SetName(names.CertName)
	if err := dst.Delete(ctx, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete mirrored Secret %s/%s: %w", ns, names.CertName, err)
	}
	return true, nil
}

// secretDataEqual compares two Secret data maps byte-for-byte.
func secretDataEqual(a, b map[string][]byte) bool {
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
