package meshagents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/meshstatus"
)

func agentPod(name, node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace, Labels: map[string]string{"k8s-app": "cilium"}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// region is a region of six nodes: one control-plane node and five workers.
func region() *kfake.Clientset {
	objs := []runtime.Object{agentPod("cilium-cp", "cp1", corev1.PodRunning)}
	for _, w := range []string{"w1", "w2", "w3", "w4", "w5"} {
		objs = append(objs, agentPod("cilium-"+w, w, corev1.PodRunning))
	}
	// A Pod of another DaemonSet must not be read as an agent.
	other := agentPod("clustermesh-proxy-x", "w1", corev1.PodRunning)
	other.Labels = map[string]string{"k8s-app": "clustermesh-proxy"}
	return kfake.NewSimpleClientset(append(objs, other)...)
}

// answers returns an Exec that prints, per Pod, the given status line.
func answers(byPod map[string]string, fallback string) Exec {
	return func(_ context.Context, pod string) (string, error) {
		if out, ok := byPod[pod]; ok {
			if out == "error" {
				return "", errors.New("unable to upgrade connection: Forbidden\nsecond line")
			}
			return out, nil
		}
		return fallback, nil
	}
}

func TestNotReady(t *testing.T) {
	peers := []string{"alpha-mesh"}
	t.Run("every agent reports the peer ready", func(t *testing.T) {
		if why := NotReady(context.Background(), region(), answers(nil, "alpha-mesh=true;"), peers); why != "" {
			t.Fatalf("NotReady = %q, want none", why)
		}
	})
	// The state the fresh installation was in: the control-plane node
	// connected, all five workers "waiting for initial connection".
	t.Run("the workers are not connected and the control-plane node is", func(t *testing.T) {
		why := NotReady(context.Background(), region(), answers(map[string]string{"cilium-cp": "alpha-mesh=true;"}, "alpha-mesh=false;"), peers)
		for _, want := range []string{"5 of 6 agents", "w1 (alpha-mesh not ready)", "w2 (", "w3 (", "and 2 more"} {
			if !strings.Contains(why, want) {
				t.Fatalf("NotReady = %q, want it to hold %q", why, want)
			}
		}
		if strings.Contains(why, "cp1") {
			t.Fatalf("NotReady = %q names the connected control-plane node", why)
		}
	})
	t.Run("an agent that does not know the peer at all is not ready", func(t *testing.T) {
		why := NotReady(context.Background(), region(), answers(map[string]string{"cilium-w3": ""}, "alpha-mesh=true;"), peers)
		if !strings.Contains(why, "1 of 6 agents") || !strings.Contains(why, "w3 (alpha-mesh not ready)") {
			t.Fatalf("NotReady = %q", why)
		}
	})
	t.Run("an agent that is ready for one peer of two is not ready", func(t *testing.T) {
		why := NotReady(context.Background(), region(), answers(nil, "alpha-mesh=true;beta-mesh=false;"), []string{"alpha-mesh", "beta-mesh"})
		if !strings.Contains(why, "6 of 6 agents") || !strings.Contains(why, "beta-mesh not ready") {
			t.Fatalf("NotReady = %q", why)
		}
	})
	t.Run("an agent whose status cannot be read is not ready, and only the first line of the error is kept", func(t *testing.T) {
		why := NotReady(context.Background(), region(), answers(map[string]string{"cilium-w2": "error"}, "alpha-mesh=true;"), peers)
		if !strings.Contains(why, "w2 (status not read: unable to upgrade connection: Forbidden)") || strings.Contains(why, "second line") {
			t.Fatalf("NotReady = %q", why)
		}
	})
	t.Run("an agent Pod that is not running is not ready and is not asked", func(t *testing.T) {
		c := region()
		pending := agentPod("cilium-new", "w6", corev1.PodPending)
		if _, err := c.CoreV1().Pods(Namespace).Create(context.Background(), pending, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		asked := false
		run := func(_ context.Context, pod string) (string, error) {
			asked = asked || pod == "cilium-new"
			return "alpha-mesh=true;", nil
		}
		why := NotReady(context.Background(), c, run, peers)
		if !strings.Contains(why, "1 of 7 agents") || !strings.Contains(why, "w6 (agent Pod is Pending)") || asked {
			t.Fatalf("NotReady = %q, asked = %v", why, asked)
		}
	})
	t.Run("a region without agents is not ready", func(t *testing.T) {
		if why := NotReady(context.Background(), kfake.NewSimpleClientset(), answers(nil, "alpha-mesh=true;"), peers); !strings.Contains(why, "no Pod") {
			t.Fatalf("NotReady = %q", why)
		}
	})
	t.Run("without a client nothing is reported as ready", func(t *testing.T) {
		if why := NotReady(context.Background(), nil, answers(nil, "alpha-mesh=true;"), peers); why == "" {
			t.Fatal("NotReady = none")
		}
	})
}

func TestDemote(t *testing.T) {
	now := time.Date(2026, 11, 15, 6, 15, 0, 0, time.UTC)
	statuses := func() []meshstatus.ClusterMeshStatus {
		return []meshstatus.ClusterMeshStatus{
			{RegionKey: "", ClusterName: "alpha-mesh", ReadyAt: now, Peers: []meshstatus.PeerStatus{{Name: "alpha-b", Connected: true}}},
			{RegionKey: "b-1", ClusterName: "alpha-b", ReadyAt: now, Peers: []meshstatus.PeerStatus{{Name: "alpha-mesh", Connected: true}}},
			{RegionKey: "c-1", ClusterName: "alpha-c", Peers: []meshstatus.PeerStatus{{Name: "alpha-mesh", Error: "peer LB IP absent"}}},
		}
	}
	clients := map[string]*kfake.Clientset{"": region(), "b-1": region(), "c-1": region()}
	read := []string{}
	lookup := func(key string) (string, kubernetes.Interface) {
		read = append(read, key)
		return "/kubeconfigs/" + key, clients[key]
	}
	// Region b's workers do not reach the primary; the primary's agents reach region b.
	factory := func(path string, _ kubernetes.Interface) (Exec, error) {
		if path == "/kubeconfigs/b-1" {
			return answers(map[string]string{"cilium-cp": "alpha-mesh=true;"}, "alpha-mesh=false;"), nil
		}
		return answers(nil, "alpha-b=true;"), nil
	}
	st := statuses()
	msgs := Demote(context.Background(), st, lookup, factory)
	if len(msgs) != 1 || !strings.Contains(msgs[0], `region "b-1"`) || !strings.Contains(msgs[0], "5 of 6 agents") {
		t.Fatalf("messages = %q", msgs)
	}
	if st[0].ReadyAt.IsZero() || !st[0].Peers[0].Connected {
		t.Fatalf("the connected region lost its stamp: %+v", st[0])
	}
	if !st[1].ReadyAt.IsZero() || st[1].Peers[0].Connected || !strings.Contains(st[1].Peers[0].Error, "5 of 6 agents") {
		t.Fatalf("the region whose workers are not connected kept its stamp: %+v", st[1])
	}
	if strings.Join(read, ",") != ",b-1" {
		t.Fatalf("regions read = %q: a region that is not stamped must not be read", read)
	}
	if st[2].Peers[0].Error != "peer LB IP absent" {
		t.Fatalf("the reason of the region that was not read changed: %+v", st[2])
	}

	// What cannot be measured is not reported as connected.
	st = statuses()
	broken := func(string, kubernetes.Interface) (Exec, error) {
		return nil, errors.New("read kubeconfig: no such file")
	}
	msgs = Demote(context.Background(), st, lookup, broken)
	if len(msgs) != 2 || !st[0].ReadyAt.IsZero() || !st[1].ReadyAt.IsZero() || !strings.Contains(st[0].Peers[0].Error, "the agents cannot be read") {
		t.Fatalf("unreadable agents: messages = %q, statuses = %+v", msgs, st)
	}
}

// The command is the read-only status call, with the output this package parses.
func TestCommand(t *testing.T) {
	got := strings.Join(Command, " ")
	if got != "cilium-dbg status -o jsonpath={range .cluster-mesh.clusters[*]}{.name}={.ready};{end}" {
		t.Fatalf("Command = %q", got)
	}
	if _, err := NewExec("/does/not/exist", kfake.NewSimpleClientset()); err == nil {
		t.Fatal("NewExec read a kubeconfig that does not exist")
	}
}
