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
	"sort"
	"strings"
)

// env holds the tree under judgement, the tree the guard runs from (its own
// image-path and scripts/lib/helm-dep-repos.sh), a scratch folder, the charts
// of the tree by name and the renders already made.
type env struct {
	root, self, work string
	imagePath        string
	helmRepos        string
	charts           map[string]string
	renders          map[string][]string
	built            map[string]bool
}

// release is one HelmRelease of a kit as one value set substitutes it.
type release struct {
	file, name, namespace, chart string
	deps                         []string // namespace/name of each spec.dependsOn entry
	suspended                    bool
	outside                      bool     // its chart is not a chart of this tree (outsideTree)
	kinds                        []string // namespace/name of each SecretProviderClass it renders
}

func (r release) id() string { return r.namespace + "/" + r.name }

// setup checks the tools, builds image-path (the substitution of the kit) and
// makes a private copy of Helm's repository list.
func (e *env) setup() error {
	for _, t := range []string{"bash", "go", "helm", "yq"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("%s is not on PATH: %v", t, err)
		}
	}
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
	if err := os.WriteFile(e.helmRepos, repos, 0o600); err != nil {
		return err
	}
	charts, err := chartDirs(e.root)
	if err != nil {
		return err
	}
	e.charts, e.renders, e.built = charts, map[string][]string{}, map[string]bool{}
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

// readKit substitutes and renders every HelmRelease of the files a kit lists.
// A kit entry that is not a file, a release whose chart is neither a chart of
// this tree nor in outsideTree, a release defined twice and a chart that does
// not render are errors: a verdict that misses them is not the kit's.
func (e *env) readKit(kit string, vars []string) ([]release, error) {
	path := filepath.Join(e.root, kit)
	b, err := e.cmd(nil, "", nil, "yq", "-o=json", "-I=0", ".resources", path)
	if err != nil {
		return nil, fmt.Errorf("the kit %s cannot be read: %v", kit, err)
	}
	var files []string
	if err := json.Unmarshal(b, &files); err != nil || len(files) == 0 {
		return nil, fmt.Errorf("%s lists no resource", kit)
	}
	var vargs []string
	for _, v := range vars {
		vargs = append(vargs, "-vars", v)
	}
	seen := map[string]string{}
	var out []release
	for _, f := range files {
		full := filepath.Clean(filepath.Join(filepath.Dir(path), f))
		rel, err := filepath.Rel(e.root, full)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%s: the entry %s is outside the tree", kit, f)
		}
		if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: the entry %s is not a file (a folder or a remote is not read here)", kit, f)
		}
		text, err := e.cmd(nil, "", nil, "yq", `select(.kind == "HelmRelease") | ... comments=""`, full)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
		if len(bytes.TrimSpace(text)) == 0 {
			continue
		}
		sub, err := e.cmd(text, "", nil, e.imagePath, append([]string{"subst"}, vargs...)...)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
		hrs, err := e.objects(sub)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
		for _, hr := range hrs {
			r, err := e.release(rel, hr)
			if err != nil {
				return nil, err
			}
			if other, dup := seen[r.id()]; dup {
				return nil, fmt.Errorf("%s: the HelmRelease %s is also defined in %s", rel, r.id(), other)
			}
			seen[r.id()] = rel
			out = append(out, r)
		}
	}
	return out, nil
}

// release reads one substituted HelmRelease and, unless it is suspended,
// renders its chart.
func (e *env) release(file string, hr obj) (release, error) {
	r := release{file: file, name: str(dig(hr, "metadata", "name")), namespace: str(dig(hr, "metadata", "namespace")),
		chart: str(dig(hr, "spec", "chart", "spec", "chart"))}
	if r.namespace == "" {
		r.namespace = "default"
	}
	if r.name == "" {
		return r, fmt.Errorf("%s: a HelmRelease has no metadata.name", file)
	}
	for _, d := range list(dig(hr, "spec", "dependsOn")) {
		name, ns := str(dig(d, "name")), str(dig(d, "namespace"))
		if name == "" {
			return r, fmt.Errorf("%s: an entry of spec.dependsOn of %s has no name", file, r.id())
		}
		if ns == "" {
			ns = r.namespace
		}
		r.deps = append(r.deps, ns+"/"+name)
	}
	if dig(hr, "spec", "suspend") == true {
		r.suspended = true
		return r, nil
	}
	dir := e.charts[r.chart]
	if dir == "" {
		if _, listed := outsideTree[r.id()]; listed {
			r.outside = true
			return r, nil
		}
		return r, fmt.Errorf("%s: the chart %q of %s is not a chart of this tree (platform/*/chart, products/*/chart, products/*/*/chart), and outsideTree in main.go does not name the release", file, r.chart, r.id())
	}
	kinds, err := e.render(file, r, hr, dir)
	if err != nil {
		return r, err
	}
	r.kinds = kinds
	return r, nil
}

// render renders the chart of one HelmRelease as Flux installs it and returns
// the SecretProviderClass objects it holds. Two releases with one chart, name,
// namespace, values and tests are rendered once.
func (e *env) render(file string, r release, hr obj, dir string) ([]string, error) {
	target := str(dig(hr, "spec", "targetNamespace"))
	name := str(dig(hr, "spec", "releaseName"))
	if name == "" {
		name = r.name
		if target != "" {
			name = target + "-" + r.name
		}
	}
	if target == "" {
		target = r.namespace
	}
	values, err := json.Marshal(orEmpty(dig(hr, "spec", "values")))
	if err != nil {
		return nil, err
	}
	test, err := json.Marshal(dig(hr, "spec", "test"))
	if err != nil {
		return nil, err
	}
	key := strings.Join([]string{dir, name, target, string(values), string(test)}, "\x00")
	if kinds, done := e.renders[key]; done {
		return kinds, nil
	}
	if !e.built[dir] {
		if err := e.dependencies(dir); err != nil {
			return nil, fmt.Errorf("%s: the dependencies of %s cannot be built: %v", file, r.chart, err)
		}
		e.built[dir] = true
	}
	vfile := filepath.Join(e.work, "values.json")
	if err := os.WriteFile(vfile, values, 0o600); err != nil {
		return nil, err
	}
	// The flags of the kit render (scripts/check-freshprov-image-registry.sh
	// render_chart), and the API version of the kind: on a cluster where the
	// driver runs, a template that asks for it renders the object.
	text, err := e.cmd(nil, dir, nil, "helm", "template", name, ".", "--namespace", target, "-f", vfile,
		"--api-versions", "postgresql.cnpg.io/v1", "--api-versions", "cilium.io/v2",
		"--api-versions", kindGroup+"/v1", "--skip-schema-validation")
	if err != nil {
		return nil, fmt.Errorf("%s: chart %s does not render for %s: %v", file, r.chart, r.id(), err)
	}
	objects, err := e.objects(text)
	if err != nil {
		return nil, fmt.Errorf("%s: the render of %s: %v", file, r.chart, err)
	}
	testsOn := dig(hr, "spec", "test", "enable") == true
	filtered := testFiltered(hr)
	var kinds []string
	for _, o := range objects {
		if str(o["kind"]) != kindName || group(str(o["apiVersion"])) != kindGroup {
			continue
		}
		hook := str(dig(o, "metadata", "annotations", "helm.sh/hook"))
		if hook != "" && testOnly(hook) && (!testsOn || filtered(str(dig(o, "metadata", "name")))) {
			continue
		}
		ns := str(dig(o, "metadata", "namespace"))
		if ns == "" {
			ns = target
		}
		kinds = append(kinds, ns+"/"+str(dig(o, "metadata", "name")))
	}
	sort.Strings(kinds)
	e.renders[key] = kinds
	return kinds, nil
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

func orEmpty(v any) any {
	if v == nil {
		return obj{}
	}
	return v
}

// testFiltered reports whether the release's spec.test.filters leave the test
// hook of a name out: helm-controller hands them to Helm as !name (exclude
// true) and name, and Helm skips a hook an exclude filter names and, when a
// filter without exclude names any hook, every hook it does not name.
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
