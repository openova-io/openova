package main

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// flux builds a Flux Kustomization of the given path and postBuild.
func flux(name, path, postBuild string) string {
	return "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: " + name +
		"\n  namespace: flux-system\nspec:\n  interval: 5m\n  path: " + path + "\n  prune: true\n  sourceRef:\n    kind: GitRepository\n    name: openova\n" + postBuild
}

const (
	seedOnly = "  postBuild:\n    substituteFrom:\n      - kind: Secret\n        name: seed\n"
	// A ConfigMap of the repository, beside the Secret source; it does not hold A.
	varsAndSeed = "  postBuild:\n    substituteFrom:\n      - kind: ConfigMap\n        name: vars\n        optional: false\n      - kind: Secret\n        name: seed\n"
	varsMap     = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vars\n  namespace: flux-system\ndata:\n  SOVEREIGN_FQDN: example.test\n"
)

// sourceTree is a repository with one template folder clusters/a, the
// template referencing A, and the given extra files.
func sourceTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"clusters/a/secret.yaml":        secret("data:\n  token: \"${A}\"\n"),
		"clusters/a/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - secret.yaml\n",
	}
	for p, c := range extra {
		files[p] = c
	}
	for p, c := range files {
		write(t, root, p, c)
	}
	gitInit(t, root)
	return root
}

// runOn runs the command on a tree and returns what it lists and what it says.
func runOn(t *testing.T, root string) (string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errw.String())
	}
	return out.String(), errw.String()
}

func wantFinding(t *testing.T, root, reason string) {
	t.Helper()
	out, errw := runOn(t, root)
	if strings.Contains(out, "clusters/a/secret.yaml") {
		t.Errorf("clusters/a/secret.yaml is listed as a template: %q", out)
	}
	if !strings.Contains(errw, "clusters/a/secret.yaml is not a template of references: "+reason) {
		t.Errorf("stderr %q does not say %q", errw, reason)
	}
}

// A NAME that is also a key of the inline postBuild.substitute of the
// Kustomization that renders the folder: its value is in Git.
func TestTemplateNameFromInlineSubstituteIsAFinding(t *testing.T) {
	inline := "  postBuild:\n    substitute:\n      A: a-literal\n    substituteFrom:\n      - kind: Secret\n        name: seed\n"
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", inline)}),
		"the name A is also a key of the inline postBuild.substitute of the Flux Kustomization a (clusters/root/a.yaml)")
	// Another key of the inline map is not a finding.
	other := strings.Replace(inline, "A: a-literal", "SOVEREIGN_FQDN: example.test", 1)
	if out, _ := runOn(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", other)})); out != "clusters/a/secret.yaml\n" {
		t.Errorf("an inline key the template does not reference is a finding: listed %q", out)
	}
}

// A NAME that is a key of a ConfigMap the Kustomization's substituteFrom
// names, where that ConfigMap is tracked: a manifest of its own, a chart
// template, or a document inside the first-boot template.
func TestTemplateNameFromTrackedConfigMapIsAFinding(t *testing.T) {
	for where, file := range map[string]map[string]string{
		"a manifest under clusters/": {"clusters/vars/configmap.yaml": strings.Replace(varsMap, "  SOVEREIGN_FQDN: example.test\n", "  SOVEREIGN_FQDN: example.test\n  A: a-literal\n", 1)},
		"a chart template":           {"platform/vars/chart/templates/configmap.yaml": "{{- if .Values.enabled }}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vars\ndata:\n  A: {{ .Values.a | quote }}\n{{- end }}\n"},
		"the first-boot template":    {"infra/providers/_shared/cloudinit.tftpl": "write_files:\n  - path: /var/lib/x.yaml\n    content: |\n      ---\n      apiVersion: v1\n      kind: ConfigMap\n      metadata:\n        name: vars\n      data:\n%{ if x ~}\n            A: ${a}\n%{ endif ~}\n  - path: /etc/other\n"},
	} {
		extra := map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", varsAndSeed)}
		for p, c := range file {
			extra[p] = c
		}
		root := sourceTree(t, extra)
		out, errw := runOn(t, root)
		if strings.Contains(out, "clusters/a/secret.yaml") || !strings.Contains(errw, "the name A is a key of the ConfigMap vars (") {
			t.Errorf("%s: the ConfigMap's key A is not found: listed %q, stderr %q", where, out, errw)
		}
	}
	// The same key in a ConfigMap of another name, or under docs/, is not a finding.
	for where, file := range map[string]string{
		"clusters/other/configmap.yaml": strings.Replace(strings.Replace(varsMap, "name: vars", "name: other", 1), "  SOVEREIGN_FQDN: example.test\n", "  A: a-literal\n", 1),
		"docs/configmap.yaml":           strings.Replace(varsMap, "  SOVEREIGN_FQDN: example.test\n", "  A: a-literal\n", 1),
	} {
		root := sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", varsAndSeed), where: file})
		if out, _ := runOn(t, root); out != "clusters/a/secret.yaml\n" {
			t.Errorf("%s: a key outside the source ConfigMap is a finding: listed %q", where, out)
		}
	}
}

// A folder of templates whose Kustomization names no substituteFrom entry of
// kind Secret, whose postBuild cannot be read, or that no Kustomization renders.
func TestTemplateNameWithoutASecretSourceIsAFinding(t *testing.T) {
	configMapOnly := "  postBuild:\n    substituteFrom:\n      - kind: ConfigMap\n        name: vars\n"
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", configMapOnly), "clusters/vars/configmap.yaml": varsMap}),
		"the Flux Kustomization a (clusters/root/a.yaml) renders its folder and names no postBuild.substituteFrom entry of kind Secret")
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", "")}),
		"the Flux Kustomization a (clusters/root/a.yaml) renders its folder and names no postBuild.substituteFrom entry of kind Secret")
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", "  postBuild: {substituteFrom: [{kind: Secret, name: seed}]}\n")}),
		"the Flux Kustomization a (clusters/root/a.yaml) renders its folder and its postBuild cannot be read")
	wantFinding(t, sourceTree(t, nil), "no Flux Kustomization under clusters/ renders its folder clusters/a")
	// One of two Kustomizations that render the folder without a Secret source.
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", seedOnly), "clusters/root/b.yaml": flux("b", "./clusters", configMapOnly)}),
		"the Flux Kustomization b (clusters/root/b.yaml) renders its folder and names no postBuild.substituteFrom entry of kind Secret")
	// A Kustomization of another folder is not one that renders it.
	wantFinding(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/b", seedOnly), "clusters/b/kustomization.yaml": "resources: []\n"}),
		"no Flux Kustomization under clusters/ renders its folder clusters/a")
}

// A Flux Kustomization document the reader cannot parse: which folder it
// renders is not known, so the template is a finding that names its file,
// although a readable Kustomization renders the folder from a Secret source.
func TestTemplateNameInAnUnreadableKustomizationIsAFinding(t *testing.T) {
	broken := strings.Replace(flux("b", "./clusters/a", seedOnly), "\n  interval: 5m", "\n\tinterval: 5m", 1)
	root := sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", seedOnly), "clusters/root/b.yaml": broken})
	wantFinding(t, root, "a Flux Kustomization document in clusters/root/b.yaml cannot be read")
	if _, errw := runOn(t, root); strings.Count(errw, "is not a template of references") != 1 {
		t.Errorf("want one finding, stderr %q", errw)
	}
	// The same document, readable, is no finding.
	if out, errw := runOn(t, sourceTree(t, map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", seedOnly), "clusters/root/b.yaml": flux("b", "./clusters/a", seedOnly)})); out != "clusters/a/secret.yaml\n" {
		t.Errorf("a readable second Kustomization is a finding: listed %q, stderr %q", out, errw)
	}
}

// The NAME comes only from the Secret source: directly, through an overlay
// that builds the folder (the path naming the overlay with a ${VAR}), or
// through a folder without a kustomization.yaml above it.
func TestTemplateNameFromTheSecretSourcePasses(t *testing.T) {
	overlay := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ../../../a\n  - extra.yaml\n"
	for what, extra := range map[string]map[string]string{
		"directly": {"clusters/root/a.yaml": flux("a", "./clusters/a", seedOnly)},
		"beside a tracked ConfigMap without the name": {
			"clusters/root/a.yaml": flux("a", "./clusters/a", varsAndSeed), "clusters/vars/configmap.yaml": varsMap,
			"platform/vars/chart/templates/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: vars\ndata:\n  B: x\n"},
		"through an overlay named with a variable": {
			"clusters/root/a.yaml":                          flux("a", "./clusters/packs/${PACK}/secrets", seedOnly),
			"clusters/packs/one/secrets/kustomization.yaml": overlay,
			"clusters/packs/one/secrets/extra.yaml":         secret("stringData:\n  KEY: \"${B}\"\n"),
		},
		"through a folder without a kustomization.yaml": {"clusters/root/a.yaml": flux("a", "./clusters", seedOnly)},
	} {
		out, errw := runOn(t, sourceTree(t, extra))
		if !strings.Contains(out, "clusters/a/secret.yaml\n") || strings.Contains(errw, "not a template") {
			t.Errorf("%s: listed %q, stderr %q", what, out, errw)
		}
	}
}

func TestParseYAML(t *testing.T) {
	doc := strings.Split(`# a comment
metadata:
  name: "quoted" # a comment after it
  annotations:
    a.b/c: "x: y"
spec:
  path: ./clusters/a
  script: |
    not: parsed
    - nor this
  postBuild:
    substitute:
      'A': x
    substituteFrom:
    - kind: Secret
      name: seed
    -
      kind: ConfigMap
      name: vars
  list: [a, b]
`, "\n")
	n, err := parseYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	if n.get("metadata").get("name").scalar != "quoted" || n.get("metadata").get("annotations").get("a.b/c").scalar != "x: y" {
		t.Errorf("metadata %+v", n.get("metadata"))
	}
	if n.get("spec").get("script").isMap || n.get("spec").get("path").scalar != "./clusters/a" || n.get("spec").get("list").scalar != "[a, b]" {
		t.Errorf("spec %+v", n.get("spec"))
	}
	from := n.get("spec").get("postBuild").get("substituteFrom")
	var got []string
	for _, e := range from.seq {
		got = append(got, e.get("kind").scalar+"/"+e.get("name").scalar)
	}
	if !reflect.DeepEqual(got, []string{"Secret/seed", "ConfigMap/vars"}) || !reflect.DeepEqual(n.get("spec").get("postBuild").get("substitute").keys, []string{"A"}) {
		t.Errorf("postBuild: substituteFrom %v, substitute %v", got, n.get("spec").get("postBuild").get("substitute").keys)
	}
	for _, bad := range []string{"a: 1\n  b: 2\n", "a:\n\tb: 1\n", "a: 1\nnot an entry\n"} {
		if _, err := parseYAML(strings.Split(bad, "\n")); err == nil {
			t.Errorf("%q parses", bad)
		}
	}
}

func TestRenderedFolders(t *testing.T) {
	tree := fstest.MapFS{
		"clusters/o/x/kustomization.yaml":    {Data: []byte("resources:\n  - ../../shared\n  - https://example.test/remote\n  - file.yaml\n")},
		"clusters/o/x/file.yaml":             {Data: []byte("{}\n")},
		"clusters/shared/kustomization.yaml": {Data: []byte("resources:\n  - a.yaml\ncomponents:\n  - ../comp\n")},
		"clusters/shared/a.yaml":             {Data: []byte("{}\n")},
		"clusters/comp/kustomization.yaml":   {Data: []byte("kind: Component\n")},
		"clusters/free/sub/deeper/a.yaml":    {Data: []byte("{}\n")},
	}
	got := func(p string) []string {
		var out []string
		for d := range renderedFolders(tree, fluxKustomization{path: p}) {
			out = append(out, d)
		}
		sort.Strings(out)
		return out
	}
	if g := got("./clusters/o/${X}"); !reflect.DeepEqual(g, []string{"clusters/comp", "clusters/o/x", "clusters/shared"}) {
		t.Errorf("an overlay through a variable renders %v", g)
	}
	if g := got("./clusters/free"); !reflect.DeepEqual(g, []string{"clusters/free", "clusters/free/sub", "clusters/free/sub/deeper"}) {
		t.Errorf("a folder without a kustomization.yaml renders %v", g)
	}
	if g := got("../outside"); len(g) != 0 {
		t.Errorf("a path outside the tree renders %v", g)
	}
}
