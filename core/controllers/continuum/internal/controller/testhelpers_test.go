// Test helpers for the controller package.

package controller

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pdm"
)

// fakeDoer satisfies pdm.Doer for tests; always returns 200.
type fakeDoer struct {
	calls int
	last  *http.Request
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	f.last = req
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
	}, nil
}

func newPDMClientWithFake(t *testing.T, baseURL string) *pdm.Client {
	t.Helper()
	c := pdm.New(baseURL, "")
	c.HTTP = &fakeDoer{}
	return c
}

// allAuditTypes returns a slice of recorded audit types from the
// events.Recorder. Test-helper for failure messages.
func allAuditTypes(r interface {
	Events() []events.Event
}) []string {
	out := []string{}
	for _, e := range r.Events() {
		out = append(out, e.Type+":"+e.Message)
	}
	return out
}
