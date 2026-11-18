// capabilities.go — the optional capabilities of this adapter that the
// provisioning engine calls through providers.As: PostApplier after
// `tofu apply`, ApplyErrorClassifier on a failed `tofu apply`. They replace
// the engine's former post-apply hook and its inline list of this cloud's
// apply failures.

package huawei

import (
	"context"
	"fmt"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

var (
	_ providers.PostApplier          = (*Provider)(nil)
	_ providers.ApplyErrorClassifier = (*Provider)(nil)
)

// rotateNATEIPs is the cloud call of PostApply. A package variable only so a
// test can stand in for the cloud API; production never reassigns it.
var rotateNATEIPs = RotateBlocklistedNATEIPs

// PostApply implements providers.PostApplier: Wave 5.93 (#2445) rotates any
// NAT EIP that the free pool assigned from the blocklist, before cloud-init
// pulls from harbor.openova.io. A failed rotation is reported and does not
// fail the prov.
func (p *Provider) PostApply(ctx context.Context, in providers.PostApplyInput) (providers.PostApplyResult, error) {
	progress := in.Progress
	if progress == nil {
		progress = func(string) {}
	}
	s := in.Spec
	rotated, err := rotateNATEIPs(ctx, Name, s.DeploymentID, s.SovereignFQDN,
		s.Creds.Get("access_key"), s.Creds.Get("secret_key"), s.Creds.Get("project_id"), s.Creds.Get("region"), progress)
	if err != nil {
		progress("EIP pre-flight check failed (non-fatal — cloudinit may stall on harbor.openova.io if a blocklisted EIP is in play): " + err.Error())
	} else if rotated > 0 {
		progress(fmt.Sprintf("Wave 5.93: rotated %d blocklisted NAT EIPs before Phase 1", rotated))
	}
	return providers.PostApplyResult{}, nil
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
