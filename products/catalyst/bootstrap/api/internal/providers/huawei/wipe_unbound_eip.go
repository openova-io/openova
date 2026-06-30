// Wave 5.x (#4636) — UNBOUND / nameless EIP sweep for the canonical wipe.
//
// Root cause (found live wiping dep 8fd457a8 / omantel.biz on kom4dc,
// 2026-07-01): the canonical Wipe enumerates EIPs to purge BY NAME-PREFIX
// (listEIPsByNamePrefix, keyed on the SovereignFQDN bandwidth_name) and
// verifyZeroOrphans checks the same prefix-matched set. An UNBOUND, DOWN
// EIP with NO `catalyst-<stem>-` bandwidth name and NO port_id
// (e.g. 212.72.24.36) is invisible to both — so the wipe reports
// `verifiedZeroOrphans:true` while leaking the EIP. Orphan EIPs then
// accumulate per wipe → Huawei publicIp quota exhaustion → the same
// quota-leak class behind the #4614 production reap (#4454 janitor
// inversion fixed the runtime self-destruct; this fixes the wipe-side
// leak that feeds it).
//
// Fix: a project-wide `GET /v1/{proj}/publicips` sweep (the same raw list
// SweepOrphanEIPs / allocateCleanEIP already use) that flags every EIP
// which is NON-bastion AND UNBOUND (port_id == "") as an orphan. The wipe
// purge releases them (DELETE only — conservatively, never a BOUND EIP),
// and verifyZeroOrphans surfaces any survivor in
// ResidualOrphans["floating_ips"] so the existing G103 contract gate
// fails.
//
// Bastion protection (founder HARD RULE: bastion 212.72.24.20 is sacred):
// the bastion EIP is BOUND to the bastion-* ECS, so the unbound-only
// filter (port_id == "") already excludes it. As defence-in-depth we ALSO
// explicitly skip the known bastion IP and any EIP whose bandwidth_name
// carries the "bastion" substring, mirroring every other bastion guard in
// this package (provider.go SweepOrphanKeypairs / sweepVPCs / etc.).

package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// bastionEIPAddress is the operator-owned bastion EIP. It is BOUND to the
// bastion-* ECS (so it carries a port_id and is excluded by the
// unbound-only filter), but we hard-protect the literal address too as
// defence-in-depth — losing the bastion is catastrophic + irreversible
// (founder 2026-06-04: "you never need my approval for any resources you
// created in Huawei other than the bastion node").
const bastionEIPAddress = "212.72.24.20"

// rawPublicIP is the minimal projection of one entry in the
// `GET /v1/{proj}/publicips` listing, carrying exactly the fields the
// unbound-orphan decision needs.
type rawPublicIP struct {
	ID            string `json:"id"`
	Address       string `json:"public_ip_address"`
	Status        string `json:"status"`
	PortID        string `json:"port_id"`
	BandwidthName string `json:"bandwidth_name"`
}

// isProtectedBastionEIP reports whether an EIP must NEVER be touched
// because it is (or might be) the bastion. Three independent guards:
//   - the literal bastion address,
//   - a bandwidth_name carrying the "bastion" substring (mirrors the
//     name-substring bastion guard used everywhere else in this package),
//   - a BOUND EIP (port_id != "") — the bastion is bound, and we never
//     release a bound EIP regardless, so a bound EIP is always protected
//     by the unbound-only orphan rule below.
func isProtectedBastionEIP(e rawPublicIP) bool {
	if e.Address == bastionEIPAddress {
		return true
	}
	if strings.Contains(strings.ToLower(e.BandwidthName), "bastion") {
		return true
	}
	return false
}

// listUnboundOrphanEIPs lists EVERY publicIP in the project and returns
// the ones that are orphans: NON-bastion AND UNBOUND (port_id == "").
// This is the name-prefix-blind half of the EIP residual scan — it catches
// the nameless / DOWN / pool-recycled EIPs that listEIPsByNamePrefix
// cannot see (#4636).
//
// Conservative by construction: a BOUND EIP (port_id set) is NEVER
// returned, so an in-flight allocation that is momentarily attached, the
// bastion EIP, and any operator workload's bound EIP are all excluded. A
// transient DOWN+unbound window during another deployment's `tofu apply`
// is the only false-positive surface; the wipe only runs on an explicit
// per-deployment wipe whose own EIPs are already being torn down, so the
// blast radius is the project's genuine unbound leftovers.
func listUnboundOrphanEIPs(ctx context.Context, client *http.Client, hw hwCreds, region string) ([]hwEIP, error) {
	url := fmt.Sprintf("%s/v1/%s/publicips", endpointFor("vpc", region), hw.ProjectID)
	resp, status, err := doSignedRequest(client, hw, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		if status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("list publicips: status %d: %s", status, snippet(resp, 240))
	}
	var decoded struct {
		PublicIPs []rawPublicIP `json:"publicips"`
	}
	if err := json.Unmarshal(resp, &decoded); err != nil {
		return nil, fmt.Errorf("decode publicips: %w", err)
	}
	out := []hwEIP{}
	for _, e := range decoded.PublicIPs {
		if isProtectedBastionEIP(e) {
			continue
		}
		if e.PortID != "" {
			// BOUND — never release a bound EIP from the wipe path. If it
			// belongs to this deployment the name-prefix purge + detach
			// chain handles it; if it belongs to another workload it's
			// out of scope entirely.
			continue
		}
		out = append(out, hwEIP{ID: e.ID, Address: e.Address})
	}
	return out, nil
}
