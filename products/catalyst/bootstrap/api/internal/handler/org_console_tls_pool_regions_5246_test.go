// org_console_tls_pool_regions_5246_test.go — #5246, the region-set-divergence
// lock.
//
// THE DEFECT THESE TESTS GO RED ON. PR #5246 (merge 6f62061a6, 2026-07-19)
// replaced `local.primary_lb_node_ips` with `local.gateway_lb_members` so the
// gateway AND console ELB backend pools span EVERY region's nodes — correct,
// and required for region-kill EIP failover (#5244). The pool's region set is
// enumerated by discoverSecondaryNodeInternalIPs (post_handover_gateway_elb.go)
// from the on-disk `<depID>-<regionKey>.yaml` kubeconfigs, whose own godoc
// calls that "the authoritative source of which secondary regions exist".
//
// The per-Org LISTENER writer was left on a DIFFERENT region set:
// h.k8sCache.Clusters(). That set is lossy by construction —
// k8scache.NewFactory logs `k8scache: skipping cluster` and CONTINUES when a
// cluster's AddCluster fails (factory.go:550-556), and a restarted chroot
// re-registers its secondaries asynchronously (hw292 logs the secondary being
// added 5m06s after boot, as a "runtime add"). Every region in the pool but
// absent from the cache receives customer traffic on the shared console EIP
// and has no `console-https-<slug>` listener to answer with, so the connection
// is reset at the TLS handshake.
//
// Measured on hw292 (dep 1c56518035a83e03) 2026-08-09, region A vs region B
// `kube-system/cilium-gateway-console` spec.listeners:
//
//	region A: console-https-uatco *.uatco.omani.homes, console-https-walk-stranger-two …
//	region B: console-https-r17probe *.r17probe.omani.homes   <- a DELETED Org, and nothing else
//
// Both regions' nodes are in the console ELB pool, so fresh-TCP samples of
// https://console.uatco.omani.homes measure 5/10 HTTP 200 against a 10/10
// control on https://console.hw292.omani.works — same VIP, same ELB, same
// port. (Fresh TCP per sample: a browser or an HTTP/2 loop pins one backend
// and structurally cannot sample a round-robin.)
//
// WHAT MAKES THESE TESTS ABLE TO FAIL. Both drive the real emitter with a
// k8sCache that carries ONLY the primary — the lossy state — while the on-disk
// kubeconfig set (the ELB pool's own source) names a secondary region. On the
// unfixed tree orgConsoleTLSTargets consults only the cache, returns the host
// target alone, and provisionOrgConsoleTLS then reports "listener pair
// admitted in every region" over a one-region list: the region that actually
// drops customer traffic is neither written nor read back, so the existing
// admission guard passes on the exact live cluster state that is broken.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// seedELBPoolKubeconfigs writes the on-disk kubeconfig set that DEFINES the
// console ELB backend pool's region span — `<depID>.yaml` for the primary plus
// one `<depID>-<regionKey>.yaml` per secondary — and points
// secondaryKubeconfigsDir() at it. Contents are irrelevant: the pool writer
// and this fix both key off the FILENAMES (onDiskSecondaryKubeconfigKeys), and
// the client builder is stubbed per-test.
func seedELBPoolKubeconfigs(t *testing.T, depID string, secondaryRegions ...string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
			t.Fatalf("seed kubeconfig %s: %v", name, err)
		}
	}
	write(depID + ".yaml")
	for _, r := range secondaryRegions {
		write(depID + "-" + r + ".yaml")
	}
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	return dir
}

// stubPoolRegionClients makes the on-disk-kubeconfig fallback hand back the
// given fakes, keyed by the kubeconfig file's basename. A path with no entry
// returns an error — that is the "region in the pool, no client obtainable"
// case, which must be reported, never silently dropped.
func stubPoolRegionClients(t *testing.T, byFile map[string]struct {
	dyn  dynamic.Interface
	core kubernetes.Interface
},
) {
	t.Helper()
	prev := orgConsoleTLSClientsFromKubeconfig
	orgConsoleTLSClientsFromKubeconfig = func(path string) (dynamic.Interface, kubernetes.Interface, error) {
		c, ok := byFile[filepath.Base(path)]
		if !ok {
			return nil, nil, errors.New("dial tcp: connect: connection refused")
		}
		return c.dyn, c.core, nil
	}
	t.Cleanup(func() { orgConsoleTLSClientsFromKubeconfig = prev })
}

// admitPerOrgListenersOnGet makes every GET of the console Gateway on this
// fake answer with the apex listener pair in spec AND the named per-Org
// listeners in status.listeners — i.e. a region whose cilium-operator DID
// admit the pair. It is what lets the "not ready" assertion below be about the
// unreached ELB-pool region and nothing else: with the host region admitting,
// the #5511 read-back is satisfied for every target, so the success line fires
// on the unfixed tree and cannot fire on the fixed one.
func admitPerOrgListenersOnGet(dyn *dynamicfake.FakeDynamicClient, perOrgListenerNames ...string) {
	statusListeners := []any{
		map[string]any{"name": "console-https"},
		map[string]any{"name": "console-http"},
	}
	for _, n := range perOrgListenerNames {
		statusListeners = append(statusListeners, map[string]any{"name": n})
	}
	dyn.PrependReactor("get", consoleGatewayGVR.Resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
		ga, ok := action.(k8stesting.GetAction)
		if !ok || ga.GetName() != consoleGatewayName {
			return false, nil, nil
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "Gateway",
			"metadata": map[string]any{
				"name": consoleGatewayName, "namespace": consoleGatewayNamespace, "generation": int64(2),
			},
			"spec": map[string]any{"listeners": []any{
				map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS"},
				map[string]any{"name": "console-http", "port": int64(8080), "protocol": "HTTP"},
			}},
			"status": map[string]any{
				"conditions": []any{map[string]any{"type": "Accepted", "status": "True", "observedGeneration": int64(2)}},
				"listeners":  statusListeners,
			},
		}}, nil
	})
}

// newPoolRegionHandler wires a chroot Handler whose host region is hostDyn and
// whose k8sCache carries ONLY the primary cluster — the lossy-cache state the
// live defect ran in. Logs are captured so the "every region" success claim
// can be asserted against.
func newPoolRegionHandler(t *testing.T, depID string, hostDyn *dynamicfake.FakeDynamicClient, hostCore *k8sfake.Clientset) (*Handler, *bytes.Buffer) {
	t.Helper()
	shrinkAdmitBudget(t)
	var logs bytes.Buffer
	h := &Handler{log: slog.New(slog.NewTextHandler(&logs, nil))}
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: hostCore, dyn: hostDyn}, nil
	})
	h.SetOrganizationDeps(OrganizationDeps{OTECHFQDN: "hw292.omani.works"})
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clusters: []k8scache.ClusterRef{{ID: depID, DynamicClient: hostDyn, CoreClient: hostCore}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	h.SetK8sCache(f, k8scache.NewSARCache(), "")
	return h, &logs
}

func uatcoRecord() store.OrganizationProvisionRecord {
	return store.OrganizationProvisionRecord{
		OrganizationID: "tid-uatco",
		Subdomain:      "uatco",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		OTECHFQDN:      "hw292.omani.works",
		CompanyName:    "UAT Co",
	}
}

// listenerHostnamesAppliedTo returns listener NAME -> rendered hostname for
// every Gateway SSA payload one region's client received. Asserting on the
// rendered name+hostname (not merely "the Gateway was patched") is what makes
// this able to go red on hw292: region B WAS patched there — for a different,
// deleted Org.
func listenerHostnamesAppliedTo(t *testing.T, dyn *dynamicfake.FakeDynamicClient) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, a := range dyn.Actions() {
		if a.GetResource() != consoleGatewayGVR || a.GetVerb() != "patch" {
			continue
		}
		pa, ok := a.(k8stesting.PatchAction)
		if !ok || pa.GetName() != consoleGatewayName {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(pa.GetPatch(), &obj); err != nil {
			t.Fatalf("unmarshal Gateway apply payload: %v", err)
		}
		listeners, found, err := unstructured.NestedSlice(obj, "spec", "listeners")
		if err != nil || !found {
			continue
		}
		for _, l := range listeners {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			if name, _ := lm["name"].(string); name != "" {
				hostname, _ := lm["hostname"].(string)
				out[name] = hostname
			}
		}
	}
	return out
}

// TestProvisionOrgConsoleTLS_WritesEveryRegionInTheELBPool — #5246 lock 1.
//
// The console ELB pool spans region `me-east-215-b-1` (its kubeconfig is on
// disk, which is exactly how discoverSecondaryNodeInternalIPs put that
// region's nodes into the pool). h.k8sCache carries only the primary. The
// per-Org listener pair MUST still be written into that region, because the
// shared console EIP already round-robins customer TLS onto its cilium-envoy.
//
// UNFIXED TREE: orgConsoleTLSTargets reads h.k8sCache.Clusters() only, yields
// the host target alone, and region B receives no apply at all.
func TestProvisionOrgConsoleTLS_WritesEveryRegionInTheELBPool(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep292")
	seedELBPoolKubeconfigs(t, "dep292", "me-east-215-b-1")

	hostDyn := fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
	hostCore := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"))
	regionBDyn := fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
	regionBCore := k8sfake.NewSimpleClientset()

	stubPoolRegionClients(t, map[string]struct {
		dyn  dynamic.Interface
		core kubernetes.Interface
	}{
		"dep292-me-east-215-b-1.yaml": {dyn: regionBDyn, core: regionBCore},
	})

	h, _ := newPoolRegionHandler(t, "dep292", hostDyn, hostCore)
	h.provisionOrgConsoleTLS(context.Background(), uatcoRecord())

	for region, dyn := range map[string]*dynamicfake.FakeDynamicClient{
		"region-a-host": hostDyn, "region-b-elb-pool": regionBDyn,
	} {
		applied := listenerHostnamesAppliedTo(t, dyn)
		if got := applied["console-https-uatco"]; got != "*.uatco.omani.homes" {
			t.Errorf("[%s] console-https-uatco hostname = %q, want %q — this region is in the console ELB pool, so every connection it receives for console.uatco.omani.homes resets at the TLS handshake",
				region, got, "*.uatco.omani.homes")
		}
		if got := applied["console-http-uatco"]; got != "*.uatco.omani.homes" {
			t.Errorf("[%s] console-http-uatco hostname = %q, want %q", region, got, "*.uatco.omani.homes")
		}
	}

	// The listener's certificateRef must resolve in that region too — a
	// listener pointing at a Secret that is not there closes the door just as
	// completely (the per-region secret-split class #5394/#5406/#5414/#5416).
	if _, err := regionBCore.CoreV1().Secrets(consoleCertNamespace).
		Get(context.Background(), "org-wildcard-tls-uatco-omani-homes", metav1.GetOptions{}); err != nil {
		t.Errorf("cert secret not mirrored into the ELB-pool region: %v", err)
	}
}

// TestProvisionOrgConsoleTLS_UnreachablePoolRegionIsNotReportedReady —
// #5246 lock 2, the vacuity lock.
//
// A region is in the console ELB pool and NO client can be built for it. The
// pass must report NOT-ready and name the region. The pre-fix code path is
// worse than merely incomplete: it drops such a region from the target list
// before the #5511 admission read-back, so the read-back — the guard that is
// supposed to catch a closed door — iterates only over regions we chose to
// write and passes. A guard whose scope is decided by the same step that lost
// the region cannot fail on this defect.
// The single-region CONTROL is what makes the negative assertion mean
// something: same fixture, same admitting host Gateway, one variable changed —
// whether the ELB pool spans a second region. The control must still log the
// success line, so "no success line" in the pool case can only be caused by
// the unreached region and not by some unrelated failure in the fixture.
func TestProvisionOrgConsoleTLS_UnreachablePoolRegionIsNotReportedReady(t *testing.T) {
	for _, tc := range []struct {
		name            string
		poolSecondaries []string
		wantSuccessLine bool
		wantRegionNamed string
	}{
		{name: "control: console ELB pool is single-region", poolSecondaries: nil, wantSuccessLine: true},
		{name: "pool spans a region no client can be built for", poolSecondaries: []string{"me-east-215-b-1"}, wantSuccessLine: false, wantRegionNamed: "me-east-215-b-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SOVEREIGN_FQDN", "hw292.omani.works")
			t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep292")
			seedELBPoolKubeconfigs(t, "dep292", tc.poolSecondaries...)

			// The host Gateway ADMITS the pair (status.listeners carries both
			// names), so the #5511 read-back is satisfied for every region in
			// the target list. The ONLY remaining variable is the pool span.
			hostDyn := fakeDynForConsoleTLSWithApexPorts(t, 8443, 8080)
			admitPerOrgListenersOnGet(hostDyn, "console-https-uatco", "console-http-uatco")
			hostCore := k8sfake.NewSimpleClientset(issuedOrgWildcardSecret("org-wildcard-tls-uatco-omani-homes"))

			// No entry for any secondary kubeconfig => the builder errors,
			// mimicking an unreachable / unparseable secondary kubeconfig.
			stubPoolRegionClients(t, map[string]struct {
				dyn  dynamic.Interface
				core kubernetes.Interface
			}{})

			h, logs := newPoolRegionHandler(t, "dep292", hostDyn, hostCore)
			h.provisionOrgConsoleTLS(context.Background(), uatcoRecord())

			out := logs.String()
			gotSuccess := strings.Contains(out, "listener pair admitted in every region")
			if gotSuccess != tc.wantSuccessLine {
				t.Errorf("success line present = %t, want %t — the emitter must never claim EVERY region over a target list that silently lost one; logs:\n%s",
					gotSuccess, tc.wantSuccessLine, out)
			}
			if tc.wantRegionNamed != "" && !strings.Contains(out, tc.wantRegionNamed) {
				t.Errorf("the unreached ELB-pool region %q is not named anywhere in the output — an operator cannot act on it; logs:\n%s",
					tc.wantRegionNamed, out)
			}
		})
	}
}

// TestOrgConsoleTLSSelfDeploymentID_DerivesPrimaryWithoutEnv pins the fallback
// used when CATALYST_SELF_DEPLOYMENT_ID is unstamped: on a chroot the cache
// holds one primary plus its `<primary>-<region>` children, so the primary is
// the single id that is no other id's child. An ambiguous set (a mothership
// cache holding sibling deployments) must yield "" so the on-disk fallback
// never reads another deployment's kubeconfigs.
func TestOrgConsoleTLSSelfDeploymentID_DerivesPrimaryWithoutEnv(t *testing.T) {
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"primary only", []string{"1c56518035a83e03"}, "1c56518035a83e03"},
		{"primary + secondary", []string{"1c56518035a83e03", "1c56518035a83e03-me-east-215-b-1"}, "1c56518035a83e03"},
		{"sibling deployments (mothership)", []string{"depA", "depB"}, ""},
		{"empty", nil, ""},
	} {
		if got := orgConsoleTLSSelfDeploymentID(tc.ids); got != tc.want {
			t.Errorf("%s: orgConsoleTLSSelfDeploymentID(%v) = %q, want %q", tc.name, tc.ids, got, tc.want)
		}
	}
}

// TestOrgConsoleTLSPoolRegions_MatchesTheELBPoolEnumeration pins the fix's
// central claim: the listener writer's region set is read from the SAME files
// the console ELB pool writer reads. `.nodeip` sidecars and the primary's own
// `<depID>.yaml` are not regions; another deployment's kubeconfigs are not
// ours.
func TestOrgConsoleTLSPoolRegions_MatchesTheELBPoolEnumeration(t *testing.T) {
	dir := seedELBPoolKubeconfigs(t, "dep292", "me-east-215-b-1", "eu-west-101-c-1")
	if err := os.WriteFile(filepath.Join(dir, "dep292-me-east-215-b-1.nodeip"), []byte("10.218.1.5"), 0o600); err != nil {
		t.Fatalf("seed nodeip sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "otherdep-ap-southeast-3.yaml"), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("seed alien kubeconfig: %v", err)
	}

	got := orgConsoleTLSPoolRegions("dep292")
	want := map[string]string{
		"me-east-215-b-1": filepath.Join(dir, "dep292-me-east-215-b-1.yaml"),
		"eu-west-101-c-1": filepath.Join(dir, "dep292-eu-west-101-c-1.yaml"),
	}
	if len(got) != len(want) {
		t.Fatalf("pool regions = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("pool region %q = %q, want %q", k, got[k], v)
		}
	}
	if r := orgConsoleTLSPoolRegions(""); r != nil {
		t.Errorf("blank deployment id must yield no regions, got %v", r)
	}
}
