package suite

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

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
)

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
	if _, ok := registry[bornLocalID]; !ok || bornLocalID != "SOV-G01" {
		t.Fatalf("check %s is not registered", bornLocalID)
	}
	times := func(n int, s string) []string { return strings.Split(strings.Repeat(s+"\n", n), "\n")[:n] }
	owner := "the owner sovereign-region of [bootstrap-kit bootstrap-kit-crs sovereign-tls infrastructure-providers] "
	today := "region region-a: the owner sovereign-root of [bootstrap-kit bootstrap-kit-crs sovereign-tls infrastructure-providers] reads "
	for name, c := range map[string]struct {
		scenario, file string
		want           []string // the errors of the failed steps, in order; none: PASS
	}{
		"born local":                 {"born-local", bornLocalFile, nil},
		"a node lacks the flag":      {"born-local-no-flag", bornLocalFile, []string{"region region-b: k3s.io/node-args of the nodes [node-2] does not hold --disable-default-registry-endpoint"}},
		"the flag is set false":      {"born-local-flag-false", bornLocalFile, []string{"region region-a: k3s.io/node-args of the nodes [node-1] does not hold"}},
		"a node has no annotation":   {"born-local-no-annotation", bornLocalFile, []string{"region region-a: k3s.io/node-args of the nodes [node-2] does not hold"}},
		"a region lists no node":     {"born-local-no-node", bornLocalFile, []string{"region region-b lists no node"}},
		"the catalogue is outside":   {"born-local-parent-source", bornLocalFile, []string{"region region-a: GitRepositories read from outside the Sovereign: [flux-system/openova https://github.com/openova-io/openova]"}},
		"a host only ends like it":   {"born-local-lookalike-host", bornLocalFile, []string{"[flux-system/openova https://evilt99.example.test/openova/openova]"}},
		"no SOVEREIGN_FQDN":          {"born-local-no-fqdn", bornLocalFile, []string{"sovereign-variables of region region-a has no SOVEREIGN_FQDN to judge the hosts by"}},
		"no GitRepository":           {"born-local-no-repository", bornLocalFile, []string{"region region-b: Flux has no GitRepository, not even the Sovereign's own repository", "region region-b: the owner sovereign-region of [bootstrap-kit bootstrap-kit-crs sovereign-tls] reads the GitRepository flux-system/sovereign-region, which is no GitRepository of the region"}},
		"today's first-boot root":    {"born-local-today", bornLocalFile, []string{"region region-a: GitRepositories read from outside", today + "https://github.com/openova-io/openova, not the Sovereign's own repository"}},
		"the owner reads the kit":    {"born-local-catalogue-owner", bornLocalFile, []string{today + bornLocalCat + ", not the Sovereign's own repository"}},
		"a root applied by hand":     {"born-local-by-hand", bornLocalFile, []string{"region region-a: bootstrap-kit is not owned by a Kustomization of flux-system"}},
		"a root owned elsewhere":     {"born-local-owner-elsewhere", bornLocalFile, []string{"region region-a: sovereign-tls is not owned by a Kustomization of flux-system"}},
		"a root is not Ready":        {"born-local-root-not-ready", bornLocalFile, []string{"region region-b: sovereign-tls is not Ready (health check failed)"}},
		"the owner is behind":        {"born-local-behind", bornLocalFile, []string{"region region-a: " + owner + `applied the revision "main@sha1:0000", its source holds "main@sha1:4f2a9c1b0d4e"`}},
		"the owner is not Ready":     {"born-local-owner-not-ready", bornLocalFile, []string{"region region-a: " + owner + "is not Ready (health check failed)"}},
		"the primary lacks a root":   {"born-local-primary-only", bornLocalFile, []string{"region region-a: no Kustomization flux-system/infrastructure-providers"}},
		"no variables":               {"born-local-no-variables", bornLocalFile, times(3, "region region-b has no ConfigMap flux-system/sovereign-variables")},
		"no LOCAL_REGISTRY_HOST":     {"born-local-no-registry", bornLocalFile, []string{"sovereign-variables of region region-a has no LOCAL_REGISTRY_HOST"}},
		"another region's registry":  {"born-local-other-registry", bornLocalFile, times(2, "the first endpoint is not https://harbor-b.t99.example.test for [* docker.io harbor.t99.example.test]")},
		"the origin is first":        {"born-local", strings.Replace(bornLocalFile, `["https://harbor.t99.example.test","https://harbor.openova.io"]`, `["https://harbor.openova.io","https://harbor.t99.example.test"]`, 1), times(4, "the first endpoint is not https://harbor.t99.example.test for [docker.io]")},
		"no entry for every name":    {"born-local", strings.Replace(bornLocalFile, `"*":{"endpoint":["https://harbor.t99.example.test"]},`, "", 1), times(4, `the registry file has no entry "*"`)},
		"the local pack's YAML file": {"born-local", "mirrors:\n  docker.io:\n    endpoint:\n      - \"https://mirror.gcr.io\"\n", times(4, "/etc/rancher/k3s/registries.yaml is not the JSON the provider modules write")},
		"a file with no name":        {"born-local", `{"mirrors":{}}`, times(4, "the registry file lists no name")},
		"no registry file":           {"born-local", "", times(4, "the node has no readable /etc/rancher/k3s/registries.yaml")},
	} {
		t.Run(name, func(t *testing.T) {
			f := &probeFake{t: t, nodes: "node-1 node-2", host: bornLocalHost(c.file)}
			r := runCheck(t, c.scenario, bornLocalCheck{kubectl: f.kubectl}, &CheckContext{})
			got := failedSteps(r)
			ok := len(got) == len(c.want) && (r.run.Checks[0].Status == Pass) == (len(c.want) == 0)
			for i := 0; ok && i < len(got); i++ {
				ok = strings.Contains(got[i], c.want[i])
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
	r := runCheck(t, "born-local", bornLocalCheck{kubectl: kubectl}, &CheckContext{})
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
		2: `"owners":{"sovereign-region":"` + bornLocalOwn + `"}`, 7: `"docker.io":["https://harbor.t99.example.test","https://harbor.openova.io"]`,
	} {
		if e, _ := json.Marshal(r.run.Steps[i].Evidence); !strings.Contains(string(e), want) {
			t.Errorf("the evidence of step %s lacks %s: %s", r.run.Steps[i].ID, want, e)
		}
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
