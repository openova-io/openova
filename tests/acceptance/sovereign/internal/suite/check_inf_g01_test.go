package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// rebootWorld is the fake cluster of check INF-G01. Both regions hold the
// same nodes. kured restarts a node by the timetable `restarts`: the seconds,
// counted from the moment the sentinel Pods of the region were created, at
// which the node is cordoned, shows a new boot identifier and is uncordoned;
// -1 is never. While the first node, the control-plane node, is cordoned and
// has not booted, the API does not answer. The clock moves only when the
// check waits.
type rebootWorld struct {
	t        *testing.T
	clock    time.Time
	nodes    []string
	args     []string          // of kured; nil: no DaemonSet
	restarts map[string][3]int // by node
	sick     map[string]string // node -> "cordoned" or "not ready" from the start, or "not ready after" its restart
	waitErr  error             // of kubectl wait for the sentinel Pods
	placed   map[string]time.Time
	created  []string // the regions that got a namespace
	deleted  []string // the regions whose namespace was deleted
	pods     []string // the nodes that got a sentinel Pod
}

func newRebootWorld() *rebootWorld {
	return &rebootWorld{clock: time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC), nodes: []string{"cp-1", "cp-2", "w-1", "w-2"},
		args: []string{"--period=15m", "--lock-release-delay=5m", "--drain-timeout=15m", "--reboot-days=su", "--reboot-days=mo",
			"--start-time=00:30", "--end-time=22:00", "--time-zone=UTC", "--reboot-sentinel=/sentinel/reboot-required", "--concurrency=1"},
		restarts: map[string][3]int{"cp-1": {600, 640, 700}, "w-1": {1800, 1840, 1900}, "w-2": {3000, 3040, 3100}, "cp-2": {-1, -1, -1}},
		sick:     map[string]string{}, placed: map[string]time.Time{}}
}

func (w *rebootWorld) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	at, placed := w.placed[r.ID]
	in := func(from, to int) bool {
		second := int(w.clock.Sub(at).Seconds())
		return placed && from >= 0 && second >= from && (to < 0 || second < to)
	}
	switch joined := strings.Join(args, " "); {
	case strings.HasSuffix(joined, " daemonsets -n kube-system"):
		if w.args == nil {
			return []byte(kubeList(obj{"metadata": obj{"name": "node-patch-config"}})), nil
		}
		return []byte(kubeList(obj{"metadata": obj{"name": "kured"}, "spec": obj{"template": obj{"spec": obj{
			"containers": []obj{{"name": "kured", "args": w.args}},
			"volumes":    []obj{{"name": "sentinel", "hostPath": obj{"path": "/var/run"}}}}}}})), nil
	case strings.HasSuffix(joined, " nodes"):
		if cp := w.restarts[w.nodes[0]]; in(cp[0], cp[1]) {
			return nil, errors.New("the API server does not answer")
		}
		list := []obj{}
		for _, name := range w.nodes {
			at, boot, ready := w.restarts[name], "boot-1", "True"
			if in(at[1], -1) {
				boot = "boot-2"
			}
			if w.sick[name] == "not ready" || (w.sick[name] == "not ready after" && boot == "boot-2") {
				ready = "False"
			}
			list = append(list, obj{"metadata": obj{"name": name, "labels": obj{"node-role.kubernetes.io/control-plane": strings.HasPrefix(name, "cp-")}},
				"spec":   obj{"unschedulable": in(at[0], at[2]) || w.sick[name] == "cordoned"},
				"status": obj{"nodeInfo": obj{"bootID": boot}, "conditions": kubeConditions("Ready", ready)}})
		}
		return []byte(strings.ReplaceAll(strings.ReplaceAll(kubeList(list...), `plane":true`, `plane":"true"`), `plane":false`, `plane":"false"`)), nil
	case args[0] == "create" && args[1] == "namespace":
		w.created = append(w.created, r.ID)
	case args[0] == "delete" && args[1] == "namespace":
		w.deleted = append(w.deleted, r.ID)
	case len(args) > 2 && args[2] == "create":
		var pod kube.Object
		b, _ := io.ReadAll(stdin)
		if err := json.Unmarshal(b, &pod); err != nil || pod.Namespace() != args[1] {
			w.t.Errorf("the sentinel Pod is not JSON in its namespace %s: %s", args[1], b)
			return nil, nil
		}
		container := pod.Objects("spec", "containers")[0]
		if !reflect.DeepEqual(container.Get("command"), []any{"touch", "/host/reboot-required"}) || container.Str("image") != secProbeImage ||
			pod.Objects("spec", "volumes")[0].Str("hostPath", "path") != "/var/run" || pod.Str("spec", "restartPolicy") != "Never" {
			w.t.Errorf("the sentinel Pod does not create reboot-required in /var/run of its node once: %s", b)
		}
		w.pods, w.placed[r.ID] = append(w.pods, pod.Str("spec", "nodeName")), w.clock
	case len(args) > 2 && args[2] == "wait":
		return nil, w.waitErr
	}
	return nil, nil
}

// rebootRun runs the check against w, after a journey J1 with the status j1.
func rebootRun(t *testing.T, w *rebootWorld, j1 string) (CheckResult, []*Step) {
	t.Helper()
	w.t = t
	c := rebootCheck{kubectl: w.kubectl, now: func() time.Time { return w.clock },
		wait: func(_ context.Context, d time.Duration) error { w.clock = w.clock.Add(d); return nil }}
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: []*Step{{Journey: "J1", ID: "J1.1", Status: j1}}}}
	m := &Manifest{Checks: []ManifestCheck{entry(rebootID, StateExpectedFail)}}
	cc := &CheckContext{RunIn: RunInCloud, Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
	r.runChecks(context.Background(), m, []Check{c}, cc, true)
	return r.run.Checks[0], r.run.Steps[1:]
}

func TestRebootCheck(t *testing.T) {
	without := func(flag string) func(*rebootWorld) {
		return func(w *rebootWorld) { w.args = slices.DeleteFunc(w.args, func(a string) bool { return a == flag }) }
	}
	for name, tc := range map[string]struct {
		change  func(w *rebootWorld)
		j1      string // the status of journey J1; "" is PASS
		fails   string // a part of the reason; "" when the check passes
		regions int    // the regions that got sentinel Pods
		waited  string // how long the first region waited for the reboot span; "" is 0s
		chosen  string // the nodes that got a sentinel Pod; "" is cp-1 w-1 w-2
	}{
		"three nodes restart one after the other in both regions": {regions: 2},
		"journey J1 did not pass":                                 {j1: Fail, fails: "journey J1: step J1.1 is FAIL"},
		"kured is not installed":                                  {change: func(w *rebootWorld) { w.args = nil }, fails: "kured is not installed"},
		"the period is not an argument":                           {change: without("--period=15m"), fails: "no usable value for: --period"},
		"the drain timeout is not an argument":                    {change: without("--drain-timeout=15m"), fails: "no usable value for: --drain-timeout"},
		"the span and the sentinel are not arguments": {change: func(w *rebootWorld) { w.args = w.args[:3] },
			fails: "no usable value for: --end-time, --reboot-sentinel with the hostPath of the volume sentinel, --start-time, --time-zone"},
		"three control-plane nodes and no other": {change: func(w *rebootWorld) {
			w.nodes, w.restarts["cp-2"], w.restarts["cp-3"] = []string{"cp-1", "cp-2", "cp-3"}, w.restarts["w-1"], w.restarts["w-2"]
		}, regions: 2, chosen: "cp-1 cp-2 cp-3"},
		"the region has two nodes":          {change: func(w *rebootWorld) { w.nodes = w.nodes[:2] }, fails: "the region has 2 nodes, 2 of them"},
		"the region has no control-plane":   {change: func(w *rebootWorld) { w.nodes = []string{"w-1", "w-2", "w-3"} }, fails: "the region has 3 nodes, 0 of them"},
		"a node is cordoned before":         {change: func(w *rebootWorld) { w.sick["cp-2"] = "cordoned" }, fails: "before the check started: cp-2"},
		"a node is not Ready before":        {change: func(w *rebootWorld) { w.sick["cp-2"] = "not ready" }, fails: "before the check started: cp-2"},
		"the span closes before the limit":  {change: func(w *rebootWorld) { w.clock = w.clock.Add(15 * time.Hour) }, fails: "open for 1h0m0s, the time limit is 1h15m0s, and the span opens again in 3h30m0s"},
		"the span is closed and opens soon": {change: func(w *rebootWorld) { w.clock = w.clock.Add(16 * time.Hour) }, regions: 2, waited: "2h30m0s"},
		"the sentinel Pods do not finish":   {change: func(w *rebootWorld) { w.waitErr = errors.New("timed out waiting for the condition") }, fails: "timed out waiting", regions: 1},
		"two nodes are unschedulable":       {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{650, 690, 750} }, fails: "2 nodes are unschedulable at once: cp-1, w-1", regions: 1},
		"a drain does not finish": {change: func(w *rebootWorld) { w.restarts["w-1"], w.restarts["w-2"] = [3]int{1800, -1, -1}, [3]int{-1, -1, -1} },
			fails: "node w-1 has been unschedulable for 20m5s", regions: 1},
		"a node restarts without a drain":    {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{-1, 1840, -1} }, fails: "node w-1 has a new boot identifier and was never read unschedulable", regions: 1},
		"the third node does not restart":    {change: func(w *rebootWorld) { w.restarts["w-2"] = [3]int{-1, -1, -1} }, fails: "1h15m0s after the pending reboots were placed: 2 of 3 nodes restarted, 0 unschedulable", regions: 1},
		"a node stays cordoned at the limit": {change: func(w *rebootWorld) { w.restarts["w-2"] = [3]int{4000, 4040, -1} }, fails: "3 of 3 nodes restarted, 1 unschedulable", regions: 1},
		"a node is not Ready at the limit":   {change: func(w *rebootWorld) { w.sick["w-2"] = "not ready after" }, fails: "3 of 3 nodes restarted, 0 unschedulable, 1 not Ready", regions: 1},
		"a longer period gives a longer limit": {change: func(w *rebootWorld) {
			w.args[0], w.restarts["w-2"] = "--period=30m", [3]int{6000, 6040, 6100}
		}, regions: 2},
	} {
		t.Run(name, func(t *testing.T) {
			w := newRebootWorld()
			if tc.change != nil {
				tc.change(w)
			}
			start := w.clock
			res, steps := rebootRun(t, w, nonEmpty(tc.j1, Pass))
			if (res.Status == Pass) != (tc.fails == "") || !strings.Contains(res.Reason, tc.fails) {
				t.Errorf("the check is %s (%s), want the reason %q", res.Status, res.Reason, tc.fails)
			}
			if len(w.created) != tc.regions || !reflect.DeepEqual(w.created, w.deleted) {
				t.Errorf("namespaces created in %v and deleted in %v, Pods on %v: want %d regions, each cleaned", w.created, w.deleted, w.pods, tc.regions)
			}
			if want := strings.TrimSpace(strings.Repeat(nonEmpty(tc.chosen, "cp-1 w-1 w-2")+" ", len(w.placed))); strings.Join(w.pods, " ") != want {
				t.Errorf("the sentinel Pods are on %v, want %s", w.pods, want)
			}
			if at, placed := w.placed["region-a"]; placed && (at.Sub(start).String() != nonEmpty(tc.waited, "0s") || steps[3].Evidence["waited"] != at.Sub(start).String()) {
				t.Errorf("the first region waited %s for the reboot span and recorded %v, want %q", at.Sub(start), steps[3].Evidence["waited"], tc.waited)
			}
			if last := steps[len(steps)-1]; tc.fails == "" && (last.Evidence["most_unschedulable"] != 1 || last.Evidence["failed_readings"] != 8 ||
				len(last.Evidence["boot_ids_after"].(map[string]string)) != 3) {
				t.Errorf("the evidence of the last step is %v", last.Evidence)
			}
		})
	}
}
