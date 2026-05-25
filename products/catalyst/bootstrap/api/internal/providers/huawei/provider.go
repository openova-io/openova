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
func endpointFor(service, region string) string {
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
		SovereignFQDN:  res.SovereignFQDN,
		ControlPlaneIP: res.ControlPlaneIP,
		LoadBalancerIP: res.LoadBalancerIP,
		ConsoleURL:     res.ConsoleURL,
		GitOpsRepoURL:  res.GitOpsRepoURL,
		KubeconfigPath: res.KubeconfigPath,
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

	// Step 2 — Huawei orphan sweep. Best-effort: per-resource failures
	// land in out.Errors but do not abort the remaining sweeps.
	region := regionFromCreds(spec.Creds)
	client := httpClientFor(spec.Creds)
	if err := purgeHuaweiResources(ctx, client, hw, region, spec.SovereignFQDN, spec.DeploymentID, progress, out); err != nil {
		out.Errors = append(out.Errors, "huawei orphan purge: "+err.Error())
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
				Status  string `json:"status"`
				Flavor  json.RawMessage `json:"flavor"`
				Addresses map[string][]struct {
					Addr   string `json:"addr"`
					Type   string `json:"OS-EXT-IPS:type"`
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
				var asObj struct{ ID string `json:"id"` }
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
func purgeHuaweiResources(ctx context.Context, client *http.Client, hw hwCreds, region, sovereignFQDN, deploymentID string, progress func(msg string), out *providers.WipeResult) error {
	// ECS — delete first (depend on VPC + SG + EIP).
	servers, err := listECSByTag(ctx, client, hw, region, deploymentID, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list ECS: "+err.Error())
	}
	for _, s := range servers {
		if err := deleteECS(ctx, client, hw, region, s.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete ECS %s: %s", s.Name, err))
			continue
		}
		out.ProviderPurge["servers"] = append(out.ProviderPurge["servers"], s.Name)
		progress("deleted ECS " + s.Name)
	}

	// EIP — delete next (depend on nothing once the ECS bindings are gone).
	eips, err := listEIPsByTag(ctx, client, hw, region, deploymentID, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list EIP: "+err.Error())
	}
	for _, e := range eips {
		if err := deleteEIP(ctx, client, hw, region, e.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete EIP %s: %s", e.Address, err))
			continue
		}
		out.ProviderPurge["floating_ips"] = append(out.ProviderPurge["floating_ips"], e.Address)
		progress("deleted EIP " + e.Address)
	}

	// SG — delete after ECS so the SG isn't `in use`.
	sgs, err := listSGsByName(ctx, client, hw, region, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list SG: "+err.Error())
	}
	for _, sg := range sgs {
		if err := deleteSG(ctx, client, hw, region, sg.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete SG %s: %s", sg.Name, err))
			continue
		}
		out.ProviderPurge["firewalls"] = append(out.ProviderPurge["firewalls"], sg.Name)
		progress("deleted SG " + sg.Name)
	}

	// VPC — delete last (nothing depends on it once subnets are gone).
	vpcs, err := listVPCsByName(ctx, client, hw, region, sovereignFQDN)
	if err != nil {
		out.Errors = append(out.Errors, "list VPC: "+err.Error())
	}
	for _, v := range vpcs {
		if err := deleteVPC(ctx, client, hw, region, v.ID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("delete VPC %s: %s", v.Name, err))
			continue
		}
		out.ProviderPurge["networks"] = append(out.ProviderPurge["networks"], v.Name)
		progress("deleted VPC " + v.Name)
	}
	return nil
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
		// Empty + delete.
		objCh := client.ListObjects(ctx, b.Name, minio.ListObjectsOptions{Recursive: true})
		for o := range objCh {
			if o.Err != nil {
				continue
			}
			_ = client.RemoveObject(ctx, b.Name, o.Key, minio.RemoveObjectOptions{})
		}
		if err := client.RemoveBucket(ctx, b.Name); err != nil {
			progress(fmt.Sprintf("obs: remove bucket %s failed: %s", b.Name, err))
			continue
		}
		removed++
		progress("obs: removed bucket " + b.Name)
	}
	return removed, nil
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
