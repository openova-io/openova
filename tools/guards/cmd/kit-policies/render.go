package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// kitDir is the folder of the kit under the root of a tree.
const kitDir = "clusters/_template/bootstrap-kit"

// afterWindow is laid over the value set, so the render is a Sovereign's after
// the hand-over: the hand-over sets SOVEREIGN_CONSUME to local, which closes the
// bootstrap window of the policy chart (kit slot 27a); and the test fixtures of
// the console's QA matrix (slot 13, qaFixtures) are off, their default
// (qa_fixtures_enabled of infra/providers/_contract/contract.json): they are
// not the kit's workloads, and tests/fixtures/variables/today.env switches them on.
const afterWindow = "SOVEREIGN_CONSUME=local\nQA_FIXTURES_ENABLED=false\n"

// env holds the tree under judgement, the tree the guard runs from (its own
// image-path and scripts/lib/helm-dep-repos.sh), a scratch folder and the tools.
type env struct {
	root, self, work string
	vars             []string
	kyverno          string
	imagePath        string
	helmRepos        string
}

// release is one HelmRelease of the kit and the objects its render holds.
type release struct {
	file, name, namespace, chart, target string
	objects                              []obj
}

// setup checks the tools and builds image-path, the substitution of the kit.
func (e *env) setup() error {
	for _, t := range []string{"bash", "go", "helm", "yq", "kubectl", e.kyverno} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("%s is not on PATH: %v", t, err)
		}
	}
	if err := e.checkKyverno(); err != nil {
		return err
	}
	window := filepath.Join(e.work, "after-window.env")
	if err := os.WriteFile(window, []byte(afterWindow), 0o600); err != nil {
		return err
	}
	e.vars = append(e.vars, window)
	e.imagePath = filepath.Join(e.work, "image-path")
	if _, err := e.cmd(nil, filepath.Join(e.self, "tools", "guards"), nil, "go", "build", "-o", e.imagePath, "./cmd/image-path"); err != nil {
		return fmt.Errorf("the substitution of the kit (tools/guards/cmd/image-path) cannot be built: %v", err)
	}
	// A private copy of Helm's repository list, so the repositories a dependency
	// build adds are never written into the host's list.
	e.helmRepos = filepath.Join(e.work, "repositories.yaml")
	repos := []byte("apiVersion: \"\"\nrepositories: []\n")
	if b, err := e.cmd(nil, "", nil, "helm", "env", "HELM_REPOSITORY_CONFIG"); err == nil {
		if host, err := os.ReadFile(strings.TrimSpace(string(b))); err == nil && len(host) > 0 {
			repos = host
		}
	}
	return os.WriteFile(e.helmRepos, repos, 0o600)
}

var cliVersion = regexp.MustCompile(`(?m)^Version:\s*v?(\S+)`)

// checkKyverno: the CLI must be the version of the engine, the appVersion of
// platform/kyverno/chart of the tree under judgement.
func (e *env) checkKyverno() error {
	b, err := os.ReadFile(filepath.Join(e.root, "platform", "kyverno", "chart", "Chart.yaml"))
	if err != nil {
		return fmt.Errorf("the engine chart cannot be read: %v", err)
	}
	m := regexp.MustCompile(`(?m)^appVersion:\s*"?v?([^"\s]+)"?`).FindSubmatch(b)
	if m == nil {
		return fmt.Errorf("platform/kyverno/chart/Chart.yaml names no appVersion")
	}
	got, err := e.cmd(nil, "", nil, e.kyverno, "version")
	if err != nil {
		return err
	}
	v := cliVersion.FindSubmatch(got)
	if v == nil || string(v[1]) != string(m[1]) {
		return fmt.Errorf("the Kyverno CLI %s is not the engine's version v%s (platform/kyverno/chart appVersion): another version judges differently", e.kyverno, m[1])
	}
	return nil
}

// cmd runs a tool in dir with stdin and extra environment, and returns its
// standard output. A failure carries the first line of its error output.
func (e *env) cmd(stdin []byte, dir string, extra []string, name string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...) // #nosec G204 -- the guard's own tools
	c.Dir = dir
	c.Env = append(os.Environ(), extra...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s %s: %v: %s", filepath.Base(name), strings.Join(args, " "), err, firstLine(errb.String()))
	}
	return out.Bytes(), nil
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "WARNING") {
			return l
		}
	}
	return "(no error output)"
}

// objects reads YAML documents through yq into JSON objects; an empty
// document is skipped, any other document that is not an object is an error.
func (e *env) objects(yaml []byte) ([]obj, error) {
	b, err := e.cmd(yaml, "", nil, "yq", "-o=json", "-I=0", ".", "-")
	if err != nil {
		return nil, err
	}
	var out []obj
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "null" {
			continue
		}
		var o obj
		if err := json.Unmarshal([]byte(line), &o); err != nil {
			return nil, fmt.Errorf("a document is not an object: %v", err)
		}
		out = append(out, o)
	}
	return out, sc.Err()
}

// renderKit renders every HelmRelease of the kit. A kit entry that is not a
// file, a release whose chart has no folder and a chart that does not render
// are errors: a render that misses them is not the kit.
func (e *env) renderKit() ([]release, error) {
	dir := filepath.Join(e.root, kitDir)
	b, err := e.cmd(nil, "", nil, "yq", "-o=json", "-I=0", ".resources", filepath.Join(dir, "kustomization.yaml"))
	if err != nil {
		return nil, fmt.Errorf("the kit %s cannot be read: %v", kitDir, err)
	}
	var files []string
	if err := json.Unmarshal(b, &files); err != nil || len(files) == 0 {
		return nil, fmt.Errorf("%s/kustomization.yaml lists no resource", kitDir)
	}
	charts, err := chartDirs(e.root)
	if err != nil {
		return nil, err
	}
	var vargs []string
	for _, v := range e.vars {
		vargs = append(vargs, "-vars", v)
	}
	var out []release
	for _, f := range files {
		path := filepath.Join(dir, f)
		if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: not a file of the kit (a folder or a remote is not read here)", f)
		}
		text, err := e.cmd(nil, "", nil, "yq", `select(.kind == "HelmRelease") | ... comments=""`, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		if len(bytes.TrimSpace(text)) == 0 {
			continue
		}
		sub, err := e.cmd(text, "", nil, e.imagePath, append([]string{"subst"}, vargs...)...)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		hrs, err := e.objects(sub)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		for _, hr := range hrs {
			if dig(hr, "spec", "suspend") == true {
				continue
			}
			r, err := e.render(f, hr, charts)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the kit %s holds no HelmRelease", kitDir)
	}
	return out, nil
}

var chartName = regexp.MustCompile(`(?m)^name:\s*(\S+)\s*$`)

// chartDirs maps the name of each chart of platform/ and products/ to its folder.
func chartDirs(root string) (map[string]string, error) {
	out := map[string]string{}
	for _, pattern := range []string{"platform/*/chart/Chart.yaml", "products/*/chart/Chart.yaml", "products/*/*/chart/Chart.yaml"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			b, err := os.ReadFile(m) // #nosec G304 -- a chart of the tree
			if err != nil {
				return nil, err
			}
			if n := chartName.FindSubmatch(b); n != nil {
				if _, seen := out[string(n[1])]; !seen {
					out[string(n[1])] = filepath.Dir(m)
				}
			}
		}
	}
	return out, nil
}

// render renders one HelmRelease as Flux installs it.
func (e *env) render(file string, hr obj, charts map[string]string) (release, error) {
	r := release{file: file, name: str(dig(hr, "metadata", "name")), namespace: str(dig(hr, "metadata", "namespace")),
		chart: str(dig(hr, "spec", "chart", "spec", "chart"))}
	if r.namespace == "" {
		r.namespace = "default"
	}
	r.target = str(dig(hr, "spec", "targetNamespace"))
	if r.target == "" {
		r.target = r.namespace
	}
	name := str(dig(hr, "spec", "releaseName"))
	if name == "" {
		name = r.name
		if t := str(dig(hr, "spec", "targetNamespace")); t != "" {
			name = t + "-" + r.name
		}
	}
	dir := charts[r.chart]
	if dir == "" {
		return r, fmt.Errorf("%s: no chart folder for %q", file, r.chart)
	}
	if err := e.dependencies(dir); err != nil {
		return r, fmt.Errorf("%s: the dependencies of %s cannot be built: %v", file, r.chart, err)
	}
	values, err := json.Marshal(orEmpty(dig(hr, "spec", "values")))
	if err != nil {
		return r, err
	}
	vfile := filepath.Join(e.work, "values.json")
	if err := os.WriteFile(vfile, values, 0o600); err != nil {
		return r, err
	}
	// The flags of the kit render, scripts/check-freshprov-image-registry.sh render_chart.
	text, err := e.cmd(nil, dir, nil, "helm", "template", name, ".", "--namespace", r.target, "-f", vfile,
		"--api-versions", "postgresql.cnpg.io/v1", "--api-versions", "cilium.io/v2", "--skip-schema-validation")
	if err != nil {
		return r, fmt.Errorf("%s: chart %s does not render: %v", file, r.chart, err)
	}
	objects, err := e.objects(text)
	if err != nil {
		return r, fmt.Errorf("%s: the render of %s: %v", file, r.chart, err)
	}
	testsOn := dig(hr, "spec", "test", "enable") == true
	filtered := testFiltered(hr)
	var manifest, hooks []obj
	for _, o := range objects {
		hook := str(dig(o, "metadata", "annotations", "helm.sh/hook"))
		switch {
		case hook == "":
			manifest = append(manifest, o)
		case testOnly(hook) && (!testsOn || filtered(str(dig(o, "metadata", "name")))):
		default:
			hooks = append(hooks, o)
		}
	}
	if manifest, err = e.postRender(hr, manifest); err != nil {
		return r, fmt.Errorf("%s: the postRenderers of %s/%s: %v", file, r.namespace, r.name, err)
	}
	for _, o := range manifest {
		setLabel(o, "helm.toolkit.fluxcd.io/name", r.name)
		setLabel(o, "helm.toolkit.fluxcd.io/namespace", r.namespace)
	}
	r.objects = append(manifest, hooks...)
	for _, o := range r.objects {
		if str(dig(o, "metadata", "namespace")) == "" && !clusterScoped[str(o["kind"])] {
			meta, _ := o["metadata"].(obj)
			if meta == nil {
				meta = obj{}
				o["metadata"] = meta
			}
			meta["namespace"] = r.target
		}
	}
	return r, nil
}

func orEmpty(v any) any {
	if v == nil {
		return obj{}
	}
	return v
}

// testFiltered reports whether the release's spec.test.filters leave the test
// hook of a name out. helm-controller hands them to Helm's ReleaseTesting as
// !name (exclude true) and name, and Helm skips a hook an exclude filter names
// and, when a filter without exclude names any hook, every hook it does not
// name, so the Pod is never created (slot 08 excludes openbao-server-test this
// way, SEC-162).
func testFiltered(hr obj) func(name string) bool {
	exclude, include := map[string]bool{}, map[string]bool{}
	for _, f := range list(dig(hr, "spec", "test", "filters")) {
		if dig(f, "exclude") == true {
			exclude[str(dig(f, "name"))] = true
		} else {
			include[str(dig(f, "name"))] = true
		}
	}
	return func(name string) bool {
		return exclude[name] || (len(include) > 0 && !include[name])
	}
}

// testOnly: every event of the hook is a test event.
func testOnly(hook string) bool {
	for _, ev := range strings.Split(hook, ",") {
		switch strings.TrimSpace(ev) {
		case "test", "test-success", "test-failure":
		default:
			return false
		}
	}
	return true
}

func setLabel(o obj, k, v string) {
	meta, _ := o["metadata"].(obj)
	if meta == nil {
		meta = obj{}
		o["metadata"] = meta
	}
	labels, _ := meta["labels"].(obj)
	if labels == nil {
		labels = obj{}
		meta["labels"] = labels
	}
	labels[k] = v
}

// dependencies builds the dependencies of a chart that declares some and has
// none under charts/; when the build fails for a repository Helm does not know,
// it adds the repositories the chart names (add_dep_repos of
// scripts/lib/helm-dep-repos.sh, in the private list) and builds once more.
func (e *env) dependencies(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "Chart.yaml")) // #nosec G304 -- a chart of the tree
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^dependencies:`).Match(b) {
		return nil
	}
	for _, p := range []string{"charts/*.tgz", "charts/*/Chart.yaml"} {
		if m, _ := filepath.Glob(filepath.Join(dir, p)); len(m) > 0 {
			return nil
		}
	}
	extra := []string{"HELM_REPOSITORY_CONFIG=" + e.helmRepos}
	if _, err := e.cmd(nil, dir, extra, "helm", "dependency", "build"); err == nil {
		return nil
	}
	lib := filepath.Join(e.self, "scripts", "lib", "helm-dep-repos.sh")
	if _, err := e.cmd(nil, dir, extra, "bash", "-c", `. "$1" && add_dep_repos .`, "add-dep-repos", lib); err != nil {
		return err
	}
	_, err = e.cmd(nil, dir, extra, "helm", "dependency", "build")
	return err
}

// postRender applies the postRenderers of the release in their order, as
// helm-controller does: one kustomization over the release manifest each.
// Only kustomize patches and images are read; any other field stops the guard.
func (e *env) postRender(hr obj, objects []obj) ([]obj, error) {
	for i, pr := range list(dig(hr, "spec", "postRenderers")) {
		k, ok := dig(pr, "kustomize").(obj)
		if !ok {
			return nil, fmt.Errorf("postRenderer %d is not a kustomize post-renderer", i)
		}
		dir := filepath.Join(e.work, fmt.Sprintf("post-%d", i))
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		kust := obj{"apiVersion": "kustomize.config.k8s.io/v1beta1", "kind": "Kustomization"}
		for field, v := range k {
			if field != "patches" && field != "images" {
				return nil, fmt.Errorf("postRenderer %d sets kustomize.%s, which the guard does not apply", i, field)
			}
			kust[field] = v
		}
		var names []any
		for n, o := range objects {
			b, err := json.Marshal(o)
			if err != nil {
				return nil, err
			}
			name := fmt.Sprintf("r%05d.json", n)
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
				return nil, err
			}
			names = append(names, name)
		}
		kust["resources"] = names
		b, err := json.Marshal(kust)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), b, 0o600); err != nil {
			return nil, err
		}
		built, err := e.cmd(nil, "", nil, "kubectl", "kustomize", dir)
		if err != nil {
			return nil, err
		}
		if objects, err = e.objects(built); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// clusterScoped names the kinds without a namespace that the kit renders; an
// object of any other kind without one gets the release namespace, as Helm
// sets it at install.
var clusterScoped = map[string]bool{
	"APIService": true, "CSIDriver": true, "ClusterIssuer": true, "ClusterPolicy": true, "ClusterRole": true,
	"ClusterRoleBinding": true, "ClusterSecretStore": true, "ClusterExternalSecret": true, "Composition": true,
	"CompositeResourceDefinition": true, "Configuration": true, "CustomResourceDefinition": true,
	"DeletingPolicy": true, "DeploymentRuntimeConfig": true, "Function": true, "GatewayClass": true,
	"GeneratingPolicy": true, "ImageValidatingPolicy": true, "IngressClass": true, "MutatingPolicy": true,
	"MutatingWebhookConfiguration": true, "Namespace": true, "PersistentVolume": true, "PriorityClass": true,
	"Provider": true, "RuntimeClass": true, "StorageClass": true, "ValidatingAdmissionPolicy": true,
	"ValidatingAdmissionPolicyBinding": true, "ValidatingPolicy": true, "ValidatingWebhookConfiguration": true,
	"VolumeSnapshotClass": true, "CiliumClusterwideNetworkPolicy": true, "CiliumLoadBalancerIPPool": true,
	"ClusterPolicyReport": true, "ClusterCleanupPolicy": true,
}
