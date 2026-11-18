package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check INF-G02, node host scans. In every region one scan run of the default
// node policy gives every node a hardening report and a bill of materials. The
// check:
//  1. reads in every region the Nodes, ScanPolicy catalyst-infra/nodes and the
//     Machines of catalyst-infra: every Node has a Machine named after it, of
//     class node and in phase Active, so the policy selects it;
//  2. creates in every region one MachineRun inf-g02-<run> of kind scan for
//     that policy;
//  3. waits until the run of every region has a final phase, at most 30
//     minutes in all; each must be Succeeded;
//  4. judges every Node: its target of the run is Succeeded and lists the key
//     catalyst-infra/<node>/<run>/sbom.cdx.json; Machine.status.sbom holds that
//     key, a sha256 of 64 hexadecimal digits and a collection time in RFC 3339
//     form; PolicyReport catalyst-infra/machine-<node>-hardening has at least
//     one result, a result of a rule that was checked, and every result names
//     the run;
//  5. deletes the run in every region it sent a create to, also when the create
//     returned an error (the API server may have stored the run before the
//     answer was lost), whatever happened before.
//
// The bill of materials is read through the Kubernetes API, as the key and the
// checksum that the operations controller wrote into Machine.status.sbom. The
// check never reads the evidence bucket: no check of the suite reads object
// storage and no route of catalyst-api serves the bucket. That the file is
// CycloneDX is proven by the runner, which refuses to upload a file whose
// bomFormat is not CycloneDX (machines/runner/inventory.go and its test).
const (
	infScanID        = "INF-G02"
	infScanNamespace = "catalyst-infra"
	infScanPolicy    = "nodes"
	infScanLimit     = 30 * time.Minute
	infScanPoll      = 15 * time.Second
	infScanRuns      = "machineruns.machines.openova.io"
	infScanMachines  = "machines.machines.openova.io"
	infScanPolicies  = "scanpolicies.machines.openova.io"
	infScanReports   = "policyreports.wgpolicyk8s.io"
	infScanSucceeded = "Succeeded"
	// infScanNoProfile is the rule of the single row of a hardening report in
	// which no rule was checked: the baseline has no profile for the release of
	// the node, or the content has no data stream for it (runner.MessageNoProfile).
	infScanNoProfile = "no-profile-for-release"
)

// infScanRun is the MachineRun the check creates: namespace, name.
const infScanRun = `{"apiVersion": "machines.openova.io/v1alpha1", "kind": "MachineRun",
  "metadata": {"namespace": %q, "name": %q, "labels": {"app.kubernetes.io/managed-by": "acceptance-suite"}},
  "spec": {"kind": "scan", "policyRef": {"kind": "ScanPolicy", "name": %q},
    "requestedBy": "acceptance-suite", "reason": "gate check INF-G02"}}`

var infScanSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

type infScanCheck struct {
	kubectl kubectlFunc
	now     func() time.Time
	wait    func(ctx context.Context, d time.Duration) error // waits d, or until ctx ends
}

func init() {
	RegisterCheck(infScanCheck{kubectl: kube.Region.Run, now: time.Now, wait: rebootWait})
}

func (infScanCheck) ID() string { return infScanID }

// get reads one object, or the list of a kind, as JSON.
func (c infScanCheck) get(ctx context.Context, region kube.Region, args ...string) (kube.Object, error) {
	out, err := c.kubectl(region, ctx, nil, append(append([]string{"get"}, args...), "-o", "json")...)
	if err != nil {
		return nil, err
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("[%s] decode %s: %w", region.ID, strings.Join(args, " "), err)
	}
	return o, nil
}

// nodes returns the names of the Nodes of a region that the policy does not
// select through a Machine, and the names of all its Nodes, sorted.
func (c infScanCheck) nodes(ctx context.Context, region kube.Region) (all, unselected []string, err error) {
	if _, err := c.get(ctx, region, infScanPolicies, infScanPolicy, "-n", infScanNamespace); err != nil {
		return nil, nil, fmt.Errorf("[%s] ScanPolicy %s/%s: %w", region.ID, infScanNamespace, infScanPolicy, err)
	}
	nodes, err := c.get(ctx, region, "nodes")
	if err != nil {
		return nil, nil, err
	}
	machines, err := c.get(ctx, region, infScanMachines, "-n", infScanNamespace)
	if err != nil {
		return nil, nil, err
	}
	active := map[string]bool{}
	for _, m := range machines.Objects("items") {
		active[m.Name()] = m.Str("spec", "class") == "node" && m.Str("status", "phase") == "Active" &&
			m.Str("metadata", "labels", "machines.openova.io/class") == "node"
	}
	for _, n := range nodes.Objects("items") {
		all = append(all, n.Name())
		if !active[n.Name()] {
			unselected = append(unselected, n.Name())
		}
	}
	sort.Strings(all)
	sort.Strings(unselected)
	if len(all) == 0 {
		return nil, nil, fmt.Errorf("[%s] the region lists no Node", region.ID)
	}
	return all, unselected, nil
}

// judge returns what is wrong with the scan of the nodes of one region.
func (c infScanCheck) judge(ctx context.Context, region kube.Region, run kube.Object, nodes []string, ev map[string]any) ([]string, error) {
	targets := map[string]kube.Object{}
	for _, t := range run.Objects("status", "targets") {
		targets[t.Str("machine")] = t
	}
	machines, err := c.get(ctx, region, infScanMachines, "-n", infScanNamespace)
	if err != nil {
		return nil, err
	}
	sboms := map[string]kube.Object{}
	for _, m := range machines.Objects("items") {
		if s, isMap := m.Get("status", "sbom").(map[string]any); isMap {
			sboms[m.Name()] = s
		}
	}
	reports, err := c.get(ctx, region, infScanReports, "-n", infScanNamespace)
	if err != nil {
		return nil, err
	}
	byName := map[string]kube.Object{}
	for _, r := range reports.Objects("items") {
		byName[r.Name()] = r
	}
	wrong, keys := []string{}, map[string]string{}
	for _, node := range nodes {
		key, report := infScanNamespace+"/"+node+"/"+run.Name()+"/sbom.cdx.json", "machine-"+node+"-hardening"
		t, sbom := targets[infScanNamespace+"/"+node], sboms[node]
		switch {
		case t == nil:
			wrong = append(wrong, node+": the run has no target "+infScanNamespace+"/"+node)
		case t.Str("outcome") != infScanSucceeded:
			wrong = append(wrong, fmt.Sprintf("%s: the target ended %q (%s)", node, t.Str("outcome"), t.Str("message")))
		case !infScanLists(t, key):
			wrong = append(wrong, node+": the target lists no evidence "+key)
		case t.Str("reportName") != report:
			wrong = append(wrong, fmt.Sprintf("%s: the target names the report %q, want %s", node, t.Str("reportName"), report))
		case sbom.Str("key") != key:
			wrong = append(wrong, fmt.Sprintf("%s: Machine.status.sbom.key is %q, want %s", node, sbom.Str("key"), key))
		case !infScanSHA.MatchString(sbom.Str("sha256")) || !infScanTime(sbom.Str("collectedAt")):
			wrong = append(wrong, fmt.Sprintf("%s: Machine.status.sbom has the sha256 %q and the collection time %q", node, sbom.Str("sha256"), sbom.Str("collectedAt")))
		default:
			keys[node] = key + " sha256:" + sbom.Str("sha256")
			results := byName[report].Objects("results")
			switch {
			case len(results) == 0:
				wrong = append(wrong, fmt.Sprintf("%s: PolicyReport %s/%s is missing or has no result", node, infScanNamespace, report))
			case !infScanChecked(results):
				wrong = append(wrong, fmt.Sprintf("%s: PolicyReport %s/%s checked no hardening rule, it holds only the row %s (%s)",
					node, infScanNamespace, report, infScanNoProfile, t.Str("message")))
			}
			for _, r := range results {
				if got := r.Str("properties", "runName"); got != run.Name() {
					wrong = append(wrong, fmt.Sprintf("%s: a result of PolicyReport %s names the run %q, want %s", node, report, got, run.Name()))
					break
				}
			}
		}
	}
	ev[region.ID] = keys
	return wrong, nil
}

func infScanLists(t kube.Object, key string) bool {
	list, _ := t.Get("evidence").([]any)
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}

// infScanTime reports whether s is a time in RFC 3339 form, the form the CRD
// gives collectedAt (date-time).
func infScanTime(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// infScanChecked reports whether a hardening report holds a result of a rule
// that was checked, not only the row no-profile-for-release. The operations
// controller writes that row, and the target still succeeds, when no rule of
// the node could be checked.
func infScanChecked(results []kube.Object) bool {
	for _, r := range results {
		if r.Str("rule") != infScanNoProfile {
			return true
		}
	}
	return false
}

func (c infScanCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return fmt.Errorf("the check needs one region")
	}
	name := "inf-g02-" + strings.ToLower(NewRunID(c.now()))
	nodes := map[string][]string{}
	created := []kube.Region{}
	runs := map[string]kube.Object{}

	ok := checkStep(rec, infScanID, 1, "every Node has an Active Machine of class node that ScanPolicy "+infScanNamespace+"/"+infScanPolicy+" selects", func(ev Evidence) error {
		for _, region := range cc.Regions {
			all, unselected, err := c.nodes(ctx, region)
			if err != nil {
				return err
			}
			nodes[region.ID], ev[region.ID] = all, all
			if len(unselected) > 0 {
				return fmt.Errorf("[%s] %d of %d Nodes have no Active Machine of class node in %s: %s",
					region.ID, len(unselected), len(all), infScanNamespace, strings.Join(unselected, ", "))
			}
		}
		return nil
	})
	ok = ok && checkStep(rec, infScanID, 2, "a MachineRun of kind scan for policy "+infScanPolicy+" is created in every region", func(ev Evidence) error {
		ev["run"] = name
		for _, region := range cc.Regions {
			manifest := fmt.Sprintf(infScanRun, infScanNamespace, name, infScanPolicy)
			// a create whose answer is lost may still have stored the run: step 5
			// deletes in every region a create was sent to (--ignore-not-found)
			created = append(created, region)
			if _, err := c.kubectl(region, ctx, strings.NewReader(manifest), "create", "-f", "-"); err != nil {
				return fmt.Errorf("[%s] create MachineRun %s/%s: %w", region.ID, infScanNamespace, name, err)
			}
		}
		return nil
	})
	ok = ok && checkStep(rec, infScanID, 3, "the run of every region is Succeeded within "+infScanLimit.String(), func(ev Evidence) error {
		deadline, phases := c.now().Add(infScanLimit), map[string]string{}
		var last error
		for {
			final := 0
			for _, region := range cc.Regions {
				run, err := c.get(ctx, region, infScanRuns, name, "-n", infScanNamespace)
				if err != nil {
					last = err // a reading that fails is no observation
					continue
				}
				runs[region.ID], phases[region.ID] = run, run.Str("status", "phase")
				if p := phases[region.ID]; p == infScanSucceeded || p == "PartiallyFailed" || p == "Failed" {
					final++
				}
			}
			ev["phases"] = phases
			if final == len(cc.Regions) {
				break
			}
			if !c.now().Before(deadline) {
				return fmt.Errorf("after %s %d of %d runs have a final phase: %v (last failed reading: %v)", infScanLimit, final, len(cc.Regions), phases, last)
			}
			if err := c.wait(ctx, infScanPoll); err != nil {
				return err
			}
		}
		for _, region := range cc.Regions {
			if p := phases[region.ID]; p != infScanSucceeded {
				return fmt.Errorf("[%s] MachineRun %s/%s ended %s, counts %v", region.ID, infScanNamespace, name, p, runs[region.ID].Get("status", "counts"))
			}
		}
		return nil
	})
	if ok {
		checkStep(rec, infScanID, 4, "every Node has the bill of materials of the run in Machine.status.sbom and a hardening report of the run", func(ev Evidence) error {
			wrong := []string{}
			for _, region := range cc.Regions {
				w, err := c.judge(ctx, region, runs[region.ID], nodes[region.ID], ev)
				if err != nil {
					return err
				}
				for _, line := range w {
					wrong = append(wrong, "["+region.ID+"] "+line)
				}
			}
			if len(wrong) > 0 {
				return fmt.Errorf("%d nodes are not scanned: %s", len(wrong), strings.Join(wrong, "; "))
			}
			return nil
		})
	}
	if len(created) > 0 {
		checkStep(rec, infScanID, 5, "the runs the check created are deleted", func(ev Evidence) error {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			deleted, failed := []string{}, []string{}
			for _, region := range created {
				if _, err := c.kubectl(region, cleanup, nil, "delete", infScanRuns, name, "-n", infScanNamespace,
					"--wait=false", "--ignore-not-found"); err != nil {
					failed = append(failed, fmt.Sprintf("[%s] delete MachineRun %s/%s: %v", region.ID, infScanNamespace, name, err))
					continue // the runs of the other regions are still deleted
				}
				deleted = append(deleted, region.ID)
				ev["deleted"] = deleted
			}
			if len(failed) > 0 {
				return fmt.Errorf("%s; a run that is not deleted is deleted after the retention of the chart", strings.Join(failed, "; "))
			}
			return nil
		})
	}
	return nil
}
