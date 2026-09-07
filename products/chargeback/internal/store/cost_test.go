package store

import (
	"strings"
	"testing"
	"time"
)

// Tag dimensions without a database: the key is validated by IsTagDimension
// and BOUND by filteredCTE/groupExprs — the query text carries a placeholder,
// the args carry the key, and a key that fails the rule never gets that far.
func TestIsTagDimension(t *testing.T) {
	for name, wantKey := range map[string]string{
		"tag:team":                        "team",
		"tag:Env":                         "Env",
		"tag:cost-centre":                 "cost-centre",
		"tag:a.b/c:d@e_f":                 "a.b/c:d@e_f",
		"tag:" + strings.Repeat("k", 128): strings.Repeat("k", 128),
	} {
		key, ok := IsTagDimension(name)
		if !ok || key != wantKey {
			t.Fatalf("IsTagDimension(%q) = %q, %v", name, key, ok)
		}
	}
	for _, name := range []string{"team", "tag", "tag:", "tag:a b", "tag:te'am", `tag:te"am`, "tag:a;b", "tag:a=b", "tag:$1", "tag:" + strings.Repeat("k", 129), "Tag:team", "tags:team"} {
		if key, ok := IsTagDimension(name); ok {
			t.Fatalf("IsTagDimension(%q) accepted key %q", name, key)
		}
	}
	if !ValidTagKey("team") || ValidTagKey("te'am") || ValidTagKey("") {
		t.Fatal("ValidTagKey disagrees with IsTagDimension")
	}
}

func TestFilteredCTEBindsTagKeysAsParameters(t *testing.T) {
	from, to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	q := CostQuery{
		GroupBy: "tag:team",
		Include: map[string][]string{"tag:cost-centre": {"CC-42"}, "kind": {"ecs"}},
		Exclude: map[string][]string{"tag:env": {"dev"}},
	}
	sqlText, a, err := filteredCTE(q, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// Two window params, then (sorted) kind include, tag:cost-centre key +
	// values, tag:env key + values.
	if len(a.args) != 7 {
		t.Fatalf("args = %d: %v", len(a.args), a.args)
	}
	if a.args[3] != "cost-centre" || a.args[5] != "env" {
		t.Fatalf("tag keys must be bound as parameters: %v", a.args)
	}
	for _, key := range []string{"cost-centre", "env"} {
		if strings.Contains(sqlText, "'"+key+"'") || strings.Contains(sqlText, ">>'"+key) {
			t.Fatalf("tag key %q spliced into SQL: %s", key, sqlText)
		}
	}
	if !strings.Contains(sqlText, "COALESCE(u.labels->'tags'->>$4, '(untagged)') = ANY($5)") {
		t.Fatalf("include clause: %s", sqlText)
	}
	if !strings.Contains(sqlText, "NOT (COALESCE(u.labels->'tags'->>$6, '(untagged)') = ANY($7))") {
		t.Fatalf("exclude clause: %s", sqlText)
	}
	// group_by binds the key after the CTE args.
	grp, lbl, err := groupExprs(a, q.GroupBy)
	if err != nil {
		t.Fatal(err)
	}
	if grp != "COALESCE(tags->>$8, '(untagged)')" || lbl != grp || a.args[7] != "team" {
		t.Fatalf("group exprs = %q / %q args=%v", grp, lbl, a.args)
	}
	// The static dimension still resolves to its column.
	if grp, lbl, err := groupExprs(a, "enterprise_project"); err != nil || grp != "enterprise_project" || lbl != "enterprise_project" {
		t.Fatalf("enterprise_project = %q %q %v", grp, lbl, err)
	}

	// Invalid keys are errors before any SQL exists, in every position.
	for _, bad := range []string{"tag:te'am", "tag:", "tag:a b", "colour"} {
		if _, _, err := filteredCTE(CostQuery{Include: map[string][]string{bad: {"x"}}}, from, to); err == nil {
			t.Fatalf("include %q accepted", bad)
		}
		if _, _, err := filteredCTE(CostQuery{Exclude: map[string][]string{bad: {"x"}}}, from, to); err == nil {
			t.Fatalf("exclude %q accepted", bad)
		}
		if _, _, err := groupExprs(&costArgs{}, bad); err == nil {
			t.Fatalf("group_by %q accepted", bad)
		}
	}
	// A hostile VALUE is data: it lands in the args, not in the text.
	sqlText, a, err = filteredCTE(CostQuery{Include: map[string][]string{"tag:team": {"a' OR 1=1--"}}}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sqlText, "1=1") {
		t.Fatalf("value spliced into SQL: %s", sqlText)
	}
	if len(a.args) != 4 {
		t.Fatalf("args = %v", a.args)
	}
}

func TestCostQueryTagDimensions(t *testing.T) {
	q := CostQuery{GroupBy: "tag:team", Include: map[string][]string{"tag:env": {"prod"}, "kind": {"ecs"}, "tag:bad key": {"x"}}, Exclude: map[string][]string{"tag:team": {"a"}}}
	if got := q.TagDimensions(); len(got) != 2 || got[0] != "tag:env" || got[1] != "tag:team" {
		t.Fatalf("TagDimensions = %v", got)
	}
	if got := (CostQuery{GroupBy: "kind"}).TagDimensions(); len(got) != 0 {
		t.Fatalf("no tags → %v", got)
	}
	// CostDimensions is the static list — tags are not in it, enterprise_project is.
	dims := strings.Join(CostDimensions(), ",")
	if strings.Contains(dims, "tag:") || !strings.Contains(dims, "enterprise_project") {
		t.Fatalf("CostDimensions = %s", dims)
	}
}
