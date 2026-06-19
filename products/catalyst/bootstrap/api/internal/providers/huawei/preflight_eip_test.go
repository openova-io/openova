package huawei

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRotateBlocklistedNATEIPs_PassesAccessKeyThrough is the regression
// guard for #3716. The original RotateBlocklistedNATEIPs built a
// providers.ProviderCreds{Raw} map keyed with the OpenTofu *tfvars*
// names (`huawei_access_key` etc.) and handed it to
// credsFromProviderCreds(), which reads the BARE signing keys
// (`access_key`/`secret_key`/`project_id`). The key-name mismatch made
// the function fail with "huawei: access_key is required" on EVERY
// Huawei prov — even when a valid access_key WAS supplied — so the
// poisoned-EIP self-heal never ran and hw151–154 wedged with no egress
// to harbor.openova.io.
//
// We cannot point endpointFor() at an httptest server without a wider
// refactor, so we assert the contract at the creds boundary:
//   - a SUPPLIED access_key must NOT yield "access_key is required"
//     (the call proceeds to the HTTP layer and fails there on DNS/dial,
//     which is a DIFFERENT error) — this is exactly what the bug broke;
//   - an EMPTY access_key MUST yield "access_key is required".
func TestRotateBlocklistedNATEIPs_PassesAccessKeyThrough(t *testing.T) {
	// Short ctx so the real signed request to nat.<region>.kom4dc
	// .nationalcloud.om (which does not resolve from CI) fails fast
	// instead of hanging on the httpTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	t.Run("supplied access_key is not reported missing", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"AKIA-test-key", "secret-key-of-decent-length-32chrs", "0123456789abcdef0123456789abcdef",
			"me-east-215", nil)
		if err == nil {
			// A nil error would mean the HTTP call somehow succeeded — not
			// expected from CI, but it definitively proves the creds were
			// accepted, so it satisfies the regression guard.
			return
		}
		if strings.Contains(err.Error(), "access_key is required") {
			t.Fatalf("#3716 regression: valid access_key reported as missing — creds wiring broken: %v", err)
		}
		if strings.Contains(err.Error(), "secret_key is required") ||
			strings.Contains(err.Error(), "project_id is required") {
			t.Fatalf("#3716 regression: valid creds reported as missing: %v", err)
		}
	})

	t.Run("empty access_key fails closed", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"", "secret-key", "project-id", "me-east-215", nil)
		if err == nil || !strings.Contains(err.Error(), "access_key is required") {
			t.Fatalf("empty access_key must fail with 'access_key is required', got: %v", err)
		}
	})

	t.Run("empty secret_key fails closed", func(t *testing.T) {
		_, err := RotateBlocklistedNATEIPs(ctx, "huawei", "deadbeefcafef00d", "t99.omani.works",
			"AKIA-test-key", "", "project-id", "me-east-215", nil)
		if err == nil || !strings.Contains(err.Error(), "secret_key is required") {
			t.Fatalf("empty secret_key must fail with 'secret_key is required', got: %v", err)
		}
	})
}

// TestPreflightBlocklist_SeedAndEnvMerge guards the blocklist() helper —
// the seed addresses are always present and CATALYST_HUAWEI_NAT_EIP_BLOCKLIST
// extends (never replaces) them. The poisoned-pool self-heal leans on
// operators being able to add freshly-discovered bad EIPs without a code
// change.
func TestPreflightBlocklist_SeedAndEnvMerge(t *testing.T) {
	// Isolate the persisted-store path (#3722) to a non-existent temp
	// file so this test asserts ONLY the seed+env merge and never reads a
	// real /var/lib/catalyst store on a dev box.
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", filepath.Join(t.TempDir(), "absent.json"))

	for _, seed := range []string{"212.72.24.48", "212.72.24.14"} {
		if !blocklist()[seed] {
			t.Fatalf("seed blocklist missing %s", seed)
		}
	}

	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", " 45.151.123.77 , 45.151.123.88 ")
	bl := blocklist()
	for _, want := range []string{"212.72.24.48", "212.72.24.14", "45.151.123.77", "45.151.123.88"} {
		if !bl[want] {
			t.Fatalf("merged blocklist missing %s (got %v)", want, bl)
		}
	}
}

// ---------------------------------------------------------------------
// #3722 — persistent, auto-growing, TTL-bounded learned blocklist.
// These guard the zero-touch poisoned-pool drain that replaces the manual
// ROTATE_ALL + flux-suspend dance.
// ---------------------------------------------------------------------

// TestEIPBlocklistStore_RecordThenLoadRoundtrip is the core zero-touch
// contract: an EIP rotated away from on prov N (recordPoisonedEIPs) is
// auto-avoided on prov N+1 (loadPersistedBlocklist / blocklist()) with no
// operator action.
func TestEIPBlocklistStore_RecordThenLoadRoundtrip(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)

	// Anchor `now` to the REAL wall clock, not a fixed past date: the
	// blocklist() merged-accessor below reads loadPersistedBlocklist with
	// time.Now() internally, so a hardcoded timestamp older than the TTL
	// (default 24h) would age the just-recorded entries out by the time
	// the test runs on a later calendar day, failing spuriously. Recording
	// at "just now" keeps the round-trip within TTL regardless of the date.
	now := time.Now()
	// Prov N rotates away from a freshly-poisoned address NOT in the seed.
	if err := recordPoisonedEIPs([]string{"212.72.24.8", "212.72.24.59"}, "depN", now); err != nil {
		t.Fatalf("recordPoisonedEIPs: %v", err)
	}

	// Prov N+1 (same now, well within TTL) must see the learned poison.
	got := loadPersistedBlocklist(now.Add(1 * time.Hour))
	for _, want := range []string{"212.72.24.8", "212.72.24.59"} {
		if !got[want] {
			t.Fatalf("learned blocklist missing %s after record (got %v)", want, got)
		}
	}

	// And it must surface through blocklist() merged with the seed.
	merged := blocklist()
	for _, want := range []string{"212.72.24.8", "212.72.24.59", "212.72.24.48"} {
		if !merged[want] {
			t.Fatalf("blocklist() missing %s (seed+learned merge broken): %v", want, merged)
		}
	}
}

// TestEIPBlocklistStore_TTLAgesOut is the recovery path: an EIP that
// became clean again (last seen poisoned longer ago than the TTL) ages
// out of the learned blocklist so it can re-enter rotation, while a
// recently-seen poison stays.
func TestEIPBlocklistStore_TTLAgesOut(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_TTL", "24h")

	base := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	// Old poison recorded 30h ago, recent poison recorded 1h ago.
	if err := recordPoisonedEIPs([]string{"212.72.24.43"}, "old", base.Add(-30*time.Hour)); err != nil {
		t.Fatalf("record old: %v", err)
	}
	if err := recordPoisonedEIPs([]string{"212.72.24.29"}, "recent", base.Add(-1*time.Hour)); err != nil {
		t.Fatalf("record recent: %v", err)
	}

	got := loadPersistedBlocklist(base)
	if got["212.72.24.43"] {
		t.Fatalf("expired poison .43 (30h old, TTL 24h) should have aged out, but is still blocked: %v", got)
	}
	if !got["212.72.24.29"] {
		t.Fatalf("recent poison .29 (1h old) must still be blocked: %v", got)
	}
}

// TestEIPBlocklistStore_RecordRefreshesTimestamp guards that re-recording
// a recurring poison refreshes its timestamp (so it never ages out while
// it keeps recurring), and that the second record's older siblings are
// pruned on write.
func TestEIPBlocklistStore_RecordRefreshesTimestamp(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_TTL", "24h")

	base := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	// First sighting 20h ago — would age out by base+5h without a refresh.
	if err := recordPoisonedEIPs([]string{"212.72.24.46"}, "first", base.Add(-20*time.Hour)); err != nil {
		t.Fatalf("record first: %v", err)
	}
	// Recurs "now" — refresh the timestamp.
	if err := recordPoisonedEIPs([]string{"212.72.24.46"}, "again", base); err != nil {
		t.Fatalf("record again: %v", err)
	}

	// 5h after base: 20h-old would be 25h (expired), refreshed is 5h (live).
	got := loadPersistedBlocklist(base.Add(5 * time.Hour))
	if !got["212.72.24.46"] {
		t.Fatalf("recurring poison .46 must stay blocked after refresh: %v", got)
	}

	// The store must contain exactly one record for .46 (upsert, not dup).
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var f eipBlocklistFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal store: %v", err)
	}
	count := 0
	for _, r := range f.Records {
		if r.IP == "212.72.24.46" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 record for .46 (upsert), got %d: %+v", count, f.Records)
	}
}

// TestEIPBlocklistStore_CorruptFileIsGraceful asserts a garbage store
// never fails a prov — load returns empty and a subsequent record
// overwrites it cleanly (atomic write). This is the "degrade to seed+env"
// safety net.
func TestEIPBlocklistStore_CorruptFileIsGraceful(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)
	if err := os.WriteFile(store, []byte("{not valid json at all"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	// Load must not panic / error — just empty.
	if got := loadPersistedBlocklist(time.Now()); len(got) != 0 {
		t.Fatalf("corrupt store should load empty, got %v", got)
	}

	// Record over the corruption must succeed and produce a parseable file.
	now := time.Now()
	if err := recordPoisonedEIPs([]string{"212.72.24.8"}, "recover", now); err != nil {
		t.Fatalf("record over corrupt store: %v", err)
	}
	if got := loadPersistedBlocklist(now); !got["212.72.24.8"] {
		t.Fatalf("record over corrupt store did not heal it: %v", got)
	}
}

// TestEIPBlocklistStore_EmptyRecordIsNoop asserts an all-empty record
// call writes nothing (no spurious empty file) — the common case where a
// prov found no EIPs to rotate.
func TestEIPBlocklistStore_EmptyRecordIsNoop(t *testing.T) {
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)

	if err := recordPoisonedEIPs([]string{"", "  ", ""}, "noop", time.Now()); err != nil {
		t.Fatalf("empty record should be a no-op, got err: %v", err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("empty record must not create the store file (stat err=%v)", err)
	}
}

// TestEIPBlocklistStore_MissingFileLoadsEmpty asserts the first-ever prov
// (no store yet) loads an empty set rather than erroring.
func TestEIPBlocklistStore_MissingFileLoadsEmpty(t *testing.T) {
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", filepath.Join(t.TempDir(), "never-written.json"))
	if got := loadPersistedBlocklist(time.Now()); len(got) != 0 {
		t.Fatalf("missing store must load empty, got %v", got)
	}
}

// TestEIPBlocklistTTL_MalformedFallsBackToDefault asserts a junk TTL env
// doesn't disable recovery (TTL<=0 would never prune → permanent exile).
func TestEIPBlocklistTTL_MalformedFallsBackToDefault(t *testing.T) {
	for _, bad := range []string{"", "not-a-duration", "0s", "-5h"} {
		t.Setenv("CATALYST_HUAWEI_NAT_EIP_TTL", bad)
		if got := eipBlocklistTTL(); got != defaultEIPBlocklistTTL {
			t.Fatalf("TTL %q should fall back to default %v, got %v", bad, defaultEIPBlocklistTTL, got)
		}
	}
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_TTL", "48h")
	if got := eipBlocklistTTL(); got != 48*time.Hour {
		t.Fatalf("valid TTL 48h not honoured, got %v", got)
	}
}
