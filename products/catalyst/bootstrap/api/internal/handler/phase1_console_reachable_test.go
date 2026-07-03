package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDefaultConsoleReachable_StatusBar (#4706) pins the tightened < 400 bar:
// the console front door must SERVE (2xx/3xx), not merely answer. hw218 proved
// a 404 (envoy up, no route — the broken console) was mislabelled reachable
// under the old < 500 bar; this locks it as UNREACHABLE.
func TestDefaultConsoleReachable_StatusBar(t *testing.T) {
	cases := []struct {
		code      int
		reachable bool
	}{
		{200, true},  // SSR landing, signed-in
		{302, true},  // redirect to Keycloak login (silent SSO)
		{404, false}, // hw218: envoy up, no vhost/route to console → NOT ready
		{401, false}, // misconfigured front door
		{403, false},
		{502, false}, // backend down
		{503, false},
	}
	for _, c := range cases {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
		}))
		// Point the probe at the test server by overriding the target host: the
		// prod func builds https://console.<fqdn>/, so run it against a stub that
		// mirrors its logic on the test server URL.
		err := probeReachableForTest(srv.URL)
		srv.Close()
		got := err == nil
		if got != c.reachable {
			t.Errorf("HTTP %d: reachable=%v, want %v (err=%v)", c.code, got, c.reachable, err)
		}
	}
}

func TestDefaultConsoleReachable_EmptyFQDN(t *testing.T) {
	if err := defaultConsoleReachable("   "); err == nil || !strings.Contains(err.Error(), "empty sovereign FQDN") {
		t.Fatalf("empty FQDN must error, got %v", err)
	}
}
