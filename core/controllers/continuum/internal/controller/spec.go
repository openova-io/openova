// Spec parsing for the Continuum CR — extracts the typed view from
// unstructured.Unstructured for the reconciler.
//
// Per ADR-0001 §2.7 we deliberately don't depend on a generated
// typed client; parseSpec is the boundary between the dynamic-client
// view and the strongly-typed reconcile loop.

package controller

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
)

// ContinuumSpec is the parsed view of Continuum.spec used by the
// reconciler + per-CR goroutine.
//
// Field names mirror the CRD shape at
// products/catalyst/chart/crds/continuum.yaml. Adding a new field
// requires updating the CRD AND parseSpec — keep them in lockstep.
type ContinuumSpec struct {
	// ApplicationRef = `<namespace>/<name>` of the Application this
	// Continuum governs (the Application controller writes this CR's
	// peer; here we only read its name).
	ApplicationRef string

	// PrimaryRegion is the host-cluster name currently primary.
	PrimaryRegion string

	// HotStandbyRegions lists the standby host-clusters in priority
	// order. Index 0 is the preferred failover target.
	HotStandbyRegions []string

	// LeaseClientKind = `cloudflare-kv` | `dns-quorum` | `in-memory`
	// (the last is test-only; gated by DefaultSelector.InMemoryAllowed).
	LeaseClientKind string

	// LeaseClientConfig is the raw config map; the witness Selector
	// reads its kind-specific fields.
	LeaseClientConfig map[string]any

	// TTLSeconds + RenewSeconds — overridable lease tuning. Empty =
	// default 30/10.
	TTLSeconds   int
	RenewSeconds int

	// RTOSeconds — derived from spec.rto duration string. Used as
	// the lag-breach threshold for auto-failover.
	RTOSeconds int

	// AutoFailover — when true, lag-breach + lease-quorum-confirms
	// triggers switchover. False = surface alarm only and wait for
	// operator.
	AutoFailover bool

	// StandbyGraceSeconds — #4901 follow-up: how long the required
	// synchronous hot-standby must be CONTINUOUSLY unavailable (or its
	// Cluster CR unresolvable after having been seen) before the
	// controller degrades the CR and writes StandbyAvailable=False. A
	// brief flap (one failed probe, apiserver hiccup, rolling restart)
	// inside the window makes no determination — the prior condition
	// is preserved. Recovery always surfaces immediately. Read from
	// spec.standbyGraceSeconds (the CRD's spec preserves unknown
	// fields); absent = DefaultStandbyGraceSeconds, explicit 0 =
	// degrade on first observation.
	StandbyGraceSeconds int

	// Mechanism — the switchover state-promotion mechanism
	// (`cnpg-pair` default | `raft-transition`). Read from
	// spec.switchover.mechanism; mirrors the Blueprint topology
	// declaration's switchover.mechanism column. Empty keeps the
	// cnpg-pair default (#3492).
	Mechanism switchover.Mechanism

	// CNPGPair + CNPGNamespace — name + namespace of the
	// bp-cnpg-pair Cluster CR pair this Continuum operates on.
	// Resolved from the Continuum CR's spec.cnpgPair field (or via
	// the Application — TBD with C-DB-1's wiring). Used by the
	// cnpg-pair mechanism.
	CNPGPair      string
	CNPGNamespace string

	// RaftTransition — the openbao standby target for the
	// `raft-transition` mechanism. Read from
	// spec.switchover.raftTransition. Empty for cnpg-pair (#3492).
	RaftTransition switchover.RaftTransitionTarget

	// HTTPRouteName + HTTPRouteNamespace — Gateway-API HTTPRoute the
	// drain step targets. Empty = no drain.
	HTTPRouteName      string
	HTTPRouteNamespace string

	// PDMZone — PowerDNS zone the lua-records land in.
	PDMZone string

	// SynthParams holds the per-region IP map + healthcheck
	// configuration for the lua-record synthesizer.
	SynthParams dns.SynthParams

	// Application is the Application CR Unstructured passed to the
	// synthesizer for hostname extraction. Optional when
	// SynthParams.Hostnames is non-empty.
	Application *unstructured.Unstructured

	// InitiatedBy — defaults to "controller" when unset; operator
	// edits to spec.switchover.requestedBy override.
	InitiatedBy string
}

// parseSpec extracts the typed view of Continuum.spec from an
// Unstructured CR. Returns an error iff a CRD-required field is
// missing — defensive: in tests the CRD's openAPI validation isn't
// enforced.
func parseSpec(cr *unstructured.Unstructured) (ContinuumSpec, error) {
	out := ContinuumSpec{}
	if cr == nil {
		return out, errors.New("nil Continuum CR")
	}

	out.ApplicationRef, _, _ = unstructured.NestedString(cr.Object, "spec", "applicationRef")
	if out.ApplicationRef == "" {
		return out, errors.New("spec.applicationRef is required")
	}

	out.PrimaryRegion, _, _ = unstructured.NestedString(cr.Object, "spec", "primaryRegion")
	if out.PrimaryRegion == "" {
		return out, errors.New("spec.primaryRegion is required")
	}

	regions, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "hotStandbyRegions")
	if len(regions) == 0 {
		return out, errors.New("spec.hotStandbyRegions[] is required")
	}
	out.HotStandbyRegions = regions

	out.LeaseClientKind, _, _ = unstructured.NestedString(cr.Object, "spec", "leaseClient", "kind")
	if out.LeaseClientKind == "" {
		return out, errors.New("spec.leaseClient.kind is required")
	}
	out.LeaseClientConfig, _, _ = unstructured.NestedMap(cr.Object, "spec", "leaseClient", "config")

	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "leaseClient", "config", "ttlSeconds"); ok {
		out.TTLSeconds = int(v)
	}
	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "leaseClient", "config", "renewSeconds"); ok {
		out.RenewSeconds = int(v)
	}

	rto, _, _ := unstructured.NestedString(cr.Object, "spec", "rto")
	if rto != "" {
		s, err := parseDurationSeconds(rto)
		if err != nil {
			return out, fmt.Errorf("spec.rto: %w", err)
		}
		out.RTOSeconds = s
	}

	out.AutoFailover, _, _ = unstructured.NestedBool(cr.Object, "spec", "autoFailover")

	// #4901 follow-up — standby-absent grace window. Absent field =
	// default; explicit 0 = degrade on first unavailable observation.
	out.StandbyGraceSeconds = DefaultStandbyGraceSeconds
	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "standbyGraceSeconds"); ok {
		out.StandbyGraceSeconds = int(v)
	}

	out.CNPGPair, _, _ = unstructured.NestedString(cr.Object, "spec", "cnpgPair", "name")
	out.CNPGNamespace, _, _ = unstructured.NestedString(cr.Object, "spec", "cnpgPair", "namespace")
	// Default the namespace to the Continuum CR's own namespace when
	// not supplied (most common case: cluster-pair lives alongside
	// the Continuum CR).
	if out.CNPGNamespace == "" {
		out.CNPGNamespace = cr.GetNamespace()
	}

	// #3492 — switchover mechanism + raft-transition target. Empty
	// mechanism keeps the cnpg-pair default so existing CRs are
	// unchanged.
	if m, _, _ := unstructured.NestedString(cr.Object, "spec", "switchover", "mechanism"); m != "" {
		out.Mechanism = switchover.Mechanism(m)
	}
	out.RaftTransition = parseRaftTransition(cr)

	out.HTTPRouteName, _, _ = unstructured.NestedString(cr.Object, "spec", "httpRoute", "name")
	out.HTTPRouteNamespace, _, _ = unstructured.NestedString(cr.Object, "spec", "httpRoute", "namespace")
	if out.HTTPRouteNamespace == "" && out.HTTPRouteName != "" {
		out.HTTPRouteNamespace = cr.GetNamespace()
	}

	out.PDMZone, _, _ = unstructured.NestedString(cr.Object, "spec", "pdmZone")

	out.SynthParams = parseSynthParams(cr)

	out.InitiatedBy, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "requestedBy")
	if out.InitiatedBy == "" {
		out.InitiatedBy = "controller"
	}

	return out, nil
}

// parseSynthParams reads the lua-record knobs off the CR.
func parseSynthParams(cr *unstructured.Unstructured) dns.SynthParams {
	sp := dns.SynthParams{}
	sp.Selector, _, _ = unstructured.NestedString(cr.Object, "spec", "luaRecord", "selector")
	sp.HealthCheckURL, _, _ = unstructured.NestedString(cr.Object, "spec", "luaRecord", "healthCheck", "url")
	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "luaRecord", "healthCheck", "intervalSeconds"); ok {
		sp.HealthCheckIntervalSeconds = int(v)
	}
	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "luaRecord", "healthCheck", "timeoutSeconds"); ok {
		sp.HealthCheckTimeoutSeconds = int(v)
	}
	if v, ok, _ := unstructured.NestedInt64(cr.Object, "spec", "luaRecord", "ttl"); ok {
		sp.TTL = int(v)
	}
	regionToIPs := map[string][]string{}
	regions, _, _ := unstructured.NestedSlice(cr.Object, "spec", "regions")
	for _, r := range regions {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(rm, "name")
		ips, _, _ := unstructured.NestedStringSlice(rm, "lbIPs")
		if name != "" && len(ips) > 0 {
			regionToIPs[name] = ips
		}
	}
	sp.RegionToIPs = regionToIPs
	hns, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "luaRecord", "hostnames")
	sp.Hostnames = hns
	return sp
}

// parseRaftTransition reads the openbao raft-transition target off the CR
// (spec.switchover.raftTransition). All fields optional at parse time; the
// SwitchoverPlan.Validate gate enforces namespace + (pod|podSelector) when
// the mechanism is actually raft-transition (#3492).
func parseRaftTransition(cr *unstructured.Unstructured) switchover.RaftTransitionTarget {
	t := switchover.RaftTransitionTarget{}
	t.Namespace, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "namespace")
	t.Pod, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "pod")
	t.PodSelector, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "podSelector")
	t.Container, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "container")
	t.RaftDataPath, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "raftDataPath")
	t.SnapshotPath, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "snapshotPath")
	t.BaoBinary, _, _ = unstructured.NestedString(cr.Object, "spec", "switchover", "raftTransition", "baoBinary")
	return t
}

// parseDurationSeconds parses a `[0-9]+(s|m|h)` duration into
// seconds. Mirrors the CRD's `pattern: '^[0-9]+(s|m|h)$'`.
func parseDurationSeconds(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	unit := s[len(s)-1]
	digits := s[:len(s)-1]
	n := 0
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit in duration: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	switch unit {
	case 's':
		return n, nil
	case 'm':
		return n * 60, nil
	case 'h':
		return n * 3600, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q in %q", unit, s)
	}
}
