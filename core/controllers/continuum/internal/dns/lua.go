// Package dns synthesizes PowerDNS lua-record bodies for the
// continuum-controller's switchover sequence (slice K-Cont-2 of
// EPIC-6 #1101).
//
// Per docs/MULTI-REGION-DNS.md, every Application with placement:
// active-hotstandby has its public hostnames published as PowerDNS
// lua-records. The body of each record is a string-encoded Lua
// expression that PowerDNS evaluates at query time:
//
//	ifurlup("https://probe-fsn.<host>", {{"<fsn-A>", "<fsn-B>"}}, {selector="random", timeout=2})
//
// On switchover from `fsn` to `hel`, the controller flips:
//
//   - the probe URL host: probe-fsn.<host> → probe-hel.<host>
//   - the IP set order: primary IPs are listed FIRST so `pickfirst`
//     (or `pickclosest`-as-tiebreaker) prefers them.
//
// The synthesizer is a pure function: it takes the Application
// CR (Unstructured), the new primary region, and a SynthParams with
// per-region IP lists + healthcheck intervals, and returns a single
// PATCH body suitable for posting via the PDM /v1/commit endpoint.
//
// Synthesizer output is byte-stable across calls with the same input
// — the controller's idempotency check (re-emit only on diff) relies
// on this so a steady-state reconcile is a no-op DNS write.
package dns

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SynthParams holds the per-call inputs to Synthesize. Caller
// (controller switchover sequencer) populates from the Continuum
// CR's spec.luaRecord + per-region IP map.
type SynthParams struct {
	// PrimaryRegion is the host-cluster name that should be probed
	// first. Used to pick the probe URL host (probe-<region>.<host>).
	PrimaryRegion string

	// RegionToIPs maps host-cluster name → list of LB IPv4 addresses
	// reachable from public DNS. The synthesizer orders entries with
	// PrimaryRegion first, then the rest sorted alphabetically (for
	// byte-stable output).
	RegionToIPs map[string][]string

	// Selector is the lua-record selector. Default "ifurlup" — the
	// CRD's spec.luaRecord.selector enum:
	//   ifurlup       (health-checked failover)
	//   pickfirst     (no health check; ordered preference)
	//   pickclosest   (geo-affinity)
	//   pickwhashed   (consistent-hash weighted)
	Selector string

	// HealthCheckURL is the absolute HTTPS URL the lua-record probe
	// hits. Per the CRD: spec.luaRecord.healthCheck.url. The
	// synthesizer rewrites the host portion to probe-<primary>.<rest>
	// at switchover time.
	HealthCheckURL string

	// HealthCheckInterval (seconds) and HealthCheckTimeout (seconds)
	// drive the {selector=..., timeout=..., interval=...} options
	// table.
	HealthCheckIntervalSeconds int
	HealthCheckTimeoutSeconds  int

	// Hostnames is the list of public hostnames the Application
	// publishes (taken from the Application's HTTPRoute /
	// spec.routes[].hostnames). Each one gets its own LUA record.
	Hostnames []string

	// TTL is the DNS record TTL (seconds). Default 30 — short for
	// fast convergence at switchover.
	TTL int
}

// Defaults applies missing-field defaults. Returns a copy.
func (p SynthParams) Defaults() SynthParams {
	out := p
	if out.Selector == "" {
		out.Selector = "ifurlup"
	}
	if out.HealthCheckIntervalSeconds <= 0 {
		out.HealthCheckIntervalSeconds = 5
	}
	if out.HealthCheckTimeoutSeconds <= 0 {
		out.HealthCheckTimeoutSeconds = 2
	}
	if out.TTL <= 0 {
		out.TTL = 30
	}
	return out
}

// Record is one synthesized lua-record. The PDM client serializes a
// list of these into the /v1/commit request body.
type Record struct {
	// Hostname is the FQDN owner-name for the record (e.g.
	// "api.example.com").
	Hostname string `json:"hostname"`

	// LuaBody is the string-encoded Lua expression PowerDNS will
	// evaluate. Example:
	//   ifurlup("https://probe-fsn.example.com/healthz", {{"5.1.2.3", "5.1.2.4"}}, {selector="pickfirst", timeout=2})
	LuaBody string `json:"luaBody"`

	// TTL is the DNS record TTL (seconds).
	TTL int `json:"ttl"`

	// PrimaryRegion is the region that's PRIMARY at the time of
	// emission. Stamped on the audit event so observers can correlate
	// "the LUA record was rewritten when fsn→hel happened".
	PrimaryRegion string `json:"primaryRegion"`
}

// Synthesize generates lua-record bodies for the Application's
// HTTPRoute hostnames pointing at the new primary's region.
//
// Returns one Record per hostname in params.Hostnames. The output is
// stable: the same inputs always yield byte-identical output (used by
// the idempotency check in the switchover sequencer).
//
// For active-hotstandby placement, the canonical lua-record idiom is
// `ifurlup(...)` with `selector=pickfirst` (per
// docs/MULTI-REGION-DNS.md §3.2 table) — the primary IPs come first
// and are returned exclusively while the probe says they're healthy;
// when the probe fails, PowerDNS falls back to the standby IPs.
func Synthesize(app *unstructured.Unstructured, params SynthParams) ([]Record, error) {
	if app == nil && len(params.Hostnames) == 0 {
		return nil, errors.New("dns.Synthesize: nil Application AND no params.Hostnames")
	}
	if params.PrimaryRegion == "" {
		return nil, errors.New("dns.Synthesize: PrimaryRegion is required")
	}
	if len(params.RegionToIPs) == 0 {
		return nil, errors.New("dns.Synthesize: RegionToIPs is empty")
	}
	if _, ok := params.RegionToIPs[params.PrimaryRegion]; !ok {
		return nil, fmt.Errorf("dns.Synthesize: PrimaryRegion %q not present in RegionToIPs", params.PrimaryRegion)
	}
	params = params.Defaults()

	hostnames := params.Hostnames
	if len(hostnames) == 0 {
		// Caller didn't pass hostnames; try to extract from
		// Application spec. We deliberately don't make this fatal
		// (caller-supplied list wins) so tests can exercise the
		// synthesizer without a fully-populated Application object.
		hostnames = extractHostnamesFromApplication(app)
	}
	if len(hostnames) == 0 {
		return nil, errors.New("dns.Synthesize: no hostnames available")
	}

	probeURL, err := rewriteProbeURL(params.HealthCheckURL, regionLabel(params.PrimaryRegion))
	if err != nil {
		return nil, fmt.Errorf("rewrite probe URL: %w", err)
	}

	primaryIPs := append([]string(nil), params.RegionToIPs[params.PrimaryRegion]...)
	standbyIPs := orderedStandbyIPs(params.RegionToIPs, params.PrimaryRegion)

	out := make([]Record, 0, len(hostnames))
	for _, host := range hostnames {
		body, err := buildLuaBody(probeURL, params.Selector, primaryIPs, standbyIPs, params.HealthCheckIntervalSeconds, params.HealthCheckTimeoutSeconds)
		if err != nil {
			return nil, fmt.Errorf("buildLuaBody for %q: %w", host, err)
		}
		out = append(out, Record{
			Hostname:      host,
			LuaBody:       body,
			TTL:           params.TTL,
			PrimaryRegion: params.PrimaryRegion,
		})
	}
	return out, nil
}

// rewriteProbeURL replaces the host's leading label (`probe-<x>`)
// with `probe-<primary>`. Inputs without that prefix get the prefix
// prepended to their first label.
//
// Examples:
//
//	rewriteProbeURL("https://probe-fsn.example.com/healthz", "hel")
//	  → "https://probe-hel.example.com/healthz"
//
//	rewriteProbeURL("https://api.example.com/healthz", "fsn")
//	  → "https://probe-fsn.api.example.com/healthz"
func rewriteProbeURL(url, region string) (string, error) {
	if url == "" {
		return "", errors.New("HealthCheckURL is required")
	}
	if region == "" {
		return "", errors.New("region is required")
	}

	// Split scheme from rest.
	scheme := ""
	rest := url
	for _, sep := range []string{"://"} {
		if idx := indexOf(url, sep); idx > 0 {
			scheme = url[:idx+len(sep)]
			rest = url[idx+len(sep):]
			break
		}
	}

	// Split host from path.
	host := rest
	pathPart := ""
	if idx := indexOf(rest, "/"); idx > 0 {
		host = rest[:idx]
		pathPart = rest[idx:]
	}

	// Strip port if present (we add it back later).
	port := ""
	if idx := indexOf(host, ":"); idx > 0 {
		port = host[idx:]
		host = host[:idx]
	}

	// First label.
	firstDot := indexOf(host, ".")
	first := host
	tail := ""
	if firstDot > 0 {
		first = host[:firstDot]
		tail = host[firstDot:]
	}

	// Replace probe-<x> with probe-<region>; otherwise prepend.
	newFirst := "probe-" + region
	if hasPrefix(first, "probe-") {
		first = newFirst
	} else {
		// `api` → `probe-fsn.api`
		first = newFirst + "." + first
	}
	return scheme + first + tail + port + pathPart, nil
}

// orderedStandbyIPs returns all standby region IPs in deterministic
// order (regions sorted alphabetically excluding the primary). Within
// a region the IP order from RegionToIPs is preserved (caller is
// responsible for the per-region ordering).
func orderedStandbyIPs(m map[string][]string, primary string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == primary {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		out = append(out, m[k]...)
	}
	return out
}

// buildLuaBody constructs the Lua expression. Per
// docs/MULTI-REGION-DNS.md §3.2 active-hotstandby uses:
//
//	ifurlup("<probeURL>", {{<primary>...}, {<standby>...}}, {selector="pickfirst", timeout=N, interval=M})
//
// The double-curly is intentional Lua syntax: the second arg is a
// LIST OF GROUPS — each group has the IPs that share a probe target.
// `selector=pickfirst` returns the first healthy GROUP (so when
// primary's group is healthy, you only get primary's IPs).
func buildLuaBody(probeURL, selector string, primary, standby []string, intervalSec, timeoutSec int) (string, error) {
	if len(primary) == 0 {
		return "", errors.New("primary IPs empty")
	}
	var b bytes.Buffer
	b.WriteString(selector)
	b.WriteString(`("`)
	b.WriteString(probeURL)
	b.WriteString(`", {`)
	// First group: primary IPs.
	b.WriteString("{")
	for i, ip := range primary {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"`)
		b.WriteString(ip)
		b.WriteString(`"`)
	}
	b.WriteString("}")
	// Second group: standby IPs (omit if none — single-region
	// degenerate case).
	if len(standby) > 0 {
		b.WriteString(", {")
		for i, ip := range standby {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(`"`)
			b.WriteString(ip)
			b.WriteString(`"`)
		}
		b.WriteString("}")
	}
	b.WriteString("}, {selector=\"pickfirst\", interval=")
	b.WriteString(fmt.Sprintf("%d", intervalSec))
	b.WriteString(", timeout=")
	b.WriteString(fmt.Sprintf("%d", timeoutSec))
	b.WriteString("})")
	return b.String(), nil
}

// extractHostnamesFromApplication walks the Application CR and
// returns the union of spec.routes[].hostnames + .status.hostnames.
// Used as a fallback when SynthParams.Hostnames is empty.
func extractHostnamesFromApplication(app *unstructured.Unstructured) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	if routes, ok, _ := unstructured.NestedSlice(app.Object, "spec", "routes"); ok {
		for _, r := range routes {
			rm, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			hns, _, _ := unstructured.NestedStringSlice(rm, "hostnames")
			for _, h := range hns {
				add(h)
			}
			if h, _, _ := unstructured.NestedString(rm, "hostname"); h != "" {
				add(h)
			}
		}
	}
	hns, _, _ := unstructured.NestedStringSlice(app.Object, "status", "hostnames")
	for _, h := range hns {
		add(h)
	}
	sort.Strings(out)
	return out
}

// MarshalRecords returns a deterministic JSON encoding of records,
// suitable for the PDM /v1/commit body. Used by the PDM client.
func MarshalRecords(records []Record) ([]byte, error) {
	// Sort by hostname for byte-stability.
	cp := append([]Record(nil), records...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Hostname < cp[j].Hostname })
	return json.Marshal(struct {
		Records []Record `json:"records"`
	}{Records: cp})
}

// indexOf returns the first index of substr in s, or -1.
func indexOf(s, substr string) int {
	if substr == "" {
		return 0
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// hasPrefix is a stdlib-free check.
func hasPrefix(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}

// regionLabel extracts the short location-code from a NAMING-format
// region name. The CRD's pattern is `^[a-z]+-[a-z]+-[a-z]+-[a-z]+$`
// (e.g. `hz-fsn-rtz-prod`); the second token is the IATA-style site
// code that becomes the probe-host prefix per
// docs/MULTI-REGION-DNS.md.
//
// If the input doesn't match the expected 4-token shape, the function
// returns the full input verbatim so an operator using a non-canonical
// region name still gets a deterministic-but-uglier probe URL.
func regionLabel(region string) string {
	tokens := splitOn(region, '-')
	if len(tokens) >= 2 {
		return tokens[1]
	}
	return region
}

// splitOn is a tiny strings.Split substitute (avoiding the import to
// keep the package's stdlib dep set minimal).
func splitOn(s string, sep byte) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
