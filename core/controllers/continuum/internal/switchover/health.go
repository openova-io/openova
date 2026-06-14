// Post-switchover health check — slice F-3 of EPIC-6 (#1101).
//
// `Sequencer.PostSwitchoverHealth` runs ~30s after a successful
// switchover (the controller chains it from `runSwitchover` once the
// 7-step Sequence returns nil). It surfaces the new primary's health
// across four checks:
//
//  1. Replicas healthy — CNPG status reports the new primary's
//     instances Ready count == declared (we read both halves of the
//     cluster-pair: the new-primary should have replica.enabled=false
//     AND status.Ready=true; the new-replica should have
//     replica.enabled=true).
//  2. Lua-record probes returning new primary — multi-vantage-point
//     DNS resolution: query the lua-record's hostnames against
//     `MultiResolverDial` (default = three resolvers
//     8.8.8.8 / 1.1.1.1 / 9.9.9.9) and assert every vantage returns
//     one of the new primary's IPs.
//  3. Latency normal — DEFERRED in this slice; surfaced as a
//     `Passed=false, Detail="deferred"` check the UI can render
//     greyed out. Wiring to Cilium hubble metrics is out-of-scope
//     for the controller (no metrics scraping client).
//  4. Audit event posted — confirm the `continuum-switchover`
//     event landed on NATS by inspecting an injected
//     `AuditTail` (in-process tail of recently-published events).
//     Production wires this to the JetStream consumer; tests pass a
//     Recorder.
//
// Invariants:
//
//   - The function is read-only. It MUST NOT mutate any CR / lease /
//     DNS state.
//   - All four checks always run; the function does NOT short-circuit
//     on the first failure (the operator wants the full picture).
//   - `OverallHealthy` is true iff every NON-DEFERRED check passed.
//
// Per INVIOLABLE-PRINCIPLES #5 the health endpoint is owner-tier
// gated by the API server (api/server.go); the Sequencer method
// itself is the pure-Go computation.
package switchover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// HealthReport is the output of Sequencer.PostSwitchoverHealth.
//
// Schema is part of the cross-slice contract: U-DR-1 (UI) decodes
// against this shape. Field renames here ripple through the API
// server's handler + the UI's JSON decoder. Coordinate via the
// canonical-seam map before changing.
type HealthReport struct {
	// NewPrimaryRegion is the region the switchover promoted to
	// primary. Echoes plan.ToRegion.
	NewPrimaryRegion string `json:"newPrimaryRegion"`

	// Checks lists the 4 health checks in fixed order:
	// "replicas-healthy", "dns-probes", "latency-normal", "audit-posted".
	Checks []HealthCheck `json:"checks"`

	// OverallHealthy is true iff every NON-DEFERRED check Passed.
	OverallHealthy bool `json:"overallHealthy"`

	// GeneratedAt is the wall-clock UTC stamp at report generation.
	GeneratedAt time.Time `json:"generatedAt"`
}

// HealthCheck is a single check result.
type HealthCheck struct {
	// Name is the canonical check name (one of the four above).
	Name string `json:"name"`

	// Passed reports the check outcome. Deferred checks set Passed
	// false + Detail starting with "deferred".
	Passed bool `json:"passed"`

	// Detail is a human-readable description suitable for surfacing
	// in the UI's per-check row.
	Detail string `json:"detail,omitempty"`

	// Deferred — true when this check is intentionally not exercised
	// in this build (e.g. latency check pending Cilium-metrics
	// integration). The UI renders deferred checks greyed out.
	Deferred bool `json:"deferred,omitempty"`
}

// Canonical check names — exported so tests + UI pin to symbols.
const (
	CheckReplicasHealthy = "replicas-healthy"
	CheckDNSProbes       = "dns-probes"
	CheckLatencyNormal   = "latency-normal"
	CheckAuditPosted     = "audit-posted"
)

// MultiResolverDial is the DNS-vantage-point fanout. Default returns
// three resolvers (Google / Cloudflare / Quad9); tests inject fakes.
//
// Returning `[]Resolver` (rather than just a list of IPs) keeps the
// seam swappable — fakes can return Resolver impls that don't
// actually hit the network.
type MultiResolverDial func() []Resolver

// Resolver is the minimal DNS interface PostSwitchoverHealth needs.
// `*net.Resolver` satisfies it via the `LookupHost` method, but
// we keep our own surface so test fakes don't need to implement
// the full `*net.Resolver` API.
type Resolver interface {
	// Name is a short label like "8.8.8.8" used in HealthCheck.Detail.
	Name() string
	// LookupHost returns the IPs returned for `host` from this
	// vantage point. Errors propagate up so the check records "DNS
	// lookup failed at <vantage>".
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// AuditTail is the post-switchover audit-event lookup contract.
// The Sequencer uses it to confirm a `continuum-switchover` event
// was published for the given `ContinuumName`. Production wires this
// to a NATS JetStream PullConsumer (see api/server.go). Tests pass
// an in-memory recorder.
//
// AuditTail.Recent returns the tail of recently-published events
// optionally filtered by audit-type. The Sequencer queries by
// `eventsTypeSwitchover` (avoiding an import cycle on `events`).
type AuditTail interface {
	// Recent returns events newer than `since`, optionally filtered
	// by audit-type. Implementations MAY return more events than
	// strictly necessary (the caller filters again in-memory).
	Recent(ctx context.Context, since time.Time, auditType string) ([]AuditTailEvent, error)
}

// AuditTailEvent is the minimal event shape the health check needs.
// Mirrors the relevant fields of events.Event without taking a
// dependency on that package (which depends on this one indirectly).
type AuditTailEvent struct {
	Type          string
	ContinuumName string
	FromPrimary   string
	ToPrimary     string
	Timestamp     time.Time
}

// HealthOptions carries injectable dependencies for
// PostSwitchoverHealth. The Sequencer's `Health*` fields populate
// the defaults; tests override per-call.
type HealthOptions struct {
	// Resolvers — the multi-vantage-point DNS resolver factory.
	// nil = default (Google / Cloudflare / Quad9).
	Resolvers MultiResolverDial

	// AuditTail — the audit-event lookup. nil = audit check is
	// recorded as deferred.
	AuditTail AuditTail

	// AuditWindow — how far back AuditTail.Recent is queried.
	// Default 5 minutes (covers a slow switchover + post-switchover
	// settle time).
	AuditWindow time.Duration

	// DNSTimeout — per-vantage timeout. Default 3 seconds.
	DNSTimeout time.Duration
}

// PostSwitchoverHealth runs the 4 post-switchover checks and returns
// a HealthReport. The plan's `ToRegion` is the new primary; the
// SynthParams' `RegionToIPs[ToRegion]` is the canonical IP set the
// DNS probes assert against; the Application's hostnames (or
// SynthParams.Hostnames) are the names being probed.
//
// On a missing dependency (nil Resolvers / nil AuditTail) the
// affected check is recorded with Deferred=true rather than failing
// the report.
//
// The function NEVER returns an error — every failure is a check
// result the UI renders. The `error` return is reserved for
// ctx-cancel.
func (s *Sequencer) PostSwitchoverHealth(ctx context.Context, plan SwitchoverPlan, opts HealthOptions) (HealthReport, error) {
	if err := ctx.Err(); err != nil {
		return HealthReport{}, err
	}
	plan = plan.Defaults()

	if opts.AuditWindow <= 0 {
		opts.AuditWindow = 5 * time.Minute
	}
	if opts.DNSTimeout <= 0 {
		opts.DNSTimeout = 3 * time.Second
	}

	report := HealthReport{
		NewPrimaryRegion: plan.ToRegion,
		GeneratedAt:      s.now().UTC(),
	}

	// Check 1 — Replicas healthy.
	report.Checks = append(report.Checks, s.healthReplicas(ctx, plan))

	// Check 2 — DNS probes.
	report.Checks = append(report.Checks, s.healthDNSProbes(ctx, plan, opts))

	// Check 3 — Latency normal (deferred for now).
	report.Checks = append(report.Checks, HealthCheck{
		Name:     CheckLatencyNormal,
		Passed:   false,
		Deferred: true,
		Detail:   "deferred: Cilium hubble metrics scrape not wired (slice F-3 deferred to F-4 / SRE follow-up)",
	})

	// Check 4 — Audit posted.
	report.Checks = append(report.Checks, s.healthAuditPosted(ctx, plan, opts))

	// OverallHealthy: every non-deferred check Passed.
	report.OverallHealthy = true
	for _, c := range report.Checks {
		if c.Deferred {
			continue
		}
		if !c.Passed {
			report.OverallHealthy = false
			break
		}
	}
	return report, nil
}

func (s *Sequencer) healthReplicas(ctx context.Context, plan SwitchoverPlan) HealthCheck {
	c := HealthCheck{Name: CheckReplicasHealthy}
	// The replicas-healthy check inspects a CNPG cluster-pair, which only
	// the cnpg-pair mechanism has. For raft-transition (bp-openbao) there
	// is no Cluster-CR pair — the meaningful post-promotion signal is the
	// DNS-probe check (traffic lands on the promoted region) — so record
	// this check as deferred rather than spuriously failing (#3492).
	mech := plan.Mechanism
	if mech == "" {
		mech = DefaultMechanism
	}
	if mech != MechanismCNPGPair {
		c.Deferred = true
		c.Detail = fmt.Sprintf("deferred: replicas-healthy is cnpg-pair-specific; mechanism=%q has no Cluster-CR pair (DNS-probe check is the post-promotion health signal)", mech)
		return c
	}
	if s.CNPG == nil {
		c.Detail = "CNPG reader not configured"
		return c
	}
	primary, replica, err := s.CNPG.FindPair(ctx, plan.CNPGNamespace, plan.CNPGPair)
	if err != nil {
		c.Detail = fmt.Sprintf("FindPair %s/%s: %v", plan.CNPGNamespace, plan.CNPGPair, err)
		return c
	}
	primaryStatus, _, pErr := s.CNPG.Get(ctx, primary.GetNamespace(), primary.GetName())
	if pErr != nil {
		c.Detail = fmt.Sprintf("Get primary %s: %v", primary.GetName(), pErr)
		return c
	}
	replicaStatus, _, rErr := s.CNPG.Get(ctx, replica.GetNamespace(), replica.GetName())
	if rErr != nil {
		c.Detail = fmt.Sprintf("Get replica %s: %v", replica.GetName(), rErr)
		return c
	}
	// After a successful switchover from FromRegion → ToRegion, the
	// CR named `<pair>-primary` has already been flipped to the new
	// REPLICA half (replica.enabled=true), and the CR named
	// `<pair>-replica` has been flipped to the new PRIMARY half
	// (replica.enabled=false). Sequencer.steps()[5] does this.
	//
	// So the post-switchover assertion is:
	//   - The "becoming-primary" CR (previously labeled 'replica',
	//     now `replica.enabled=false`) has Ready=true.
	//   - The "becoming-replica" CR (previously labeled 'primary',
	//     now `replica.enabled=true`) has Ready=true.
	if !replicaStatus.Ready {
		c.Detail = fmt.Sprintf("new-primary CR %q is not Ready (phase=%q lag=%d)", replica.GetName(), replicaStatus.Phase, replicaStatus.LagSeconds)
		return c
	}
	if !primaryStatus.Ready {
		c.Detail = fmt.Sprintf("new-replica CR %q is not Ready (phase=%q lag=%d)", primary.GetName(), primaryStatus.Phase, primaryStatus.LagSeconds)
		return c
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("both halves Ready (new-primary=%s, new-replica=%s)", replica.GetName(), primary.GetName())
	return c
}

func (s *Sequencer) healthDNSProbes(ctx context.Context, plan SwitchoverPlan, opts HealthOptions) HealthCheck {
	c := HealthCheck{Name: CheckDNSProbes}
	if opts.Resolvers == nil {
		c.Deferred = true
		c.Detail = "deferred: no MultiResolverDial wired (production sets defaults via api/server.go)"
		return c
	}
	resolvers := opts.Resolvers()
	if len(resolvers) == 0 {
		c.Deferred = true
		c.Detail = "deferred: MultiResolverDial returned 0 resolvers"
		return c
	}
	expected, ok := plan.SynthParams.RegionToIPs[plan.ToRegion]
	if !ok || len(expected) == 0 {
		c.Detail = fmt.Sprintf("no expected IPs for ToRegion %q in SynthParams", plan.ToRegion)
		return c
	}
	hostnames := plan.SynthParams.Hostnames
	if len(hostnames) == 0 {
		c.Detail = "no hostnames in SynthParams to probe"
		return c
	}
	expectedSet := map[string]struct{}{}
	for _, ip := range expected {
		expectedSet[ip] = struct{}{}
	}

	var failures []string
	checks := 0
	for _, host := range hostnames {
		for _, r := range resolvers {
			cctx, cancel := context.WithTimeout(ctx, opts.DNSTimeout)
			ips, err := r.LookupHost(cctx, host)
			cancel()
			checks++
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s@%s: %v", host, r.Name(), err))
				continue
			}
			if len(ips) == 0 {
				failures = append(failures, fmt.Sprintf("%s@%s: 0 IPs returned", host, r.Name()))
				continue
			}
			matched := false
			for _, ip := range ips {
				if _, in := expectedSet[ip]; in {
					matched = true
					break
				}
			}
			if !matched {
				failures = append(failures, fmt.Sprintf("%s@%s: returned %s, none in expected set %v",
					host, r.Name(), strings.Join(ips, ","), expected))
			}
		}
	}
	if len(failures) > 0 {
		c.Detail = fmt.Sprintf("%d/%d vantage probes failed: %s", len(failures), checks, strings.Join(failures, "; "))
		return c
	}
	c.Passed = true
	c.Detail = fmt.Sprintf("all %d vantage probes resolved to expected IPs", checks)
	return c
}

func (s *Sequencer) healthAuditPosted(ctx context.Context, plan SwitchoverPlan, opts HealthOptions) HealthCheck {
	c := HealthCheck{Name: CheckAuditPosted}
	if opts.AuditTail == nil {
		c.Deferred = true
		c.Detail = "deferred: no AuditTail wired (production sets via JetStream consumer)"
		return c
	}
	since := s.now().Add(-opts.AuditWindow)
	events, err := opts.AuditTail.Recent(ctx, since, "continuum-switchover")
	if err != nil {
		c.Detail = fmt.Sprintf("AuditTail.Recent: %v", err)
		return c
	}
	for _, e := range events {
		if e.ContinuumName != plan.ContinuumName {
			continue
		}
		if e.FromPrimary != plan.FromRegion || e.ToPrimary != plan.ToRegion {
			continue
		}
		c.Passed = true
		c.Detail = fmt.Sprintf("found continuum-switchover audit at %s (from=%s to=%s)",
			e.Timestamp.UTC().Format(time.RFC3339), e.FromPrimary, e.ToPrimary)
		return c
	}
	c.Detail = fmt.Sprintf("no matching continuum-switchover audit in last %s", opts.AuditWindow)
	return c
}

// DefaultMultiResolverDial returns the production three-vantage
// resolver fanout: Google (8.8.8.8), Cloudflare (1.1.1.1), Quad9
// (9.9.9.9). Each resolver issues UDP queries via the standard library's
// `*net.Resolver` with PreferGo=true so the response order is
// vantage-deterministic.
func DefaultMultiResolverDial() []Resolver {
	return []Resolver{
		newSystemResolver("8.8.8.8"),
		newSystemResolver("1.1.1.1"),
		newSystemResolver("9.9.9.9"),
	}
}

type systemResolver struct {
	server   string // "8.8.8.8"
	resolver *net.Resolver
}

func newSystemResolver(server string) *systemResolver {
	return &systemResolver{
		server: server,
		resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				// Override the address to the configured server (port 53).
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(server, "53"))
			},
		},
	}
}

func (r *systemResolver) Name() string { return r.server }

func (r *systemResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if r == nil || r.resolver == nil {
		return nil, errors.New("systemResolver: nil")
	}
	return r.resolver.LookupHost(ctx, host)
}
