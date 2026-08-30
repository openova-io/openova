package huawei

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// fakeRepo is an in-memory Repository so per-source isolation can be proven
// without Postgres.
type fakeRepo struct {
	mu               sync.Mutex
	sources          []store.CostSource
	creds            map[string][2]string // id -> {access key, encrypted secret}
	credReads        map[string]int
	startDates       map[string]time.Time
	inventory        map[string]map[string]store.InventoryItem
	usage            map[string]store.UsageRecord
	panicOnCollected map[string]bool
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		creds: map[string][2]string{}, credReads: map[string]int{}, startDates: map[string]time.Time{},
		inventory: map[string]map[string]store.InventoryItem{}, usage: map[string]store.UsageRecord{}, panicOnCollected: map[string]bool{},
	}
}

func (f *fakeRepo) addSource(id, customer, project, credID string, enc []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cid := credID
	f.creds[credID] = [2]string{"AKTEST", string(enc)}
	f.sources = append(f.sources, store.CostSource{ID: id, CustomerID: customer, Kind: "huawei-project", Region: "me-east-215", ProjectID: project, CredentialID: &cid, Status: "verified"})
	f.startDates[customer] = time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
}

func (f *fakeRepo) source(id string) store.CostSource {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sources {
		if s.ID == id {
			return s
		}
	}
	return store.CostSource{}
}

func (f *fakeRepo) update(id string, fn func(*store.CostSource)) {
	for i := range f.sources {
		if f.sources[i].ID == id {
			fn(&f.sources[i])
		}
	}
}

func (f *fakeRepo) ListVerifiedSources(context.Context) ([]store.CostSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.CostSource
	for _, s := range f.sources {
		if s.Status == "verified" && s.CredentialID != nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeRepo) GetCredentialSecret(_ context.Context, id string) (string, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.credReads[id]++
	c, ok := f.creds[id]
	if !ok {
		return "", nil, store.ErrNotFound
	}
	return c[0], []byte(c[1]), nil
}

func (f *fakeRepo) SetSourceError(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.update(id, func(s *store.CostSource) { m := msg; s.LastError = &m })
	return nil
}

func (f *fakeRepo) SetSourceFailed(_ context.Context, id, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.update(id, func(s *store.CostSource) { m := msg; s.LastError = &m; s.Status = "failed" })
	return nil
}

func (f *fakeRepo) SetSourceCollected(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnCollected[id] {
		panic("simulated store failure for " + id)
	}
	f.update(id, func(s *store.CostSource) { t := at; s.LastCollectedAt = &t; s.LastError = nil })
	return nil
}

func (f *fakeRepo) CustomerStartDate(_ context.Context, id string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startDates[id], nil
}

func (f *fakeRepo) UpsertInventory(_ context.Context, sourceID string, items []store.InventoryUpsert) (map[string]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := map[string]json.RawMessage{}
	inv := f.inventory[sourceID]
	if inv == nil {
		inv = map[string]store.InventoryItem{}
		f.inventory[sourceID] = inv
	}
	for _, it := range items {
		attrs, _ := json.Marshal(it.Attrs)
		if old, ok := inv[it.ResourceID]; ok {
			prev[it.ResourceID] = old.Attrs
			old.Name, old.Attrs, old.LastSeen, old.DeletedAt = it.Name, attrs, it.SeenAt, nil
			inv[it.ResourceID] = old
			continue
		}
		first := it.SeenAt
		if !it.Created.IsZero() {
			first = it.Created
		}
		inv[it.ResourceID] = store.InventoryItem{SourceID: sourceID, ResourceID: it.ResourceID, Kind: it.Kind, Name: it.Name, Attrs: attrs, FirstSeen: first, LastSeen: it.SeenAt}
	}
	return prev, nil
}

func (f *fakeRepo) SetInventoryAttrs(_ context.Context, sourceID, resourceID string, attrs any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.inventory[sourceID][resourceID]
	it.Attrs, _ = json.Marshal(attrs)
	f.inventory[sourceID][resourceID] = it
	return nil
}

func (f *fakeRepo) MarkInventoryDeleted(_ context.Context, sourceID string, kinds, seen []string, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seenSet := map[string]bool{}
	for _, s := range seen {
		seenSet[s] = true
	}
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	var n int64
	for id, it := range f.inventory[sourceID] {
		if kindSet[it.Kind] && it.DeletedAt == nil && !seenSet[id] {
			t := at
			it.DeletedAt = &t
			f.inventory[sourceID][id] = it
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) ListInventory(_ context.Context, sourceID string) ([]store.InventoryItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.InventoryItem
	for _, it := range f.inventory[sourceID] {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name })
	return out, nil
}

func (f *fakeRepo) GetInventoryItem(_ context.Context, sourceID, resourceID string) (store.InventoryItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.inventory[sourceID][resourceID]
	if !ok {
		return store.InventoryItem{}, store.ErrNotFound
	}
	return it, nil
}

func (f *fakeRepo) SetInventoryBounds(_ context.Context, sourceID, resourceID string, firstSeen, deletedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := f.inventory[sourceID][resourceID]
	if firstSeen != nil {
		it.FirstSeen = *firstSeen
	}
	if deletedAt != nil {
		it.DeletedAt = deletedAt
	}
	f.inventory[sourceID][resourceID] = it
	return nil
}

func (f *fakeRepo) UpsertUsage(_ context.Context, recs []store.UsageRecord) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		f.usage[r.SourceID+"|"+r.ResourceID+"|"+r.SKU+"|"+r.WindowStart.Format(time.RFC3339)] = r
	}
	return len(recs), nil
}

func (f *fakeRepo) DeleteUsageInRange(_ context.Context, sourceID, resourceID string, from, to time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for k, r := range f.usage {
		if r.SourceID == sourceID && r.ResourceID == resourceID && !r.WindowStart.Before(from) && r.WindowStart.Before(to) {
			delete(f.usage, k)
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) usageCount(sourceID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.usage {
		if r.SourceID == sourceID {
			n++
		}
	}
	return n
}

func (f *fakeRepo) inventoryCount(sourceID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inventory[sourceID])
}

// newIsolationCollector wires a fake gateway (13 ECS + volumes + EIP + ELB +
// NAT) with an optional per-project rejection and a fake repository.
func newIsolationCollector(t *testing.T, rejectProject string) (*Collector, *fakeRepo, *fakeGateway) {
	t.Helper()
	gw := &fakeGateway{}
	for i := 1; i <= 13; i++ {
		gw.servers = append(gw.servers, server(fmt.Sprintf("srv-%02d", i), "s6.large.2", "ACTIVE"))
	}
	inner := gw.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectProject != "" && strings.Contains(r.URL.Path, "/"+rejectProject+"/") {
			w.WriteHeader(401)
			w.Write([]byte(`{"error_code":"APIGW.0301","error_msg":"Incorrect IAM authentication information"}`))
			return
		}
		inner(w, r)
	}))
	t.Cleanup(srv.Close)
	keys, _ := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{5}, 32))
	repo := newFakeRepo()
	col := &Collector{
		Store:           repo,
		Client:          NewClient(srv.URL+"/%s/%s", false, 5*time.Second, metrics.New()),
		Keys:            keys,
		Metrics:         metrics.New(),
		CollectInterval: 15 * time.Minute,
		CTSInterval:     5 * time.Minute,
		CESInterval:     time.Hour,
		Now:             func() time.Time { return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) },
	}
	return col, repo, gw
}

func TestCollectAllIsolatesUndecryptableCredential(t *testing.T) {
	col, repo, _ := newIsolationCollector(t, "")
	good, _ := col.Keys.Seal([]byte(testSecret))
	// The first source's secret was sealed under another key (or is garbage):
	// it must fail alone, be disabled, and never abort the tick.
	repo.addSource("src-a", "cust", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cred-a", []byte("sealed-under-a-previous-key"))
	repo.addSource("src-b", "cust", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cred-b", good)

	res := col.CollectAll(context.Background())
	if res.Result != "partial" || res.Sources != 2 || res.Failed != 1 || res.Errors["src-a"] == nil || res.Errors["src-b"] != nil {
		t.Fatalf("tick = %+v", res)
	}
	a := repo.source("src-a")
	if a.Status != "failed" || a.LastError == nil || !strings.Contains(*a.LastError, "APP_ENCRYPTION_KEY") || !strings.Contains(*a.LastError, "POST /sources/{id}/credential") {
		t.Fatalf("src-a = %+v", a)
	}
	b := repo.source("src-b")
	if b.Status != "verified" || b.LastCollectedAt == nil || b.LastError != nil {
		t.Fatalf("src-b = %+v", b)
	}
	if n := repo.inventoryCount("src-b"); n != 16 {
		t.Fatalf("src-b inventory = %d, want 16", n)
	}
	if n := repo.usageCount("src-b"); n == 0 {
		t.Fatal("src-b produced no usage")
	}
	if repo.inventoryCount("src-a") != 0 || repo.usageCount("src-a") != 0 {
		t.Fatal("failed source wrote inventory or usage")
	}
	m := col.Metrics
	if m.Get("chargeback_collect_ticks_total", map[string]string{"step": "collect", "result": "partial"}) != 1 ||
		m.Get("chargeback_collect_sources_total", map[string]string{"step": "collect", "result": "error"}) != 1 ||
		m.Get("chargeback_collect_sources_total", map[string]string{"step": "collect", "result": "ok"}) != 1 {
		t.Fatal("tick/source metrics not recorded as partial + 1 error + 1 ok")
	}

	// The disabled source is not retried on later ticks: it is no longer
	// listed, its credential is not read again, and the tick is clean.
	res = col.CollectAll(context.Background())
	if res.Result != "ok" || res.Sources != 1 || res.Failed != 0 {
		t.Fatalf("second tick = %+v", res)
	}
	if repo.credReads["cred-a"] != 1 {
		t.Fatalf("undecryptable credential read %d times, want 1", repo.credReads["cred-a"])
	}
	if m.Get("chargeback_collect_ticks_total", map[string]string{"step": "collect", "result": "ok"}) != 1 {
		t.Fatal("clean tick not counted as ok")
	}
	// The same isolation applies to the CTS and CES loops.
	if r := col.PollCTSAll(context.Background()); r.Result != "ok" || r.Sources != 1 {
		t.Fatalf("cts tick = %+v", r)
	}
	if r := col.SampleCESAll(context.Background()); r.Result != "ok" || r.Sources != 1 {
		t.Fatalf("ces tick = %+v", r)
	}
}

func TestCollectAllIsolatesGatewayRejectionWithBackoff(t *testing.T) {
	col, repo, _ := newIsolationCollector(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	good, _ := col.Keys.Seal([]byte(testSecret))
	repo.addSource("src-a", "cust", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cred-a", good)
	repo.addSource("src-b", "cust", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cred-b", good)

	res := col.CollectAll(context.Background())
	if res.Result != "partial" || res.Failed != 1 || res.Errors["src-a"] == nil {
		t.Fatalf("tick = %+v", res)
	}
	a := repo.source("src-a")
	if a.Status != "verified" || a.LastError == nil || !strings.Contains(*a.LastError, "APIGW.0301") {
		t.Fatalf("src-a = %+v", a)
	}
	if strings.Contains(*a.LastError, testSecret) {
		t.Fatal("secret in last_error")
	}
	if repo.inventoryCount("src-b") != 16 {
		t.Fatal("healthy source did not collect")
	}
	// Backoff: the failed source is skipped, the healthy one keeps going.
	res = col.CollectAll(context.Background())
	if res.Result != "ok" || res.Skipped != 1 || res.Sources != 2 || res.Failed != 0 {
		t.Fatalf("tick in backoff = %+v", res)
	}
	if repo.credReads["cred-a"] != 1 {
		t.Fatalf("backed-off source was retried (%d reads)", repo.credReads["cred-a"])
	}
	// After the backoff window it is tried again.
	col.Now = func() time.Time { return time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC) }
	res = col.CollectAll(context.Background())
	if res.Skipped != 0 || res.Failed != 1 || repo.credReads["cred-a"] != 2 {
		t.Fatalf("tick after backoff = %+v reads=%d", res, repo.credReads["cred-a"])
	}
}

func TestCollectAllIsolatesPanic(t *testing.T) {
	col, repo, _ := newIsolationCollector(t, "")
	good, _ := col.Keys.Seal([]byte(testSecret))
	repo.addSource("src-a", "cust", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cred-a", good)
	repo.addSource("src-b", "cust", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cred-b", good)
	repo.panicOnCollected["src-a"] = true

	res := col.CollectAll(context.Background())
	if res.Result != "partial" || res.Failed != 1 || res.Errors["src-a"] == nil || !strings.Contains(res.Errors["src-a"].Error(), "panic in collect") {
		t.Fatalf("tick = %+v", res)
	}
	a := repo.source("src-a")
	if a.LastError == nil || !strings.Contains(*a.LastError, "panic in collect") || a.Status != "verified" {
		t.Fatalf("src-a = %+v", a)
	}
	if b := repo.source("src-b"); b.LastCollectedAt == nil || repo.inventoryCount("src-b") != 16 {
		t.Fatalf("src-b = %+v inventory=%d", b, repo.inventoryCount("src-b"))
	}
	if !col.inBackoff("src-a") {
		t.Fatal("panicking source not backed off")
	}
}
