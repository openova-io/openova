package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
)

// tree is a repository with the real capability schema, one Blueprint and no contract file.
func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	schema, err := os.ReadFile(filepath.Join("../../../../../../../..", capability.SchemaPath))
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string][]byte{
		capability.SchemaPath:                         schema,
		"platform/cilium/blueprint.yaml":              []byte("apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata: {name: bp-cilium}\n"),
		"products/.keep":                              nil,
		filepath.Dir(capability.IndexPath) + "/.keep": nil,
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRun(t *testing.T) {
	root := tree(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"-root", root, "--check"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "is stale; run sh products/catalyst/bootstrap/api/internal/capability/write.sh") {
		t.Fatalf("--check without an index: exit %d, stderr %q; want 1 and the stale line", code, errOut.String())
	}
	if code := run([]string{"-root", root}, &out, &errOut); code != 0 {
		t.Fatalf("write: exit %d, stderr %q", code, errOut.String())
	}
	written, err := fs.ReadFile(os.DirFS(root), capability.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "\"capabilities\": []") {
		t.Fatalf("written index:\n%s", written)
	}
	if code := run([]string{"-root", root, "--check"}, &out, &errOut); code != 0 {
		t.Fatalf("--check after write: exit %d", code)
	}
	if err := os.WriteFile(filepath.Join(root, capability.IndexPath), append(written, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-root", root, "--check"}, &out, &errOut); code != 1 {
		t.Fatalf("--check on a hand-edited index: exit %d, want 1", code)
	}
	if err := os.WriteFile(filepath.Join(root, "platform/cilium/blueprint.yaml"), []byte("spec: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"-root", root, "--check"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "platform/cilium/blueprint.yaml: not YAML") {
		t.Fatalf("a wrong input: exit %d, stderr %q; want 2 naming the file", code, errOut.String())
	}
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no -root: exit %d, want 2", code)
	}
}
