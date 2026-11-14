// Package precheck implements the catalyst-api side of the three
// pre-checks the G117.3 endpoint-CRUD pipeline runs BEFORE opening a
// PR against `gitea.<sov>/<org>/iac` per ADR-0009.
//
// The three checks are the same three named status checks the
// branch-protection contract requires for auto-merge (see
// `tools/bootstrap-org-iac-repo.sh` step 4):
//
//	kyverno-admission        — provided by the per-Org Kyverno CI workflow
//	cert-manager-precheck    — provided HERE (this package)
//	dns-conflict-precheck    — provided HERE (this package)
//
// Surfacing the cert-manager + DNS checks server-side BEFORE the PR is
// opened keeps the IaC repo free of known-bad PRs that would otherwise
// sit in a `pending` / `failed` status check until a human cleaned them
// up. The Kyverno check still runs as the third status-check workflow
// in the per-Org repo CI because Kyverno policies are Org-local and
// best evaluated by the per-Org workflow.
//
// Pure functions: no state, no global config, no I/O outside the
// caller-provided lookup closures. Every input is an argument; every
// output is a return value. Tests inject lookup fakes via the typed
// closures below; production wires real DNS + cert-manager clients in
// the consuming handler.
package precheck

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// hostnameRE is a permissive RFC-1123-flavoured hostname matcher: each
// dot-separated label is 1-63 chars of letters/digits/hyphen; no
// leading/trailing hyphen per label. The overall hostname can be at
// most 253 chars.
var hostnameRE = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)(\.[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?)*$`)

// endpointNameRE mirrors the EndpointMutationRequest.name pattern in
// docs/api/catalyst-api-openapi.yaml.
var endpointNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// Mutation captures the proposed endpoint state. The PR pipeline
// converts this into a manifest write at
// `<org>/iac/apps/<app>/endpoints/<name>.yaml`.
type Mutation struct {
	// Org is the per-Org Gitea repo owner (also the K8s namespace for
	// the Application).
	Org string
	// App is the Application name (immutable; namespace-scoped).
	App string
	// Name is the endpoint name within the Application.
	Name string
	// Hostname is the concrete FQDN this endpoint will be served at.
	// Empty Hostname is rejected — the per-Org IaC template requires
	// concrete hostnames (the template fan-out happens at Blueprint
	// publish time, NOT at endpoint mutation time).
	Hostname string
	// Port — service port (1..65535). 0 means "use the protocol default".
	Port int
	// Protocol — https | http | grpc | tcp | udp.
	Protocol string
	// TLS — does the endpoint terminate TLS? Default true.
	TLS bool
	// Visibility — public | private | internal. Required.
	Visibility string
	// SSOEnabled — is this endpoint behind the Sovereign SSO chain?
	SSOEnabled bool
	// Op describes whether this mutation creates, updates or deletes.
	Op Operation
}

// Operation identifies the kind of mutation this is.
type Operation int

const (
	OpUnknown Operation = iota
	OpCreate
	OpUpdate
	OpDelete
)

// Result is the structured outcome of a single precheck stage. Empty
// Error means "pass"; non-empty Error means "fail with this reason".
type Result struct {
	Stage   string // canonical stage name (matches the branch-protection check)
	Pass    bool
	Code    string // short machine-readable code on failure
	Message string // human-readable detail
}

// Bundle aggregates the three pre-PR checks.
type Bundle struct {
	Kyverno     Result
	CertManager Result
	DNSConflict Result
}

// AllPass returns true when every check has Pass=true.
func (b Bundle) AllPass() bool {
	return b.Kyverno.Pass && b.CertManager.Pass && b.DNSConflict.Pass
}

// FailedStages returns the stage names that did not pass, in
// deterministic order.
func (b Bundle) FailedStages() []string {
	out := []string{}
	if !b.Kyverno.Pass {
		out = append(out, b.Kyverno.Stage)
	}
	if !b.CertManager.Pass {
		out = append(out, b.CertManager.Stage)
	}
	if !b.DNSConflict.Pass {
		out = append(out, b.DNSConflict.Stage)
	}
	return out
}

// CertConflictLookup answers: does this hostname already have a
// Certificate owned by a different Application/Org? On a write the
// existing owner must match; otherwise the cert would be silently
// reassigned and the previous owner's traffic would 502.
//
// G117 #2864 (Gap 6): When the Sovereign's Gateway carries a wildcard
// Certificate (e.g. `*.<sov-fqdn>`) every new endpoint hostname under
// that wildcard would otherwise be rejected as a cert-conflict because
// the wildcard cert's owner labels (= Gateway / Sovereign infra) do
// NOT match the per-Application endpoint mutation. Lookups MUST set
// `IsWildcard=true` + `WildcardDomain=<base-domain>` when they find a
// wildcard Certificate so CheckCertManager can auto-PASS new
// hostnames under that wildcard.
//
// Returns:
//   - (owner, true, nil)  — an existing Certificate covers `hostname`
//     owned by `<org>/<app>` (in our own label vocabulary). When the
//     covering Certificate is a wildcard, owner.IsWildcard=true and
//     owner.WildcardDomain is the base domain (e.g. "hw86.omani.works"
//     for a `*.hw86.omani.works` cert).
//   - ("", false, nil)    — no existing Certificate
//   - ("", false, err)    — lookup failed; the caller treats as 503
type CertConflictLookup func(ctx context.Context, hostname string) (owner CertOwner, exists bool, err error)

// CertOwner labels a Certificate's owning Application.
//
// IsWildcard / WildcardDomain populated when the covering Certificate
// is a wildcard (e.g. `*.<sov-fqdn>`). See CertConflictLookup +
// CheckCertManager for the auto-PASS semantics this enables.
type CertOwner struct {
	Org string
	App string
	// IsWildcard is true when the discovered Certificate's spec.commonName
	// (or any entry in spec.dnsNames[]) begins with `*.`. G117 #2864.
	IsWildcard bool
	// WildcardDomain is the base domain of the wildcard Certificate —
	// the suffix after `*.`. Empty when IsWildcard=false. Example:
	// for a Certificate covering `*.hw86.omani.works`, WildcardDomain
	// is `hw86.omani.works`. G117 #2864.
	WildcardDomain string
}

// DNSConflictLookup answers: is this hostname already pinned to a
// different IP/Service in the PowerDNS zone?
//
// Returns:
//   - (target, true, nil)  — A/CNAME record exists targeting `target`
//   - ("", false, nil)     — no existing record
//   - ("", false, err)     — lookup failed; caller treats as 503
type DNSConflictLookup func(ctx context.Context, hostname string) (target string, exists bool, err error)

// KyvernoLookup is an OPTIONAL pre-PR Kyverno check. In ADR-0009 the
// Kyverno workflow runs inside the per-Org Gitea Actions repo; we may
// run a redundant pre-flight here if a lookup is wired, otherwise the
// stage is marked Pass to allow the PR to open and let the in-repo
// workflow be the gate.
type KyvernoLookup func(ctx context.Context, m Mutation) (Result, error)

// ValidateMutation runs the local-shape checks that don't need any
// external lookups. Returns the first error.
func ValidateMutation(m Mutation) error {
	if strings.TrimSpace(m.Org) == "" {
		return errors.New("precheck: mutation.Org is required")
	}
	if strings.TrimSpace(m.App) == "" {
		return errors.New("precheck: mutation.App is required")
	}
	if !endpointNameRE.MatchString(m.Name) {
		return fmt.Errorf("precheck: endpoint name %q must match %s", m.Name, endpointNameRE.String())
	}
	if m.Op == OpDelete {
		// Delete only requires Org/App/Name. Hostname/Port/Protocol
		// are not asserted on delete (we delete by name regardless of
		// what the old endpoint pointed at).
		return nil
	}
	if strings.TrimSpace(m.Hostname) == "" {
		return errors.New("precheck: hostname is required for create/update")
	}
	if len(m.Hostname) > 253 || !hostnameRE.MatchString(strings.ToLower(m.Hostname)) {
		return fmt.Errorf("precheck: hostname %q is not RFC-1123 valid", m.Hostname)
	}
	if m.Port < 0 || m.Port > 65535 {
		return fmt.Errorf("precheck: port %d out of range 0..65535", m.Port)
	}
	switch strings.ToLower(strings.TrimSpace(m.Protocol)) {
	case "", "https", "http", "grpc", "tcp", "udp":
	default:
		return fmt.Errorf("precheck: protocol %q not in {https,http,grpc,tcp,udp}", m.Protocol)
	}
	switch strings.ToLower(strings.TrimSpace(m.Visibility)) {
	case "", "public", "private", "internal":
	default:
		return fmt.Errorf("precheck: visibility %q not in {public,private,internal}", m.Visibility)
	}
	return nil
}

// CheckCertManager runs the cert-manager-precheck stage.
//
// Pass when: no existing Certificate covers the hostname, OR an
// existing Certificate already belongs to the same Application
// (re-issue / update case is benign), OR the existing Certificate is
// a wildcard (`*.<base-domain>`) and the new hostname falls under
// that base domain (G117 #2864 — Gap 6 of #2856).
//
// Fail when: an existing non-wildcard Certificate belongs to a
// different Application — issuing a duplicate would steal the cert
// from the existing owner.
//
// 503 ("lookup-unavailable") when the wired lookup function errors.
// The caller-side handler converts this into a precheck-pending state
// the caller can retry; we do NOT silently pass.
func CheckCertManager(ctx context.Context, m Mutation, lookup CertConflictLookup) Result {
	const stage = "cert-manager-precheck"
	if m.Op == OpDelete {
		// Delete is always safe from a cert-conflict standpoint —
		// removing the endpoint frees the Certificate.
		return Result{Stage: stage, Pass: true, Code: "delete-skip"}
	}
	if lookup == nil {
		return Result{
			Stage:   stage,
			Pass:    false,
			Code:    "lookup-unavailable",
			Message: "cert-manager lookup not wired; cannot evaluate conflict",
		}
	}
	owner, exists, err := lookup(ctx, m.Hostname)
	if err != nil {
		return Result{
			Stage:   stage,
			Pass:    false,
			Code:    "lookup-failed",
			Message: err.Error(),
		}
	}
	if !exists {
		return Result{Stage: stage, Pass: true, Code: "no-existing-cert"}
	}
	if strings.EqualFold(owner.Org, m.Org) && strings.EqualFold(owner.App, m.App) {
		return Result{Stage: stage, Pass: true, Code: "same-owner"}
	}
	// G117 #2864 (Gap 6): wildcard Certificate coverage auto-PASS.
	// When the Gateway's wildcard cert (`*.<sov-fqdn>`) already covers
	// the new endpoint hostname, the per-Application owner-mismatch is
	// benign — the new endpoint will reuse the wildcard cert via the
	// Gateway and no per-Application Certificate is required.
	if owner.IsWildcard && hostnameUnderWildcard(m.Hostname, owner.WildcardDomain) {
		return Result{
			Stage:   stage,
			Pass:    true,
			Code:    "covered-by-wildcard",
			Message: fmt.Sprintf("hostname %q is covered by wildcard Certificate for %q", m.Hostname, "*."+owner.WildcardDomain),
		}
	}
	return Result{
		Stage:   stage,
		Pass:    false,
		Code:    "cert-conflict",
		Message: fmt.Sprintf("hostname %q already has a Certificate owned by %s/%s", m.Hostname, owner.Org, owner.App),
	}
}

// hostnameUnderWildcard reports whether `hostname` is covered by a
// wildcard Certificate whose base domain is `base` (the suffix after
// `*.`).
//
// Wildcards in cert-manager / TLS spec terms cover EXACTLY ONE label
// of hostname depth — `*.example.com` covers `foo.example.com` but
// NOT `foo.bar.example.com` and NOT `example.com` itself. We mirror
// that constraint here so the precheck doesn't over-broadly approve.
func hostnameUnderWildcard(hostname, base string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	base = strings.ToLower(strings.TrimSpace(base))
	if hostname == "" || base == "" {
		return false
	}
	suffix := "." + base
	if !strings.HasSuffix(hostname, suffix) {
		return false
	}
	// Strip the matching suffix and assert the remainder is exactly
	// one DNS label (no further dots).
	prefix := strings.TrimSuffix(hostname, suffix)
	if prefix == "" || strings.Contains(prefix, ".") {
		return false
	}
	return true
}

// CheckDNSConflict runs the dns-conflict-precheck stage.
//
// Pass when: no existing PowerDNS record exists for `hostname`, OR
// the existing record points at the canonical Gateway IP for this
// Application (re-create after a stale delete is benign).
//
// Fail when: the existing record points at a different target —
// overwriting would silently re-route traffic. ExpectedTarget is the
// canonical Gateway endpoint hostname for the calling Org; when empty
// we treat ANY existing record as a conflict.
//
// 503 when the wired lookup errors. The handler converts to a
// precheck-pending state.
func CheckDNSConflict(ctx context.Context, m Mutation, expectedTarget string, lookup DNSConflictLookup) Result {
	const stage = "dns-conflict-precheck"
	if m.Op == OpDelete {
		// Delete is always safe — the record will be removed by the
		// PR. (If the record points at a different target, that's a
		// drift state, not a conflict.)
		return Result{Stage: stage, Pass: true, Code: "delete-skip"}
	}
	if lookup == nil {
		return Result{
			Stage:   stage,
			Pass:    false,
			Code:    "lookup-unavailable",
			Message: "PowerDNS lookup not wired; cannot evaluate conflict",
		}
	}
	target, exists, err := lookup(ctx, m.Hostname)
	if err != nil {
		return Result{
			Stage:   stage,
			Pass:    false,
			Code:    "lookup-failed",
			Message: err.Error(),
		}
	}
	if !exists {
		return Result{Stage: stage, Pass: true, Code: "no-existing-record"}
	}
	if expectedTarget != "" && strings.EqualFold(target, expectedTarget) {
		return Result{Stage: stage, Pass: true, Code: "same-target"}
	}
	return Result{
		Stage:   stage,
		Pass:    false,
		Code:    "dns-conflict",
		Message: fmt.Sprintf("hostname %q already resolves to %q (expected %q)", m.Hostname, target, expectedTarget),
	}
}

// CheckKyverno is the OPTIONAL local stub for the Kyverno admission
// gate. When `lookup` is nil we mark the stage `Pass=true,
// Code="deferred-to-repo-workflow"` so the PR opens and the in-repo
// Gitea Actions workflow is the source of truth.
func CheckKyverno(ctx context.Context, m Mutation, lookup KyvernoLookup) Result {
	const stage = "kyverno-admission"
	if lookup == nil {
		return Result{Stage: stage, Pass: true, Code: "deferred-to-repo-workflow"}
	}
	res, err := lookup(ctx, m)
	if err != nil {
		return Result{
			Stage:   stage,
			Pass:    false,
			Code:    "lookup-failed",
			Message: err.Error(),
		}
	}
	if res.Stage == "" {
		res.Stage = stage
	}
	return res
}

// Run executes all three stages and returns a Bundle. The caller
// decides what to do with the result; this function never opens a PR
// or mutates state.
func Run(
	ctx context.Context,
	m Mutation,
	expectedDNSTarget string,
	certLookup CertConflictLookup,
	dnsLookup DNSConflictLookup,
	kyvernoLookup KyvernoLookup,
) Bundle {
	return Bundle{
		Kyverno:     CheckKyverno(ctx, m, kyvernoLookup),
		CertManager: CheckCertManager(ctx, m, certLookup),
		DNSConflict: CheckDNSConflict(ctx, m, expectedDNSTarget, dnsLookup),
	}
}
