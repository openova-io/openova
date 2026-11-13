package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

// providers is infra/providers of this repository.
var providers = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")

// document is a complete installation document with neutral values; PACK is
// replaced by the name of a pack that requires an installation.
const document = `apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata: {name: site-a}
spec:
  extends: PACK
  lifecycle: proven
  api: {domain: example-cloud.test, authURL: "", insecureTLS: false}
  network: {apiCIDRs: ["192.0.2.0/24"]}
  regions:
    - {code: region-1, label: Region 1, location: Example City, zones: [region-1a], defaultZone: region-1a}
  compute:
    image: 00000000-0000-0000-0000-000000000000
    flavors:
      - {name: example.xlarge.8, vcpu: 4, memoryGiB: 32, roles: [control-plane, worker]}
    defaults: {controlPlaneFlavor: example.xlarge.8, workerFlavor: example.xlarge.8}
  registry: {mirrorEndpoint: "https://registry-mirror.example-cloud.test"}
`

// packRequiringInstallation is the first pack, by name, of this repository
// whose profile requires an installation.
func packRequiringInstallation(t *testing.T) string {
	t.Helper()
	profiles, err := cloudprofile.Load(providers)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var names []string
	for name, p := range profiles {
		if p.Spec.RequiresInstallation {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no pack requires an installation")
	}
	return names[0]
}

func writeDoc(t *testing.T, doc string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "site-a.yaml")
	if err := os.WriteFile(f, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRun(t *testing.T) {
	valid := strings.Replace(document, "PACK", packRequiringInstallation(t), 1)
	cases := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"valid document", []string{"-providers", providers, writeDoc(t, valid)}, 0, "name: cloud-installation-site-a", ""},
		{"refused document", []string{"-providers", providers, writeDoc(t, valid+"  storage: {defaultClass: other, driver: other}\n")}, 1, "", "refused: spec.storage: not an installation field"},
		{"no providers directory", []string{writeDoc(t, valid)}, 2, "", "usage:"},
		{"no document", []string{"-providers", providers}, 2, "", "usage:"},
		{"document cannot be read", []string{"-providers", providers, filepath.Join(t.TempDir(), "absent.yaml")}, 2, "", "no such file"},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		code := run(c.args, &stdout, &stderr)
		if code != c.code {
			t.Errorf("%s: exit code %d, want %d (stderr %q)", c.name, code, c.code, stderr.String())
		}
		if c.stdout == "" && stdout.Len() != 0 {
			t.Errorf("%s: standard output %q, want nothing", c.name, stdout.String())
		}
		if !strings.Contains(stdout.String(), c.stdout) || !strings.Contains(stderr.String(), c.stderr) {
			t.Errorf("%s: standard output %q, standard error %q", c.name, stdout.String(), stderr.String())
		}
	}
}
