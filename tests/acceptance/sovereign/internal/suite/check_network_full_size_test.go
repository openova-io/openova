package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// fullSizeAddr is the InternalIP of every node of the fixtures.
var fullSizeAddr = map[string]string{"a-cp1": "10.0.1.10", "a-w0": "10.0.1.20", "a-w1": "10.0.1.21", "a-w2": "10.0.1.22",
	"a-w3": "10.0.1.23", "a-w4": "10.0.1.24", "b-cp1": "10.0.2.10", "b-w1": "10.0.2.21", "b-w2": "10.0.2.22"}

// fullSizeNodeObj is a Node of a fixture; kind is worker, control, cordoned or notready.
func fullSizeNodeObj(name, kind string) obj {
	labels, ready := obj{"kubernetes.io/hostname": name}, "True"
	if kind == "control" {
		labels[fullSizeRole] = "true"
	}
	if kind == "notready" {
		ready = "False"
	}
	return obj{"metadata": obj{"name": name, "labels": labels}, "spec": obj{"unschedulable": kind == "cordoned"},
		"status": obj{"conditions": kubeConditions("Ready", ready),
			"addresses": []obj{{"type": "Hostname", "address": name}, {"type": "ExternalIP", "address": "203.0.113.9"},
				{"type": "InternalIP", "address": "fd00::9"}, {"type": "InternalIP", "address": fullSizeAddr[name]}}}}
}

// The fake tools of an exec: ip prints the addresses of the node, wget answers
// from the file answers (address, exit code, bytes; default 0 and 35000), and
// timeout runs its command only with the limit of 5 seconds.
const (
	fullSizeFakeIP = `#!/bin/sh
printf '1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever\n'
printf '2: eth0    inet %s/24 brd 10.0.255.255 scope global eth0\\       valid_lft forever\n' "$(cat "${0%/*}/address")"
printf '5: cilium_host    inet 10.42.1.7/32 scope global cilium_host\\       valid_lft forever\n'
`
	fullSizeFakeWget = `#!/bin/sh
[ "$FULL_SIZE_LIMIT" = 5 ] || exit 98
a=${4#http://}
set -- $(grep -F "${a%%/*} " "${0%/*}/answers") x 0 35000
head -c "$3" /dev/zero | tr '\000' a
exit "$2"
`
	fullSizeFakeTimeout = "#!/bin/sh\n[ \"$1\" = 5 ] || exit 99\nshift\nFULL_SIZE_LIMIT=5 exec \"$@\"\n"
)

// fullSizeFake answers kubectl for check NET-G18. Every exec runs the real
// script with sh, with the fake tools first in PATH and a folder that stands
// in for /sys/class/net.
type fullSizeFake struct {
	t                             *testing.T
	nodes                         map[string]string // region -> the answer to get nodes
	hostMTU, answers              map[string]string // node -> MTU of cilium_host (default 1500); "<node> <address>:<port>" -> "<exit> <bytes>"
	waitErr                       error
	pods                          map[string]string // "<region>/<pod>" -> node
	pod                           string            // the last object created with create -f -
	created, deleted, kinds, gets []string
}

func newFullSizeFake(t *testing.T) *fullSizeFake {
	return &fullSizeFake{t: t, hostMTU: map[string]string{}, answers: map[string]string{}, pods: map[string]string{}, nodes: map[string]string{
		"region-a": kubeList(fullSizeNodeObj("a-w4", "worker"), fullSizeNodeObj("a-w3", "worker"), fullSizeNodeObj("a-cp1", "control"),
			fullSizeNodeObj("a-w2", "worker"), fullSizeNodeObj("a-w1", "notready"), fullSizeNodeObj("a-w0", "cordoned")),
		"region-b": kubeList(fullSizeNodeObj("b-cp1", "control"), fullSizeNodeObj("b-w2", "worker"), fullSizeNodeObj("b-w1", "worker")),
	}}
}

func (f *fullSizeFake) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	verb := args[0]
	if verb == "-n" {
		verb = args[2]
	}
	switch {
	case verb == "get" && args[1] == "nodes":
		return []byte(f.nodes[r.ID]), nil
	case verb == "get" && strings.Join(args, " ") == "-n kube-system get configmap cilium-config -o jsonpath={.data.mtu}":
		return []byte("1500"), nil
	case verb == "create" && args[1] == "namespace":
		f.created = append(f.created, r.ID+"/"+args[2])
	case verb == "create":
		b, _ := io.ReadAll(stdin)
		var o kube.Object
		if err := json.Unmarshal(b, &o); err != nil || o.Namespace() != args[1] {
			f.t.Errorf("create -f - got an object outside its namespace %s: %s", args[1], b)
		}
		f.kinds, f.pod, f.pods[r.ID+"/"+o.Name()] = append(f.kinds, o.Str("kind")), string(b), o.Str("spec", "nodeName")
	case verb == "wait":
		return nil, f.waitErr
	case verb == "exec":
		node, dir := f.pods[r.ID+"/"+args[4]], f.t.TempDir()
		if strings.Join(args[7:12], " ") != "-- sh -s -- "+fullSizeNet {
			f.t.Fatalf("the probe does not run the script with sh: %v", args)
		}
		answers := ""
		for k, v := range f.answers {
			if from, to, _ := strings.Cut(k, " "); from == node {
				answers += to + " " + v + "\n"
			}
		}
		mtu := map[string]string{"eth0": "1500", "cilium_host": "1500", "cilium_wg0": "1420", "lo": "65536"}
		if m, ok := f.hostMTU[node]; ok {
			mtu["cilium_host"] = m
		}
		for dev, m := range mtu {
			hostFile(f.t, dir, "net/"+dev+"/mtu", 0o600, []byte(m+"\n"))
		}
		for name, text := range map[string]string{"bin/ip": fullSizeFakeIP, "bin/wget": fullSizeFakeWget, "bin/timeout": fullSizeFakeTimeout,
			"bin/address": fullSizeAddr[node], "bin/answers": answers} {
			hostFile(f.t, dir, name, 0o700, []byte(text))
		}
		if args[12] == "get" {
			f.gets = append(f.gets, node+">"+args[13]+":"+args[14])
		}
		cmd := exec.Command("sh")
		cmd.Args = append(append(cmd.Args, "-s", "--", dir+"/net"), args[12:]...)
		cmd.Env, cmd.Stdin = append(os.Environ(), "PATH="+dir+"/bin"+string(os.PathListSeparator)+os.Getenv("PATH")), stdin
		return cmd.Output()
	case verb == "delete" && args[1] == "namespace":
		f.deleted = append(f.deleted, r.ID+"/"+args[2])
	default:
		f.t.Fatalf("unexpected kubectl call %v", args)
	}
	return nil, nil
}

// fullSizeRun runs check NET-G18 on two regions the way the runner does and
// returns the runner and the errors of the failed steps. It fails the test
// unless every namespace that was created was deleted.
func fullSizeRun(t *testing.T, f *fullSizeFake) (*runner, string) {
	t.Helper()
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := fullSizeCheck{kubectl: f.kubectl}
	cc := &CheckContext{Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, cc, true)
	if strings.Join(f.created, " ") != strings.Join(f.deleted, " ") || (len(f.created) > 0 && !strings.Contains(f.created[0], "/net-path-")) {
		t.Errorf("namespaces created %v, deleted %v; want net-path-<run>, each deleted", f.created, f.deleted)
	}
	return r, strings.Join(failedSteps(r), "\n")
}

// fullSizeFails runs the check and wants exactly one failed step that holds every part of want.
func fullSizeFails(t *testing.T, f *fullSizeFake, want ...string) {
	t.Helper()
	_, failed := fullSizeRun(t, f)
	for _, w := range want {
		if strings.Count(failed, "\n") != 0 || !strings.Contains(failed, w) {
			t.Errorf("want one failed step that holds %q, got %q", w, failed)
		}
	}
}

func TestFullSizePathCheck(t *testing.T) {
	t.Run("the first two workers by name are chosen in each region", func(t *testing.T) {
		f := newFullSizeFake(t)
		r, failed := fullSizeRun(t, f)
		pods := []string{f.pods["region-a/probe-0"], f.pods["region-a/probe-1"], f.pods["region-b/probe-0"], f.pods["region-b/probe-1"]}
		if failed != "" || len(r.run.Steps) != 14 || strings.Join(pods, " ") != "a-w2 a-w3 b-w1 b-w2" || len(f.gets) != 12 {
			t.Errorf("want a pass in 14 steps with probes on a-w2 a-w3 b-w1 b-w2 and 12 GETs, got %q, %d steps, %v, %v", failed, len(r.run.Steps), pods, f.gets)
		}
		if e := r.run.Steps[0].Evidence; e["control_plane"] != "a-cp1" || e["namespace"] == "" {
			t.Errorf("the start step of region-a records %v", e)
		}
		if e := r.run.Steps[2].Evidence; e["interface"] != "eth0" || e["interface_mtu"] != "1500" || e["cilium_wg0_mtu"] != "1420" || e["cilium_config_mtu"] != "1500" {
			t.Errorf("the MTU step of a-w2 records %v", e)
		}
	})
	t.Run("a region with fewer than two workers fails", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.nodes["region-b"] = kubeList(fullSizeNodeObj("b-cp1", "control"), fullSizeNodeObj("b-w1", "worker"), fullSizeNodeObj("b-w2", "cordoned"))
		fullSizeFails(t, f, "region region-b has 1 Ready, schedulable worker(s)", "not measured")
		if strings.Join(f.created, " ") != f.created[0] || len(f.gets) != 4 {
			t.Errorf("want a namespace and 4 GETs in region-a only, got %v and %v", f.created, f.gets)
		}
	})
	t.Run("cilium_host below the interface MTU fails", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.hostMTU["b-w2"] = "1420"
		fullSizeFails(t, f, "node b-w2 of region region-b: cilium_host has MTU 1420 and the interface eth0 that holds 10.0.2.22 has MTU 1500")
	})
	t.Run("a transfer that does not end within 5 seconds fails", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.answers["a-w2 10.0.1.23:9965"] = "143 0"
		fullSizeFails(t, f, "the GET from node a-w2 of http://10.0.1.23:9965/metrics (node a-w3) ended with exit code 143 and 0 bytes")
		f = newFullSizeFake(t) // a transfer that stalls after 20000 bytes
		f.answers["a-w2 10.0.1.23:9965"] = "124 20000"
		fullSizeFails(t, f, "(node a-w3) ended with exit code 124 and 20000 bytes")
	})
	t.Run("an answer below 16384 bytes fails", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.answers["a-w3 10.0.1.22:9965"] = "0 9000"
		fullSizeFails(t, f, "(node a-w2) ended with exit code 0 and 9000 bytes", "want exit code 0 and at least 16384 bytes within 5 seconds")
	})
	t.Run("a failed control transfer fails the pair as not measured", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.answers["b-w2 10.0.2.10:9965"] = "1 0"
		fullSizeFails(t, f, "not measured: the control transfer from the control-plane node b-cp1 of region region-b failed", "(node b-cp1) ended with exit code 1")
		if g := strings.Join(f.gets, " "); !strings.Contains(g, "b-w2>10.0.2.10:9965") || strings.Contains(g, "b-w2>10.0.2.21:9965") {
			t.Errorf("want the control GET of b-w2 and no GET of b-w1 from it, got %v", f.gets)
		}
	})
	t.Run("the first workers of two regions transfer both ways", func(t *testing.T) {
		f := newFullSizeFake(t)
		r, failed := fullSizeRun(t, f)
		want := "a-w2>10.0.2.10:9965 a-w2>10.0.2.21:9965 b-w1>10.0.1.10:9965 b-w1>10.0.1.22:9965"
		if failed != "" || len(f.gets) != 12 || strings.Join(f.gets[8:], " ") != want || !strings.Contains(r.run.Steps[11].Name, "node b-w1 of region region-b fetches at least 16384 bytes from node a-w2 of region region-a") {
			t.Errorf("want the last GETs %s, got %q and %v", want, failed, f.gets)
		}
		f = newFullSizeFake(t)
		f.answers["b-w1 10.0.1.22:9965"] = "0 9000"
		fullSizeFails(t, f, "the GET from node b-w1 of http://10.0.1.22:9965/metrics (node a-w2) ended with exit code 0 and 9000 bytes")
	})
	t.Run("the probe Pod is on the host network with no privilege volume or Service", func(t *testing.T) {
		f := newFullSizeFake(t)
		fullSizeRun(t, f)
		var o kube.Object
		if err := json.Unmarshal([]byte(f.pod), &o); err != nil {
			t.Fatal(err)
		}
		c := o.Objects("spec", "containers")[0]
		sc, _ := c.Get("securityContext").(map[string]any)
		drop, _ := c.Get("securityContext", "capabilities", "drop").([]any)
		if o.Get("spec", "hostNetwork") != true || o.Get("spec", "automountServiceAccountToken") != false || o.Get("spec", "volumes") != nil ||
			c.Get("volumeMounts") != nil || c.Str("image") != secProbeImage || o.Str("spec", "nodeName") != "b-w2" {
			t.Errorf("the probe Pod is not on the host network of its node, or has a token or a volume: %s", f.pod)
		}
		if sc["runAsUser"] != float64(65534) || sc["runAsNonRoot"] != true || sc["readOnlyRootFilesystem"] != true ||
			sc["allowPrivilegeEscalation"] != false || len(drop) != 1 || drop[0] != "ALL" {
			t.Errorf("the probe runs with privilege: %v", sc)
		}
		if k := strings.Join(f.kinds, " "); k != "Pod Pod Pod Pod" {
			t.Errorf("want four Pods created and nothing else (no Service), got %q", k)
		}
	})
	t.Run("probe Pods that are not Ready fail and the namespace is deleted", func(t *testing.T) {
		f := newFullSizeFake(t)
		f.waitErr = errors.New("timed out waiting for the condition")
		if r, failed := fullSizeRun(t, f); strings.Count(failed, "timed out") != 2 || len(f.gets) != 0 || len(r.run.Steps) != 4 || len(f.deleted) != 2 {
			t.Errorf("want two failed start steps, no GET and both namespaces deleted, got %q, %v, %d steps", failed, f.gets, len(r.run.Steps))
		}
		if c, ok := registry[fullSizeID].(fullSizeCheck); !ok || c.kubectl == nil {
			t.Errorf("the registry must hold %s with kube.Region.Run", fullSizeID)
		}
	})
}
