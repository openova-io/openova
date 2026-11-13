package intent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// repoRoot is the repository root, seen from this package's directory.
const repoRoot = "../../../../../.."

// definitionFiles returns the files the schema bundle is built from, as
// paths from the repository root, sorted.
func definitionFiles(t *testing.T, root fs.FS) []string {
	t.Helper()
	files, err := fs.Glob(root, "products/catalyst/chart/crds/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "platform/crossplane-claims/chart/templates/crds/useraccess.yaml")
	sort.Strings(files)
	return files
}

// dropTemplateLines removes the lines of a Helm template whose first
// non-blank characters are "{{".
func dropTemplateLines(b []byte) []byte {
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		if strings.HasPrefix(strings.TrimSpace(sc.Text()), "{{") {
			continue
		}
		out.Write(sc.Bytes())
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// buildBundle builds the bytes of crds/schemas.json from the definition
// files: one line per "<group>/<version>/<Kind>" key, in key order, each
// holding the version's openAPIV3Schema as compact JSON.
func buildBundle(root fs.FS, files []string) ([]byte, error) {
	entries := map[string][]byte{}
	for _, f := range files {
		b, err := fs.ReadFile(root, f)
		if err != nil {
			return nil, err
		}
		if strings.Contains(f, "/templates/") {
			b = dropTemplateLines(b)
		}
		r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
		for {
			doc, err := r.Read()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			var crd struct {
				Kind string `json:"kind"`
				Spec struct {
					Group string `json:"group"`
					Names struct {
						Kind string `json:"kind"`
					} `json:"names"`
					Versions []struct {
						Name   string `json:"name"`
						Schema struct {
							OpenAPIV3Schema json.RawMessage `json:"openAPIV3Schema"`
						} `json:"schema"`
					} `json:"versions"`
				} `json:"spec"`
			}
			if err := yaml.Unmarshal(doc, &crd); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if crd.Kind != "CustomResourceDefinition" {
				continue
			}
			for _, v := range crd.Spec.Versions {
				if len(v.Schema.OpenAPIV3Schema) == 0 {
					continue
				}
				key := crd.Spec.Group + "/" + v.Name + "/" + crd.Spec.Names.Kind
				if _, dup := entries[key]; dup {
					return nil, fmt.Errorf("%s: %s is defined twice", f, key)
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, v.Schema.OpenAPIV3Schema); err != nil {
					return nil, fmt.Errorf("%s: %s: %w", f, key, err)
				}
				entries[key] = compact.Bytes()
			}
		}
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, k := range keys {
		q, _ := json.Marshal(k)
		out.Write(q)
		out.WriteString(":")
		out.Write(entries[k])
		if i < len(keys)-1 {
			out.WriteString(",")
		}
		out.WriteString("\n")
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

// TestSchemasMatchSources fails when crds/schemas.json differs from what
// the definition files give. With INTENT_WRITE_SCHEMAS=1 it writes the file.
func TestSchemasMatchSources(t *testing.T) {
	root := os.DirFS(repoRoot)
	files := definitionFiles(t, root)
	if len(files) < 2 {
		t.Fatalf("found %d definition files; the repository root is not %s", len(files), repoRoot)
	}
	want, err := buildBundle(root, files)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("INTENT_WRITE_SCHEMAS") == "1" {
		if err := os.WriteFile("crds/schemas.json", want, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote crds/schemas.json from %d files", len(files))
		return
	}
	if !bytes.Equal(want, schemaBundle) {
		t.Fatalf("crds/schemas.json differs from the definition files %v; run: INTENT_WRITE_SCHEMAS=1 go test ./internal/intent/ -run '^TestSchemasMatchSources$'", files)
	}
}

func readSample(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(repoRoot), path.Join("products/catalyst/chart/crds/tests", name))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]interface{}{}
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: m}
}

// TestValidateSamples checks the chart's sample objects: every valid
// sample passes and every invalid one fails.
func TestValidateSamples(t *testing.T) {
	for _, kind := range []string{"application", "blueprint", "continuum", "environment", "environmentpolicy", "organization", "sovereign"} {
		if errs := Validate(readSample(t, kind+"-sample-valid.yaml")); len(errs) != 0 {
			t.Errorf("%s valid sample: %v", kind, errs)
		}
		if errs := Validate(readSample(t, kind+"-sample-invalid.yaml")); len(errs) == 0 {
			t.Errorf("%s invalid sample passed", kind)
		}
	}
}

func application(name string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps.openova.io/v1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": name, "namespace": "acme"},
		"spec":       spec,
	}}
}

func TestValidate(t *testing.T) {
	good := readSample(t, "application-sample-valid.yaml")
	if errs := Validate(good); len(errs) != 0 {
		t.Fatalf("valid Application: %v", errs)
	}
	// A wrong type names the field.
	bad := good.DeepCopy()
	if err := unstructured.SetNestedField(bad.Object, int64(7), "spec", "blueprintRef", "name"); err != nil {
		t.Fatal(err)
	}
	errs := Validate(bad)
	if len(errs) == 0 || !strings.Contains(errs.ToAggregate().Error(), "spec.blueprintRef.name") {
		t.Fatalf("a number as spec.blueprintRef.name: %v", errs)
	}
	// A missing required field is reported as required.
	errs = Validate(application("x", map[string]interface{}{}))
	if len(errs) == 0 || errs[0].Type != "FieldValueRequired" {
		t.Fatalf("an empty spec: %v", errs)
	}
	// apiVersion, kind and metadata.name are required.
	if errs := Validate(&unstructured.Unstructured{Object: map[string]interface{}{}}); len(errs) != 3 {
		t.Fatalf("an empty object: %v", errs)
	}
	// An own kind with no embedded schema is an error.
	own := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps.openova.io/v9", "kind": "Application", "metadata": map[string]interface{}{"name": "x"}}}
	if errs := Validate(own); len(errs) != 1 {
		t.Fatalf("an unknown version of an own kind: %v", errs)
	}
	// A kind of another group is not checked further.
	cm := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "x", "namespace": "flux-system"}, "data": map[string]interface{}{"a": "b"}}}
	if errs := Validate(cm); len(errs) != 0 {
		t.Fatalf("a ConfigMap: %v", errs)
	}
	// The UserAccess definition comes from the Helm template.
	ua := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "access.openova.io/v1alpha1", "kind": "UserAccess", "metadata": map[string]interface{}{"name": "x"}, "spec": "not an object"}}
	if errs := Validate(ua); len(errs) == 0 {
		t.Fatal("a UserAccess with a string spec passed")
	}
}
