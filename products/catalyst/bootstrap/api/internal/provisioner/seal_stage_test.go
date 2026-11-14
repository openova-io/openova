// seal_stage_test.go — the provisioner stages the seal hook of a provider
// pack (SEC-150).
//
// Contract C8 (docs/contracts/c8-seal-provider.md): the seal hook of a pack
// is the OpenTofu child module infra/providers/<name>/seal, and the pack's
// main.tf calls it as module "seal" with source "./seal". The provisioner
// runs OpenTofu in a workdir of its own, into which stageModule copies the
// module. A hook that is not copied passes every test that runs in the
// repository and fails the first real provision at `tofu init`. These tests
// stage a module the way a provision does.
package provisioner

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeTree writes files, given as path relative to root → content.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// TestStageModule_StagesSealHook: the .tf files of <module>/seal land in
// <workdir>/seal, and nothing else of the module's directories does.
func TestStageModule_StagesSealHook(t *testing.T) {
	root := t.TempDir()
	const hook = "output \"kind\" { value = \"static-node-file\" }\n"
	writeTree(t, root, map[string]string{
		"providers/a/main.tf":                "module \"seal\" {\n  source = \"./seal\"\n}\n",
		"providers/a/seal/main.tf":           hook,
		"providers/a/seal/notes.md":          "not a module file\n",
		"providers/a/tests/a.tftest.hcl":     "run \"x\" {}\n",
		"providers/_shared/cloudinit.tftpl":  "#cloud-config\n",
		"providers/a/seal/nested/nested.tf":  "locals {}\n",
		"providers/a/seal/extra.tftpl":       "x\n",
		"providers/a/cloudinit-worker.tftpl": "#cloud-config\n",
	})
	dst := filepath.Join(root, "work", "dep-1")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := stageModule(filepath.Join(root, "providers", "a"), dst); err != nil {
		t.Fatalf("stageModule: %v", err)
	}
	got, err := fs.ReadFile(os.DirFS(dst), "seal/main.tf")
	if err != nil {
		t.Fatalf("SEC-150: the seal hook was not staged, so `tofu init` would fail on module \"seal\": %v", err)
	}
	if string(got) != hook {
		t.Errorf("staged seal/main.tf = %q; want the bytes of the source file", got)
	}
	for _, staged := range []string{"main.tf", "cloudinit-worker.tftpl", filepath.Join("seal", "extra.tftpl")} {
		if _, err := os.Stat(filepath.Join(dst, staged)); err != nil {
			t.Errorf("%s was not staged: %v", staged, err)
		}
	}
	for _, absent := range []string{filepath.Join("seal", "notes.md"), filepath.Join("seal", "nested"), "tests"} {
		if _, err := os.Stat(filepath.Join(dst, absent)); !os.IsNotExist(err) {
			t.Errorf("%s must not be staged (stat error: %v)", absent, err)
		}
	}
}

// TestStageModule_WithoutSealHook: a module without the directory stages as
// before, and no empty seal directory appears in the workdir.
func TestStageModule_WithoutSealHook(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"providers/a/main.tf": "locals {}\n"})
	dst := filepath.Join(root, "work", "dep-1")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := stageModule(filepath.Join(root, "providers", "a"), dst); err != nil {
		t.Fatalf("stageModule: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "seal")); !os.IsNotExist(err) {
		t.Errorf("a module without a seal hook must not get a seal directory (stat error: %v)", err)
	}
}

// localModuleSource finds a module call with a local source in a .tf file.
var localModuleSource = regexp.MustCompile(`(?m)^[ \t]*source[ \t]*=[ \t]*"(\.\.?/[^"]*)"`)

// TestStageModule_EveryLocalChildModuleOfAPackIsStaged reads the provider
// packs of this repository. Every child module a pack calls by a local path
// must be in its staged workdir. stageModule stages the seal hook and
// nothing else, so a pack that calls another local module fails here, not
// in a provision.
func TestStageModule_EveryLocalChildModuleOfAPackIsStaged(t *testing.T) {
	providersDir := filepath.Dir(modulePath(t)) // <repo>/infra/providers
	packs, err := os.ReadDir(providersDir)
	if err != nil {
		t.Skipf("provider packs not found: %v", err)
	}
	calls := 0
	for _, pack := range packs {
		if !pack.IsDir() || strings.HasPrefix(pack.Name(), "_") {
			continue
		}
		src := filepath.Join(providersDir, pack.Name())
		work := t.TempDir()
		dst := filepath.Join(work, "dep-1")
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatalf("mkdir workdir: %v", err)
		}
		if err := stageModule(src, dst); err != nil {
			t.Fatalf("stageModule(%s): %v", pack.Name(), err)
		}
		module := os.DirFS(src)
		files, err := fs.Glob(module, "*.tf")
		if err != nil {
			t.Fatalf("glob %s: %v", src, err)
		}
		for _, f := range files {
			raw, err := fs.ReadFile(module, f)
			if err != nil {
				t.Fatalf("read %s of pack %s: %v", f, pack.Name(), err)
			}
			for _, m := range localModuleSource.FindAllStringSubmatch(string(raw), -1) {
				calls++
				child := filepath.Join(dst, m[1])
				entries, err := filepath.Glob(filepath.Join(child, "*.tf"))
				if err != nil || len(entries) == 0 {
					t.Errorf("pack %s: %s calls the local module %q, and the staged workdir has no .tf file at %s. stageModule stages only <module>/seal (contract C8).", pack.Name(), f, m[1], child)
				}
			}
		}
	}
	if calls == 0 {
		t.Errorf("no provider pack calls a local child module: every pack must call its seal hook as module \"seal\" with source \"./seal\" (contract C8)")
	}
}
