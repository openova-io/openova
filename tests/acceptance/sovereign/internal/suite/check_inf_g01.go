package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check INF-G01, coordinated reboot. The check makes kured restart nodes: it
// is destructive and runs in the cloud only. It fails, and touches no node,
// unless journey J1 of this run passed. Then it takes the regions one after
// the other; a region is started only when every region before it passed.
// In a region it:
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
//  3. chooses rebootNodes nodes: the first control-plane node by name, then
//     the first other nodes by name, nodes without the control-plane label
//     first. Fewer nodes, no control-plane node, or a node that is
//     unschedulable or not Ready already, fail the step. So does a
//     DaemonSet that cannot run on control-plane nodes (the default of the
//     chart bp-kured, which leaves the restart of a region's control-plane
//     node to a person): the chosen control-plane node would never restart;
//  4. waits when the reboot span is not open for the whole time limit: until
//     the span opens the next time, at most rebootMaxWait. A longer wait
//     fails the step;
//  5. reads the HelmReleases of every namespace once and keeps those that are
//     Ready. None fails the step;
//  6. creates a namespace of its own and one Pod on each chosen node that
//     creates the file kured looks for, and deletes the namespace;
//  7. reads the nodes every rebootEvery until each chosen node has a new
//     boot identifier, no node is unschedulable, every node is Ready, the
//     DaemonSet of kured has every Pod ready, and every HelmRelease of step 5
//     is Ready again. The time limit is one round of kured (period + lock
//     release delay) per chosen node plus rebootSettle. The step fails at
//     once when two nodes are unschedulable in one reading, when a node is
//     unschedulable for longer than drain timeout + lock release delay, or
//     when a chosen node shows a new boot identifier and was never read
//     unschedulable. A reading that fails is no observation: the API of a
//     region with one control-plane node is away while that node restarts.
//
// The check never restarts a node itself and never changes kured.
const (
	rebootID        = "INF-G01"
	rebootNamespace = "kube-system"                 // of the DaemonSet of kured
	rebootDaemonSet = "kured"                       // also the name of its container
	rebootVolume    = "sentinel"                    // the volume of the DaemonSet with the folder of the sentinel file
	rebootLock      = "weave.works/kured-node-lock" // where kured locks when it has no --lock-annotation
	rebootHold      = "catalyst-install-hold"       // who holds the lock while the install hold of bp-kured is in place
	rebootPlans     = "plans.upgrade.cattle.io"
	rebootNodes     = 3
	rebootEvery     = 5 * time.Second
	rebootSettle    = 15 * time.Minute
	rebootMaxWait   = 150 * time.Minute
	rebootGap       = 30 // minutes from the end of a Plan window to the start of the reboot span, at least
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

// rebootChoose returns the nodes that get a pending reboot, or why the region has none to give.
func rebootChoose(nodes map[string]rebootNode) ([]string, error) {
	var servers, others, busy []string
	for name, n := range nodes {
		switch {
		case n.cordoned || !n.ready:
			busy = append(busy, name)
		case n.controlPlane:
			servers = append(servers, name)
		default:
			others = append(others, name)
		}
	}
	sort.Strings(servers)
	sort.Strings(others)
	sort.Strings(busy)
	switch {
	case len(busy) > 0:
		return nil, fmt.Errorf("unschedulable or not Ready before the check started: %s", strings.Join(busy, ", "))
	case len(nodes) < rebootNodes || len(servers) == 0:
		return nil, fmt.Errorf("the region has %d nodes, %d of them with the label %s: the check needs %d nodes and one control-plane node", len(nodes), len(servers), secControlPlane, rebootNodes)
	}
	return append([]string{servers[0]}, append(others, servers[1:]...)[:rebootNodes-1]...), nil
}

// plans judges the upgrade Plans of every namespace. It puts into ev whether
// the cluster serves the kind, how many Plans there are and the window of each.
func (c rebootCheck) plans(ctx context.Context, region kube.Region, k rebootSettings, ev Evidence) error {
	items, err := c.list(ctx, region, rebootPlans, "-A")
	served := err == nil || !strings.Contains(err.Error(), notServed)
	ev["plan_kind_served"], ev["plans"] = served, len(items)
	if !served {
		return nil
	}
	if err != nil {
		return err
	}
	windows, bad := map[string]string{}, []string{}
	for _, p := range items {
		name := p.Namespace() + "/" + p.Name()
		window, problem := rebootWindow(p, k, c.now())
		if windows[name] = window; problem != "" {
			bad = append(bad, "Plan "+name+" "+problem)
		}
	}
	ev["plan_windows"] = windows
	if sort.Strings(bad); len(bad) > 0 {
		return fmt.Errorf("%d of %d upgrade Plans break the rule that a window ends at least %d minutes before the reboot span starts: %s", len(bad), len(items), rebootGap, strings.Join(bad, "; "))
	}
	return nil
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

// recovery reads the DaemonSet of kured and the HelmReleases. It returns what
// has not recovered yet, "" when kured has every Pod ready and every
// HelmRelease of ready is Ready again.
func (c rebootCheck) recovery(ctx context.Context, region kube.Region, ready []string, ev Evidence) (string, error) {
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
	return strings.Join(waiting, "; "), nil
}

func (c rebootCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 1
	if !checkStep(rec, rebootID, n, "journey J1 ran and every one of its steps passed", func(ev Evidence) error {
		return priorPassed(cc.Prior, func(s *Step) bool { return s.Journey == "J1" }, "journey J1", ev)
	}) {
		return nil
	}
	for _, region := range cc.Regions {
		if !c.region(ctx, region, rec, &n) {
			break
		}
	}
	return nil
}

// region runs the steps of one region and reports whether every one passed.
func (c rebootCheck) region(ctx context.Context, region kube.Region, rec StepRecorder, n *int) bool {
	step := func(name string, fn func(ev Evidence) error) bool {
		*n++
		return checkStep(rec, rebootID, *n, name+" in region "+region.ID, fn)
	}
	var k rebootSettings
	var chosen, ready []string
	var before map[string]rebootNode
	ns, created := "inf-reboot-"+strings.ToLower(NewRunID(c.now())), false
	ok := step("kured is installed and its arguments give the settings", func(ev Evidence) (err error) {
		k, err = c.settings(ctx, region)
		ev["reboot_span"], ev["time_limit"], ev["sentinel"] = k.span, k.limit().String(), path.Join(k.dir, k.file)
		return err
	}) && step("every upgrade Plan has a window that ends at least "+strconv.Itoa(rebootGap)+" minutes before the reboot span starts", func(ev Evidence) error {
		return c.plans(ctx, region, k, ev)
	}) && step("three nodes are chosen, one of them a control-plane node, and every node is schedulable and Ready", func(ev Evidence) (err error) {
		if before, err = c.nodes(ctx, region); err != nil {
			return err
		}
		if k.workersOnly {
			return fmt.Errorf("DaemonSet %s/%s cannot run on a node with the label %s, so kured never restarts the control-plane node this step chooses: no node was touched", rebootNamespace, rebootDaemonSet, secControlPlane)
		}
		chosen, err = rebootChoose(before)
		ev["nodes"], ev["chosen"] = len(before), chosen
		return err
	}) && step("the reboot span is open for the time limit", func(ev Evidence) error {
		open, next := k.window(c.now())
		ev["open_for"], ev["waited"] = open.String(), "0s"
		if open >= k.limit() {
			return nil
		}
		if next > rebootMaxWait {
			return fmt.Errorf("the reboot span %s is open for %s, the time limit is %s, and the span opens again in %s, more than %s: no node was touched", k.span, open, k.limit(), next, rebootMaxWait)
		}
		ev["waited"] = next.String()
		return c.wait(ctx, next)
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
	}) && step("a pending reboot is put on the three nodes", func(ev Evidence) error {
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
	return ok && step("the three nodes restart one at a time, drained, and every node, kured and every HelmRelease that was Ready is ready again within "+k.limit().String(), func(ev Evidence) error {
		return c.watch(ctx, region, k, chosen, before, ready, placed, ev)
	})
}

// watch reads the nodes until the three chosen nodes have restarted, every
// node is schedulable and Ready, kured has every Pod ready and every
// HelmRelease of ready is Ready again, a rule is broken or the time limit ends.
func (c rebootCheck) watch(ctx context.Context, region kube.Region, k rebootSettings, chosen []string, before map[string]rebootNode, ready []string, placed time.Time, ev Evidence) error {
	since, drained, oldIDs, newIDs := map[string]time.Time{}, map[string]bool{}, map[string]string{}, map[string]string{}
	for _, name := range chosen {
		oldIDs[name] = before[name].boot
	}
	readings, failed, most, last := 0, 0, 0, "no reading of the nodes succeeded"
	ev["boot_ids_before"], ev["boot_ids_after"] = oldIDs, newIDs
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
			if len(newIDs) == len(chosen) && len(down) == 0 && notReady == 0 {
				waiting, err := c.recovery(ctx, region, ready, ev)
				switch {
				case err != nil:
					failed++
					ev["failed_readings"] = failed
				case waiting == "":
					return nil
				default:
					last += "; " + waiting
				}
			}
		}
		if now.Sub(placed) >= k.limit() {
			return fmt.Errorf("%s after the pending reboots were placed: %s", k.limit(), last)
		}
		if err := c.wait(ctx, rebootEvery); err != nil {
			return err
		}
	}
}
