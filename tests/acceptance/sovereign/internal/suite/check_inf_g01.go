package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check INF-G01, coordinated reboot. The check makes kured restart nodes: it
// is destructive and runs in the cloud only. It starts, in every region, by
// looking for the note (rebootMarker, annotation rebootMark) that an earlier
// run writes with a span into the region's variables document and removes
// when it restores the values before: a note found there is a run whose
// runner was stopped between the change and the restore. The step writes
// the values before that run back, by the rule of the restore in step 4,
// removes the note and fails, naming that run; the check then touches no
// node. It fails, and touches no node, unless journey J1 of this run passed.
// Then it takes the regions one after the other; a region is started only
// when every region before it passed. In a region it:
//  1. reads the settings of kured from the arguments of DaemonSet
//     kube-system/kured. A setting that is not an argument fails the step:
//     the check assumes no default. The step also fails, before any node is
//     touched, while the DaemonSet carries the install hold of the chart
//     bp-kured: the annotation kured locks in (--lock-annotation, else
//     rebootLock) holds a lock of rebootHold. kured restarts no node until
//     the hand-over chain has released it;
//  2. reads every upgrade Plan (rebootPlans) of every namespace. Each must
//     have spec.window with startTime and endTime (HH:MM) and timeZone, and
//     the window must end at least rebootGap before the reboot span starts
//     and must not overlap it. A cluster that does not serve the kind, or
//     has no Plan, passes. Both spans are compared as times of the day in
//     UTC, each converted with the offset of its time zone at the moment of
//     the step. The days of a window are not read, as the days of the span
//     are not: both are judged as if they were open every day, which is the
//     strictest reading;
//  3. chooses one worker node, a node without the control-plane label. It
//     reads the nodes, every Pod of the region once, and the EndpointSlices
//     of Service harbor/harbor, the registry's front: after the cutover every
//     node pulls every image through it. The worker is the first by name
//     that runs a Ready endpoint of the front; when no worker runs one, the
//     first by name that runs a workload Pod (a Running Pod that is not of a
//     DaemonSet and not a static Pod). The evidence says which rule chose.
//     kured never restarts a control-plane node: the chart bp-kured keeps its
//     DaemonSet off them (platform/kured/chart/values.yaml, affinity), and a
//     DaemonSet that could run there fails the step. So do a node that is
//     unschedulable or not Ready already, no control-plane node (that none
//     restarts could not be observed), fewer than two worker nodes (the
//     workloads need another node to move to), no worker node with a
//     workload Pod (a restart would move nothing off), and a container of
//     the region that waits on an image already (reason ErrImagePull or
//     ImagePullBackOff: the drain could not be judged). For each workload
//     Pod on the worker the step records its controller, the owner reference
//     marked controller (a ReplicaSet of a Deployment counts as the
//     Deployment), and how many Ready Pods that controller has in the region.
//     A Pod of a Job, which ends by design, and a Pod without a controller,
//     which nothing brings back, are not judged;
//  4. opens a window for the run when the reboot span is not open for the
//     whole time limit, through the setting of the sovereign-admin: it reads
//     the keys rebootStartKey and rebootEndKey of the region's variables
//     document (ConfigMap flux-system/sovereign-variables, contract C5; the
//     Kustomization bootstrap-kit substitutes it into kit slot 04b, whose
//     values are kured's arguments). The span it needs starts at the minute
//     of now and ends at that minute plus rebootApply, the time limit and one
//     more minute (the minute of now has begun already), both in kured's time
//     zone. The step judges that span with the rule of step 2 against every
//     upgrade Plan of step 2; when one breaks it, the step writes nothing and
//     fails, naming the Plan and its window: the run needs that span now. It
//     writes nothing either while Flux applies the document (label
//     rebootFluxOwner), which would undo the change. Else it writes the span
//     and the note of the run (rebootMarker: the run, when, the values before
//     and the values written) with one merge patch, made only while the
//     document is still at the version it read, and waits at most rebootApply
//     until the arguments of the DaemonSet of kured give that span and every
//     Pod of it is updated and ready (its status has observed its current
//     generation). The step records the document, the values before and the
//     values written. After the last step of the region, also when a step
//     failed, a step of its own writes the values before back (a key the
//     document did not have is removed) and removes the note, only while the
//     document holds exactly the span the run wrote: a document that holds
//     anything else was changed during the run, is left as it is (the note
//     of the run is removed) and fails the step, which names both values;
//  5. reads the HelmReleases of every namespace once and keeps those that are
//     Ready. None fails the step;
//  6. creates a namespace of its own and one Pod on the chosen worker that
//     creates the file kured looks for, and deletes the namespace;
//  7. reads the nodes and every Pod of the region every rebootEvery until
//     the chosen worker has a new boot identifier, no node is
//     unschedulable, every node is Ready, none
//     of the workload Pods that ran on the worker in step 3 is still on it
//     (they moved off during the drain), the DaemonSet of kured has every
//     Pod ready (its Pod on the worker among them: the node is back), every
//     HelmRelease of step 5 is Ready again, every controller of step 3 has
//     as many Ready Pods as it had then (the moved workloads run again), and
//     no container, init or main, of a Pod of the region waits on an image;
//     a container that waits fails the step even when every HelmRelease is
//     Ready. At the time limit each such controller and container is named
//     with its image. The time limit is one round of kured (period + lock
//     release delay) plus rebootSettle. The step fails at once when a
//     control-plane node shows a new boot identifier, when two nodes are
//     unschedulable in one reading,
//     when a node is unschedulable for longer than drain timeout + lock
//     release delay, when the worker shows a new boot identifier and was
//     never read unschedulable, when a workload Pod of step 3 is still on
//     the worker after its restart, or when a controller of step 3 does not
//     have as many Ready Pods as it had then within rebootBack of the first
//     reading in which none of its Pods of step 3 is on the worker (gone,
//     being deleted or ended); the reason names the controller, its images
//     and the seconds, and the evidence holds for each controller the
//     seconds from that reading to the first in which it has them back. A
//     reading that fails is no observation.
//
// The check never restarts a node itself. It changes kured only through the
// variables document, for the length of its run (step 4).
const (
	rebootID        = "INF-G01"
	rebootNamespace = "kube-system"                 // of the DaemonSet of kured
	rebootDaemonSet = "kured"                       // also the name of its container
	rebootVolume    = "sentinel"                    // the volume of the DaemonSet with the folder of the sentinel file
	rebootLock      = "weave.works/kured-node-lock" // where kured locks when it has no --lock-annotation
	rebootHold      = "catalyst-install-hold"       // who holds the lock while the install hold of bp-kured is in place
	rebootPlans     = "plans.upgrade.cattle.io"
	rebootNodes     = 1 // worker nodes restarted per region
	rebootMirror    = "kubernetes.io/config.mirror"
	rebootEvery     = 5 * time.Second
	rebootSettle    = 15 * time.Minute
	rebootDocument  = "sovereign-variables" // ConfigMap flux-system/sovereign-variables, the variables document of the region (contract C5)
	rebootDocNS     = "flux-system"
	rebootStartKey  = "KURED_START_TIME" // the keys of the document that kit slot 04b gives kured as its reboot span
	rebootEndKey    = "KURED_END_TIME"
	rebootGap       = 30                                   // minutes from the end of a Plan window to the start of the reboot span, at least
	rebootMark      = "catalyst.openova.io/inf-g01-window" // the annotation of the variables document that holds the note of the span the check wrote
	rebootFluxOwner = "kustomize.toolkit.fluxcd.io/name"   // the label by which Flux marks an object that a Kustomization applies

	// rebootApply is how long a span written into the document may take to
	// reach kured: Kustomization bootstrap-kit substitutes the document every
	// 5 minutes, then HelmRelease bp-kured upgrades and the DaemonSet rolls.
	rebootApply = 15 * time.Minute

	rebootFront   = "harbor"            // Service harbor/harbor, the registry's front, in the namespace of the same name
	rebootHash    = "pod-template-hash" // the label by which a Pod of a Deployment's ReplicaSet names its hash
	rebootByFront = "the worker that runs a Ready endpoint of the registry front harbor/harbor"
	rebootByName  = "the first worker by name that runs a workload Pod: no worker runs a Ready endpoint of the registry front harbor/harbor"

	// rebootBack is how long a controller of a moved Pod may take to have its
	// Ready Pods of step 3 back, from the first reading in which none of its
	// Pods of step 3 is on the worker. Measured on a healthy drain of a
	// worker of a two-region install on 20 November: its 28 moved Pods were
	// Ready 1 to 76 seconds after their creation (median 31), all within 84
	// seconds of the first eviction; in the other region the Pods whose new
	// node held their image, within 36 seconds. 180 seconds is about twice
	// the slowest. A Pod whose image copy stalls for minutes fails here; the
	// time limit of the node's restart alone would let it come back late.
	rebootBack = 180 * time.Second
)

// rebootPod creates the sentinel file of its node and ends. Its values are
// its name, namespace, node and image, the file and the folder of the node.
const rebootPod = `{"apiVersion": "v1", "kind": "Pod",
  "metadata": {"name": %q, "namespace": %q, "labels": {"app.kubernetes.io/name": "inf-reboot"}},
  "spec": {"nodeName": %q, "restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
    "automountServiceAccountToken": false, "enableServiceLinks": false, "tolerations": [{"operator": "Exists"}],
    "containers": [{"name": "sentinel", "image": %q, "command": ["touch", %q],
      "securityContext": {"runAsUser": 0, "runAsNonRoot": false, "allowPrivilegeEscalation": false,
        "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}},
      "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"memory": "128Mi"}},
      "volumeMounts": [{"name": "sentinel", "mountPath": "/host"}]}],
    "volumes": [{"name": "sentinel", "hostPath": {"path": %q, "type": "Directory"}}]}}`

type rebootCheck struct {
	kubectl kubectlFunc
	now     func() time.Time
	wait    func(ctx context.Context, d time.Duration) error // waits d, or until ctx ends
}

func init() { RegisterCheck(rebootCheck{kubectl: kube.Region.Run, now: time.Now, wait: rebootWait}) }

func (rebootCheck) ID() string { return rebootID }

func rebootWait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// rebootSettings are the arguments of kured that the check reads.
type rebootSettings struct {
	period, release, drain time.Duration
	start, end             int // minutes of the day in zone
	zone                   *time.Location
	span                   string // the reboot span as the arguments write it
	workersOnly            bool   // the DaemonSet cannot run on control-plane nodes
	held                   string // the annotation kured locks in, when it holds the install hold
	dir, file              string // the folder of the node that holds the sentinel file, and its name
	rolled                 bool   // every Pod of the DaemonSet runs its current template and is ready
}

func (k rebootSettings) limit() time.Duration { return rebootNodes*(k.period+k.release) + rebootSettle }

// window returns how long the reboot span stays open from now, 0 when it is
// closed, and how long it is until the span opens the next time.
func (k rebootSettings) window(now time.Time) (open, next time.Duration) {
	local := now.In(k.zone)
	minute := local.Hour()*60 + local.Minute()
	if since, length := rebootMod(minute-k.start), rebootMod(k.end-k.start); since < length {
		open = time.Duration(length-since) * time.Minute
	}
	return open, time.Duration(rebootMod(k.start-minute-1)+1)*time.Minute - time.Duration(local.Second())*time.Second
}

// rebootMod is m modulo the minutes of a day, never negative.
func rebootMod(m int) int { return (m%1440 + 1440) % 1440 }

// rebootUTC turns a minute of the day in zone into a minute of the day in
// UTC, with the offset that zone has at now.
func rebootUTC(minute int, zone *time.Location, now time.Time) int {
	_, offset := now.In(zone).Zone()
	return rebootMod(minute - offset/60)
}

// rebootWindow judges the window of one upgrade Plan against the reboot span
// of k at now. It returns the window as the Plan writes it and why it fails,
// "" when it passes.
func rebootWindow(plan kube.Object, k rebootSettings, now time.Time) (window, problem string) {
	w, _ := plan.Get("spec", "window").(map[string]any)
	if w == nil {
		return "", "has no spec.window"
	}
	o := kube.Object(w)
	window = o.Str("startTime") + "-" + o.Str("endTime") + " " + o.Str("timeZone")
	start, errStart := time.Parse("15:04", o.Str("startTime"))
	end, errEnd := time.Parse("15:04", o.Str("endTime"))
	zone, errZone := time.LoadLocation(o.Str("timeZone"))
	switch {
	case errStart != nil || errEnd != nil:
		return window, "has no startTime and endTime of the form HH:MM in spec.window"
	case errZone != nil || o.Str("timeZone") == "":
		return window, "has no time zone that the check knows in spec.window.timeZone"
	}
	ws := rebootUTC(start.Hour()*60+start.Minute(), zone, now)
	length := rebootMod(end.Hour()*60 + end.Minute() - start.Hour()*60 - start.Minute())
	ks, span := rebootUTC(k.start, k.zone, now), rebootMod(k.end-k.start)
	switch {
	case length == 0:
		return window, "has a window that starts when it ends"
	case rebootMod(ks-ws) < length || rebootMod(ws-ks) < span:
		return window, "has a window that overlaps the reboot span " + k.span
	case rebootMod(ks-ws-length) < rebootGap:
		return window, fmt.Sprintf("has a window that ends %d minutes before the reboot span %s starts, less than %d", rebootMod(ks-ws-length), k.span, rebootGap)
	}
	return window, ""
}

// list runs `kubectl get -o json <args>` and returns the objects of the list.
func (c rebootCheck) list(ctx context.Context, region kube.Region, args ...string) ([]kube.Object, error) {
	out, err := c.kubectl(region, ctx, nil, append([]string{"get", "-o", "json", "--request-timeout=15s"}, args...)...)
	if err != nil {
		return nil, err
	}
	var l kube.List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] decode kubectl get %s: %w", region.ID, args[0], err)
	}
	return l.Items, nil
}

// settings reads the arguments of kured. It names every setting it cannot read.
func (c rebootCheck) settings(ctx context.Context, region kube.Region) (rebootSettings, error) {
	k := rebootSettings{zone: time.UTC}
	sets, err := c.list(ctx, region, "daemonsets", "-n", rebootNamespace)
	if err != nil {
		return k, err
	}
	arg, found := map[string]string{}, false
	for _, ds := range sets {
		if ds.Name() != rebootDaemonSet {
			continue
		}
		found = true
		label, _, _ := strings.Cut(secControlPlane, "=")
		for _, term := range ds.Objects("spec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms") {
			for _, e := range term.Objects("matchExpressions") {
				k.workersOnly = k.workersOnly || (e.Str("key") == label && e.Str("operator") == "DoesNotExist")
			}
		}
		for _, container := range ds.Objects("spec", "template", "spec", "containers") {
			list, _ := container.Get("args").([]any)
			for _, a := range list {
				if s, _ := a.(string); container.Str("name") == rebootDaemonSet {
					name, value, _ := strings.Cut(s, "=")
					arg[name] = value
				}
			}
		}
		for _, v := range ds.Objects("spec", "template", "spec", "volumes") {
			if v.Str("name") == rebootVolume {
				k.dir = v.Str("hostPath", "path")
			}
		}
		key, lock := rebootLock, struct {
			NodeID string `json:"nodeID"`
		}{}
		if arg["--lock-annotation"] != "" {
			key = arg["--lock-annotation"]
		}
		desired := ds.Int("status", "desiredNumberScheduled")
		k.rolled = desired > 0 && ds.Int("status", "updatedNumberScheduled") == desired && ds.Int("status", "numberReady") == desired &&
			ds.Int("status", "observedGeneration") >= ds.Int("metadata", "generation")
		if _ = json.Unmarshal([]byte(ds.Str("metadata", "annotations", key)), &lock); lock.NodeID == rebootHold {
			k.held = key
		}
	}
	if !found {
		return k, fmt.Errorf("region %s has no DaemonSet %s/%s: kured is not installed", region.ID, rebootNamespace, rebootDaemonSet)
	}
	if k.held != "" {
		return k, fmt.Errorf("DaemonSet %s/%s carries the install hold (annotation %s, held by %s): kured restarts no node until the hand-over chain has released it in this region: no node was touched", rebootNamespace, rebootDaemonSet, k.held, rebootHold)
	}
	bad := []string{}
	for flag, into := range map[string]*time.Duration{"--period": &k.period, "--lock-release-delay": &k.release, "--drain-timeout": &k.drain} {
		if *into, err = time.ParseDuration(arg[flag]); err != nil {
			bad = append(bad, flag)
		}
	}
	for flag, into := range map[string]*int{"--start-time": &k.start, "--end-time": &k.end} {
		clock, err := time.Parse("15:04", arg[flag])
		if *into = clock.Hour()*60 + clock.Minute(); err != nil {
			bad = append(bad, flag)
		}
	}
	if zone, err := time.LoadLocation(arg["--time-zone"]); err == nil && arg["--time-zone"] != "" {
		k.zone = zone
	} else {
		bad = append(bad, "--time-zone")
	}
	if k.file = path.Base(arg["--reboot-sentinel"]); arg["--reboot-sentinel"] == "" || k.dir == "" {
		bad = append(bad, "--reboot-sentinel with the hostPath of the volume "+rebootVolume)
	}
	k.span = arg["--start-time"] + "-" + arg["--end-time"] + " " + arg["--time-zone"]
	if sort.Strings(bad); len(bad) > 0 {
		return k, fmt.Errorf("DaemonSet %s/%s gives no usable value for: %s", rebootNamespace, rebootDaemonSet, strings.Join(bad, ", "))
	}
	return k, nil
}

// rebootMarker is the note that the check writes into the annotation
// rebootMark of the variables document with the same change as its span:
// the run that wrote it, when (RFC 3339, UTC), the values before and the
// values written (a key the document did not have is null). The restore of
// the run removes it; a note that a run finds at its start is an earlier
// run's span that was never restored.
type rebootMarker struct {
	Run     string             `json:"run"`
	Written string             `json:"written"`
	Before  map[string]*string `json:"values_before"`
	Set     map[string]*string `json:"values_set"`
}

// rebootDoc is what the check reads of the region's variables document.
type rebootDoc struct {
	values  map[string]*string // the keys rebootStartKey and rebootEndKey; a key the document does not have is nil
	version string             // metadata.resourceVersion: a change made with it fails when the document changed in between
	note    *rebootMarker      // the note of rebootMark; nil: none
	owner   string             // the Flux Kustomization that applies the document (label rebootFluxOwner); "": none
}

// doc reads the region's variables document. A note that is not one of the
// check fails the read: it is left as it is.
func (c rebootCheck) doc(ctx context.Context, region kube.Region) (rebootDoc, error) {
	out, err := c.kubectl(region, ctx, nil, "get", "-o", "json", "--request-timeout=15s", "configmap", rebootDocument, "-n", rebootDocNS)
	if err != nil {
		return rebootDoc{}, err
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return rebootDoc{}, fmt.Errorf("[%s] decode ConfigMap %s/%s: %w", region.ID, rebootDocNS, rebootDocument, err)
	}
	d := rebootDoc{values: map[string]*string{rebootStartKey: nil, rebootEndKey: nil}, version: o.Str("metadata", "resourceVersion"),
		owner: o.Str("metadata", "labels", rebootFluxOwner)}
	data, _ := o.Get("data").(map[string]any)
	for key := range d.values {
		if value, ok := data[key].(string); ok {
			d.values[key] = &value
		}
	}
	if note := o.Str("metadata", "annotations", rebootMark); note != "" {
		d.note = &rebootMarker{}
		if err := json.Unmarshal([]byte(note), d.note); err != nil || d.note.Run == "" {
			return d, fmt.Errorf("the annotation %s of ConfigMap %s/%s is not a note of this check, so it is left as it is: %q", rebootMark, rebootDocNS, rebootDocument, note)
		}
	}
	return d, nil
}

// write changes the region's variables document with one merge patch that
// holds the version read, so that it fails when the document changed since:
// values (a nil value removes its key; a nil map changes no key) and the note
// of rebootMark (nil removes it).
func (c rebootCheck) write(ctx context.Context, region kube.Region, version string, values map[string]*string, note *rebootMarker) error {
	var mark any // null: the annotation is removed
	if note != nil {
		b, err := json.Marshal(note)
		if err != nil {
			return err
		}
		mark = string(b)
	}
	patch := map[string]any{"metadata": map[string]any{"resourceVersion": version, "annotations": map[string]any{rebootMark: mark}}}
	if values != nil {
		patch["data"] = values
	}
	b, err := json.Marshal(patch)
	if err == nil {
		_, err = c.kubectl(region, ctx, nil, "patch", "configmap", rebootDocument, "-n", rebootDocNS, "--type", "merge", "-p", string(b))
	}
	return err
}

// rebootSame reports whether have holds exactly the span of want.
func rebootSame(have, want map[string]*string) bool {
	for _, key := range []string{rebootStartKey, rebootEndKey} {
		if (have[key] == nil) != (want[key] == nil) || (have[key] != nil && *have[key] != *want[key]) {
			return false
		}
	}
	return true
}

// rebootShow is the span of values as a reason names it.
func rebootShow(values map[string]*string) string {
	out := []string{}
	for _, key := range []string{rebootStartKey, rebootEndKey} {
		if values[key] == nil {
			out = append(out, key+" absent")
		} else {
			out = append(out, key+"="+*values[key])
		}
	}
	return strings.Join(out, ", ")
}

// rebootValues is values as the record shows them: a key the document does not have is null.
func rebootValues(values map[string]*string) map[string]any {
	out := map[string]any{}
	for key, value := range values {
		out[key] = nil
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

// openWindow writes a reboot span into the region's variables document that
// is open from the minute of now for rebootApply, the time limit and one more
// minute (the minute of now has begun), in kured's time zone, together with
// the note of the run, and waits until kured runs with it. It writes nothing,
// and fails, while Flux applies the document or when the span breaks the
// rule of step 2 for one of the upgrade Plans plans. It returns the settings
// kured then has; note is set as soon as the change was made.
func (c rebootCheck) openWindow(ctx context.Context, region kube.Region, k rebootSettings, plans []kube.Object, run string, note **rebootMarker, ev Evidence) (rebootSettings, error) {
	doc, err := c.doc(ctx, region)
	if err != nil {
		return k, err
	}
	from := c.now().In(k.zone).Truncate(time.Minute)
	until := from.Add(rebootApply + k.limit() + time.Minute)
	start, end := from.Format("15:04"), until.Format("15:04")
	set := map[string]*string{rebootStartKey: &start, rebootEndKey: &end}
	ev["variables_document"], ev["values_before"], ev["values_set"] = rebootDocNS+"/"+rebootDocument, rebootValues(doc.values), rebootValues(set)
	if doc.owner != "" {
		return k, fmt.Errorf("ConfigMap %s/%s is applied by the Flux Kustomization %s (label %s), which would undo a span written for the run: no span was written and no node was touched", rebootDocNS, rebootDocument, doc.owner, rebootFluxOwner)
	}
	span := k
	span.start, span.end, span.span = from.Hour()*60+from.Minute(), until.Hour()*60+until.Minute(), start+"-"+end+" "+k.zone.String()
	if windows, problems := rebootPlanRule(plans, span, c.now()); len(problems) > 0 {
		bad := []string{}
		for _, name := range slices.Sorted(maps.Keys(problems)) {
			bad = append(bad, "Plan "+name+" (window "+windows[name]+") "+problems[name])
		}
		return k, fmt.Errorf("the span %s that the run needs from now (%s for kured to take it, the time limit %s and one minute) breaks the rule of the upgrade Plans: %s: no span was written and no node was touched", span.span, rebootApply, k.limit(), strings.Join(bad, "; "))
	}
	mark := &rebootMarker{Run: run, Written: c.now().UTC().Format(time.RFC3339), Before: doc.values, Set: set}
	if err := c.write(ctx, region, doc.version, set, mark); err != nil {
		return k, err
	}
	*note = mark
	ev["note"] = map[string]any{"annotation": rebootMark, "run": mark.Run, "written": mark.Written}
	want, changed, last := span.span, c.now(), k.span
	for {
		got, err := c.settings(ctx, region)
		if err == nil && got.span == want && got.rolled {
			ev["applied_after"] = c.now().Sub(changed).String()
			return got, nil
		}
		if err == nil {
			last = got.span
		}
		if c.now().Sub(changed) >= rebootApply {
			return k, fmt.Errorf("DaemonSet %s/%s does not run with the span %s, every Pod updated and ready, within %s of the change of %s/%s (its arguments give %s): no node was touched", rebootNamespace, rebootDaemonSet, want, rebootApply, rebootDocNS, rebootDocument, last)
		}
		if err := c.wait(ctx, rebootEvery); err != nil {
			return k, err
		}
	}
}

// restore writes the values before the run back and removes its note, only
// while the document holds exactly the span the run wrote. A document that
// holds anything else was changed during the run, which is a finding: its
// keys are left as they are, the note of the run is removed and the step
// fails, naming both values.
func (c rebootCheck) restore(ctx context.Context, region kube.Region, note *rebootMarker, ev Evidence) error {
	ev["variables_document"], ev["values_written"] = rebootDocNS+"/"+rebootDocument, rebootValues(note.Set)
	doc, err := c.doc(ctx, region)
	if err != nil {
		return err
	}
	if ev["values_found"] = rebootValues(doc.values); !rebootSame(doc.values, note.Set) {
		err := fmt.Errorf("the variables document %s/%s holds %s, not the span the run wrote (%s): it was changed during the run, so it is left as it is and the values before the run (%s) are not written back", rebootDocNS, rebootDocument, rebootShow(doc.values), rebootShow(note.Set), rebootShow(note.Before))
		if doc.note != nil && doc.note.Run == note.Run {
			return errors.Join(err, c.write(ctx, region, doc.version, nil, nil))
		}
		return err
	}
	ev["values_restored"] = rebootValues(note.Before)
	return c.write(ctx, region, doc.version, note.Before, nil)
}

// leftover looks for the note of an earlier run in the region's variables
// document and passes when there is none. A note is a span that its run did
// not restore (the runner was stopped between the change and the restore):
// the values before that run are written back when the document still holds
// exactly the span it wrote, else the keys are left as they are; the note is
// removed and the step fails, naming that run.
func (c rebootCheck) leftover(ctx context.Context, region kube.Region, ev Evidence) error {
	ev["variables_document"], ev["previous_run"] = rebootDocNS+"/"+rebootDocument, nil
	doc, err := c.doc(ctx, region)
	if err != nil || doc.note == nil {
		return err
	}
	m := doc.note
	ev["previous_run"], ev["previous_written"], ev["values_set"], ev["values_found"], ev["values_before"] = m.Run, m.Written, rebootValues(m.Set), rebootValues(doc.values), rebootValues(m.Before)
	left := fmt.Sprintf("a previous run left its window open: run %s wrote %s into %s/%s at %s and did not restore it", m.Run, rebootShow(m.Set), rebootDocNS, rebootDocument, m.Written)
	if !rebootSame(doc.values, m.Set) {
		if err := c.write(ctx, region, doc.version, nil, nil); err != nil {
			return fmt.Errorf("%s; the document holds %s since, which is left as it is, and the note cannot be removed: %w", left, rebootShow(doc.values), err)
		}
		return fmt.Errorf("%s; the document holds %s since, which is left as it is (the values before that run were %s), and the note is removed: no node was touched", left, rebootShow(doc.values), rebootShow(m.Before))
	}
	if err := c.write(ctx, region, doc.version, m.Before, nil); err != nil {
		return fmt.Errorf("%s; writing back the values before it (%s) failed: %w", left, rebootShow(m.Before), err)
	}
	ev["values_restored"] = rebootValues(m.Before)
	return fmt.Errorf("%s; the values before it (%s) are written back and the note is removed: no node was touched", left, rebootShow(m.Before))
}

// rebootNode is what the check reads of one Node.
type rebootNode struct {
	boot                          string
	cordoned, ready, controlPlane bool
}

func (c rebootCheck) nodes(ctx context.Context, region kube.Region) (map[string]rebootNode, error) {
	items, err := c.list(ctx, region, "nodes")
	label, value, _ := strings.Cut(secControlPlane, "=")
	out := map[string]rebootNode{}
	for _, o := range items {
		cordoned, _ := o.Get("spec", "unschedulable").(bool)
		ready, _, _, _ := o.Condition("Ready")
		out[o.Name()] = rebootNode{boot: o.Str("status", "nodeInfo", "bootID"), cordoned: cordoned, ready: ready == "True",
			controlPlane: o.Str("metadata", "labels", label) == value}
	}
	return out, err
}

// rebootChoose returns the worker node that gets a pending reboot and the rule
// that chose it, or why the region has none to give. workloads holds the
// workload Pods of each node, front the nodes that run a Ready endpoint of
// the registry's front.
func rebootChoose(nodes map[string]rebootNode, workloads map[string][]string, front map[string]bool) ([]string, string, error) {
	var servers, workers, busy []string
	for name, n := range nodes {
		switch {
		case n.cordoned || !n.ready:
			busy = append(busy, name)
		case n.controlPlane:
			servers = append(servers, name)
		default:
			workers = append(workers, name)
		}
	}
	sort.Strings(workers)
	sort.Strings(busy)
	switch {
	case len(busy) > 0:
		return nil, "", fmt.Errorf("unschedulable or not Ready before the check started: %s", strings.Join(busy, ", "))
	case len(servers) == 0:
		return nil, "", fmt.Errorf("the region has %d nodes and none with the label %s: that no control-plane node restarts could not be observed", len(nodes), secControlPlane)
	case len(workers) < 2:
		return nil, "", fmt.Errorf("the region has %d worker nodes: the check needs 2, one to restart and one to take its workloads", len(workers))
	}
	for _, name := range workers {
		if front[name] {
			return []string{name}, rebootByFront, nil
		}
	}
	for _, name := range workers {
		if len(workloads[name]) > 0 {
			return []string{name}, rebootByName, nil
		}
	}
	return nil, "", fmt.Errorf("none of the %d worker nodes runs a workload Pod (one not of a DaemonSet and not static): a restart would move nothing off", len(workers))
}

// rebootPodState is what the check reads of one Pod.
type rebootPodState struct {
	key, node, phase string // key: "<namespace>/<name> <uid>"
	controller       string // "<Kind> <namespace>/<name>" of the controller that brings the Pod back; "" when none does
	images           string // of its containers
	workload, ready  bool
}

// rebootPods is one reading of every Pod of a region.
type rebootPods struct {
	pods  []rebootPodState
	pulls []string // every container that waits on an image: "<namespace>/<pod>/<container> <reason> <image>", sorted
}

// rebootMoved is a controller of a Pod that the drain moves: its Ready Pods
// before the reboot, the images of the moved Pod and the keys of its Pods on
// the worker.
type rebootMoved struct {
	ready  int
	images string
	pods   []string
}

// pods reads every Pod of the region once. A workload Pod is one that is not
// of a DaemonSet, not a static Pod, not being deleted and in phase Pending
// or Running: a drain moves exactly these off a node.
func (c rebootCheck) pods(ctx context.Context, region kube.Region) (rebootPods, error) {
	items, err := c.list(ctx, region, "pods", "-A")
	if err != nil {
		return rebootPods{}, err
	}
	out := rebootPods{pulls: []string{}}
	for _, p := range items {
		phase, deleting := p.Str("status", "phase"), p.Str("metadata", "deletionTimestamp") != ""
		ready, _, _, _ := p.Condition("Ready")
		s := rebootPodState{key: p.Namespace() + "/" + p.Name() + " " + p.Str("metadata", "uid"), node: p.Str("spec", "nodeName"), phase: phase,
			ready: ready == "True" && !deleting}
		daemon := false
		for _, owner := range p.Objects("metadata", "ownerReferences") {
			daemon = daemon || owner.Str("kind") == "DaemonSet"
			if controls, _ := owner.Get("controller").(bool); controls {
				s.controller = rebootController(p, owner.Str("kind"), owner.Str("name"))
			}
		}
		s.workload = !daemon && !deleting && p.Str("metadata", "annotations", rebootMirror) == "" && (phase == "Running" || phase == "Pending")
		images := []string{}
		for _, container := range p.Objects("spec", "containers") {
			images = append(images, container.Str("image"))
		}
		s.images = strings.Join(images, " ")
		out.pods = append(out.pods, s)
		for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
			for _, status := range p.Objects("status", field) {
				if reason := status.Str("state", "waiting", "reason"); reason == "ErrImagePull" || reason == "ImagePullBackOff" {
					out.pulls = append(out.pulls, p.Namespace()+"/"+p.Name()+"/"+status.Str("name")+" "+reason+" "+status.Str("image"))
				}
			}
		}
	}
	sort.Strings(out.pulls)
	return out, nil
}

// rebootController names the controller of Pod p, kind and name of its owner
// reference. A ReplicaSet that a Deployment made (its name is the
// Deployment's, "-" and the Pod's label pod-template-hash) counts as the
// Deployment, so that a Pod that comes back under a new ReplicaSet counts. A
// Job ends its Pods by design: it is not named.
func rebootController(p kube.Object, kind, name string) string {
	if hash := p.Str("metadata", "labels", rebootHash); kind == "ReplicaSet" && hash != "" && strings.HasSuffix(name, "-"+hash) {
		kind, name = "Deployment", strings.TrimSuffix(name, "-"+hash)
	}
	if kind == "Job" {
		return ""
	}
	return kind + " " + p.Namespace() + "/" + name
}

// running returns, by node, the keys of the workload Pods in phase Running, sorted.
func (r rebootPods) running() map[string][]string {
	out := map[string][]string{}
	for _, p := range r.pods {
		if p.workload && p.phase == "Running" {
			out[p.node] = append(out[p.node], p.key)
		}
	}
	for node := range out {
		sort.Strings(out[node])
	}
	return out
}

// stayed returns those of the workload Pods moving that are still on node.
func (r rebootPods) stayed(node string, moving []string) []string {
	on, out := map[string]bool{}, []string{}
	for _, p := range r.pods {
		on[p.key] = p.workload && p.node == node
	}
	for _, key := range moving {
		if on[key] {
			out = append(out, key)
		}
	}
	return out
}

// readyBy returns the Ready Pods of each controller.
func (r rebootPods) readyBy() map[string]int {
	out := map[string]int{}
	for _, p := range r.pods {
		if p.ready && p.controller != "" {
			out[p.controller]++
		}
	}
	return out
}

// moved returns the controllers of the Pods moving, each with its Ready Pods
// now, the images of the moved Pod and the keys of its Pods moving.
func (r rebootPods) moved(moving []string) map[string]rebootMoved {
	out, ready := map[string]rebootMoved{}, r.readyBy()
	for _, p := range r.pods {
		if p.controller != "" && slices.Contains(moving, p.key) {
			out[p.controller] = rebootMoved{ready: ready[p.controller], images: p.images, pods: append(out[p.controller].pods, p.key)}
		}
	}
	return out
}

// returned judges one reading of the Pods, made at at, against rebootBack.
// It notes in left when each controller of moved first had none of its
// Pods of step 3 on node, and in back the seconds from then to the first
// reading in which it has its Ready Pods of step 3 again, counted up to a
// whole second. It returns, sorted, each controller that is not back within
// rebootBack of leaving, or was first read back later.
func (r rebootPods) returned(node string, moved map[string]rebootMoved, left map[string]time.Time, back map[string]int, at time.Time) []string {
	ready, late := r.readyBy(), []string{}
	for _, name := range slices.Sorted(maps.Keys(moved)) {
		m := moved[name]
		if _, done := back[name]; done {
			continue
		}
		if _, gone := left[name]; !gone && len(r.stayed(node, m.pods)) == 0 {
			left[name] = at
		}
		since, gone := left[name]
		if !gone {
			continue
		}
		took := at.Sub(since)
		seconds := int((took + time.Second - 1) / time.Second)
		if ready[name] >= m.ready {
			back[name] = seconds
		}
		switch {
		case took > rebootBack && ready[name] >= m.ready:
			late = append(late, fmt.Sprintf("%s has its %d Ready Pods back after %d seconds (%s)", name, m.ready, seconds, m.images))
		case took > rebootBack:
			late = append(late, fmt.Sprintf("%s has %d of %d Ready Pods after %d seconds (%s)", name, ready[name], m.ready, seconds, m.images))
		}
	}
	return late
}

// front returns the nodes that run a Ready endpoint of the registry's front:
// an endpoint of its EndpointSlices whose condition ready is true or unset.
func (c rebootCheck) front(ctx context.Context, region kube.Region) (map[string]bool, error) {
	items, err := c.list(ctx, region, "endpointslices.discovery.k8s.io", "-n", rebootFront, "-l", "kubernetes.io/service-name="+rebootFront)
	out := map[string]bool{}
	for _, s := range items {
		for _, e := range s.Objects("endpoints") {
			if ready, set := e.Get("conditions", "ready").(bool); (ready || !set) && e.Str("nodeName") != "" {
				out[e.Str("nodeName")] = true
			}
		}
	}
	return out, err
}

// rebootPlanRule judges every upgrade Plan of items with rebootWindow against
// the reboot span of k at now. It returns the window of each Plan and why
// each Plan that breaks the rule does, by namespace/name.
func rebootPlanRule(items []kube.Object, k rebootSettings, now time.Time) (windows, problems map[string]string) {
	windows, problems = map[string]string{}, map[string]string{}
	for _, p := range items {
		name := p.Namespace() + "/" + p.Name()
		window, problem := rebootWindow(p, k, now)
		if windows[name] = window; problem != "" {
			problems[name] = problem
		}
	}
	return windows, problems
}

// plans judges the upgrade Plans of every namespace and returns them, none
// when the cluster does not serve the kind. It puts into ev whether the
// cluster serves the kind, how many Plans there are and the window of each.
func (c rebootCheck) plans(ctx context.Context, region kube.Region, k rebootSettings, ev Evidence) ([]kube.Object, error) {
	items, err := c.list(ctx, region, rebootPlans, "-A")
	served := err == nil || !strings.Contains(err.Error(), notServed)
	ev["plan_kind_served"], ev["plans"] = served, len(items)
	if !served {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	windows, problems := rebootPlanRule(items, k, c.now())
	bad := []string{}
	for _, name := range slices.Sorted(maps.Keys(problems)) {
		bad = append(bad, "Plan "+name+" "+problems[name])
	}
	ev["plan_windows"] = windows
	if len(bad) > 0 {
		return items, fmt.Errorf("%d of %d upgrade Plans break the rule that a window ends at least %d minutes before the reboot span starts: %s", len(bad), len(items), rebootGap, strings.Join(bad, "; "))
	}
	return items, nil
}

// releases returns the Ready state of every HelmRelease, by namespace/name.
func (c rebootCheck) releases(ctx context.Context, region kube.Region) (map[string]bool, error) {
	items, err := c.list(ctx, region, hrResource, "-A")
	out := map[string]bool{}
	for _, hr := range items {
		status, _, _, _ := hr.Condition("Ready")
		out[hr.Namespace()+"/"+hr.Name()] = status == "True"
	}
	return out, err
}

// recovery reads the DaemonSet of kured and the HelmReleases, and judges them
// with all, a reading of the Pods. It returns what has not recovered yet, ""
// when kured has every Pod ready, every HelmRelease of ready is Ready again,
// every controller of moved has as many Ready Pods as before and no
// container waits on an image.
func (c rebootCheck) recovery(ctx context.Context, region kube.Region, ready []string, moved map[string]rebootMoved, all rebootPods, ev Evidence) (string, error) {
	sets, err := c.list(ctx, region, "daemonsets", "-n", rebootNamespace)
	if err != nil {
		return "", err
	}
	now, err := c.releases(ctx, region)
	if err != nil {
		return "", err
	}
	podsReady, desired := 0, 0
	for _, ds := range sets {
		if ds.Name() == rebootDaemonSet {
			podsReady, desired = ds.Int("status", "numberReady"), ds.Int("status", "desiredNumberScheduled")
		}
	}
	waiting, notAgain := []string{}, []string{}
	for _, name := range ready {
		if !now[name] {
			notAgain = append(notAgain, name)
		}
	}
	ev["kured_ready"], ev["helmreleases_not_ready_again"] = fmt.Sprintf("%d/%d", podsReady, desired), notAgain
	if desired == 0 || podsReady != desired {
		waiting = append(waiting, fmt.Sprintf("DaemonSet %s/%s has %d of %d Pods ready", rebootNamespace, rebootDaemonSet, podsReady, desired))
	}
	if len(notAgain) > 0 {
		waiting = append(waiting, fmt.Sprintf("%d of %d HelmReleases that were Ready before are not Ready: %s", len(notAgain), len(ready), strings.Join(notAgain, ", ")))
	}
	pods, fewer := all.readyBy(), []string{}
	for _, name := range slices.Sorted(maps.Keys(moved)) {
		if m := moved[name]; pods[name] < m.ready {
			fewer = append(fewer, fmt.Sprintf("%s has %d of %d Ready Pods (%s)", name, pods[name], m.ready, m.images))
		}
	}
	ev["controllers_not_ready_again"], ev["image_waits"] = fewer, all.pulls
	if len(fewer) > 0 {
		waiting = append(waiting, fmt.Sprintf("%d of %d controllers whose Pods the drain moved have fewer Ready Pods than before: %s", len(fewer), len(moved), strings.Join(fewer, ", ")))
	}
	if len(all.pulls) > 0 {
		waiting = append(waiting, fmt.Sprintf("%d containers of the region wait on an image: %s", len(all.pulls), strings.Join(all.pulls, ", ")))
	}
	return strings.Join(waiting, "; "), nil
}

func (c rebootCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	run, n, clean := NewRunID(c.now()), 0, true
	for _, region := range cc.Regions {
		n++
		clean = checkStep(rec, rebootID, n, "no previous run left its window open in the variables document of region "+region.ID, func(ev Evidence) error {
			return c.leftover(ctx, region, ev)
		}) && clean
	}
	n++
	if !clean || !checkStep(rec, rebootID, n, "journey J1 ran and every one of its steps passed", func(ev Evidence) error {
		return priorPassed(cc.Prior, func(s *Step) bool { return s.Journey == "J1" }, "journey J1", ev)
	}) {
		return nil
	}
	for _, region := range cc.Regions {
		if !c.region(ctx, region, rec, &n, run) {
			break
		}
	}
	return nil
}

// region runs the steps of one region and reports whether every one passed.
// run names the run in the note of a span the check writes and in its namespace.
func (c rebootCheck) region(ctx context.Context, region kube.Region, rec StepRecorder, n *int, run string) bool {
	step := func(name string, fn func(ev Evidence) error) bool {
		*n++
		return checkStep(rec, rebootID, *n, name+" in region "+region.ID, fn)
	}
	var k rebootSettings
	var chosen, ready, moving []string
	var before map[string]rebootNode
	var moved map[string]rebootMoved
	var plans []kube.Object
	var note *rebootMarker // the note of the span the check wrote into the variables document; nil: nothing written
	ns, created := "inf-reboot-"+strings.ToLower(run), false
	ok := step("kured is installed and its arguments give the settings", func(ev Evidence) (err error) {
		k, err = c.settings(ctx, region)
		ev["reboot_span"], ev["time_limit"], ev["sentinel"] = k.span, k.limit().String(), path.Join(k.dir, k.file)
		return err
	}) && step("every upgrade Plan has a window that ends at least "+strconv.Itoa(rebootGap)+" minutes before the reboot span starts", func(ev Evidence) (err error) {
		plans, err = c.plans(ctx, region, k, ev)
		return err
	}) && step("kured cannot run on control-plane nodes, every node is schedulable and Ready, no container waits on an image, and the worker that runs the registry front, else the first that runs workloads, is chosen", func(ev Evidence) (err error) {
		if !k.workersOnly {
			return fmt.Errorf("DaemonSet %s/%s can run on a node with the label %s, so kured could restart a region's control-plane node, which it must never do: no node was touched", rebootNamespace, rebootDaemonSet, secControlPlane)
		}
		if before, err = c.nodes(ctx, region); err != nil {
			return err
		}
		all, err := c.pods(ctx, region)
		if err != nil {
			return err
		}
		front, err := c.front(ctx, region)
		if err != nil {
			return err
		}
		running := all.running()
		var by string
		chosen, by, err = rebootChoose(before, running, front)
		ev["nodes"], ev["chosen"], ev["chosen_by"], ev["registry_front"] = len(before), chosen, by, slices.Sorted(maps.Keys(front))
		if err != nil {
			return err
		}
		moving, moved = running[chosen[0]], all.moved(running[chosen[0]])
		counts := map[string]int{}
		for name, m := range moved {
			counts[name] = m.ready
		}
		ev["workloads_to_move"], ev["controllers_moved"], ev["image_waits_before"] = moving, counts, all.pulls
		if len(all.pulls) > 0 {
			return fmt.Errorf("%d containers of the region wait on an image before the check started, so the drain could not be judged: no node was touched: %s", len(all.pulls), strings.Join(all.pulls, ", "))
		}
		return nil
	}) && step("the reboot span is open for the time limit, else the check opens a window for its run through the variables document", func(ev Evidence) (err error) {
		open, _ := k.window(c.now())
		ev["open_for"], ev["window_opened"] = open.String(), false
		if open >= k.limit() {
			return nil
		}
		if k, err = c.openWindow(ctx, region, k, plans, run, &note, ev); err != nil {
			return err
		}
		open, _ = k.window(c.now())
		ev["reboot_span"], ev["open_for"], ev["window_opened"] = k.span, open.String(), true
		if open < k.limit() {
			return fmt.Errorf("the span %s written for the run is open for %s, less than the time limit %s: no node was touched", k.span, open, k.limit())
		}
		return nil
	}) && step("the HelmReleases that are Ready before the first reboot are read", func(ev Evidence) error {
		all, err := c.releases(ctx, region)
		if err != nil {
			return err
		}
		for name, isReady := range all {
			if isReady {
				ready = append(ready, name)
			}
		}
		sort.Strings(ready)
		ev["helmreleases"], ev["ready_before"] = len(all), len(ready)
		if len(ready) == 0 {
			return fmt.Errorf("none of %d HelmReleases is Ready, so their recovery cannot be judged: no node was touched", len(all))
		}
		return nil
	}) && step("a pending reboot is put on the chosen worker node", func(ev Evidence) error {
		ev["namespace"] = ns
		if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
			return err
		}
		created = true
		for i, node := range chosen {
			pod := fmt.Sprintf(rebootPod, "sentinel-"+strconv.Itoa(i), ns, node, secProbeImage, "/host/"+k.file, k.dir)
			if _, err := c.kubectl(region, ctx, strings.NewReader(pod), "-n", ns, "create", "-f", "-"); err != nil {
				return err
			}
		}
		_, err := c.kubectl(region, ctx, nil, "-n", ns, "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod", "--all", "--timeout="+secProbeWait)
		return err
	})
	placed := c.now()
	if created {
		deleted := step("the namespace "+ns+" is deleted", func(Evidence) error {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			_, err := c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
			return err
		})
		ok = ok && deleted
	}
	ok = ok && step("the worker restarts drained, its workloads move off and it comes back, no control-plane node restarts, every node, kured and every HelmRelease that was Ready is ready again, every controller of a moved Pod has its Ready Pods back within "+rebootBack.String()+" of its Pods leaving the worker, and no container waits on an image within "+k.limit().String(), func(ev Evidence) error {
		return c.watch(ctx, region, k, chosen, moving, moved, before, ready, placed, ev)
	})
	if note != nil {
		restored := step("the reboot span of the variables document is restored", func(ev Evidence) error {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			return c.restore(cleanup, region, note, ev)
		})
		ok = ok && restored
	}
	return ok
}

// watch reads the nodes and the Pods until the chosen worker has restarted
// with its workload Pods moved off, every node is schedulable and Ready,
// kured has every Pod ready, every HelmRelease of ready is Ready again, every
// controller of moved has its Ready Pods back, each within rebootBack of its
// Pods leaving the worker, and no container waits on an image, a rule is
// broken or the time limit ends.
func (c rebootCheck) watch(ctx context.Context, region kube.Region, k rebootSettings, chosen, moving []string, moved map[string]rebootMoved, before map[string]rebootNode, ready []string, placed time.Time, ev Evidence) error {
	since, drained, oldIDs, newIDs := map[string]time.Time{}, map[string]bool{}, map[string]string{}, map[string]string{}
	left, back := map[string]time.Time{}, map[string]int{}
	ev["controllers_seconds_back"] = back
	for _, name := range chosen {
		oldIDs[name] = before[name].boot
	}
	servers := map[string]string{}
	for name, node := range before {
		if node.controlPlane {
			servers[name] = node.boot
		}
	}
	readings, failed, most, last := 0, 0, 0, "no reading of the nodes succeeded"
	ev["boot_ids_before"], ev["boot_ids_after"], ev["control_plane_boot_ids"] = oldIDs, newIDs, servers
	for {
		nodes, err := c.nodes(ctx, region)
		now := c.now()
		if err != nil {
			failed++
		} else {
			readings++
		}
		ev["readings"], ev["failed_readings"], ev["seconds"] = readings, failed, int(now.Sub(placed).Seconds())
		if err == nil {
			for name, boot := range servers {
				if got := nodes[name].boot; got != "" && got != boot {
					return fmt.Errorf("control-plane node %s restarted (boot identifier %s, before %s): kured must never restart a control-plane node", name, got, boot)
				}
			}
			down, notReady := []string{}, 0
			for name, node := range nodes {
				if !node.ready {
					notReady++
				}
				if !node.cordoned {
					delete(since, name)
					continue
				}
				down = append(down, name)
				if _, seen := since[name]; !seen {
					since[name] = now
				}
				if held := now.Sub(since[name]); held > k.drain+k.release {
					return fmt.Errorf("node %s has been unschedulable for %s, longer than drain timeout + lock release delay (%s): its drain does not finish", name, held, k.drain+k.release)
				}
			}
			sort.Strings(down)
			most = max(most, len(down))
			ev["most_unschedulable"] = most
			if len(down) > 1 {
				return fmt.Errorf("%d nodes are unschedulable at once: %s", len(down), strings.Join(down, ", "))
			}
			for _, name := range chosen {
				node := nodes[name]
				drained[name] = drained[name] || node.cordoned
				if node.boot == "" || node.boot == oldIDs[name] {
					continue
				}
				if !drained[name] {
					return fmt.Errorf("node %s has a new boot identifier and was never read unschedulable: it restarted without a drain", name)
				}
				newIDs[name] = node.boot
			}
			last = fmt.Sprintf("%d of %d nodes restarted, %d unschedulable, %d not Ready", len(newIDs), len(chosen), len(down), notReady)
			all, err := c.pods(ctx, region)
			late := []string{}
			if err == nil {
				late = all.returned(chosen[0], moved, left, back, c.now())
			}
			if len(late) > 0 {
				pulls := ""
				if len(all.pulls) > 0 {
					pulls = fmt.Sprintf("; %d containers of the region wait on an image: %s", len(all.pulls), strings.Join(all.pulls, ", "))
				}
				return fmt.Errorf("%d of %d controllers whose Pods the drain moved are not back within %d seconds of their Pods leaving %s: %s%s", len(late), len(moved), int(rebootBack.Seconds()), chosen[0], strings.Join(late, ", "), pulls)
			}
			if err == nil && len(newIDs) == len(chosen) && len(down) == 0 && notReady == 0 {
				if stayed := all.stayed(chosen[0], moving); len(stayed) > 0 {
					return fmt.Errorf("%d of the %d workload Pods that ran on %s are still on it after its restart, so its drain did not move them off: %s", len(stayed), len(moving), chosen[0], strings.Join(stayed, ", "))
				}
				var waiting string
				ev["workloads_moved_off"] = len(moving)
				waiting, err = c.recovery(ctx, region, ready, moved, all, ev)
				if err == nil && waiting == "" {
					return nil
				}
				if err == nil {
					last += "; " + waiting
				}
			}
			if err != nil {
				failed++
				ev["failed_readings"] = failed
			}
		}
		if now.Sub(placed) >= k.limit() {
			return fmt.Errorf("%s after the pending reboot was placed: %s", k.limit(), last)
		}
		if err := c.wait(ctx, rebootEvery); err != nil {
			return err
		}
	}
}
