package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// UAT rows 90 + 234 — the per-Org app A-records point at the WRONG front door.
//
// ProvisionFreeSubdomain writes six A-records for an Org's pool subtree. Five of
// them (`*`, `wordpress`, `openclaw`, `mail`, `keycloak`) were written at
// ingressIPv4 -- the SHARED gateway -- and only `console` at consoleIPv4.
//
// The premise for that split is stated at organization_dns.go:164-167:
//
//	"App hosts (wordpress/openclaw/mail/keycloak + the per-Org wildcard) ride
//	 the SHARED gateway (ingressIPv4)."
//
// That premise is false on this platform, and two independent facts show it:
//
//  1. The ONLY writer of a per-Org wildcard listener is
//     core/controllers/organization/internal/controller/tenant_console_tls.go:307,
//     which builds WildcardHost = "*.<slug>.<parent>" and appends the
//     `console-https-<slug>` / `console-http-<slug>` listener pair to
//     consoleGatewayName() -- `cilium-gateway-console`. Nothing appends a
//     per-Org listener to the shared gateway, and the matching wildcard cert
//     (same file, the SAN block) is mounted only there. So an app host that
//     resolves to the shared gateway arrives with an SNI that gateway holds no
//     listener for, and the TLS connection is RESET before any HTTP status.
//
//  2. The platform already contains a SECOND writer of the very same `*`
//     record -- the org-controller at
//     core/controllers/organization/internal/controller/tenant_dns.go:182-192 --
//     and it writes BOTH `console` and `*` at consoleIP. Two writers emitting
//     the same RRset with different targets is an inconsistency on its face,
//     independent of which one reads better. The reconciler re-asserts, so `*`
//     converges to the console IP while the four app prefixes -- which the
//     reconciler never touches -- stay orphaned at the shared IP. Being
//     EXPLICIT records they then shadow the corrected wildcard, which is why
//     the fault survives reconciliation.
//
// Live corroboration on hw293 (dep a0077ba47e3720e5), Org `g7freea`, captured
// read-only shortly before that env was wiped:
//
//	mail.g7freea.omani.homes       -> 212.72.24.74 (shared)  curl exit 35, TLS reset
//	wordpress.g7freea.omani.homes  -> 212.72.24.74 (shared)  curl exit 35, TLS reset
//	console.g7freea.omani.homes    -> 212.72.24.49 (console) HTTP 200, ssl_verify 0
//
// and the discriminating experiment -- forcing the SAME SNI onto the console
// EIP -- returns 503 with a publicly-trusted cert, i.e. the listener, the
// wildcard cert and the HTTPRoute are all present and correct on the console
// gateway:
//
//	curl --resolve mail.g7freea.omani.homes:443:212.72.24.49  -> 503, ssl_verify 0
//
// 503 (route matched, no healthy upstream) versus exit 35 (no listener for this
// SNI) is what separates "wrong front door" from "broken app".
func TestProvisionFreeSubdomain_AppHostsTargetConsoleGateway_UAT90_234(t *testing.T) {
	const (
		ingressIP = "203.0.113.74" // shared gateway
		consoleIP = "203.0.113.49" // console gateway — where the listener lives
	)

	captured, srv := captureRRSets(t)
	defer srv.Close()

	p := DefaultOrganizationDNSProvisioner{
		PoolWriter: &PowerDNSWriter{
			BaseURL:    srv.URL,
			ServerID:   "localhost",
			APIKey:     "test-key",
			HTTPClient: srv.Client(),
		},
	}

	if err := p.ProvisionFreeSubdomain(context.Background(), "g7freea", "omani.homes", ingressIP, consoleIP); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}

	got := *captured

	// VACUITY CHECK — the writer really emitted the full prefix set. Without
	// this, an empty capture would make every assertion below pass on nothing.
	if len(got) != len(theFreeSubdomainPrefixes) {
		t.Fatalf("vacuity: expected %d RRsets (one per prefix), got %d: %+v",
			len(theFreeSubdomainPrefixes), len(theFreeSubdomainPrefixes), got)
	}

	// Every host in the per-Org pool subtree must land on the console gateway,
	// because that is the only gateway carrying a listener + cert for
	// `*.<slug>.<parent>`.
	for _, rr := range got {
		if len(rr.Records) != 1 {
			t.Fatalf("%s: expected exactly 1 record, got %d", rr.Name, len(rr.Records))
		}
		if content := rr.Records[0].Content; content != consoleIP {
			t.Errorf("%s targets %s (the SHARED gateway); the only per-Org listener for *.g7freea.omani.homes is on the CONSOLE gateway (%s), so this host resets at the TLS handshake",
				rr.Name, content, consoleIP)
		}
	}
}

// CONTROL 1 — shares the suspect property (same function, same prefix loop) but
// must stay GREEN both before and after. A single-gateway Sovereign passes
// consoleIPv4 == "" and every record must fall back to the ingress IP. A fix
// that hardcoded the console IP, or that dropped the fallback, would turn this
// red.
func TestProvisionFreeSubdomain_SingleGatewaySovereignStillFallsBack(t *testing.T) {
	const ingressIP = "203.0.113.74"

	captured, srv := captureRRSets(t)
	defer srv.Close()

	p := DefaultOrganizationDNSProvisioner{
		PoolWriter: &PowerDNSWriter{
			BaseURL: srv.URL, ServerID: "localhost", APIKey: "test-key", HTTPClient: srv.Client(),
		},
	}

	if err := p.ProvisionFreeSubdomain(context.Background(), "solo", "omani.rest", ingressIP, ""); err != nil {
		t.Fatalf("ProvisionFreeSubdomain: %v", err)
	}

	got := *captured
	if len(got) != len(theFreeSubdomainPrefixes) {
		t.Fatalf("vacuity: expected %d RRsets, got %d", len(theFreeSubdomainPrefixes), len(got))
	}
	for _, rr := range got {
		if content := rr.Records[0].Content; content != ingressIP {
			t.Errorf("%s: single-gateway Sovereign must fall back to the ingress IP %s, got %s", rr.Name, ingressIP, content)
		}
	}
}

// CONTROL 2 — the #4459 write/delete-drift guard. Both paths must keep walking
// the SAME prefix list, so a fix to the write path cannot silently orphan
// records on delete (write N, delete <N: the surplus survives the Org and
// shadows a re-prov's wildcard with a dead IP).
func TestProvisionAndDeprovision_WalkTheSamePrefixSet(t *testing.T) {
	const ingressIP = "203.0.113.74"
	const consoleIP = "203.0.113.49"

	provisioned, srv1 := captureRRSets(t)
	defer srv1.Close()
	p1 := DefaultOrganizationDNSProvisioner{PoolWriter: &PowerDNSWriter{
		BaseURL: srv1.URL, ServerID: "localhost", APIKey: "k", HTTPClient: srv1.Client(),
	}}
	if err := p1.ProvisionFreeSubdomain(context.Background(), "drift", "omani.trade", ingressIP, consoleIP); err != nil {
		t.Fatalf("provision: %v", err)
	}

	deprovisioned, srv2 := captureRRSets(t)
	defer srv2.Close()
	p2 := DefaultOrganizationDNSProvisioner{PoolWriter: &PowerDNSWriter{
		BaseURL: srv2.URL, ServerID: "localhost", APIKey: "k", HTTPClient: srv2.Client(),
	}}
	if err := p2.DeprovisionFreeSubdomain(context.Background(), "drift", "omani.trade"); err != nil {
		t.Fatalf("deprovision: %v", err)
	}

	names := func(rrs []pdnsRRSet) map[string]bool {
		m := map[string]bool{}
		for _, r := range rrs {
			m[r.Name] = true
		}
		return m
	}
	pn, dn := names(*provisioned), names(*deprovisioned)
	if len(pn) == 0 {
		t.Fatal("vacuity: provision emitted no RRsets")
	}
	for n := range pn {
		if !dn[n] {
			t.Errorf("#4459 drift: provision writes %s but deprovision never deletes it", n)
		}
	}
}

// captureRRSets stands up a PowerDNS-shaped test server that records the RRsets
// of the last PATCH it received.
func captureRRSets(t *testing.T) (*[]pdnsRRSet, *httptest.Server) {
	t.Helper()
	captured := &[]pdnsRRSet{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload struct {
			RRSets []pdnsRRSet `json:"rrsets"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unmarshal %q: %v", string(body), err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		*captured = payload.RRSets
		w.WriteHeader(http.StatusNoContent)
	}))
	return captured, srv
}
