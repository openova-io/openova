package suite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

const (
	bornLocalFQDN = "t99.example.test"
	bornLocalOwn  = "http://gitea-http.gitea.svc.cluster.local:3000/openova/sovereign-regions"
	bornLocalCat  = "http://gitea-http.gitea.svc.cluster.local:3000/openova/openova"
	bornLocalRev  = "main@sha1:4f2a9c1b0d4e"
	// bornLocalFile is a registry file as a provider module writes it, for
	// LOCAL_REGISTRY_HOST harbor.t99.example.test while the Sovereign consumes
	// its origin. Its configs hold a login that no evidence may show.
	bornLocalFile = `{"configs":{"harbor.openova.io":{"auth":{"password":"robot-secret","username":"robot$openova-bot"}}},` +
		`"mirrors":{"*":{"endpoint":["https://harbor.t99.example.test"]},"docker.io":{"endpoint":["https://harbor.t99.example.test",` +
		`"https://harbor.openova.io"],"rewrite":{"(.*)":"proxy-dockerhub/$1"}},"harbor.t99.example.test":{"endpoint":["https://harbor.t99.example.test"]}}}`
	// bornLocalBlock is bornLocalFile in the block form of YAML, written by hand.
	bornLocalBlock = `---
# the registry file of a provider module, in block form
configs:
  harbor.openova.io:
    auth:
      password: robot-secret
      username: robot$openova-bot
mirrors:
  "*":
    endpoint:
      - https://harbor.t99.example.test
  docker.io:
    endpoint:
    - "https://harbor.t99.example.test"
    - 'https://harbor.openova.io'  # the origin, last
    rewrite:
      "(.*)": proxy-dockerhub/$1
  harbor.t99.example.test:
    endpoint: ["https://harbor.t99.example.test"]
`
	// bornLocalShort and bornLocalLong bound the repeated pass in the tests: a
	// pass that is repeated in bornLocalLong shows as more than one pass.
	bornLocalShort, bornLocalLong = 40 * time.Millisecond, 5 * time.Second
)

// bornLocalTest is the check that reads through the fake kubectl and repeats
// step 3 for up to settle.
func bornLocalTest(kubectl kubectlFunc, settle time.Duration) bornLocalCheck {
	return bornLocalCheck{kubectl: kubectl, get: kube.Region.GetList, settle: settle, every: 10 * time.Millisecond}
}

var bornLocalArgs = []string{"server", "--cluster-init", bornLocalFlag, "--node-ip", "10.0.0.2"}

// bornLocalNode is a Node whose k3s started with args; nil args: no annotation.
func bornLocalNode(name string, args []string) obj {
	meta := kubeMeta("", name)
	if args != nil {
		b, _ := json.Marshal(args)
		meta["annotations"] = obj{"k3s.io/node-args": string(b)}
	}
	return obj{"metadata": meta}
}

// bornLocalVars is the ConfigMap list of flux-system: sovereign-variables, less the keys drop.
func bornLocalVars(registry string, drop ...string) string {
	d := obj{"SOVEREIGN_FQDN": bornLocalFQDN, "LOCAL_REGISTRY_HOST": registry}
	for _, k := range drop {
		delete(d, k)
	}
	return kubeList(obj{"metadata": kubeMeta("flux-system", "sovereign-variables"), "data": d})
}

// bornLocalRepos are the GitRepositories of a region: the Sovereign's own repository, the catalogue at catalogue, and one Organization's.
func bornLocalRepos(catalogue string) string {
	repo := func(ns, name, url string) obj {
		return obj{"metadata": kubeMeta(ns, name), "spec": obj{"url": url}, "status": obj{"artifact": obj{"revision": bornLocalRev}}}
	}
	return kubeList(repo("flux-system", "sovereign-region", bornLocalOwn), repo("flux-system", "openova", catalogue),
		repo("org-acme", "acme", "https://gitea.hfmp."+bornLocalFQDN+"/acme/acme.git"))
}

// bornLocalKsList are the Kustomizations of flux-system: owner (its source is source)
// and the roots it applied, but skip. edit changes the one it names.
func bornLocalKsList(owner, source, skip string, edit map[string]func(o obj)) string {
	k := func(name, by, src string) obj {
		meta := kubeMeta("flux-system", name)
		if by != "" {
			meta["labels"] = obj{"kustomize.toolkit.fluxcd.io/name": by, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
		}
		o := obj{"metadata": meta, "spec": obj{"sourceRef": obj{"kind": "GitRepository", "name": src}},
			"status": obj{"conditions": kubeConditions("Ready", "True"), "lastAppliedRevision": bornLocalRev}}
		if edit[name] != nil {
			edit[name](o)
		}
		return o
	}
	items := []obj{k(owner, "", source)}
	for _, n := range bornLocalRoots {
		if n != skip {
			items = append(items, k(n, owner, "openova"))
		}
	}
	return kubeList(items...)
}

func bornLocalNotReady(o obj) {
	o["status"].(obj)["conditions"] = []obj{{"type": "Ready", "status": "False", "message": "health check failed"}}
}

func init() {
	scenario := func(change map[string]string) map[string]string {
		s := map[string]string{
			"* nodes":                           kubeList(bornLocalNode("node-1", bornLocalArgs), bornLocalNode("node-2", []string{"agent", bornLocalFlag})),
			"* configmaps flux-system":          bornLocalVars("harbor.t99.example.test"),
			"* " + bornLocalGit:                 bornLocalRepos(bornLocalCat),
			"* " + bornLocalKs + " flux-system": bornLocalKsList("sovereign-region", "sovereign-region", "", nil),
			"b " + bornLocalKs + " flux-system": bornLocalKsList("sovereign-region", "sovereign-region", bornLocalPrimaryOnly, nil),
		}
		for k, v := range change {
			s[k] = v
		}
		return s
	}
	ks := "a " + bornLocalKs + " flux-system"
	parent := "https://github.com/openova-io/openova"
	for name, change := range map[string]map[string]string{
		"":                 nil,
		"-no-flag":         {"b nodes": kubeList(bornLocalNode("node-1", bornLocalArgs), bornLocalNode("node-2", []string{"agent", "--node-label", "x=y"}))},
		"-flag-false":      {"a nodes": kubeList(bornLocalNode("node-1", []string{"server", bornLocalFlag, "false"}), bornLocalNode("node-2", bornLocalArgs))},
		"-no-annotation":   {"a nodes": kubeList(bornLocalNode("node-1", bornLocalArgs), bornLocalNode("node-2", nil))},
		"-no-node":         {"b nodes": kubeList()},
		"-parent-source":   {"a " + bornLocalGit: bornLocalRepos(parent)},
		"-lookalike-host":  {"a " + bornLocalGit: bornLocalRepos("https://evilt99.example.test/openova/openova")},
		"-no-fqdn":         {"a configmaps flux-system": bornLocalVars("harbor.t99.example.test", "SOVEREIGN_FQDN")},
		"-no-repository":   {"b " + bornLocalGit: kubeList()},
		"-today":           {"a " + bornLocalGit: bornLocalRepos(parent), ks: bornLocalKsList("sovereign-root", "openova", "", nil)},
		"-catalogue-owner": {ks: bornLocalKsList("sovereign-root", "openova", "", nil)},
		"-by-hand": {ks: bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){
			"bootstrap-kit": func(o obj) { delete(o["metadata"].(obj), "labels") }})},
		"-owner-elsewhere": {ks: bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){
			"sovereign-tls": func(o obj) { o["metadata"].(obj)["labels"].(obj)["kustomize.toolkit.fluxcd.io/namespace"] = "org-acme" }})},
		"-root-not-ready": {"b " + bornLocalKs + " flux-system": bornLocalKsList("sovereign-region", "sovereign-region", bornLocalPrimaryOnly,
			map[string]func(obj){"sovereign-tls": bornLocalNotReady})},
		"-behind": {ks: bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){
			"sovereign-region": func(o obj) { o["status"].(obj)["lastAppliedRevision"] = "main@sha1:0000" }})},
		"-owner-not-ready": {ks: bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){"sovereign-region": bornLocalNotReady})},
		"-primary-only":    {ks: bornLocalKsList("sovereign-region", "sovereign-region", bornLocalPrimaryOnly, nil)},
		"-no-variables":    {"b configmaps flux-system": kubeList()},
		"-no-registry":     {"a configmaps flux-system": bornLocalVars("", "LOCAL_REGISTRY_HOST")},
		"-other-registry":  {"b configmaps flux-system": bornLocalVars("harbor-b.t99.example.test")},
	} {
		fakeAnswers["born-local"+name] = scenario(change)
	}
}

// bornLocalHost writes the registry file of a node; "" writes none.
func bornLocalHost(file string) func(*testing.T, string, []string) {
	return func(t *testing.T, root string, _ []string) {
		if file != "" {
			hostFile(t, root, "etc/rancher/k3s/registries.yaml", 0o600, []byte(file))
		}
	}
}

func TestBornLocalCheck(t *testing.T) {
	if c, ok := registry[bornLocalID].(bornLocalCheck); !ok || bornLocalID != "SOV-G01" || c.kubectl == nil || c.get == nil || c.settle != 6*time.Minute || c.every != 15*time.Second {
		t.Fatalf("the registry must hold %s with kubectl, the reads of the Flux objects and step 3 read again every 15s for up to 6m", bornLocalID)
	}
	// Only an owner that is not Ready or behind its source settles; every other fault fails on the first pass.
	settles := map[string]bool{"the owner is behind": true, "the owner is not Ready": true}
	if _, fault, settling := bornLocalOwner(nil, nil, "sovereign-region", bornLocalFQDN); fault == "" || settling {
		t.Errorf("an owner that is no Kustomization of flux-system must fail at once, got %q (settling %t)", fault, settling)
	}
	times := func(n int, s string) []string { return strings.Split(strings.Repeat(s+"\n", n), "\n")[:n] }
	owner := "the owner sovereign-region of [bootstrap-kit bootstrap-kit-crs sovereign-tls infrastructure-providers] "
	today := "region region-a: the owner sovereign-root of [bootstrap-kit bootstrap-kit-crs sovereign-tls infrastructure-providers] reads "
	for name, c := range map[string]struct {
		scenario, file string
		want           []string // the errors of the failed steps, in order; none: PASS
	}{
		"born local":                {"born-local", bornLocalFile, nil},
		"a node lacks the flag":     {"born-local-no-flag", bornLocalFile, []string{"region region-b: k3s.io/node-args of the nodes [node-2] does not hold --disable-default-registry-endpoint"}},
		"the flag is set false":     {"born-local-flag-false", bornLocalFile, []string{"region region-a: k3s.io/node-args of the nodes [node-1] does not hold"}},
		"a node has no annotation":  {"born-local-no-annotation", bornLocalFile, []string{"region region-a: k3s.io/node-args of the nodes [node-2] does not hold"}},
		"a region lists no node":    {"born-local-no-node", bornLocalFile, []string{"region region-b lists no node"}},
		"the catalogue is outside":  {"born-local-parent-source", bornLocalFile, []string{"region region-a: GitRepositories read from outside the Sovereign: [flux-system/openova https://github.com/openova-io/openova]"}},
		"a host only ends like it":  {"born-local-lookalike-host", bornLocalFile, []string{"[flux-system/openova https://evilt99.example.test/openova/openova]"}},
		"no SOVEREIGN_FQDN":         {"born-local-no-fqdn", bornLocalFile, []string{"sovereign-variables of region region-a has no SOVEREIGN_FQDN to judge the hosts by"}},
		"no GitRepository":          {"born-local-no-repository", bornLocalFile, []string{"region region-b: Flux has no GitRepository, not even the Sovereign's own repository", "region region-b: the owner sovereign-region of [bootstrap-kit bootstrap-kit-crs sovereign-tls] reads the GitRepository flux-system/sovereign-region, which is no GitRepository of the region"}},
		"today's first-boot root":   {"born-local-today", bornLocalFile, []string{"region region-a: GitRepositories read from outside", today + "https://github.com/openova-io/openova, not the Sovereign's own repository"}},
		"the owner reads the kit":   {"born-local-catalogue-owner", bornLocalFile, []string{today + bornLocalCat + ", not the Sovereign's own repository"}},
		"a root applied by hand":    {"born-local-by-hand", bornLocalFile, []string{"region region-a: bootstrap-kit is not owned by a Kustomization of flux-system"}},
		"a root owned elsewhere":    {"born-local-owner-elsewhere", bornLocalFile, []string{"region region-a: sovereign-tls is not owned by a Kustomization of flux-system"}},
		"a root is not Ready":       {"born-local-root-not-ready", bornLocalFile, []string{"region region-b: sovereign-tls is not Ready (health check failed)"}},
		"the owner is behind":       {"born-local-behind", bornLocalFile, []string{"region region-a: " + owner + `applied the revision "main@sha1:0000", its source holds "main@sha1:4f2a9c1b0d4e"`}},
		"the owner is not Ready":    {"born-local-owner-not-ready", bornLocalFile, []string{"region region-a: " + owner + "is not Ready (health check failed)"}},
		"the primary lacks a root":  {"born-local-primary-only", bornLocalFile, []string{"region region-a: no Kustomization flux-system/infrastructure-providers"}},
		"no variables":              {"born-local-no-variables", bornLocalFile, times(3, "region region-b has no ConfigMap flux-system/sovereign-variables")},
		"no LOCAL_REGISTRY_HOST":    {"born-local-no-registry", bornLocalFile, []string{"sovereign-variables of region region-a has no LOCAL_REGISTRY_HOST"}},
		"another region's registry": {"born-local-other-registry", bornLocalFile, times(2, "the first endpoint is not https://harbor-b.t99.example.test for [* docker.io harbor.t99.example.test]")},
		"the origin is first":       {"born-local", strings.Replace(bornLocalFile, `["https://harbor.t99.example.test","https://harbor.openova.io"]`, `["https://harbor.openova.io","https://harbor.t99.example.test"]`, 1), times(4, "the first endpoint is not https://harbor.t99.example.test for [docker.io]")},
		"no entry for every name":   {"born-local", strings.Replace(bornLocalFile, `"*":{"endpoint":["https://harbor.t99.example.test"]},`, "", 1), times(4, `the registry file has no entry "*"`)},
		"a YAML file judged":        {"born-local", "mirrors:\n  docker.io:\n    endpoint:\n      - \"https://mirror.gcr.io\"\n", times(4, `the registry file has no entry "*", so a name it does not list does not go to the region's own registry; the first endpoint is not https://harbor.t99.example.test for [docker.io]`)},
		"a file with no name":       {"born-local", `{"mirrors":{}}`, times(4, "the registry file lists no name")},
		"no registry file":          {"born-local", "", times(4, "the node has no readable /etc/rancher/k3s/registries.yaml")},
	} {
		t.Run(name, func(t *testing.T) {
			f := &probeFake{t: t, nodes: "node-1 node-2", host: bornLocalHost(c.file)}
			r := runCheck(t, c.scenario, bornLocalTest(f.kubectl, bornLocalShort), &CheckContext{})
			got := failedSteps(r)
			ok := len(got) == len(c.want) && (r.run.Checks[0].Status == Pass) == (len(c.want) == 0)
			for i := 0; ok && i < len(got); i++ {
				ok = strings.Contains(got[i], c.want[i]) && strings.Contains(got[i], "timed out after") == settles[name]
			}
			if !ok {
				t.Errorf("want the failed steps %q, got %q (status %s)", c.want, got, r.run.Checks[0].Status)
			}
			// Per region three steps, and the probe's start, one per node and its end;
			// a region whose probe could not start has no step per node.
			steps := map[string]int{"no variables": 12, "no LOCAL_REGISTRY_HOST": 12}[name]
			if steps == 0 {
				steps = 14
			}
			if len(r.run.Steps) != steps || len(f.created) != 2 || strings.Join(f.created, " ") != strings.Join(f.deleted, " ") {
				t.Errorf("want %d steps and two namespaces created and deleted, got %d steps, %v and %v", steps, len(r.run.Steps), f.created, f.deleted)
			}
		})
	}
}

func TestBornLocalEvidence(t *testing.T) {
	f := &probeFake{t: t, nodes: "node-1 node-2", host: bornLocalHost(bornLocalFile)}
	selectors := []string{}
	kubectl := func(region kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		if len(args) > 3 && args[0] == "get" && args[1] == "nodes" && args[2] == "-l" {
			selectors = append(selectors, args[3])
		}
		return f.kubectl(region, ctx, stdin, args...)
	}
	r := runCheck(t, "born-local", bornLocalTest(kubectl, bornLocalShort), &CheckContext{})
	if strings.Join(selectors, " ") != "kubernetes.io/hostname kubernetes.io/hostname" {
		t.Errorf("the probe must start on every node of each region, by the label kubernetes.io/hostname; it asked for %q", selectors)
	}
	b, err := json.Marshal(r.run.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "robot-secret") || strings.Contains(string(b), "configs") {
		t.Errorf("the evidence holds the configs of the registry file: %s", b)
	}
	for i, want := range map[int]string{
		0: `"without_flag":[]`, 1: `"org-acme/acme":"https://gitea.hfmp.t99.example.test/acme/acme.git"`,
		2: `"owners":{"sovereign-region":"` + bornLocalOwn + `"},"passes":1`, 7: `"docker.io":["https://harbor.t99.example.test","https://harbor.openova.io"]`,
	} {
		if e, _ := json.Marshal(r.run.Steps[i].Evidence); !strings.Contains(string(e), want) {
			t.Errorf("the evidence of step %s lacks %s: %s", r.run.Steps[i].ID, want, e)
		}
	}
}

// bornLocalReads answers the Flux reads of region-a in order: the i-th read
// of a resource gets its i-th list, and the last list after that. The reads
// of region-b go to the fake kubectl.
type bornLocalReads struct {
	lists map[string][]string // resource -> lists
	reads map[string]int
}

func (s *bornLocalReads) get(r kube.Region, ctx context.Context, resource, ns string, extra ...string) ([]kube.Object, error) {
	lists := s.lists[resource]
	if r.ID != "region-a" {
		return r.GetList(ctx, resource, ns, extra...)
	}
	text := lists[min(s.reads[resource], len(lists)-1)]
	s.reads[resource]++
	var l kube.List
	err := json.Unmarshal([]byte(text), &l)
	return l.Items, err
}

func TestBornLocalSettles(t *testing.T) {
	const next = "main@sha1:9b1e7d3c2a10"
	owner := func(edit func(o obj)) string {
		return bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){"sovereign-region": edit})
	}
	applied := func(rev string) func(o obj) { return func(o obj) { o["status"].(obj)["lastAppliedRevision"] = rev } }
	repos, moved := bornLocalRepos(bornLocalCat), strings.ReplaceAll(bornLocalRepos(bornLocalCat), bornLocalRev, next)
	empty := strings.ReplaceAll(repos, `"status":{"artifact":{"revision":"`+bornLocalRev+`"}}`, `"status":{}`)
	byHand := bornLocalKsList("sovereign-region", "sovereign-region", "", map[string]func(obj){"sovereign-region": applied(next),
		"bootstrap-kit": func(o obj) { delete(o["metadata"].(obj), "labels") }})
	behind := "region region-a: the owner sovereign-region of [bootstrap-kit bootstrap-kit-crs sovereign-tls infrastructure-providers] applied the revision "
	for name, c := range map[string]struct {
		ks, repos []string // the lists of region-a, in the order of the reads
		settle    time.Duration
		passes    int    // of step 3 in region-a; 0: more than one
		want      string // the error of that step; "": it passes
	}{
		"a commit lands during the run":      {[]string{owner(applied(bornLocalRev)), owner(applied(next))}, []string{repos, moved}, bornLocalLong, 2, ""},
		"the source moved after step 2":      {[]string{owner(applied(next))}, []string{repos, moved}, bornLocalLong, 1, ""},
		"the owner becomes Ready":            {[]string{owner(bornLocalNotReady), owner(nil)}, []string{repos}, bornLocalLong, 2, ""},
		"the owner stays behind":             {[]string{owner(applied(bornLocalRev))}, []string{moved}, 200 * time.Millisecond, 0, "timed out after 200ms; last observed: " + behind + `"main@sha1:4f2a9c1b0d4e", its source holds "main@sha1:9b1e7d3c2a10"`},
		"an owner behind and a root by hand": {[]string{byHand}, []string{repos}, bornLocalLong, 1, "region region-a: bootstrap-kit is not owned by a Kustomization of flux-system; the owner sovereign-region of [bootstrap-kit-crs sovereign-tls infrastructure-providers] applied the revision"},
		"the source holds no artifact":       {[]string{owner(nil)}, []string{empty}, bornLocalLong, 1, behind + `"main@sha1:4f2a9c1b0d4e", its source holds ""`},
		"a read fails":                       {[]string{"not JSON"}, []string{repos}, bornLocalLong, 1, "invalid character"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &bornLocalReads{lists: map[string][]string{bornLocalKs: c.ks, bornLocalGit: c.repos}, reads: map[string]int{}}
			check := bornLocalTest((&probeFake{t: t, nodes: "node-1 node-2", host: bornLocalHost(bornLocalFile)}).kubectl, c.settle)
			check.get = s.get
			r := runCheck(t, "born-local", check, &CheckContext{})
			steps := map[string]*Step{}
			for _, st := range r.run.Steps {
				steps[st.ID] = st
			}
			a, b := steps[bornLocalID+".3"], steps[bornLocalID+".6"]
			passes, _ := a.Evidence["passes"].(int)
			if (c.passes == 0 && passes < 2) || (c.passes > 0 && passes != c.passes) || b.Evidence["passes"] != 1 {
				t.Errorf("want %d passes of step 3 in region-a (0: more than one) and one in region-b, got %v and %v", c.passes, a.Evidence["passes"], b.Evidence["passes"])
			}
			if got := failedSteps(r); (c.want == "" && len(got) > 0) || (c.want != "" && (len(got) != 1 || a.Error != got[0] || !strings.Contains(a.Error, c.want))) {
				t.Errorf("want step 3 of region-a to fail with %q and no other step to fail, got %q", c.want, got)
			}
			if (r.run.Checks[0].Status == Pass) != (c.want == "") {
				t.Errorf("want the check to pass: %v, got %s", c.want == "", r.run.Checks[0].Status)
			}
		})
	}
}

// TestBornLocalYAML reads the forms the registry file comes in and refuses
// those the check does not read; an error never shows the text of a line.
func TestBornLocalYAML(t *testing.T) {
	for name, c := range map[string]struct{ in, want, err string }{
		"the provider modules' JSON": {bornLocalFile, bornLocalFile, ""},
		"the same in block form":     {bornLocalBlock, bornLocalFile, ""},
		"the forms it reads": {"--- \n# head\na: 'it''s'  # tail\nb:\n- x\n- k: v\n  l: \"w\\u0021\"\n- - y\n-\nc: ~\nd:\ne: [\"j\", 1]\n",
			`{"a":"it's","b":["x",{"k":"v","l":"w!"},["y"],null],"c":null,"d":null,"e":["j",1]}`, ""},
		"a tab":                  {"mirrors:\n\tdocker.io: {}\n", "", "line 2 is indented with a tab"},
		"an alias":               {"mirrors:\n  docker.io: *origin\n", "", "line 2 holds a value in a YAML form this check does not read"},
		"an anchored key":        {"&m mirrors: {}\n", "", "line 1 has a key in a YAML form this check does not read"},
		"a block scalar":         {"mirrors: |\n  docker.io\n", "", "line 1 holds a value in a YAML form this check does not read"},
		"a flow list not JSON":   {"mirrors:\n  docker.io:\n    endpoint: [a, b]\n", "", "line 3 starts a flow collection that is not JSON"},
		"a repeated key":         {"mirrors:\n  docker.io: {}\n  docker.io: {}\n", "", "line 3 is no new key of the mapping it is in"},
		"a line under no key":    {"mirrors:\n  docker.io: {}\n    endpoint: []\n", "", "line 3 is indented under no key"},
		"a line out of its list": {"a:\n    - x\n  - y\n", "", "line 3 is indented under no key"},
		"a line left of the top": {"  a: x\nb: y\n", "", "line 2 is outside the structure above it"},
		"a second document":      {"mirrors: {}\n---\nmirrors: {}\n", "", "line 2 is no new key of the mapping it is in"},
		"a mapping as a scalar":  {"mirrors:\n  docker.io:\n    endpoint: a: b\n", "", "line 3 holds a mapping where a scalar belongs"},
		"a quote left open":      {"configs:\n  harbor.openova.io:\n    auth:\n      password: \"robot-secret\n", "", "line 4 holds a quoted scalar that does not end on it"},
		"broken JSON":            {`{"mirrors":{"docker.io":`, "", "line 1 starts a flow collection that is not JSON"},
		"no document":            {"---\n# nothing\n\n", "", "it holds no document"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, err := bornLocalYAML([]byte(c.in))
			got, _ := json.Marshal(tree)
			switch {
			case c.err == "" && (err != nil || string(got) != c.want):
				t.Errorf("want %s, got %s (%v)", c.want, got, err)
			case c.err != "" && (err == nil || err.Error() != c.err || tree != nil):
				t.Errorf("want the error %q and nothing read, got %v and %s", c.err, err, got)
			}
		})
	}
}

// TestBornLocalJudgesTheFileByItsContent judges registry files in block form:
// the one the local pack writes (MIRROR of scripts/local-pack.sh), the
// provider modules' file, and a form the check does not read.
func TestBornLocalJudgesTheFileByItsContent(t *testing.T) {
	src, err := os.ReadFile("../../../../../scripts/local-pack.sh")
	if err != nil {
		t.Fatal(err)
	}
	pack := regexp.MustCompile(`(?m)^MIRROR='([^']*)'$`).FindSubmatch(src)
	if pack == nil {
		t.Fatal("scripts/local-pack.sh has no line MIRROR='...")
	}
	own := `["https://harbor.t99.example.test"]`
	for name, c := range map[string]struct{ file, err, mirrors string }{
		"the local pack's file": {string(pack[1]), `the registry file has no entry "*", so a name it does not list does not go to the region's own registry; ` +
			"the first endpoint is not https://harbor.t99.example.test for [docker.io harbor.openova.io]",
			`{"docker.io":["https://mirror.gcr.io"],"harbor.openova.io":["https://ghcr.io","https://quay.io","https://registry.k8s.io","https://mirror.gcr.io","https://registry-1.docker.io"]}`},
		"the provider modules' file": {bornLocalBlock, "",
			`{"*":` + own + `,"docker.io":["https://harbor.t99.example.test","https://harbor.openova.io"],"harbor.t99.example.test":` + own + `}`},
		"a form it does not read": {strings.Replace(bornLocalBlock, "  docker.io:\n", "  docker.io: &origin\n", 1),
			"/etc/rancher/k3s/registries.yaml is not YAML this check reads: line 12 holds a value in a YAML form this check does not read", "null"},
	} {
		t.Run(name, func(t *testing.T) {
			ev := Evidence{}
			err := judgeBornLocalRegistry(probeValues{"own": {"harbor.t99.example.test"}, "registries": {base64.StdEncoding.EncodeToString([]byte(c.file))}}, ev)
			mirrors, _ := json.Marshal(ev["mirrors"])
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != c.err || string(mirrors) != c.mirrors {
				t.Errorf("want the error %q and the mirrors %s, got %q and %s", c.err, c.mirrors, got, mirrors)
			}
		})
	}
}

// TestBornLocalRootsAreTheSeededOnes keeps bornLocalRoots equal to the list seed-repo commits.
func TestBornLocalRootsAreTheSeededOnes(t *testing.T) {
	src, err := os.ReadFile("../../../../../core/cmd/sovereign-ops/internal/seedrepo/seedrepo.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^var RootKustomizations = \[\]string\{([^}]*)\}$`).FindSubmatch(src)
	want, _ := json.Marshal(bornLocalRoots)
	if m == nil || "["+string(m[1])+"]" != strings.ReplaceAll(string(want), ",", ", ") {
		t.Errorf("RootKustomizations of seedrepo.go is %q; bornLocalRoots is %s", m, want)
	}
}
