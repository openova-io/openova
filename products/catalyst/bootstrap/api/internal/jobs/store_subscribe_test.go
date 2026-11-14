package jobs

import (
	"context"
	"testing"
	"time"
)

// nextChange reads one Change or fails the test after a second.
func nextChange(t *testing.T, ch <-chan Change) Change {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed")
		}
		return c
	case <-time.After(time.Second):
		t.Fatal("no change delivered within a second")
	}
	return Change{}
}

// noChange fails the test when a Change is waiting on ch.
func noChange(t *testing.T, ch <-chan Change) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("unexpected change %+v", c)
	default:
	}
}

func TestSubscribeDeliversEachChangeKind(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := st.Subscribe("dep-1")
	defer cancel()

	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: "install-a", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	c := nextChange(t, ch)
	if c.Kind != JobUpserted || c.DeploymentID != "dep-1" || c.JobID != JobID("dep-1", "install-a") || c.ExecutionID != "" {
		t.Fatalf("upsert: got %+v", c)
	}
	seq := c.Seq

	exec, err := st.StartExecution("dep-1", "install-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c = nextChange(t, ch)
	if c.Kind != ExecutionStarted || c.JobID != JobID("dep-1", "install-a") || c.ExecutionID != exec.ID || c.Seq <= seq {
		t.Fatalf("start: got %+v", c)
	}
	seq = c.Seq

	if err := st.AppendLogLines("dep-1", exec.ID, []LogLine{{Message: "hello"}}); err != nil {
		t.Fatal(err)
	}
	c = nextChange(t, ch)
	if c.Kind != LogAppended || c.ExecutionID != exec.ID || c.JobID != "" || c.Seq <= seq {
		t.Fatalf("append: got %+v", c)
	}
	seq = c.Seq

	if err := st.FinishExecution("dep-1", exec.ID, StatusSucceeded, time.Now()); err != nil {
		t.Fatal(err)
	}
	c = nextChange(t, ch)
	if c.Kind != ExecutionFinished || c.JobID != JobID("dep-1", "install-a") || c.ExecutionID != exec.ID || c.Seq <= seq {
		t.Fatalf("finish: got %+v", c)
	}
	if c.At.IsZero() {
		t.Fatal("change has no time")
	}
	noChange(t, ch)
}

func TestSubscribeFiltersByDeployment(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one, cancelOne := st.Subscribe("dep-1")
	defer cancelOne()
	all, cancelAll := st.Subscribe("")
	defer cancelAll()

	if err := st.UpsertJob(Job{DeploymentID: "dep-2", JobName: "install-b"}); err != nil {
		t.Fatal(err)
	}
	if c := nextChange(t, all); c.DeploymentID != "dep-2" {
		t.Fatalf("all: got %+v", c)
	}
	noChange(t, one)
}

func TestSubscribeOverflowDeliversResync(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := st.Subscribe("dep-1")
	defer cancel()
	for i := 0; i <= SubscribeBuffer; i++ {
		st.publish(Change{DeploymentID: "dep-1", JobID: "dep-1:x", Kind: JobUpserted})
	}
	c := nextChange(t, ch)
	if c.Kind != Resync || c.DeploymentID != "dep-1" || c.JobID != "" || c.ExecutionID != "" {
		t.Fatalf("first change after overflow: got %+v, want a resync", c)
	}
	noChange(t, ch)
	st.publish(Change{DeploymentID: "dep-1", JobID: "dep-1:y", Kind: JobUpserted})
	if c := nextChange(t, ch); c.Kind != JobUpserted || c.JobID != "dep-1:y" {
		t.Fatalf("after the resync: got %+v", c)
	}
}

func TestUpsertJobUnchangedPublishesNothing(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	j := Job{DeploymentID: "dep-1", JobName: "install-a", Type: JobTypeInstall, Status: StatusRunning}
	if err := st.UpsertJob(j); err != nil {
		t.Fatal(err)
	}
	ch, cancel := st.Subscribe("dep-1")
	defer cancel()
	if err := st.UpsertJob(j); err != nil {
		t.Fatal(err)
	}
	noChange(t, ch)
	j.Status = StatusSucceeded
	if err := st.UpsertJob(j); err != nil {
		t.Fatal(err)
	}
	if c := nextChange(t, ch); c.Kind != JobUpserted {
		t.Fatalf("got %+v", c)
	}
}

func TestSubscribeCancelClosesChannel(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := st.Subscribe("dep-1")
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel still open after cancel")
	}
	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: "install-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestCoalesceOneSignalPerWindow(t *testing.T) {
	in := make(chan Change, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := Coalesce(ctx, in, 100*time.Millisecond)
	for i := 0; i < 10; i++ {
		in <- Change{Kind: JobUpserted}
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("no signal for a burst of changes")
	}
	select {
	case <-out:
		t.Fatal("a second signal for one burst")
	case <-time.After(300 * time.Millisecond):
	}
	in <- Change{Kind: JobUpserted}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("no signal for a later change")
	}
	close(in)
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("signal after the input closed")
		}
	case <-time.After(time.Second):
		t.Fatal("output not closed after the input closed")
	}
}
