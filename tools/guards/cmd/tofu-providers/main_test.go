package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	dWidget = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	dGadget = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	dRandom = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

// tree is a repository the tests build: two packs (alpha, beta), their modules,
// lock files and lists, the composition with its inline module, and the shared
// list as write-list.sh writes it.
type tree struct {
	t    *testing.T
	root string
}

func (tr tree) write(rel, text string) {
	tr.t.Helper()
	p := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr tree) regenerate() {
	tr.t.Helper()
	if _, problems, err := writeList(tr.root, false); err != nil || len(problems) > 0 {
		tr.t.Fatalf("writeList: %v %v", err, problems)
	}
}

func lock(pins ...string) string {
	var b strings.Builder
	b.WriteString("# This file is maintained automatically by \"tofu init\".\n")
	for i := 0; i < len(pins); i += 2 {
		b.WriteString("\nprovider \"registry.opentofu.org/" + pins[i] + "\" {\n  version     = \"" + pins[i+1] + "\"\n  constraints = \"~> 1.0\"\n  hashes = [\n    \"zh:00\",\n  ]\n}\n")
	}
	return b.String()
}

func newTree(t *testing.T) tree {
	tr := tree{t: t, root: t.TempDir()}
	tr.write("infra/providers/alpha/pack.yaml", `apiVersion: catalyst.openova.io/v1alpha1
kind: ProviderPack
spec:
  tofuProviders:
    - source: registry.opentofu.org/acme/widget
    - source: registry.opentofu.org/hashicorp/random
  leakTokens: [alpha]
`)
	tr.write("infra/providers/alpha/versions.tf", `terraform {
  required_version = ">= 1.6.0"
  required_providers {
    # the widget API; "~> 9.0" in this comment is not read
    widget = {
      source  = "acme/widget"
      version = "~> 1.2"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6, < 4.0"
    }
  }
}
`)
	tr.write("infra/providers/alpha/seal/main.tf", `terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}
`)
	// A test folder is not a module of the pack: what it requires is not judged.
	tr.write("infra/providers/alpha/tests/fixture.tf", `terraform {
  required_providers {
    other = { source = "acme/other", version = "= 0.0.1" }
  }
}
`)
	tr.write("infra/providers/alpha/.terraform.lock.hcl", lock("acme/widget", "1.4.0", "hashicorp/random", "3.9.1"))
	tr.write("infra/providers/alpha/"+packListName, "# alpha's packages\n\nregistry.opentofu.org/acme/widget 1.4.0 "+dWidget+"\nregistry.opentofu.org/hashicorp/random 3.9.1 "+dRandom+"\n")
	tr.write("infra/providers/beta/pack.yaml", "spec:\n  tofuProviders: [{source: registry.opentofu.org/acme/gadget}]\n")
	tr.write("infra/providers/beta/versions.tf", "terraform {\n  required_providers {\n    gadget = { source = \"acme/gadget\", version = \"!= 2.0.0, <= 2.1.0\" }\n  }\n}\n")
	tr.write("infra/providers/beta/.terraform.lock.hcl", lock("acme/gadget", "2.1.0"))
	tr.write("infra/providers/beta/"+packListName, "registry.opentofu.org/acme/gadget 2.1.0 "+dGadget+"\n")
	tr.write(compositionPath, `apiVersion: apiextensions.crossplane.io/v1
kind: Composition
spec:
  pipeline:
    - step: patch-and-transform
      input:
        resources:
{{- include "x.resources" . | replace "\n" "\n      " }}
{{- define "x.resources" }}
    - name: adoption-workspace
      base:
        spec:
          forProvider:
            # required_providers in a YAML comment is not a block
            module: |
              variable "cloud" { type = string }
              terraform {
                required_providers {
                  widget = { source = "acme/widget", version = "> 1.3" }
                  gadget = { source = "acme/gadget", version = "~> 2.1.0" }
                }
              }
{{- end }}
`)
	tr.regenerate()
	return tr
}

func findings(t *testing.T, root string) []string {
	t.Helper()
	res, err := check(root)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	return res.findings
}

// wantFinding holds when exactly one finding holds every part.
func wantFinding(t *testing.T, got []string, parts ...string) {
	t.Helper()
	n := 0
	for _, f := range got {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(f, p)
		}
		if all {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one finding with %q, got %d in %q", parts, n, got)
	}
}

func TestTofuProvidersTreePasses(t *testing.T) {
	tr := newTree(t)
	res, err := check(tr.root)
	if err != nil || len(res.findings) > 0 {
		t.Fatalf("check: %v %q", err, res.findings)
	}
	// composition 2, alpha's module 2 and seal 1, beta 1; the test folder is left out.
	if res.listed != 3 || res.requirements != 6 || res.pins != 3 {
		t.Fatalf("listed %d, requirements %d, pins %d; want 3, 6, 3", res.listed, res.requirements, res.pins)
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"check", "-root", tr.root}, &out, &errOut); code != 0 {
		t.Fatalf("check exit %d: %s %s", code, out.String(), errOut.String())
	}
	if code := run(context.Background(), []string{"list", "-check", "-root", tr.root}, &out, &errOut); code != 0 {
		t.Fatalf("list -check exit %d: %s", code, out.String())
	}
	have, _ := os.ReadFile(filepath.Join(tr.root, listPath))
	if !strings.HasPrefix(string(have), "# generated by bash "+writerScript) ||
		!strings.Contains(string(have), "  providers: |\n    registry.opentofu.org/acme/gadget 2.1.0 "+dGadget+"\n    registry.opentofu.org/acme/widget 1.4.0 "+dWidget+"\n    registry.opentofu.org/hashicorp/random 3.9.1 "+dRandom+"\n") {
		t.Fatalf("the list is not the sorted union of the packs' lists:\n%s", have)
	}
	if code := run(context.Background(), []string{"check", "-root", filepath.Join(tr.root, "absent")}, &out, &errOut); code != 2 {
		t.Fatalf("check of a tree that cannot be read: exit %d, want 2", code)
	}
}

func TestTofuProvidersConstraintUnmet(t *testing.T) {
	ops := []struct {
		c, v string
		ok   bool
	}{
		{"= 1.2.3", "1.2.3", true}, {"1.2.3", "1.2.4", false}, {"!= 1.2.3", "1.2.3", false}, {"!= 1.2.3", "1.2.4", true},
		{"> 1.2", "1.2.0", false}, {"> 1.2", "1.2.1", true}, {">= 1.2", "1.2.0", true}, {">= 1.2", "1.1.9", false},
		{"< 2", "1.9.9", true}, {"< 2", "2.0.0", false}, {"<= 2.0", "2.0.0", true}, {"<= 2.0", "2.0.1", false},
		{"~> 1.65", "1.91.0", true}, {"~> 1.65", "2.0.0", false}, {"~> 1.65", "1.64.9", false},
		{"~> 1.2.3", "1.2.9", true}, {"~> 1.2.3", "1.3.0", false}, {"~> 1", "1.9.0", true}, {"~> 1", "2.0.0", false},
		{">= 3.6, < 4.0", "3.9.1", true}, {">= 3.6, < 4.0", "4.0.0", false},
	}
	for _, o := range ops {
		terms, err := parseConstraint(o.c)
		v, _, verr := parseVersion(o.v)
		if err != nil || verr != nil || meets(v, terms) != o.ok {
			t.Errorf("%s meets %q: want %v (%v %v)", o.v, o.c, o.ok, err, verr)
		}
	}
	if _, err := parseConstraint(">= x.1"); err == nil {
		t.Errorf("a constraint with a version that is not a number was read")
	}

	tr := newTree(t)
	tr.write(compositionPath, strings.Replace(readFile(t, tr.root, compositionPath), `version = "> 1.3"`, `version = "~> 2.0"`, 1))
	wantFinding(t, findings(t, tr.root), compositionPath, "registry.opentofu.org/acme/widget", `"~> 2.0"`, "lists 1.4.0, which does not meet it")

	tr = newTree(t)
	tr.write("infra/providers/beta/versions.tf", "terraform {\n  required_providers {\n    gadget = { source = \"acme/gadget\", version = \"< 2.1\" }\n    extra  = \"~> 1.0\"\n  }\n}\n")
	got := findings(t, tr.root)
	wantFinding(t, got, "infra/providers/beta/versions.tf", "registry.opentofu.org/acme/gadget", `"< 2.1"`)
	wantFinding(t, got, "infra/providers/beta/versions.tf", "registry.opentofu.org/hashicorp/extra", "does not list it")
}

func TestTofuProvidersLockedVersionMissing(t *testing.T) {
	tr := newTree(t)
	tr.write("infra/providers/alpha/seal/.terraform.lock.hcl", lock("hashicorp/random", "3.9.2"))
	wantFinding(t, findings(t, tr.root), "infra/providers/alpha/seal/.terraform.lock.hcl pins registry.opentofu.org/hashicorp/random 3.9.2", "does not list that version")

	tr = newTree(t)
	tr.write("infra/providers/beta/.terraform.lock.hcl", lock("acme/gadget", "2.1.0", "acme/spare", "0.1.0"))
	wantFinding(t, findings(t, tr.root), "pins registry.opentofu.org/acme/spare 0.1.0")
}

func TestTofuProvidersPackSourceMissing(t *testing.T) {
	tr := newTree(t)
	tr.write("infra/providers/beta/pack.yaml", "spec:\n  tofuProviders:\n  - source: registry.opentofu.org/acme/gadget\n  - source: \"registry.opentofu.org/acme/sprocket\"\n  charts: []\n")
	wantFinding(t, findings(t, tr.root), "infra/providers/beta/pack.yaml names registry.opentofu.org/acme/sprocket", "does not list it")
}

func TestTofuProvidersDigestMalformed(t *testing.T) {
	for _, bad := range []string{
		"registry.opentofu.org/acme/gadget 2.1.0 sha256:2222",                 // a short digest
		"registry.opentofu.org/acme/gadget 2.1.0 " + strings.ToUpper(dGadget), // not lower-case hex
		"registry.opentofu.org/acme/gadget 2.1.0",                             // no digest
		"registry.opentofu.org/acme/gadget 2.1 " + dGadget,                    // a version of two parts
	} {
		tr := newTree(t)
		tr.write("infra/providers/beta/"+packListName, bad+"\n")
		wantFinding(t, findings(t, tr.root), "infra/providers/beta/"+packListName+":1:", "is not <registry.opentofu.org/namespace/type>")
	}
	tr := newTree(t)
	tr.write("infra/providers/beta/"+packListName, "registry.opentofu.org/acme/gadget 2.1.0 "+dGadget+"\nregistry.opentofu.org/acme/gadget 2.0.0 "+dGadget+"\n")
	wantFinding(t, findings(t, tr.root), "infra/providers/beta/"+packListName+":2:", "listed twice (first at line 1)")

	tr = newTree(t)
	text := readFile(t, tr.root, listPath)
	tr.write(listPath, strings.Replace(text, dGadget, "sha256:22", 1))
	got := findings(t, tr.root)
	wantFinding(t, got, listPath+":", "acme/gadget 2.1.0 sha256:22")
	wantFinding(t, got, "is not what "+writerScript+" writes")
}

// TestTofuProvidersListIsGenerated: rule L5, the shared list is the packs' union.
func TestTofuProvidersListIsGenerated(t *testing.T) {
	tr := newTree(t)
	tr.write(listPath, strings.Replace(readFile(t, tr.root, listPath), "random 3.9.1", "random 3.9.0", 1))
	wantFinding(t, findings(t, tr.root), "is not what "+writerScript+" writes")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"list", "-check", "-root", tr.root}, &out, &errOut); code != 1 {
		t.Fatalf("list -check of a hand-edited list: exit %d, want 1", code)
	}
	if code := run(context.Background(), []string{"list", "-root", tr.root}, &out, &errOut); code != 0 || len(findings(t, tr.root)) != 0 {
		t.Fatalf("list did not write the list back: exit %d %q", code, findings(t, tr.root))
	}

	tr = newTree(t)
	tr.write("infra/providers/beta/"+packListName, "registry.opentofu.org/acme/gadget 2.1.0 "+dGadget+"\nregistry.opentofu.org/hashicorp/random 3.9.1 "+dGadget+"\n")
	wantFinding(t, findings(t, tr.root), "infra/providers/alpha/"+packListName+" lists registry.opentofu.org/hashicorp/random 3.9.1", "one source has one version and one digest")
	if _, problems, _ := writeList(tr.root, false); len(problems) != 1 {
		t.Fatalf("writeList wrote a list from packs that disagree: %q", problems)
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(root, rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func blobOf(t *testing.T, dir, d string) []byte {
	t.Helper()
	return []byte(readFile(t, dir, filepath.Join("blobs", "sha256", strings.TrimPrefix(d, "sha256:"))))
}

func TestTofuProvidersPackIsDeterministic(t *testing.T) {
	zips := t.TempDir()
	z, err := writeStub(zips, "registry.opentofu.org/acme/widget", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(z) != "terraform-provider-widget_1.4.0_linux_amd64.zip" {
		t.Fatalf("stub name %s", z)
	}
	zr, err := zip.OpenReader(z)
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "terraform-provider-widget_v1.4.0" || zr.File[0].Mode()&0o111 == 0 {
		t.Fatalf("the stub is not one executable terraform-provider-widget_v1.4.0: %v", err)
	}
	_ = zr.Close()
	z2, _ := writeStub(t.TempDir(), "registry.opentofu.org/acme/widget", "1.4.0")
	if readFile(t, "/", z) != readFile(t, "/", z2) {
		t.Fatalf("two stubs of one version differ")
	}

	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	da, err := packLayout(a, "1.4.0", []string{z})
	if err != nil {
		t.Fatal(err)
	}
	db, err := packLayout(b, "1.4.0", []string{z})
	if err != nil || da != db || !digestRE.MatchString(da) {
		t.Fatalf("two runs: %s and %s (%v)", da, db, err)
	}
	tags, err := readLayout(a)
	if err != nil || len(tags) != 1 || tags["1.4.0"].Digest != da || tags["1.4.0"].ArtifactType != atProvider {
		t.Fatalf("the layout does not tag the index 1.4.0: %v %v", tags, err)
	}
	raw := blobOf(t, a, da)
	for _, banned := range []string{"annotations", "created", "org.opencontainers.image"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("the index holds %q: %s", banned, raw)
		}
	}
	var idx manifest
	if err := json.Unmarshal(raw, &idx); err != nil || idx.MediaType != mtIndex || idx.ArtifactType != atProvider || len(idx.Manifests) != 1 {
		t.Fatalf("index: %s (%v)", raw, err)
	}
	md := idx.Manifests[0]
	if md.MediaType != mtManifest || md.ArtifactType != atProviderTarget || md.Platform == nil || md.Platform.OS != "linux" || md.Platform.Architecture != "amd64" {
		t.Fatalf("manifest descriptor: %+v", md)
	}
	raw = blobOf(t, a, md.Digest)
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.ArtifactType != atProviderTarget || m.Config == nil || m.Config.MediaType != mtEmpty || len(m.Layers) != 1 || bytes.Contains(raw, []byte("annotations")) {
		t.Fatalf("manifest: %s (%v)", raw, err)
	}
	if m.Layers[0].MediaType != mtZip || m.Layers[0].Digest != digestOf([]byte(readFile(t, "/", z))) {
		t.Fatalf("the layer is not the zip as archive/zip: %+v", m.Layers[0])
	}

	if _, err := packLayout(filepath.Join(t.TempDir(), "c"), "1.5.0", []string{z}); err == nil {
		t.Fatalf("pack took a zip whose name gives another version")
	}
	if _, err := packLayout(a, "1.4.0", []string{z}); err == nil {
		t.Fatalf("pack wrote into a folder that is not empty")
	}
}

func TestTofuProvidersServeAnswersTheMirror(t *testing.T) {
	z, err := writeStub(t.TempDir(), "registry.opentofu.org/acme/widget", "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "layout")
	index, err := packLayout(dir, "1.4.0", []string{z})
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	ln, srv, err := startServe(map[string]string{"mirror/acme/widget": dir}, caFile)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	if !strings.HasPrefix(ln.Addr().String(), "127.0.0.1:") {
		t.Fatalf("serve listens on %s", ln.Addr())
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(readFile(t, "/", caFile))) {
		t.Fatalf("the CA file holds no certificate")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	get := func(method, path string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, "https://"+ln.Addr().String()+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	if r, _ := get("GET", "/v2/"); r.StatusCode != 200 {
		t.Fatalf("/v2/: %d", r.StatusCode)
	}
	if r, b := get("GET", "/v2/mirror/acme/widget/tags/list"); r.StatusCode != 200 || !strings.Contains(string(b), `"tags":["1.4.0"]`) {
		t.Fatalf("tags: %d %s", r.StatusCode, b)
	}
	r, byTag := get("GET", "/v2/mirror/acme/widget/manifests/1.4.0")
	if r.StatusCode != 200 || r.Header.Get("Docker-Content-Digest") != index || r.Header.Get("Content-Type") != mtIndex || digestOf(byTag) != index {
		t.Fatalf("manifest by tag: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := get("HEAD", "/v2/mirror/acme/widget/manifests/1.4.0"); r.StatusCode != 200 || r.Header.Get("Docker-Content-Digest") != index {
		t.Fatalf("HEAD manifest by tag: %d", r.StatusCode)
	}
	var idx manifest
	_ = json.Unmarshal(byTag, &idx)
	r, mb := get("GET", "/v2/mirror/acme/widget/manifests/"+idx.Manifests[0].Digest)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != mtManifest || digestOf(mb) != idx.Manifests[0].Digest {
		t.Fatalf("manifest by digest: %d %v", r.StatusCode, r.Header)
	}
	var m manifest
	_ = json.Unmarshal(mb, &m)
	if r, blob := get("GET", "/v2/mirror/acme/widget/blobs/"+m.Layers[0].Digest); r.StatusCode != 200 || string(blob) != readFile(t, "/", z) {
		t.Fatalf("blob: %d", r.StatusCode)
	}
	if r, _ := get("GET", "/v2/mirror/acme/other/tags/list"); r.StatusCode != 404 {
		t.Fatalf("a repository that is not served: %d", r.StatusCode)
	}
	if r, _ := get("GET", "/v2/mirror/acme/widget/manifests/9.9.9"); r.StatusCode != 404 {
		t.Fatalf("a tag that does not exist: %d", r.StatusCode)
	}
	if r, _ := get("GET", "/v2/mirror/acme/widget/blobs/sha256:"+strings.Repeat("0", 64)); r.StatusCode != 404 {
		t.Fatalf("a blob that does not exist: %d", r.StatusCode)
	}
}
