// Package hetzner is the providers.CloudProvider adapter for Hetzner
// Cloud.
//
// Architectural role: adapter (Ports + Adapters / hexagonal). The
// catalyst-api handlers speak the providers.CloudProvider interface
// (the "port"); this package translates each interface method into
// calls against the underlying internal/hetzner (orphan purge +
// object-storage bucket lifecycle) and internal/provisioner
// (`tofu apply` + `tofu destroy` against infra/providers/hetzner/)
// packages.
//
// Wave 2 (this PR) — the adapter is registered into providers.registry
// at init() time but the catalyst-api handlers continue to call
// internal/hetzner + internal/provisioner directly. This is intentional:
// the refactor introduces the seam without rewriting the handler
// bodies. Wave 3 switches the handlers to providers.Get(dep.Provider)
// once Huawei has a concrete implementation worth dispatching on.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase-0), the
// adapter's Provision method shells out to `tofu apply`; the orphan
// sweep + bucket purge are recovery fallbacks invoked by Wipe, never
// by Provision.

package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	hcloudpurge "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/hetzner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// Name is the canonical provider identifier ("hetzner"). MUST match
// the enum row in core/controllers/environment/api/v1/types.go.
const Name = "hetzner"

// Provider implements providers.CloudProvider for Hetzner Cloud.
//
// All state-bearing dependencies (the underlying *provisioner.Provisioner
// instance) are created lazily on the first Provision call so that
// init-time registration does not pay the cost of building a fully
// configured Provisioner that may never be used (e.g. when the catalyst-
// api Pod only handles GETs against persisted records).
type Provider struct{}

// New returns a ready-to-use Hetzner adapter. Cheap; no I/O.
func New() *Provider { return &Provider{} }

// init registers the adapter so providers.Get("hetzner") returns a
// usable instance. Per registry.go RegisterProvider contract, this
// MUST be called from an init() — never from runtime code.
func init() {
	providers.RegisterProvider(Name, New())
}

// Name returns the canonical provider name.
func (p *Provider) Name() string { return Name }

// Provision delegates to provisioner.Provisioner.Provision with a
// Hetzner-shaped Request constructed from the cloud-agnostic
// ProvisionSpec.
//
// MUST stay byte-equivalent to the existing handler path through
// internal/provisioner — Wave 2 is refactor-only.
func (p *Provider) Provision(ctx context.Context, spec providers.ProvisionSpec, events chan<- providers.ProvisionEvent) (*providers.ProvisionResult, error) {
	if len(spec.Regions) == 0 {
		return nil, errors.New("hetzner provider: at least one region required")
	}
	req, err := toProvisionerRequest(spec)
	if err != nil {
		return nil, err
	}
	// Bridge the cloud-agnostic event channel into the provisioner's
	// concrete provisioner.Event channel. Closes when ctx cancels.
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
		SovereignFQDN:  res.SovereignFQDN,
		ControlPlaneIP: res.ControlPlaneIP,
		LoadBalancerIP: res.LoadBalancerIP,
		ConsoleURL:     res.ConsoleURL,
		GitOpsRepoURL:  res.GitOpsRepoURL,
		KubeconfigPath: res.KubeconfigPath,
	}, nil
}

// Wipe runs the canonical purge sequence — tofu destroy + Hetzner
// orphan sweep + S3 bucket purge.
//
// Mirrors handler.WipeDeployment's body but with the call sites
// hidden behind the interface. Wave 2 does NOT migrate the actual
// handler to call this — the handler retains its existing inline
// implementation so the refactor stays byte-equivalent.
func (p *Provider) Wipe(ctx context.Context, spec providers.WipeSpec, progress func(msg string)) (*providers.WipeResult, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if spec.SovereignFQDN == "" {
		return nil, errors.New("hetzner provider: SovereignFQDN required for Wipe")
	}
	token := spec.Creds.Get("hcloud_token")
	if token == "" {
		return nil, errors.New("hetzner provider: hcloud_token required for Wipe")
	}

	out := &providers.WipeResult{
		DeploymentID:  spec.DeploymentID,
		SovereignFQDN: spec.SovereignFQDN,
		ProviderPurge: map[string][]string{},
		WipedAt:       time.Now().UTC(),
	}

	// Step 1 — tofu destroy. Built from a minimal Request shape so
	// provisioner.Destroy's writeTfvars renders a valid tfvars file
	// for the destroy run.
	destroyReq := provisioner.Request{
		DeploymentID:     spec.DeploymentID,
		SovereignFQDN:    spec.SovereignFQDN,
		HetznerToken:     token,
		HetznerProjectID: spec.Creds.Get("hcloud_project_id"),
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

	// Step 2 — Hetzner orphan sweep.
	purge, err := hcloudpurge.Purge(ctx, token, spec.SovereignFQDN, progress)
	if err != nil {
		out.Errors = append(out.Errors, "hetzner purge: "+err.Error())
	}
	if len(purge.Servers) > 0 {
		out.ProviderPurge["servers"] = purge.Servers
	}
	if len(purge.LoadBalancers) > 0 {
		out.ProviderPurge["load_balancers"] = purge.LoadBalancers
	}
	if len(purge.Networks) > 0 {
		out.ProviderPurge["networks"] = purge.Networks
	}
	if len(purge.Firewalls) > 0 {
		out.ProviderPurge["firewalls"] = purge.Firewalls
	}
	if len(purge.SSHKeys) > 0 {
		out.ProviderPurge["ssh_keys"] = purge.SSHKeys
	}
	if len(purge.Volumes) > 0 {
		out.ProviderPurge["volumes"] = purge.Volumes
	}
	if len(purge.PrimaryIPs) > 0 {
		out.ProviderPurge["primary_ips"] = purge.PrimaryIPs
	}
	if len(purge.FloatingIPs) > 0 {
		out.ProviderPurge["floating_ips"] = purge.FloatingIPs
	}

	// Step 3 — object-storage bucket purge. Per issue #166 the
	// handler MUST surface a HARD error when S3 creds are missing
	// from BOTH the wipe request body AND the in-memory dep.Request
	// (the on-disk record redacts S3 creds per the credential-
	// hygiene principle, so a Pod restart after provision drops
	// them). The pre-#166 silent-skip leaked 10 buckets on
	// omantel.biz alone; the explicit error message names both
	// sources so future readers immediately see why bucket-purge
	// was skipped.
	accessKey := spec.Creds.Get("object_storage_access_key")
	secretKey := spec.Creds.Get("object_storage_secret_key")
	region := spec.Creds.Get("object_storage_region")
	if accessKey != "" && secretKey != "" && region != "" {
		bucketCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		removed, berr := hcloudpurge.PurgeBuckets(
			bucketCtx,
			hcloudpurge.PurgeBucketsConfig{
				AccessKey: accessKey,
				SecretKey: secretKey,
				Region:    region,
			},
			spec.SovereignFQDN,
			spec.DeploymentID,
			progress,
		)
		cancel()
		if berr != nil {
			out.Errors = append(out.Errors, "object-storage bucket purge: "+berr.Error())
		}
		prefix := hcloudpurge.BucketNamePrefixForSovereign(spec.SovereignFQDN)
		for i := 0; i < removed; i++ {
			out.S3Buckets = append(out.S3Buckets, prefix+"-*")
		}
	} else {
		out.Errors = append(out.Errors,
			"object-storage credentials not supplied on request body AND not retained on in-memory deployment record (Pod likely restarted) — bucket purge SKIPPED; re-issue the wipe with objectStorageAccessKey/SecretKey/Region in the body, or run the manual sweep from docs/feedback_idempotent_iac_purge.md")
	}

	// G103 (Refs #2670) — Hetzner-side post-wipe zero-orphan
	// verification. Parity with the HCS port (huawei/provider.go
	// verifyZeroOrphans) so the G104 (Refs #2671) 3xZT certification
	// script can certify either provider via the same single signal.
	//
	// Walks every Hetzner Cloud resource kind the cascade purge targets
	// (servers / load_balancers / networks / firewalls / ssh_keys /
	// volumes / primary_ips / floating_ips) ONE MORE TIME and records
	// any catalyst.openova.io/sovereign-labelled survivor — bastion-*
	// hard-excluded. VerifiedZeroOrphans=true only when every kind
	// returns empty.
	verifyReport, verified := hcloudpurge.VerifyZeroOrphans(ctx, token, spec.SovereignFQDN, progress)
	out.VerifiedZeroOrphans = verified
	if !verified {
		out.ResidualOrphans = verifyReport.AsMap()
		out.Errors = append(out.Errors, fmt.Sprintf(
			"G103 wipe-contract violation: %d catalyst-* Hetzner resource(s) survived wipe (bastion-* excluded) — see ResidualOrphans",
			verifyReport.Total(),
		))
	}
	if len(verifyReport.Errors) > 0 {
		out.Errors = append(out.Errors, verifyReport.Errors...)
	}

	return out, nil
}

// ListServers returns the current Hetzner-side server inventory for
// one deployment by querying the Hetzner Cloud API with the label
// selector `catalyst.openova.io/sovereign=<fqdn>`.
//
// Wave 2 returns nil + nil (placeholder) — the catalyst-api handlers
// that need this surface today read from kubeconfig+kubectl. Wave 3
// will wire this through to a direct hcloud API call once at least
// one handler genuinely needs cloud-side inventory (the wizard's
// infrastructure tab is the main planned caller; today it reads
// from the tofu outputs).
func (p *Provider) ListServers(ctx context.Context, deploymentID, sovereignFQDN string, creds providers.ProviderCreds) ([]providers.ServerInfo, error) {
	// Intentional minimal implementation — see method comment.
	// Returning nil signals "no inventory available via this seam
	// today"; callers fall back to existing paths.
	return nil, nil
}

// ValidateCreds confirms the Hetzner Cloud API token by issuing a
// GET against /v1/servers (small list, low cost). Delegates to
// internal/hetzner.ValidateToken.
func (p *Provider) ValidateCreds(ctx context.Context, creds providers.ProviderCreds) error {
	token := creds.Get("hcloud_token")
	if token == "" {
		return errors.New("hetzner: hcloud_token is required")
	}
	ok, err := hcloudpurge.ValidateToken(ctx, token)
	if err != nil {
		return fmt.Errorf("hetzner: validate token: %w", err)
	}
	if !ok {
		return errors.New("hetzner: hcloud_token was rejected by Hetzner Cloud API")
	}
	return nil
}

// BucketNameForDeployment returns the deterministic per-deployment
// Hetzner Object Storage bucket name. Pure function; no I/O.
// Implements providers.ObjectStorageNamer.
func (p *Provider) BucketNameForDeployment(sovereignFQDN, deploymentID string) string {
	return hcloudpurge.BucketNameForSovereign(sovereignFQDN, deploymentID)
}

// BucketNamePrefixForSovereign returns the Sovereign-scoped prefix
// every Hetzner bucket for one Sovereign shares. Pure function.
// Implements providers.ObjectStorageNamer.
func (p *Provider) BucketNamePrefixForSovereign(sovereignFQDN string) string {
	return hcloudpurge.BucketNamePrefixForSovereign(sovereignFQDN)
}

// toProvisionerRequest converts a cloud-agnostic ProvisionSpec into
// the legacy provisioner.Request the existing Provision path
// consumes. The mapping is intentionally narrow — only the fields
// the existing Hetzner-only path uses are copied; extension fields
// for other clouds stay untouched in spec.Raw / spec.Creds and
// future adapters will consume them directly.
func toProvisionerRequest(spec providers.ProvisionSpec) (provisioner.Request, error) {
	if spec.SovereignFQDN == "" {
		return provisioner.Request{}, errors.New("hetzner: SovereignFQDN required")
	}
	req := provisioner.Request{
		DeploymentID:         spec.DeploymentID,
		SovereignFQDN:        spec.SovereignFQDN,
		HetznerToken:         spec.Creds.Get("hcloud_token"),
		HetznerProjectID:     spec.Creds.Get("hcloud_project_id"),
		HandoverJWTPublicKey: spec.HandoverJWTPublic,
	}
	// Per-region payload (singular fields derived from Regions[0]
	// inside Validate()).
	for _, r := range spec.Regions {
		req.Regions = append(req.Regions, provisioner.RegionSpec{
			Provider:         Name,
			CloudRegion:      r.Code,
			ControlPlaneSize: r.ControlPlaneSize,
			WorkerSize:       r.WorkerSize,
			WorkerCount:      r.WorkerCount,
		})
	}
	// Parent domains.
	for _, pd := range spec.ParentDomains {
		req.ParentDomains = append(req.ParentDomains, provisioner.ParentDomain{
			Name:          pd.Name,
			Role:          pd.Role,
			RegistrarKind: pd.Registrar,
		})
	}
	// Raw knobs the adapter consumes today.
	if v := spec.Raw["workerCount"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.WorkerCount = n
		}
	}
	return req, nil
}
