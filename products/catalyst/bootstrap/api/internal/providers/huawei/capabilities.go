// capabilities.go — the optional capabilities of this adapter that the
// provisioning engine calls through providers.As: Preflighter before
// `tofu init`, ApplyErrorClassifier on a failed `tofu apply`. They replace the
// engine's former hook variables. The adapter has no PostApplier: the NAT EIP
// it used to swap after `tofu apply` is chosen before the plan now
// (preflight_eip.go), so that the state holds it.

package huawei

import (
	"context"
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

var (
	_ providers.Preflighter          = (*Provider)(nil)
	_ providers.ApplyErrorClassifier = (*Provider)(nil)
)

// The cloud calls of the capabilities. Package variables only so a test can
// stand in for the cloud API; production never reassigns them.
var (
	vpcQuota  = (*Provider).VPCQuota
	sweepVPCs = (*Provider).SweepOrphanVPCs
	chooseNAT = chooseNATAddresses
)

// Preflight implements providers.Preflighter: first the VPC quota
// (checkVPCQuota), whose refusal fails the prov; then the NAT EIP of every
// region (chooseNATAddresses, Wave 5.93 #2445), returned as imports for the
// engine. A failed NAT choice is reported and does not fail the prov: OpenTofu
// then allocates the NAT EIPs itself, as it did before the choice existed.
func (p *Provider) Preflight(ctx context.Context, in providers.PreflightInput) (providers.PreflightResult, error) {
	progress := in.Progress
	if progress == nil {
		progress = func(string) {}
	}
	if err := p.checkVPCQuota(ctx, in, progress); err != nil {
		return providers.PreflightResult{}, err
	}
	imports, err := chooseNAT(ctx, in.Spec, in.InState, progress)
	if err != nil {
		progress("NAT EIP pre-flight failed (non-fatal — OpenTofu allocates the NAT EIPs; cloudinit may stall on harbor.openova.io if one is blocklisted): " + err.Error())
		return providers.PreflightResult{}, nil
	}
	return providers.PreflightResult{Imports: imports}, nil
}

// checkVPCQuota is the HCS VPC-quota pre-flight
// (G68 #2617, #4431, #6225). The project has a hard cap on VPCs (Kom4DC
// me-east-215 default 5; the raise endpoint is not tenant-facing), and a prov
// needs one VPC per region. Over the cap, the cause is almost always
// catalyst-* VPCs leaked by a prior wipe whose teardown lagged, so the
// orphans are reclaimed in-band (destructive: this prov needs the room; the
// prefixes of in.ProtectedIDs are never touched, #4614) and the quota is read
// again. The check is best-effort: a quota-API failure lets the prov proceed,
// and `tofu apply` decides. vpcPreflightRefuses holds the verdict.
func (p *Provider) checkVPCQuota(ctx context.Context, in providers.PreflightInput, progress func(string)) error {
	creds := in.Spec.Creds
	ak, sk, projectID := creds.Get("access_key"), creds.Get("secret_key"), creds.Get("project_id")
	region := strings.TrimSpace(creds.Get("region"))
	if region == "" {
		region = defaultRegion
	}
	needed := len(in.Spec.Regions)
	var first, after quotaReading
	first.used, first.limit, first.err = vpcQuota(p, ctx, ak, sk, projectID, region)
	if !first.known() {
		progress(fmt.Sprintf("HCS VPC quota check skipped (transient API error): %v", first.err))
		return nil
	}
	progress(fmt.Sprintf("HCS VPC quota: used %d / %d, requesting %d", first.used, first.limit, needed))
	reclaimAttempted := !first.fits(needed)
	if reclaimAttempted {
		progress(fmt.Sprintf("HCS VPC quota over budget (%d/%d, need %d) — reclaiming orphaned catalyst VPCs before failing", first.used, first.limit, needed))
		reclaimed, err := sweepVPCs(p, ctx, ak, sk, projectID, region, in.ProtectedIDs, true, func(msg string) { progress("vpc-reclaim: " + msg) })
		if err != nil {
			progress(fmt.Sprintf("HCS orphan-VPC reclaim error (continuing to re-check quota): %v", err))
		} else {
			progress(fmt.Sprintf("HCS orphan-VPC reclaim freed %d VPC(s); re-reading quota", reclaimed))
		}
		// #6225: a failed re-read lets the prov proceed, as a failed first
		// read does; the project is not known to be insufficient.
		after.used, after.limit, after.err = vpcQuota(p, ctx, ak, sk, projectID, region)
		if after.known() {
			progress(fmt.Sprintf("HCS VPC quota after reclaim: used %d / %d, requesting %d", after.used, after.limit, needed))
		} else {
			progress(fmt.Sprintf("HCS VPC quota re-read after reclaim failed (%v) — reclaimed %d VPC(s); proceeding to tofu apply, which is the canonical authority on project capacity", after.err, reclaimed))
		}
	}
	if vpcPreflightRefuses(needed, first, reclaimAttempted, after) {
		eff := effectiveReading(first, reclaimAttempted, after)
		return fmt.Errorf("HCS VPC quota exhausted: project at %d/%d after orphan reclaim, this prov requests %d more — a concurrent Sovereign genuinely occupies the project; wipe one before retrying", eff.used, eff.limit, needed)
	}
	return nil
}

// ClassifyApplyError implements providers.ApplyErrorClassifier: the HCS
// failures of `tofu apply` that a backoff and a re-plan fix.
//   - Common.0021 / CollectInfoTask-fail (Wave 5.132): the scheduler's
//     per-worker info task timed out; the instance gets a new name salt.
//   - "conflict in the request" on an EIP, VPC or other create (Waves 5.143
//     and 5.149): the project's pool is at its cap until a prior wipe's
//     deletes propagate.
//   - VPC.2030 (Wave 5.158, #2527): the NAT plane has not seen a new EIP yet.
//   - "Provider produced inconsistent result after apply" (#3142).
//
// Addresses are the compute instances the failure names (Wave 5.145); a
// Common.0021 on the same instance three times in a row stops the retries.
func (p *Provider) ClassifyApplyError(stderr string) providers.ApplyErrorClass {
	has := func(s string) bool { return strings.Contains(stderr, s) }
	conflict := has("conflict in the request") && (has("error allocating EIP") || has("error creating"))
	retryable := has("Common.0021") || has("CollectInfoTask-fail") || conflict || has("VPC.2030") ||
		has("Provider produced inconsistent result after apply")
	if !retryable {
		return providers.ApplyErrorClass{}
	}
	reason := "HCS Common.0021 (CollectInfoTask-fail)"
	switch {
	case has("error allocating EIP"):
		reason = "HCS EIP-conflict (quota / propagation)"
	case has("error creating VPC"):
		reason = "HCS VPC-conflict (quota / propagation)"
	case has("error creating") && has("conflict in the request"):
		reason = "HCS resource-conflict (quota / propagation)"
	}
	return providers.ApplyErrorClass{Retryable: true, Reason: reason, Addresses: failedComputeAddrs(stderr), StopOnRepeat: has("Common.0021")}
}

// failedComputeAddrs returns the compute-instance addresses of tofu's error
// block, the text after a `with huaweicloud_compute_instance.` line, such as
// "control_plane[0]" or "worker[\"me-east-215-a-3\"]".
func failedComputeAddrs(stderr string) []string {
	var addrs []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "with huaweicloud_compute_instance."); ok {
			addrs = append(addrs, strings.TrimSuffix(rest, ","))
		}
	}
	return addrs
}
