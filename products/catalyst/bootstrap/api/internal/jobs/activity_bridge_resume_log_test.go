package jobs

import (
	"testing"
	"time"
)

// A replayed in-flight step must NOT gain one placeholder line per /jobs
// read (hw307 2026-11-06: 20 identical "[in flight …]" lines and no real
// output), and the step's live output must land in the same Execution.
func TestActivityBridge_ResumeStepDoesNotDuplicateAndAppendStepLogLands(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	base := time.Date(2026, 11, 6, 5, 45, 0, 0, time.UTC)
	step := ActivityStep{Slug: "gitea-mirror", Order: 1}
	if err := ab.SeedSteps([]ActivityStep{step}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		fresh := NewActivityBridge(st, depID, GroupCutover, GroupCutoverDisplay)
		if err := fresh.SeedSteps([]ActivityStep{step}); err != nil {
			t.Fatal(err)
		}
		if err := fresh.ResumeStep(step, "[in flight — replayed]", base); err != nil {
			t.Fatal(err)
		}
	}
	if err := ab.AppendStepLog("gitea-mirror", []LogLine{{Timestamp: base, Level: LevelInfo, Message: "[gitea-mirror] pushing batch 13/14"}}); err != nil {
		t.Fatal(err)
	}
	job, execs, err := st.GetJob(depID, ActivityStepJobName(GroupCutover, "gitea-mirror"))
	if err != nil || len(execs) != 1 {
		t.Fatalf("GetJob: err=%v execs=%d, want exactly 1 execution", err, len(execs))
	}
	page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	var placeholders, output int
	for _, l := range page.Lines {
		switch l.Message {
		case "[in flight — replayed]":
			placeholders++
		case "[gitea-mirror] pushing batch 13/14":
			output++
		}
	}
	if placeholders != 1 || output != 1 {
		t.Fatalf("placeholders=%d output=%d (lines=%d), want 1 and 1", placeholders, output, len(page.Lines))
	}
}

// AppendStepLog on a step that never started is a no-op, not an error.
func TestActivityBridge_AppendStepLogWithoutExecutionIsNoop(t *testing.T) {
	_, ab, _ := newActivityFixture(t)
	if err := ab.AppendStepLog("never-started", []LogLine{{Message: "x"}}); err != nil {
		t.Fatalf("AppendStepLog: %v", err)
	}
}
