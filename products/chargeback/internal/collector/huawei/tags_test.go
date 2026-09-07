package huawei

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestNormalizeTagsShapes: the three shapes the Huawei APIs emit fold into
// one map; keys keep their case; empty keys go; a bare "key" keeps the key.
func TestNormalizeTagsShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"ecs/eip key=value strings", `["team=platform","Env=prod","cost-centre=CC-42"]`, map[string]string{"team": "platform", "Env": "prod", "cost-centre": "CC-42"}},
		{"value with an equals sign", `["url=https://x/?a=b"]`, map[string]string{"url": "https://x/?a=b"}},
		{"elb key/value objects", `[{"key":"team","value":"platform"},{"key":"Env","value":"prod"}]`, map[string]string{"team": "platform", "Env": "prod"}},
		{"evs object", `{"team":"platform","Env":"prod"}`, map[string]string{"team": "platform", "Env": "prod"}},
		{"object with a numeric value", `{"replicas":3,"canary":true}`, map[string]string{"replicas": "3", "canary": "true"}},
		{"empty keys dropped", `["=novalue","",{"key":"","value":"x"}]`, map[string]string{}},
		{"null", `null`, map[string]string{}},
		{"unknown scalar shape", `"team=platform"`, map[string]string{}},
		{"mixed array entries", `["team=a",{"key":"env","value":"b"},42]`, map[string]string{"team": "a", "env": "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeTags(decode(t, c.in)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("normalizeTags(%s) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestNormalizeTagsBareKeyKeepsEmptyValue is the mutant killer: a tag with
// no "=" is a real tag with an empty value, not garbage. A normaliser that
// drops it (or one that stores the key as the value) fails here.
func TestNormalizeTagsBareKeyKeepsEmptyValue(t *testing.T) {
	got := normalizeTags(decode(t, `["novalue","team=a"]`))
	v, ok := got["novalue"]
	if !ok {
		t.Fatalf("bare key dropped: %v", got)
	}
	if v != "" {
		t.Fatalf("bare key value = %q, want empty", v)
	}
	if got["team"] != "a" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizeTagsCaseAndLimits(t *testing.T) {
	// Keys are case-sensitive on Huawei: Team and team are two tags.
	got := normalizeTags(decode(t, `["Team=a","team=b"]`))
	if got["Team"] != "a" || got["team"] != "b" {
		t.Fatalf("case folded: %v", got)
	}
	// A key over 128 characters is dropped; one of exactly 128 is kept.
	long := ""
	for i := 0; i < 129; i++ {
		long += "k"
	}
	got = normalizeTags(map[string]any{long: "x", long[:128]: "y"})
	if _, ok := got[long]; ok || got[long[:128]] != "y" || len(got) != 1 {
		t.Fatalf("key length cap: %v", got)
	}
	// More than 50 tags: the first 50 in key order survive, deterministically.
	m := map[string]any{}
	for i := 0; i < 60; i++ {
		m[fmt.Sprintf("k%02d", i)] = "v"
	}
	got = normalizeTags(m)
	if len(got) != 50 {
		t.Fatalf("cap = %d tags", len(got))
	}
	if _, ok := got["k49"]; !ok {
		t.Fatal("k49 must survive the cap")
	}
	if _, ok := got["k50"]; ok {
		t.Fatal("k50 must be cut by the cap")
	}
}

func TestTagsFromRawAndPutTagAttrs(t *testing.T) {
	if got := tagsFromRaw(nil); len(got) != 0 {
		t.Fatalf("nil raw = %v", got)
	}
	if got := tagsFromRaw(json.RawMessage(`{not json`)); len(got) != 0 {
		t.Fatalf("bad raw = %v", got)
	}
	attrs := map[string]any{"status": "ACTIVE"}
	putTagAttrs(attrs, json.RawMessage(`[]`), "")
	if _, ok := attrs["tags"]; ok {
		t.Fatal("empty tags must not be stored")
	}
	if _, ok := attrs["enterprise_project_id"]; ok {
		t.Fatal("empty enterprise project must not be stored")
	}
	putTagAttrs(attrs, json.RawMessage(`["team=a"]`), " ep-1 ")
	if tags, _ := attrs["tags"].(map[string]string); tags["team"] != "a" {
		t.Fatalf("tags = %v", attrs["tags"])
	}
	if attrs["enterprise_project_id"] != "ep-1" {
		t.Fatalf("enterprise_project_id = %v", attrs["enterprise_project_id"])
	}
}

// TestEmitUsageCarriesTagsAndEnterpriseProject: inventory attrs written by
// the listers reach every usage record as labels.tags (a map) and
// labels.enterprise_project, and an untagged resource carries neither key.
func TestEmitUsageCarriesTagsAndEnterpriseProject(t *testing.T) {
	repo := newFakeRepo()
	src := store.CostSource{ID: "src-1", CustomerID: "cust-1", Kind: "huawei-project", Region: "me-east-215", ProjectID: "pid", Status: "verified"}
	repo.sources = append(repo.sources, src)
	col := &Collector{Store: repo, Metrics: metrics.New(), Now: func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }}
	created := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if _, err := repo.UpsertInventory(context.Background(), src.ID, []store.InventoryUpsert{
		{ResourceID: "s1", Kind: KindECS, Name: "web-1", Created: created, SeenAt: created,
			Attrs: map[string]any{"flavor": "s6.large.2", "status": "ACTIVE", "tags": map[string]string{"team": "platform", "novalue": ""}, "enterprise_project_id": "ep-1"}},
		{ResourceID: "s2", Kind: KindECS, Name: "web-2", Created: created, SeenAt: created,
			Attrs: map[string]any{"flavor": "s6.large.2", "status": "ACTIVE"}},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := col.emitUsage(context.Background(), src, created, created.Add(2*time.Hour), "", "")
	if err != nil || n == 0 {
		t.Fatalf("emit = %d, %v", n, err)
	}
	labelsOf := func(res string) map[string]any {
		t.Helper()
		repo.mu.Lock()
		defer repo.mu.Unlock()
		for _, r := range repo.usage {
			if r.ResourceID == res {
				var m map[string]any
				if err := json.Unmarshal(r.Labels, &m); err != nil {
					t.Fatal(err)
				}
				return m
			}
		}
		t.Fatalf("no usage for %s", res)
		return nil
	}
	tagged := labelsOf("s1")
	tags, _ := tagged["tags"].(map[string]any)
	if tags["team"] != "platform" || tags["novalue"] != "" || len(tags) != 2 {
		t.Fatalf("tags label = %v", tagged["tags"])
	}
	if tagged["enterprise_project"] != "ep-1" {
		t.Fatalf("enterprise_project label = %v", tagged["enterprise_project"])
	}
	plain := labelsOf("s2")
	if _, ok := plain["tags"]; ok {
		t.Fatalf("untagged resource must carry no tags label: %v", plain)
	}
	if _, ok := plain["enterprise_project"]; ok {
		t.Fatalf("resource without enterprise project must carry no label: %v", plain)
	}
}
