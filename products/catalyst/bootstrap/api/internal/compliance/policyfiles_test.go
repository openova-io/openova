package compliance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

const pfFQDN = "t2.example.org"

// pfPath is a path in the policy folder of one region.
func pfPath(region, rel string) string {
	return "clusters/" + pfFQDN + "/regions/" + region + "/platform/policy/" + rel
}

// pfRegion makes a region folder exist, as the controllers' files do.
func pfRegion(w *fake.Writer, region string) {
	w.Put(FleetRepo, "clusters/"+pfFQDN+"/regions/"+region+"/orgs/acme.yaml", []byte("kind: Kustomization\n"))
}

func pfRead(t *testing.T, w *fake.Writer, p string) (string, bool) {
	t.Helper()
	b, _, err := w.Read(context.Background(), PolicyScope(pfFQDN, nil), FleetRepo, p)
	if errors.Is(err, gitwrite.ErrNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b), true
}

func pfChange() gitwrite.Change {
	return gitwrite.Change{Summary: "policy: test change", ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaAPI}
}

// The empty document as contract C4, section 2.2 names it, with the
// change identifier of the commit and the serialisation of every intent
// file: keys sorted, two-space YAML, one trailing newline.
const pfEmptyDoc = `apiVersion: v1
data:
  values.yaml: |
    {}
kind: ConfigMap
metadata:
  annotations:
    catalyst.openova.io/change-id: 01JCDZ7K9M3Q4T5V6W7X8Y9Z0A
    kustomize.toolkit.fluxcd.io/substitute: disabled
  labels:
    app.kubernetes.io/managed-by: flux
    reconcile.fluxcd.io/watch: Enabled
  name: compliance-policy-values
  namespace: flux-system
`

// TestPolicyFilesScope: the scope of the policy files accepts a path below
// platform/policy/ of a listed region, refuses another region's orgs/
// folder, and an empty region list refuses every commit.
func TestPolicyFilesScope(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	s := PolicyScope(pfFQDN, []string{"r1", "r2"})
	ch := pfChange()
	ch.Scope, ch.Repo = s, FleetRepo
	ch.Files = []gitwrite.FileChange{{Path: pfPath("r2", "decisions/a.yaml"), Op: gitwrite.OpUpsert, Content: []byte("a: 1\n")}}
	if res, err := w.Commit(ctx, ch); err != nil || !res.Committed {
		t.Fatalf("a path below platform/policy/ of a listed region: %+v %v", res, err)
	}
	for name, sc := range map[string]struct {
		s gitwrite.Scope
		p string
	}{
		"another region's orgs folder": {s, "clusters/" + pfFQDN + "/regions/r1/orgs/acme.yaml"},
		"another module":               {s, "clusters/" + pfFQDN + "/regions/r1/platform/root/vars.yaml"},
		"an unlisted region":           {s, pfPath("r3", "decisions/a.yaml")},
		"an empty region list":         {PolicyScope(pfFQDN, nil), pfPath("r1", "decisions/a.yaml")},
	} {
		ch := pfChange()
		ch.Scope, ch.Repo = sc.s, FleetRepo
		ch.Files = []gitwrite.FileChange{{Path: sc.p, Op: gitwrite.OpUpsert, Content: []byte("a: 1\n")}}
		if _, err := w.Commit(ctx, ch); !errors.Is(err, gitwrite.ErrForbiddenPath) {
			t.Errorf("%s: got %v, want ErrForbiddenPath", name, err)
		}
	}
	if n := len(w.Commits()); n != 1 {
		t.Fatalf("%d commits, want 1", n)
	}
}

// TestPolicyFilesRegions: the region folders sorted by name, files left
// out; no region folder is ErrNoRegion.
func TestPolicyFilesRegions(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	if _, err := Regions(ctx, w, pfFQDN); !errors.Is(err, ErrNoRegion) {
		t.Fatalf("no regions folder: got %v, want ErrNoRegion", err)
	}
	w.Put(FleetRepo, "clusters/"+pfFQDN+"/regions/README.md", []byte("not a region\n"))
	if _, err := Regions(ctx, w, pfFQDN); !errors.Is(err, ErrNoRegion) {
		t.Fatalf("a regions folder with only a file: got %v, want ErrNoRegion", err)
	}
	pfRegion(w, "r2")
	pfRegion(w, "r1")
	got, err := Regions(ctx, w, pfFQDN)
	if err != nil || !reflect.DeepEqual(got, []string{"r1", "r2"}) {
		t.Fatalf("Regions = %q %v, want [r1 r2]", got, err)
	}
}

// TestPolicyFilesWriteAllTwoFiles: one write touches exactly two files with
// two regions, with the same bytes, in one commit; a nil content deletes
// the file in both regions.
func TestPolicyFilesWriteAllTwoFiles(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	pfRegion(w, "r1")
	pfRegion(w, "r2")
	res, err := WriteAll(ctx, w, pfFQDN, "decisions/allow-a.yaml", []byte("kind: ComplianceDecision\n"), gitwrite.AbsentSHA, pfChange())
	if err != nil || !res.Committed {
		t.Fatalf("WriteAll: %+v %v", res, err)
	}
	commits := w.Commits()
	want := []string{pfPath("r1", "decisions/allow-a.yaml"), pfPath("r2", "decisions/allow-a.yaml")}
	if len(commits) != 1 || !reflect.DeepEqual(commits[0].Paths, want) {
		t.Fatalf("commits = %+v, want one commit of %q", commits, want)
	}
	for _, p := range want {
		if got, ok := pfRead(t, w, p); !ok || got != "kind: ComplianceDecision\n" {
			t.Fatalf("%s = %q (present %v)", p, got, ok)
		}
	}
	if _, err := WriteAll(ctx, w, pfFQDN, "decisions/allow-a.yaml", nil, "", pfChange()); err != nil {
		t.Fatalf("WriteAll delete: %v", err)
	}
	commits = w.Commits()
	if len(commits) != 2 || !reflect.DeepEqual(commits[1].Paths, want) {
		t.Fatalf("delete commits = %+v, want a second commit of %q", commits, want)
	}
	for _, p := range want {
		if _, ok := pfRead(t, w, p); ok {
			t.Fatalf("%s still exists after the delete", p)
		}
	}
}

// TestPolicyFilesWriteAllRefusals: a path that is not a file below the
// policy folder, an index file, a change that already names its scope,
// repository or files, a stale compare-and-swap value, and a Sovereign
// with no region commit nothing.
func TestPolicyFilesWriteAllRefusals(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	if _, err := WriteAll(ctx, w, pfFQDN, ValuesFile, []byte("a: 1\n"), "", pfChange()); !errors.Is(err, ErrNoRegion) {
		t.Fatalf("no region: got %v, want ErrNoRegion", err)
	}
	pfRegion(w, "r1")
	pfRegion(w, "r2")
	for _, rel := range []string{"", "/a.yaml", "a/", "../root/vars.yaml", "decisions/../../root/a.yaml", "a//b.yaml", "./a.yaml", "kustomization.yaml", "decisions/kustomization.yml", "exceptions/Kustomization"} {
		if _, err := WriteAll(ctx, w, pfFQDN, rel, []byte("a: 1\n"), "", pfChange()); !errors.Is(err, gitwrite.ErrInvalid) {
			t.Errorf("rel %q: got %v, want ErrInvalid", rel, err)
		}
	}
	withFiles := pfChange()
	withFiles.Files = []gitwrite.FileChange{{Path: "x.yaml", Op: gitwrite.OpUpsert}}
	withRepo := pfChange()
	withRepo.Repo = gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"}
	withScope := pfChange()
	withScope.Scope = gitwrite.FleetIntent(pfFQDN)
	for name, c := range map[string]gitwrite.Change{"files": withFiles, "repository": withRepo, "scope": withScope} {
		if _, err := WriteAll(ctx, w, pfFQDN, ValuesFile, []byte("a: 1\n"), "", c); !errors.Is(err, gitwrite.ErrInvalid) {
			t.Errorf("a change with its %s set: got %v, want ErrInvalid", name, err)
		}
	}
	w.Put(FleetRepo, pfPath("r1", ValuesFile), []byte("v: 1\n"))
	stale := strings.Repeat("0", 40)
	_, err := WriteAll(ctx, w, pfFQDN, ValuesFile, []byte("v: 2\n"), stale, pfChange())
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Path != pfPath("r1", ValuesFile) {
		t.Fatalf("stale ifMatch: got %v, want a ConflictError on the first region's copy", err)
	}
	if n := len(w.Commits()); n != 0 {
		t.Fatalf("%d commits after refusals, want 0", n)
	}
	if got, _ := pfRead(t, w, pfPath("r1", ValuesFile)); got != "v: 1\n" {
		t.Fatalf("the refused write changed the file: %q", got)
	}
}

// TestPolicyFilesSerialize: the label, the change identifier and the
// annotation that turns substitution off; the exact bytes of the empty
// document; the input is not changed; no object or no identifier refuses.
func TestPolicyFilesSerialize(t *testing.T) {
	const id = "01JCDZ7K9M3Q4T5V6W7X8Y9Z0A"
	in := EmptyValuesDocument()
	b, err := Serialize(in, id)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if string(b) != pfEmptyDoc {
		t.Fatalf("the empty document:\n%s\nwant:\n%s", b, pfEmptyDoc)
	}
	var got unstructured.Unstructured
	if err := yaml.Unmarshal(b, &got.Object); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v := got.GetAnnotations()["kustomize.toolkit.fluxcd.io/substitute"]; v != "disabled" {
		t.Fatalf("substitute annotation = %q, want disabled", v)
	}
	if v := got.GetAnnotations()["catalyst.openova.io/change-id"]; v != id {
		t.Fatalf("change-id annotation = %q, want %s", v, id)
	}
	if v := got.GetLabels()["app.kubernetes.io/managed-by"]; v != "flux" {
		t.Fatalf("managed-by label = %q, want flux", v)
	}
	data, _, _ := unstructured.NestedStringMap(got.Object, "data")
	if !reflect.DeepEqual(data, map[string]string{"values.yaml": "{}\n"}) {
		t.Fatalf("data = %q, want values.yaml {}", data)
	}
	if len(in.GetAnnotations()) != 0 {
		t.Fatalf("Serialize changed its input: %v", in.GetAnnotations())
	}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kyverno.io/v2", "kind": "PolicyException",
		"metadata": map[string]interface{}{"name": "w-1", "namespace": "acme", "annotations": map[string]interface{}{"reason": "uses ${HOME}"}},
		"status":   map[string]interface{}{"ready": true},
	}}
	b, err = Serialize(other, id)
	if err != nil {
		t.Fatalf("Serialize another kind: %v", err)
	}
	got = unstructured.Unstructured{}
	if err := yaml.Unmarshal(b, &got.Object); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.GetAnnotations()["kustomize.toolkit.fluxcd.io/substitute"] != "disabled" || got.GetAnnotations()["reason"] != "uses ${HOME}" || got.Object["status"] != nil {
		t.Fatalf("another kind serialised as %s", b)
	}
	if _, err := Serialize(nil, id); !errors.Is(err, gitwrite.ErrInvalid) {
		t.Fatalf("no object: got %v, want ErrInvalid", err)
	}
	if _, err := Serialize(EmptyValuesDocument(), ""); !errors.Is(err, gitwrite.ErrInvalid) {
		t.Fatalf("no change identifier: got %v, want ErrInvalid", err)
	}
}
