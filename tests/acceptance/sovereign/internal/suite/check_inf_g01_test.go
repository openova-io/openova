package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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
// off it once it is cordoned, unless stuck: each comes back on another node
// under a new uid, as back says. The name of a workload Pod says its
// controller: "job-..." a Job, "...-0" a StatefulSet, any other a ReplicaSet
// of a Deployment (its name up to the last "-"). The clock moves only when
// the check waits.
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
	back       map[string]string   // workload Pod -> how it comes back after it moved: "" Ready, "never" never Ready, "pull" waits on its image, "rolled" Ready under a new ReplicaSet
	late       map[string]int      // workload Pod -> the seconds after its node is cordoned at which its replacement is Ready; until then it is ContainerCreating
	deleting   map[string]int      // workload Pod -> the seconds after its node is cordoned during which it stays on the node with a deletion timestamp
	waits      map[string]string   // workload Pod that does not move -> "before" its container waits on its image from the start, "after" from the first restart on
	front      map[string]string   // node -> its endpoint of Service harbor/harbor: "ready", "not ready", "unset" (no condition ready); nil: no endpoint
	frontErr   error               // of get endpointslices
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

	doc        map[string]string    // the data of the variables document flux-system/sovereign-variables, both regions
	notes      map[string]string    // its annotations
	owner      string               // its label kustomize.toolkit.fluxcd.io/name: the Kustomization that applies it; "" none
	version    int                  // its resourceVersion; every change adds one, and a change made with another fails
	meddle     map[string]string    // keys that someone else writes into the document when the first sentinel Pod is created
	noDoc      bool                 // the variables document cannot be read
	applyAfter int                  // the seconds after a change of the document from which kured runs with its span; -1 never
	observe    int                  // the seconds from then until the status of kured's DaemonSet has observed its new generation
	roll       int                  // the seconds from then until every Pod of kured's DaemonSet is updated
	changed    map[string]time.Time // by region, the last change of the document
	patches    []string             // "<region> <merge patch>" of every change of the document
}

// later moves the clock of w by d, to an hour at which the configured span
// 00:30 to 22:00 UTC is closed or closes before the limit, and moves the
// window of its Plans to 23:30 to 00:00 UTC: that window ends 30 minutes
// before the configured span starts and leaves free the spans written for
// the run from 21:30 or 22:00 on, about 51 minutes in each region, the second
// region about half an hour after the first. The Plans of 22:00 to 00:00 do
// not leave them free: the check refuses such a span.
func (w *rebootWorld) later(d time.Duration) {
	w.clock = w.clock.Add(d)
	w.plans = []obj{rebootPlan("k3s-server", "23:30", "00:00", "UTC", rebootAllDays...), rebootPlan("k3s-agent", "23:30", "00:00", "UTC", rebootAllDays...)}
}

// rebootTestNote is the note an earlier run left with the span start-end,
// written over the values before (null: the key was absent).
func rebootTestNote(start, end, before string) string {
	return `{"run":"20261119T100000Z-ab12","written":"2026-11-19T10:00:00Z","values_before":` + before +
		`,"values_set":{"KURED_START_TIME":"` + start + `","KURED_END_TIME":"` + end + `"}}`
}

// rebootTestLock is the annotation in which the chart bp-kured writes the install hold.
const rebootTestLock = "catalyst.openova.io/kured-install-hold"

// rebootTestMark is the annotation of the variables document in which the check notes the span it wrote.
const rebootTestMark = "catalyst.openova.io/inf-g01-window"

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
		noReleases: [2]int{-1, -1}, sick: map[string]string{}, placed: map[string]time.Time{}, back: map[string]string{}, waits: map[string]string{},
		late: map[string]int{}, deleting: map[string]int{},
		doc: map[string]string{"KURED_START_TIME": "00:30", "KURED_END_TIME": "22:00", "SOVEREIGN_TIME_ZONE": "UTC"}, notes: map[string]string{}, version: 4711,
		applyAfter: 120, changed: map[string]time.Time{}}
}

// kuredArgs are the arguments of kured in region r: those of w, with the span
// of the variables document from applyAfter seconds after its last change on.
func (w *rebootWorld) kuredArgs(r kube.Region) []string {
	at, changed := w.changed[r.ID]
	if !changed || w.applyAfter < 0 || w.clock.Sub(at) < time.Duration(w.applyAfter)*time.Second {
		return w.args
	}
	out := slices.Clone(w.args)
	for i, a := range out {
		for flag, key := range map[string]string{"--start-time=": "KURED_START_TIME", "--end-time=": "KURED_END_TIME"} {
			if value, ok := w.doc[key]; ok && strings.HasPrefix(a, flag) {
				out[i] = flag + value
			}
		}
	}
	return out
}

// rebootTestPod is workload Pod ns/name on node, with its controller as its
// name says. hash is the pod-template-hash of a Pod of a ReplicaSet; wait is
// the reason its container "main" waits with, "init <reason>" the reason of
// its init container "prepare" (main then waits with PodInitializing), ""
// none.
func rebootTestPod(ns, name, uid, node, hash string, ready bool, wait string) obj {
	meta, base := obj{"namespace": ns, "name": name, "uid": uid}, name[:strings.LastIndex(name, "-")]
	switch {
	case strings.HasPrefix(name, "etcd-"):
		meta["annotations"] = obj{rebootMirror: "x"}
	case strings.HasPrefix(name, "job-"):
		meta["ownerReferences"] = []obj{{"kind": "Job", "name": name, "controller": true}}
	case strings.HasSuffix(name, "-0"):
		meta["ownerReferences"] = []obj{{"kind": "StatefulSet", "name": base, "controller": true}}
	default:
		meta["labels"] = obj{"pod-template-hash": hash}
		meta["ownerReferences"] = []obj{{"kind": "ReplicaSet", "name": base + "-" + hash, "controller": true}}
	}
	image := "registry.example/" + base + ":1"
	main := obj{"name": "main", "image": image, "state": obj{"running": obj{}}}
	status := obj{"phase": "Running", "conditions": kubeConditions("Ready", map[bool]string{true: "True", false: "False"}[ready]), "containerStatuses": []obj{main}}
	if reason, init := strings.CutPrefix(wait, "init "); init {
		status["initContainerStatuses"] = []obj{{"name": "prepare", "image": image, "state": obj{"waiting": obj{"reason": reason}}}}
		status["phase"], main["state"] = "Pending", obj{"waiting": obj{"reason": "PodInitializing"}}
	} else if wait != "" {
		status["phase"], main["state"] = "Pending", obj{"waiting": obj{"reason": wait}}
	}
	return obj{"metadata": meta, "spec": obj{"nodeName": node, "containers": []obj{{"name": "main", "image": image}}}, "status": status}
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
		// kured runs with a new span from applyAfter on: its template is then a
		// new generation, which the status observes observe seconds later and
		// every Pod runs roll seconds later.
		generation, observed, updated := 1, 1, desired
		if at, changed := w.changed[r.ID]; changed && w.applyAfter >= 0 {
			if since, apply := w.clock.Sub(at), time.Duration(w.applyAfter)*time.Second; since >= apply {
				generation = 2
				if since >= apply+time.Duration(w.observe)*time.Second {
					observed = 2
				}
				if since < apply+time.Duration(w.roll)*time.Second {
					updated--
				}
			}
		}
		meta, affinity := obj{"name": "kured", "generation": generation}, obj{}
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
			"containers": []obj{{"name": "kured", "args": w.kuredArgs(r)}},
			"volumes":    []obj{{"name": "sentinel", "hostPath": obj{"path": "/var/run"}}}}}},
			"status": obj{"desiredNumberScheduled": desired, "numberReady": ready, "updatedNumberScheduled": updated, "observedGeneration": observed}})), nil
	case strings.HasSuffix(joined, " configmap sovereign-variables -n flux-system"):
		if w.noDoc {
			return nil, errors.New(`[` + r.ID + `] kubectl get configmap: exit status 1: Error from server (NotFound): configmaps "sovereign-variables" not found`)
		}
		meta := obj{"name": "sovereign-variables", "namespace": "flux-system", "resourceVersion": strconv.Itoa(w.version), "annotations": w.notes}
		if w.owner != "" {
			meta["labels"] = obj{"kustomize.toolkit.fluxcd.io/name": w.owner, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
		}
		b, _ := json.Marshal(obj{"metadata": meta, "data": w.doc})
		return b, nil
	case args[0] == "patch" && strings.HasPrefix(joined, "patch configmap sovereign-variables -n flux-system --type merge -p "):
		var patch struct {
			Metadata struct {
				ResourceVersion string             `json:"resourceVersion"`
				Annotations     map[string]*string `json:"annotations"`
			} `json:"metadata"`
			Data map[string]*string `json:"data"`
		}
		if err := json.Unmarshal([]byte(args[len(args)-1]), &patch); err != nil || patch.Metadata.ResourceVersion == "" {
			w.t.Errorf("the change of the variables document is not a merge patch with the version read: %s", args[len(args)-1])
		}
		if v := patch.Metadata.ResourceVersion; v != "" && v != strconv.Itoa(w.version) { // the API server applies a merge patch without a version as it is
			return nil, errors.New(`[` + r.ID + `] kubectl patch configmap: exit status 1: Error from server (Conflict): Operation cannot be fulfilled on configmaps "sovereign-variables": the object has been modified`)
		}
		for into, values := range map[*map[string]string]map[string]*string{&w.doc: patch.Data, &w.notes: patch.Metadata.Annotations} {
			for key, value := range values {
				if delete(*into, key); value != nil {
					(*into)[key] = *value
				}
			}
		}
		w.version++
		w.changed[r.ID], w.patches = w.clock, append(w.patches, r.ID+" "+args[len(args)-1])
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
	case strings.HasSuffix(joined, " pods -A"):
		list := []obj{}
		for _, node := range w.nodes {
			list = append(list, obj{"metadata": obj{"namespace": "kube-system", "name": "cilium-" + node, "uid": "uid-cilium-" + node,
				"ownerReferences": []obj{{"kind": "DaemonSet", "name": "cilium", "controller": true}}}, "spec": obj{"nodeName": node},
				"status": obj{"phase": "Running", "conditions": kubeConditions("Ready", "True")}})
			moved, other := !w.stuck && in(w.restarts[node][0], -1), map[bool]string{true: "w-2", false: "w-3"}[node == "w-3"]
			for _, name := range w.workloads[node] {
				ns, n, _ := strings.Cut(name, "/")
				wait := map[string]string{"before": "init ErrImagePull", "after": "init ImagePullBackOff"}[w.waits[name]]
				if w.waits[name] == "after" && !in(600, -1) {
					wait = ""
				}
				if !moved {
					list = append(list, rebootTestPod(ns, n, "uid-"+n, node, "7f9c", wait == "", wait))
					continue
				}
				// evicted: its replacement runs on another node, under another uid
				cordon := w.restarts[node][0]
				if in(cordon, cordon+w.deleting[name]) {
					old := rebootTestPod(ns, n, "uid-"+n, node, "7f9c", true, "")
					old["metadata"].(obj)["deletionTimestamp"] = "2026-11-20T06:10:00Z"
					list = append(list, old)
				}
				state, hash := w.back[name], map[bool]string{true: "5d2b", false: "7f9c"}[w.back[name] == "rolled"]
				wait = map[bool]string{true: "ImagePullBackOff", false: ""}[state == "pull"]
				slow := in(cordon, cordon+w.late[name])
				if slow {
					wait = "ContainerCreating"
				}
				list = append(list, rebootTestPod(ns, n, "uid-"+n+"-2", other, hash, (state == "" || state == "rolled") && !slow, wait))
			}
		}
		return []byte(kubeList(list...)), nil
	case strings.HasSuffix(joined, " endpointslices.discovery.k8s.io -n harbor -l kubernetes.io/service-name=harbor"):
		if w.frontErr != nil {
			return nil, w.frontErr
		}
		endpoints := []obj{}
		for _, node := range slices.Sorted(maps.Keys(w.front)) {
			e := obj{"addresses": []string{"10.43.0.1"}, "nodeName": node, "conditions": obj{"ready": w.front[node] == "ready"}}
			if w.front[node] == "unset" {
				e["conditions"] = obj{}
			}
			endpoints = append(endpoints, e)
		}
		return []byte(kubeList(obj{"metadata": obj{"namespace": "harbor", "name": "harbor-x7k2p"}, "endpoints": endpoints})), nil
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
		if w.meddle != nil {
			maps.Copy(w.doc, w.meddle)
			w.version, w.meddle = w.version+1, nil
		}
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
	plan := func(p obj) func(*rebootWorld) { return func(w *rebootWorld) { w.plans[1] = p } }
	release := func(state string) func(*rebootWorld) {
		return func(w *rebootWorld) { w.releases["flux-system/bp-cilium"] = state }
	}
	for name, tc := range map[string]struct {
		change  func(w *rebootWorld)
		j1      string            // the status of journey J1; "" is PASS
		fails   string            // a part of the reason; "" when the check passes
		regions int               // the regions that got a sentinel Pod
		waited  string            // how long the first region waited for kured to run with the span written for the run; "" is 0s
		opened  int               // the regions in which the check wrote a span into the variables document; each is restored
		set     string            // the span written in the first region, "<start>-<end>"; "" is not compared
		writes  int               // the changes of the variables document; 0 is 2 per region opened (the span and its restore)
		left    map[string]string // the data of the variables document after the run; nil is the data before
		chosen  string            // the node that got the sentinel Pod; "" is w-1
		by      string            // the rule that chose it, in a check that passes; "" is rebootByName
		judged  int               // the controllers of the moved Pods, in a check that passes; 0 is 2

		at      int            // the second after the placing at which the last step ends; 0 is not compared
		seconds map[string]int // the seconds of each moved controller to be back, in a check that passes; nil: each at most 180
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
		}, regions: 2, chosen: "w-2", judged: 1},
		"no worker runs a workload": {change: func(w *rebootWorld) { w.workloads = map[string][]string{"cp-1": {"kube-system/etcd-cp-1"}} },
			fails: "none of the 3 worker nodes runs a workload Pod"},
		"the region has one worker":       {change: func(w *rebootWorld) { w.nodes = w.nodes[:2] }, fails: "the region has 1 worker nodes: the check needs 2"},
		"the region has no control-plane": {change: func(w *rebootWorld) { w.nodes = []string{"w-1", "w-2", "w-3"} }, fails: "the region has 3 nodes and none with the label"},
		"a node is cordoned before":       {change: func(w *rebootWorld) { w.sick["w-2"] = "cordoned" }, fails: "before the check started: w-2"},
		"a node is not Ready before":      {change: func(w *rebootWorld) { w.sick["cp-1"] = "not ready" }, fails: "before the check started: cp-1"},
		"the span closes before the limit: the check opens a window for its run": {change: func(w *rebootWorld) { w.later(15*time.Hour + 30*time.Minute) },
			regions: 2, waited: "2m0s", opened: 2, set: "21:30-22:21"},
		"the span is closed: the check opens a window for its run and restores the setting": {change: func(w *rebootWorld) { w.later(16 * time.Hour) },
			regions: 2, waited: "2m0s", opened: 2, set: "22:00-22:51"},
		"the window is opened in the time zone of kured": {change: func(w *rebootWorld) {
			// 06:00 UTC is 10:00 in Muscat: the night span 01:00 to 05:00 there is closed. The Plans end at 00:30 there.
			w.args[5], w.args[6], w.args[7] = "--start-time=01:00", "--end-time=05:00", "--time-zone=Asia/Muscat"
			w.plans = []obj{rebootPlan("k3s-server", "19:30", "20:30", "UTC", rebootAllDays...), rebootPlan("k3s-agent", "23:30", "00:30", "Asia/Muscat", rebootAllDays...)}
		}, regions: 2, waited: "2m0s", opened: 2, set: "10:00-10:51"},
		"the document has no span: restoring it removes the keys again": {change: func(w *rebootWorld) {
			w.later(16 * time.Hour)
			w.doc = map[string]string{"SOVEREIGN_TIME_ZONE": "UTC"}
		}, regions: 2, waited: "2m0s", opened: 2},
		"the span written for the run does not reach kured: no node is touched and the setting is restored": {change: func(w *rebootWorld) {
			w.later(16 * time.Hour)
			w.applyAfter = -1
		}, fails: "DaemonSet kube-system/kured does not run with the span 22:00-22:51 UTC, every Pod updated and ready, within 15m0s of the change of flux-system/sovereign-variables (its arguments give 00:30-22:00 UTC): no node was touched", opened: 1},
		"the variables document cannot be read: no node is touched": {change: func(w *rebootWorld) { w.later(16 * time.Hour); w.noDoc = true },
			fails: `configmaps "sovereign-variables" not found`},
		"the worker does not restart in the window written for the run: the setting is restored all the same": {change: func(w *rebootWorld) {
			w.later(16 * time.Hour)
			w.restarts["w-1"] = [3]int{-1, -1, -1}
		}, fails: "35m0s after the pending reboot was placed: 0 of 1 nodes restarted", regions: 1, waited: "2m0s", opened: 1},
		"the span the run needs overlaps the window of the upgrade Plans: nothing is written and no node is touched": {change: func(w *rebootWorld) { w.clock = w.clock.Add(16 * time.Hour) },
			fails: "the span 22:00-22:51 UTC that the run needs from now (15m0s for kured to take it, the time limit 35m0s and one minute) breaks the rule of the upgrade Plans: " +
				"Plan system-upgrade/k3s-agent (window 22:00-00:00 UTC) has a window that overlaps the reboot span 22:00-22:51 UTC; " +
				"Plan system-upgrade/k3s-server (window 22:00-00:00 UTC) has a window that overlaps the reboot span 22:00-22:51 UTC: no span was written and no node was touched"},
		"the span the run needs starts less than 30 minutes after a Plan window: nothing is written and no node is touched": {change: func(w *rebootWorld) {
			w.clock = w.clock.Add(18*time.Hour + 10*time.Minute)
		}, fails: "Plan system-upgrade/k3s-agent (window 22:00-00:00 UTC) has a window that ends 10 minutes before the reboot span 00:10-01:01 UTC starts, less than 30"},
		"kured's DaemonSet has not observed its new generation yet: the check waits for it": {change: func(w *rebootWorld) { w.later(16 * time.Hour); w.observe = 60 },
			regions: 2, waited: "3m0s", opened: 2, set: "22:00-22:51"},
		"not every Pod of kured runs the new span yet: the check waits for the roll": {change: func(w *rebootWorld) { w.later(16 * time.Hour); w.roll = 90 },
			regions: 2, waited: "3m30s", opened: 2, set: "22:00-22:51"},
		"the document is changed during the run: it is not overwritten and the restore fails": {change: func(w *rebootWorld) {
			w.later(16 * time.Hour)
			w.meddle = map[string]string{"KURED_START_TIME": "02:00", "KURED_END_TIME": "04:00"}
		}, fails: "the variables document flux-system/sovereign-variables holds KURED_START_TIME=02:00, KURED_END_TIME=04:00, not the span the run wrote (KURED_START_TIME=22:00, KURED_END_TIME=22:51): " +
			"it was changed during the run, so it is left as it is and the values before the run (KURED_START_TIME=00:30, KURED_END_TIME=22:00) are not written back",
			regions: 1, waited: "2m0s", opened: 1, left: map[string]string{"KURED_START_TIME": "02:00", "KURED_END_TIME": "04:00", "SOVEREIGN_TIME_ZONE": "UTC"}},
		"the document is applied by Flux: nothing is written and no node is touched": {change: func(w *rebootWorld) { w.later(16 * time.Hour); w.owner = "sovereign-root" },
			fails: "ConfigMap flux-system/sovereign-variables is applied by the Flux Kustomization sovereign-root (label kustomize.toolkit.fluxcd.io/name), which would undo a span written for the run: no span was written and no node was touched"},
		"a previous run left its window open: its values before are written back and no node is touched": {change: func(w *rebootWorld) {
			w.doc["KURED_START_TIME"], w.doc["KURED_END_TIME"] = "10:00", "10:51"
			w.notes[rebootTestMark] = rebootTestNote("10:00", "10:51", `{"KURED_START_TIME":"00:30","KURED_END_TIME":null}`)
		}, fails: "a previous run left its window open: run 20261119T100000Z-ab12 wrote KURED_START_TIME=10:00, KURED_END_TIME=10:51 into flux-system/sovereign-variables at 2026-11-19T10:00:00Z and did not restore it; " +
			"the values before it (KURED_START_TIME=00:30, KURED_END_TIME absent) are written back and the note is removed: no node was touched",
			writes: 1, left: map[string]string{"KURED_START_TIME": "00:30", "SOVEREIGN_TIME_ZONE": "UTC"}},
		"a previous run left its window open and the span was changed since: it is left as it is": {change: func(w *rebootWorld) {
			w.doc["KURED_START_TIME"], w.doc["KURED_END_TIME"] = "02:00", "04:00"
			w.notes[rebootTestMark] = rebootTestNote("10:00", "10:51", `{"KURED_START_TIME":"00:30","KURED_END_TIME":"22:00"}`)
		}, fails: "a previous run left its window open: run 20261119T100000Z-ab12 wrote KURED_START_TIME=10:00, KURED_END_TIME=10:51 into flux-system/sovereign-variables at 2026-11-19T10:00:00Z and did not restore it; " +
			"the document holds KURED_START_TIME=02:00, KURED_END_TIME=04:00 since, which is left as it is (the values before that run were KURED_START_TIME=00:30, KURED_END_TIME=22:00), and the note is removed: no node was touched",
			writes: 1, left: map[string]string{"KURED_START_TIME": "02:00", "KURED_END_TIME": "04:00", "SOVEREIGN_TIME_ZONE": "UTC"}},
		"the sentinel Pod does not finish": {change: func(w *rebootWorld) { w.waitErr = errors.New("timed out waiting for the condition") }, fails: "timed out waiting", regions: 1},
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
		"the worker that runs the registry front is chosen": {change: func(w *rebootWorld) {
			w.front, w.restarts["w-1"], w.restarts["w-2"] = map[string]string{"cp-1": "ready", "w-1": "not ready", "w-2": "ready"}, [3]int{-1, -1, -1}, [3]int{600, 640, 700}
			w.workloads["w-2"] = append(w.workloads["w-2"], "harbor/harbor-nginx-1")
		}, regions: 2, chosen: "w-2", by: rebootByFront},
		"an endpoint of the registry front without the condition ready counts as Ready": {change: func(w *rebootWorld) {
			w.front, w.restarts["w-1"], w.restarts["w-3"] = map[string]string{"w-3": "unset"}, [3]int{-1, -1, -1}, [3]int{600, 640, 700}
			w.workloads["w-3"] = []string{"harbor/harbor-nginx-1"}
		}, regions: 2, chosen: "w-3", by: rebootByFront, judged: 1},
		"without a registry front the first worker by name is chosen": {change: func(w *rebootWorld) {
			w.front = map[string]string{"cp-1": "ready", "w-2": "not ready"}
		}, regions: 2},
		"the registry front cannot be read": {change: func(w *rebootWorld) { w.frontErr = errors.New("endpointslices.discovery.k8s.io is forbidden") },
			fails: "endpointslices.discovery.k8s.io is forbidden"},
		"the registry front waits on its image after the drain of its node": {change: func(w *rebootWorld) {
			w.front, w.restarts["w-1"], w.restarts["w-2"] = map[string]string{"w-2": "ready"}, [3]int{-1, -1, -1}, [3]int{600, 640, 700}
			w.workloads["w-2"], w.back["harbor/harbor-nginx-1"] = append(w.workloads["w-2"], "harbor/harbor-nginx-1"), "pull"
		}, fails: "1 of 2 controllers whose Pods the drain moved are not back within 180 seconds of their Pods leaving w-2: Deployment harbor/harbor-nginx has 0 of 1 Ready Pods after 185 seconds (registry.example/harbor-nginx:1); " +
			"1 containers of the region wait on an image: harbor/harbor-nginx-1/main ImagePullBackOff registry.example/harbor-nginx:1", regions: 1, chosen: "w-2", at: 785},
		"a moved workload that is not Ready again fails": {change: func(w *rebootWorld) { w.back["shop/db-0"] = "never" },
			fails: "1 of 2 controllers whose Pods the drain moved are not back within 180 seconds of their Pods leaving w-1: StatefulSet shop/db has 0 of 1 Ready Pods after 185 seconds (registry.example/db:1)", regions: 1, at: 785},
		"a moved workload back after more than 180 seconds fails": {change: func(w *rebootWorld) {
			w.late["shop/web-1"], w.releases["catalyst-system/bp-catalyst-platform"] = 185, "ready"
		}, fails: "1 of 2 controllers whose Pods the drain moved are not back within 180 seconds of their Pods leaving w-1: Deployment shop/web has its 2 Ready Pods back after 185 seconds (registry.example/web:1)", regions: 1, at: 785},
		"a moved workload back within 180 seconds passes": {change: func(w *rebootWorld) {
			w.late["shop/web-1"], w.releases["catalyst-system/bp-catalyst-platform"] = 175, "ready"
		}, regions: 2, seconds: map[string]int{"Deployment shop/web": 175, "StatefulSet shop/db": 0}},
		"the 180 seconds count from the reading in which its Pod left the worker": {change: func(w *rebootWorld) {
			w.restarts["w-1"], w.late["shop/web-1"] = [3]int{1200, 1240, 1300}, 60
		}, regions: 2, seconds: map[string]int{"Deployment shop/web": 60, "StatefulSet shop/db": 0}},
		"a moved Pod being deleted on the worker has left it": {change: func(w *rebootWorld) {
			w.late["shop/web-1"], w.deleting["shop/web-1"], w.releases["catalyst-system/bp-catalyst-platform"] = 185, 100, "ready"
		}, fails: "Deployment shop/web has its 2 Ready Pods back after 185 seconds (registry.example/web:1)", regions: 1, at: 785},
		"a moved workload that left less than 180 seconds before the limit is named at the limit": {change: func(w *rebootWorld) {
			w.back["shop/db-0"], w.restarts["w-1"] = "never", [3]int{1950, 1990, 2050}
		}, fails: "35m0s after the pending reboot was placed: 1 of 1 nodes restarted, 0 unschedulable, 0 not Ready; " +
			"1 of 2 controllers whose Pods the drain moved have fewer Ready Pods than before: StatefulSet shop/db has 0 of 1 Ready Pods (registry.example/db:1)", regions: 1, at: 2100},
		"a moved Pod that comes back under a new ReplicaSet of its Deployment counts": {change: func(w *rebootWorld) { w.back["shop/web-1"] = "rolled" }, regions: 2},
		"a moved Pod of a Job is not judged": {change: func(w *rebootWorld) {
			w.workloads["w-1"], w.back["scan/job-1"] = append(w.workloads["w-1"], "scan/job-1"), "never"
		}, regions: 2},
		"a container waiting on an image fails while every HelmRelease is Ready": {change: func(w *rebootWorld) {
			w.workloads["w-3"], w.waits["mon/agent-1"], w.releases["catalyst-system/bp-catalyst-platform"] = []string{"mon/agent-1"}, "after", "ready"
		}, fails: "0 not Ready; 1 containers of the region wait on an image: mon/agent-1/prepare ImagePullBackOff registry.example/agent:1", regions: 1},
		"a container that waits on an image before the check: no node is touched": {change: func(w *rebootWorld) {
			w.workloads["w-3"], w.waits["mon/agent-1"] = []string{"mon/agent-1"}, "before"
		}, fails: "1 containers of the region wait on an image before the check started, so the drain could not be judged: no node was touched: mon/agent-1/prepare ErrImagePull registry.example/agent:1"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newRebootWorld()
			if tc.change != nil {
				tc.change(w)
			}
			start, doc := w.clock, maps.Clone(w.doc)
			res, steps := rebootRun(t, w, nonEmpty(tc.j1, Pass))
			want, writes := doc, tc.writes
			if tc.left != nil {
				want = tc.left
			}
			if writes == 0 {
				writes = 2 * tc.opened
			}
			if !reflect.DeepEqual(w.doc, want) || len(w.patches) != writes || len(w.notes) != 0 {
				t.Errorf("the variables document is %v with the notes %v after the run, want %v and none, after %d changes, want %d: %v", w.doc, w.notes, want, len(w.patches), writes, w.patches)
			}
			// Each restore that passes writes back what the step that opened the window
			// read, and the last step that judges the restart is the watch, not the restore.
			var opening, watch *Step
			for _, s := range steps {
				switch {
				case strings.HasPrefix(s.Name, "the reboot span is open"):
					opening = s
				case strings.HasPrefix(s.Name, "the worker restarts drained"):
					watch = s
				case strings.HasPrefix(s.Name, "the reboot span of the variables document is restored"):
					if s.Status == Pass && (opening == nil || !reflect.DeepEqual(s.Evidence["values_restored"], opening.Evidence["values_before"])) {
						t.Errorf("step %q restores %v, not the values the region's document had before", s.Name, s.Evidence["values_restored"])
					}
				}
			}
			if watch == nil {
				watch = steps[len(steps)-1]
			}
			// The span is written with the note of the run: its id, when and the values before and written.
			if open := slices.IndexFunc(steps, func(s *Step) bool { return strings.HasPrefix(s.Name, "the reboot span is open") }); tc.opened > 0 && len(w.patches) > 0 {
				var patch struct {
					Metadata struct{ Annotations map[string]string }
				}
				var note map[string]any
				_, first, _ := strings.Cut(w.patches[0], " ")
				_ = json.Unmarshal([]byte(first), &patch)
				_ = json.Unmarshal([]byte(patch.Metadata.Annotations[rebootTestMark]), &note)
				ev, _ := steps[open].Evidence["note"].(map[string]any)
				if run, _ := note["run"].(string); run == "" || ev["run"] != run || note["written"] != start.UTC().Format(time.RFC3339) || !reflect.DeepEqual(note["values_before"], steps[open].Evidence["values_before"]) ||
					!reflect.DeepEqual(note["values_set"], steps[open].Evidence["values_set"]) {
					t.Errorf("the first change of the document notes %v, want the run, the time %s and the values of the step %v", note, start.UTC().Format(time.RFC3339), steps[open].Evidence)
				}
			}
			if open := slices.IndexFunc(steps, func(s *Step) bool { return strings.HasPrefix(s.Name, "the reboot span is open") }); tc.set != "" {
				set, _ := steps[open].Evidence["values_set"].(map[string]any)
				if got := fmt.Sprint(set["KURED_START_TIME"], "-", set["KURED_END_TIME"]); got != tc.set || steps[open].Evidence["window_opened"] != true {
					t.Errorf("the first region wrote the span %s (opened %v), want %s", got, steps[open].Evidence["window_opened"], tc.set)
				}
			}
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
				applied := ""
				if span >= 0 {
					applied, _ = steps[span].Evidence["applied_after"].(string)
				}
				if span < 0 || at.Sub(start).String() != nonEmpty(tc.waited, "0s") || (applied != "") != (tc.opened > 0) || (applied != "" && applied != at.Sub(start).String()) {
					t.Errorf("the first region waited %s for kured to run with the span written for the run (evidence %q), want %q", at.Sub(start), applied, tc.waited)
				}
			}
			workers := strconv.Itoa(len(w.nodes) - 1)
			if last := watch; tc.fails == "" && (last.Evidence["most_unschedulable"] != 1 || last.Evidence["failed_readings"] != 0 ||
				len(last.Evidence["boot_ids_after"].(map[string]string)) != 1 || last.Evidence["kured_ready"] != workers+"/"+workers ||
				last.Evidence["workloads_moved_off"] == nil || len(last.Evidence["control_plane_boot_ids"].(map[string]string)) != 1 ||
				len(last.Evidence["controllers_not_ready_again"].([]string)) != 0 || len(last.Evidence["image_waits"].([]string)) != 0) {
				t.Errorf("the evidence of the last step is %v", last.Evidence)
			}
			if last := watch; tc.at != 0 && last.Evidence["seconds"] != tc.at {
				t.Errorf("the last step ends at second %v after the placing, want %d", last.Evidence["seconds"], tc.at)
			}
			choose, judged := slices.IndexFunc(steps, func(s *Step) bool { return strings.HasPrefix(s.Name, "kured cannot run on control-plane nodes") }), map[bool]int{true: 2, false: tc.judged}[tc.judged == 0]
			if seconds, _ := watch.Evidence["controllers_seconds_back"].(map[string]int); tc.fails == "" &&
				(len(seconds) != judged || slices.ContainsFunc(slices.Collect(maps.Values(seconds)), func(s int) bool { return s > 180 }) ||
					(tc.seconds != nil && !reflect.DeepEqual(seconds, tc.seconds))) {
				t.Errorf("the seconds of the moved controllers to be back are %v, want %d controllers, each at most 180 (%v)", seconds, judged, tc.seconds)
			}
			if tc.fails == "" && (choose < 0 || steps[choose].Evidence["chosen_by"] != nonEmpty(tc.by, rebootByName) ||
				len(steps[choose].Evidence["controllers_moved"].(map[string]int)) != judged) {
				t.Errorf("the node choice says %v, want %q and %d controllers of the moved Pods", steps[choose].Evidence, nonEmpty(tc.by, rebootByName), judged)
			}
		})
	}
}
