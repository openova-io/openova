package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

// natCloud is a fake of the EIP API that the NAT EIP pre-flight calls: the
// listing, the publicIp quota, allocation (from pool, in order) and release.
// It counts the EIPs that exist at once, the bastion's among them.
type natCloud struct {
	t        *testing.T
	mu       sync.Mutex
	pool     []string // the addresses the next allocations return, in order
	quota    int
	eips     map[string]rawPublicIP // by id
	order    []string               // ids in allocation order
	released []string               // addresses, in order
	peak     int
	failPost bool
	n        int
}

func newNATCloud(t *testing.T, pool ...string) *natCloud {
	c := &natCloud{t: t, pool: pool, quota: 10, eips: map[string]rawPublicIP{}}
	c.eips["eip-bastion"] = rawPublicIP{ID: "eip-bastion", Address: bastionEIPAddress, Status: "ACTIVE", PortID: "port-bastion", BandwidthName: "bastion-openova-bw"}
	c.order = append(c.order, "eip-bastion")
	c.peak = 1
	return c
}

func (c *natCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/quotas"):
		_, _ = fmt.Fprintf(w, `{"quotas":{"resources":[{"type":"publicIp","used":%d,"quota":%d}]}}`, len(c.eips), c.quota)
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/publicips"):
		list := []rawPublicIP{}
		for _, id := range c.order {
			if e, ok := c.eips[id]; ok {
				list = append(list, e)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"publicips": list})
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/publicips"):
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			PublicIP struct {
				Type string `json:"type"`
			} `json:"publicip"`
			Bandwidth struct {
				Name       string `json:"name"`
				Size       int    `json:"size"`
				ShareType  string `json:"share_type"`
				ChargeMode string `json:"charge_mode"`
			} `json:"bandwidth"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			c.t.Errorf("allocation body %s: %v", raw, err)
		}
		if req.PublicIP.Type != "5_bgp" || req.Bandwidth.Size != 100 || req.Bandwidth.ShareType != "PER" || req.Bandwidth.ChargeMode != "traffic" {
			c.t.Errorf("allocation %s does not carry the module's NAT EIP settings (5_bgp, 100, PER, traffic)", raw)
		}
		if c.failPost || len(c.eips) >= c.quota || len(c.pool) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"VPC.0501","message":"quota exceeded"}`))
			return
		}
		c.n++
		e := rawPublicIP{ID: fmt.Sprintf("eip-%d", c.n), Address: c.pool[0], Status: "DOWN", BandwidthName: req.Bandwidth.Name}
		c.pool = c.pool[1:]
		c.eips[e.ID] = e
		c.order = append(c.order, e.ID)
		if len(c.eips) > c.peak {
			c.peak = len(c.eips)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"publicip": map[string]string{"id": e.ID, "public_ip_address": e.Address}})
	case r.Method == http.MethodDelete && strings.Contains(p, "/publicips/"):
		id := p[strings.LastIndex(p, "/")+1:]
		e, ok := c.eips[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(c.eips, id)
		c.released = append(c.released, e.Address)
		// A released address goes back to the front of the pool, as the
		// kom4dc pool hands a just-released EIP to the next allocation.
		c.pool = append([]string{e.Address}, c.pool...)
		w.WriteHeader(http.StatusNoContent)
	default:
		c.t.Errorf("unexpected %s %s", r.Method, p)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// serveNAT points the package at the fake and isolates the learned store.
func serveNAT(t *testing.T, c *natCloud) string {
	t.Helper()
	srv := httptest.NewTLSServer(c)
	orig := endpointFor
	endpointFor = func(string, string) string { return srv.URL }
	t.Cleanup(func() { endpointFor = orig; srv.Close() })
	store := filepath.Join(t.TempDir(), "nat-eip-blocklist.json")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_STATE", store)
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", "")
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_ROTATE_ALL", "")
	return store
}

var natSpec = providers.ProvisionSpec{
	DeploymentID: "3f83959f21abc560", SovereignFQDN: "hw99.omani.works",
	Creds:   providers.ProviderCreds{Raw: map[string]string{"access_key": "AK", "secret_key": "SK", "project_id": "P", "region": "me-east-215"}},
	Regions: []providers.RegionSpec{{Code: "me-east-215-a"}, {Code: "me-east-215-b"}},
}

const natPrefix = "catalyst-hw99-omani-works-3f83959f"

// CLD-G12 (2026-11-21): the first draw of region a is on the blocklist. The
// address that region a's NAT uses is the import the engine hands to
// OpenTofu, so the state names it after the install; the refused one is
// recorded and released before the plan, and nothing is left that no state
// knows. On main the pre-flight swapped the address after `tofu apply`
// through the cloud API, and the state kept the deleted one.
func TestChooseNATAddresses_ARefusedDrawIsHeldAndTheImportNamesTheCleanAddress(t *testing.T) {
	c := newNATCloud(t, "212.72.24.48", "212.72.24.24", "212.72.24.25")
	store := serveNAT(t, c)
	var msgs []string
	imports, err := chooseNATAddresses(context.Background(), natSpec, map[string]bool{}, func(m string) { msgs = append(msgs, m) })
	if err != nil {
		t.Fatalf("chooseNATAddresses: %v (progress %q)", err, msgs)
	}
	want := []providers.StateImport{
		{Address: `huaweicloud_vpc_eip.nat["me-east-215-a"]`, ID: "eip-2", Label: "212.72.24.24"},
		{Address: `huaweicloud_vpc_eip.nat["me-east-215-b"]`, ID: "eip-3", Label: "212.72.24.25"},
	}
	if !reflect.DeepEqual(imports, want) {
		t.Fatalf("imports = %+v; want %+v", imports, want)
	}
	if !reflect.DeepEqual(c.released, []string{"212.72.24.48"}) {
		t.Errorf("released %v; want only the refused 212.72.24.48", c.released)
	}
	for _, im := range imports {
		e, ok := c.eips[im.ID]
		if !ok || e.Address != im.Label || !strings.HasPrefix(e.BandwidthName, natPrefix+"-") {
			t.Errorf("import %+v: the cloud holds %+v; want that EIP under the deployment's name prefix", im, e)
		}
	}
	if len(c.eips) != 3 {
		t.Errorf("%d EIPs exist after the choice; want the bastion's and the two imported", len(c.eips))
	}
	if !loadPersistedBlocklist(time.Now())["212.72.24.48"] {
		raw, _ := os.ReadFile(filepath.Clean(store))
		t.Errorf("the refused address is not in the learned blocklist: %s", raw)
	}
}

// The wipe records every address it released, so right after one the pool
// hands back up to six refused addresses first. The choice holds them all and
// never has more EIPs alive than the quota of 10.
func TestChooseNATAddresses_AfterAWipeStaysWithinTheQuota(t *testing.T) {
	refused := []string{"212.72.24.1", "212.72.24.2", "212.72.24.3", "212.72.24.4", "212.72.24.5", "212.72.24.6"}
	c := newNATCloud(t, append(append([]string{}, refused...), "212.72.24.30", "212.72.24.31")...)
	serveNAT(t, c)
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", strings.Join(refused, ","))
	imports, err := chooseNATAddresses(context.Background(), natSpec, nil, nil)
	if err != nil || len(imports) != 2 || imports[0].Label != "212.72.24.30" || imports[1].Label != "212.72.24.31" {
		t.Fatalf("imports %+v, %v; want 212.72.24.30 and 212.72.24.31", imports, err)
	}
	if c.peak > 10 || c.peak != 9 {
		t.Errorf("at most %d EIPs existed at once; want 9 (bastion 1 + held 6 + chosen 2), never above the quota of 10", c.peak)
	}
	if len(c.released) != 6 || len(c.eips) != 3 {
		t.Errorf("released %v, %d left; want the six refused released and three left", c.released, len(c.eips))
	}
}

// A pool with more refused addresses than the quota leaves room for: the
// choice stops at the quota, releases what it held, and the region it could
// not serve gets no import (OpenTofu allocates it).
func TestChooseNATAddresses_TheHoldBudgetEndsAtTheQuota(t *testing.T) {
	var refused []string
	for i := 1; i <= 9; i++ {
		refused = append(refused, fmt.Sprintf("212.72.24.%d", i))
	}
	c := newNATCloud(t, append(append([]string{}, refused...), "212.72.24.30")...)
	serveNAT(t, c)
	t.Setenv("CATALYST_HUAWEI_NAT_EIP_BLOCKLIST", strings.Join(refused, ","))
	var msgs []string
	imports, err := chooseNATAddresses(context.Background(), natSpec, nil, func(m string) { msgs = append(msgs, m) })
	if err != nil || len(imports) != 0 {
		t.Fatalf("imports %+v, %v; want none and no error", imports, err)
	}
	if c.peak > 10 {
		t.Errorf("%d EIPs existed at once; the quota is 10", c.peak)
	}
	if len(c.eips) != 1 {
		t.Errorf("%d EIPs left; want only the bastion's", len(c.eips))
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "OpenTofu allocates it") {
		t.Errorf("progress %q does not say that OpenTofu allocates the region's NAT EIP", msgs)
	}
}

// A region whose NAT EIP the state holds gets nothing; one whose EIP an
// earlier attempt (or the module) made keeps it; nothing is allocated twice.
func TestChooseNATAddresses_KeepsWhatExists(t *testing.T) {
	c := newNATCloud(t, "212.72.24.40")
	c.eips["eip-old"] = rawPublicIP{ID: "eip-old", Address: "212.72.24.24", Status: "DOWN", BandwidthName: natPrefix + "-me-east-215-b-nat-preflight-2"}
	c.order = append(c.order, "eip-old")
	serveNAT(t, c)
	inState := map[string]bool{`huaweicloud_vpc_eip.nat["me-east-215-a"]`: true}
	imports, err := chooseNATAddresses(context.Background(), natSpec, inState, nil)
	want := []providers.StateImport{{Address: `huaweicloud_vpc_eip.nat["me-east-215-b"]`, ID: "eip-old", Label: "212.72.24.24"}}
	if err != nil || !reflect.DeepEqual(imports, want) {
		t.Fatalf("imports %+v, %v; want %+v", imports, err, want)
	}
	if c.n != 0 || len(c.released) != 0 {
		t.Errorf("%d allocated, %v released; want nothing", c.n, c.released)
	}
}

// A failed allocation releases every EIP the call made and returns the error.
func TestChooseNATAddresses_AFailureLeavesNothing(t *testing.T) {
	c := newNATCloud(t, "212.72.24.48", "212.72.24.24")
	serveNAT(t, c)
	imports, err := chooseNATAddresses(context.Background(), natSpec, nil, nil)
	if err == nil || imports != nil {
		t.Fatalf("imports %+v, %v; want the allocation error", imports, err)
	}
	if len(c.eips) != 1 {
		t.Errorf("%d EIPs left; want only the bastion's", len(c.eips))
	}
}

// #3716: the signing credentials are the bare keys of the credentials map; a
// missing one fails before any cloud call.
func TestChooseNATAddresses_NeedsTheCredentials(t *testing.T) {
	for key, want := range map[string]string{"access_key": "access_key is required", "secret_key": "secret_key is required", "project_id": "project_id is required"} {
		spec := natSpec
		spec.Creds = providers.ProviderCreds{Raw: map[string]string{"access_key": "AK", "secret_key": "SK", "project_id": "P"}}
		delete(spec.Creds.Raw, key)
		if _, err := chooseNATAddresses(context.Background(), spec, nil, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("without %s: %v; want %q", key, err, want)
		}
	}
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

// The pre-flight allocates what the module declares: the import of its EIP
// differs from huaweicloud_vpc_eip.nat in the bandwidth name alone, and the
// names it reuses are the module's. A change to the module's NAT EIP fails
// here first.
func TestChooseNATAddresses_MatchesTheModulesNATEIP(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..", "..", "infra", "providers", "huawei", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	tf := string(raw)
	start := strings.Index(tf, "resource \"huaweicloud_vpc_eip\" \"nat\" {")
	end := strings.Index(tf[max(start, 0):], "\n}\n")
	if start < 0 || end < 0 {
		t.Fatal("main.tf has no resource huaweicloud_vpc_eip.nat")
	}
	block := strings.Join(strings.Fields(tf[start:start+end]), " ")
	for _, want := range []string{
		"for_each = toset(local.region_keys)",
		fmt.Sprintf("publicip { type = %q }", natEIPType),
		fmt.Sprintf("name = \"${local.name_prefix}-${each.key}-nat-bw\" size = %d share_type = %q charge_mode = %q }", natBandwidthSize, natBandwidthShare, natBandwidthCharged),
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the module's NAT EIP %q lacks %q", block, want)
		}
	}
	for _, want := range []string{
		`fqdn_slug = replace(var.sovereign_fqdn, ".", "-")`,
		`name_prefix = "catalyst-${local.fqdn_slug}-${substr(var.deployment_id, 0, 8)}"`,
		`region_keys = [for r in var.regions : r.code]`,
	} {
		if !strings.Contains(strings.Join(strings.Fields(tf), " "), want) {
			t.Errorf("main.tf lacks %q, from which the pre-flight's names follow", want)
		}
	}
}
