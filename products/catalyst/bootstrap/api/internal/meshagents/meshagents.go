// Package meshagents reads from the Cilium agents of a region whether they
// are connected to the other regions of the ClusterMesh.
//
// The mesh step of catalyst-api writes the peer configuration of a region and
// used to call the region meshed when that write succeeded. A written
// configuration says nothing about the agents: an agent whose dial is dropped
// on the way keeps "0 nodes, waiting for initial connection" for ever, and
// the Services shared over the mesh have no backends on its node. Only the
// agent knows its own state, and it gives it through its status command and
// nowhere in the Kubernetes API. So this package runs that one read-only
// command in the agent container of every agent Pod of the region.
//
// It writes nothing: the only calls are a list of Pods and one exec of the
// status command per Pod.
package meshagents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/meshstatus"
)

// The agent Pods and the command that is run in them.
const (
	Namespace = meshstatus.Namespace
	Selector  = "k8s-app=cilium"
	Container = "cilium-agent"
	// execTimeout bounds one exec; a region of twenty nodes is read in well
	// under the time limit of one pass of the mesh step.
	execTimeout = 20 * time.Second
	// named is how many nodes a message names before it says "and N more".
	named = 3
)

// Command prints one "<cluster name>=<true|false>;" per remote cluster the
// agent knows, from the field `ready` of its status.
var Command = []string{"cilium-dbg", "status", "-o", "jsonpath={range .cluster-mesh.clusters[*]}{.name}={.ready};{end}"}

// Exec runs Command in the agent container of one Pod and returns what it printed.
type Exec func(ctx context.Context, pod string) (string, error)

// ExecFactory builds the Exec of one region. NewExec is the one that talks to a cluster.
type ExecFactory func(kubeconfigPath string, client kubernetes.Interface) (Exec, error)

// Region gives the kubeconfig file and the client of the region with a key.
type Region func(key string) (kubeconfigPath string, client kubernetes.Interface)

// Demote takes the "fully meshed" stamp (ReadyAt) from every region whose
// agents do not all report every peer ready, and marks the peers of that
// region not connected, with the reason. It returns one message per region it
// demoted. A region that is not stamped is not read: its peers are not even
// configured. A region whose agents cannot be read is demoted too: what was
// not measured is not reported as connected.
func Demote(ctx context.Context, statuses []meshstatus.ClusterMeshStatus, region Region, newExec ExecFactory) []string {
	var out []string
	for i := range statuses {
		st := &statuses[i]
		if st.ReadyAt.IsZero() {
			continue
		}
		peers := make([]string, 0, len(st.Peers))
		for _, p := range st.Peers {
			peers = append(peers, p.Name)
		}
		path, client := region(st.RegionKey)
		why := ""
		if run, err := newExec(path, client); err != nil {
			why = fmt.Sprintf("the agents cannot be read: %v", err)
		} else {
			why = NotReady(ctx, client, run, peers)
		}
		if why == "" {
			continue
		}
		st.ReadyAt = time.Time{}
		for k := range st.Peers {
			st.Peers[k].Connected, st.Peers[k].Error = false, why
		}
		out = append(out, fmt.Sprintf("ClusterMesh: region %q has its peer configuration but is not connected: %s", st.RegionKey, why))
	}
	return out
}

// NotReady returns "" when the region has at least one agent Pod and every
// agent Pod reports every cluster of peers ready. Otherwise it returns the
// reason, naming the nodes.
func NotReady(ctx context.Context, client kubernetes.Interface, run Exec, peers []string) string {
	if client == nil {
		return "the agents cannot be read: no client"
	}
	listCtx, cancel := context.WithTimeout(ctx, execTimeout)
	pods, err := client.CoreV1().Pods(Namespace).List(listCtx, metav1.ListOptions{LabelSelector: Selector})
	cancel()
	if err != nil {
		return fmt.Sprintf("the agents cannot be read: list Pods %s in %s: %v", Selector, Namespace, err)
	}
	if len(pods.Items) == 0 {
		return fmt.Sprintf("no Pod %s in %s", Selector, Namespace)
	}
	var bad []string
	for _, pod := range pods.Items {
		if reason := agent(ctx, pod, run, peers); reason != "" {
			bad = append(bad, pod.Spec.NodeName+" ("+reason+")")
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	total, more := len(bad), ""
	if total > named {
		bad, more = bad[:named], fmt.Sprintf(" and %d more", total-named)
	}
	return fmt.Sprintf("%d of %d agents do not report every peer ready: %s%s", total, len(pods.Items), strings.Join(bad, ", "), more)
}

// agent returns why one agent Pod does not report every peer ready, or "".
func agent(ctx context.Context, pod corev1.Pod, run Exec, peers []string) string {
	if pod.Status.Phase != corev1.PodRunning {
		return "agent Pod is " + string(pod.Status.Phase)
	}
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	out, err := run(execCtx, pod.Name)
	if err != nil {
		return "status not read: " + firstLine(err.Error())
	}
	ready := map[string]bool{}
	for _, item := range strings.Split(out, ";") {
		if name, value, ok := strings.Cut(strings.TrimSpace(item), "="); ok {
			ready[name] = value == "true"
		}
	}
	for _, peer := range peers {
		if !ready[peer] {
			return peer + " not ready"
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 160 {
		line = line[:160]
	}
	return line
}

// NewExec returns the Exec of the cluster of a kubeconfig file. client must
// be a client of the same cluster.
func NewExec(kubeconfigPath string, client kubernetes.Interface) (Exec, error) {
	if client == nil {
		return nil, fmt.Errorf("no client")
	}
	raw, err := os.ReadFile(kubeconfigPath) // #nosec G304 -- the kubeconfig file of a region, a path from catalyst-api's own store
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	cfg, err := helmwatch.RESTConfigFromKubeconfig(string(raw))
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 0 // a stream has no request timeout; the context of each exec bounds it
	return func(ctx context.Context, pod string) (string, error) {
		req := client.CoreV1().RESTClient().Post().Resource("pods").Namespace(Namespace).Name(pod).SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{Container: Container, Command: Command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
		executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
		if err != nil {
			return "", err
		}
		var stdout, stderr bytes.Buffer
		if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
			return "", fmt.Errorf("%w: %s", err, firstLine(stderr.String()))
		}
		return stdout.String(), nil
	}, nil
}
