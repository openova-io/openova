package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// scanWorld is the fake cluster of check INF-G02: two regions with the nodes
// cp-1 and w-1, the release bp-machine-operations installed, and an operations
// controller that ends a run after `reads` readings of it. fault names what is
// wrong; "" is a platform that scans every node.
type scanWorld struct {
	t      *testing.T
	clock  time.Time
	fault  string
	reads  int               // readings of a run before it is final
	runs   map[string]string // region -> the name of the run created there
	name   string            // the name of the run the check sends in a create
	read   map[string]int    // region -> readings of its run
	calls  []string          // the writes, in order
	extras []string          // nodes that have no Machine
}

func (w *scanWorld) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	list := func(items ...obj) ([]byte, error) { return json.Marshal(obj{"items": items}) }
	run, final := w.runs[r.ID], w.read[r.ID] > w.reads
	switch {
	case strings.Contains(joined, "patch"):
		w.t.Errorf("the check ran kubectl %s", joined)
	case args[0] == "create":
		w.calls = append(w.calls, "create "+r.ID)
		var m struct {
			Metadata struct{ Namespace, Name string }
			Spec     struct {
				Kind, RequestedBy string
				PolicyRef         struct{ Kind, Name string }
			}
		}
		body, _ := io.ReadAll(stdin)
		if err := json.Unmarshal(body, &m); err != nil || m.Metadata.Namespace != "catalyst-infra" || !strings.HasPrefix(m.Metadata.Name, "inf-g02-") ||
			m.Spec.Kind != "scan" || m.Spec.PolicyRef != (struct{ Kind, Name string }{"ScanPolicy", "nodes"}) || m.Spec.RequestedBy == "" {
			w.t.Errorf("the check created %s", body)
		}
		w.name = m.Metadata.Name
		switch {
		case w.fault == "refused" && r.ID == "region-b":
			return nil, errors.New("admission webhook denied the request")
		case w.fault == "lost" && r.ID == "region-b":
			w.runs[r.ID] = m.Metadata.Name // the API server stored the run, its answer is lost
			return nil, errors.New("Unable to connect to the server: EOF")
		}
		w.runs[r.ID] = m.Metadata.Name
		return nil, nil
	case args[0] == "delete":
		w.calls = append(w.calls, "delete "+r.ID)
		if joined != "delete machineruns.machines.openova.io "+w.name+" -n catalyst-infra --wait=false --ignore-not-found" {
			w.t.Errorf("the check ran kubectl %s", joined)
		}
		if w.fault == "delete refused" && r.ID == "region-a" {
			return nil, errors.New("Unable to connect to the server: EOF")
		}
		return nil, nil
	case joined == "get scanpolicies.machines.openova.io nodes -n catalyst-infra -o json":
		if w.fault == "no policy" {
			return nil, errors.New(`error: the server doesn't have a resource type "scanpolicies"`)
		}
		return json.Marshal(obj{"metadata": obj{"name": "nodes"}})
	case joined == "get nodes -o json":
		if w.fault == "no nodes" && r.ID == "region-b" {
			return list()
		}
		items := []obj{}
		for _, n := range append([]string{"cp-1", "w-1"}, w.extras...) {
			items = append(items, obj{"metadata": obj{"name": n}})
		}
		return list(items...)
	case joined == "get machines.machines.openova.io -n catalyst-infra -o json":
		items := []obj{}
		for _, n := range []string{"cp-1", "w-1"} {
			m := obj{"metadata": obj{"name": n, "labels": obj{"machines.openova.io/class": "node"}}, "spec": obj{"class": "node"},
				"status": obj{"phase": "Active"}}
			switch {
			case w.fault == "stale" && n == "w-1":
				m["status"] = obj{"phase": "Stale"}
			case w.fault == "not class" && n == "w-1":
				m["spec"] = obj{"class": "vm"}
			case w.fault == "no label" && n == "w-1":
				m["metadata"] = obj{"name": n, "labels": obj{}}
			}
			key, sum, at := "catalyst-infra/"+n+"/"+run+"/sbom.cdx.json", strings.Repeat("ab", 32), "2026-11-18T10:05:00Z"
			switch {
			case w.fault == "old bill" && n == "w-1":
				key = "catalyst-infra/w-1/scan-nightly/sbom.cdx.json"
			case w.fault == "bad sum" && n == "cp-1":
				sum = "md5:00"
			case w.fault == "no time" && n == "cp-1":
				at = ""
			case w.fault == "bad time" && n == "cp-1":
				at = "yesterday"
			}
			if run != "" && final {
				m["status"].(obj)["sbom"] = obj{"key": key, "sha256": sum, "collectedAt": at}
			}
			items = append(items, m)
		}
		return list(items...)
	case run != "" && joined == "get machineruns.machines.openova.io "+run+" -n catalyst-infra -o json":
		w.read[r.ID]++
		if w.fault == "flaky" && w.read[r.ID] == 1 {
			return nil, errors.New("Unable to connect to the server: EOF")
		}
		status := obj{"phase": "Running"}
		if final && w.fault != "never ends" {
			targets := []any{}
			for _, n := range []string{"cp-1", "w-1"} {
				t := obj{"machine": "catalyst-infra/" + n, "outcome": "Succeeded", "reportName": "machine-" + n + "-hardening",
					"evidence": []any{"catalyst-infra/" + n + "/" + run + "/sbom.cdx.json", "catalyst-infra/" + n + "/" + run + "/hardening.results.json"}}
				switch {
				case w.fault == "no evidence" && n == "w-1":
					t["evidence"] = []any{}
				case w.fault == "target failed" && n == "w-1":
					t["outcome"], t["message"] = "Failed", "inventory: no-sbom-file"
				case w.fault == "report name" && n == "w-1":
					t["reportName"] = "machine-w-1-vulnerability"
				case w.fault == "no profile" && n == "w-1":
					t["evidence"], t["message"] = []any{"catalyst-infra/w-1/" + run + "/sbom.cdx.json"}, "hardening: no-profile-for-release"
				case w.fault == "no target" && n == "w-1":
					continue
				}
				targets = append(targets, t)
			}
			status = obj{"phase": "Succeeded", "targets": targets, "counts": obj{"Succeeded": 2}}
			if w.fault == "partly" {
				status["phase"], targets[1].(obj)["outcome"], targets[1].(obj)["message"] = "PartiallyFailed", "Failed", "hardening: no-results-file"
			}
		}
		return json.Marshal(obj{"metadata": obj{"name": run, "namespace": "catalyst-infra"}, "status": status})
	case joined == "get policyreports.wgpolicyk8s.io -n catalyst-infra -o json":
		items := []obj{}
		for _, n := range []string{"cp-1", "w-1"} {
			name := run
			if w.fault == "old report" && n == "cp-1" {
				name = "scan-nightly"
			}
			results := []any{obj{"rule": "r1", "result": "pass", "properties": obj{"runName": name}}}
			switch {
			case w.fault == "empty report" && n == "w-1":
				results = nil
			case w.fault == "no profile" && n == "w-1":
				// what the operations controller writes when no rule of the node is checked
				results = []any{obj{"rule": "no-profile-for-release", "result": "skip", "properties": obj{"runName": name}}}
			}
			items = append(items, obj{"metadata": obj{"name": "machine-" + n + "-hardening"}, "results": results})
		}
		return list(items...)
	}
	return nil, fmt.Errorf("the fake cluster cannot answer kubectl %s", joined)
}

func TestNodeScanCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		fault   string
		extras  []string
		fails   string // a part of the reason; "" when the check passes
		created int    // the regions the check sends a create to, and must send a delete to
	}{
		"every node of both regions is scanned":   {created: 2},
		"a failed reading of a run is no failure": {fault: "flaky", created: 2},
		"the release is not installed":            {fault: "no policy", fails: "ScanPolicy catalyst-infra/nodes"},
		"a Node has no Machine":                   {extras: []string{"w-2"}, fails: "[region-a] 1 of 3 Nodes have no Active Machine of class node in catalyst-infra: w-2"},
		"a Machine is not Active":                 {fault: "stale", fails: "1 of 2 Nodes have no Active Machine"},
		"the run is refused in one region":        {fault: "refused", created: 2, fails: "[region-b] create MachineRun catalyst-infra/inf-g02-"},
		"the answer to a create is lost":          {fault: "lost", created: 2, fails: "[region-b] create MachineRun catalyst-infra/inf-g02-"},
		"the run never ends":                      {fault: "never ends", created: 2, fails: "after 30m0s 0 of 2 runs have a final phase"},
		"a node fails its scan":                   {fault: "partly", created: 2, fails: "ended PartiallyFailed"},
		"a target lists no bill of materials":     {fault: "no evidence", created: 2, fails: "[region-a] w-1: the target lists no evidence catalyst-infra/w-1/inf-g02-"},
		"the bill of materials is of another run": {fault: "old bill", created: 2, fails: `w-1: Machine.status.sbom.key is "catalyst-infra/w-1/scan-nightly/sbom.cdx.json"`},
		"the checksum is no sha256":               {fault: "bad sum", created: 2, fails: `cp-1: Machine.status.sbom has the sha256 "md5:00"`},
		"a hardening report has no result":        {fault: "empty report", created: 2, fails: "w-1: PolicyReport catalyst-infra/machine-w-1-hardening is missing or has no result"},
		"a hardening report is of another run":    {fault: "old report", created: 2, fails: `cp-1: a result of PolicyReport machine-cp-1-hardening names the run "scan-nightly"`},
		"a node has no profile for its release":   {fault: "no profile", created: 2, fails: "[region-a] w-1: PolicyReport catalyst-infra/machine-w-1-hardening checked no hardening rule, it holds only the row no-profile-for-release (hardening: no-profile-for-release)"},
		"the run has no target for a node":        {fault: "no target", created: 2, fails: "[region-a] w-1: the run has no target catalyst-infra/w-1"},
		"a target fails inside a succeeded run":   {fault: "target failed", created: 2, fails: `w-1: the target ended "Failed" (inventory: no-sbom-file)`},
		"a target names another report":           {fault: "report name", created: 2, fails: `w-1: the target names the report "machine-w-1-vulnerability", want machine-w-1-hardening`},
		"the bill of materials has no time":       {fault: "no time", created: 2, fails: `cp-1: Machine.status.sbom has the sha256 "` + strings.Repeat("ab", 32) + `" and the collection time ""`},
		"the collection time is no time":          {fault: "bad time", created: 2, fails: `cp-1: Machine.status.sbom has the sha256 "` + strings.Repeat("ab", 32) + `" and the collection time "yesterday"`},
		"a Machine is not of class node":          {fault: "not class", fails: "[region-a] 1 of 2 Nodes have no Active Machine of class node in catalyst-infra: w-1"},
		"a Machine has no class label":            {fault: "no label", fails: "[region-a] 1 of 2 Nodes have no Active Machine of class node in catalyst-infra: w-1"},
		"a region lists no Node":                  {fault: "no nodes", fails: "[region-b] the region lists no Node"},
		"a delete fails in one region":            {fault: "delete refused", created: 2, fails: "[region-a] delete MachineRun catalyst-infra/inf-g02-"},
	} {
		t.Run(name, func(t *testing.T) {
			w := &scanWorld{t: t, clock: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC), fault: tc.fault, reads: 3,
				runs: map[string]string{}, read: map[string]int{}, extras: tc.extras}
			c := infScanCheck{kubectl: w.kubectl, now: func() time.Time { return w.clock },
				wait: func(_ context.Context, d time.Duration) error { w.clock = w.clock.Add(d); return nil }}
			r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
			m := &Manifest{Checks: []ManifestCheck{entry(infScanID, StateExpectedFail)}}
			r.runChecks(context.Background(), m, []Check{c}, &CheckContext{RunIn: RunInLocal, Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}, true)
			res := r.run.Checks[0]
			if (res.Status == Pass) != (tc.fails == "") || !strings.Contains(res.Reason, tc.fails) {
				t.Errorf("the check is %s (%s), want the reason %q", res.Status, res.Reason, tc.fails)
			}
			want := []string{}
			for _, op := range []string{"create", "delete"} {
				for _, region := range []string{"region-a", "region-b"}[:tc.created] {
					want = append(want, op+" "+region)
				}
			}
			if strings.Join(w.calls, ", ") != strings.Join(want, ", ") {
				t.Errorf("the writes are %v, want %v", w.calls, want)
			}
		})
	}
}
