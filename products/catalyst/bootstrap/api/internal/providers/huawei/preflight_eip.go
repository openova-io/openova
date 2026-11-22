// The NAT EIP pre-flight: every region's NAT EIP is chosen BEFORE `tofu plan`,
// so that OpenTofu's state holds the address the NAT gateway uses.
//
// Why a choice at all (Wave 5.93, #2445): some EIPs of the Huawei me-east-215
// free pool are refused by third-party IP-reputation lists for Contabo
// 45.151.123.0/24 (harbor.openova.io), so a region whose NAT EIP is one of
// them has no egress to harbor and cloudinit stalls. catalyst-api runs on
// Contabo and cannot probe a Huawei EIP's egress (that needs an ECS behind
// the EIP), so the choice reads a blocklist (blocklist()): the static seed
// below, CATALYST_HUAWEI_NAT_EIP_BLOCKLIST=ip1,ip2,..., and the learned store
// of eip_blocklist_store.go (#3722), into which every refused address is
// recorded with a TTL. CATALYST_HUAWEI_NAT_EIP_ROTATE_ALL=true (#3716)
// refuses every region's first draw as well, to drain a poisoned pool.
//
// Why before the plan: until CLD-G12 (2026-11-21) the choice was a swap right
// after `tofu apply` — allocate a clean EIP, delete the SNAT rule, create a new
// one, delete the old EIP — through the cloud API alone. The state kept the
// deleted EIP and SNAT rule, so every later plan wanted to create both and to
// replace the security-group rules that name the NAT address, and the readers
// of the state (the network registry, the Crossplane adoption claims) named
// an address that no longer existed. Now chooseNATAddresses allocates the EIP
// that tofu would create, with the module's own bandwidth settings, until one
// is not on the blocklist, and returns it as an import: the engine imports it
// into huaweicloud_vpc_eip.nat[<region>] before the plan, the plan creates the
// SNAT rule and the security-group rules from it, and the first apply renames
// its bandwidth to the module's name. After the install a plan shows no change,
// and the wipe's `tofu destroy` deletes the EIP with the rest.

package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// hwBlocklistedEIPs is the seed list of Huawei me-east-215 EIPs observed
// 2026-05-25 to be blocklisted for outbound to Contabo 45.151.123.0/24
// (harbor.openova.io). Caller can extend via CATALYST_HUAWEI_NAT_EIP_BLOCKLIST.
var hwBlocklistedEIPs = map[string]bool{
	"212.72.24.48": true, // hw01 ~10-attempt observation, hw03 b-region NAT
	"212.72.24.14": true, // hw01 b-region NAT, hw03 b-region NAT
}

// blocklist returns the merged set of seed + env-supplied + persisted
// (learned) blocklist.
//
// Wave 5.157 (#3722): the third source is the zero-touch fix. The static
// seed only knows .48/.14; the kom4dc free-pool gets poisoned by repeated
// wipe→re-prov cycles (released EIPs stay un-routable to harbor for
// hours), so a fresh prov draws a poisoned IP that ISN'T in the seed.
// chooseNATAddresses RECORDS every EIP it refuses to an on-PVC store
// (eip_blocklist_store.go); merging that store here means prov N's poison
// becomes prov N+1's auto-avoid set — no operator env var, no flux
// suspend. TTL-pruned on load so a recovered EIP ages back in.
func blocklist() map[string]bool {
	out := map[string]bool{}
	for k := range hwBlocklistedEIPs {
		out[k] = true
	}
	if v := os.Getenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST"); v != "" {
		for _, ip := range strings.Split(v, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				out[ip] = true
			}
		}
	}
	// Learned poison persisted across provs on the catalyst-api PVC.
	for ip := range loadPersistedBlocklist(time.Now()) {
		out[ip] = true
	}
	return out
}

// natEIPResource is the module's resource instance of the NAT EIP of the
// region with the given code (infra/providers/huawei/main.tf).
func natEIPResource(code string) string {
	return fmt.Sprintf("huaweicloud_vpc_eip.nat[%q]", code)
}

// The bandwidth of the module's NAT EIP (infra/providers/huawei/main.tf,
// resource huaweicloud_vpc_eip.nat). An allocated EIP carries the same
// settings, so the import differs from the configuration in the bandwidth's
// name alone, which the first apply changes in place.
const (
	natEIPType          = "5_bgp"
	natBandwidthSize    = 100
	natBandwidthShare   = "PER"
	natBandwidthCharged = "traffic"
)

// chooseNATAddresses gives the NAT EIP of every region of spec that the state
// does not hold yet (inState) an address that is not on the blocklist, and
// returns one import per region for the engine (providers.PreflightResult).
//
// A region whose EIP already exists keeps it: the module's own (bandwidth
// <prefix>-<region>-nat-bw) or one an earlier attempt of this deployment
// allocated (<prefix>-<region>-nat-preflight-<n>, not on the blocklist), so a
// retried install allocates nothing twice. Otherwise EIPs are allocated with
// the module's bandwidth settings, one after the other; a refused one is held,
// so that the pool cannot hand the same address back, until every region has
// one or the hold budget is spent. The held ones are recorded to the learned
// blocklist and released before the plan.
//
// The hold budget keeps the project within its EIP quota: the EIPs that exist
// (the listing) plus one per region still to choose plus the held ones never
// exceed the quota (publicIPQuota; 10 on kom4dc), and at most natMaxHeld are
// held. It runs before OpenTofu allocates any EIP of the install: the
// control-plane and load-balancer EIPs come after the held ones are released,
// so the install ends at its own count (6 for two regions). A wipe records
// every address it released (provider.go), so right after one the pool hands
// back up to 6 refused addresses first; for two regions and the bastion's one
// EIP the worst moment is 1 + 2 + 7 = 10 EIPs, the quota, never above it.
// A region left without a clean address gets no import, and OpenTofu
// allocates its NAT EIP as before.
//
// On a failed cloud call every EIP this call allocated is released and the
// error returned: no address is left that no state knows.
func chooseNATAddresses(ctx context.Context, spec providers.ProvisionSpec, inState map[string]bool, progress func(msg string)) ([]providers.StateImport, error) {
	if progress == nil {
		progress = func(string) {}
	}
	// #3716: the signing creds are the bare keys of the credentials map.
	hw := hwCreds{AccessKey: spec.Creds.Get("access_key"), SecretKey: spec.Creds.Get("secret_key"), ProjectID: spec.Creds.Get("project_id")}
	switch {
	case hw.AccessKey == "":
		return nil, fmt.Errorf("huawei: access_key is required (CATALYST_HUAWEI_ACCESS_KEY missing on catalyst-api — see secret huawei-operator-creds)")
	case hw.SecretKey == "":
		return nil, fmt.Errorf("huawei: secret_key is required (CATALYST_HUAWEI_SECRET_KEY missing on catalyst-api)")
	case hw.ProjectID == "":
		return nil, fmt.Errorf("huawei: project_id is required (CATALYST_HUAWEI_PROJECT_ID missing on catalyst-api)")
	case len(spec.DeploymentID) < 8 || strings.TrimSpace(spec.SovereignFQDN) == "":
		return nil, fmt.Errorf("huawei: the NAT EIP pre-flight needs the Sovereign FQDN and a deployment id of at least 8 characters")
	}
	var codes []string
	for _, r := range spec.Regions {
		code := strings.TrimSpace(r.Code)
		if code == "" {
			code = strings.TrimSpace(r.CloudRegion)
		}
		if code == "" {
			continue
		}
		if inState[natEIPResource(code)] {
			progress(fmt.Sprintf("NAT EIP of region %s: the state holds %s, nothing to choose", code, natEIPResource(code)))
			continue
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil, nil
	}
	region := regionFromCreds(spec.Creds)
	client := httpClientFor(spec.Creds)
	existing, err := listPublicIPs(client, hw, region)
	if err != nil {
		return nil, err
	}
	bl := blocklist()
	rotateAll := strings.EqualFold(strings.TrimSpace(os.Getenv("CATALYST_HUAWEI_NAT_EIP_ROTATE_ALL")), "true")
	if rotateAll {
		progress("Wave 5.156 (#3716): CATALYST_HUAWEI_NAT_EIP_ROTATE_ALL=true — refusing the first NAT EIP drawn for every region to drain a poisoned free-pool")
	}
	prefix := "catalyst-" + strings.ReplaceAll(strings.TrimSpace(spec.SovereignFQDN), ".", "-") + "-" + spec.DeploymentID[:8]
	holdBudget := natMaxHeld
	if room := publicIPQuota(client, hw, region) - len(existing) - len(codes); room < holdBudget {
		holdBudget = room
	}
	var imports []providers.StateImport
	var chosen, held []rawPublicIP
	release := func(list []rawPublicIP) {
		for _, e := range list {
			if err := deleteEIP(ctx, client, hw, region, e.ID); err != nil {
				progress(fmt.Sprintf("NAT EIP pre-flight: could not release %s (%s): %v — the wipe's name-prefix purge removes it", e.Address, e.BandwidthName, err))
			}
		}
	}
	for _, code := range codes {
		if e, ok := reusableNATEIP(existing, prefix, code, bl); ok {
			progress(fmt.Sprintf("NAT EIP of region %s: keeps %s (%s) of an earlier attempt", code, e.Address, e.BandwidthName))
			imports = append(imports, providers.StateImport{Address: natEIPResource(code), ID: e.ID, Label: e.Address})
			continue
		}
		for n := 1; ; n++ {
			if len(held) >= holdBudget {
				progress(fmt.Sprintf("NAT EIP of region %s: no address off the blocklist within %d held — OpenTofu allocates it", code, holdBudget))
				break
			}
			e, err := allocateNATEIP(client, hw, region, fmt.Sprintf("%s-%s-nat-preflight-%d", prefix, code, n))
			if err != nil {
				release(held)
				release(chosen)
				return nil, err
			}
			if bl[e.Address] || (rotateAll && n == 1) {
				progress(fmt.Sprintf("NAT EIP of region %s: %s is refused (blocklist or ROTATE_ALL), holding it and drawing again", code, e.Address))
				held = append(held, e)
				continue
			}
			progress(fmt.Sprintf("NAT EIP of region %s: %s (%s), for the plan to import as %s", code, e.Address, e.ID, natEIPResource(code)))
			chosen = append(chosen, e)
			imports = append(imports, providers.StateImport{Address: natEIPResource(code), ID: e.ID, Label: e.Address})
			break
		}
	}
	if len(held) > 0 {
		// Wave 5.157 (#3722) — persist the poison BEFORE releasing it, so the
		// next prov does not draw it again from the pool.
		addrs := make([]string, 0, len(held))
		for _, e := range held {
			addrs = append(addrs, e.Address)
		}
		if err := recordPoisonedEIPs(addrs, spec.DeploymentID, time.Now()); err != nil {
			progress(fmt.Sprintf("Wave 5.157: could not persist learned poison %v (non-fatal): %v", addrs, err))
		} else {
			progress(fmt.Sprintf("Wave 5.157 (#3722): recorded %d refused NAT EIP(s) to the learned blocklist", len(addrs)))
		}
		release(held)
	}
	return imports, nil
}

// reusableNATEIP returns the EIP of region code that this deployment already
// has: the module's own, or one an earlier attempt allocated that is not on
// the blocklist.
func reusableNATEIP(existing []rawPublicIP, prefix, code string, bl map[string]bool) (rawPublicIP, bool) {
	for _, e := range existing {
		if e.BandwidthName == prefix+"-"+code+"-nat-bw" {
			return e, true
		}
	}
	for _, e := range existing {
		if strings.HasPrefix(e.BandwidthName, prefix+"-"+code+"-nat-preflight-") && !bl[e.Address] {
			return e, true
		}
	}
	return rawPublicIP{}, false
}

// natMaxHeld bounds the refused EIPs the NAT pre-flight holds at once (the
// attempts of the former post-apply swap).
const natMaxHeld = 8

// defaultPublicIPQuota is the project's EIP quota when the quota API does not
// answer: kom4dc grants 10.
const defaultPublicIPQuota = 10

// publicIPQuota returns the project's EIP quota (GET quotas?type=publicIp, the
// call VPCQuota makes for VPCs), or defaultPublicIPQuota when it cannot be read.
func publicIPQuota(client *http.Client, hw hwCreds, region string) int {
	url := fmt.Sprintf("%s/v1/%s/quotas?type=publicIp", endpointFor("vpc", region), hw.ProjectID)
	body, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return defaultPublicIPQuota
	}
	var decoded struct {
		Quotas struct {
			Resources []struct {
				Type  string `json:"type"`
				Quota int    `json:"quota"`
			} `json:"resources"`
		} `json:"quotas"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return defaultPublicIPQuota
	}
	for _, r := range decoded.Quotas.Resources {
		if r.Type == "publicIp" && r.Quota > 0 {
			return r.Quota
		}
	}
	return defaultPublicIPQuota
}

// listPublicIPs lists the project's EIPs.
func listPublicIPs(client *http.Client, hw hwCreds, region string) ([]rawPublicIP, error) {
	url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
	body, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("list publicips: %w", err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("list publicips: status %d: %s", status, snippet(body, 240))
	}
	var decoded struct {
		PublicIPs []rawPublicIP `json:"publicips"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode publicips: %w", err)
	}
	return decoded.PublicIPs, nil
}

// allocateNATEIP allocates one EIP with the bandwidth settings of the module's
// NAT EIP and the given bandwidth name.
func allocateNATEIP(client *http.Client, hw hwCreds, region, name string) (rawPublicIP, error) {
	type publicIP struct {
		Type string `json:"type"`
	}
	type bandwidth struct {
		Name       string `json:"name"`
		Size       int    `json:"size"`
		ShareType  string `json:"share_type"`
		ChargeMode string `json:"charge_mode"`
	}
	req, err := json.Marshal(struct {
		PublicIP  publicIP  `json:"publicip"`
		Bandwidth bandwidth `json:"bandwidth"`
	}{publicIP{natEIPType}, bandwidth{name, natBandwidthSize, natBandwidthShare, natBandwidthCharged}})
	if err != nil {
		return rawPublicIP{}, err
	}
	url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodPost, url, req)
	if err != nil || status >= 400 {
		return rawPublicIP{}, fmt.Errorf("allocate NAT EIP %s: status %d err %v: %s", name, status, err, snippet(resp, 240))
	}
	var decoded struct {
		PublicIP rawPublicIP `json:"publicip"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return rawPublicIP{}, fmt.Errorf("decode the allocated NAT EIP %s: %w", name, err)
	}
	if decoded.PublicIP.ID == "" || decoded.PublicIP.Address == "" {
		return rawPublicIP{}, fmt.Errorf("allocate NAT EIP %s: the answer names no id or address: %s", name, snippet(resp, 240))
	}
	decoded.PublicIP.BandwidthName = name
	return decoded.PublicIP, nil
}
