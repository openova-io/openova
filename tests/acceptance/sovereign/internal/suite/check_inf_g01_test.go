package suite

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// rebootWorld is the fake cluster of check INF-G01. Both regions hold the
// same nodes. kured restarts a node by the timetable `restarts`: the seconds,
// counted from the moment the sentinel Pod of the region was created, at
// which the node is cordoned, shows a new boot identifier and is uncordoned;
// -1 is never. While the first node, the control-plane node, is cordoned and
// has not booted, the API does not answer. The workload Pods of a node move
// off it once it is cordoned, unless stuck. The clock moves only when the
// check waits.
type rebootWorld struct {
	t          *testing.T
	clock      time.Time
	nodes      []string
	args       []string            // of kured; nil: no DaemonSet
	lock       string              // what the annotation rebootTestLock of the DaemonSet holds; "" is none
	lockKey    string              // the annotation that holds lock; "" is rebootTestLock
	servers    bool                // the DaemonSet can run on control-plane nodes
	workloads  map[string][]string // node -> the names of its workload Pods ("<namespace>/<name>"); every node also runs a Pod of a DaemonSet
	stuck      bool                // the workload Pods never move off their node
	restarts   map[string][3]int   // by node
	sick       map[string]string   // node -> "cordoned" or "not ready" from the start, or "not ready after" its restart
	noPlanKind bool                // the cluster does not serve the kind of the upgrade Plans
	plans      []obj               // the upgrade Plans
	kuredBack  int                 // the second from which kured has every Pod ready again after the Pods were placed; -1 never
	releases   map[string]string   // HelmRelease -> "ready" always, "never" Ready, "lost" from the first restart on, "back" Ready again at 3300 s
	noReleases [2]int              // the seconds from and to which the HelmReleases cannot be read; -1 is never
	waitErr    error               // of kubectl wait for the sentinel Pods
	placed     map[string]time.Time
	created    []string // the regions that got a namespace
	deleted    []string // the regions whose namespace was deleted
	pods       []string // the nodes that got a sentinel Pod
}

// rebootTestLock is the annotation in which the chart bp-kured writes the install hold.
const rebootTestLock = "catalyst.openova.io/kured-install-hold"

// rebootPlan is an upgrade Plan whose window is start, end and zone, on the given days.
func rebootPlan(name, start, end, zone string, days ...string) obj {
	return obj{"metadata": obj{"name": name, "namespace": "system-upgrade"}, "spec": obj{"concurrency": 1,
		"window": obj{"days": days, "startTime": start, "endTime": end, "timeZone": zone}}}
}

var rebootAllDays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

func newRebootWorld() *rebootWorld {
	return &rebootWorld{clock: time.Date(2026, 11, 20, 6, 0, 0, 0, time.UTC), nodes: []string{"cp-1", "w-1", "w-2", "w-3"},
		args: []string{"--period=15m", "--lock-release-delay=5m", "--drain-timeout=15m", "--reboot-days=su", "--reboot-days=mo",
			"--start-time=00:30", "--end-time=22:00", "--time-zone=UTC", "--reboot-sentinel=/sentinel/reboot-required", "--concurrency=1"},
		restarts:  map[string][3]int{"cp-1": {-1, -1, -1}, "w-1": {600, 640, 700}, "w-2": {-1, -1, -1}, "w-3": {-1, -1, -1}},
		workloads: map[string][]string{"cp-1": {"kube-system/etcd-cp-1"}, "w-1": {"shop/web-1", "shop/db-0"}, "w-2": {"shop/web-2"}},
		plans:     []obj{rebootPlan("k3s-server", "22:00", "00:00", "UTC", rebootAllDays...), rebootPlan("k3s-agent", "22:00", "00:00", "UTC", rebootAllDays...)},
		kuredBack: 750, releases: map[string]string{"flux-system/bp-cilium": "ready", "catalyst-system/bp-catalyst-platform": "back", "kube-system/bp-kured": "ready"},
		noReleases: [2]int{-1, -1}, sick: map[string]string{}, placed: map[string]time.Time{}}
}

func (w *rebootWorld) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	at, placed := w.placed[r.ID]
	second := int(w.clock.Sub(at).Seconds())
	in := func(from, to int) bool {
		return placed && from >= 0 && second >= from && (to < 0 || second < to)
	}
	if cp := w.restarts[w.nodes[0]]; args[0] == "get" && in(cp[0], cp[1]) {
		return nil, errors.New("the API server does not answer")
	}
	switch joined := strings.Join(args, " "); {
	case strings.HasSuffix(joined, " daemonsets -n kube-system"):
		if w.args == nil {
			return []byte(kubeList(obj{"metadata": obj{"name": "node-patch-config"}})), nil
		}
		desired := len(w.nodes)
		if !w.servers {
			desired = 0
			for _, name := range w.nodes {
				if !strings.HasPrefix(name, "cp-") {
					desired++
				}
			}
		}
		ready := desired
		if placed && (w.kuredBack < 0 || second < w.kuredBack) {
			ready--
		}
		meta, affinity := obj{"name": "kured"}, obj{}
		if w.lock != "" {
			key := rebootTestLock
			if w.lockKey != "" {
				key = w.lockKey
			}
			meta["annotations"] = obj{key: w.lock}
		}
		if !w.servers {
			affinity = obj{"nodeAffinity": obj{"requiredDuringSchedulingIgnoredDuringExecution": obj{"nodeSelectorTerms": []obj{
				{"matchExpressions": []obj{{"key": "node-role.kubernetes.io/control-plane", "operator": "DoesNotExist"}}}}}}}
		}
		return []byte(kubeList(obj{"metadata": meta, "spec": obj{"template": obj{"spec": obj{
			"affinity":   affinity,
			"containers": []obj{{"name": "kured", "args": w.args}},
			"volumes":    []obj{{"name": "sentinel", "hostPath": obj{"path": "/var/run"}}}}}},
			"status": obj{"desiredNumberScheduled": desired, "numberReady": ready}})), nil
	case strings.HasSuffix(joined, " plans.upgrade.cattle.io -A"):
		if w.noPlanKind {
			return nil, errors.New(`[` + r.ID + `] kubectl get plans.upgrade.cattle.io: exit status 1: error: the server doesn't have a resource type "plans"`)
		}
		return []byte(kubeList(w.plans...)), nil
	case strings.HasSuffix(joined, " helmreleases.helm.toolkit.fluxcd.io -A"):
		if in(w.noReleases[0], w.noReleases[1]) {
			return nil, errors.New("the API server does not answer")
		}
		list := []obj{}
		for name, state := range w.releases {
			ns, n, _ := strings.Cut(name, "/")
			ready := state == "ready" || (state == "lost" && !in(600, -1)) || (state == "back" && !in(600, 1500))
			list = append(list, obj{"metadata": obj{"namespace": ns, "name": n},
				"status": obj{"conditions": kubeConditions("Ready", map[bool]string{true: "True", false: "False"}[ready])}})
		}
		return []byte(kubeList(list...)), nil
	case strings.Contains(joined, " pods -A --field-selector "):
		_, selector, _ := strings.Cut(joined, " --field-selector ")
		list := []obj{}
		for _, node := range w.nodes {
			if selector != "status.phase=Running" && selector != "spec.nodeName="+node {
				continue
			}
			list = append(list, obj{"metadata": obj{"namespace": "kube-system", "name": "cilium-" + node, "uid": "uid-cilium-" + node,
				"ownerReferences": []obj{{"kind": "DaemonSet", "name": "cilium"}}}, "spec": obj{"nodeName": node}, "status": obj{"phase": "Running"}})
			moved := !w.stuck && in(w.restarts[node][0], -1)
			for _, name := range w.workloads[node] {
				ns, n, _ := strings.Cut(name, "/")
				meta := obj{"namespace": ns, "name": n, "uid": "uid-" + n, "ownerReferences": []obj{{"kind": "ReplicaSet", "name": n}}}
				if strings.HasPrefix(n, "etcd-") {
					meta = obj{"namespace": ns, "name": n, "uid": "uid-" + n, "annotations": obj{rebootMirror: "x"}}
				}
				if moved {
					continue // evicted: its replacement runs on another node, under another uid
				}
				list = append(list, obj{"metadata": meta, "spec": obj{"nodeName": node}, "status": obj{"phase": "Running"}})
			}
		}
		return []byte(kubeList(list...)), nil
	case strings.HasSuffix(joined, " nodes"):
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
	cc := &CheckContext{RunIn: RunInCloud, Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}, Caps: map[string]bool{CutoverComplete: true}}
	r.runChecks(context.Background(), m, []Check{c}, cc, true)
	return r.run.Checks[0], r.run.Steps[1:]
}

func TestRebootCheck(t *testing.T) {
	without := func(flag string) func(*rebootWorld) {
		return func(w *rebootWorld) { w.args = slices.DeleteFunc(w.args, func(a string) bool { return a == flag }) }
	}
	plan := func(p obj) func(*rebootWorld) { return func(w *rebootWorld) { w.plans[1] = p } }
	release := func(state string) func(*rebootWorld) {
		return func(w *rebootWorld) { w.releases["flux-system/bp-cilium"] = state }
	}
	for name, tc := range map[string]struct {
		change  func(w *rebootWorld)
		j1      string // the status of journey J1; "" is PASS
		fails   string // a part of the reason; "" when the check passes
		regions int    // the regions that got a sentinel Pod
		waited  string // how long the first region waited for the reboot span; "" is 0s
		chosen  string // the node that got the sentinel Pod; "" is w-1
	}{
		"one worker restarts in each region, its workloads move off and it comes back": {regions: 2},
		"journey J1 did not pass":       {j1: Fail, fails: "journey J1: step J1.1 is FAIL"},
		"kured is not installed":        {change: func(w *rebootWorld) { w.args = nil }, fails: "kured is not installed"},
		"the period is not an argument": {change: without("--period=15m"), fails: "no usable value for: --period"},
		"the install hold is in place": {change: func(w *rebootWorld) {
			w.lock, w.args = `{"nodeID":"catalyst-install-hold","TTL":0}`, append(w.args, "--lock-annotation="+rebootTestLock)
		}, fails: "carries the install hold (annotation " + rebootTestLock + ", held by catalyst-install-hold)"},
		"the install hold is released: kured locks in its default annotation": {change: func(w *rebootWorld) {
			w.lock = `{"nodeID":"catalyst-install-hold","TTL":0}`
		}, regions: 2},
		"the install hold in the default annotation": {change: func(w *rebootWorld) {
			w.lock, w.args = `{"nodeID":"catalyst-install-hold"}`, append(w.args, "--lock-annotation=weave.works/kured-node-lock")
			w.lockKey = "weave.works/kured-node-lock"
		}, fails: "carries the install hold (annotation weave.works/kured-node-lock, held by catalyst-install-hold)"},
		"a node holds the lock": {change: func(w *rebootWorld) {
			w.lock, w.args = `{"nodeID":"w-9","created":"2026-11-20T05:00:00Z","TTL":3600000000000}`, append(w.args, "--lock-annotation="+rebootTestLock)
		}, regions: 2},
		"kured can run on control-plane nodes": {change: func(w *rebootWorld) { w.servers = true },
			fails: "can run on a node with the label node-role.kubernetes.io/control-plane=true, so kured could restart a region's control-plane node"},
		"the drain timeout is not an argument": {change: without("--drain-timeout=15m"), fails: "no usable value for: --drain-timeout"},
		"the span and the sentinel are not arguments": {change: func(w *rebootWorld) { w.args = w.args[:3] },
			fails: "no usable value for: --end-time, --reboot-sentinel with the hostPath of the volume sentinel, --start-time, --time-zone"},
		"the first worker runs no workload: the next one is chosen": {change: func(w *rebootWorld) {
			w.workloads["w-1"], w.restarts["w-1"], w.restarts["w-2"] = nil, [3]int{-1, -1, -1}, [3]int{600, 640, 700}
		}, regions: 2, chosen: "w-2"},
		"no worker runs a workload": {change: func(w *rebootWorld) { w.workloads = map[string][]string{"cp-1": {"kube-system/etcd-cp-1"}} },
			fails: "none of the 3 worker nodes runs a workload Pod"},
		"the region has one worker":         {change: func(w *rebootWorld) { w.nodes = w.nodes[:2] }, fails: "the region has 1 worker nodes: the check needs 2"},
		"the region has no control-plane":   {change: func(w *rebootWorld) { w.nodes = []string{"w-1", "w-2", "w-3"} }, fails: "the region has 3 nodes and none with the label"},
		"a node is cordoned before":         {change: func(w *rebootWorld) { w.sick["w-2"] = "cordoned" }, fails: "before the check started: w-2"},
		"a node is not Ready before":        {change: func(w *rebootWorld) { w.sick["cp-1"] = "not ready" }, fails: "before the check started: cp-1"},
		"the span closes before the limit":  {change: func(w *rebootWorld) { w.clock = w.clock.Add(15*time.Hour + 30*time.Minute) }, fails: "open for 30m0s, the time limit is 35m0s, and the span opens again in 3h0m0s"},
		"the span is closed and opens soon": {change: func(w *rebootWorld) { w.clock = w.clock.Add(16 * time.Hour) }, regions: 2, waited: "2h30m0s"},
		"the sentinel Pod does not finish":  {change: func(w *rebootWorld) { w.waitErr = errors.New("timed out waiting for the condition") }, fails: "timed out waiting", regions: 1},
		"a control-plane node restarts": {change: func(w *rebootWorld) { w.restarts["cp-1"] = [3]int{900, 940, 1000} },
			fails: "control-plane node cp-1 restarted (boot identifier boot-2, before boot-1): kured must never restart a control-plane node", regions: 1},
		"two nodes are unschedulable": {change: func(w *rebootWorld) { w.restarts["w-2"] = [3]int{650, 690, 750} }, fails: "2 nodes are unschedulable at once: w-1, w-2", regions: 1},
		"a drain does not finish":     {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{600, -1, -1} }, fails: "node w-1 has been unschedulable for 20m5s", regions: 1},
		"the worker restarts without a drain": {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{-1, 640, -1} },
			fails: "node w-1 has a new boot identifier and was never read unschedulable", regions: 1},
		"the workloads do not move off": {change: func(w *rebootWorld) { w.stuck = true },
			fails: "2 of the 2 workload Pods that ran on w-1 are still on it after its restart, so its drain did not move them off: shop/db-0 uid-db-0, shop/web-1 uid-web-1", regions: 1},
		"the worker does not restart": {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{-1, -1, -1} },
			fails: "35m0s after the pending reboot was placed: 0 of 1 nodes restarted, 0 unschedulable", regions: 1},
		"the worker stays cordoned at the limit": {change: func(w *rebootWorld) { w.restarts["w-1"] = [3]int{1500, 1540, -1} },
			fails: "1 of 1 nodes restarted, 1 unschedulable", regions: 1},
		"the worker is not Ready at the limit": {change: func(w *rebootWorld) { w.sick["w-1"] = "not ready after" },
			fails: "1 of 1 nodes restarted, 0 unschedulable, 1 not Ready", regions: 1},
		"a longer period gives a longer limit": {change: func(w *rebootWorld) {
			w.args[0], w.restarts["w-1"] = "--period=30m", [3]int{2400, 2440, 2500}
		}, regions: 2},
		"the cluster does not serve the Plan kind": {change: func(w *rebootWorld) { w.noPlanKind = true }, regions: 2},
		"the cluster has no Plan":                  {change: func(w *rebootWorld) { w.plans = nil }, regions: 2},
		"a Plan has no window": {change: plan(obj{"metadata": obj{"name": "k3s-agent", "namespace": "system-upgrade"}, "spec": obj{"concurrency": 1}}),
			fails: "1 of 2 upgrade Plans break the rule that a window ends at least 30 minutes before the reboot span starts: Plan system-upgrade/k3s-agent has no spec.window"},
		"a Plan window overlaps the reboot span": {change: plan(rebootPlan("k3s-agent", "21:00", "23:00", "UTC", rebootAllDays...)),
			fails: "Plan system-upgrade/k3s-agent has a window that overlaps the reboot span 00:30-22:00 UTC"},
		"a Plan window ends 20 minutes before the span": {change: plan(rebootPlan("k3s-agent", "22:00", "00:10", "UTC", rebootAllDays...)),
			fails: "Plan system-upgrade/k3s-agent has a window that ends 20 minutes before the reboot span 00:30-22:00 UTC starts, less than 30"},
		"a Plan window of one day is judged as every day": {change: plan(rebootPlan("k3s-agent", "12:00", "13:00", "UTC", "sunday")),
			fails: "Plan system-upgrade/k3s-agent has a window that overlaps the reboot span"},
		"a Plan window in another time zone": {change: plan(rebootPlan("k3s-agent", "02:00", "04:00", "Asia/Muscat", rebootAllDays...)), regions: 2},
		"a Plan window is not written as HH:MM": {change: plan(rebootPlan("k3s-agent", "10pm", "00:00", "UTC", rebootAllDays...)),
			fails: "Plan system-upgrade/k3s-agent has no startTime and endTime of the form HH:MM"},
		"a Plan window starts when it ends": {change: plan(rebootPlan("k3s-agent", "22:00", "22:00", "UTC", rebootAllDays...)),
			fails: "Plan system-upgrade/k3s-agent has a window that starts when it ends"},
		"a Plan window has no time zone": {change: plan(rebootPlan("k3s-agent", "22:00", "00:00", "", rebootAllDays...)),
			fails: "Plan system-upgrade/k3s-agent has no time zone that the check knows"},
		"kured is not fully ready at the limit": {change: func(w *rebootWorld) { w.kuredBack = -1 },
			fails: "1 of 1 nodes restarted, 0 unschedulable, 0 not Ready; DaemonSet kube-system/kured has 2 of 3 Pods ready", regions: 1},
		"a failed read of the recovery is no observation": {change: func(w *rebootWorld) { w.kuredBack, w.noReleases = -1, [2]int{700, 1000} },
			fails: "DaemonSet kube-system/kured has 2 of 3 Pods ready", regions: 1},
		"a HelmRelease does not become Ready again": {change: release("lost"),
			fails: "1 of 3 HelmReleases that were Ready before are not Ready: flux-system/bp-cilium", regions: 1},
		"a HelmRelease that was not Ready before is not judged": {change: release("never"), regions: 2},
		"no HelmRelease is Ready before": {change: func(w *rebootWorld) {
			for name := range w.releases {
				w.releases[name] = "never"
			}
		}, fails: "none of 3 HelmReleases is Ready, so their recovery cannot be judged"},
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
			if want := strings.TrimSpace(strings.Repeat(nonEmpty(tc.chosen, "w-1")+" ", len(w.placed))); strings.Join(w.pods, " ") != want {
				t.Errorf("the sentinel Pods are on %v, want %s", w.pods, want)
			}
			if at, placed := w.placed["region-a"]; placed {
				span := slices.IndexFunc(steps, func(s *Step) bool { return strings.HasPrefix(s.Name, "the reboot span is open") })
				if span < 0 || at.Sub(start).String() != nonEmpty(tc.waited, "0s") || steps[span].Evidence["waited"] != at.Sub(start).String() {
					t.Errorf("the first region waited %s for the reboot span, want %q", at.Sub(start), tc.waited)
				}
			}
			workers := strconv.Itoa(len(w.nodes) - 1)
			if last := steps[len(steps)-1]; tc.fails == "" && (last.Evidence["most_unschedulable"] != 1 || last.Evidence["failed_readings"] != 0 ||
				len(last.Evidence["boot_ids_after"].(map[string]string)) != 1 || last.Evidence["kured_ready"] != workers+"/"+workers ||
				last.Evidence["workloads_moved_off"] == nil || len(last.Evidence["control_plane_boot_ids"].(map[string]string)) != 1) {
				t.Errorf("the evidence of the last step is %v", last.Evidence)
			}
		})
	}
}
