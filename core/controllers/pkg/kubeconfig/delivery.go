// delivery.go — the SECOND question a secondary region's kubeconfig has to
// answer, because the first one is demonstrably not enough.
//
// WHAT kubeconfig.go ALREADY ANSWERS, AND WHY IT WAS NOT SUFFICIENT
// -----------------------------------------------------------------
// Defects answers "can these bytes produce a working client?". That closed the
// 95-byte credential-less shell (#6054/#6112/#6116) and every hop now shares
// the one implementation, so a document accepted by the producer cannot be
// rejected by the reader.
//
// It did not close the state measured on hw293 (dep a0077ba47e3720e5) on
// 2026-08-11, and that state is why those merged fixes are INERT there:
//
//	Secret catalyst/cutover-secondary-kubeconfigs
//	  me-east-215-b-1.yaml  219 bytes  sha256 881231f95cd49646…
//	  server: https://212.72.24.6:6443
//
// Five top-level sections, a resolvable current-context, a bearer credential.
// `Defects` returns EMPTY on it. And `212.72.24.6` is neither of this
// deployment's two regions — region A's apiserver answers on `212.72.24.43`
// and region B's on `212.72.24.25`, each proven by its own serving
// certificate's SANs (`…-me-east-215-a-cp1-…` / `…-me-east-215-b-cp1-…`,
// both issued by this Sovereign's own `k3s-server-ca`). `212.72.24.6` answers
// nothing at all: no ICMP, no :443, and a 12-second timeout on :6443.
//
// Those 219 bytes are, to the byte, this repository's own unit-test control
// fixture `completeKubeconfigSameCluster` (220 bytes with its trailing
// newline, 219 without; sha256 of the stripped form matches the live value
// exactly). The fixture was built to be maximally usable-by-the-contract while
// pointing at a deliberately fake host — which is precisely what makes it the
// perfect counterexample: **a document engineered to satisfy "usable" is not
// thereby a delivery.** Presence stood in for usability once; usability then
// stood in for delivery. Same substitution, one rung further in.
//
// WHAT "DELIVERED" MUST ADDITIONALLY MEAN
// ---------------------------------------
// Two properties, both of which the hw293 value fails and a genuine peer-region
// credential passes:
//
//  1. ATTRIBUTION — the region the document is filed under must be one this
//     deployment DECLARES. The declared set is `CATALYST_CONFIGURED_REGIONS`
//     (`catalyst-system/sovereign-fqdn` key `configuredRegions`), live on
//     hw293 as `hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod`. It is
//     written by the IaC at provision time, so it is independent of both
//     ClusterMesh (absent until the mesh establishes — NotFound on hw293) and
//     the cutover chart that owns the bridge Secret.
//
//  2. PROOF — the endpoint the document names must have been PROVEN to answer,
//     by whatever oracle the calling component legitimately has. This package
//     performs no I/O and never will: it takes the oracle as a function and
//     asks it. A caller with no oracle passes nil and gets exactly the
//     pre-existing bytes-only verdict, so nothing regresses for a consumer
//     that cannot probe.
//
// WHY ATTRIBUTION IS ON THE REGION KEY AND NOT ON THE ENDPOINT
// ------------------------------------------------------------
// The obvious form of check 1 — "is the server host one of the declared
// regions?" — is NOT decidable from local state, and saying so is more useful
// than shipping a check that cannot fire. The declared witness names CLOUD
// REGIONS (`hw-me-east-215-b-rtz-prod`) while a kubeconfig names an IP
// (`212.72.24.25`), and no in-cluster object on hw293 maps one to the other:
// region A's node list contains only region-A nodes, `cilium-clustermesh` is
// NotFound, and `sovereign-fqdn` carries the LOCAL control-plane IP only. A
// host-versus-declared-region comparison would therefore reject every genuine
// delivery — the exact over-correction this contract must not make.
//
// So the endpoint is bound by PROOF (check 2) rather than by name, and the
// NAME is bound where names actually exist (check 1). Between them the hw293
// value has nowhere to hide: the endpoint answers nothing, so no oracle can
// prove it.
//
// Refs #5488 #6015 #6027 #6054 #6104 #6107 #6112 #6116.
package kubeconfig

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
)

// Defect names this file can add, on top of the section names Defects
// returns. They are deliberately not section names: an operator reading
// "users" knows to look at a `users:` block, and must not be sent looking for
// an `endpoint:` block that does not exist in the format.
const (
	// DefectEndpoint — the document declares no server URL that can be
	// parsed, so there is nothing to prove and nothing to dial.
	DefectEndpoint = "endpoint-absent"
	// DefectRegionUndeclared — the region key this document is filed under
	// is not one the deployment declares.
	DefectRegionUndeclared = "region-undeclared"
	// DefectEndpointUnproven — the caller's oracle could not prove the
	// endpoint belongs to a live apiserver of this deployment.
	DefectEndpointUnproven = "endpoint-unproven"
)

// Delivery carries the context a delivery verdict needs beyond the bytes.
// Every field is optional: a zero Delivery makes DeliveryDefects behave
// exactly like Defects, which is what keeps this additive for callers that
// legitimately know less.
type Delivery struct {
	// RegionKey is the key the document is filed under — the `<regionKey>`
	// half of the bridge Secret's `<regionKey>.yaml` data key, or of the
	// on-disk `<depID>-<regionKey>.yaml` file name. Empty skips attribution.
	RegionKey string

	// DeclaredRegions is the deployment's own region list, normally
	// CATALYST_CONFIGURED_REGIONS. Empty skips attribution: a Sovereign with
	// no witness must behave exactly as it did before this file existed,
	// never worse.
	DeclaredRegions []string

	// EndpointProven is the caller's oracle, given the server URL as written
	// in the document. Nil skips the proof — a consumer that cannot dial is
	// not thereby entitled to a verdict about reachability, and inventing one
	// would be reporting from absent evidence.
	EndpointProven func(endpoint string) bool
}

// DeliveryDefects reports why raw is not a credible DELIVERY of the named
// region's credential: everything Defects reports, plus attribution and proof.
// An empty slice means the document is usable AND is a delivery.
//
// Ordering is stable (sorted) so a log line or a condition message diffs
// cleanly between passes.
func DeliveryDefects(raw string, d Delivery) []string {
	defects := Defects(raw)

	// A document that cannot build a client is already disqualified; adding
	// attribution/proof findings on top would bury the primary cause under
	// consequences of it. Report the bytes verdict alone.
	if len(defects) > 0 {
		return defects
	}

	extra := map[string]struct{}{}

	endpoint := Endpoint(raw)
	if endpoint == "" {
		extra[DefectEndpoint] = struct{}{}
	}

	if d.RegionKey != "" && len(d.DeclaredRegions) > 0 && !RegionDeclared(d.RegionKey, d.DeclaredRegions) {
		extra[DefectRegionUndeclared] = struct{}{}
	}

	// The proof runs only when there IS an endpoint to prove and an oracle to
	// ask. Both guards matter: asking an oracle about "" would make every
	// caller's prober answer a question about nothing.
	if endpoint != "" && d.EndpointProven != nil && !d.EndpointProven(endpoint) {
		extra[DefectEndpointUnproven] = struct{}{}
	}

	if len(extra) == 0 {
		return nil
	}
	out := make([]string, 0, len(extra))
	for k := range extra {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Endpoint returns the server URL of the cluster the document's current
// context names. When the current context cannot be resolved to a cluster it
// falls back to the first cluster carrying a non-empty server, which is the
// same relaxation kubeconfigServerHost has always made on the self-heal path.
// Empty when the document is unparseable or declares no server.
func Endpoint(raw string) string {
	cfg, err := clientcmd.Load([]byte(raw))
	if err != nil || cfg == nil {
		return ""
	}
	if ctx, ok := cfg.Contexts[strings.TrimSpace(cfg.CurrentContext)]; ok && ctx != nil {
		if c, ok := cfg.Clusters[strings.TrimSpace(ctx.Cluster)]; ok && c != nil {
			if s := strings.TrimSpace(c.Server); s != "" {
				return s
			}
		}
	}
	// Deterministic fallback — map iteration order must not decide which
	// endpoint a message names.
	names := make([]string, 0, len(cfg.Clusters))
	for name := range cfg.Clusters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if c := cfg.Clusters[name]; c != nil {
			if s := strings.TrimSpace(c.Server); s != "" {
				return s
			}
		}
	}
	return ""
}

// EndpointHostPort renders a DOCUMENT's endpoint as host:port, supplying
// defaultPort when the URL carries none. Empty when there is no parseable
// endpoint. Takes raw kubeconfig bytes, like every other function in this
// package — see HostPort for the URL-in variant.
func EndpointHostPort(raw, defaultPort string) string {
	return HostPort(Endpoint(raw), defaultPort)
}

// HostPort renders a SERVER URL as host:port, supplying defaultPort when the
// URL carries none. Empty when the URL is unparseable or names no host.
//
// It exists as its own function because Delivery.EndpointProven is handed the
// server URL, not the document: an oracle that had to re-parse the whole
// kubeconfig to find the host it was just told about would be re-deriving what
// the caller already computed — and a caller that mistakenly fed a URL to a
// raw-bytes function would get a silent empty string and a probe that always
// answers "unreachable", i.e. a check that fails closed for the wrong reason.
func HostPort(serverURL, defaultPort string) string {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return ""
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	if port == "" {
		return u.Hostname()
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// RegionDeclared reports whether regionKey names one of declared.
//
// The two vocabularies do not match literally and never have: kubeconfigs are
// keyed by the secondary control plane's own region key (`me-east-215-b-1`,
// where the trailing ordinal counts control planes within the region) while
// the declared list names cloud regions (`hw-me-east-215-b-rtz-prod`). The
// match is therefore on dash-delimited TOKENS, not on raw substrings:
// `me-east-215-b-1` reduces to the token run [me east 215 b], which appears
// contiguously in [hw me east 215 b rtz prod].
//
// Token containment rather than string containment is deliberate. `strings.
// Contains` would let `me-east-21` match `me-east-215`, i.e. a neighbouring
// region would be accepted as this one — a mis-delivery of exactly the class
// this function exists to catch, waved through by the check meant to catch it.
//
// Containment is tested BOTH ways so neither vocabulary has to be the longer
// one; a future provider whose region key is the more specific string is
// handled without a second rule.
//
// Containment alone is still too generous at the short end — `me-east-2`
// reduces to [me east], which IS a run inside [hw me east 215 b rtz prod],
// and accepting it would attribute a truncated key to a region it merely
// shares a prefix with. So a match must ALSO be substantial: either the run is
// at least minRegionRunTokens long, or it is the declared identifier exactly
// (which is what keeps a genuinely short provider key like `fsn1-1` against a
// declared `fsn1` attributable).
func RegionDeclared(regionKey string, declared []string) bool {
	key := regionKeyRun(regionKey)
	if len(key) == 0 {
		return false
	}
	for _, d := range declared {
		dt := regionTokens(d)
		if len(dt) == 0 {
			continue
		}
		if !containsRun(dt, key) && !containsRun(key, dt) {
			continue
		}
		if len(key) >= minRegionRunTokens || equalRun(key, dt) {
			return true
		}
	}
	return false
}

// minRegionRunTokens is how many tokens a region key must contribute before
// mere containment counts as attribution. Two is not enough: [me east] is a
// run inside most of a region list. Three is the shortest run that names a
// region rather than a family of them, and anything shorter still attributes
// when it matches a declared identifier exactly.
const minRegionRunTokens = 3

// regionKeyRun tokenizes a region key and drops a trailing control-plane
// ordinal (`me-east-215-b-1` → [me east 215 b]). Tokenizing FIRST is what
// makes the strip separator-agnostic: `me_east_215_b_1` reduces identically,
// where a `-`-only trim would have left the ordinal attached and the key
// unattributable. A key that is a bare number keeps it — stripping would leave
// nothing to match on, and an empty run must never attribute.
func regionKeyRun(regionKey string) []string {
	t := regionTokens(regionKey)
	if len(t) > 1 && allDigits(t[len(t)-1]) {
		t = t[:len(t)-1]
	}
	return t
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// equalRun reports token-sequence equality.
func equalRun(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// regionTokens lowercases and splits a region identifier on any run of
// non-alphanumeric characters, so `hw-me-east-215-b-rtz-prod`,
// `hw_me_east_215_b`, and `HW.ME.EAST.215.B` all tokenize the same way.
func regionTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.TrimSpace(s)), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
}

// containsRun reports whether needle appears as a CONTIGUOUS run inside
// haystack. An empty needle never matches — "nothing is contained in
// everything" would make attribution vacuous.
func containsRun(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
