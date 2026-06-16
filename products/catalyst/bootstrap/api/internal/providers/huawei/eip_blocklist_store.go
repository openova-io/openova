// Wave 5.157 (#3722) — persistent, auto-growing, TTL-bounded learned
// blocklist for poisoned NAT EIPs. This is the zero-touch replacement
// for the manual `CATALYST_HUAWEI_NAT_EIP_ROTATE_ALL=true` +
// `flux suspend catalyst-platform` dance.
//
// THE LEAK THIS CLOSES
// ====================
// RotateBlocklistedNATEIPs (preflight_eip.go) drains the Huawei
// free-pool DURING one prov: allocateCleanEIP HOLDs poisoned EIPs so a
// clean one is returned, then RELEASES every held EIP + the old SNAT
// EIP at the end (deleteEIP). Releasing a poisoned EIP returns it to the
// HCS free-pool — but it stays reputation-blocklisted / un-routable to
// harbor.openova.io (45.151.123.50) "for hours". The very NEXT prov's
// fresh allocate() can draw that same just-released poison. The static
// seed blocklist only knows 212.72.24.48 / .14, so a freshly-poisoned
// address (.8, .59, .43, .29, .46, …) is NOT recognised → the default
// self-heal misses it → the fresh prov wedges (0 HelmReleases, kubeconfig
// never PUT, no egress to harbor). hw151–154 shape.
//
// THE FIX
// =======
// Every EIP RotateBlocklistedNATEIPs rotates away from (the old SNAT EIP
// + every held reject) is RECORDED to a JSON store on the
// catalyst-api-deployments PVC before it is released. blocklist() then
// merges seed ∪ env ∪ this store, so prov N's poison becomes prov N+1's
// auto-avoid set — with NO operator env var and NO flux suspend. The
// behaviour lives in the binary + the PVC, so a Flux/chart reconcile
// can't revert it the way it reverted the manual env (~5 min window).
//
// RECOVERY (TTL)
// ==============
// HCS eventually un-poisons a released EIP (reputation lists age out).
// To avoid permanently exiling an address that became clean again, each
// record carries a last-seen-poisoned timestamp and entries older than
// the TTL (default 24h, override CATALYST_HUAWEI_NAT_EIP_TTL) are pruned
// on load. So a poison that recurs keeps getting re-recorded (its
// timestamp refreshes); one that genuinely recovered ages out and
// re-enters the free rotation.
//
// STATE PATH
// ==========
// Default /var/lib/catalyst/tofu/nat-eip-blocklist.json — a SIBLING of
// the per-deployment tofu workdirs (CATALYST_TOFU_WORKDIR), NOT inside
// one. Wipe (provisioner.Destroy) RemoveAll's only the per-deployment
// subdir, so this learned state SURVIVES a wipe — which is the whole
// point, since wipes are what poison the pool. Override with
// CATALYST_HUAWEI_NAT_EIP_STATE. An empty/unwritable path degrades
// gracefully to in-memory-only (seed+env) behaviour — never fatal.

package huawei

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// eipBlocklistFileEnv overrides the on-PVC store path.
const eipBlocklistFileEnv = "CATALYST_HUAWEI_NAT_EIP_STATE"

// eipBlocklistTTLEnv overrides the recovery TTL (Go duration, e.g. "24h",
// "48h", "90m"). Entries last-seen-poisoned longer ago than this age out
// of the learned blocklist on load.
const eipBlocklistTTLEnv = "CATALYST_HUAWEI_NAT_EIP_TTL"

// defaultEIPBlocklistTTL is how long a learned-poison record is honoured
// before it ages out so a recovered EIP can re-enter rotation.
const defaultEIPBlocklistTTL = 24 * time.Hour

// defaultEIPBlocklistPath is the fallback store location. Sibling of the
// per-deployment tofu workdirs so it survives a wipe.
const defaultEIPBlocklistPath = "/var/lib/catalyst/tofu/nat-eip-blocklist.json"

// eipBlocklistStoreMu serialises concurrent provs (a 2-region prov calls
// the preflight once; but two deployments can run back-to-back / the
// mothership is single-replica yet goroutine-concurrent). Read-modify-
// write of the on-disk file must not interleave.
var eipBlocklistStoreMu sync.Mutex

// eipBlocklistRecord is one learned-poison entry. LastSeen is unix
// seconds; it refreshes every time the EIP is re-recorded so a recurring
// poison never ages out, while a one-off does.
type eipBlocklistRecord struct {
	IP       string `json:"ip"`
	LastSeen int64  `json:"lastSeenPoisonedUnix"`
	// Note is a human breadcrumb (deployment id that last saw it
	// poisoned) — purely diagnostic, never load-bearing.
	Note string `json:"note,omitempty"`
}

// eipBlocklistFile is the on-disk schema. Versioned so a future format
// change can migrate rather than choke.
type eipBlocklistFile struct {
	Version int                  `json:"version"`
	Records []eipBlocklistRecord `json:"records"`
}

// eipBlocklistPath resolves the store path from env or the default.
func eipBlocklistPath() string {
	if p := strings.TrimSpace(os.Getenv(eipBlocklistFileEnv)); p != "" {
		return p
	}
	return defaultEIPBlocklistPath
}

// eipBlocklistTTL resolves the recovery TTL from env or the default. A
// malformed or non-positive value falls back to the default rather than
// disabling recovery (TTL<=0 would never prune → permanent exile).
func eipBlocklistTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv(eipBlocklistTTLEnv)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultEIPBlocklistTTL
}

// loadPersistedBlocklist reads the store, prunes entries older than the
// TTL, and returns the still-valid poisoned IPs as a set. It is
// corruption-tolerant: a missing file is an empty set (first prov ever),
// and an unparseable file is treated as empty rather than failing the
// prov — the seed+env blocklist still applies, so we degrade to the
// pre-#3722 behaviour instead of blocking provisioning. now is injected
// for testability.
func loadPersistedBlocklist(now time.Time) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(eipBlocklistPath())
	if err != nil {
		return out // missing/unreadable → empty (graceful)
	}
	var f eipBlocklistFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return out // corrupt → empty (graceful; never fatal)
	}
	ttl := eipBlocklistTTL()
	cutoff := now.Add(-ttl).Unix()
	for _, r := range f.Records {
		ip := strings.TrimSpace(r.IP)
		if ip == "" {
			continue
		}
		if r.LastSeen >= cutoff {
			out[ip] = true
		}
	}
	return out
}

// recordPoisonedEIPs appends/refreshes the given IPs in the on-PVC store
// with a now timestamp, prunes aged-out entries, and writes the file
// back atomically (temp-file + rename). It holds eipBlocklistStoreMu for
// the whole read-modify-write. Failures (read-only FS, no PVC) are
// returned but treated as non-fatal by the caller — a prov must not fail
// because it couldn't persist the learned poison; it just loses the
// cross-prov memory for that address. now + the note (deployment id) are
// injected for testability + diagnostics. Empty input is a no-op.
func recordPoisonedEIPs(ips []string, note string, now time.Time) error {
	// Filter to non-empty, de-duplicated IPs first so an all-empty call
	// is a true no-op (no file write, no lock contention).
	want := map[string]bool{}
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			want[ip] = true
		}
	}
	if len(want) == 0 {
		return nil
	}

	eipBlocklistStoreMu.Lock()
	defer eipBlocklistStoreMu.Unlock()

	path := eipBlocklistPath()
	ttl := eipBlocklistTTL()
	cutoff := now.Add(-ttl).Unix()
	nowUnix := now.Unix()

	// Read existing (corruption-tolerant) and index by IP.
	merged := map[string]eipBlocklistRecord{}
	if raw, err := os.ReadFile(path); err == nil {
		var f eipBlocklistFile
		if json.Unmarshal(raw, &f) == nil {
			for _, r := range f.Records {
				ip := strings.TrimSpace(r.IP)
				if ip == "" || r.LastSeen < cutoff {
					continue // drop blanks + aged-out (TTL prune on write)
				}
				merged[ip] = r
			}
		}
	}

	// Upsert the freshly-observed poison with a refreshed timestamp.
	for ip := range want {
		merged[ip] = eipBlocklistRecord{IP: ip, LastSeen: nowUnix, Note: note}
	}

	// Stable, sorted output for deterministic files (easier to eyeball
	// on the PVC + stable test assertions).
	recs := make([]eipBlocklistRecord, 0, len(merged))
	for _, r := range merged {
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].IP < recs[j].IP })

	body, err := json.MarshalIndent(eipBlocklistFile{Version: 1, Records: recs}, "", "  ")
	if err != nil {
		return err
	}

	// Ensure the parent dir exists (first-ever write on a fresh PVC, or
	// a custom CATALYST_HUAWEI_NAT_EIP_STATE under a not-yet-created dir).
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}

	// Atomic write: temp file in the same dir + rename, so a crash mid-
	// write never leaves a truncated/corrupt store that the next prov
	// can't parse.
	tmp := path + ".tmp." + strconv.FormatInt(nowUnix, 10)
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
