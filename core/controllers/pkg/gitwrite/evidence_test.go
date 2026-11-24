package gitwrite_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
)

// TestFleetEvidenceScope: name, robot and the one prefix of the record
// folder; a Sovereign FQDN that is not a valid name gives no prefix.
func TestFleetEvidenceScope(t *testing.T) {
	s := gitwrite.FleetEvidence(regionFQDN)
	if s.Name != "fleet-evidence" || s.Robot != "catalyst-fleet-bot" {
		t.Fatalf("scope = %+v", s)
	}
	if want := []string{"clusters/t2.example.org/evidence/"}; !reflect.DeepEqual(s.Prefixes, want) {
		t.Fatalf("prefixes = %q, want %q", s.Prefixes, want)
	}
	for _, fqdn := range []string{"", ".", "..", "a/b"} {
		if p := gitwrite.FleetEvidence(fqdn).Prefixes; len(p) != 0 {
			t.Errorf("FQDN %q: prefixes = %q, want none", fqdn, p)
		}
	}
}

// TestFleetEvidencePaths: a record under clusters/<fqdn>/evidence/ is
// committed by the fleet robot; every path outside that folder, and every
// path under a scope built from a bad FQDN, is refused before Gitea is
// called.
func TestFleetEvidencePaths(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	s := gitwrite.FleetEvidence(regionFQDN)
	record := "clusters/" + regionFQDN + "/evidence/posture/2026/2026-11-24.json"
	res, err := c.Commit(ctx, regionChange(s, regionUpsert(record, "{\"score\": 91}\n")))
	if err != nil || !res.Committed {
		t.Fatalf("a record in the evidence folder: %+v %v", res, err)
	}
	commits := d.Commits()
	if len(commits) != 1 || commits[0].CommitterName != "catalyst-fleet-bot" || commits[0].Token != "region-catalyst-fleet-bot" {
		t.Fatalf("commits = %+v", commits)
	}
	if got := regionFile(d, record); got != "{\"score\": 91}\n" {
		t.Fatalf("record content = %q", got)
	}
	before := d.Calls("")
	for name, ch := range map[string]gitwrite.Change{
		"a region's orgs folder":        regionChange(s, regionUpsert(rp("r1", "orgs/acme.yaml"), "a: 1\n")),
		"a region's policy folder":      regionChange(s, regionUpsert(rp("r1", "platform/policy/policy-values.yaml"), "a: 1\n")),
		"the writer-only intent":        regionChange(s, regionUpsert("clusters/"+regionFQDN+"/intent/a.yaml", "a: 1\n")),
		"another Sovereign's evidence":  regionChange(s, regionUpsert("clusters/t3.example.org/evidence/a.json", "{}\n")),
		"a sibling sharing the name":    regionChange(s, regionUpsert("clusters/"+regionFQDN+"/evidence-old/a.json", "{}\n")),
		"a climb out of the folder":     regionChange(s, regionUpsert("clusters/"+regionFQDN+"/evidence/../intent/a.yaml", "a: 1\n")),
		"one bad file of two":           regionChange(s, regionUpsert(record+".bak", "{}\n"), regionUpsert(rp("r1", "orgs/b.yaml"), "b: 1\n")),
		"a scope from an empty FQDN":    regionChange(gitwrite.FleetEvidence(""), regionUpsert("clusters/evidence/a.json", "{}\n")),
		"a scope from a climbing FQDN":  regionChange(gitwrite.FleetEvidence(".."), regionUpsert("clusters/evidence/a.json", "{}\n")),
		"a scope from an FQDN with a /": regionChange(gitwrite.FleetEvidence("a/b"), regionUpsert("clusters/a/b/evidence/a.json", "{}\n")),
	} {
		if _, err := c.Commit(ctx, ch); !errors.Is(err, gitwrite.ErrForbiddenPath) {
			t.Errorf("%s: got %v, want ErrForbiddenPath", name, err)
		}
	}
	if d.Calls("") != before || len(d.Commits()) != 1 {
		t.Fatalf("a refused change reached Gitea: %d calls, %d commits", d.Calls("")-before, len(d.Commits()))
	}
}
