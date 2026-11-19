// sweeper.go — the capabilities of this adapter that the janitor and the
// wipe reach through providers.As: OrphanSweeper (the five project-wide
// orphan sweeps) and LivenessProber (the name-prefix server probe), and the
// post-destroy volume step of Wipe (#5028). They replace the janitor's type
// assertion on *Provider and its five sweep calls (CLD-111).

package huawei

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

var (
	_ providers.OrphanSweeper  = (*Provider)(nil)
	_ providers.LivenessProber = (*Provider)(nil)
)

// sweepFunc is the shape of the five SweepOrphan* methods.
type sweepFunc func(p *Provider, ctx context.Context, ak, sk, projectID, region string, protected map[string]struct{}, destructive bool, progress func(msg string)) (int, error)

// sweepKind is one kind of SweepOrphans: its stable name (the janitor logs
// orphan<name>Deleted or orphan<name>WouldReap), the time it may take, and
// the sweep it runs.
type sweepKind struct {
	name   string
	budget time.Duration
	sweep  sweepFunc
}

// sweepKinds run in this order. The budgets are those the janitor gave each
// sweep: the VPC cascade and the bucket emptying are slower than a single
// delete, and the throttled EVS reap (#5028, about 0.9 s per delete) needs ten
// minutes to drain a full backlog. A package variable only so a test can stand
// in for the cloud API; production never reassigns it.
var sweepKinds = []sweepKind{
	{"EIPs", 2 * time.Minute, (*Provider).SweepOrphanEIPs},
	{"Keypairs", 2 * time.Minute, (*Provider).SweepOrphanKeypairs},
	{"VPCs", 5 * time.Minute, (*Provider).SweepOrphanVPCs},
	{"EVS", 10 * time.Minute, (*Provider).SweepOrphanEVS},
	{"OBSBuckets", 5 * time.Minute, (*Provider).SweepOrphanOBSBuckets},
}

// SweepKinds implements providers.OrphanSweeper.
func (p *Provider) SweepKinds() []string {
	out := make([]string, 0, len(sweepKinds))
	for _, k := range sweepKinds {
		out = append(out, k.name)
	}
	return out
}

// SweepOrphans implements providers.OrphanSweeper: every kind of sweepKinds
// in turn, each within its budget, with the protected prefixes and the
// destructive gate of in. A kind that fails does not stop the next; the
// errors are joined. The region is in.CloudRegion, else the credentials',
// else the default region. A nil in.ProtectedIDs is refused before any call:
// every sweep would treat every catalyst resource as an orphan.
func (p *Provider) SweepOrphans(ctx context.Context, in providers.SweepInput) (providers.SweepResult, error) {
	progress := in.Progress
	if progress == nil {
		progress = func(string) {}
	}
	if in.ProtectedIDs == nil {
		return providers.SweepResult{}, errors.New("huawei: orphan sweep refused: no protected deployment prefixes")
	}
	hw, err := credsFromProviderCreds(in.Creds)
	if err != nil {
		return providers.SweepResult{}, err
	}
	region := strings.TrimSpace(in.CloudRegion)
	if region == "" {
		region = regionFromCreds(in.Creds)
	}
	res := providers.SweepResult{Deleted: map[string]int{}}
	var errs []error
	for _, k := range sweepKinds {
		kctx, cancel := context.WithTimeout(ctx, k.budget)
		n, serr := k.sweep(p, kctx, hw.AccessKey, hw.SecretKey, hw.ProjectID, region, in.ProtectedIDs, in.Destructive, progress)
		cancel()
		res.Deleted[k.name] = n
		if serr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k.name, serr))
		}
	}
	return res, errors.Join(errs...)
}

// HasLiveInfrastructure implements providers.LivenessProber: whether any ECS
// still bears the deployment's name prefix (HasServersByNamePrefix, #5327).
// Wave 5.4 disabled tags on HCS, so the tag-scoped ListServers can return zero
// for a live deployment; this probe does not read tags. Missing credentials
// are an error, never "gone".
func (p *Provider) HasLiveInfrastructure(ctx context.Context, deploymentID, sovereignFQDN string, creds providers.ProviderCreds) (bool, error) {
	hw, err := credsFromProviderCreds(creds)
	if err != nil {
		return true, err
	}
	alive, err := p.HasServersByNamePrefix(ctx, hw.AccessKey, hw.SecretKey, hw.ProjectID, regionFromCreds(creds), sovereignFQDN, deploymentID)
	if err != nil {
		return true, fmt.Errorf("huawei name-prefix probe: %w", err)
	}
	return alive, nil
}

// reapDetachedVolumes is the last step of Wipe (#5028). `tofu destroy`
// removes what the module declared, not the `pvc-*` volumes the storage
// driver created at run time; once the nodes are gone nothing detaches and
// deletes them, and they fill the project's volume quota. This reaps the
// detached `pvc-*` volumes of the wiped deployment, destructively (the
// operator asked to wipe), while every prefix of spec.ProtectedIDs stays
// protected. The wiped deployment's own prefix is removed from that set. A
// volume that is attached, or not named `pvc-*`, is never deleted
// (isReclaimableOrphanEVS). A nil spec.ProtectedIDs reaps nothing. Best
// effort: an error is reported, never returned.
func (p *Provider) reapDetachedVolumes(ctx context.Context, spec providers.WipeSpec, hw hwCreds, region string, progress func(msg string)) int {
	if spec.ProtectedIDs == nil {
		return 0
	}
	protected := maps.Clone(spec.ProtectedIDs)
	if len(spec.DeploymentID) >= 8 {
		delete(protected, spec.DeploymentID[:8])
	}
	reaped, err := p.SweepOrphanEVS(ctx, hw.AccessKey, hw.SecretKey, hw.ProjectID, region, protected, true, func(msg string) {
		progress("post-destroy EVS backstop: " + msg)
	})
	if err != nil {
		progress("post-destroy EVS backstop error (#5028): " + err.Error())
	}
	if reaped > 0 {
		progress(fmt.Sprintf("post-destroy EVS backstop reaped %d detached CSI volume(s) tofu destroy left orphaned (#5028)", reaped))
	}
	return reaped
}
