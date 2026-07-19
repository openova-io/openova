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

// TestDefaultConsoleReachable_RootSPA404HandoverFallback pins the #5253
// false-negative fix (hw276): a HEALTHY console whose bare SPA root answers
// 404 must still count reachable because the catalyst-api-registered
// /auth/handover route answers (302 to the SPA error page → 200 after
// redirect-following). The hw218 no-vhost envoy shape — 404 on EVERY path —
// stays unreachable (pinned by the status-bar table above), so the #4706
// false-green bar is intact.
func TestDefaultConsoleReachable_RootSPA404HandoverFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/handover", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/auth/handover-error?reason=missing_token", http.StatusFound)
	})
	mux.HandleFunc("/auth/handover-error", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // the hw276 SPA root
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	if err := probeReachableForTest(srv.URL); err != nil {
		t.Fatalf("healthy console with a 404 SPA root must be reachable via the /auth/handover fallback (#5253), got %v", err)
	}
}

// TestDefaultConsoleReachable_RedirectFinalStatusDecides pins the #5253
// redirect contract: the reachability decision is made on the FINAL status of
// the redirect chain — a 2xx-after-redirect is a healthy silent-SSO front
// door; a redirect landing on a 4xx is not.
func TestDefaultConsoleReachable_RedirectFinalStatusDecides(t *testing.T) {
	t.Run("2xx after redirect -> reachable", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/landed", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/landed", http.StatusFound)
		})
		srv := httptest.NewTLSServer(mux)
		defer srv.Close()
		if err := probeReachableForTest(srv.URL); err != nil {
			t.Fatalf("302 -> 200 must be reachable, got %v", err)
		}
	})

	t.Run("4xx after redirect on every path -> unreachable", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/dead", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dead", http.StatusFound)
		})
		srv := httptest.NewTLSServer(mux)
		defer srv.Close()
		if err := probeReachableForTest(srv.URL); err == nil {
			t.Fatalf("a redirect chain terminating on 404 for every probed path must stay unreachable")
		}
	})
}
