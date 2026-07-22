// Package huawei is the providers.CloudProvider adapter for Huawei
// Cloud (Stack — on-prem HCS).
//
// Architectural role: adapter (Ports + Adapters / hexagonal). Same
// structure as providers/hetzner — translates the
// providers.CloudProvider interface to:
//   - `internal/provisioner` (`tofu apply` / `tofu destroy` against
//     `infra/providers/huawei/`) for Provision + Wipe.
//   - A small Huawei-specific HTTP client (signed AK/SK Signature v3 —
//     see sigv3.go) for ValidateCreds + ListServers + orphan-purge.
//   - The same `minio-go` S3 client `internal/hetzner/buckets.go` uses,
//     pointed at the HCS OBS endpoint, for OBS bucket purge.
//
// Per docs/PRINCIPLES.md #14 (target-state shape, no workarounds): we
// implement the full CloudProvider interface for Huawei in this PR
// (Wave 3) so the platform can dispatch on `dep.Provider == "huawei"`
// end-to-end. Wave 4 is the live fresh-prov walk on a real HCS endpoint.
//
// Credentials shape (passed via providers.ProviderCreds.Raw):
//
//	access_key  — Huawei IAM access key (AK)
//	secret_key  — Huawei IAM secret key (SK)
//	project_id  — Huawei project ID (region-scoped)
//	region      — Huawei region code (default "me-east-215" — HCS)
//	insecure    — "true" to skip TLS verify (default for on-prem HCS)
//
// Tracking: Issue #2140 (Wave 3) + Issue #1841 (DoD A6 — provider-mix).

package huawei

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// Name is the canonical provider identifier ("huawei"). MUST match
// the enum row in core/controllers/environment/api/v1/types.go.
const Name = "huawei"

// defaultRegion is the operator-confirmed HCS region. Public Huawei
// Cloud customers override this via wizard input.
const defaultRegion = "me-east-215"

// httpTimeout caps every signed HTTP call. ValidateCreds is the
// latency-sensitive caller (the wizard's StepCredentials submit
// blocks on it); 10s comfortably covers a cold HCS endpoint.
var httpTimeout = 15 * time.Second

// Provider implements providers.CloudProvider for Huawei Cloud (Stack).
type Provider struct{}

// New returns a ready-to-use Huawei adapter. Cheap; no I/O.
func New() *Provider { return &Provider{} }

// init registers the adapter so providers.Get("huawei") returns a
// usable instance. Per registry.go RegisterProvider contract, this
// MUST be called from an init() — never from runtime code.
//
// Wave 5.93 (#2445): also wires RotateBlocklistedNATEIPs as the
// provisioner-package NATEIPPreflightHook so the provisioner's
// generic post-apply step calls into the Huawei-specific rotation
// without an import cycle.
func init() {
	providers.RegisterProvider(Name, New())
	provisioner.NATEIPPreflightHook = RotateBlocklistedNATEIPs
	provisioner.VPCQuotaHook = VPCQuotaPreflight
	provisioner.VPCReclaimHook = VPCReclaimPreflight
}

// VPCQuotaPreflight is the function registered as
// provisioner.VPCQuotaHook by init(). It is the same shape as
// (*Provider).VPCQuota but as a free function so the provisioner
// package can call it without importing huawei (preventing the
// provisioner ↔ huawei import cycle — huawei already imports
// provisioner for the Provision delegate).
func VPCQuotaPreflight(ctx context.Context, accessKey, secretKey, projectID, region string) (used, limit int, err error) {
	return New().VPCQuota(ctx, accessKey, secretKey, projectID, region)
}

// VPCReclaimPreflight is the function registered as
// provisioner.VPCReclaimHook by init() (#4431). Same free-function
// shape as VPCQuotaPreflight so the provisioner can reclaim leaked VPC
// quota in-band without importing huawei. Delegates to SweepOrphanVPCs.
//
// This is the in-band, provision-time quota-reclaim path (NOT the
// background janitor), so it passes destructive=true: a fresh prov that
// hit the VPC cap genuinely needs the leaked orphans gone to make room.
// The #4466 log-only gate is the background janitor's default; it does not
// apply to this explicit on-demand reclaim. Protect-by-default
// (activeDepIDPrefixes) still hard-protects every in-flight dep's VPC.
func VPCReclaimPreflight(ctx context.Context, accessKey, secretKey, projectID, region string, activeDepIDPrefixes map[string]struct{}, progress func(msg string)) (int, error) {
	return New().SweepOrphanVPCs(ctx, accessKey, secretKey, projectID, region, activeDepIDPrefixes, true, progress)
}

// Name returns the canonical provider name.
func (p *Provider) Name() string { return Name }

// regionFromCreds returns the region for one call. Falls back to
// the HCS default when the operator didn't override it (Wave 4
// extends the wizard's StepCredentials to surface this).
func regionFromCreds(creds providers.ProviderCreds) string {
	if r := creds.Get("region"); r != "" {
		return r
	}
	return defaultRegion
}

// endpointFor returns the per-service endpoint for one call.
// Pattern: `https://<service>.<region>.kom4dc.nationalcloud.om`.
//
// It is a package var (not a plain func) ONLY so tests can point the HCS
// service endpoints at a httptest server — production never reassigns it.
// (#4466: the orphan-sweep log-only/destructive contract is asserted
// end-to-end against a fake HCS by overriding this.)
var endpointFor = func(service, region string) string {
	return fmt.Sprintf("https://%s.%s.kom4dc.nationalcloud.om", service, region)
}

// httpClientFor returns a *http.Client configured for the operator's
// trust posture. On-prem HCS (default) skips TLS verify; public
// Huawei Cloud (operator-supplied insecure=false) verifies normally.
func httpClientFor(creds providers.ProviderCreds) *http.Client {
	insecure := creds.Get("insecure") != "false" // default true (on-prem HCS)
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &http.Client{
		Timeout:   httpTimeout,
		Transport: tr,
	}
}

// credsFromProviderCreds extracts the signing AK/SK + project_id.
// Returns an error when any required field is missing so the caller
// surfaces a clear 400 rather than a 401 several seconds in.
func credsFromProviderCreds(c providers.ProviderCreds) (hwCreds, error) {
	out := hwCreds{
		AccessKey: c.Get("access_key"),
		SecretKey: c.Get("secret_key"),
		ProjectID: c.Get("project_id"),
	}
	if out.AccessKey == "" {
		return out, errors.New("huawei: access_key is required")
	}
	if out.SecretKey == "" {
		return out, errors.New("huawei: secret_key is required")
	}
	if out.ProjectID == "" {
		return out, errors.New("huawei: project_id is required")
	}
	return out, nil
}

// ValidateCreds issues a cheap GET against /v1/<project_id>/vpcs to
// confirm the AK/SK + project_id triplet authenticates. Mirrors the
// shape internal/hetzner/client.go uses against /v1/servers.
//
// Returns nil when the creds authenticate (200 OK). Returns a
// rejection error (containing the substring "rejected") on 401/403
// so the wizard's error-card machinery branches on the same shape
// the Hetzner adapter uses (see handler/credentials.go).
func (p *Provider) ValidateCreds(ctx context.Context, creds providers.ProviderCreds) error {
	hw, err := credsFromProviderCreds(creds)
	if err != nil {
		return err
	}
	region := regionFromCreds(creds)
	url := fmt.Sprintf("%s/v1/%s/vpcs?limit=1", endpointFor("vpc", region), hw.ProjectID)

	client := httpClientFor(creds)
	body, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("huawei: validate request failed: %w", err)
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("huawei: AK/SK pair was rejected by IAM (401/403)")
	default:
		return fmt.Errorf("huawei: unexpected status %d from VPC endpoint: %s",
			status, snippet(body, 240))
	}
}

// Provision shells out to `tofu apply` against
// infra/providers/huawei/ via the canonical internal/provisioner
// package. The catalyst-api writes the operator's AK/SK + project_id
// into tofu.auto.tfvars.json at workdir-prep time (mirroring how the
// Hetzner adapter writes `hcloud_token`).
//
// MUST be idempotent: re-running on a deployment whose tofu workdir
// already holds state returns the existing outputs without re-creating
// cloud resources.
func (p *Provider) Provision(ctx context.Context, spec providers.ProvisionSpec, events chan<- providers.ProvisionEvent) (*providers.ProvisionResult, error) {
	if len(spec.Regions) == 0 {
		return nil, errors.New("huawei provider: at least one region required")
	}
	if _, err := credsFromProviderCreds(spec.Creds); err != nil {
		return nil, err
	}
	req, err := toProvisionerRequest(spec)
	if err != nil {
		return nil, err
	}

	// Bridge the cloud-agnostic event channel into the provisioner's
	// concrete provisioner.Event channel — same plumbing the Hetzner
	// adapter uses.
	provEvents := make(chan provisioner.Event, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range provEvents {
			select {
			case events <- providers.ProvisionEvent{
				Time:    e.Time,
				Phase:   e.Phase,
				Level:   e.Level,
				Message: e.Message,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	prov := provisioner.New()
	res, err := prov.Provision(ctx, req, provEvents)
	close(provEvents)
	<-done
	if err != nil {
		return nil, err
	}
	return &providers.ProvisionResult{
		SovereignFQDN:         res.SovereignFQDN,
		ControlPlaneIP:        res.ControlPlaneIP,
		LoadBalancerIP:        res.LoadBalancerIP,
		ConsoleLoadBalancerIP: res.ConsoleLoadBalancerIP, // #4053 — dedicated console ELB
		ConsoleURL:            res.ConsoleURL,
		GitOpsRepoURL:         res.GitOpsRepoURL,
		KubeconfigPath:        res.KubeconfigPath,
	}, nil
}

// Wipe runs the canonical purge sequence:
//
//  1. `tofu destroy` against the per-deployment workdir.
//  2. Huawei-side orphan sweep — query ECS / VPC / EIP / SG by the
//     canonical `catalyst.openova.io/deployment-id` tag and delete in
//     dependency order (ECS → EIP → SG → VPC).
//  3. OBS bucket purge via the same minio-go client the Hetzner
//     adapter uses, pointed at the HCS OBS endpoint.
//
// Idempotent — re-calling on an already-wiped deployment returns a
// report with zero deletions and no error.
func (p *Provider) Wipe(ctx context.Context, spec providers.WipeSpec, progress func(msg string)) (*providers.WipeResult, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if spec.SovereignFQDN == "" {
		return nil, errors.New("huawei provider: SovereignFQDN required for Wipe")
	}
	hw, err := credsFromProviderCreds(spec.Creds)
	if err != nil {
		return nil, err
	}

	out := &providers.WipeResult{
		DeploymentID:  spec.DeploymentID,
		SovereignFQDN: spec.SovereignFQDN,
		ProviderPurge: map[string][]string{},
		WipedAt:       time.Now().UTC(),
	}

	// Step 1 — tofu destroy. Build a destroy Request that carries the
	// Huawei creds parsed from spec.Creds so writeTfvars renders a
	// tfvars file with valid AK/SK/project_id/region — provisioner.Destroy
	// re-stages the module + re-writes tfvars at provisioner.go:1252-1256,
	// so leaving these empty would OVERWRITE the on-disk tfvars with
	// empty creds and tofu would fail with 401 verify aksk signature.
	// Surfaced live by #2428 during the inline #2423 wipe-recreate.
	destroyReq := provisioner.Request{
		DeploymentID:    spec.DeploymentID,
		SovereignFQDN:   spec.SovereignFQDN,
		Provider:        "huawei",
		HuaweiAccessKey: hw.AccessKey,
		HuaweiSecretKey: hw.SecretKey,
		HuaweiProjectID: hw.ProjectID,
		HuaweiRegion:    regionFromCreds(spec.Creds),
	}
	provEvents := make(chan provisioner.Event, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range provEvents {
			progress("tofu: " + e.Message)
		}
	}()
	prov := provisioner.New()
	if err := prov.Destroy(ctx, destroyReq, provEvents); err != nil {
		out.Errors = append(out.Errors, "tofu destroy: "+err.Error())
	} else {
		out.TofuDestroyed = true
	}
	close(provEvents)
	<-done

	// Step 2 + 4 (#3065) — Huawei orphan sweep with verify-and-re-purge
	// retry. Best-effort: per-resource failures land in out.Errors but do
	// not abort the remaining sweeps.
	//
	// Why the retry: the async NAT/port/subnet teardown on HCS Kom4DC can
	// lag the first purge pass. A NAT gateway whose delete is still
	// settling keeps a router/dhcp port bound in its subnet, so the
	// subnet→VPC delete 409s ("Router contains subnets" / "Subnet used by
	// routes" / "Port device_owner not empty") and the VPC is left
	// orphaned. The single-pass design only DETECTED that residual (it
	// lands in out.ResidualOrphans via verifyZeroOrphans) — it never got
	// cleaned, so HCS VPC + EIP quota leaked and the NEXT fresh prov hit
	// the quota precheck ("project at 5/5 VPCs"). Verified live: hw94/hw96
	// VPCs had to be deleted by hand from the bastion (NAT-snat-rules → NAT
	// → subnet → VPC). Fix: re-run the purge up to maxPurgePasses, letting
	// the async teardown settle PROGRESSIVELY (20s·pass, so ~5min total
	// across the gaps) between attempts, until the verify finds zero
	// quota-relevant residuals (VPC/NAT/EIP — the resources that gate a
	// re-prov). The HCS NAT/VPC teardown can lag minutes after the delete
	// call (memory reference_hcs_vpc_quota_wipe_then_prov_lag: ~10-20min in
	// the extreme), so a flat 60s of retries could not outlast it; a growing
	// settle covers the common multi-minute case, and any residual still
	// present after the passes is recorded in out.ResidualOrphans for the
	// G104 zero-touch gate (never silently leaked). bastion-* stays
	// hard-protected throughout (verifyZeroOrphans + every list*ByName
	// helper exclude it).
	region := regionFromCreds(spec.Creds)
	client := httpClientFor(spec.Creds)
	const maxPurgePasses = 6
	for pass := 1; pass <= maxPurgePasses; pass++ {
		if err := purgeHuaweiResources(ctx, client, hw, region, spec.SovereignFQDN, spec.DeploymentID, progress, out); err != nil {
			out.Errors = append(out.Errors, "huawei orphan purge: "+err.Error())
		}
		// Fresh verify each pass — reset the residual map + flag so a
		// clean pass doesn't inherit stale entries from a prior one.
		out.ResidualOrphans = map[string][]string{}
		out.VerifiedZeroOrphans = false
		verifyZeroOrphans(ctx, client, hw, region, spec.SovereignFQDN, progress, out)
		// #4046 (2026-06-21 live audit): break on the FULL zero-orphans
		// verdict, not just the 3-kind quota sum (networks/nat_gateways/
		// floating_ips). The old quota-only condition broke the retry the
		// instant VPC/NAT/EIP were clean even when a security group (or
		// server / load_balancer / keypair) survived — typically a 409 on
		// deleteSG while the region's ports were still settling. Because SGs
		// are non-quota resources they fell outside the break sum, so the
		// loop exited and ONE region's security group leaked on EVERY wipe
		// (hw40/hw77/hw180 forensic). verifyZeroOrphans already records SG
		// residuals under out.ResidualOrphans["firewalls"] and sets
		// VerifiedZeroOrphans=false whenever ANY kind survives, so gating the
		// break on that flag keeps re-purging (with the per-pass SG retry
		// above) until the account is genuinely clean — no early exit.
		totalResidual := 0
		for _, names := range out.ResidualOrphans {
			totalResidual += len(names)
		}
		if wipeRetryShouldBreak(out) {
			break
		}
		if pass < maxPurgePasses {
			settle := time.Duration(20*pass) * time.Second
			progress(fmt.Sprintf("post-wipe verify found %d residual orphan(s) across all kinds (incl. SG/server/LB/keypair, not just VPC/NAT/EIP) — re-purging pass %d/%d after %s async-teardown settle (#3065, #4046)", totalResidual, pass+1, maxPurgePasses, settle))
			select {
			case <-ctx.Done():
			case <-time.After(settle):
			}
		}
	}

	// Dedup ProviderPurge — across the retry passes a resource that took
	// more than one pass to delete (listed-then-failed, then deleted on a
	// later pass) gets appended more than once, so the success report would
	// over-count it. Extracted to a tested helper (#3070 reviewer note,
	// sharper now that maxPurgePasses=6).
	dedupProviderPurge(out)

	// #4046 (2026-06-21 live audit) — EIP-egress cooldown preflight.
	//
	// THE REAL region-a-never-bootstraps fix. kom4dc's small EIP free-pool +
	// rapid wipe→re-prov recycles RECENTLY-RELEASED EIPs back into the very
	// next allocate(). A just-released EIP stays reputation-blocklisted for
	// outbound to harbor.openova.io / ghcr "for hours" after release, so when
	// the immediate next prov's NAT SNAT draws it, that region's cloud-init
	// image pull fails, the region never PUTs its kubeconfig, and the prov
	// dies on the phase-1 15-minute watch timeout (region-a "created
	// everything but no egress" wedge).
	//
	// preflight_eip.go already LEARNS poison it rotated away from at
	// preflight time, but only AFTER a prov has drawn + rotated it — the
	// FIRST prov to draw a freshly-released EIP still wedges. Active
	// egress-validation from the control plane is NOT feasible: catalyst-api
	// runs on Contabo, OUTSIDE the Huawei VPC, so it cannot reachability-probe
	// a Huawei EIP's egress to harbor without an ECS in-VPC with that EIP
	// attached (the chicken-and-egg documented in preflight_eip.go's header).
	// So we take the fallback the operator described: record EVERY EIP this
	// wipe just released into the SAME cooldown blocklist store
	// (eip_blocklist_store.go), TTL-bounded so a genuinely-recovered address
	// ages back into rotation. blocklist() merges this store on the next
	// prov, so the immediate re-prov auto-avoids every address this wipe
	// freed — exactly "wait for dirty EIPs to clear". Non-fatal: a failure to
	// persist only loses the cooldown memory for these addresses, it never
	// fails the wipe.
	releasedEIPs := append([]string(nil), out.ProviderPurge["floating_ips"]...)
	// Also cool down any EIP that survived the purge (still in HCS) — it was
	// in service for this Sovereign and will be released/recycled soon; the
	// next prov must avoid it too. These are recorded in out.Errors as
	// "survived" but we want their addresses in the cooldown set as well.
	for _, addr := range out.ResidualOrphans["floating_ips"] {
		releasedEIPs = append(releasedEIPs, addr)
	}
	if len(releasedEIPs) > 0 {
		note := "wipe:" + spec.DeploymentID
		if rerr := recordPoisonedEIPs(releasedEIPs, note, time.Now()); rerr != nil {
			progress(fmt.Sprintf("#4046: could not persist %d released EIP(s) to the cooldown blocklist (non-fatal): %v", len(releasedEIPs), rerr))
		} else {
			progress(fmt.Sprintf("#4046: recorded %d released EIP(s) to the cooldown blocklist — the immediate next prov will not re-draw a freshly-freed (reputation-poisoned) EIP", len(releasedEIPs)))
		}
	}

	// Step 3 — OBS bucket purge (best effort).
	endpoint := fmt.Sprintf("obs.%s.kom4dc.nationalcloud.om", region)
	bucketCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	removed, berr := purgeOBSBuckets(bucketCtx, endpoint, hw, region, spec.SovereignFQDN, progress)
	if berr != nil {
		out.Errors = append(out.Errors, "obs bucket purge: "+berr.Error())
	}
	prefix := BucketNamePrefixForSovereign(spec.SovereignFQDN)
	for i := 0; i < removed; i++ {
		out.S3Buckets = append(out.S3Buckets, prefix+"-*")
	}

	return out, nil
}

// dedupProviderPurge collapses each ProviderPurge kind to unique names.
// The verify-and-re-purge retry (Wipe) can append the same resource name
// on more than one pass — listed-then-delete-failed on an early pass, then
// deleted on a later pass — which would over-count the success report
// (sharper now that maxPurgePasses=6). In-place dedup preserves order and
// reuses each slice's backing array. (#3070 reviewer note; #3065.)
func dedupProviderPurge(out *providers.WipeResult) {
	if out == nil {
		return
	}
	for k, names := range out.ProviderPurge {
		seen := make(map[string]bool, len(names))
		uniq := names[:0]
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				uniq = append(uniq, n)
			}
		}
		out.ProviderPurge[k] = uniq
	}
}

// wipeRetryShouldBreak decides whether the Wipe() purge-retry loop may
// stop. #4046 (2026-06-21 live audit): it returns true ONLY when the
// post-pass verify found ZERO residual orphans across EVERY resource kind
// — i.e. out.VerifiedZeroOrphans is true. The previous inline condition
// summed only the three quota-relevant kinds (networks + nat_gateways +
// floating_ips), so a security group (or server / load_balancer / keypair)
// that survived a 409-on-port-settle fell outside the sum and the loop
// broke early, leaking one region's SG on EVERY wipe. Gating on the full
// zero-orphans verdict keeps re-purging until the account is genuinely
// clean. verifyZeroOrphans is the sole writer of VerifiedZeroOrphans and
// already accounts for firewalls/servers/load_balancers/keypairs, so this
// stays a thin, single-source predicate.
func wipeRetryShouldBreak(out *providers.WipeResult) bool {
	if out == nil {
		return true // nothing to retry against
	}
	return out.VerifiedZeroOrphans
}

// verifyZeroOrphans walks every cleanup-bearing resource kind and
// records any catalyst-<stem>- prefixed entries (excluding bastion-*)
// in out.ResidualOrphans. Sets out.VerifiedZeroOrphans=true when the
// scan finds zero residuals across every kind — that is the canonical
// "wipe was complete" signal the operator + CI gate read.
//
// G103 (Refs #2670) acceptance: post-wipe account must contain
// 0 catalyst-* VPCs / EIPs / NATs / ECSs / keypairs (the bastion-*
// fleet is hard-protected throughout this codebase per
// memory feedback_hcs_kom4dc_wipe_cascade_quirks bastion-IP
// preservation rule).
func verifyZeroOrphans(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string, progress func(msg string), out *providers.WipeResult) {
	if sovereignFQDN == "" {
		return
	}
	progress("verifying zero orphans (G103 #2670 post-wipe gate)")
	if out.ResidualOrphans == nil {
		out.ResidualOrphans = map[string][]string{}
	}

	// ECS residuals.
	if servers, err := listECSByNamePrefix(ctx, client, hw, region, sovereignFQDN, ""); err == nil {
		for _, s := range servers {
			if strings.HasPrefix(s.Name, "bastion") || strings.Contains(s.Name, "bastion") {
				continue
			}
			out.ResidualOrphans["servers"] = append(out.ResidualOrphans["servers"], s.Name)
		}
	}

	// EIP residuals — name-prefix-matched (catalyst-<stem>- bandwidth name).
	eipSeen := map[string]bool{}
	if eips, err := listEIPsByNamePrefix(ctx, client, hw, region, sovereignFQDN, ""); err == nil {
		for _, e := range eips {
			if strings.HasPrefix(e.Address, "bastion") {
				continue
			}
			if eipSeen[e.Address] {
				continue
			}
			eipSeen[e.Address] = true
			out.ResidualOrphans["floating_ips"] = append(out.ResidualOrphans["floating_ips"], e.Address)
		}
	}
	// #4636 — UNBOUND / nameless EIP residuals. The name-prefix scan above
	// is blind to an EIP that has no catalyst-<stem>- bandwidth name (the
	// live 212.72.24.36 leak on the 8fd457a8 wipe). Sweep the WHOLE project
	// for NON-bastion UNBOUND (port_id == "") EIPs and record any survivor
	// so the G103 zero-orphans verdict FAILS instead of falsely passing.
	// The bastion EIP is BOUND (port_id set) + literal-IP/name guarded, so
	// it is never flagged.
	if orphanEIPs, err := listUnboundOrphanEIPs(ctx, client, hw, region); err == nil {
		for _, e := range orphanEIPs {
			if eipSeen[e.Address] {
				continue
			}
			eipSeen[e.Address] = true
			out.ResidualOrphans["floating_ips"] = append(out.ResidualOrphans["floating_ips"], e.Address)
		}
	}

	// NAT residuals.
	if nats, err := listNATsByName(ctx, client, hw, region, sovereignFQDN); err == nil {
		for _, n := range nats {
			if strings.HasPrefix(n.Name, "bastion") {
				continue
			}
			out.ResidualOrphans["nat_gateways"] = append(out.ResidualOrphans["nat_gateways"], n.Name)
		}
	}

	// VPC residuals.
	if vpcs, err := listVPCsByName(ctx, client, hw, region, sovereignFQDN); err == nil {
		for _, v := range vpcs {
			if strings.HasPrefix(v.Name, "bastion") {
				continue
			}
			out.ResidualOrphans["networks"] = append(out.ResidualOrphans["networks"], v.Name)
		}
	}

	// SG residuals.
	if sgs, err := listSGsByName(ctx, client, hw, region, sovereignFQDN); err == nil {
		for _, sg := range sgs {
			if strings.HasPrefix(sg.Name, "bastion") {
				continue
			}
			out.ResidualOrphans["firewalls"] = append(out.ResidualOrphans["firewalls"], sg.Name)
		}
	}

	// ELB residuals.
	if elbs, err := listELBsByName(ctx, client, hw, region, sovereignFQDN); err == nil {
		for _, lb := range elbs {
			if strings.HasPrefix(lb.Name, "bastion") {
				continue
			}
			out.ResidualOrphans["load_balancers"] = append(out.ResidualOrphans["load_balancers"], lb.Name)
		}
	}

	// Keypair residuals — list ALL keypairs in project, filter by stem
	// prefix excluding bastion. Mirrors sweepKeypairs's listing logic.
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	kpBase := fmt.Sprintf("%s/v2/%s/os-keypairs", endpointFor("ecs", region), hw.ProjectID)
	resp, _, _ := doSignedRequest(client, hw, http.MethodGet, kpBase, nil)
	var keyList struct {
		Keypairs []struct {
			Keypair struct {
				Name string `json:"name"`
			} `json:"keypair"`
		} `json:"keypairs"`
	}
	_ = json.Unmarshal(resp, &keyList)
	for _, k := range keyList.Keypairs {
		name := k.Keypair.Name
		if strings.HasPrefix(name, "bastion") || strings.Contains(name, "bastion") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out.ResidualOrphans["keypairs"] = append(out.ResidualOrphans["keypairs"], name)
	}

	// Compute the zero-orphans verdict + log the residuals (if any) so
	// the operator's SSE banner + the CI gate see the same signal.
	totalResiduals := 0
	for _, names := range out.ResidualOrphans {
		totalResiduals += len(names)
	}
	if totalResiduals == 0 {
		out.VerifiedZeroOrphans = true
		progress("verified zero orphans — wipe contract honoured")
		// Drop the empty map so consumers can compare against nil.
		out.ResidualOrphans = nil
		return
	}
	out.VerifiedZeroOrphans = false
	out.Errors = append(out.Errors, fmt.Sprintf(
		"G103 wipe-contract violation: %d catalyst-* resource(s) survived wipe (bastion-* excluded) — see ResidualOrphans",
		totalResiduals,
	))
	for kind, names := range out.ResidualOrphans {
		progress(fmt.Sprintf("RESIDUAL ORPHAN: %d %s entries survived wipe: %v", len(names), kind, names))
	}
}

// ListServers returns the Huawei-side ECS inventory for one
// deployment by querying the ECS API filtered by the canonical
// `catalyst.openova.io/deployment-id` tag.
func (p *Provider) ListServers(ctx context.Context, deploymentID, sovereignFQDN string, creds providers.ProviderCreds) ([]providers.ServerInfo, error) {
	hw, err := credsFromProviderCreds(creds)
	if err != nil {
		return nil, err
	}
	region := regionFromCreds(creds)
	client := httpClientFor(creds)

	servers, err := listECSByTag(ctx, client, hw, region, deploymentID, sovereignFQDN)
	if err != nil {
		return nil, fmt.Errorf("huawei: list ECS: %w", err)
	}
	out := make([]providers.ServerInfo, 0, len(servers))
	for _, s := range servers {
		out = append(out, providers.ServerInfo{
			ID:        s.ID,
			Name:      s.Name,
			Region:    region,
			PublicIP:  s.PublicIP,
			PrivateIP: s.PrivateIP,
			Status:    s.Status,
			Labels:    map[string]string{"flavor": s.Flavor},
		})
	}
	return out, nil
}

// HasServersByNamePrefix reports whether ANY ECS bearing the deployment's
// canonical name prefix (`catalyst-<fqdn-dashed>-<dep8>-…`, the
// local.name_prefix from infra/providers/huawei/main.tf) still exists in the
// project. #5327 — the janitor's failed-record reap gate needs a
// tag-independent liveness probe: Wave 5.4 disabled tags on HCS, so the
// tag-filtered ListServers returns zero even for a LIVE deployment — the
// exact blindness Wave 5.147 closed for the wipe's purge path with
// listECSByNamePrefix. This exposes that same fallback read-only, so a zero
// tag-scoped inventory is never mistaken for "infra gone" on HCS.
func (p *Provider) HasServersByNamePrefix(ctx context.Context, ak, sk, projectID, region, sovereignFQDN, deploymentID string) (bool, error) {
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})
	servers, err := listECSByNamePrefix(ctx, client, hw, region, sovereignFQDN, deploymentID)
	if err != nil {
		return false, err
	}
	return len(servers) > 0, nil
}

// BucketNameForDeployment returns the deterministic per-deployment
// OBS bucket name. Pattern mirrors Hetzner's shape so the wipe
// handler can reproduce the same name post-restart.
// Implements providers.ObjectStorageNamer.
func (p *Provider) BucketNameForDeployment(sovereignFQDN, deploymentID string) string {
	return BucketNameForSovereign(sovereignFQDN, deploymentID)
}

// BucketNamePrefixForSovereign returns the Sovereign-scoped prefix
// every OBS bucket for one Sovereign shares. Pure function.
// Implements providers.ObjectStorageNamer.
func (p *Provider) BucketNamePrefixForSovereign(sovereignFQDN string) string {
	return BucketNamePrefixForSovereign(sovereignFQDN)
}

// BucketNameForSovereign returns the canonical per-Sovereign +
// per-deployment OBS bucket name. Format mirrors Hetzner's:
// `catalyst-<fqdn-dashed>-<dep-id-prefix>`.
//
// Exposed as a top-level function (not just a method) so test code
// can pin the contract.
func BucketNameForSovereign(fqdn, deploymentID string) string {
	stem := "catalyst-" + strings.ReplaceAll(fqdn, ".", "-")
	suffix := bucketSuffix(deploymentID)
	if suffix == "" {
		return stem
	}
	return stem + "-" + suffix
}

// BucketNamePrefixForSovereign returns the prefix every per-Sovereign
// OBS bucket shares (matches both the legacy no-suffix shape and the
// suffix-bearing shape).
func BucketNamePrefixForSovereign(fqdn string) string {
	return "catalyst-" + strings.ReplaceAll(fqdn, ".", "-")
}

// bucketSuffix mirrors internal/hetzner/buckets.go's suffix derivation
// — take the first 8 hex chars of the deployment ID when it's long
// enough, otherwise empty (legacy records).
func bucketSuffix(deploymentID string) string {
	id := strings.ToLower(strings.TrimSpace(deploymentID))
	if len(id) < 8 {
		return ""
	}
	for _, r := range id[:8] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return id[:8]
}

// ── ECS / VPC / EIP / SG sweep + OBS bucket purge ─────────────────────────

// hwECSServer is the minimal projection of the Huawei ECS list
// response we care about.
type hwECSServer struct {
	ID        string
	Name      string
	Status    string
	Flavor    string
	PublicIP  string
	PrivateIP string
}

// listECSByTag queries the ECS API for instances tagged with the
// deployment-id. Falls back to the sovereign-fqdn tag when the
// deployment-id is empty (legacy callers).
func listECSByTag(ctx context.Context, client *http.Client, hw hwCreds, region, deploymentID, sovereignFQDN string) ([]hwECSServer, error) {
	// Huawei ECS list-by-tag endpoint:
	//   POST /v1/<project_id>/cloudservers/resource_instances/action
	// with body `{"action": "filter", "tags": [{"key": "...", "values": ["..."]}]}`.
	body := buildECSTagFilterBody(deploymentID, sovereignFQDN)
	url := fmt.Sprintf("%s/v1/%s/cloudservers/resource_instances/action",
		endpointFor("ecs", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		// 404 on an empty deployment is success-with-no-servers.
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("ECS list-by-tag: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		Resources []struct {
			ResourceID     string `json:"resource_id"`
			ResourceName   string `json:"resource_name"`
			ResourceDetail struct {
				Status    string          `json:"status"`
				Flavor    json.RawMessage `json:"flavor"`
				Addresses map[string][]struct {
					Addr string `json:"addr"`
					Type string `json:"OS-EXT-IPS:type"`
				} `json:"addresses"`
			} `json:"resource_detail"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode ECS list: %w", err)
	}
	out := make([]hwECSServer, 0, len(decoded.Resources))
	for _, r := range decoded.Resources {
		s := hwECSServer{
			ID:     r.ResourceID,
			Name:   r.ResourceName,
			Status: r.ResourceDetail.Status,
		}
		// Flavor projection — Huawei encodes it as either a string ID or
		// an inline {id: "...", ...} object depending on the API
		// version; handle both.
		if len(r.ResourceDetail.Flavor) > 0 {
			var asStr string
			if err := json.Unmarshal(r.ResourceDetail.Flavor, &asStr); err == nil {
				s.Flavor = asStr
			} else {
				var asObj struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(r.ResourceDetail.Flavor, &asObj); err == nil {
					s.Flavor = asObj.ID
				}
			}
		}
		for _, addrs := range r.ResourceDetail.Addresses {
			for _, a := range addrs {
				switch a.Type {
				case "floating":
					s.PublicIP = a.Addr
				case "fixed":
					s.PrivateIP = a.Addr
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// buildECSTagFilterBody constructs the JSON request body for the
// ECS list-by-tag endpoint. Filters on `deployment-id` when
// supplied; otherwise on the sovereign label.
func buildECSTagFilterBody(deploymentID, sovereignFQDN string) []byte {
	tagKey := "catalyst.openova.io/sovereign"
	tagValue := sovereignFQDN
	if deploymentID != "" {
		tagKey = "catalyst.openova.io/deployment-id"
		tagValue = deploymentID
	}
	body := map[string]any{
		"action": "filter",
		"tags": []map[string]any{
			{
				"key":    tagKey,
				"values": []string{tagValue},
			},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// purgeHuaweiResources sweeps ECS → EIP → SG → VPC in dependency
// order, deleting every resource tagged with the deployment-id or
// sovereign label.
//
// Wave 5.147 (2026-05-27): also list ECSs by NAME prefix (not just
// tag) because the huaweicloud_compute_instance resource in
// infra/providers/huawei/main.tf does NOT set any `tags` attribute —
// Wave 5.4 disabled tags due to HCS TMS endpoint divergence. So
// listECSByTag returned zero results across every prior wipe and the
// orphan ECSs from failed tofu destroys piled up unseen. By 2026-05-27
// hw30 #17 alone had left 6 stranded s7n.large.4 ECSs which combined
// with hw29's legitimate consumption to exhaust the HCS scheduler's
// flavor pool — the real cause of the "Common.0021: No valid host"
// failures the prior 11 waves chased a wrong theory for.
//
// Name-prefix fallback uses the same pattern as Wave 5.138 EIP sweep:
// catalyst-<sovereign-stem>-<dep-id-short>-* identifies catalyst-owned
// ECSs regardless of tag state, and excluding by stem keeps neighbor
// Sovereigns' ECSs safe.
func purgeHuaweiResources(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN, deploymentID string, progress func(msg string), out *providers.WipeResult) error {
	// Wave 5.153 (2026-05-27): full cascade purge in proper dependency
	// order. Prior versions only handled ECS+EIP+SG+VPC — but ELB,
	// listeners, pools, NAT gateways with snat_rules, and subnet ports
	// were all left stranded, blocking subnet+VPC delete via dependency
	// chain 409s. Live witnessed on hw29/hw30/hw31 after canonical wipe:
	//   - 3 ELBs stuck (listeners/pools holding the LB)
	//   - 3 NATs stuck (snat_rules not deleted)
	//   - 4 VPCs stuck (subnets with router/dhcp ports still bound)
	//   - 1 SG stuck (referenced by stranded ports)
	// The root cause of every "wipe leaves rubbish" report this session
	// (#2517, #2519). New cascade order:
	//   1. ECS (release ports + EIP bindings via delete_publicip:true)
	//   2. ELB → listeners → pools → members → healthmonitors → LB
	//      (frees ELB-attached EIPs)
	//   3. NAT → snat_rules → dnat_rules → NAT itself (frees NAT EIP)
	//   4. EIP (now all unbound)
	//   5. SG (after ECSs gone and ELBs/NATs gone, no port refs)
	//   6. Subnet ports cleanup (custom router/non-network ports)
	//   7. Subnets
	//   8. VPC (last — only after all subnets gone)

	// 1. ECS — delete first. tag + name-prefix union for full coverage.
	servers, err := listECSByTag(ctx, client, hw, region, deploymentID, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list ECS by tag: "+err.Error())
	}
	byName, nerr := listECSByNamePrefix(ctx, client, hw, region, sovereignFQDN, deploymentID)
	if nerr != nil {
		out.Errors = append(out.Errors, "list ECS by name-prefix: "+nerr.Error())
	}
	seen := map[string]bool{}
	for _, s := range servers {
		seen[s.ID] = true
	}
	for _, s := range byName {
		if !seen[s.ID] {
			servers = append(servers, s)
			seen[s.ID] = true
		}
	}
	for _, s := range servers {
		if err := deleteECS(ctx, client, hw, region, s.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete ECS %s: %s", s.Name, err))
			continue
		}
		out.ProviderPurge["servers"] = append(out.ProviderPurge["servers"], s.Name)
		progress("deleted ECS " + s.Name)
	}

	// 2. ELB cascade (Wave 5.153) — listeners → pools → members →
	// healthmonitors → LB. This frees ELB-attached EIPs.
	elbs, eerr := listELBsByName(ctx, client, hw, region, sovereignFQDN)
	if eerr != nil {
		out.Errors = append(out.Errors, "list ELB: "+eerr.Error())
	}
	for _, lb := range elbs {
		if err := cascadeDeleteELB(ctx, client, hw, region, lb.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete ELB %s: %s", lb.Name, err))
			continue
		}
		out.ProviderPurge["load_balancers"] = append(out.ProviderPurge["load_balancers"], lb.Name)
		progress("deleted ELB " + lb.Name)
	}

	// 3. NAT cascade (Wave 5.153) — snat_rules → dnat_rules → NAT
	// itself. NAT.0205 "Rule has not been deleted" was the consistent
	// HCS rejection witnessed on hw29/hw30/hw31 when canonical wipe
	// tried to delete NAT without clearing rules first.
	nats, naterr := listNATsByName(ctx, client, hw, region, sovereignFQDN)
	if naterr != nil {
		out.Errors = append(out.Errors, "list NAT: "+naterr.Error())
	}
	for _, n := range nats {
		if err := cascadeDeleteNAT(ctx, client, hw, region, n.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete NAT %s: %s", n.Name, err))
			continue
		}
		out.ProviderPurge["nat_gateways"] = append(out.ProviderPurge["nat_gateways"], n.Name)
		progress("deleted NAT " + n.Name)
	}

	// 4. EIP — by-tag (returns 0 with tags disabled) + name-prefix
	// fallback so untagged EIPs still get found.
	eips, err := listEIPsByTag(ctx, client, hw, region, deploymentID, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list EIP by tag: "+err.Error())
	}
	eipsByName, _ := listEIPsByNamePrefix(ctx, client, hw, region, sovereignFQDN, deploymentID)
	eipSeen := map[string]bool{}
	for _, e := range eips {
		eipSeen[e.ID] = true
	}
	for _, e := range eipsByName {
		if !eipSeen[e.ID] {
			eips = append(eips, e)
		}
	}
	// Wave 5.160 G13 (Refs #2545 hw40 forensic 2026-05-28): the first
	// deleteEIP pass can return 200 OK while HCS reports `floatingip:
	// associated_with_loadbalancer` or `still attached to ECS`. Surfaced
	// live on hw40 wipe — `ProviderPurge["floating_ips"]` listed 5
	// addresses as "deleted" but a fresh GET /publicips post-wipe showed
	// all 5 still present with status=ACTIVE. Caused publicIp quota
	// (10) exhaustion on the very next prov.
	//
	// Mitigation: run the delete loop UP TO 3 times with backoff. After
	// each pass, re-list and only keep `floating_ips` entries that are
	// actually gone. Records that survive 3 passes are logged in
	// `out.Errors` so the operator sees the real residual.
	const eipPasses = 3
	eipDone := map[string]string{}
	for pass := 1; pass <= eipPasses; pass++ {
		for _, e := range eips {
			if _, ok := eipDone[e.ID]; ok {
				continue
			}
			if err := deleteEIP(ctx, client, hw, region, e.ID); err != nil {
				if pass == eipPasses {
					out.Errors = append(out.Errors, fmt.Sprintf("delete EIP %s (pass %d): %s", e.Address, pass, err))
				}
				continue
			}
			eipDone[e.ID] = e.Address
			progress(fmt.Sprintf("deleted EIP %s (pass %d)", e.Address, pass))
		}
		// Re-list to verify; drop any IDs that came back from the dead.
		stillThere, _ := listEIPsByNamePrefix(ctx, client, hw, region, sovereignFQDN, deploymentID)
		survived := map[string]bool{}
		for _, e := range stillThere {
			survived[e.ID] = true
		}
		for id := range eipDone {
			if survived[id] {
				delete(eipDone, id) // not actually deleted; will retry next pass
			}
		}
		if len(eipDone) == len(eips) {
			break
		}
		// Brief backoff between passes so HCS can finish detach work
		// (LB unbind, ECS port release).
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(2*pass) * time.Second):
		}
	}
	for _, addr := range eipDone {
		out.ProviderPurge["floating_ips"] = append(out.ProviderPurge["floating_ips"], addr)
	}
	// Any EIPs that survived all 3 passes — surface them so the
	// operator + the next prov see real residual.
	stillStuck, _ := listEIPsByNamePrefix(ctx, client, hw, region, sovereignFQDN, deploymentID)
	for _, e := range stillStuck {
		out.Errors = append(out.Errors, fmt.Sprintf("EIP %s survived %d wipe passes (still in HCS — quota will be hit on next prov)", e.Address, eipPasses))
	}

	// #4636 — UNBOUND / nameless EIP sweep. The name-prefix purge above
	// only sees EIPs whose bandwidth_name carries the catalyst-<stem>-
	// prefix. An UNBOUND, DOWN EIP with no name match (live: 212.72.24.36
	// on the 8fd457a8 wipe) is invisible to it AND to verifyZeroOrphans,
	// so it leaks per wipe → publicIp quota exhaustion (#4614 reap class).
	// Release every NON-bastion UNBOUND (port_id == "") EIP project-wide;
	// listUnboundOrphanEIPs never returns a bound or bastion EIP, so this
	// can only release genuine leftovers.
	if orphanEIPs, oerr := listUnboundOrphanEIPs(ctx, client, hw, region); oerr != nil {
		out.Errors = append(out.Errors, "list unbound orphan EIPs (#4636): "+oerr.Error())
	} else {
		for _, e := range orphanEIPs {
			if err := deleteEIP(ctx, client, hw, region, e.ID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("delete unbound orphan EIP %s (#4636): %s", e.Address, err))
				continue
			}
			out.ProviderPurge["floating_ips"] = append(out.ProviderPurge["floating_ips"], e.Address)
			progress(fmt.Sprintf("#4636: released unbound orphan EIP %s (no port_id, no name match)", e.Address))
		}
	}

	// 5. SG — by-name (covers tags-disabled case).
	//
	// #4046 (2026-06-21 live audit): the first deleteSG pass routinely
	// returns 409 ("security group is in use" / port device-owner still
	// settling) while the ECS/NAT/LB ports that reference the SG are still
	// being torn down asynchronously by HCS. The old single-pass loop logged
	// that 409 to out.Errors and moved on, leaking ONE region's security
	// group on EVERY wipe (the SG is a non-quota resource, so the outer
	// Wipe() retry loop — which used to break the instant VPC/NAT/EIP were
	// clean — never came back to re-delete it). Mirror the EIP 3-pass
	// per-resource retry above: re-list between passes and only keep
	// `firewalls` entries that actually vanished, with a short backoff so HCS
	// can finish releasing the bound ports. SGs that survive all passes
	// surface in out.Errors AND are caught by verifyZeroOrphans → the outer
	// loop (now gated on VerifiedZeroOrphans) keeps retrying.
	const sgPasses = 3
	sgs, err := listSGsByName(ctx, client, hw, region, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list SG: "+err.Error())
	}
	sgDone := map[string]string{} // id → name (deleted + verified gone)
	for pass := 1; pass <= sgPasses && len(sgDone) < len(sgs); pass++ {
		for _, sg := range sgs {
			if _, ok := sgDone[sg.ID]; ok {
				continue
			}
			if err := deleteSG(ctx, client, hw, region, sg.ID); err != nil {
				if pass == sgPasses {
					out.Errors = append(out.Errors, fmt.Sprintf("delete SG %s (pass %d): %s", sg.Name, pass, err))
				}
				continue
			}
			sgDone[sg.ID] = sg.Name
			progress(fmt.Sprintf("deleted SG %s (pass %d)", sg.Name, pass))
		}
		// Re-list to verify; a 409-on-port-settle can return 2xx on the
		// DELETE yet leave the SG present, so drop any ID that came back.
		stillThere, _ := listSGsByName(ctx, client, hw, region, sovereignFQDN)
		survived := map[string]bool{}
		for _, sg := range stillThere {
			survived[sg.ID] = true
		}
		for id := range sgDone {
			if survived[id] {
				delete(sgDone, id) // not actually gone; retry next pass
			}
		}
		if len(sgDone) == len(sgs) {
			break
		}
		// Brief backoff so HCS can finish releasing the ports that pin the SG.
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(2*pass) * time.Second):
		}
	}
	for _, name := range sgDone {
		out.ProviderPurge["firewalls"] = append(out.ProviderPurge["firewalls"], name)
	}

	// 6+7+8. VPC cascade (Wave 5.153) — find ports in each subnet,
	// delete non-network ports, then subnets, then VPC. Without this
	// the subnet delete returns VPC.0209 "subnet is still used" and
	// VPC delete cascades to 409 "Router contains subnet".
	vpcs, err := listVPCsByName(ctx, client, hw, region, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list VPC: "+err.Error())
	}

	// 5b. VPC PEERINGS (#3140, 2026-06-12) — delete BEFORE the VPC
	// cascade. Huawei refuses `DELETE /vpcs/{id}` while a peering
	// references the VPC, and #3307 creates a mesh peering on EVERY
	// multi-VPC prov — so without this step every wipe leaks its full
	// VPC pair (witnessed live: hw128 + hw129 both left their pairs +
	// ACTIVE peerings, wedging the project at 5/5 quota until a manual
	// API cleanup). Matched by EITHER endpoint VPC belonging to this
	// deployment OR the catalyst-<stem>- name prefix (union, same
	// philosophy as the ECS tag+name listing). bastion's VPC never
	// appears here: it owns no catalyst-* peerings and the VPC-set
	// filter only carries this Sovereign's VPC IDs.
	purgeVPCPeerings(ctx, client, hw, region, sovereignFQDN, vpcs, progress, out)

	for _, v := range vpcs {
		if err := cascadeDeleteVPC(ctx, client, hw, region, v.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete VPC %s: %s", v.Name, err))
			continue
		}
		out.ProviderPurge["networks"] = append(out.ProviderPurge["networks"], v.Name)
		progress("deleted VPC " + v.Name)
	}

	// 9. Neutron-layer subnet+network leftovers (Wave 5.155).
	// On HCS Kom4DC, VPC delete via v1 sometimes leaves the underlying
	// Neutron subnet+network as orphan records. Witnessed on hw29
	// 2026-05-27: v1/vpcs returned 204 OK but v2.0/subnets and
	// v2.0/networks still listed the catalyst-hw29-* entries until
	// explicit DELETE on /v2.0/subnets/{id} and /v2.0/networks/{id}.
	sweepNeutronOrphans(ctx, client, hw, region, sovereignFQDN, progress, out)

	// 10. Keypair sweep (Wave 5.155). ECS keypairs accumulate one per
	// prov attempt — failed provs leave them stranded. Without this
	// sweep we accumulated 68 orphan keypairs on me-east-215 across
	// hw01-hw33 attempts. Match by `catalyst-<stem>-` prefix with HARD
	// bastion-* protection.
	sweepKeypairs(ctx, client, hw, region, sovereignFQDN, progress, out)
	return nil
}

// sweepNeutronOrphans removes Neutron-layer subnet+network records that
// outlive the VPC delete on HCS Kom4DC. Hard bastion-* protection.
func sweepNeutronOrphans(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string, progress func(msg string), out *providers.WipeResult) {
	if sovereignFQDN == "" {
		return
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	vpcNeutron := fmt.Sprintf("%s/v2.0", endpointFor("vpc", region))
	// Subnets
	resp, _, _ := doSignedRequest(client, hw, http.MethodGet, vpcNeutron+"/subnets", nil)
	var subnetList struct {
		Subnets []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			NetworkID string `json:"network_id"`
		} `json:"subnets"`
	}
	_ = json.Unmarshal(resp, &subnetList)
	netIDs := []string{}
	for _, s := range subnetList.Subnets {
		if strings.HasPrefix(s.Name, "bastion") {
			continue
		}
		if !strings.HasPrefix(s.Name, prefix) {
			continue
		}
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, vpcNeutron+"/subnets/"+s.ID, nil)
		out.ProviderPurge["neutron_subnets"] = append(out.ProviderPurge["neutron_subnets"], s.Name)
		progress("deleted neutron subnet " + s.Name)
		if s.NetworkID != "" {
			netIDs = append(netIDs, s.NetworkID)
		}
	}
	for _, nid := range netIDs {
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, vpcNeutron+"/networks/"+nid, nil)
	}
}

// sweepKeypairs removes ECS keypairs whose name matches
// `catalyst-<sovereign-stem>-` with HARD bastion-* protection. Wave 5.155.
func sweepKeypairs(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string, progress func(msg string), out *providers.WipeResult) {
	if sovereignFQDN == "" {
		return
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	base := fmt.Sprintf("%s/v2/%s/os-keypairs", endpointFor("ecs", region), hw.ProjectID)
	resp, _, _ := doSignedRequest(client, hw, http.MethodGet, base, nil)
	var keyList struct {
		Keypairs []struct {
			Keypair struct {
				Name string `json:"name"`
			} `json:"keypair"`
		} `json:"keypairs"`
	}
	_ = json.Unmarshal(resp, &keyList)
	for _, k := range keyList.Keypairs {
		name := k.Keypair.Name
		if strings.HasPrefix(name, "bastion") || strings.Contains(name, "bastion") {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/"+name, nil)
		out.ProviderPurge["keypairs"] = append(out.ProviderPurge["keypairs"], name)
		progress("deleted keypair " + name)
	}
}

// ── Wave 5.153 cascade helpers ─────────────────────────────────────────

type hwELB struct {
	ID   string
	Name string
}

// listELBsByName enumerates v3 ELB load balancers in the project and
// filters by the `catalyst-<sovereign-stem>-` name prefix.
func listELBsByName(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string) ([]hwELB, error) {
	if sovereignFQDN == "" {
		return nil, nil
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	url := fmt.Sprintf("%s/v3/%s/elb/loadbalancers", endpointFor("elb", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list ELB: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		LoadBalancers []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"loadbalancers"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode ELB list: %w", err)
	}
	out := []hwELB{}
	for _, lb := range decoded.LoadBalancers {
		if strings.HasPrefix(lb.Name, prefix) {
			out = append(out, hwELB{ID: lb.ID, Name: lb.Name})
		}
	}
	return out, nil
}

// cascadeDeleteELB walks listeners → pools → members → healthmonitors
// → LB so HCS doesn't 409 on a still-in-use LB. Witnessed live:
// canonical wipe always 409'd here because it tried to DELETE the LB
// with listeners still attached.
func cascadeDeleteELB(ctx context.Context, client *http.Client, hw hwCreds, region, lbID string) error {
	base := fmt.Sprintf("%s/v3/%s/elb", endpointFor("elb", region), hw.ProjectID)
	// Fetch LB with listeners+pools expanded
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, base+"/loadbalancers/"+lbID, nil)
	if err != nil || status >= 400 {
		if status == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("get LB: status %d: %s", status, snippet(resp, 240))
	}
	var lb struct {
		LoadBalancer struct {
			Listeners []struct {
				ID string `json:"id"`
			} `json:"listeners"`
			Pools []struct {
				ID string `json:"id"`
			} `json:"pools"`
		} `json:"loadbalancer"`
	}
	if err := json.Unmarshal(resp, &lb); err != nil {
		return fmt.Errorf("decode LB: %w", err)
	}
	// Listeners (unset default_pool first to release pool reference)
	for _, L := range lb.LoadBalancer.Listeners {
		// PUT to unset default_pool_id
		_, _, _ = doSignedRequest(client, hw, http.MethodPut, base+"/listeners/"+L.ID,
			[]byte(`{"listener":{"default_pool_id":null}}`))
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/listeners/"+L.ID, nil)
	}
	// Pools (delete members + healthmonitor first)
	for _, p := range lb.LoadBalancer.Pools {
		// Get pool detail for members + healthmonitor
		pResp, pStat, _ := doSignedRequest(client, hw, http.MethodGet, base+"/pools/"+p.ID, nil)
		if pStat < 400 {
			var pool struct {
				Pool struct {
					Members []struct {
						ID string `json:"id"`
					} `json:"members"`
					HealthMonitorID string `json:"healthmonitor_id"`
				} `json:"pool"`
			}
			if err := json.Unmarshal(pResp, &pool); err == nil {
				for _, m := range pool.Pool.Members {
					_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/pools/"+p.ID+"/members/"+m.ID, nil)
				}
				if pool.Pool.HealthMonitorID != "" {
					_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/healthmonitors/"+pool.Pool.HealthMonitorID, nil)
				}
			}
		}
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/pools/"+p.ID, nil)
	}
	// LB itself
	_, status, err = doSignedRequest(client, hw, http.MethodDelete, base+"/loadbalancers/"+lbID, nil)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("delete LB: status %d", status)
	}
	return nil
}

type hwNAT struct {
	ID   string
	Name string
}

func listNATsByName(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string) ([]hwNAT, error) {
	if sovereignFQDN == "" {
		return nil, nil
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	url := fmt.Sprintf("%s/v2/%s/nat_gateways", endpointFor("nat", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list NAT: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		NATGateways []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nat_gateways"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode NAT list: %w", err)
	}
	out := []hwNAT{}
	for _, n := range decoded.NATGateways {
		if strings.HasPrefix(n.Name, prefix) {
			out = append(out, hwNAT{ID: n.ID, Name: n.Name})
		}
	}
	return out, nil
}

// cascadeDeleteNAT clears snat_rules + dnat_rules then deletes NAT
// itself. Without this, NAT delete returns VPC.2016 "Rule has not been
// deleted" forever.
//
// Wave 5.154 (2026-05-27): HCS Kom4DC me-east-215 does NOT publish the
// public Huawei spec DELETE /v2/{pid}/snat_rules/{rid} — that path
// returns APIGW.0101 "method DELETE not found". The ONLY working delete
// path on Kom4DC is the nested form: DELETE /v2/{pid}/nat_gateways/
// {nid}/snat_rules/{rid}. Verified empirically against hw29/hw30/hw31
// stuck NATs that Wave 5.153 left behind. Same fix for dnat_rules.
func cascadeDeleteNAT(ctx context.Context, client *http.Client, hw hwCreds, region, natID string) error {
	base := fmt.Sprintf("%s/v2/%s", endpointFor("nat", region), hw.ProjectID)
	// snat_rules — nested delete path required by Kom4DC
	resp, _, _ := doSignedRequest(client, hw, http.MethodGet, base+"/snat_rules?nat_gateway_id="+natID, nil)
	var snat struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"snat_rules"`
	}
	if err := json.Unmarshal(resp, &snat); err == nil {
		for _, r := range snat.Rules {
			_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/nat_gateways/"+natID+"/snat_rules/"+r.ID, nil)
		}
	}
	// dnat_rules — same nested form
	resp, _, _ = doSignedRequest(client, hw, http.MethodGet, base+"/dnat_rules?nat_gateway_id="+natID, nil)
	var dnat struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"dnat_rules"`
	}
	if err := json.Unmarshal(resp, &dnat); err == nil {
		for _, r := range dnat.Rules {
			_, _, _ = doSignedRequest(client, hw, http.MethodDelete, base+"/nat_gateways/"+natID+"/dnat_rules/"+r.ID, nil)
		}
	}
	// Wave 5.157 (2026-05-27): Kom4DC NAT delete returns VPC.2016
	// "Rule has not been deleted" on the FIRST attempt even when
	// snat_rule DELETE returned 204, because Kom4DC has an
	// eventual-consistency gap between the snat_rule control-plane
	// commit and NAT-readable visibility. Witnessed live on hw34 wipe
	// 2026-05-27T14:12:03Z: cascade looked correct but NAT delete 400'd;
	// 5s later same DELETE returned 204. Solution: retry-with-backoff
	// loop up to 4 attempts (3s, 6s, 12s, 24s) on VPC.2016 / status 400.
	var status int
	var err error
	backoff := 3
	for attempt := 1; attempt <= 4; attempt++ {
		_, status, err = doSignedRequest(client, hw, http.MethodDelete, base+"/nat_gateways/"+natID, nil)
		if err != nil {
			return err
		}
		if status < 400 || status == http.StatusNotFound {
			return nil
		}
		if attempt == 4 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(backoff) * time.Second):
		}
		backoff *= 2
	}
	return fmt.Errorf("delete NAT: status %d after 4 attempts", status)
}

// listEIPsByNamePrefix uses bandwidth_name prefix (Wave 5.138 pattern)
// to find catalyst-owned EIPs even when tags are disabled.
func listEIPsByNamePrefix(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN, deploymentID string) ([]hwEIP, error) {
	if sovereignFQDN == "" {
		return nil, nil
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	prefix := "catalyst-" + stem + "-"
	url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return nil, err
	}
	var decoded struct {
		PublicIPs []struct {
			ID            string `json:"id"`
			Address       string `json:"public_ip_address"`
			BandwidthName string `json:"bandwidth_name"`
		} `json:"publicips"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, err
	}
	out := []hwEIP{}
	for _, e := range decoded.PublicIPs {
		if strings.HasPrefix(e.BandwidthName, prefix) {
			out = append(out, hwEIP{ID: e.ID, Address: e.Address})
		}
	}
	return out, nil
}

// cascadeDeleteVPC walks the full HCS Kom4DC dependency tree for a VPC:
//
//  1. List floating-IPs whose router_id matches this VPC (HCS Kom4DC
//     uses the same UUID for VPC and its built-in router). Disassociate
//     (PUT port_id=null) then DELETE each — otherwise router-interface
//     removal returns "RouterInterfaceInUseByFloatingIP".
//  2. List ports on each subnet. For COMPUTE-owned ports
//     (device_owner=compute:...), follow device_id to the ECS and
//     delete it via the canonical ECS batch-delete endpoint with
//     delete_publicip+delete_volume — these are orphan ECSs the
//     name-prefix sweep missed because the operator named them
//     ad-hoc (e.g. an RCA-recheck probe with a non-catalyst-* name
//     reused the VPC's keypair → landed in this subnet). Wait briefly
//     for the ECS to release its port.
//  3. For the router-interface port on each subnet, find the Neutron
//     subnet_id from fixed_ips (Kom4DC has TWO subnet IDs per subnet:
//     the VPC-layer ID and the underlying Neutron ID). Call
//     PUT /v2.0/routers/{router_id}/remove_router_interface with the
//     Neutron subnet_id — direct port DELETE returns VPC.2500
//     "Port's device_owner is not empty and is not neutron:VIP_PORT".
//  4. Delete the subnet, then the VPC.
//
// Wave 5.154 (2026-05-27) root cause: prior cascadeDeleteVPC deleted
// only non-network ports, never floating-IPs, never the router-interface,
// and never compute-port-attached orphan ECSs. That left hw29 stuck
// behind a single rca-recheck-s7nl4-* ECS I had spawned during s7n.large.4
// capacity-exhaustion debugging — the wipe filter (catalyst-* prefix)
// missed it, but the port-on-subnet stopped the entire VPC teardown.
func cascadeDeleteVPC(ctx context.Context, client *http.Client, hw hwCreds, region, vpcID string) error {
	vpcBase := fmt.Sprintf("%s/v1/%s", endpointFor("vpc", region), hw.ProjectID)
	vpcNeutron := fmt.Sprintf("%s/v2.0", endpointFor("vpc", region))
	ecsBase := fmt.Sprintf("%s/v1/%s", endpointFor("ecs", region), hw.ProjectID)

	// 1. Disassociate + delete floating-IPs bound to this VPC's router.
	fResp, _, _ := doSignedRequest(client, hw, http.MethodGet, vpcNeutron+"/floatingips", nil)
	var fipList struct {
		FloatingIPs []struct {
			ID       string `json:"id"`
			RouterID string `json:"router_id"`
		} `json:"floatingips"`
	}
	_ = json.Unmarshal(fResp, &fipList)
	for _, f := range fipList.FloatingIPs {
		if f.RouterID != vpcID {
			continue
		}
		disassoc, _ := json.Marshal(map[string]any{"floatingip": map[string]any{"port_id": nil}})
		_, _, _ = doSignedRequest(client, hw, http.MethodPut, vpcNeutron+"/floatingips/"+f.ID, disassoc)
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, vpcNeutron+"/floatingips/"+f.ID, nil)
	}

	// List subnets in this VPC.
	resp, _, err := doSignedRequest(client, hw, http.MethodGet, vpcBase+"/subnets", nil)
	if err != nil {
		return err
	}
	var subnetList struct {
		Subnets []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			VPCID string `json:"vpc_id"`
		} `json:"subnets"`
	}
	_ = json.Unmarshal(resp, &subnetList)

	// Single ports query reused per subnet.
	pResp, _, _ := doSignedRequest(client, hw, http.MethodGet, vpcBase+"/ports", nil)
	var portList struct {
		Ports []struct {
			ID          string `json:"id"`
			DeviceOwner string `json:"device_owner"`
			DeviceID    string `json:"device_id"`
			FixedIPs    []struct {
				SubnetID string `json:"subnet_id"`
			} `json:"fixed_ips"`
		} `json:"ports"`
	}
	_ = json.Unmarshal(pResp, &portList)

	for _, s := range subnetList.Subnets {
		if s.VPCID != vpcID {
			continue
		}
		// 2. Delete orphan ECSs whose port sits on this VPC's subnet.
		//    `network:` (router/dhcp) ports are infrastructure — handled
		//    by step 3. Compute ports (device_owner=compute:*) and any
		//    other non-network owner with a device_id imply an ECS.
		killedECS := false
		for _, p := range portList.Ports {
			// Match port to this VPC's subnet by inspecting fixed_ips.
			// fixed_ips[].subnet_id is the Neutron subnet ID. We can't
			// directly equate it to the VPC subnet ID, but since these
			// ports came back from the project-scoped list, any port
			// with a device_id and a non-network owner that we can't
			// otherwise account for AND whose port maps to our VPC via
			// network_id will be caught below. Conservative: only kill
			// when owner starts with "compute:" — clearly an ECS.
			if !strings.HasPrefix(p.DeviceOwner, "compute:") {
				continue
			}
			if p.DeviceID == "" {
				continue
			}
			// Verify ECS lives in our VPC before deleting.
			detailURL := fmt.Sprintf("%s/cloudservers/%s", ecsBase, p.DeviceID)
			eResp, _, _ := doSignedRequest(client, hw, http.MethodGet, detailURL, nil)
			var srv struct {
				Server struct {
					Name     string                      `json:"name"`
					Metadata map[string]string           `json:"metadata"`
					Addrs    map[string][]map[string]any `json:"addresses"`
				} `json:"server"`
			}
			if json.Unmarshal(eResp, &srv) != nil {
				continue
			}
			// HARD bastion protection: never touch bastion-* ECSs.
			if strings.HasPrefix(srv.Server.Name, "bastion-") {
				continue
			}
			// Confirm vpc_id in metadata OR an address keyed by this VPC.
			matchesVPC := srv.Server.Metadata["vpc_id"] == vpcID
			if !matchesVPC {
				if _, ok := srv.Server.Addrs[vpcID]; ok {
					matchesVPC = true
				}
			}
			if !matchesVPC {
				continue
			}
			delBody, _ := json.Marshal(map[string]any{
				"servers":         []map[string]string{{"id": p.DeviceID}},
				"delete_publicip": true,
				"delete_volume":   true,
			})
			_, _, _ = doSignedRequest(client, hw, http.MethodPost, ecsBase+"/cloudservers/delete", delBody)
			killedECS = true
		}
		if killedECS {
			// Give Kom4DC ~45s to release the compute port.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(45 * time.Second):
			}
		}

		// 3. Find router-interface port for THIS subnet → extract Neutron
		//    subnet_id from fixed_ips → call remove_router_interface.
		for _, p := range portList.Ports {
			if p.DeviceOwner != "network:router_interface_distributed" &&
				p.DeviceOwner != "network:router_interface" {
				continue
			}
			if p.DeviceID != vpcID {
				continue
			}
			var neutronSubnetID string
			for _, fi := range p.FixedIPs {
				if fi.SubnetID != "" {
					neutronSubnetID = fi.SubnetID
					break
				}
			}
			if neutronSubnetID == "" {
				continue
			}
			rmBody, _ := json.Marshal(map[string]string{"subnet_id": neutronSubnetID})
			_, _, _ = doSignedRequest(client, hw, http.MethodPut,
				vpcNeutron+"/routers/"+vpcID+"/remove_router_interface", rmBody)
		}

		// 4. Delete the subnet.
		_, _, _ = doSignedRequest(client, hw, http.MethodDelete, vpcBase+"/vpcs/"+vpcID+"/subnets/"+s.ID, nil)
	}

	// VPC itself.
	_, status, err := doSignedRequest(client, hw, http.MethodDelete, vpcBase+"/vpcs/"+vpcID, nil)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("delete VPC: status %d", status)
	}
	return nil
}

// listECSByNamePrefix lists every ECS in the project, filters by name
// starting with `catalyst-<sovereign-stem>-<dep-id-short>-` (the
// canonical local.name_prefix from infra/providers/huawei/main.tf).
// Wave 5.147 (2026-05-27): used as fallback when listECSByTag returns
// zero (which is always until tags are re-enabled). Without this,
// failed tofu destroys leave ECSs stranded forever — the actual cause
// of the s7n.large.4 HCS pool exhaustion that broke hw30 #4-#22.
//
// The sovereign-stem comes from sovereignFQDN with dots replaced by
// dashes (matches local.name_prefix construction). deploymentID-short
// is the first 8 chars (matches tofu's behavior).
func listECSByNamePrefix(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN, deploymentID string) ([]hwECSServer, error) {
	if sovereignFQDN == "" {
		return nil, nil
	}
	stem := strings.ReplaceAll(sovereignFQDN, ".", "-")
	depShort := deploymentID
	if len(depShort) > 8 {
		depShort = depShort[:8]
	}
	// Match: "catalyst-<stem>-<depShort>-..."
	prefix := fmt.Sprintf("catalyst-%s-%s-", stem, depShort)
	// If no deploymentID, fall back to stem-only prefix (covers ALL
	// deployments for this Sovereign — broader sweep).
	stemPrefix := fmt.Sprintf("catalyst-%s-", stem)

	// List all ECSs in project.
	url := fmt.Sprintf("%s/v1/%s/cloudservers/detail", endpointFor("ecs", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("list ECS detail: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		Servers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Flavor struct {
				ID string `json:"id"`
			} `json:"flavor"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode ECS detail: %w", err)
	}
	out := []hwECSServer{}
	for _, s := range decoded.Servers {
		matches := false
		if deploymentID != "" && strings.HasPrefix(s.Name, prefix) {
			matches = true
		} else if deploymentID == "" && strings.HasPrefix(s.Name, stemPrefix) {
			matches = true
		}
		if matches {
			out = append(out, hwECSServer{ID: s.ID, Name: s.Name, Status: s.Status, Flavor: s.Flavor.ID})
		}
	}
	return out, nil
}

func deleteECS(ctx context.Context, client *http.Client, hw hwCreds, region, id string) error {
	url := fmt.Sprintf("%s/v1/%s/cloudservers/delete", endpointFor("ecs", region), hw.ProjectID)
	body, _ := json.Marshal(map[string]any{
		"servers":         []map[string]string{{"id": id}},
		"delete_publicip": true,
		"delete_volume":   true,
	})
	_, status, err := doSignedRequest(client, hw, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

// hwEIP is the minimal projection of the EIP list response.
type hwEIP struct {
	ID      string
	Address string
}

func listEIPsByTag(ctx context.Context, client *http.Client, hw hwCreds, region, deploymentID, sovereignFQDN string) ([]hwEIP, error) {
	// EIP list-by-tag follows the standard TMS list-resources pattern.
	// Endpoint: GET /v1/<project_id>/publicips
	// We post-filter by name prefix for simplicity.
	url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("EIP list: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		PublicIPs []struct {
			ID      string `json:"id"`
			Address string `json:"public_ip_address"`
			Tags    []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"tags"`
		} `json:"publicips"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode EIP list: %w", err)
	}
	out := []hwEIP{}
	for _, e := range decoded.PublicIPs {
		match := false
		for _, t := range e.Tags {
			if (deploymentID != "" && t.Key == "catalyst.openova.io/deployment-id" && t.Value == deploymentID) ||
				(t.Key == "catalyst.openova.io/sovereign" && t.Value == sovereignFQDN) {
				match = true
				break
			}
		}
		if match {
			out = append(out, hwEIP{ID: e.ID, Address: e.Address})
		}
	}
	return out, nil
}

func deleteEIP(ctx context.Context, client *http.Client, hw hwCreds, region, id string) error {
	url := fmt.Sprintf("%s/v1/%s/publicips/%s", endpointFor("vpc", region), hw.ProjectID, id)
	_, status, err := doSignedRequest(client, hw, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

// SweepOrphanEIPs lists every publicIP in the HCS project and deletes
// the ones that are (a) status=DOWN AND (b) unbound (no port_id) AND
// (c) have a bandwidth name with the "catalyst-" prefix (so we never
// touch EIPs belonging to non-catalyst workloads in a shared HCS
// project).
//
// Wave 5.138 (2026-05-27): rationale
// ===================================
// EIP resources are NOT tagged (Wave 5.4 disabled tags due to HCS TMS
// endpoint divergence). So per-deployment Wipe (listEIPsByTag) cannot
// see EIPs from PRIOR failed deployments — they have no tags and a
// different `deployment-id` than the current Wipe scope. Across 4
// failed hw30 provs the orphan EIPs piled up to the project quota cap
// (publicIp used=10/10), and hw30 #14 failed at 50s with
// "error allocating EIP: The request could not be processed due to
// conflict in the request" — quota exhaustion. This sweep solves the
// gap by identifying catalyst-owned EIPs through their bandwidth name
// (which DOES carry the "catalyst-<sovereign>-..." prefix from
// huaweicloud_vpc_eip.{cp,nat,elb_primary} in infra/providers/huawei/
// main.tf), independent of tag presence.
//
// Intended caller: the periodic janitor (handler/janitor.go). Idempotent
// + safe to run during active provisioning: bound EIPs (port_id set)
// AND active EIPs (status=ACTIVE/BOUND) are never touched.
//
// #4466 — LOG-ONLY GATE. `destructive` defaults FALSE (the janitor only
// flips it true when CATALYST_JANITOR_DESTRUCTIVE=true). While false the
// sweep LOGS exactly the EIP it WOULD reap (`would-reap (log-only)`) but
// performs NO delete — so "deletes prod infra" is opt-in, not the default.
// The b9f9590b self-reap would have been caught in this log-only window
// before any node died. The return value is the count of would-reap (or
// actually-reaped, when destructive) EIPs.
func (p *Provider) SweepOrphanEIPs(ctx context.Context, ak, sk, projectID, region string, activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})

	listURL := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), projectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, listURL, nil)
	if err != nil {
		return 0, fmt.Errorf("list publicips: %w", err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("list publicips: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		PublicIPs []struct {
			ID            string `json:"id"`
			Address       string `json:"public_ip_address"`
			Status        string `json:"status"`
			PortID        string `json:"port_id"`
			Type          string `json:"type"`
			BandwidthName string `json:"bandwidth_name"`
		} `json:"publicips"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return 0, fmt.Errorf("decode publicips: %w", err)
	}
	deleted := 0
	for _, e := range decoded.PublicIPs {
		bound := e.PortID != ""
		if e.Status != "DOWN" || bound {
			continue
		}
		if !strings.HasPrefix(e.BandwidthName, "catalyst-") {
			continue
		}
		// G15 (Refs #2553): skip EIPs whose bandwidth_name carries the
		// 8-char prefix of any ACTIVE deployment. Format the renderer
		// uses: catalyst-<sovereign-dashed>-<8charDepIDPrefix>-<purpose>-bw
		// (e.g. catalyst-hw43-omani-works-5b413990-elb-primary-bw).
		// Without this guard the sweep eats freshly-created EIPs from
		// in-flight `tofu apply` runs — they sit DOWN+unbound for the
		// few minutes between EIP allocation and ECS/NAT bind.
		if len(activeDepIDPrefixes) > 0 {
			skip := false
			for prefix := range activeDepIDPrefixes {
				if strings.Contains(e.BandwidthName, "-"+prefix+"-") {
					skip = true
					break
				}
			}
			if skip {
				progress(fmt.Sprintf("sweep orphan EIP %s (bw=%s): skipped (active deployment)", e.Address, e.BandwidthName))
				continue
			}
		}
		if !destructive {
			// #4466 log-only: count it so the operator sees the volume the
			// destructive pass WOULD reclaim, but delete nothing.
			progress(fmt.Sprintf("sweep orphan EIP %s (bw=%s): would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)", e.Address, e.BandwidthName))
			deleted++
			continue
		}
		if err := deleteEIP(ctx, client, hw, region, e.ID); err != nil {
			progress(fmt.Sprintf("sweep orphan EIP %s (bw=%s): DELETE failed: %v", e.Address, e.BandwidthName, err))
			continue
		}
		progress(fmt.Sprintf("sweep orphan EIP %s (bw=%s): deleted", e.Address, e.BandwidthName))
		deleted++
	}
	return deleted, nil
}

// SweepOrphanKeypairs lists all keypairs in the HCS project and deletes
// catalyst-owned keypairs whose name carries no in-flight deployment-ID
// prefix. Wave 5.155 RCA in
// feedback_hcs_kom4dc_wipe_cascade_quirks.md observed 68 orphan
// keypairs accumulated across failed provs because keypairs are not
// tagged on HCS — per-deployment Wipe.sweepKeypairs (line 912) finds
// them only when their parent Sovereign FQDN survives in tfvars,
// missing every keypair from a wiped record. The project keypair quota
// (default 100) gets eaten silently until the next prov fails with
// `KeyPair.0001 quota exceeded`.
//
// G73 (Refs #2620): mirror SweepOrphanEIPs — list all catalyst-prefixed
// keypairs, skip any whose name contains an active deployment ID
// prefix, delete the rest. Bastion keypairs are hard-protected.
//
// Returns the count of keypairs deleted. activeDepIDPrefixes is a set
// of 8-char prefixes of in-flight deployment IDs (status one of
// pending / provisioning / tofu-applying / flux-bootstrapping /
// phase1-watching / wiping). Empty = no protection (post-wipe sweep).
//
// #4466: `destructive` gates the real delete (default false → log-only
// `would-reap`). See SweepOrphanEIPs for the rationale.
func (p *Provider) SweepOrphanKeypairs(ctx context.Context, ak, sk, projectID, region string, activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})

	listURL := fmt.Sprintf("%s/v2/%s/os-keypairs", endpointFor("ecs", region), projectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, listURL, nil)
	if err != nil {
		return 0, fmt.Errorf("list keypairs: %w", err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("list keypairs: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		Keypairs []struct {
			Keypair struct {
				Name string `json:"name"`
			} `json:"keypair"`
		} `json:"keypairs"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return 0, fmt.Errorf("decode keypairs: %w", err)
	}
	deleted := 0
	for _, k := range decoded.Keypairs {
		name := k.Keypair.Name
		if name == "" {
			continue
		}
		// Hard-protect bastion-* keypairs — these are operator-owned and
		// never belong to a catalyst deployment.
		if strings.HasPrefix(name, "bastion") || strings.Contains(name, "bastion") {
			continue
		}
		// Only sweep catalyst-prefixed keypairs. Naming shape per
		// infra/providers/huawei/main.tf: `catalyst-<sovereign-dashed>-<purpose>`.
		if !strings.HasPrefix(name, "catalyst-") {
			continue
		}
		// Skip keypairs of any in-flight deployment. The 8-char
		// deployment-ID prefix appears in keypair names rendered for
		// the per-region cluster bootstrap, mirroring the EIP
		// bandwidth-name protection pattern in SweepOrphanEIPs.
		if len(activeDepIDPrefixes) > 0 {
			skip := false
			for prefix := range activeDepIDPrefixes {
				if strings.Contains(name, "-"+prefix+"-") || strings.Contains(name, "-"+prefix) {
					skip = true
					break
				}
			}
			if skip {
				progress(fmt.Sprintf("sweep orphan keypair %s: skipped (active deployment)", name))
				continue
			}
		}
		if !destructive {
			progress(fmt.Sprintf("sweep orphan keypair %s: would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)", name))
			deleted++
			continue
		}
		delURL := fmt.Sprintf("%s/%s", listURL, name)
		_, dstatus, derr := doSignedRequest(client, hw, http.MethodDelete, delURL, nil)
		if derr != nil {
			progress(fmt.Sprintf("sweep orphan keypair %s: DELETE failed: %v", name, derr))
			continue
		}
		if dstatus >= 400 && dstatus != http.StatusNotFound {
			progress(fmt.Sprintf("sweep orphan keypair %s: DELETE status %d", name, dstatus))
			continue
		}
		progress(fmt.Sprintf("sweep orphan keypair %s: deleted", name))
		deleted++
	}
	return deleted, nil
}

// SweepOrphanVPCs lists every VPC in the HCS project and reclaims
// catalyst-owned VPCs that carry no in-flight deployment-ID prefix. It
// is the VPC-tier sibling of SweepOrphanEIPs (Wave 5.138) and
// SweepOrphanKeypairs (G73 #2620): both EIPs and keypairs already have a
// project-wide reclaim sweep because they are untagged on HCS so the
// per-deployment Wipe cannot find them once the parent record is gone —
// VPCs had the SAME leak with NO reclaim path (#4431).
//
// Why VPCs leak: the per-Sovereign Wipe (cascadeDeleteVPC) only runs
// while the deployment record still names the VPC's Sovereign FQDN. A
// wipe whose VPC teardown lagged (the NAT→subnet→port→VPC chain 409s on
// residual ports/routes, and a peering pinning the VPC blocks the delete
// — see purgeVPCPeerings) leaves an orphaned catalyst-* VPC that counts
// against the project quota forever. The HCS Kom4DC me-east-215 default
// VPC quota is 5 (publicIp 10), and the raise endpoint is NOT
// tenant-facing (GET-only; PUT/POST return APIGW.0101 — memory
// feedback_hcs_quota_raise_apigw_not_published). So the only way to keep
// a fresh 2-region prov (needs 2 VPCs) from false-failing the G68
// pre-flight at "project at 5/5 VPCs" is to RECLAIM the leaked orphans —
// code cannot raise the wall. Witnessed live: hw94/hw96/hw128/hw129 each
// left their VPC pair until a manual bastion cleanup.
//
// Match shape: VPC name prefix `catalyst-` (the renderer in
// infra/providers/huawei/main.tf names every VPC
// `catalyst-<sovereign-dashed>-<8charDepIDPrefix>-<region>-vpc`).
// bastion / non-catalyst VPCs are hard-protected — the prefix filter
// alone excludes them. activeDepIDPrefixes is the set of 8-char prefixes
// of in-flight deployment IDs (status pending / provisioning /
// tofu-applying / flux-bootstrapping / phase1-watching / wiping); any
// VPC whose name contains one is skipped so the sweep never eats a VPC
// from a live `tofu apply`. Empty set = no protection (post-wipe sweep).
//
// Each reclaimable orphan is torn down via cascadeDeleteVPC (the same
// NAT/floating-IP/subnet/port cascade the per-Sovereign Wipe uses) after
// deleting any VPC peering that references it (HCS refuses the VPC
// delete while a peering pins it — #3140). Idempotent + safe to run
// during active provisioning.
//
// Returns the count of VPCs deleted.
//
// #4466: `destructive` gates the cascade teardown (default false →
// log-only `would-reap`). VPC reaping cascade-deleted the live dep's
// peering/EIPs on b9f9590b, so the log-only window matters most here. See
// SweepOrphanEIPs for the rationale.
func (p *Provider) SweepOrphanVPCs(ctx context.Context, ak, sk, projectID, region string, activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})

	listURL := fmt.Sprintf("%s/v1/%s/vpcs", endpointFor("vpc", region), projectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, listURL, nil)
	if err != nil {
		return 0, fmt.Errorf("list vpcs: %w", err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("list vpcs: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		VPCs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"vpcs"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return 0, fmt.Errorf("decode vpcs: %w", err)
	}
	deleted := 0
	for _, v := range decoded.VPCs {
		if !isReclaimableOrphanVPC(v.Name, activeDepIDPrefixes) {
			if v.Name != "" && strings.HasPrefix(v.Name, "catalyst-") {
				progress(fmt.Sprintf("sweep orphan VPC %s: skipped (active deployment)", v.Name))
			}
			continue
		}
		if !destructive {
			progress(fmt.Sprintf("sweep orphan VPC %s: would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)", v.Name))
			deleted++
			continue
		}
		// Delete any peering pinning this VPC before the cascade, else
		// the VPC delete 409s "Router contains subnet" / peering-bound.
		deleteVPCPeeringsTouching(ctx, client, hw, region, v.ID, progress)
		if err := cascadeDeleteVPC(ctx, client, hw, region, v.ID); err != nil {
			progress(fmt.Sprintf("sweep orphan VPC %s: cascade delete failed: %v", v.Name, err))
			continue
		}
		progress(fmt.Sprintf("sweep orphan VPC %s: deleted", v.Name))
		deleted++
	}
	return deleted, nil
}

// isReclaimableOrphanVPC is the pure match/protection predicate used by
// SweepOrphanVPCs (#4431). A VPC is reclaimable when its name carries the
// `catalyst-` prefix (so bastion / operator-owned VPCs are excluded) AND
// the name contains none of the in-flight deployment-ID 8-char prefixes
// (so a live `tofu apply`'s VPC is never swept). Bastion is doubly
// guarded: any name containing "bastion" is protected even if it somehow
// carried the catalyst- prefix. Extracted as a free function so the
// decision is unit-testable without an HCS endpoint seam.
func isReclaimableOrphanVPC(name string, activeDepIDPrefixes map[string]struct{}) bool {
	if name == "" {
		return false
	}
	// Hard-protect bastion-* and any non-catalyst VPC — operator-owned,
	// never part of a catalyst deployment.
	if strings.Contains(name, "bastion") || !strings.HasPrefix(name, "catalyst-") {
		return false
	}
	// Skip VPCs of any in-flight deployment. The 8-char deployment-ID
	// prefix appears in the name as `catalyst-<sovereign>-<prefix>-<region>-vpc`.
	for prefix := range activeDepIDPrefixes {
		if strings.Contains(name, "-"+prefix+"-") {
			return false
		}
	}
	return true
}

// hwEVSVolume is the subset of the EVS list response the orphan sweep
// reads. `Attachments` carries the server bindings (empty when detached);
// `Metadata` is the free-form map the CSI driver stamps (it carries the
// owning cluster ID under keys like `cluster_id` / the PV's namespace).
type hwEVSVolume struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Status      string            `json:"status"`
	Attachments []map[string]any  `json:"attachments"`
	Metadata    map[string]string `json:"metadata"`
}

// isReclaimableOrphanEVS is the pure match/protection predicate for the
// detached-EVS-orphan sweep (#4466). Node self-reap BEFORE a wipe (the
// b9f9590b incident) strands CSI `pvc-*` volumes — 283 were stranded as
// pure quota debt because nothing detaches+deletes them once the node is
// gone. The provider had EIP/keypair/VPC reclaim sweeps but no
// detached-volume sweep; this closes the last quota-debt fault.
//
// A volume is reclaimable when ALL of:
//   - name carries the CSI `pvc-` prefix (so we ONLY ever touch
//     Kubernetes-provisioned PV volumes, never a bastion / operator-owned
//     system disk),
//   - it is genuinely detached: status=="available" AND zero attachments
//     (an in-use / attaching / creating volume is NEVER swept — same
//     protect-the-live-resource posture as the bound-EIP guard),
//   - its owning-cluster metadata carries NONE of the in-flight
//     deployment-ID 8-char prefixes (so a live cluster's PVCs are never
//     reaped; protect-by-default — a volume whose metadata matches an
//     active dep is protected even though it is momentarily detached).
//
// Bastion volumes never carry the `pvc-` prefix, so the prefix filter
// alone hard-protects them; the explicit bastion substring guard is a
// belt-and-suspenders second layer.
func isReclaimableOrphanEVS(name, status string, attachmentCount int, metadata map[string]string, activeDepIDPrefixes map[string]struct{}) bool {
	if name == "" {
		return false
	}
	// Hard-protect anything that is not a CSI-provisioned PV, and any
	// bastion-named volume (defence-in-depth — a `pvc-`-named volume can't
	// be the bastion, but the check costs nothing).
	if strings.Contains(name, "bastion") || !strings.HasPrefix(name, "pvc-") {
		return false
	}
	// Only sweep genuinely-detached volumes. An attached / attaching /
	// creating / deleting volume is live and must never be reaped.
	if status != "available" || attachmentCount > 0 {
		return false
	}
	// Protect-by-default: if the volume's owning-cluster metadata carries
	// any in-flight deployment-ID prefix, it belongs to a live env (its
	// node may merely be momentarily detached during a roll) — protect it.
	for _, v := range metadata {
		for prefix := range activeDepIDPrefixes {
			if v != "" && strings.Contains(v, prefix) {
				return false
			}
		}
	}
	return true
}

// evsDeleteSpacing throttles consecutive EVS DELETE calls in the orphan
// sweep. The HCS EVS API rate-limits HARD — deletes fired faster than
// ~0.8s apart trip 429 ("too many 429 error responses") and a large backlog
// (the 332-volume hw244 heal, #5028) fails wholesale. 900ms leaves margin
// over the observed ~0.8s floor. Package var so tests run instantly.
var evsDeleteSpacing = 900 * time.Millisecond

// evsDelete429MaxRetries / evsDelete429BaseBackoff bound the per-volume
// retry when a single DELETE still 429s despite the inter-call spacing (a
// burst from a concurrent janitor pass, say). Exponential, ctx-interruptible.
// Package vars so tests run instantly.
var (
	evsDelete429MaxRetries  = 5
	evsDelete429BaseBackoff = 1 * time.Second
)

// deleteEVSVolume issues DELETE /v2/{project}/cloudvolumes/{id} with
// retry-with-backoff on HTTP 429 (the HCS EVS hard rate-limit, #5028).
// Returns the final HTTP status. A transport error aborts immediately; a
// non-429 status (incl. 404 already-gone) returns straight away. Honours ctx
// cancellation between retries so a wipe/janitor deadline is respected.
func deleteEVSVolume(ctx context.Context, client *http.Client, hw hwCreds, region, projectID, volID string) (int, error) {
	delURL := fmt.Sprintf("%s/v2/%s/cloudvolumes/%s", endpointFor("evs", region), projectID, volID)
	backoff := evsDelete429BaseBackoff
	var status int
	var err error
	for attempt := 0; ; attempt++ {
		_, status, err = doSignedRequest(client, hw, http.MethodDelete, delURL, nil)
		if err != nil {
			return status, err
		}
		if status != http.StatusTooManyRequests {
			return status, nil
		}
		// 429 — the rate-limit the un-throttled bulk reap tripped. Back off
		// (exponential) and retry, unless we've exhausted the budget (the
		// caller logs the residual 429; the periodic janitor retries later).
		if attempt >= evsDelete429MaxRetries {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// SweepOrphanEVS lists every EVS volume in the HCS project and reclaims
// detached CSI `pvc-*` volumes that belong to no in-flight deployment
// (#4466). It is the volume-tier sibling of SweepOrphanEIPs / Keypairs /
// VPCs: the per-deployment Wipe tears down volumes via the cluster's CSI
// controller, but a node self-reap BEFORE the wipe leaves the volumes
// orphaned with no controller left to detach+delete them — 283 piled up
// on b9f9590b as pure quota debt.
//
// Match + protection live in isReclaimableOrphanEVS (pure, unit-testable).
// `destructive` gates the real delete (default false → log-only
// `would-reap`), identical to the sibling sweeps. activeDepIDPrefixes is
// the protect-by-default set of in-flight deployment-ID 8-char prefixes.
// Returns the count of volumes reaped (or would-reap, in log-only mode).
// Idempotent + safe to run during active provisioning.
func (p *Provider) SweepOrphanEVS(ctx context.Context, ak, sk, projectID, region string, activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})

	// EVS detail list — /v2/{project}/cloudvolumes/detail returns name,
	// status, attachments and metadata in one call.
	listURL := fmt.Sprintf("%s/v2/%s/cloudvolumes/detail", endpointFor("evs", region), projectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, listURL, nil)
	if err != nil {
		return 0, fmt.Errorf("list volumes: %w", err)
	}
	if status >= 400 {
		return 0, fmt.Errorf("list volumes: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		Volumes []hwEVSVolume `json:"volumes"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return 0, fmt.Errorf("decode volumes: %w", err)
	}
	deleted := 0
	for _, v := range decoded.Volumes {
		if !isReclaimableOrphanEVS(v.Name, v.Status, len(v.Attachments), v.Metadata, activeDepIDPrefixes) {
			if strings.HasPrefix(v.Name, "pvc-") {
				progress(fmt.Sprintf("sweep orphan EVS %s (status=%s, attachments=%d): skipped (in-use or active deployment)", v.Name, v.Status, len(v.Attachments)))
			}
			continue
		}
		if !destructive {
			progress(fmt.Sprintf("sweep orphan EVS %s (status=%s): would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)", v.Name, v.Status))
			deleted++
			continue
		}
		// #5028 — THROTTLE. The HCS EVS API rate-limits HARD: firing DELETEs
		// back-to-back on a large backlog trips 429 ("too many 429 error
		// responses") and the reap fails wholesale (live: the 332-volume
		// hw244 heal could not run un-throttled). Space consecutive deletes
		// ≥evsDeleteSpacing apart, ctx-interruptible so a wipe/janitor
		// deadline still bounds the pass.
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		case <-time.After(evsDeleteSpacing):
		}
		dstatus, derr := deleteEVSVolume(ctx, client, hw, region, projectID, v.ID)
		if derr != nil {
			progress(fmt.Sprintf("sweep orphan EVS %s: DELETE failed: %v", v.Name, derr))
			continue
		}
		if dstatus == http.StatusTooManyRequests {
			progress(fmt.Sprintf("sweep orphan EVS %s: still 429 after %d backoff retries — leaving for the next pass", v.Name, evsDelete429MaxRetries))
			continue
		}
		if dstatus >= 400 && dstatus != http.StatusNotFound {
			progress(fmt.Sprintf("sweep orphan EVS %s: DELETE status %d", v.Name, dstatus))
			continue
		}
		progress(fmt.Sprintf("sweep orphan EVS %s: deleted", v.Name))
		deleted++
	}
	return deleted, nil
}

// deleteVPCPeeringsTouching removes every VPC peering whose requester or
// accepter endpoint is the given VPC, so the subsequent cascadeDeleteVPC
// is not blocked by a peering pinning the VPC (HCS #3140). Best-effort —
// peering-list / delete failures are surfaced via progress and never
// abort the sweep. Sibling of purgeVPCPeerings (which is scoped to a
// Sovereign FQDN); this variant is scoped to a single VPC ID for the
// orphan reclaim path where the Sovereign record is already gone.
func deleteVPCPeeringsTouching(ctx context.Context, client *http.Client, hw hwCreds, region, vpcID string, progress func(msg string)) {
	url := fmt.Sprintf("%s/v2.0/vpc/peerings", endpointFor("vpc", region))
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return
	}
	var decoded struct {
		Peerings []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			RequestVPC struct {
				VPCID string `json:"vpc_id"`
			} `json:"request_vpc_info"`
			AcceptVPC struct {
				VPCID string `json:"vpc_id"`
			} `json:"accept_vpc_info"`
		} `json:"peerings"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return
	}
	for _, pr := range decoded.Peerings {
		if pr.RequestVPC.VPCID != vpcID && pr.AcceptVPC.VPCID != vpcID {
			continue
		}
		delURL := fmt.Sprintf("%s/v2.0/vpc/peerings/%s", endpointFor("vpc", region), pr.ID)
		if _, dStatus, dErr := doSignedRequest(client, hw, http.MethodDelete, delURL, nil); dErr != nil || (dStatus >= 400 && dStatus != http.StatusNotFound) {
			progress(fmt.Sprintf("sweep orphan VPC: delete peering %s failed", pr.Name))
			continue
		}
		progress("sweep orphan VPC: deleted peering " + pr.Name)
	}
}

// VPCQuota returns the project's VPC quota usage as (used, limit). HCS
// API: GET /v1/{project_id}/quotas?type=vpc returns
// `{quotas: {resources: [{type: "vpc", used: N, quota: M}, ...]}}`.
//
// G68 (Refs #2617): catalyst-api Phase 0 pre-flight check. Without it,
// a fresh prov requesting 2 regions on a project with 5/5 VPCs in use
// fails mid `tofu apply` after Phase 0 has already created CP + EIPs +
// SGs (~3 min sunk cost). With the pre-flight the request fails fast
// with `HCS VPC quota exhausted (5/5)` before any resource is created.
//
// Returns (used, limit, err). On any API or decode failure returns
// (0, 0, err) — the caller MUST treat err != nil as "quota unknown,
// proceed" to avoid blocking provs when the quota endpoint is
// transiently unavailable. The pre-flight is a fast-fail signal, not a
// hard gate; the canonical authority is the tofu apply error path.
func (p *Provider) VPCQuota(ctx context.Context, ak, sk, projectID, region string) (used, limit int, err error) {
	hw := hwCreds{AccessKey: ak, SecretKey: sk, ProjectID: projectID}
	client := httpClientFor(providers.ProviderCreds{Raw: map[string]string{"region": region}})
	url := fmt.Sprintf("%s/v1/%s/quotas?type=vpc", endpointFor("vpc", region), projectID)
	resp, status, reqErr := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if reqErr != nil {
		return 0, 0, fmt.Errorf("VPC quota GET: %w", reqErr)
	}
	if status >= 400 {
		return 0, 0, fmt.Errorf("VPC quota GET: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		Quotas struct {
			Resources []struct {
				Type  string `json:"type"`
				Used  int    `json:"used"`
				Quota int    `json:"quota"`
			} `json:"resources"`
		} `json:"quotas"`
	}
	if jerr := json.Unmarshal(resp, &decoded); jerr != nil {
		return 0, 0, fmt.Errorf("decode VPC quota: %w", jerr)
	}
	for _, r := range decoded.Quotas.Resources {
		if r.Type == "vpc" {
			return r.Used, r.Quota, nil
		}
	}
	return 0, 0, fmt.Errorf("VPC quota: type=vpc resource not present in HCS quota response")
}

type hwSG struct {
	ID   string
	Name string
}

func listSGsByName(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string) ([]hwSG, error) {
	url := fmt.Sprintf("%s/v1/%s/security-groups", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("SG list: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		SecurityGroups []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"security_groups"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode SG list: %w", err)
	}
	prefix := "catalyst-" + strings.ReplaceAll(sovereignFQDN, ".", "-")
	out := []hwSG{}
	for _, sg := range decoded.SecurityGroups {
		if strings.HasPrefix(sg.Name, prefix) {
			out = append(out, hwSG{ID: sg.ID, Name: sg.Name})
		}
	}
	return out, nil
}

func deleteSG(ctx context.Context, client *http.Client, hw hwCreds, region, id string) error {
	url := fmt.Sprintf("%s/v1/%s/security-groups/%s", endpointFor("vpc", region), hw.ProjectID, id)
	_, status, err := doSignedRequest(client, hw, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

type hwVPC struct {
	ID   string
	Name string
}

func listVPCsByName(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string) ([]hwVPC, error) {
	url := fmt.Sprintf("%s/v1/%s/vpcs", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("VPC list: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		VPCs []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"vpcs"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode VPC list: %w", err)
	}
	prefix := "catalyst-" + strings.ReplaceAll(sovereignFQDN, ".", "-")
	out := []hwVPC{}
	for _, v := range decoded.VPCs {
		if strings.HasPrefix(v.Name, prefix) {
			out = append(out, hwVPC{ID: v.ID, Name: v.Name})
		}
	}
	return out, nil
}

func deleteVPC(ctx context.Context, client *http.Client, hw hwCreds, region, id string) error {
	url := fmt.Sprintf("%s/v1/%s/vpcs/%s", endpointFor("vpc", region), hw.ProjectID, id)
	_, status, err := doSignedRequest(client, hw, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if status >= 400 && status != http.StatusNotFound {
		return fmt.Errorf("status %d", status)
	}
	return nil
}

// purgeVPCPeerings deletes every VPC peering connection that references
// any of the deployment's VPCs (or carries the per-Sovereign name
// prefix). Failures are recorded per-peering in out.Errors — the VPC
// cascade then proceeds and its own 409s surface any residual.
func purgeVPCPeerings(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN string, vpcs []hwVPC, progress func(msg string), out *providers.WipeResult) {
	vpcSet := map[string]struct{}{}
	for _, v := range vpcs {
		vpcSet[v.ID] = struct{}{}
	}
	prefix := "catalyst-" + strings.ReplaceAll(sovereignFQDN, ".", "-")
	url := fmt.Sprintf("%s/v2.0/vpc/peerings", endpointFor("vpc", region))
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		out.Errors = append(out.Errors, "list VPC peerings: "+err.Error())
		return
	}
	if status >= 400 {
		if status != http.StatusNotFound {
			out.Errors = append(out.Errors, fmt.Sprintf("list VPC peerings: status %d: %s", status, snippet(resp, 240)))
		}
		return
	}
	var decoded struct {
		Peerings []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			RequestVPC struct {
				VPCID string `json:"vpc_id"`
			} `json:"request_vpc_info"`
			AcceptVPC struct {
				VPCID string `json:"vpc_id"`
			} `json:"accept_vpc_info"`
		} `json:"peerings"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		out.Errors = append(out.Errors, "decode VPC peering list: "+err.Error())
		return
	}
	for _, pr := range decoded.Peerings {
		_, reqMatch := vpcSet[pr.RequestVPC.VPCID]
		_, accMatch := vpcSet[pr.AcceptVPC.VPCID]
		if !reqMatch && !accMatch && !strings.HasPrefix(pr.Name, prefix) {
			continue
		}
		delURL := fmt.Sprintf("%s/v2.0/vpc/peerings/%s", endpointFor("vpc", region), pr.ID)
		_, dStatus, dErr := doSignedRequest(client, hw, http.MethodDelete, delURL, nil)
		if dErr != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete VPC peering %s: %s", pr.Name, dErr))
			continue
		}
		if dStatus >= 400 && dStatus != http.StatusNotFound {
			out.Errors = append(out.Errors, fmt.Sprintf("delete VPC peering %s: status %d", pr.Name, dStatus))
			continue
		}
		out.ProviderPurge["vpc_peerings"] = append(out.ProviderPurge["vpc_peerings"], pr.Name)
		progress("deleted VPC peering " + pr.Name)
	}
}

// purgeOBSBuckets empties + deletes every OBS bucket whose name
// matches the per-Sovereign prefix. Same minio-go client + prefix
// strategy internal/hetzner/buckets.go uses.
func purgeOBSBuckets(ctx context.Context, endpoint string, hw hwCreds, region, sovereignFQDN string, progress func(msg string)) (int, error) {
	if strings.TrimSpace(hw.AccessKey) == "" || strings.TrimSpace(hw.SecretKey) == "" {
		return 0, errors.New("obs access/secret keys are empty")
	}
	if strings.TrimSpace(sovereignFQDN) == "" {
		return 0, errors.New("sovereign fqdn is empty")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(hw.AccessKey, hw.SecretKey, ""),
		Secure: true,
		Region: region,
	})
	if err != nil {
		return 0, fmt.Errorf("construct minio client: %w", err)
	}

	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return 0, fmt.Errorf("list buckets: %w", err)
	}
	prefix := BucketNamePrefixForSovereign(sovereignFQDN)
	removed := 0
	for _, b := range buckets {
		if !strings.HasPrefix(b.Name, prefix) {
			continue
		}
		if err := emptyAndRemoveOBSBucket(ctx, client, b.Name); err != nil {
			progress(fmt.Sprintf("obs: remove bucket %s failed: %s", b.Name, err))
			continue
		}
		removed++
		progress("obs: removed bucket " + b.Name)
	}
	return removed, nil
}

// emptyAndRemoveOBSBucket empties a bucket then deletes it. `tofu destroy`
// cannot remove a NON-EMPTY OBS bucket (the huaweicloud_obs_bucket resource
// errors with `BucketNotEmpty` unless force_destroy is set), which is the
// root cause of #4872: every prov's backup bucket outlives its wipe and the
// account marches to the 100-bucket OBS quota, false-failing the next fresh
// prov's Phase-0 `tofu apply` with `Code=TooManyBuckets`.
//
// This mirrors the battle-tested Hetzner purge (internal/hetzner/buckets.go
// purgeBucket) exactly, because the earlier per-object serial implementation
// kept leaking buckets in three ways the #4872 incident hit repeatedly:
//
//  1. Serial RemoveObject (one HTTP round-trip per key) could not drain a
//     populated Harbor / Velero / CNPG backup bucket (thousands of blobs)
//     inside the 5-minute wipe budget — the loop timed out half-empty and the
//     terminal RemoveBucket 409'd BucketNotEmpty, so the bucket survived every
//     wipe. Fix: stream every key into RemoveObjects, which batch-deletes
//     1000 per request (~1000× fewer round-trips).
//  2. ListObjects WITHOUT WithVersions enumerates only current versions, so on
//     a versioned bucket the non-current versions + delete markers survived
//     the empty and RemoveBucket failed with BucketNotEmpty. Fix:
//     WithVersions:true + version-aware batch delete (RemoveObjects consumes
//     ObjectInfo.VersionID).
//  3. The per-object delete error was swallowed (`_ = RemoveObject(...)`), so
//     a single stuck object silently left the bucket non-empty with no
//     diagnostic AND the caller was told the delete succeeded. Fix: collect +
//     return the delete errors so the caller logs them and does NOT skip on to
//     RemoveBucket (which would only 409) or report a phantom success.
//
// Emptying still has TWO parts, both required before DeleteBucket succeeds:
// every object version, and every incomplete multipart upload — an interrupted
// tofu apply or CSI backup can leave dangling parts that keep the bucket
// non-empty even after all listable objects are gone.
//
// Shared by the per-deployment wipe purge (purgeOBSBuckets) and the
// project-wide janitor reclaim (SweepOrphanOBSBuckets). Idempotent: an
// already-absent bucket (fast 404 path) is a no-op nil, so a re-run after a
// prior partial purge simply finds the bucket already gone.
func emptyAndRemoveOBSBucket(ctx context.Context, client *minio.Client, bucket string) error {
	// Fast idempotent 404 path — an already-absent bucket is success.
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		if isNoSuchBucket(err) {
			return nil
		}
		return fmt.Errorf("bucket-exists %s: %w", bucket, err)
	}
	if !exists {
		return nil
	}

	// Step 1 — list every object version + delete marker and stream them
	// into RemoveObjects (version-aware, batched at 1000 per request).
	objectsCh := make(chan minio.ObjectInfo)
	listCtx, listCancel := context.WithCancel(ctx)
	defer listCancel()
	listErrCh := make(chan error, 1)
	go func() {
		defer close(objectsCh)
		for obj := range client.ListObjects(listCtx, bucket, minio.ListObjectsOptions{
			WithVersions: true,
			Recursive:    true,
		}) {
			if obj.Err != nil {
				listErrCh <- obj.Err
				return
			}
			select {
			case objectsCh <- obj:
			case <-listCtx.Done():
				return
			}
		}
		listErrCh <- nil
	}()

	var removeErrs []string
	for re := range client.RemoveObjects(ctx, bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if re.Err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s@%s: %v", re.ObjectName, re.VersionID, re.Err))
		}
	}
	if listErr := <-listErrCh; listErr != nil {
		return fmt.Errorf("list versions in %s: %w", bucket, listErr)
	}
	if len(removeErrs) > 0 {
		// Surface a bounded summary; the full list would flood the SSE log.
		const maxHead = 5
		head := removeErrs
		suffix := ""
		if len(head) > maxHead {
			head = head[:maxHead]
			suffix = fmt.Sprintf("; …and %d more", len(removeErrs)-maxHead)
		}
		return fmt.Errorf("delete %d object(s) in %s failed: %s%s",
			len(removeErrs), bucket, strings.Join(head, "; "), suffix)
	}

	// Step 2 — abort every in-progress multipart upload; a dangling part
	// keeps the bucket non-empty even after all objects are gone.
	for up := range client.ListIncompleteUploads(ctx, bucket, "", true) {
		if up.Err != nil {
			return fmt.Errorf("list incomplete uploads in %s: %w", bucket, up.Err)
		}
		if err := client.RemoveIncompleteUpload(ctx, bucket, up.Key); err != nil {
			return fmt.Errorf("abort multipart %s in %s: %w", up.Key, bucket, err)
		}
	}

	// Step 3 — delete the now-empty bucket; a 404 is idempotent success.
	if err := client.RemoveBucket(ctx, bucket); err != nil {
		if isNoSuchBucket(err) {
			return nil
		}
		return fmt.Errorf("delete bucket %s: %w", bucket, err)
	}
	return nil
}

// isNoSuchBucket reports whether err is the canonical S3 "bucket does not
// exist" signal (NoSuchBucket code or HTTP 404), so the empty+delete path can
// treat an already-gone bucket as idempotent success. Mirrors the same guard
// internal/hetzner/buckets.go uses.
func isNoSuchBucket(err error) bool {
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		return errResp.Code == "NoSuchBucket" || errResp.StatusCode == http.StatusNotFound
	}
	return false
}

// isReclaimableOrphanOBSBucket is the pure match/protection predicate for the
// project-wide OBS-bucket orphan sweep (#4872). OBS buckets carry NO usable
// tags on Kom4DC (Wave 5.4 disabled HCS tags), so — exactly like the EIP /
// keypair / VPC / EVS sweeps — the reclaim decision is driven purely by the
// deterministic `catalyst-<sovereign-dashed>-<dep-id-prefix>` naming
// convention, never by a tag.
//
// A bucket is reclaimable when ALL of:
//   - its name carries the `catalyst-` prefix every per-Sovereign OBS bucket
//     shares (so operator- / bastion-owned buckets, and any non-catalyst
//     bucket, are hard-protected — we NEVER reach outside this platform's
//     own buckets), AND
//   - its name contains NONE of the in-flight deployment-ID 8-char prefixes
//     (protect-by-default: a bucket belonging to any live / parked / failed
//     deployment is never swept — only a genuinely-wiped deployment leaves
//     its prefix out of the active set and its bucket reclaimable).
//
// A `bastion` substring is a defence-in-depth second guard — the catalyst-
// prefix filter already excludes bastion buckets, but the explicit check
// costs nothing. Extracted as a free function so the selection logic is
// unit-testable without an OBS endpoint seam.
func isReclaimableOrphanOBSBucket(name string, activeDepIDPrefixes map[string]struct{}) bool {
	if name == "" {
		return false
	}
	if strings.Contains(name, "bastion") || !strings.HasPrefix(name, "catalyst-") {
		return false
	}
	for prefix := range activeDepIDPrefixes {
		if prefix != "" && strings.Contains(name, prefix) {
			return false
		}
	}
	return true
}

// SweepOrphanOBSBuckets lists every OBS bucket in the account and reclaims
// catalyst-owned buckets that belong to no in-flight deployment (#4872). It
// is the object-storage sibling of SweepOrphanEIPs / Keypairs / VPCs / EVS:
// the per-deployment Wipe purges only the buckets matching the CURRENT
// Sovereign's prefix (purgeOBSBuckets), so a bucket from a PRIOR failed /
// wiped prov — or one whose wipe never ran / timed out — falls out of reach
// and piles up against the OBS 100-bucket account quota until a fresh prov
// fails Phase-0 with `Code=TooManyBuckets` (58 May+June orphans back to hw01
// observed on the #4872 incident).
//
// OBS ListAllMyBuckets is account-wide (not region-scoped), so one pass with
// any region's endpoint reclaims every stranded bucket. Match + protection
// live in isReclaimableOrphanOBSBucket (pure, unit-testable). `destructive`
// gates the real empty+delete (default false → log-only `would-reap`),
// identical to the sibling sweeps. activeDepIDPrefixes is the protect-by-
// default set of in-flight deployment-ID 8-char prefixes. `projectID` is
// unused (S3-compat OBS authenticates by AK/SK) but kept in the signature so
// the janitor wires this sweep exactly like its siblings. Returns the count
// reaped (or would-reap, in log-only mode). Idempotent + safe to run during
// active provisioning.
func (p *Provider) SweepOrphanOBSBuckets(ctx context.Context, ak, sk, projectID, region string, activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(ak) == "" || strings.TrimSpace(sk) == "" {
		return 0, errors.New("obs access/secret keys are empty")
	}
	if strings.TrimSpace(region) == "" {
		region = "me-east-215"
	}
	endpoint := fmt.Sprintf("obs.%s.kom4dc.nationalcloud.om", region)
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(ak, sk, ""),
		Secure: true,
		Region: region,
	})
	if err != nil {
		return 0, fmt.Errorf("construct minio client: %w", err)
	}
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return 0, fmt.Errorf("list buckets: %w", err)
	}
	reaped := 0
	for _, b := range buckets {
		if !isReclaimableOrphanOBSBucket(b.Name, activeDepIDPrefixes) {
			if strings.HasPrefix(b.Name, "catalyst-") {
				progress(fmt.Sprintf("sweep orphan OBS bucket %s: skipped (in-flight deployment or protected)", b.Name))
			}
			continue
		}
		if !destructive {
			progress(fmt.Sprintf("sweep orphan OBS bucket %s: would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)", b.Name))
			reaped++
			continue
		}
		if err := emptyAndRemoveOBSBucket(ctx, client, b.Name); err != nil {
			progress(fmt.Sprintf("sweep orphan OBS bucket %s: remove failed: %s", b.Name, err))
			continue
		}
		progress("sweep orphan OBS bucket " + b.Name + ": deleted")
		reaped++
	}
	return reaped, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

// toProvisionerRequest converts the cloud-agnostic ProvisionSpec into
// the legacy provisioner.Request shape. The Wave 4 follow-up will
// extend provisioner.Request with a per-provider Creds map so the
// adapter can pass the AK/SK + project_id without re-using the
// HetznerToken / HetznerProjectID field names.
//
// For Wave 3 we thread the Huawei creds via spec.Raw["huawei_*"]
// which the provisioner's writeTfvars() will pick up alongside the
// existing Hetzner keys.
func toProvisionerRequest(spec providers.ProvisionSpec) (provisioner.Request, error) {
	if spec.SovereignFQDN == "" {
		return provisioner.Request{}, errors.New("huawei: SovereignFQDN required")
	}
	req := provisioner.Request{
		DeploymentID:         spec.DeploymentID,
		SovereignFQDN:        spec.SovereignFQDN,
		HandoverJWTPublicKey: spec.HandoverJWTPublic,
	}
	for _, r := range spec.Regions {
		req.Regions = append(req.Regions, provisioner.RegionSpec{
			Provider:         Name,
			CloudRegion:      r.Code,
			ControlPlaneSize: r.ControlPlaneSize,
			WorkerSize:       r.WorkerSize,
			WorkerCount:      r.WorkerCount,
		})
	}
	for _, pd := range spec.ParentDomains {
		req.ParentDomains = append(req.ParentDomains, provisioner.ParentDomain{
			Name:          pd.Name,
			Role:          pd.Role,
			RegistrarKind: pd.Registrar,
		})
	}
	if v := spec.Raw["workerCount"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerCount = n
		}
	}
	return req, nil
}

// snippet returns the first n bytes of b for safe inclusion in error
// strings (Huawei error bodies can be multi-kilobyte JSON when the
// request is malformed).
func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
