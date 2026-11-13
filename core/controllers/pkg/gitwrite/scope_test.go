package gitwrite_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/internal/giteadouble"
)

const regionFQDN = "t2.example.org"

var regionRepo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions"}

// regionTokens gives each robot its own token, so a commit shows which robot made it.
type regionTokens struct{}

func (regionTokens) Token(_ context.Context, robot string) (string, error) {
	return "region-" + robot, nil
}

func regionSetup(t *testing.T) (*giteadouble.Server, *gitwrite.Client) {
	t.Helper()
	d := giteadouble.New()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	c, err := gitwrite.New(gitwrite.Config{BaseURL: srv.URL, Tokens: regionTokens{}, RobotDomain: "robots." + regionFQDN})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, c
}

// rp is a path in the folder of one region.
func rp(region, rest string) string {
	return "clusters/" + regionFQDN + "/regions/" + region + "/" + rest
}

func regionChange(s gitwrite.Scope, files ...gitwrite.FileChange) gitwrite.Change {
	return gitwrite.Change{Scope: s, Repo: regionRepo, Files: files, Summary: "region: test change", ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaAPI}
}

func regionUpsert(path, content string) gitwrite.FileChange {
	return gitwrite.FileChange{Path: path, Op: gitwrite.OpUpsert, Content: []byte(content)}
}

func regionFile(d *giteadouble.Server, path string) string {
	return string(d.Files("openova", "sovereign-regions", "main")[path])
}

// TestRegionPlatformScope: name, robot and prefixes; regions and modules
// sorted and counted once; an empty list or a bad name gives no prefix.
func TestRegionPlatformScope(t *testing.T) {
	s := gitwrite.RegionPlatform(regionFQDN, []string{"root", "policy", "root"}, []string{"r2", "r1"})
	want := []string{
		rp("r1", "platform/policy/"), rp("r1", "platform/root/"),
		rp("r2", "platform/policy/"), rp("r2", "platform/root/"),
	}
	if s.Name != "region-platform" || s.Robot != "catalyst-fleet-bot" || !reflect.DeepEqual(s.Prefixes, want) {
		t.Fatalf("scope = %+v", s)
	}
	for name, got := range map[string]gitwrite.Scope{
		"no region":     gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, nil),
		"no module":     gitwrite.RegionPlatform(regionFQDN, nil, []string{"r1"}),
		"bad region":    gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, []string{"", ".", "..", "r1/orgs"}),
		"bad module":    gitwrite.RegionPlatform(regionFQDN, []string{"", "..", "policy/x"}, []string{"r1"}),
		"bad sovereign": gitwrite.RegionPlatform("a/b", []string{"policy"}, []string{"r1"}),
		"no sovereign":  gitwrite.RegionPlatform("", []string{"policy"}, []string{"r1"}),
	} {
		if len(got.Prefixes) != 0 || got.Robot != "catalyst-fleet-bot" {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// TestRegionPlatformPaths: only platform/<listed module>/ of a listed
// region is written; everything else is refused before any network call.
func TestRegionPlatformPaths(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	s := gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, []string{"r1", "r2"})
	res, err := c.Commit(ctx, regionChange(s, regionUpsert(rp("r2", "platform/policy/mode.yaml"), "mode: audit\n")))
	if err != nil || !res.Committed {
		t.Fatalf("listed module of a listed region: %+v %v", res, err)
	}
	commits := d.Commits()
	if len(commits) != 1 || commits[0].CommitterName != "catalyst-fleet-bot" || commits[0].Token != "region-catalyst-fleet-bot" {
		t.Fatalf("commits = %+v", commits)
	}
	before := d.Calls("")
	for name, ch := range map[string]gitwrite.Change{
		"another region's orgs folder": regionChange(s, regionUpsert(rp("r1", "orgs/acme.yaml"), "a: 1\n")),
		"an unlisted region":           regionChange(s, regionUpsert(rp("r3", "platform/policy/mode.yaml"), "a: 1\n")),
		"an unlisted module":           regionChange(s, regionUpsert(rp("r1", "platform/root/vars.yaml"), "a: 1\n")),
		"a platform Application file":  regionChange(s, regionUpsert(rp("r1", "platform/policy.yaml"), "a: 1\n")),
		"the writer-only intent":       regionChange(s, regionUpsert("clusters/"+regionFQDN+"/intent/a.yaml", "a: 1\n")),
		"a climb out of the module":    regionChange(s, regionUpsert(rp("r1", "platform/policy/../../orgs/a.yaml"), "a: 1\n")),
		"one bad file of two":          regionChange(s, regionUpsert(rp("r1", "platform/policy/a.yaml"), "a: 1\n"), regionUpsert(rp("r1", "orgs/b.yaml"), "b: 1\n")),
		"an empty region list":         regionChange(gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, nil), regionUpsert(rp("r1", "platform/policy/a.yaml"), "a: 1\n")),
		"an empty module list":         regionChange(gitwrite.RegionPlatform(regionFQDN, nil, []string{"r1"}), regionUpsert(rp("r1", "platform/policy/a.yaml"), "a: 1\n")),
		"a delete outside the scope":   regionChange(s, gitwrite.FileChange{Path: rp("r1", "platform/cnpg-pair.yaml"), Op: gitwrite.OpDelete}),
	} {
		if _, err := c.Commit(ctx, ch); !errors.Is(err, gitwrite.ErrForbiddenPath) {
			t.Errorf("%s: got %v, want ErrForbiddenPath", name, err)
		}
	}
	if d.Calls("") != before || len(d.Commits()) != 1 {
		t.Fatalf("a refused change reached Gitea: %d calls, %d commits", d.Calls("")-before, len(d.Commits()))
	}
}

// TestRegionInfrastructure: one region's infrastructure intent is written
// into that region's folder only.
func TestRegionInfrastructure(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	s := gitwrite.RegionPlatform(regionFQDN, []string{"infrastructure"}, []string{"r2"})
	path := rp("r2", "platform/infrastructure/nodepools/workers.yaml")
	if _, err := c.Commit(ctx, regionChange(s, regionUpsert(path, "kind: NodePool\n"))); err != nil {
		t.Fatalf("this region: %v", err)
	}
	if regionFile(d, path) != "kind: NodePool\n" {
		t.Fatalf("file = %q", regionFile(d, path))
	}
	if _, err := c.Commit(ctx, regionChange(s, regionUpsert(rp("r1", "platform/infrastructure/nodepools/workers.yaml"), "kind: NodePool\n"))); !errors.Is(err, gitwrite.ErrForbiddenPath) {
		t.Fatalf("another region: got %v", err)
	}
}

// TestRegions: the directories under the regions folder, sorted; files
// are not regions; a missing folder is an empty list.
func TestRegions(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	if ids, err := gitwrite.Regions(ctx, c, regionRepo, regionFQDN); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("missing folder: %v %v", ids, err)
	}
	d.Put("openova", "sovereign-regions", "main", rp("r2", "orgs/acme.yaml"), []byte("a: 1\n"))
	d.Put("openova", "sovereign-regions", "main", rp("r1", "platform/cnpg-pair.yaml"), []byte("a: 1\n"))
	d.Put("openova", "sovereign-regions", "main", "clusters/"+regionFQDN+"/regions/README.md", []byte("x\n"))
	d.Put("openova", "sovereign-regions", "main", "clusters/other.example.org/regions/r9/orgs/a.yaml", []byte("a: 1\n"))
	ids, err := gitwrite.Regions(ctx, c, regionRepo, regionFQDN)
	if err != nil || !reflect.DeepEqual(ids, []string{"r1", "r2"}) {
		t.Fatalf("Regions = %v %v", ids, err)
	}
	before := d.Calls("")
	if _, err := gitwrite.Regions(ctx, c, regionRepo, "a/b"); !errors.Is(err, gitwrite.ErrInvalid) || d.Calls("") != before {
		t.Fatalf("bad FQDN: %v", err)
	}
	dir := "clusters/" + regionFQDN + "/regions/"
	unsorted := listOnly{entries: []gitwrite.Entry{{Path: dir + "r9", IsDir: true}, {Path: dir + "r10", IsDir: true}, {Path: dir + "r1", IsDir: true}}}
	if ids, err := gitwrite.Regions(ctx, unsorted, regionRepo, regionFQDN); err != nil || !reflect.DeepEqual(ids, []string{"r1", "r10", "r9"}) {
		t.Fatalf("Regions of an unsorted listing = %v %v", ids, err)
	}
}

// listOnly is a Writer whose List returns fixed entries in the order given.
type listOnly struct {
	gitwrite.Writer
	entries []gitwrite.Entry
}

func (l listOnly) List(context.Context, gitwrite.Scope, gitwrite.Repo, string) ([]gitwrite.Entry, error) {
	return l.entries, nil
}

// TestWriteAllRegionsOneCommit: the same files go into every listed region
// in exactly one commit; the result lists the copies region by region.
func TestWriteAllRegionsOneCommit(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	regions := []string{"r3", "r1", "r2", "r1"}
	ch := regionChange(gitwrite.RegionPlatform(regionFQDN, []string{"policy", "root"}, regions),
		regionUpsert("platform/policy/mode.yaml", "mode: audit\n"),
		regionUpsert("platform/root/vars.yaml", "vars: 1\n"))
	res, err := gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, ch)
	if err != nil || !res.Committed {
		t.Fatalf("WriteAllRegions: %+v %v", res, err)
	}
	want := []string{
		rp("r1", "platform/policy/mode.yaml"), rp("r1", "platform/root/vars.yaml"),
		rp("r2", "platform/policy/mode.yaml"), rp("r2", "platform/root/vars.yaml"),
		rp("r3", "platform/policy/mode.yaml"), rp("r3", "platform/root/vars.yaml"),
	}
	var got []string
	for _, f := range res.Files {
		got = append(got, f.Path)
		if f.BlobSHA == "" {
			t.Errorf("%s: no blob SHA", f.Path)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result paths = %v", got)
	}
	commits := d.Commits()
	if d.Calls("change") != 1 || len(commits) != 1 || !reflect.DeepEqual(commits[0].Paths, want) || commits[0].SHA != res.Commit {
		t.Fatalf("commits = %+v, batch calls = %d", commits, d.Calls("change"))
	}
	if !strings.HasPrefix(commits[0].Message, "region: test change\n\nChange-Id: "+ch.ChangeID+"\n") {
		t.Fatalf("message = %q", commits[0].Message)
	}
	for _, r := range []string{"r1", "r2", "r3"} {
		if regionFile(d, rp(r, "platform/policy/mode.yaml")) != "mode: audit\n" {
			t.Errorf("%s: mode file missing", r)
		}
	}
	if ch.Files[0].Path != "platform/policy/mode.yaml" || len(ch.Files) != 2 {
		t.Fatalf("the caller's change was modified: %+v", ch.Files)
	}
	again, err := gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, ch)
	if err != nil || again.Committed || d.Calls("change") != 1 {
		t.Fatalf("same content again: %+v %v", again, err)
	}
}

// TestWriteAllRegionsCompareAndSwap: IfMatch is checked on the copy in the
// first region in sorted order only; the other copies are overwritten.
func TestWriteAllRegionsCompareAndSwap(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	regions := []string{"r2", "r1"}
	s := gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, regions)
	d.Put("openova", "sovereign-regions", "main", rp("r1", "platform/policy/mode.yaml"), []byte("mode: audit\n"))
	d.Put("openova", "sovereign-regions", "main", rp("r2", "platform/policy/mode.yaml"), []byte("mode: drifted\n"))
	_, first, err := c.Read(ctx, s, regionRepo, rp("r1", "platform/policy/mode.yaml"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	stale := regionChange(s, gitwrite.FileChange{Path: "platform/policy/mode.yaml", Op: gitwrite.OpUpsert, Content: []byte("mode: enforce\n"), IfMatch: "0000000000000000000000000000000000000000"})
	_, err = gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, stale)
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Path != rp("r1", "platform/policy/mode.yaml") || ce.Current != first {
		t.Fatalf("stale IfMatch: got %v", err)
	}
	if len(d.Commits()) != 0 || regionFile(d, rp("r2", "platform/policy/mode.yaml")) != "mode: drifted\n" {
		t.Fatal("a refused change was applied")
	}
	fresh := regionChange(s, gitwrite.FileChange{Path: "platform/policy/mode.yaml", Op: gitwrite.OpUpsert, Content: []byte("mode: enforce\n"), IfMatch: first})
	if res, err := gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, fresh); err != nil || !res.Committed {
		t.Fatalf("current IfMatch: %+v %v", res, err)
	}
	for _, r := range []string{"r1", "r2"} {
		if regionFile(d, rp(r, "platform/policy/mode.yaml")) != "mode: enforce\n" {
			t.Errorf("%s: not written", r)
		}
	}
	absent := regionChange(s, gitwrite.FileChange{Path: "platform/policy/new.yaml", Op: gitwrite.OpUpsert, Content: []byte("n: 1\n"), IfMatch: gitwrite.AbsentSHA})
	d.Put("openova", "sovereign-regions", "main", rp("r2", "platform/policy/new.yaml"), []byte("n: 0\n"))
	if _, err := gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, absent); err != nil {
		t.Fatalf("AbsentSHA, file only in the second region: %v", err)
	}
	absent.Files[0].Content = []byte("n: 2\n")
	if _, err := gitwrite.WriteAllRegions(ctx, c, regionFQDN, regions, absent); !gitwrite.IsConflict(err) {
		t.Fatalf("AbsentSHA, file now in the first region: got %v", err)
	}
}

// TestWriteAllRegionsRefusals: no region or a bad name is ErrInvalid, a
// copy outside the scope is ErrForbiddenPath; none reaches Gitea.
func TestWriteAllRegionsRefusals(t *testing.T) {
	d, c := regionSetup(t)
	ctx := context.Background()
	s := gitwrite.RegionPlatform(regionFQDN, []string{"policy"}, []string{"r1"})
	ch := regionChange(s, regionUpsert("platform/policy/mode.yaml", "mode: audit\n"))
	for name, tc := range map[string]struct {
		fqdn    string
		regions []string
		ch      gitwrite.Change
		want    error
	}{
		"no region":              {regionFQDN, nil, ch, gitwrite.ErrInvalid},
		"a bad region ID":        {regionFQDN, []string{"r1", "r1/orgs"}, ch, gitwrite.ErrInvalid},
		"a bad Sovereign FQDN":   {"", []string{"r1"}, ch, gitwrite.ErrInvalid},
		"no file":                {regionFQDN, []string{"r1"}, regionChange(s), gitwrite.ErrInvalid},
		"a region not in scope":  {regionFQDN, []string{"r1", "r2"}, ch, gitwrite.ErrForbiddenPath},
		"a module not in scope":  {regionFQDN, []string{"r1"}, regionChange(s, regionUpsert("platform/root/vars.yaml", "v: 1\n")), gitwrite.ErrForbiddenPath},
		"a climb out of regions": {regionFQDN, []string{"r1"}, regionChange(s, regionUpsert("platform/policy/../../../r2/platform/policy/a.yaml", "a: 1\n")), gitwrite.ErrForbiddenPath},
		"an orgs file":           {regionFQDN, []string{"r1"}, regionChange(s, regionUpsert("orgs/acme.yaml", "a: 1\n")), gitwrite.ErrForbiddenPath},
	} {
		if _, err := gitwrite.WriteAllRegions(ctx, c, tc.fqdn, tc.regions, tc.ch); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	if d.Calls("") != 0 {
		t.Fatalf("a refused change reached Gitea: %d calls", d.Calls(""))
	}
}
