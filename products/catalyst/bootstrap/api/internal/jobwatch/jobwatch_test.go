package jobwatch

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// jobObject is a batch/v1 Job with the given uid and conditions
// (type, status, lastTransitionTime).
func jobObject(uid string, conds ...[3]string) *unstructured.Unstructured {
	cs := make([]any, 0, len(conds))
	for _, c := range conds {
		m := map[string]any{"type": c[0], "status": c[1]}
		if c[2] != "" {
			m["lastTransitionTime"] = c[2]
		}
		cs = append(cs, m)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata":   map[string]any{"name": "wp-admin-user", "namespace": "org-a", "uid": uid},
		"status":     map[string]any{"startTime": "2026-11-12T10:00:00Z", "conditions": cs},
	}}
}

// closeCall is one call of the fake store.
type closeCall struct {
	deploymentID, uid, status string
	at                        time.Time
}

type fakeCloser struct{ calls []closeCall }

func (f *fakeCloser) CloseJobRun(deploymentID, jobUID, status string, finishedAt time.Time) (bool, error) {
	f.calls = append(f.calls, closeCall{deploymentID, jobUID, status, finishedAt})
	return true, nil
}

func TestOutcomeReadsOnlyTheTerminalCondition(t *testing.T) {
	evAt := time.Date(2026, 11, 12, 11, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		conds  [][3]string
		status string
		at     time.Time
		ok     bool
	}{
		{"complete", [][3]string{{"Complete", "True", "2026-11-12T10:05:00Z"}}, jobs.StatusSucceeded, time.Date(2026, 11, 12, 10, 5, 0, 0, time.UTC), true},
		{"success criteria met", [][3]string{{"SuccessCriteriaMet", "True", "2026-11-12T10:04:00Z"}}, jobs.StatusSucceeded, time.Date(2026, 11, 12, 10, 4, 0, 0, time.UTC), true},
		{"failed", [][3]string{{"Failed", "True", "2026-11-12T10:06:00Z"}}, jobs.StatusFailed, time.Date(2026, 11, 12, 10, 6, 0, 0, time.UTC), true},
		{"no time on the condition", [][3]string{{"Complete", "True", ""}}, jobs.StatusSucceeded, evAt, true},
		{"no condition", nil, "", time.Time{}, false},
		{"complete false", [][3]string{{"Complete", "False", "2026-11-12T10:05:00Z"}}, "", time.Time{}, false},
		{"suspended", [][3]string{{"Suspended", "True", "2026-11-12T10:05:00Z"}}, "", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, at, ok := Outcome(k8scache.Event{Kind: "job", Object: jobObject("uid-a", c.conds...), At: evAt})
			if status != c.status || !at.Equal(c.at) || ok != c.ok {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", status, at, ok, c.status, c.at, c.ok)
			}
		})
	}
}

func TestHandleClosesFromADeletedJobByUID(t *testing.T) {
	f := &fakeCloser{}
	ev := k8scache.Event{Cluster: "dep-1", Kind: "job", Type: k8scache.EventDeleted,
		Object: jobObject("uid-a", [3]string{"Complete", "True", "2026-11-12T10:05:00Z"})}
	if !Handle(ev, f, nil) {
		t.Fatal("no run closed")
	}
	want := closeCall{"dep-1", "uid-a", jobs.StatusSucceeded, time.Date(2026, 11, 12, 10, 5, 0, 0, time.UTC)}
	if len(f.calls) != 1 || f.calls[0].deploymentID != want.deploymentID || f.calls[0].uid != want.uid ||
		f.calls[0].status != want.status || !f.calls[0].at.Equal(want.at) {
		t.Fatalf("calls %+v, want [%+v]", f.calls, want)
	}
}

func TestHandleGivesNoVerdictWithoutATerminalCondition(t *testing.T) {
	f := &fakeCloser{}
	for _, ev := range []k8scache.Event{
		{Cluster: "dep-1", Kind: "job", Type: k8scache.EventDeleted, Object: jobObject("uid-a")},
		{Cluster: "dep-1", Kind: "job", Type: k8scache.EventModified, Object: jobObject("uid-a", [3]string{"Complete", "False", ""})},
		{Cluster: "dep-1", Kind: "job", Type: k8scache.EventDeleted, Object: jobObject("", [3]string{"Complete", "True", ""})},
		{Cluster: "dep-1", Kind: "cronjob", Type: k8scache.EventDeleted, Object: jobObject("uid-a", [3]string{"Complete", "True", ""})},
		{Cluster: "dep-1", Kind: "job", Type: k8scache.EventDeleted},
	} {
		if Handle(ev, f, nil) {
			t.Fatalf("event %+v closed a run", ev)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %+v, want none", f.calls)
	}
}

// fakeSource hands out one channel.
type fakeSource struct {
	ch    chan k8scache.Event
	kinds map[string]struct{}
}

func (s *fakeSource) Subscribe(_ string, kinds map[string]struct{}) (<-chan k8scache.Event, func()) {
	s.kinds = kinds
	return s.ch, func() {}
}

func TestRunClosesARunInTheStore(t *testing.T) {
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if _, _, err := jobs.NewBridge(st, "dep-1").SeedReconcilerObservations([]jobs.ReconcilerObservation{{
		Kind: jobs.ObsKindTask, Name: "wp-admin-user", Namespace: "org-a", Status: jobs.HelmStateInstalling,
		Executions: []jobs.ReconcilerExecutionObservation{{Name: "wp-admin-user", Status: jobs.HelmStateInstalling, StartedAt: start, UID: "uid-a"}},
	}}); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{ch: make(chan k8scache.Event, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(context.Background(), src, st, nil)
	}()
	src.ch <- k8scache.Event{Cluster: "dep-1", Kind: "job", Type: k8scache.EventDeleted,
		Object: jobObject("uid-a", [3]string{"Failed", "True", "2026-11-12T10:03:00Z"})}
	close(src.ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its channel closed")
	}
	if _, ok := src.kinds["job"]; !ok || len(src.kinds) != 1 {
		t.Fatalf("subscribed to %v, want only job", src.kinds)
	}
	j, execs, err := st.GetJob("dep-1", jobs.JobID("dep-1", jobs.TaskJobPrefix+"wp-admin-user"))
	if err != nil {
		t.Fatal(err)
	}
	if len(execs) != 1 || execs[0].Status != jobs.StatusFailed || j.Status != jobs.StatusFailed {
		t.Fatalf("leaf %q runs %+v", j.Status, execs)
	}
}
