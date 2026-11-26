package suite

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// classRepo is the checkout this package is tested in.
var classRepo = filepath.Join("..", "..", "..", "..", "..")

// classTempRoot is a checkout that holds the schema of the repository and the
// given files: a key with a slash is a path of the checkout, any other key a
// file of the folder of the classes that name no provider.
func classTempRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	schema, err := os.ReadFile(filepath.Clean(filepath.Join(classRepo, filepath.FromSlash(classSchema))))
	if err != nil {
		t.Fatal(err)
	}
	files[classSchema] = string(schema)
	for name, body := range files {
		if !strings.Contains(name, "/") {
			name = classDir + "/" + name
		}
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// repoClassPath is the path of the class file of name in the repository.
func repoClassPath(t *testing.T, name string) string {
	t.Helper()
	files, err := ClassFiles(classRepo)
	if err != nil {
		t.Fatal(err)
	}
	if files[name] == "" {
		t.Fatalf("the repository has no class %s: %v", name, files)
	}
	return files[name]
}

func repoClassFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(classRepo, filepath.FromSlash(repoClassPath(t, name)))))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// repoManagedClass is the name of the one class of the repository that is not
// the reference class: the managed class of ADR-0037.
func repoManagedClass(t *testing.T) string {
	t.Helper()
	names, err := ClassNames(classRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || !slices.Contains(names, referenceClass) {
		t.Fatalf("class files %v, want the reference class and the managed class of ADR-0037", names)
	}
	if names[0] == referenceClass {
		return names[1]
	}
	return names[0]
}

// TestClassFiles_OfTheRepository: every class file of the repository follows
// the schema, and the two classes declare what ADR-0037 and the reference
// class say.
func TestClassFiles_OfTheRepository(t *testing.T) {
	schema, err := ReadClassSchema(classRepo)
	if err != nil {
		t.Fatal(err)
	}
	managed := repoManagedClass(t)
	// The reference class names no provider; the managed class lives in the pack of its provider.
	if p := repoClassPath(t, referenceClass); p != classDir+"/"+referenceClass+".yaml" {
		t.Errorf("the reference class is %s", p)
	}
	if p := repoClassPath(t, managed); !strings.HasPrefix(p, "infra/providers/") || !strings.HasSuffix(p, "/cluster-classes/"+managed+".yaml") {
		t.Errorf("the managed class is %s, want it in the pack of its provider", p)
	}
	classes := map[string]*Class{}
	for _, n := range []string{referenceClass, managed} {
		c, err := ReadClass(classRepo, schema, n)
		if err != nil {
			t.Fatal(err)
		}
		classes[n] = c
	}
	ref := classes[referenceClass]
	if ref.Kind != "reference" || ref.Provider != "any" || ref.Distribution != "k3s" || !ref.MayHostControlPlane || len(ref.Capabilities) != 13 {
		t.Errorf("the reference class reads %+v", ref)
	}
	for _, f := range schema.Flags {
		if !ref.Capabilities[f] {
			t.Errorf("the reference class declares %s false; it declares every flag true", f)
		}
	}
	mc := classes[managed]
	off := []string{}
	for _, f := range schema.Flags {
		if !mc.Capabilities[f] {
			off = append(off, f)
		}
	}
	if mc.MayHostControlPlane || mc.Kind != "managed-byo-cilium" || !slices.Equal(off, []string{"dataplane.kubeProxyFree", "mesh.cilium", "encryption.wireguard"}) {
		t.Errorf("%s: mayHostControlPlane %t, kind %s, flags declared false %v", managed, mc.MayHostControlPlane, mc.Kind, off)
	}
}

// TestClassFiles_TheManagedClassIsTheTextOfADR0037: the class file holds
// exactly the block that decision MC6 of ADR-0037 shows.
func TestClassFiles_TheManagedClassIsTheTextOfADR0037(t *testing.T) {
	adr, err := os.ReadFile(filepath.Clean(filepath.Join(classRepo, "docs", "adr", "0037-managed-cluster-class-on-the-reference-cloud.md")))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)\\*\\*MC6 The class file\\.\\*\\*.*?\n```yaml\n(.*?)```\n").FindSubmatch(adr)
	if m == nil {
		t.Fatal("ADR-0037 holds no yaml block under MC6")
	}
	managed := repoManagedClass(t)
	if got := repoClassFile(t, managed); got != string(m[1]) {
		t.Errorf("%s differs from ADR-0037 MC6:\n%s\nwant:\n%s", repoClassPath(t, managed), got, m[1])
	}
}

// TestClassSchema_EveryFlagHasOneProbe: the probes of check NET-G17 are the
// flags of the schema, in its order.
func TestClassSchema_EveryFlagHasOneProbe(t *testing.T) {
	schema, err := ReadClassSchema(classRepo)
	if err != nil {
		t.Fatal(err)
	}
	probed := []string{}
	for _, p := range classProbes {
		probed = append(probed, p.flag)
	}
	if !slices.Equal(probed, schema.Flags) || len(schema.Flags) != 13 {
		t.Errorf("probes %v, schema flags %v", probed, schema.Flags)
	}
}

// TestReadClass_Refuses: each broken class file is refused with its reason;
// the control, the reference class unchanged, is read.
func TestReadClass_Refuses(t *testing.T) {
	ref := repoClassFile(t, referenceClass)
	edit := func(old, new string) string {
		if !strings.Contains(ref, old) {
			t.Fatalf("the reference class holds no %q", old)
		}
		return strings.Replace(ref, old, new, 1)
	}
	for name, c := range map[string]struct{ body, want string }{
		"control":                 {ref, ""},
		"a flag is missing":       {edit("  policy.l7: true\n", ""), "capabilities: policy.l7 is missing"},
		"an unknown flag":         {edit("  policy.l7: true\n", "  policy.l7: true\n  policy.l8: true\n"), "policy.l8 is not a field of the schema"},
		"a node port is needed":   {edit("lb.noNodePort: true", "lb.noNodePort: false"), "capabilities.lb.noNodePort: must be true, got false"},
		"a kind not in the list":  {edit("kind: reference", "kind: managed"), "kind: managed is not one of"},
		"another name":            {edit("name: reference-k3s-cilium", "name: other"), `its name is "other", want the file name`},
		"a field is missing":      {edit("provider: any\n", ""), "provider is missing"},
		"an unknown field":        {ref + "zone: edge\n", "zone is not a field of the schema"},
		"text for a boolean":      {edit("mayHostControlPlane: true", "mayHostControlPlane: maybe"), "mayHostControlPlane: want true or false"},
		"a boolean for text":      {edit("distribution: k3s", "distribution: true"), "distribution: want text"},
		"a quoted value":          {edit("distribution: k3s", `distribution: "k3s"`), "neither true, false nor text"},
		"a YAML 1.1 boolean":      {edit("policy.k8s: true", "policy.k8s: yes"), "neither true, false nor text"},
		"a colon in the text":     {edit("kind: reference", "kind: reference: x"), `holds ": "`},
		"a comment after a value": {edit("kind: reference", "kind: reference # x"), `holds ": " or " #"`},
		"a key given twice":       {edit("  policy.k8s: true\n", "  policy.k8s: true\n  policy.k8s: false\n"), "policy.k8s is given twice"},
		"an indent of four":       {edit("  policy.k8s: true", "    policy.k8s: true"), "indented by 4 spaces"},
		"an indented top field":   {edit("kind: reference", "  kind: reference"), "only the lines of capabilities are indented"},
		"a map that is not one":   {edit("notes: k3s", "notes:\n  a: k3s"), "notes has no value"},
		"capabilities as text":    {strings.ReplaceAll(edit("capabilities:\n", "capabilities: none\n"), "  ", ""), "capabilities: want a map"},
	} {
		t.Run(name, func(t *testing.T) {
			root := classTempRoot(t, map[string]string{referenceClass + ".yaml": c.body})
			schema, err := ReadClassSchema(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ReadClass(root, schema, referenceClass)
			if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
				t.Errorf("error %v, want one that holds %q (empty: none)", err, c.want)
			}
		})
	}
}

// TestReadClass_TheFolderNamesTheProvider: a class in the shared folder must
// declare provider any, a class in a pack folder the provider of that pack, and
// a name may have one file only; the controls are read.
func TestReadClass_TheFolderNamesTheProvider(t *testing.T) {
	ref := repoClassFile(t, referenceClass)
	other := strings.Replace(strings.Replace(ref, "provider: any", "provider: other", 1), "name: "+referenceClass, "name: other-class", 1)
	pack := "infra/providers/other/cluster-classes/"
	for name, c := range map[string]struct {
		files       map[string]string
		class, want string
	}{
		"control, shared":           {map[string]string{referenceClass + ".yaml": ref}, referenceClass, ""},
		"control, pack":             {map[string]string{pack + "other-class.yaml": other}, "other-class", ""},
		"a provider in the shared":  {map[string]string{"other-class.yaml": other}, "other-class", `its provider is "other", want "any"`},
		"another pack's provider":   {map[string]string{"infra/providers/third/cluster-classes/other-class.yaml": other}, "other-class", `its provider is "other", want "third"`},
		"any in a pack":             {map[string]string{pack + referenceClass + ".yaml": ref}, referenceClass, `its provider is "any", want "other"`},
		"one name in both folders":  {map[string]string{referenceClass + ".yaml": ref, pack + referenceClass + ".yaml": ref}, referenceClass, "has two files"},
		"a name with no class file": {map[string]string{referenceClass + ".yaml": ref}, "other-class", "no class file other-class.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			root := classTempRoot(t, c.files)
			schema, err := ReadClassSchema(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ReadClass(root, schema, c.class)
			if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
				t.Errorf("error %v, want one that holds %q (empty: none)", err, c.want)
			}
		})
	}
}

// TestReadClassSchema_Refuses: a schema with a keyword the reader does not
// apply, an open map, or flags that it requires but does not describe is refused.
func TestReadClassSchema_Refuses(t *testing.T) {
	b, err := os.ReadFile(filepath.Clean(filepath.Join(classRepo, filepath.FromSlash(classSchema))))
	if err != nil {
		t.Fatal(err)
	}
	schema := string(b)
	edit := func(old, new string) string {
		if !strings.Contains(schema, old) {
			t.Fatalf("the schema holds no %q", old)
		}
		return strings.Replace(schema, old, new, 1)
	}
	for name, c := range map[string]struct{ body, want string }{
		"control":                {schema, ""},
		"a keyword not applied":  {edit(`"notes": {"type": "string"}`, `"notes": {"type": "string", "maxLength": 80}`), `uses the keyword "maxLength"`},
		"an open map":            {edit(`"additionalProperties": false,`, `"additionalProperties": true,`), "additionalProperties may only be false"},
		"a flag not described":   {edit(`"mesh.cilium", `, `"mesh.cilium", "mesh.other", `), "are not exactly the flags it describes"},
		"a flag not required":    {edit(`"mesh.cilium", `, ``), "are not exactly the flags it describes"},
		"a type not applied":     {edit(`"notes": {"type": "string"}`, `"notes": {"type": "integer"}`), `the schema's type "integer" is not applied`},
		"a pattern that is none": {edit(`"pattern": "^(any|[a-z][a-z0-9-]*)$"`, `"pattern": "("`), "pattern:"},
	} {
		t.Run(name, func(t *testing.T) {
			root := classTempRoot(t, map[string]string{referenceClass + ".yaml": repoClassFile(t, referenceClass)})
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(classSchema)), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := ReadClassSchema(root)
			if err == nil {
				_, err = ReadClass(root, s, referenceClass)
			}
			if (c.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.want)) {
				t.Errorf("error %v, want one that holds %q (empty: none)", err, c.want)
			}
		})
	}
}
