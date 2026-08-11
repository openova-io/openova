// delivery_test.go — the THIRD bridge state, pinned.
//
// Every case asserts on a VALUE the shared contract produced, and every
// assertion is paired with a control that shares the suspect property and must
// answer the other way, so nothing here can pass by having widened a rule.
//
// Refs #5488 #6027 #6107.
package kubeconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// hw293DeliveredValue is the live artefact, reproduced byte-for-byte from
// `catalyst/cutover-secondary-kubeconfigs` key `me-east-215-b-1.yaml` on
// hw293 (dep a0077ba47e3720e5) as read 2026-08-11: 219 bytes, sha256 prefix
// 881231f95cd49646.
//
// It carries no secret. Its "credential" is the literal string `fake-token`,
// which is the whole point of the case: the document is credentialled in every
// sense the contract could check from the bytes, and it is still not a
// delivery. The trailing newline is absent in the live value, so the fixture
// is built by trimming one — see TestHw293DeliveredValue_MatchesLiveDigest,
// which fails if this fixture ever drifts from the measured artefact.
const hw293DeliveredValue = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
  name: c
contexts:
- name: c
  context:
    cluster: c
    user: c
current-context: c
users:
- name: c
  user:
    token: fake-token`

// hw293LiveBytes / hw293LiveDigest are what `kubectl get secret -o json` +
// base64-decode reported for that key. Pinning both is what makes this a
// regression fixture rather than a story about one.
const (
	hw293LiveBytes  = 219
	hw293LiveDigest = "881231f95cd49646"
)

// hw293DeclaredRegions is the live `catalyst-system/sovereign-fqdn` key
// `configuredRegions`, injected as CATALYST_CONFIGURED_REGIONS.
var hw293DeclaredRegions = []string{"hw-me-east-215-a-rtz-prod", "hw-me-east-215-b-rtz-prod"}

// genuineRegionBValue is the CONTROL. It shares every property that made the
// live value pass: same five sections, same resolvable current-context, same
// bearer-shaped credential, same brevity. The ONE difference is that its
// endpoint is region B's real apiserver — `212.72.24.25`, the address whose
// serving certificate names `…-me-east-215-b-cp1-…` — so an oracle can prove
// it. If the new rule were rejecting on document shape, on the token, or on
// terseness, this control would be refused too.
const genuineRegionBValue = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.25:6443
  name: c
contexts:
- name: c
  context:
    cluster: c
    user: c
current-context: c
users:
- name: c
  user:
    token: fake-token`

// provenEndpoints is an oracle that proves exactly the endpoints a real
// deployment would answer on. It stands in for catalyst-api's TCP probe.
func provenEndpoints(allowed ...string) func(string) bool {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	return func(endpoint string) bool { return set[endpoint] }
}

// TestHw293DeliveredValue_MatchesLiveDigest pins the fixture to the measured
// artefact. Without it every assertion below is about a document of this
// test's own invention.
func TestHw293DeliveredValue_MatchesLiveDigest(t *testing.T) {
	if got := len(hw293DeliveredValue); got != hw293LiveBytes {
		t.Fatalf("fixture drifted from the live Secret value: %d bytes, want %d", got, hw293LiveBytes)
	}
	sum := sha256.Sum256([]byte(hw293DeliveredValue))
	if got := hex.EncodeToString(sum[:])[:len(hw293LiveDigest)]; got != hw293LiveDigest {
		t.Fatalf("fixture sha256 prefix = %s, want %s (the live value)", got, hw293LiveDigest)
	}
}

// TestHw293DeliveredValue_PassesTheBytesContract states the defect in the
// contract's own terms: the document that left region B credential-less is
// USABLE. This is not a bug in Defects — it is the reason Defects alone could
// not be the whole answer, and if it ever starts failing here the premise of
// delivery.go has changed and its header must change with it.
func TestHw293DeliveredValue_PassesTheBytesContract(t *testing.T) {
	if d := Defects(hw293DeliveredValue); len(d) != 0 {
		t.Fatalf("premise broken: the live value now has bytes-level defects %v — delivery.go's rationale needs rewriting", d)
	}
	if !Usable(hw293DeliveredValue) {
		t.Fatal("premise broken: the live value is no longer Usable")
	}
	if got := Endpoint(hw293DeliveredValue); got != "https://212.72.24.6:6443" {
		t.Fatalf("Endpoint = %q, want the live value's server URL", got)
	}
	if got := EndpointHostPort(hw293DeliveredValue, "6443"); got != "212.72.24.6:6443" {
		t.Fatalf("EndpointHostPort = %q, want 212.72.24.6:6443", got)
	}
}

// TestDeliveryDefects_Hw293 is the RED case: the live value must be refused as
// a delivery, and the reason must name the endpoint proof rather than
// something vague.
func TestDeliveryDefects_Hw293(t *testing.T) {
	// The oracle proves only the two real regions, exactly as a live probe
	// would: 212.72.24.6 answers nothing (no ICMP, no :443, 12s timeout on
	// :6443, measured 2026-08-11).
	oracle := provenEndpoints("https://212.72.24.43:6443", "https://212.72.24.25:6443")

	got := DeliveryDefects(hw293DeliveredValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  oracle,
	})
	if len(got) == 0 {
		t.Fatal("the live hw293 value was accepted as a DELIVERY — a well-formed, credentialled document pointing at neither region is a mis-delivery, not a delivery")
	}
	if DescribeDefects(got) != DefectEndpointUnproven {
		t.Fatalf("defects = %v, want exactly [%s]: the region key IS declared and the bytes ARE usable, so the endpoint proof must be the sole discriminator",
			got, DefectEndpointUnproven)
	}

	// CONTROL — same everything, real endpoint. Must be a delivery.
	if got := DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  oracle,
	}); len(got) != 0 {
		t.Fatalf("the CONTROL — a genuinely usable kubeconfig at a declared region — was refused: %v", got)
	}
}

// TestDeliveryDefects_NoOracleIsNoVerdict pins the non-regression guarantee: a
// consumer that cannot probe gets exactly the bytes-only answer it got before
// this file existed. A package that invented a reachability verdict without an
// oracle would be reporting from absent evidence.
func TestDeliveryDefects_NoOracleIsNoVerdict(t *testing.T) {
	if got := DeliveryDefects(hw293DeliveredValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
	}); len(got) != 0 {
		t.Fatalf("with no oracle the verdict must match Defects (empty), got %v", got)
	}
	if got := DeliveryDefects(hw293DeliveredValue, Delivery{}); len(got) != 0 {
		t.Fatalf("a zero Delivery must behave exactly like Defects, got %v", got)
	}
}

// TestDeliveryDefects_BytesVerdictWins — an unusable document reports its
// SECTIONS, never the downstream consequences of being unusable. Burying
// "users" under "endpoint-absent" would make the operator chase the wrong
// thing.
func TestDeliveryDefects_BytesVerdictWins(t *testing.T) {
	const stub = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
  name: c`
	got := DescribeDefects(DeliveryDefects(stub, Delivery{
		RegionKey:       "nowhere-9",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  provenEndpoints(),
	}))
	const want = "contexts,current-context,users"
	if got != want {
		t.Fatalf("defects = %q, want %q — the bytes verdict must not be diluted by attribution findings", got, want)
	}
}

// TestDeliveryDefects_Attribution covers check 1 on its own, with the endpoint
// proof satisfied so attribution is the only thing under test.
func TestDeliveryDefects_Attribution(t *testing.T) {
	oracle := provenEndpoints("https://212.72.24.25:6443")

	// A key from a topology this Sovereign does not have.
	if got := DescribeDefects(DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:       "nbg1-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  oracle,
	})); got != DefectRegionUndeclared {
		t.Fatalf("defects = %q, want %q for a region key the deployment does not declare", got, DefectRegionUndeclared)
	}

	// CONTROL — the declared key attributes cleanly.
	if got := DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  oracle,
	}); len(got) != 0 {
		t.Fatalf("the declared region key was refused: %v", got)
	}

	// CONTROL — no witness means no verdict, never a worse one. A Sovereign
	// with an empty CATALYST_CONFIGURED_REGIONS behaves as it did before.
	if got := DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:      "nbg1-1",
		EndpointProven: oracle,
	}); len(got) != 0 {
		t.Fatalf("with no declared-region witness the attribution check must not fire, got %v", got)
	}
}

// TestRegionDeclared covers the token matcher directly, including the
// off-by-one neighbour that string containment would have accepted.
func TestRegionDeclared(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		declared []string
		want     bool
	}{
		{"live hw293 secondary", "me-east-215-b-1", hw293DeclaredRegions, true},
		{"live hw293 primary", "me-east-215-a-1", hw293DeclaredRegions, true},
		{"no ordinal suffix", "me-east-215-b", hw293DeclaredRegions, true},
		{"underscore separators", "me_east_215_b_1", hw293DeclaredRegions, true},
		{"uppercase", "ME-EAST-215-B-1", hw293DeclaredRegions, true},
		{"declared is the shorter run", "hw-me-east-215-b-rtz-prod-1", []string{"me-east-215-b"}, true},
		// The reason this is token containment and not strings.Contains: a
		// NEIGHBOURING region must not be accepted as this one.
		{"neighbouring region", "me-east-21-b-1", hw293DeclaredRegions, false},
		{"prefix of a declared token", "me-east-2", hw293DeclaredRegions, false},
		{"foreign provider region", "nbg1-1", hw293DeclaredRegions, false},
		{"empty key", "", hw293DeclaredRegions, false},
		// A bare number is a run inside the declared list ([215] sits in
		// [hw me east 215 b rtz prod]) and must NOT attribute on that alone.
		{"bare number does not attribute", "215", hw293DeclaredRegions, false},
		// The short-key escape hatch: a one-token provider region attributes
		// when it EQUALS the declared identifier, so Hetzner-shaped keys keep
		// working under the minimum-run rule.
		{"short provider key equal to declared", "fsn1-1", []string{"fsn1", "hel1"}, true},
		{"short provider key not declared", "nbg1-1", []string{"fsn1", "hel1"}, false},
		{"no declared regions", "me-east-215-b-1", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RegionDeclared(tc.key, tc.declared); got != tc.want {
				t.Fatalf("RegionDeclared(%q, %v) = %v, want %v", tc.key, tc.declared, got, tc.want)
			}
		})
	}
}

// TestEndpoint_CurrentContextWins — the endpoint reported must be the one the
// client would actually dial. A document with two clusters where the
// current-context names the second would otherwise be described by the first,
// and an operator would chase an address the process never used.
func TestEndpoint_CurrentContextWins(t *testing.T) {
	const twoClusters = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.43:6443
  name: a
- cluster:
    server: https://212.72.24.25:6443
  name: b
contexts:
- name: cb
  context:
    cluster: b
    user: u
current-context: cb
users:
- name: u
  user:
    token: fake-token`
	if got := Endpoint(twoClusters); got != "https://212.72.24.25:6443" {
		t.Fatalf("Endpoint = %q, want the current context's cluster server", got)
	}
	if got := Endpoint("not: [a kubeconfig"); got != "" {
		t.Fatalf("Endpoint on unparseable input = %q, want empty", got)
	}
	if got := EndpointHostPort(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://cp.example.internal
  name: c`, "6443"); got != "cp.example.internal:6443" {
		t.Fatalf("EndpointHostPort without an explicit port = %q, want the default applied", got)
	}
}

// TestOracleReceivesTheServerURL pins the argument the oracle is handed, and
// TestHostPort pins the URL-in helper it needs. Both exist because the first
// wiring of this seam fed a server URL to EndpointHostPort — a raw-bytes
// function — which returned "" and made the probe answer "unreachable" for
// EVERY document, including genuine ones. That is a check failing closed for a
// reason unrelated to what it claims to measure, and no assertion in the
// package caught it: the oracle was a test stub that never looked at its
// argument.
func TestOracleReceivesTheServerURL(t *testing.T) {
	var seen []string
	DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven: func(endpoint string) bool {
			seen = append(seen, endpoint)
			return true
		},
	})
	if len(seen) != 1 || seen[0] != "https://212.72.24.25:6443" {
		t.Fatalf("oracle was called with %v, want exactly [https://212.72.24.25:6443] — the SERVER URL, not the document and not a host", seen)
	}
}

func TestHostPort(t *testing.T) {
	cases := []struct{ in, def, want string }{
		{"https://212.72.24.25:6443", "6443", "212.72.24.25:6443"},
		{"https://cp.example.internal", "6443", "cp.example.internal:6443"},
		{"https://[2001:db8::1]:6443", "6443", "[2001:db8::1]:6443"},
		{"", "6443", ""},
		{"::not a url::", "6443", ""},
	}
	for _, tc := range cases {
		if got := HostPort(tc.in, tc.def); got != tc.want {
			t.Errorf("HostPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDeliveryDefects_CanFail is the VACUITY CHECK. Every assertion above is
// an equality against a value; this one proves the new machinery is capable of
// producing a non-empty verdict at all, from a document that differs from the
// accepted control ONLY in its endpoint. Without it, an implementation that
// returned nil unconditionally would pass every green assertion in the file.
func TestDeliveryDefects_CanFail(t *testing.T) {
	oracle := provenEndpoints("https://212.72.24.25:6443")
	accepted := DeliveryDefects(genuineRegionBValue, Delivery{
		RegionKey:       "me-east-215-b-1",
		DeclaredRegions: hw293DeclaredRegions,
		EndpointProven:  oracle,
	})
	refused := DeliveryDefects(
		strings.Replace(genuineRegionBValue, "212.72.24.25", "212.72.24.6", 1),
		Delivery{
			RegionKey:       "me-east-215-b-1",
			DeclaredRegions: hw293DeclaredRegions,
			EndpointProven:  oracle,
		})
	if len(accepted) != 0 {
		t.Fatalf("control refused: %v", accepted)
	}
	if len(refused) == 0 {
		t.Fatal("VACUITY: flipping the endpoint (the single byte-level difference) changed nothing — the new assertion cannot fail")
	}
	if DescribeDefects(refused) == DescribeDefects(accepted) {
		t.Fatal("VACUITY: accepted and refused describe identically")
	}
	if DescribeDefects(nil) != "none" {
		t.Fatal("DescribeDefects must name the empty case explicitly")
	}
}
