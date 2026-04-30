// Tests for the helm-controller log tailer line parser.
//
// The tailer's run() loop is hard to test without a real Pod (it
// owns the kubernetes.Interface log stream lifecycle), but the
// pumpLines() method is split out specifically so we can drive it
// against an in-memory io.Reader and prove the line→Event mapping
// is correct.
package helmwatch

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPumpLines_ExtractsBPNameAndEmitsComponentLog(t *testing.T) {
	rec := &recorder{}
	tailer := newLogTailer(nil, rec.emit, time.Now)

	input := strings.Join([]string{
		// Flat-string structured JSON (legacy)
		`{"level":"info","ts":"2026-04-29T10:00:00Z","logger":"controllers.HelmRelease","msg":"running install","helmrelease":"flux-system/bp-cilium"}`,
		// Flat-string structured JSON, capitalised key (legacy)
		`{"level":"error","ts":"2026-04-29T10:00:05Z","msg":"chart pull failed","HelmRelease":"flux-system/bp-cert-manager"}`,
		// Leader-election noise — no bp- token, must be skipped.
		`{"level":"info","msg":"leader election lost"}`,
		// logr/klog text shape (legacy)
		`level=warn msg="reconcile took too long" helmrelease="flux-system/bp-keycloak"`,
		// Real flux v2.4 NESTED-OBJECT shape — the production format
		// (regression for #305 — was silently dropped before the
		// alternation was added).
		`{"level":"info","ts":"2026-04-30T18:37:49.961Z","msg":"dependencies do not meet ready condition","HelmRelease":{"name":"bp-mimir","namespace":"flux-system"},"name":"bp-mimir","namespace":"flux-system"}`,
		`{"level":"error","ts":"2026-04-30T18:37:49.962Z","msg":"chart pull error","HelmRelease":{"name":"bp-seaweedfs","namespace":"flux-system"}}`,
	}, "\n") + "\n"

	if err := tailer.pumpLines(context.Background(), strings.NewReader(input)); err != nil {
		t.Fatalf("pumpLines: %v", err)
	}

	events := rec.snapshot()
	if got, want := len(events), 5; got != want {
		t.Fatalf("expected %d events (5 with bp-*, 1 leader-election skipped), got %d:\n%+v", want, got, events)
	}

	// Component derivation + level classification.
	wantByComponent := map[string]struct {
		level string
		state string
	}{
		"cilium":       {level: "info", state: ""},
		"cert-manager": {level: "error", state: ""},
		"keycloak":     {level: "warn", state: ""},
		"mimir":        {level: "info", state: ""},
		"seaweedfs":    {level: "error", state: ""},
	}
	for _, ev := range events {
		if ev.Phase != PhaseComponentLog {
			t.Errorf("expected Phase=%q, got %q", PhaseComponentLog, ev.Phase)
		}
		want, ok := wantByComponent[ev.Component]
		if !ok {
			t.Errorf("unexpected component in event: %q", ev.Component)
			continue
		}
		if ev.Level != want.level {
			t.Errorf("component %q: level=%q, want %q (line=%q)", ev.Component, ev.Level, want.level, ev.Message)
		}
		if ev.State != want.state {
			t.Errorf("component %q: State=%q, want empty (component-log carries log level not state)", ev.Component, ev.State)
		}
		if ev.Message == "" {
			t.Errorf("component %q: empty Message, want raw log line", ev.Component)
		}
	}
}

func TestPumpLines_ContextCancelStopsScan(t *testing.T) {
	rec := &recorder{}
	tailer := newLogTailer(nil, rec.emit, time.Now)

	// Reader that yields one line then blocks forever, simulating a
	// follow=true log stream against a quiet Pod. Context cancel must
	// release pumpLines.
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte("level=info msg=startup helmrelease=\"flux-system/bp-flux\"\n"))
		// keep the pipe open
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	defer w.Close()

	done := make(chan error, 1)
	go func() { done <- tailer.pumpLines(ctx, r) }()

	select {
	case err := <-done:
		if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			// Scanner.Err() returns nil on EOF / cancel — anything
			// else is unexpected.
			t.Logf("pumpLines returned %v (may be benign)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("pumpLines did not return after context cancel")
	}

	events := rec.snapshot()
	// We may get 0 or 1 event depending on whether the scanner read
	// the first line before ctx cancel. Both are correct. The hard
	// requirement is that pumpLines RETURNED.
	if len(events) > 1 {
		t.Errorf("expected at most 1 event before cancel, got %d: %+v", len(events), events)
	}
	// And that any event we did get was Phase=component-log for flux.
	for _, ev := range events {
		if ev.Phase != PhaseComponentLog || ev.Component != "flux" {
			t.Errorf("unexpected event before cancel: %+v", ev)
		}
	}
}

// TestLevelFromLogLine — pure helper coverage so a future change to
// helm-controller's log format shows up as a test diff.
func TestLevelFromLogLine(t *testing.T) {
	cases := map[string]string{
		`level=info msg=hello`:                                "info",
		`level=warn msg=slow`:                                 "warn",
		`level=error msg=failed`:                              "error",
		`{"level":"error","msg":"chart load failed"}`:         "error",
		`{"level":"warn","msg":"retry"}`:                      "warn",
		`{"level":"info","msg":"ok"}`:                         "info",
		`some legacy plain line with no level`:                "info",
	}
	for in, want := range cases {
		got := levelFromLogLine(in)
		if got != want {
			t.Errorf("levelFromLogLine(%q) = %q, want %q", in, got, want)
		}
	}
}

