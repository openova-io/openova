package handler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/meshagents"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/meshstatus"
)

// The fake clusters of this package have no agent to ask, so every test of
// the package reads "connected" unless it installs its own agents below.
func init() {
	clusterMeshAgents = func(context.Context, []ClusterMeshStatus, meshagents.Region, meshagents.ExecFactory) []string {
		return nil
	}
}

// installClusterMeshAgents makes the mesh step read the agents with the real
// meshagents.Demote. The agent of Pod pod in the region of kubeconfigPath
// reports every other cluster ready when connected returns true.
func installClusterMeshAgents(t *testing.T, connected func(kubeconfigPath, pod string) bool) {
	t.Helper()
	prev := clusterMeshAgents
	t.Cleanup(func() { clusterMeshAgents = prev })
	clusterMeshAgents = func(ctx context.Context, statuses []ClusterMeshStatus, region meshagents.Region, _ meshagents.ExecFactory) []string {
		return meshagents.Demote(ctx, statuses, region, func(path string, _ kubernetes.Interface) (meshagents.Exec, error) {
			return func(_ context.Context, pod string) (string, error) {
				out := ""
				for _, st := range statuses {
					out += fmt.Sprintf("%s=%v;", st.ClusterName, connected(path, pod))
				}
				return out, nil
			}, nil
		})
	}
}

func seedAgents(t *testing.T, cs kubernetes.Interface, nodes ...string) {
	t.Helper()
	for _, node := range nodes {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "cilium-" + node, Namespace: clusterMeshNamespace, Labels: map[string]string{"k8s-app": "cilium"}},
			Spec:       corev1.PodSpec{NodeName: node},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if _, err := cs.CoreV1().Pods(clusterMeshNamespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create agent Pod: %v", err)
		}
	}
}

// The state of the fresh two-region installation of 15 November: both regions
// have their peer configuration, the follower's control-plane node is
// connected and its workers are not. The mesh step must not call that fully
// meshed and must not switch on the cross-region database pair. Once the
// workers connect, the next pass does both.
func TestAutoEstablishClusterMesh_AgentsNotConnectedIsNotFullyMeshed(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	defer installClusterMeshClientFactory(fx.clients)()
	defer installClusterMeshDynamicClientFactory(fx.dynClients)()
	seedAgents(t, fx.clients[fx.primaryKubeconfigPath], "a-cp1", "a-w1")
	seedAgents(t, fx.clients[fx.secondaryKubeconfigPath], "b-cp1", "b-w1", "b-w2")

	workersConnected := false
	installClusterMeshAgents(t, func(path, pod string) bool {
		return workersConnected || path != fx.secondaryKubeconfigPath || !strings.Contains(pod, "-w")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	statuses, converged, err := fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if len(statuses) != 2 || converged {
		t.Fatalf("first pass: %d statuses, converged=%v; want 2 and false", len(statuses), converged)
	}
	if got := countFullyMeshedRegions(statuses); got != 1 {
		t.Fatalf("first pass: %d regions fully meshed, want 1 (the primary): %+v", got, statuses)
	}
	if statuses[0].RegionKey != "" || statuses[0].ReadyAt.IsZero() {
		t.Fatalf("the primary, whose agents are connected, is not fully meshed: %+v", statuses[0])
	}
	follower := statuses[1]
	if !follower.ReadyAt.IsZero() || follower.Peers[0].Connected || !strings.Contains(follower.Peers[0].Error, "2 of 3 agents do not report every peer ready") {
		t.Fatalf("the follower, whose workers are not connected, reads as meshed: %+v", follower)
	}
	if !hasClusterMeshEvent(fx.dep, "warn", "has its peer configuration but is not connected", "b-w1", "b-w2") {
		t.Fatalf("no event names the workers that are not connected:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	if !hasClusterMeshEvent(fx.dep, "info", "completed (1/2 regions fully meshed)") {
		t.Fatalf("the summary does not count connected agents:\n%s", dumpClusterMeshEvents(fx.dep))
	}
	assertGateNotFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateNotFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")

	workersConnected = true
	statuses, _, err = fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	requireFullyMeshed(t, statuses)
	assertGateFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
	assertGateFlipped(t, fx.dynClients[fx.secondaryKubeconfigPath], "secondary")
}

// A region whose agents cannot be read is not reported as meshed. The fake
// regions have a kubeconfig path that is no file, so the real reader fails.
func TestAutoEstablishClusterMesh_UnreadableAgentsAreNotFullyMeshed(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	defer installClusterMeshClientFactory(fx.clients)()
	defer installClusterMeshDynamicClientFactory(fx.dynClients)()
	prev := clusterMeshAgents
	defer func() { clusterMeshAgents = prev }()
	clusterMeshAgents = meshagents.Demote

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	statuses, _, err := fx.handler.autoEstablishClusterMesh(ctx, fx.dep)
	if err != nil {
		t.Fatalf("autoEstablishClusterMesh: %v", err)
	}
	if got := countFullyMeshedRegions(statuses); got != 0 {
		t.Fatalf("%d regions fully meshed although no agent was read: %+v", got, statuses)
	}
	assertGateNotFlipped(t, fx.dynClients[fx.primaryKubeconfigPath], "primary")
}

func seedCiliumDaemonSet(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: clusterMeshNamespace}}
	if _, err := cs.AppsV1().DaemonSets(clusterMeshNamespace).Create(context.Background(), ds, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create DaemonSet cilium: %v", err)
	}
}

func ciliumHostAliases(t *testing.T, cs kubernetes.Interface) []corev1.HostAlias {
	t.Helper()
	ds, err := cs.AppsV1().DaemonSets(clusterMeshNamespace).Get(context.Background(), "cilium", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get DaemonSet cilium: %v", err)
	}
	return ds.Spec.Template.Spec.HostAliases
}

// The follower's workers could not reach the primary's mesh endpoint at its
// public address: the rule that admits the dial port names the follower's
// public source addresses, and one of them had been replaced. Where a region
// answers the dial port on its nodes (DaemonSet clustermesh-proxy), its peers
// must dial the internal address of its control-plane node instead. A region
// without that DaemonSet keeps the address of its Service.
func TestAutoEstablishClusterMesh_PeersDialTheControlPlaneInternalAddress(t *testing.T) {
	fx := newTestFixture(t, []string{"203.0.113.10", "203.0.113.20"})
	defer installClusterMeshClientFactory(fx.clients)()
	defer installClusterMeshDynamicClientFactory(fx.dynClients)()
	primary, follower := fx.clients[fx.primaryKubeconfigPath], fx.clients[fx.secondaryKubeconfigPath]
	seedCiliumDaemonSet(t, primary)
	seedCiliumDaemonSet(t, follower)

	// Only the primary answers the dial port on its nodes.
	ctx := context.Background()
	proxy := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: meshstatus.ProxyName, Namespace: clusterMeshNamespace}}
	if _, err := primary.AppsV1().DaemonSets(clusterMeshNamespace).Create(ctx, proxy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]struct {
		controlPlane bool
		ip           string
	}{"a-cp1": {true, "10.161.1.5"}, "a-w1": {false, "10.161.1.9"}} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
		if n.controlPlane {
			node.Labels[meshstatus.ControlPlaneLabel] = "true"
		}
		node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "203.0.113.10"}, {Type: corev1.NodeInternalIP, Address: n.ip}}
		if _, err := primary.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	statuses, err := fx.handler.AutoEstablishClusterMesh(runCtx, fx.dep)
	if err != nil {
		t.Fatalf("AutoEstablishClusterMesh: %v", err)
	}
	requireFullyMeshed(t, statuses)

	got := ciliumHostAliases(t, follower)
	if len(got) != 1 || got[0].IP != "10.161.1.5" || !strings.HasSuffix(got[0].Hostnames[0], ".mesh.cilium.io") {
		t.Fatalf("the follower's agents dial the primary at %+v, want the internal address 10.161.1.5 of its control-plane node", got)
	}
	got = ciliumHostAliases(t, primary)
	if len(got) != 1 || got[0].IP != "203.0.113.20" {
		t.Fatalf("the primary's agents dial the follower, which has no host-network proxy, at %+v, want its Service address 203.0.113.20", got)
	}
	// The status keeps the address of the Service: it is what the region publishes.
	if statuses[0].LoadBalancerIP != "203.0.113.10" {
		t.Fatalf("the primary's status address = %q, want 203.0.113.10", statuses[0].LoadBalancerIP)
	}
}
