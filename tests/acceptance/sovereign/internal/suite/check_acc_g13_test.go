package suite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// g13Cluster is a cluster with the access controller for check ACC-G13. It
// keeps the grants the check applies and answers their phase from the check's
// clock by the rules of the controller, unless defect names what it does wrong.
type g13Cluster struct {
	now     time.Time
	defect  string
	applied time.Time
	grants  map[string]string // name -> manifest
	calls   []string
}

var (
	g13Name  = regexp.MustCompile(`(?m)^  name: (\S+)$`)
	g13Field = func(key string) *regexp.Regexp { return regexp.MustCompile(key + `: "([^"]+)"`) }
)

func (f *g13Cluster) kubectl(r kube.Region, _ context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	f.calls = append(f.calls, r.ID+": "+strings.Join(args, " "))
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	switch {
	case args[0] == "--as="+accG13Flux && args[1] == "apply":
		for _, doc := range strings.Split(in, "\n---\n") {
			switch {
			case strings.Contains(doc, "kind: AccessTarget"):
			case strings.Contains(doc, "- by: g13-subject@example.org") && f.defect != "self-approval accepted":
				return nil, errors.New(`The AccessGrant "g13-self" is invalid: spec: Invalid value: "object": ` + accG13SelfMsg)
			default:
				f.grants[g13Name.FindStringSubmatch(doc)[1]], f.applied = doc, f.now
			}
		}
		return nil, nil
	case args[0] == "create" && args[1] == "-f":
		if f.defect == "no writer lock" {
			return nil, nil
		}
		return nil, errors.New(`accessgrants.access.openova.io "g13-admin" is forbidden: ValidatingAdmissionPolicy 'access-kinds-writers' ` +
			`with binding 'access-kinds-writers' denied request: ` + accG13LockMsg)
	case args[0] == "get" && args[1] == "accesstarget":
		status := map[bool]string{true: "False", false: "True"}[f.defect == "target not ready"]
		return []byte(`{"status": {"conditions": [{"type": "Ready", "status": "` + status + `", "reason": "Present", "message": "m"}]}}`), nil
	case args[0] == "get" && args[1] == "accessgrants":
		items := []string{}
		for name, doc := range f.grants {
			items = append(items, fmt.Sprintf(`{"metadata": {"name": %q}, "status": {"phase": %q}}`, name, f.phase(doc)))
		}
		return []byte(`{"items": [` + strings.Join(items, ",") + `]}`), nil
	case args[0] == "get" && args[1] == "deployment":
		return []byte(`{"status": {"readyReplicas": 1}}`), nil
	case args[0] == "get" && args[1] == "pods":
		if f.defect == "a controller pod in region b" && r.ID == "region-b" {
			return []byte(`{"items": [{"metadata": {"name": "access-controller-7d9f"}}]}`), nil
		}
		return []byte(`{"items": []}`), nil
	}
	return nil, nil // create namespace, delete namespace
}

// phase is the phase the controller writes for a grant at the cluster's time.
func (f *g13Cluster) phase(doc string) string {
	from, _ := time.Parse(time.RFC3339, g13Field("notBefore").FindStringSubmatch(doc)[1])
	to, _ := time.Parse(time.RFC3339, g13Field("notAfter").FindStringSubmatch(doc)[1])
	switch {
	case f.defect == "a slow controller" && f.now.Before(f.applied.Add(15*time.Second)):
		return ""
	case f.defect == "a late expiry" && !f.now.Before(to) && f.now.Before(to.Add(11*time.Second)):
		return "Active"
	case !f.now.Before(to):
		return "Expired"
	case !strings.Contains(doc, "approvals:"):
		return "Pending"
	case f.now.Before(from) && f.defect != "no Scheduled phase":
		return "Scheduled"
	}
	return "Active"
}

func TestAccG13Check(t *testing.T) {
	if _, ok := registry[accG13ID]; !ok || accG13ID != "ACC-G13" {
		t.Fatalf("check %s is not registered", accG13ID)
	}
	root := filepath.Join("..", "..", "..", "..", "..") // the fixtures of this repository
	for defect, want := range map[string]string{
		"":                             "",
		"target not ready":             "ACC-G13.1: the target is not Ready after 30s",
		"self-approval accepted":       "ACC-G13.2: grant g13-self was accepted",
		"no writer lock":               "ACC-G13.3: grant g13-admin was accepted",
		"a slow controller":            "ACC-G13.4: grant g13-active was Active 15 seconds after",
		"no Scheduled phase":           "ACC-G13.6: grant g13-scheduled went through [Active], want Scheduled and then Active",
		"a late expiry":                "ACC-G13.7: grant g13-expiring was Expired 1",
		"a controller pod in region b": "ACC-G13.8: region region-b, not the primary, runs pods of the controller: [access-controller-7d9f]",
	} {
		f := &g13Cluster{now: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC), defect: defect, grants: map[string]string{}}
		c := accG13Check{kubectl: f.kubectl, now: func() time.Time { return f.now },
			sleep: func(_ context.Context, d time.Duration) error { f.now = f.now.Add(d); return nil }}
		r := runCheck(t, "none", c, &CheckContext{RepoRoot: root})
		got, failed := r.run.Checks[0], []string{}
		for _, s := range r.run.Steps {
			if s.Status == Fail {
				failed = append(failed, s.ID+": "+s.Error)
			}
		}
		calls := strings.Join(f.calls, "\n")
		switch {
		case want == "" && (got.Status != Pass || len(r.run.Steps) != 8):
			t.Errorf("a healthy cluster: want PASS with 8 steps, got %s with %d steps: %v", got.Status, len(r.run.Steps), failed)
		case want != "" && (got.Status != Fail || !slices.ContainsFunc(failed, func(s string) bool { return strings.HasPrefix(s, want) })):
			t.Errorf("%s: want a failed step %q, got %s: %v", defect, want, got.Status, failed)
		case !strings.Contains(calls, "region-a: delete namespace acc-g13-"):
			t.Errorf("%s: the namespace of the check is not deleted: %s", defect, calls)
		}
		if defect == "" {
			// The windows the check gives: 60 seconds ahead for the scheduled grant, 90 seconds long for the expiring one.
			for name, w := range map[string][2]time.Duration{"g13-scheduled": {60 * time.Second, 10 * time.Minute}, "g13-expiring": {-5 * time.Second, 85 * time.Second}} {
				from, to := g13Field("notBefore").FindStringSubmatch(f.grants[name])[1], g13Field("notAfter").FindStringSubmatch(f.grants[name])[1]
				t0 := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
				if from != t0.Add(w[0]).Format(time.RFC3339) || to != t0.Add(w[1]).Format(time.RFC3339) {
					t.Errorf("grant %s has the window %s to %s", name, from, to)
				}
			}
			if !strings.Contains(calls, "--as="+accG13Flux+" apply --server-side") || len(f.grants) != 4 || slices.ContainsFunc(slices.Collect(maps.Values(f.grants)), func(d string) bool { return strings.Contains(d, "${") }) {
				t.Errorf("the four grants are not applied as the Flux account, each filled in: %s\n%v", calls, f.grants)
			}
		}
	}
}
