package modelcontract

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeterEntry(t *testing.T) {
	const ring = `{"requestId":"mc-1","organization":"conformance","alias":"conformance-echo","stream":true,"status":"ok","published":true,
		"event":{"subject":"conformance","data":{"organization":"conformance","resource":{"id":"conformance-echo"},
		"measures":[{"meter":"llm.tokens.input","quantity":30},{"meter":"llm.tokens.output","quantity":2},{"meter":"llm.requests","quantity":1}]}}}`
	meter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/events/mc-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, ring)
	}))
	defer meter.Close()
	ctx, zero := context.Background(), int64(0)
	if _, found, err := ReadEntry(ctx, meter.URL, "mc-2"); found || err != nil {
		t.Fatalf("an unknown request: found %v, %v", found, err)
	}
	e, found, err := ReadEntry(ctx, meter.URL, "mc-1")
	if !found || err != nil {
		t.Fatalf("ReadEntry: found %v, %v", found, err)
	}
	u := Usage{Prompt: 30, Completion: 2, Total: 32}
	u.Details.Cached = &zero
	for _, tc := range []struct {
		name   string
		e      Entry
		stream bool
		u      Usage
		want   string
	}{
		{"the entry of the call", e, true, u, ""},
		{"an unstreamed call", e, false, u, "4: the entry has status"},
		{"other token counts", e, true, Usage{Prompt: 31, Completion: 2}, "4: the event measures"},
		{"another Organization in the event", func() Entry { x := e; x.Event.Data.Organization = "someone-else"; return x }(), true, u, "3: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.e.Metered(ConformanceAlias, tc.stream, tc.u); (tc.want == "") != (err == nil) || err != nil && !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("Metered = %v, want %q", err, tc.want)
			}
		})
	}
}
