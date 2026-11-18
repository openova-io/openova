package suite

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// metadataFake is the kubectl of check NET-G10: it creates and deletes the
// namespace of the check and hands every other call to the fake of the dial
// Pods (check_policy_dials_test.go). The answers of nc depend on the Pod: a
// Pod with the label network.openova.io/metadata-access=true gets exempt,
// every other Pod plain.
type metadataFake struct {
	dial                         *dialFake
	plain, exempt                map[string]string // <host>:<port> -> outcome
	createErr                    error
	nsCreated, nsDeleted, dialed []string // the namespaces created and deleted, and per dial whether its Pod is exempt or plain
}

func (f *metadataFake) kubectl(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	switch {
	case len(args) == 3 && args[0] == "create" && args[1] == "namespace":
		if f.createErr != nil {
			return nil, f.createErr
		}
		f.nsCreated = append(f.nsCreated, args[2])
		return nil, nil
	case args[0] == "delete" && args[1] == "namespace":
		if strings.Join(args[3:], " ") != "--wait=false --ignore-not-found" {
			f.dial.t.Errorf("unexpected delete of the namespace: %v", args)
		}
		f.nsDeleted = append(f.nsDeleted, args[2])
		return nil, nil
	case len(args) > 4 && args[2] == "exec":
		f.dial.answers = f.plain
		kind := "plain"
		if strings.Contains(f.dial.labels[args[1]+"/"+args[4]], ","+MetadataAccessLabel+"=true") {
			f.dial.answers, kind = f.exempt, "exempt"
		}
		f.dialed = append(f.dialed, kind)
	}
	return f.dial.kubectl(r, ctx, stdin, args...)
}

// metadataRun runs check NET-G10 on two regions the way the runner does. It
// fails the test unless the check passed (want is empty) or failed with a
// reason that holds want, and unless every namespace it created was deleted.
func metadataRun(t *testing.T, f *metadataFake, regions []kube.Region, want string) *runner {
	t.Helper()
	r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
	c := metadataBlockedCheck{kubectl: f.kubectl}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c}, &CheckContext{Regions: regions}, true)
	if res := r.run.Checks[0]; (want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, want) {
		t.Errorf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, want)
	}
	if strings.Join(f.nsCreated, " ") != strings.Join(f.nsDeleted, " ") {
		t.Errorf("namespaces created %v, deleted %v", f.nsCreated, f.nsDeleted)
	}
	return r
}

func twoRegions() []kube.Region { return []kube.Region{{ID: "region-a"}, {ID: "region-b"}} }

// TestMetadataBlockedCheck: the check passes only when the Pod without the
// label times out on the metadata address and the Pod with the label connects,
// and both reach the cluster DNS service.
func TestMetadataBlockedCheck(t *testing.T) {
	control, metadata := dialControlHost+":53", "169.254.169.254:80"
	for name, c := range map[string]struct {
		plainControl, plain, exempt, want string
	}{
		"blocked":         {"connected", "timed-out", "connected", ""},
		"reachable":       {"connected", "connected", "connected", "want deny; the dial of 169.254.169.254:80 connected"},
		"refused":         {"connected", "refused", "connected", "want deny; the dial of 169.254.169.254:80 was refused"},
		"nothing answers": {"connected", "timed-out", "timed-out", "want allow; the dial of 169.254.169.254:80 timed-out"},
		"no network":      {"timed-out", "timed-out", "connected", "it has no working network"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &metadataFake{dial: &dialFake{t: t}, plain: map[string]string{control: c.plainControl, metadata: c.plain},
				exempt: map[string]string{control: "connected", metadata: c.exempt}}
			r := metadataRun(t, f, twoRegions(), c.want)
			if len(r.run.Steps) != 8 || strings.Join(f.dialed, " ") != "plain exempt plain exempt" {
				t.Fatalf("want per region one start step, the dial of the plain Pod, the dial of the exempt Pod and one delete step, got %d steps and dials %v", len(r.run.Steps), f.dialed)
			}
			ns := f.nsCreated[0]
			if len(f.nsCreated) != 2 || !strings.HasPrefix(ns, "net-metadata-") || f.nsCreated[1] != ns {
				t.Errorf("want the namespace net-metadata-<run> in each region, got %v", f.nsCreated)
			}
			created := f.dial.created
			if len(created) != 4 || !strings.HasPrefix(created[0], ns+"/") || !strings.HasPrefix(created[1], ns+"/") ||
				strings.Contains(f.dial.labels[created[0]], MetadataAccessLabel) || !strings.HasSuffix(f.dial.labels[created[1]], ","+MetadataAccessLabel+"=true") {
				t.Errorf("want per region a Pod without the label and a Pod with the label set to true, both in %s: %v %v", ns, created, f.dial.labels)
			}
			if e := r.run.Steps[1].Evidence; e["expect"] != "deny" || e["observed"] != c.plain || e["block"] != "D1.5" {
				t.Errorf("the step of the plain Pod records %v", e)
			}
		})
	}
}

// TestMetadataBlockedCheck_Faults: Pods that are not Ready, a namespace that
// cannot be created and a run without a region fail, and the registry holds
// the check.
func TestMetadataBlockedCheck_Faults(t *testing.T) {
	f := &metadataFake{dial: &dialFake{t: t, waitErr: errors.New("timed out waiting for the condition")}}
	if r := metadataRun(t, f, twoRegions(), "is not Ready: timed out waiting for the condition"); len(r.run.Steps) != 4 || len(f.nsDeleted) != 2 || len(f.dialed) != 0 {
		t.Errorf("Pods that are not Ready: want no dial and both namespaces deleted, got %d steps, %v deleted, dials %v", len(r.run.Steps), f.nsDeleted, f.dialed)
	}
	f = &metadataFake{dial: &dialFake{t: t}, createErr: errors.New("forbidden")}
	if r := metadataRun(t, f, twoRegions(), "forbidden"); len(r.run.Steps) != 2 || len(f.dial.created) != 0 || len(f.nsDeleted) != 0 {
		t.Errorf("a namespace that cannot be created: want no Pod and no delete step, got %d steps, Pods %v, %v deleted", len(r.run.Steps), f.dial.created, f.nsDeleted)
	}
	f = &metadataFake{dial: &dialFake{t: t}}
	if r := metadataRun(t, f, nil, "the run has no region"); len(f.nsCreated) != 0 || len(r.run.Steps) != 1 {
		t.Errorf("no region: want one failed step and no namespace, got %d steps and %v", len(r.run.Steps), f.nsCreated)
	}
	if c, ok := registry[metadataBlockedID].(metadataBlockedCheck); !ok || c.kubectl == nil {
		t.Errorf("the registry must hold %s with kube.Region.Run", metadataBlockedID)
	}
}

// TestMetadataBlockedCheck_Chart: the deny of the chart names the label and
// the address that the check dials.
func TestMetadataBlockedCheck_Chart(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "platform", "network-policies", "chart", "templates", "baseline-deny-metadata.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"      - key: " + MetadataAccessLabel, "          - \"true\"", "        - " + metadataAddress + "/32"} {
		if strings.Count(string(b), "\n"+line+"\n") != 1 {
			t.Errorf("the deny of the chart lacks the line %q once", line)
		}
	}
}
