package orgtenantrepos

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRepointNamesEachOutcome(t *testing.T) {
	hangs := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", errors.New("git clone: signal: killed")
	}
	for name, tc := range map[string]struct {
		ctx      func() context.Context
		rerender func(context.Context) (string, error)
		level    string
		has      string
		named    bool
	}{
		"a remote that never answers": {context.Background, hangs, "warn", "within 50ms", true},
		"a failure":                   {context.Background, func(context.Context) (string, error) { return "", errors.New("push rejected") }, "warn", "push rejected", false},
		"a commit":                    {context.Background, func(context.Context) (string, error) { return "0123abcd", nil }, "info", "commit 0123abcd", false},
		"nothing to do":               {context.Background, func(context.Context) (string, error) { return "", nil }, "info", "need no re-render", false},
		"the engine stops (yield)": {func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, hangs, "warn", "re-render failed", false},
	} {
		t.Run(name, func(t *testing.T) {
			var logged bytes.Buffer
			start := time.Now()
			level, msg := Repoint(tc.ctx(), slog.New(slog.NewTextHandler(&logged, nil)), 50*time.Millisecond, tc.rerender)
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("Repoint took %s, want the deadline", took)
			}
			if level != tc.level || !strings.Contains(msg, tc.has) || strings.Contains(msg, RepointTimedOut) != tc.named {
				t.Errorf("level %q msg %q; want level %q, %q in it, named %s: %v", level, msg, tc.level, tc.has, RepointTimedOut, tc.named)
			}
			if !strings.Contains(logged.String(), tc.has) || !strings.Contains(logged.String(), "level="+strings.ToUpper(tc.level)) {
				t.Errorf("the outcome was not logged: %q", logged.String())
			}
		})
	}
}
