// Package hetzner — orphan purge surface used by the wizard's Cancel & Wipe
// path (issue #318). When `tofu destroy` either fails partway or has no
// state to act against, the operator still needs a clean cloud account.
// This file enumerates and force-deletes every Hetzner resource tagged
// with the per-Sovereign label `catalyst.openova.io/sovereign=<fqdn>` so
// the next provisioning round starts from zero. The label key is shared
// with — and pinned by — the OpenTofu module at infra/hetzner/main.tf.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase 0): under
// normal operation `tofu destroy` is the canonical purge path. This file
// is the recovery fallback. It is therefore allowed to call Hetzner API
// directly — but only for orphan cleanup, never for new resource
// creation. Per the same principle, all NEW resource creation flows
// through OpenTofu.

package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PurgeReport summarises what the purge actually deleted. Returned to the
// wizard so the SSE log shows the operator a concrete tally of what was
// removed (or what was already gone).
type PurgeReport struct {
	Servers          []string `json:"servers"`
	LoadBalancers    []string `json:"load_balancers"`
	Networks         []string `json:"networks"`
	Firewalls        []string `json:"firewalls"`
	SSHKeys          []string `json:"ssh_keys"`
	S3Buckets        []string `json:"s3_buckets"`
	// Volumes — Hetzner Cloud Volumes. Frequently created post-handover
	// by the CSI driver (e.g. CNPG / Harbor / Loki / Mimir PVCs backed
	// by Hetzner Cloud Volume) and therefore NOT in tofu state. Purge
	// catches these via a label OR name-prefix sweep so the operator
	// doesn't have to scrub them manually after a Cancel & Wipe.
	Volumes []string `json:"volumes"`
	// PrimaryIPs — standalone primary IPs (Hetzner decoupled them from
	// servers in 2023). Auto-created by the CCM when a LoadBalancer
	// service requests an IP; left as orphans when the LB delete
	// raced ahead of the IP-detach.
	PrimaryIPs []string `json:"primary_ips"`
	// FloatingIPs — manually-allocated portable IPs. Rarely used in
	// Catalyst's stack but listed here for completeness so the next
	// re-provision starts with a clean slate.
	FloatingIPs      []string `json:"floating_ips"`
	FirewallsRetried int      `json:"firewalls_retried"`
	Errors           []string `json:"errors"`
}

// Total returns Report's deleted-resource fields summed for the SSE log.
func (r PurgeReport) Total() int {
	return len(r.Servers) + len(r.LoadBalancers) + len(r.Networks) +
		len(r.Firewalls) + len(r.SSHKeys) + len(r.S3Buckets) +
		len(r.Volumes) + len(r.PrimaryIPs) + len(r.FloatingIPs)
}

// firewallRetryAttempts caps the number of firewall-delete retries we run
// against Hetzner. The Cloud API returns 422 `resource_in_use` while a
// soft-deleted server is still detaching the firewall — server delete is
// async (returns 200 with action started) so the firewall stays attached
// for 5-30 seconds. 5 attempts at 6s/12s/24s/48s = ~90s of headroom which
// covers every observed detach in production. Issue #706.
const firewallRetryAttempts = 5

// firewallRetryInitialBackoff is the first sleep between firewall-delete
// retries. Exposed as a var so tests can shrink it without slowing CI.
var firewallRetryInitialBackoff = 6 * time.Second

// PurgeLabelKey is the canonical Hetzner-resource label that the OpenTofu
// module at infra/hetzner/main.tf stamps on every resource it creates
// (network, firewall, ssh-key, server, load-balancer). The value is the
// Sovereign's fully-qualified domain (e.g. "omantel.omani.works"). This
// constant exists so the filter is exercised by a regression test that
// pins both sides of the contract — if either Tofu's emission OR purge.go
// drifts, the test fails.
const PurgeLabelKey = "catalyst.openova.io/sovereign"

// FilterByLabel returns the Hetzner API `label_selector` query value for
// the given key/value pair. Exposed so the regression test can pin the
// exact wire format used against the Hetzner Cloud API.
func FilterByLabel(key, value string) string {
	return key + "=" + value
}

// validateSovereignFQDNForPurge guards Purge against the
// dash-converted-FQDN regression vector that left omantel.biz prov #9
// (otech133, 2026-05-10) with surviving Hetzner servers after wipe
// reported "tofuDestroyed:false". The bug class: a caller passes the
// workdir-style dash form (`omantel-biz`) into Purge instead of the
// FQDN dot form (`omantel.biz`). The Hetzner label_selector then
// queries `catalyst.openova.io/sovereign=omantel-biz` while the
// OpenTofu module at infra/hetzner/main.tf stamps
// `catalyst.openova.io/sovereign=omantel.biz` on every resource — so
// List returns 0 matches, the orphan-purge silently no-ops, and the
// next provision attempt collides with surviving infra.
//
// Every legitimate Sovereign FQDN contains at least one dot
// (`omantel.biz`, `acme.omani.works`, `tenant.openova.io`). A caller
// passing a dotless string is necessarily wrong — either the
// dash-converted workdir name leaked across the seam (provisioner.go's
// Request.sovereignName() form, or handler/wipe.go's
// deploymentSovereignName()), or the value was never normalised. Refuse
// loudly so the wipe handler surfaces the error in the SSE log instead
// of silently running a no-op orphan sweep that returns
// "tofuDestroyed:false; 0 resources purged" and hands a ghost cluster
// back to the operator. Fix #120 (Fix #117 secondary).
func validateSovereignFQDNForPurge(sovereignFQDN string) error {
	trimmed := strings.TrimSpace(sovereignFQDN)
	if trimmed == "" {
		return fmt.Errorf("sovereign fqdn is empty")
	}
	if !strings.Contains(trimmed, ".") {
		return fmt.Errorf("sovereign fqdn %q is not a fully-qualified domain (no dot) — "+
			"refusing to query Hetzner with a dash-converted workdir name; "+
			"caller must pass the FQDN form (e.g. \"omantel.biz\"), not the "+
			"workdir form (e.g. \"omantel-biz\"). See Fix #120, otech133 incident", trimmed)
	}
	return nil
}

// NamePrefixForSovereign returns the deterministic Hetzner-resource name
// prefix that the OpenTofu module at infra/hetzner/main.tf uses for every
// resource it provisions for the given Sovereign FQDN. Pattern:
//
//	catalyst-<fqdn-with-dots-replaced-by-dashes>
//
// e.g. `omantel.omani.works` → `catalyst-omantel-omani-works`. The Tofu
// module then suffixes per-resource shapes: `-net`, `-fw`, `-lb`,
// `-cp1` … `-w1` …, and the SSH key uses the bare prefix.
//
// Exposed so the name-prefix fallback in Purge() can scan unlabeled
// resources without re-deriving the string at every call site, and so
// the regression test can pin both halves of the contract from one place.
//
// Issue #732: the label-based sweep alone is not sufficient because
// Hetzner resources can land in production without their canonical
// label (partial `tofu apply`, out-of-band edits, fresh PVCs that lose
// tfstate). The name prefix is deterministic at module render time and
// survives every state-loss scenario.
func NamePrefixForSovereign(fqdn string) string {
	return "catalyst-" + strings.ReplaceAll(fqdn, ".", "-")
}

// Purge enumerates and deletes every Hetzner resource tagged with the
// canonical label `catalyst.openova.io/sovereign=<sovereignFQDN>`. The
// OpenTofu module at infra/hetzner/main.tf is responsible for setting
// this label on every resource it creates; we filter on it here.
//
// progress is called for each successful delete with a human-readable
// message ("deleted server otech-cp-1", "deleted lb otech-lb", …) so the
// wizard can stream the cleanup live. Pass nil to silence.
//
// The purge is best-effort. A failure to delete one resource does not
// abort the others; failures land in PurgeReport.Errors. The caller
// decides whether non-zero errors are fatal.
func Purge(ctx context.Context, token, sovereignFQDN string, progress func(msg string)) (PurgeReport, error) {
	report := PurgeReport{}
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(token) == "" {
		return report, fmt.Errorf("hetzner token is empty")
	}
	if err := validateSovereignFQDNForPurge(sovereignFQDN); err != nil {
		return report, err
	}

	labelSelector := FilterByLabel(PurgeLabelKey, sovereignFQDN)

	// Order matters: dependents first, then independents, so deletes succeed.
	//   1. Servers reference networks + firewalls + ssh-keys → delete first.
	//   2. Load balancers reference networks → delete second.
	//   3. Firewalls + networks + ssh-keys are independent → any order.
	servers, err := listResources(ctx, token, "/v1/servers", labelSelector, "servers")
	if err != nil {
		report.Errors = append(report.Errors, "list servers: "+err.Error())
	}
	for _, r := range servers {
		if err := deleteResource(ctx, token, "/v1/servers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete server %s: %s", r.Name, err.Error()))
			continue
		}
		report.Servers = append(report.Servers, r.Name)
		progress(fmt.Sprintf("deleted server %s", r.Name))
	}

	lbs, err := listResources(ctx, token, "/v1/load_balancers", labelSelector, "load_balancers")
	if err != nil {
		report.Errors = append(report.Errors, "list load_balancers: "+err.Error())
	}
	for _, r := range lbs {
		if err := deleteResource(ctx, token, "/v1/load_balancers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete lb %s: %s", r.Name, err.Error()))
			continue
		}
		report.LoadBalancers = append(report.LoadBalancers, r.Name)
		progress(fmt.Sprintf("deleted load balancer %s", r.Name))
	}

	firewalls, err := listResources(ctx, token, "/v1/firewalls", labelSelector, "firewalls")
	if err != nil {
		report.Errors = append(report.Errors, "list firewalls: "+err.Error())
	}
	for _, r := range firewalls {
		// Issue #706: server delete is async on Hetzner — the API returns
		// 200 "action started" but the firewall stays attached to the
		// soft-deleted server for several seconds, during which firewall
		// delete fails with 422 resource_in_use. Retry with exponential
		// backoff until 204 / 404 (already gone) or attempts exhausted.
		retried, err := deleteFirewallWithRetry(ctx, token, r.ID, progress, r.Name)
		if retried > 0 {
			report.FirewallsRetried += retried
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete firewall %s (id=%d): %s", r.Name, r.ID, err.Error()))
			continue
		}
		report.Firewalls = append(report.Firewalls, r.Name)
		progress(fmt.Sprintf("deleted firewall %s", r.Name))
	}

	networks, err := listResources(ctx, token, "/v1/networks", labelSelector, "networks")
	if err != nil {
		report.Errors = append(report.Errors, "list networks: "+err.Error())
	}
	for _, r := range networks {
		if err := deleteResource(ctx, token, "/v1/networks/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete network %s: %s", r.Name, err.Error()))
			continue
		}
		report.Networks = append(report.Networks, r.Name)
		progress(fmt.Sprintf("deleted network %s", r.Name))
	}

	sshkeys, err := listResources(ctx, token, "/v1/ssh_keys", labelSelector, "ssh_keys")
	if err != nil {
		report.Errors = append(report.Errors, "list ssh_keys: "+err.Error())
	}
	for _, r := range sshkeys {
		if err := deleteResource(ctx, token, "/v1/ssh_keys/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete ssh_key %s: %s", r.Name, err.Error()))
			continue
		}
		report.SSHKeys = append(report.SSHKeys, r.Name)
		progress(fmt.Sprintf("deleted ssh-key %s", r.Name))
	}

	// Volumes — Hetzner Cloud Volumes. Detached automatically when the
	// owning server is deleted (servers go first, above), so by the
	// time we get here volumes are unattached and DELETE succeeds.
	// Created either by tofu (rare; tofu module doesn't currently emit
	// Volumes) or by the Hetzner CSI driver post-handover (common —
	// every CNPG/Harbor/Loki/Mimir StatefulSet backed by RWO storage
	// allocates one). The CSI driver labels its volumes with
	// `csi.hetzner.cloud/csi-driver-name=...` plus the per-cluster
	// label; if our canonical label was applied via Crossplane's
	// composition (preferred path), this label sweep catches them.
	// Otherwise the name-prefix pass below picks up only those whose
	// names start with `catalyst-<fqdn-dashes>-`. Volumes named by the
	// CSI driver (PVC-uid form `pvc-xxx`) are NOT caught by either pass
	// — operator must clean those manually OR the Crossplane composition
	// must be extended to label them. Tracked separately.
	volumes, err := listResources(ctx, token, "/v1/volumes", labelSelector, "volumes")
	if err != nil {
		report.Errors = append(report.Errors, "list volumes: "+err.Error())
	}
	for _, r := range volumes {
		if err := deleteResource(ctx, token, "/v1/volumes/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete volume %s: %s", r.Name, err.Error()))
			continue
		}
		report.Volumes = append(report.Volumes, r.Name)
		progress(fmt.Sprintf("deleted volume %s", r.Name))
	}

	// Primary IPs — standalone since Hetzner's 2023 IP-decoupling. The
	// CCM creates these for LoadBalancer services and tags them with
	// our canonical label via the Crossplane composition. With LBs
	// deleted above, primary IPs are unassigned and DELETE succeeds.
	primaryIPs, err := listResources(ctx, token, "/v1/primary_ips", labelSelector, "primary_ips")
	if err != nil {
		report.Errors = append(report.Errors, "list primary_ips: "+err.Error())
	}
	for _, r := range primaryIPs {
		if err := deleteResource(ctx, token, "/v1/primary_ips/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete primary_ip %s: %s", r.Name, err.Error()))
			continue
		}
		report.PrimaryIPs = append(report.PrimaryIPs, r.Name)
		progress(fmt.Sprintf("deleted primary_ip %s", r.Name))
	}

	// Floating IPs — older portable-IP API; rarely used in Catalyst
	// today but caught here so a stack that uses them doesn't leak.
	floatingIPs, err := listResources(ctx, token, "/v1/floating_ips", labelSelector, "floating_ips")
	if err != nil {
		report.Errors = append(report.Errors, "list floating_ips: "+err.Error())
	}
	for _, r := range floatingIPs {
		if err := deleteResource(ctx, token, "/v1/floating_ips/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete floating_ip %s: %s", r.Name, err.Error()))
			continue
		}
		report.FloatingIPs = append(report.FloatingIPs, r.Name)
		progress(fmt.Sprintf("deleted floating_ip %s", r.Name))
	}

	// Second pass — name-prefix fallback (issue #732).
	//
	// The label-based sweep above is the canonical path. But it depends on
	// every Hetzner resource carrying the
	// `catalyst.openova.io/sovereign=<fqdn>` label that the OpenTofu module
	// stamps at create time. In production we observed (otech83, 2026-05-04)
	// the wipe ran cleanly but left LB / network / firewall / SSH-key
	// behind because:
	//
	//   - tofu state is lost when the catalyst-api Pod's PVC is recreated
	//     (PR #715 narrows but does not eliminate this)
	//   - a partial `tofu apply` can create the resource without setting
	//     the label block (e.g. cancelled mid-create)
	//   - operators may edit a resource via Hetzner Console and strip the
	//     label by accident
	//
	// The Tofu module names every resource off the deterministic prefix
	// `catalyst-<fqdn-with-dashes>` (see NamePrefixForSovereign). That
	// prefix survives every state-loss path because it is render-time
	// fixed, not API-roundtrip-derived.
	//
	// This pass lists every resource without a label filter, then deletes
	// any whose name starts with the canonical prefix. Order matches the
	// label pass (servers → LBs → firewalls → networks → SSH keys) so
	// dependents go first.
	//
	// We dedupe against the label pass (a resource that was already deleted
	// up there returns 404 here, which deleteResource treats as success
	// and which we skip from the report so the totals don't double-count).
	prefix := NamePrefixForSovereign(sovereignFQDN)
	purgeByNamePrefix(ctx, token, prefix, &report, progress)

	// Second name-prefix pass for CCM-allocated resources that don't carry
	// the "catalyst-" prefix. The Cilium chart's clustermesh-apiserver
	// Service overlay (clusters/_template/bootstrap-kit/01-cilium.yaml)
	// names its CCM-allocated LB as
	// `${SOVEREIGN_FQDN_SLUG}-${SOVEREIGN_REGION_KEY}-clustermesh`
	// (e.g. `t126-omani-works-hel1-clustermesh`) — no "catalyst-" stem.
	// The first prefix pass therefore misses these LBs and they survive
	// `tofu destroy` because tofu doesn't manage them either. Caught
	// repeatedly: t124, t125 wipes both left 3 orphan clustermesh LBs
	// per Sovereign that had to be cleaned via direct Hetzner API DELETE.
	fqdnSlugPrefix := strings.ReplaceAll(sovereignFQDN, ".", "-") + "-"
	if fqdnSlugPrefix != prefix && fqdnSlugPrefix != "-" {
		purgeByNamePrefix(ctx, token, fqdnSlugPrefix, &report, progress)
	}

	// Third pass — SSH-key public_key comment sweep (TBD-A16).
	//
	// The label-pass and name-prefix-pass above both rely on the Hetzner
	// SSH key object's own metadata (label and name) matching the Tofu
	// module's canonical emission. In production we observed (t24 orphan,
	// 2026-05-18) SSH keys that survived every previous wipe pass because
	// either:
	//
	//   - the operator renamed the key in the Hetzner Console (name drift)
	//   - a partial `tofu apply` race-condition left the key in a state
	//     where the label was never applied
	//   - the SSH key was created OUTSIDE tofu by a manual `hcloud
	//     ssh-key create` call during incident response and never
	//     received the canonical name/label
	//
	// The one piece of metadata that resists every drift vector is the
	// `public_key` field's RFC 4716 *comment*: the trailing whitespace-
	// delimited token after the key data, e.g. `catalyst-t25-fresh@bastion`.
	// The Catalyst bootstrap-cli stamps this comment at key generation
	// time so even orphans created out-of-band carry the stem. This pass
	// lists every SSH key, parses the comment, and deletes any whose
	// comment begins with the canonical `catalyst-<fqdn-dashes>` prefix.
	//
	// Idempotent against earlier passes via the same `already` set.
	purgeSSHKeysByPublicKeyComment(ctx, token, prefix, &report, progress)

	return report, nil
}

// purgeSSHKeysByPublicKeyComment deletes SSH keys whose `public_key` comment
// begins with the canonical per-Sovereign prefix (TBD-A16). This is the
// third match vector — after label-selector and name-prefix — and catches
// keys whose name AND label both drifted from the Tofu module's canonical
// emission. Idempotent against the earlier passes.
//
// The "comment" of an OpenSSH-format public key is the trailing whitespace-
// delimited token after the key data, e.g. given:
//
//	ssh-ed25519 AAAAC3NzaC1l... catalyst-t25-fresh@bastion-vmi3305700
//
// the comment is `catalyst-t25-fresh@bastion-vmi3305700`. Keys without a
// comment field (or whose key data doesn't tokenise to 3 parts) are skipped
// — there's no way to attribute them to a Sovereign without a label or name
// match, and we don't want this fallback to delete random unrelated keys.
//
// Boundary safety: the prefix MUST match at the start of the comment AND
// be followed by either end-of-string or a non-alphanumeric separator
// (`-`, `.`, `@`, ` `, …). Without this guard, wiping `catalyst-t2` would
// match `catalyst-t20-*` comments — the same P0 safety regression class
// pinned by TestPurge_NamePrefixFallback_DoesNotTouchOtherCustomers.
func purgeSSHKeysByPublicKeyComment(ctx context.Context, token, prefix string, report *PurgeReport, progress func(string)) {
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(prefix) == "" {
		return
	}

	already := sliceToSet(report.SSHKeys)

	keys, err := listAllSSHKeysWithPublicKey(ctx, token)
	if err != nil {
		report.Errors = append(report.Errors, "public-key-comment list ssh_keys: "+err.Error())
		return
	}
	for _, k := range keys {
		comment := publicKeyComment(k.PublicKey)
		if !commentMatchesPrefix(comment, prefix) {
			continue
		}
		if _, seen := already[k.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/ssh_keys/"+strconv.FormatInt(k.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete ssh_key %s (public-key-comment): %s", k.Name, err.Error()))
			continue
		}
		report.SSHKeys = append(report.SSHKeys, k.Name)
		progress(fmt.Sprintf("deleted ssh-key %s (public-key-comment fallback)", k.Name))
	}
}

// publicKeyComment extracts the RFC-4716 / OpenSSH-format comment field
// from a public key string. The OpenSSH format is:
//
//	<type> <base64-data> <comment>
//
// where the comment may itself contain whitespace (e.g. `user@host with
// spaces`). We split off the key type, then the key data, and return
// everything after as the comment. Returns "" when the key has fewer than
// 3 whitespace-separated fields.
func publicKeyComment(publicKey string) string {
	trimmed := strings.TrimSpace(publicKey)
	if trimmed == "" {
		return ""
	}
	// First whitespace split: drop the key type (`ssh-ed25519`, `ssh-rsa`, …).
	typeAndRest := strings.SplitN(trimmed, " ", 2)
	if len(typeAndRest) < 2 {
		return ""
	}
	rest := strings.TrimSpace(typeAndRest[1])
	// Second whitespace split: separate base64 key data from comment.
	dataAndComment := strings.SplitN(rest, " ", 2)
	if len(dataAndComment) < 2 {
		return ""
	}
	return strings.TrimSpace(dataAndComment[1])
}

// commentMatchesPrefix returns true iff the comment starts with prefix AND
// the next rune (if any) is a non-alphanumeric separator. This is the
// boundary guard that prevents `catalyst-t2` from spuriously matching
// `catalyst-t20-fresh@host`. Symmetric with the HasPrefix-with-suffix-dash
// contract pinned by TestPurge_NamePrefixFallback_DoesNotTouchOtherCustomers.
func commentMatchesPrefix(comment, prefix string) bool {
	if comment == "" || prefix == "" {
		return false
	}
	if !strings.HasPrefix(comment, prefix) {
		return false
	}
	if len(comment) == len(prefix) {
		return true
	}
	next := comment[len(prefix)]
	// Allow `-`, `.`, `@`, ` `, `_` as boundary separators. A digit or
	// letter after the prefix means we're matching a longer stem (e.g.
	// `catalyst-t20-…` when prefix is `catalyst-t2`) and must be rejected.
	switch next {
	case '-', '.', '@', ' ', '_':
		return true
	}
	return false
}

// hetznerSSHKey is the SSH-key shape extending hetznerResource with the
// `public_key` field needed by the comment-match sweep. Hetzner returns
// the full OpenSSH-format key string in this field.
type hetznerSSHKey struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// listAllSSHKeysWithPublicKey enumerates every SSH key in the Hetzner
// project with the `public_key` field populated. Used by the public-key
// comment-match sweep — listAllResources / listResources drop `public_key`
// because their shape is the minimal hetznerResource.
func listAllSSHKeysWithPublicKey(ctx context.Context, token string) ([]hetznerSSHKey, error) {
	var out []hetznerSSHKey
	page := 1
	for {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "50")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.hetzner.cloud/v1/ssh_keys?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := purgeHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		body := decodeBody(resp)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("hetzner auth failed (status %d) — token may have been rotated or scope revoked", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("hetzner list-all ssh_keys: unexpected status %d: %s", resp.StatusCode, body.errMsg())
		}

		raw, ok := body.raw["ssh_keys"]
		if ok {
			var entries []hetznerSSHKey
			if err := json.Unmarshal(raw, &entries); err == nil {
				out = append(out, entries...)
			}
		}
		if !body.hasNextPage() {
			break
		}
		page++
		if page > 50 {
			break // sanity bound
		}
	}
	return out, nil
}

// purgeByNamePrefix is the unlabeled fallback half of Purge. Lists every
// resource (no selector) and deletes any whose name begins with the
// deterministic per-Sovereign prefix, e.g. `catalyst-otech83-omani-works`.
// Idempotent against the label-pass (404 = already gone = success).
//
// Resources already counted in report (by name) are NOT re-counted, so
// the totals reflect actual unique deletions across both passes.
func purgeByNamePrefix(ctx context.Context, token, prefix string, report *PurgeReport, progress func(string)) {
	if progress == nil {
		progress = func(string) {}
	}

	already := make(map[string]map[string]struct{})
	already["servers"] = sliceToSet(report.Servers)
	already["load_balancers"] = sliceToSet(report.LoadBalancers)
	already["firewalls"] = sliceToSet(report.Firewalls)
	already["networks"] = sliceToSet(report.Networks)
	already["ssh_keys"] = sliceToSet(report.SSHKeys)
	already["volumes"] = sliceToSet(report.Volumes)
	already["primary_ips"] = sliceToSet(report.PrimaryIPs)
	already["floating_ips"] = sliceToSet(report.FloatingIPs)

	// Servers — delete first so LBs / firewalls / networks they reference
	// can be cleaned up after.
	servers, err := listAllResources(ctx, token, "/v1/servers", "servers")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list servers: "+err.Error())
	}
	for _, r := range servers {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["servers"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/servers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete server %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.Servers = append(report.Servers, r.Name)
		progress(fmt.Sprintf("deleted server %s (name-prefix fallback)", r.Name))
	}

	// Load balancers — typically reference the network. Hetzner LB delete
	// is synchronous; no retry loop needed beyond the standard 4xx surface.
	lbs, err := listAllResources(ctx, token, "/v1/load_balancers", "load_balancers")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list load_balancers: "+err.Error())
	}
	for _, r := range lbs {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["load_balancers"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/load_balancers/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete lb %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.LoadBalancers = append(report.LoadBalancers, r.Name)
		progress(fmt.Sprintf("deleted load balancer %s (name-prefix fallback)", r.Name))
	}

	// Firewalls — same async-detach problem as the labelled path; reuse the
	// retry helper so the unlabeled fallback survives the same Hetzner
	// 422 resource_in_use window.
	firewalls, err := listAllResources(ctx, token, "/v1/firewalls", "firewalls")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list firewalls: "+err.Error())
	}
	for _, r := range firewalls {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["firewalls"][r.Name]; seen {
			continue
		}
		retried, err := deleteFirewallWithRetry(ctx, token, r.ID, progress, r.Name)
		if retried > 0 {
			report.FirewallsRetried += retried
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete firewall %s (id=%d, name-prefix): %s", r.Name, r.ID, err.Error()))
			continue
		}
		report.Firewalls = append(report.Firewalls, r.Name)
		progress(fmt.Sprintf("deleted firewall %s (name-prefix fallback)", r.Name))
	}

	// Networks — must be after LB + servers detach (Hetzner returns 409
	// "still in use" otherwise). The label pass + servers/LBs above handle
	// the dependency ordering.
	networks, err := listAllResources(ctx, token, "/v1/networks", "networks")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list networks: "+err.Error())
	}
	for _, r := range networks {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["networks"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/networks/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete network %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.Networks = append(report.Networks, r.Name)
		progress(fmt.Sprintf("deleted network %s (name-prefix fallback)", r.Name))
	}

	// SSH keys — independent; any order works.
	sshkeys, err := listAllResources(ctx, token, "/v1/ssh_keys", "ssh_keys")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list ssh_keys: "+err.Error())
	}
	for _, r := range sshkeys {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["ssh_keys"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/ssh_keys/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete ssh_key %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.SSHKeys = append(report.SSHKeys, r.Name)
		progress(fmt.Sprintf("deleted ssh-key %s (name-prefix fallback)", r.Name))
	}

	// Volumes (name-prefix fallback). After all servers above are gone,
	// volumes are unattached and DELETE succeeds. Volumes named via the
	// CSI driver's PVC-uid scheme (`pvc-xxx`) won't match the prefix —
	// those need the canonical label which the Crossplane composition
	// applies (or, when the composition lags, manual operator cleanup).
	volumes, err := listAllResources(ctx, token, "/v1/volumes", "volumes")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list volumes: "+err.Error())
	}
	for _, r := range volumes {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["volumes"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/volumes/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete volume %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.Volumes = append(report.Volumes, r.Name)
		progress(fmt.Sprintf("deleted volume %s (name-prefix fallback)", r.Name))
	}

	// Primary IPs (name-prefix fallback).
	primaryIPs, err := listAllResources(ctx, token, "/v1/primary_ips", "primary_ips")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list primary_ips: "+err.Error())
	}
	for _, r := range primaryIPs {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["primary_ips"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/primary_ips/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete primary_ip %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.PrimaryIPs = append(report.PrimaryIPs, r.Name)
		progress(fmt.Sprintf("deleted primary_ip %s (name-prefix fallback)", r.Name))
	}

	// Floating IPs (name-prefix fallback).
	floatingIPs, err := listAllResources(ctx, token, "/v1/floating_ips", "floating_ips")
	if err != nil {
		report.Errors = append(report.Errors, "name-prefix list floating_ips: "+err.Error())
	}
	for _, r := range floatingIPs {
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		if _, seen := already["floating_ips"][r.Name]; seen {
			continue
		}
		if err := deleteResource(ctx, token, "/v1/floating_ips/"+strconv.FormatInt(r.ID, 10)); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("delete floating_ip %s (name-prefix): %s", r.Name, err.Error()))
			continue
		}
		report.FloatingIPs = append(report.FloatingIPs, r.Name)
		progress(fmt.Sprintf("deleted floating_ip %s (name-prefix fallback)", r.Name))
	}
}

// sliceToSet returns a set view of a string slice. Used by purgeByNamePrefix
// to skip resources the labelled pass already deleted.
func sliceToSet(in []string) map[string]struct{} {
	s := make(map[string]struct{}, len(in))
	for _, v := range in {
		s[v] = struct{}{}
	}
	return s
}

// listAllResources is the no-selector variant of listResources used by the
// name-prefix fallback. Pages until exhausted (Hetzner caps at 50/page,
// 50 pages = up to 2500 resources, well above any realistic per-account
// surface). Errors are surfaced to the caller for inclusion in
// PurgeReport.Errors so an outage on one kind doesn't silently skip
// the others.
func listAllResources(ctx context.Context, token, path, listKey string) ([]hetznerResource, error) {
	var out []hetznerResource
	page := 1
	for {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "50")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.hetzner.cloud"+path+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := purgeHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		body := decodeBody(resp)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("hetzner auth failed (status %d) — token may have been rotated or scope revoked", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("hetzner list-all %s: unexpected status %d: %s", path, resp.StatusCode, body.errMsg())
		}

		entries, _ := body.list(listKey)
		out = append(out, entries...)
		if !body.hasNextPage() {
			break
		}
		page++
		if page > 50 {
			break // sanity bound
		}
	}
	return out, nil
}

// hetznerResource is the minimum shape we need from each Hetzner list
// response to drive the delete loop.
type hetznerResource struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// listResources GETs /v1/<resource> with the label selector and returns
// every entry. Hetzner pages at 25 per page by default; we follow the
// `next` link until exhausted.
func listResources(ctx context.Context, token, path, labelSelector, listKey string) ([]hetznerResource, error) {
	var out []hetznerResource
	page := 1
	for {
		q := url.Values{}
		q.Set("label_selector", labelSelector)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "50")

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.hetzner.cloud"+path+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := purgeHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		body := decodeBody(resp)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("hetzner auth failed (status %d) — token may have been rotated or scope revoked", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("hetzner list %s: unexpected status %d: %s", path, resp.StatusCode, body.errMsg())
		}

		entries, _ := body.list(listKey)
		out = append(out, entries...)
		if !body.hasNextPage() {
			break
		}
		page++
		if page > 50 {
			break // sanity bound
		}
	}
	return out, nil
}

// deleteResource issues DELETE on the given path. Hetzner returns 200/204
// on success, 404 when already gone (treated as success), 423/429 when
// retryable. We treat 404 as success and surface every other non-2xx as
// an error the caller appends to PurgeReport.Errors.
func deleteResource(ctx context.Context, token, path string) error {
	code, err := deleteResourceStatus(ctx, token, path)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("status %d", code)
	}
}

// deleteResourceStatus is the lower-level form of deleteResource that
// returns the raw HTTP status code so callers can distinguish retryable
// 422 (resource_in_use, used by Hetzner during async server detach) from
// terminal errors. Issue #706 — firewall delete needs this signal to
// drive an exponential-backoff retry while the server delete is still
// in flight.
func deleteResourceStatus(ctx context.Context, token, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		"https://api.hetzner.cloud"+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := purgeHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// deleteFirewallWithRetry deletes a firewall by id, retrying on 422
// resource_in_use with exponential backoff (6s, 12s, 24s, 48s — capped
// at firewallRetryAttempts). Returns the number of retries performed
// (i.e. attempts beyond the first) and a non-nil error only if every
// attempt returned 422 or a non-recoverable HTTP error surfaced.
//
// Hetzner's server delete is asynchronous: the API responds 200 "action
// started" while the soft-deleted server still references the firewall,
// causing firewall delete to 422 for the next 5-30 seconds. Without
// this retry the wipe handler reports "0 firewalls deleted" while the
// firewall remains live, costing money and leaking security boundary
// — verified on otech50 2026-05-03, issue #706.
func deleteFirewallWithRetry(ctx context.Context, token string, id int64, progress func(string), name string) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	path := "/v1/firewalls/" + strconv.FormatInt(id, 10)
	backoff := firewallRetryInitialBackoff
	retries := 0
	var lastCode int
	for attempt := 1; attempt <= firewallRetryAttempts; attempt++ {
		code, err := deleteResourceStatus(ctx, token, path)
		if err != nil {
			// Network error — surface immediately (no point retrying a
			// torn TCP connection in a tight loop; the upstream wipe
			// handler can rerun the whole purge).
			return retries, err
		}
		lastCode = code
		switch code {
		case http.StatusOK, http.StatusNoContent, http.StatusAccepted, http.StatusNotFound:
			return retries, nil
		case http.StatusUnprocessableEntity:
			// resource_in_use — the server delete is still in flight.
			// Sleep and retry with exponential backoff.
			if attempt == firewallRetryAttempts {
				return retries, fmt.Errorf("status 422 resource_in_use after %d attempts (server detach did not complete in ~%s)", firewallRetryAttempts, totalBackoffWindow())
			}
			progress(fmt.Sprintf("firewall %s still in use (attempt %d/%d) — backing off %s", name, attempt, firewallRetryAttempts, backoff))
			retries++
			select {
			case <-ctx.Done():
				return retries, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		default:
			return retries, fmt.Errorf("status %d", code)
		}
	}
	return retries, fmt.Errorf("status %d after %d attempts", lastCode, firewallRetryAttempts)
}

// totalBackoffWindow returns a human-readable summary of the maximum time
// the firewall retry loop sleeps. Used in error messages so operators
// can size patience accordingly without re-reading the code.
func totalBackoffWindow() time.Duration {
	total := time.Duration(0)
	b := firewallRetryInitialBackoff
	for i := 1; i < firewallRetryAttempts; i++ {
		total += b
		b *= 2
	}
	return total
}

// purgeHTTPClient is separate from the package-level httpClient in
// client.go because purge operations may legitimately take longer than
// the 10s ValidateToken bound (Hetzner async server-delete jobs can
// queue under load).
var purgeHTTPClient = &http.Client{Timeout: 60 * time.Second}

// hetznerListBody is a thin facade over the JSON body so the list+next
// logic stays readable in one place.
type hetznerListBody struct {
	raw map[string]json.RawMessage
}

func decodeBody(resp *http.Response) hetznerListBody {
	body := hetznerListBody{raw: map[string]json.RawMessage{}}
	_ = json.NewDecoder(resp.Body).Decode(&body.raw)
	return body
}

func (b hetznerListBody) list(key string) ([]hetznerResource, error) {
	raw, ok := b.raw[key]
	if !ok {
		return nil, nil
	}
	var entries []hetznerResource
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (b hetznerListBody) hasNextPage() bool {
	raw, ok := b.raw["meta"]
	if !ok {
		return false
	}
	var meta struct {
		Pagination struct {
			NextPage *int `json:"next_page"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	return meta.Pagination.NextPage != nil
}

func (b hetznerListBody) errMsg() string {
	raw, ok := b.raw["error"]
	if !ok {
		return ""
	}
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// ── G103 (Refs #2670) — Hetzner port of the post-wipe zero-orphan gate ──

// VerifyReport is the per-kind residual list returned by VerifyZeroOrphans.
// A non-zero Total() means the wipe contract was violated: catalyst-*
// resources survived the cascade purge and the next prov attempt may hit
// quota / name-collision failures on the same Hetzner project.
//
// Mirrors providers.WipeResult.ResidualOrphans shape so the catalyst-api
// provider adapter can copy keys 1:1.
type VerifyReport struct {
	Servers       []string
	LoadBalancers []string
	Networks      []string
	Firewalls     []string
	SSHKeys       []string
	Volumes       []string
	PrimaryIPs    []string
	FloatingIPs   []string
	Errors        []string
}

// Total returns the count of surviving catalyst-* resources across kinds.
// Zero = wipe contract honoured; non-zero = real residual.
func (v VerifyReport) Total() int {
	return len(v.Servers) + len(v.LoadBalancers) + len(v.Networks) +
		len(v.Firewalls) + len(v.SSHKeys) + len(v.Volumes) +
		len(v.PrimaryIPs) + len(v.FloatingIPs)
}

// AsMap returns the per-kind survivor names keyed by canonical resource
// kind ("servers" / "load_balancers" / "networks" / "firewalls" /
// "ssh_keys" / "volumes" / "primary_ips" / "floating_ips") — the same
// vocab providers.WipeResult.ResidualOrphans uses. Empty kinds are
// omitted so consumers can range over a known-non-empty map.
func (v VerifyReport) AsMap() map[string][]string {
	out := map[string][]string{}
	if len(v.Servers) > 0 {
		out["servers"] = v.Servers
	}
	if len(v.LoadBalancers) > 0 {
		out["load_balancers"] = v.LoadBalancers
	}
	if len(v.Networks) > 0 {
		out["networks"] = v.Networks
	}
	if len(v.Firewalls) > 0 {
		out["firewalls"] = v.Firewalls
	}
	if len(v.SSHKeys) > 0 {
		out["ssh_keys"] = v.SSHKeys
	}
	if len(v.Volumes) > 0 {
		out["volumes"] = v.Volumes
	}
	if len(v.PrimaryIPs) > 0 {
		out["primary_ips"] = v.PrimaryIPs
	}
	if len(v.FloatingIPs) > 0 {
		out["floating_ips"] = v.FloatingIPs
	}
	return out
}

// VerifyZeroOrphans walks every Hetzner Cloud resource kind that Purge
// targets and records any entry that still carries the Sovereign label
// `catalyst.openova.io/sovereign=<fqdn>`. Resources with names starting
// `bastion` are hard-excluded so the canonical break-glass infrastructure
// (bastion-IP, mothership bastion SSH key, etc. — same protection rule as
// the HCS port per memory feedback_hcs_kom4dc_wipe_cascade_quirks) is
// never flagged as a residual.
//
// Returns ok=true + an empty VerifyReport iff the post-wipe scan found
// zero survivors. ok=false + a populated report means the wipe contract
// was violated; the provider adapter copies the AsMap() output into
// providers.WipeResult.ResidualOrphans + leaves VerifiedZeroOrphans=false.
//
// G103 (Refs #2670) Hetzner-side parity with the existing HCS gate so
// the G104 (Refs #2671) 3xZT certification script can certify either
// provider via the same single signal.
func VerifyZeroOrphans(ctx context.Context, token, sovereignFQDN string, progress func(msg string)) (VerifyReport, bool) {
	report := VerifyReport{}
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(token) == "" {
		report.Errors = append(report.Errors, "hetzner token is empty")
		return report, false
	}
	if err := validateSovereignFQDNForPurge(sovereignFQDN); err != nil {
		report.Errors = append(report.Errors, err.Error())
		return report, false
	}

	progress("verifying zero orphans (G103 #2670 post-wipe gate)")

	labelSelector := FilterByLabel(PurgeLabelKey, sovereignFQDN)

	type kind struct {
		path    string
		listKey string
		into    *[]string
	}
	kinds := []kind{
		{"/v1/servers", "servers", &report.Servers},
		{"/v1/load_balancers", "load_balancers", &report.LoadBalancers},
		{"/v1/networks", "networks", &report.Networks},
		{"/v1/firewalls", "firewalls", &report.Firewalls},
		{"/v1/ssh_keys", "ssh_keys", &report.SSHKeys},
		{"/v1/volumes", "volumes", &report.Volumes},
		{"/v1/primary_ips", "primary_ips", &report.PrimaryIPs},
		{"/v1/floating_ips", "floating_ips", &report.FloatingIPs},
	}
	for _, k := range kinds {
		survivors, err := listResources(ctx, token, k.path, labelSelector, k.listKey)
		if err != nil {
			report.Errors = append(report.Errors, "verify list "+k.listKey+": "+err.Error())
			continue
		}
		for _, r := range survivors {
			if strings.HasPrefix(r.Name, "bastion") || strings.Contains(r.Name, "bastion") {
				continue
			}
			*k.into = append(*k.into, r.Name)
		}
	}

	if report.Total() == 0 {
		progress("verified zero orphans — wipe contract honoured")
		return report, true
	}
	progress(fmt.Sprintf("RESIDUAL ORPHANS: %d catalyst-* resource(s) survived wipe (bastion-* excluded)", report.Total()))
	return report, false
}
