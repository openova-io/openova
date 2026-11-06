package handler

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

type recordingSink struct{ lines []jobs.LogLine }

func (r *recordingSink) AppendStepLog(_ string, l []jobs.LogLine) error {
	r.lines = append(r.lines, l...)
	return nil
}

func TestPumpLogLines_CarriesJobOutputWithLevels(t *testing.T) {
	in := "[gitea-mirror] cloning\n\n[gitea-mirror] WARN: slow push\nFATAL: push rejected\r\n"
	s := &recordingSink{}
	pumpLogLines(strings.NewReader(in), s, "gitea-mirror")
	if len(s.lines) != 3 {
		t.Fatalf("lines=%d, want 3 (blank skipped)", len(s.lines))
	}
	want := []string{jobs.LevelInfo, jobs.LevelWarn, jobs.LevelError}
	for i, l := range s.lines {
		if l.Level != want[i] {
			t.Errorf("line %d %q level=%s want %s", i, l.Message, l.Level, want[i])
		}
	}
	if s.lines[2].Message != "FATAL: push rejected" {
		t.Errorf("CR not trimmed: %q", s.lines[2].Message)
	}
}
