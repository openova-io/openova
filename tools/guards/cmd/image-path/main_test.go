package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// origin starts the stand-in and returns its base URL and a node map whose
// every proxy name and the hosted rewrite of ghcr.io point at it, the shape
// of the map the provider modules build.
func origin(t *testing.T, opt stubOptions) (string, string) {
	t.Helper()
	srv := httptest.NewServer(newStub(opt))
	t.Cleanup(srv.Close)
	m := map[string]mirror{
		"ghcr.io": {Endpoint: []string{srv.URL}, Rewrite: map[string]string{
			"^openova-io/openova/(.*)": "openova-io/openova/$1",
			"^(openova-io/(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^/]).*|(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^-]|openova-[^i]|openova-i[^o]).*)": "proxy-ghcr/$0",
		}},
		"docker.io": {Endpoint: []string{srv.URL}, Rewrite: map[string]string{"(.*)": "proxy-dockerhub/$1"}},
		"*":         {},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return srv.URL, string(b)
}

func runWith(t *testing.T, env map[string]string, stdin string, args ...string) (int, string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	rc := run(args, strings.NewReader(stdin), &out, &errb)
	return rc, out.String() + errb.String()
}

func probeEnv(mirrors, user, token string) map[string]string {
	return map[string]string{"PROBE_MIRRORS": mirrors, "PROBE_PROXY_PROJECTS": `["proxy-ghcr","proxy-dockerhub"]`,
		"PROBE_USER": user, "PROBE_TOKEN": token}
}

func want(t *testing.T, label string, rc, wantRC int, out, pattern string) {
	t.Helper()
	if rc != wantRC || !regexp.MustCompile(pattern).MatchString(out) {
		t.Errorf("%s: rc=%d (want %d), output does not match %q:\n%s", label, rc, wantRC, pattern, out)
	}
}

const robot = "robot$openova-bot"

func TestProbe(t *testing.T) {
	opt := stubOptions{user: robot, password: "node-token", fillAfter: 300 * time.Millisecond,
		absent:    map[string]bool{"openova-io/openova/gone": true},
		indexed:   map[string]bool{"openova-io/openova/multi": true},
		childless: map[string]bool{"openova-io/openova/half": true}}
	_, mirrors := origin(t, opt)
	fast := []string{"probe", "-fill-wait", "3s", "-fill-every", "100ms"}
	env := probeEnv(mirrors, robot, "node-token")

	rc, out := runWith(t, env, "ghcr.io/healthy/app:16\nghcr.io/openova-io/openova/catalyst-api:1\ndocker.io/redirected/app:1\n", fast...)
	want(t, "healthy, first-party and redirected", rc, 0, out, `(?m)^IMAGE-PATH PASS 3 images resolved through the origin \(index\+child\+config\+layers\)$`)
	want(t, "  the layers were read", rc, 0, out, `(?m)^PASS ghcr.io/healthy/app:16 -> .*/proxy-ghcr/healthy/app:16 child=sha256:aaaaaaaaaaaa blobs=3$`)

	rc, out = runWith(t, env, "ghcr.io/healthy/app:16\nghcr.io/damaged/app:16\n", fast...)
	want(t, "index served, child unknown", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/2: ghcr.io/damaged/app:16 \(DAMAGED CACHE ENTRY\)$`)
	want(t, "  the repository is named", rc, 1, out, `DAMAGED CACHE ENTRY http://127\.0\.0\.1:\d+/proxy-ghcr/damaged/app \[`)

	rc, out = runWith(t, env, "ghcr.io/filling/app:1\n", fast...)
	want(t, "not cached yet, then filled", rc, 0, out, `(?m)^PASS ghcr.io/filling/app:1 .*\(was not cached yet: filled after`)
	want(t, "  and the summary says so", rc, 0, out, `(?m)^  ℹ 1 image\(s\) were not cached yet at the origin and filled within 3s$`)

	rc, out = runWith(t, env, "ghcr.io/neverfill/app:1\n", "probe", "-fill-wait", "500ms", "-fill-every", "100ms")
	want(t, "not cached yet and never filled", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/neverfill/app:1 \(NEVER FILLED\)$`)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/gone:920753e\n", fast...)
	want(t, "first-party image absent from the hosted project", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/openova-io/openova/gone:920753e \(MISSING AT THE ORIGIN\)$`)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/multi:1\n", fast...)
	want(t, "a first-party index with its child", rc, 0, out, `(?m)^PASS ghcr.io/openova-io/openova/multi:1 -> .*/openova-io/openova/multi:1 child=sha256:[0-9a-f]{12} blobs=2$`)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/half:1\n", fast...)
	want(t, "a hosted index whose child is absent is missing at the origin", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/openova-io/openova/half:1 \(MISSING AT THE ORIGIN\)$`)
	want(t, "  with the remedy to publish it again", rc, 1, out, `(?m)^FAIL ghcr.io/openova-io/openova/half:1 MISSING AT THE ORIGIN .*/openova-io/openova/half:1 \[the hosted project openova-io serves the index, but its linux/amd64 child sha256:[0-9a-f]{12} is absent: publish it again\]$`)
	if strings.Contains(out, "DAMAGED CACHE ENTRY") {
		t.Errorf("a hosted project is not a cache, its miss is not a damaged cache entry:\n%s", out)
	}

	rc, out = runWith(t, env, "busybox\n", fast...)
	want(t, "a bare name is walked as docker.io/library/<name>:latest", rc, 1, out, `(?m)^FAIL busybox NOT AT THE ORIGIN .*/proxy-dockerhub/library/busybox:latest `)
	if strings.Contains(out, "SKIP") || strings.Contains(out, "skipped") {
		t.Errorf("a bare name is a pull, never skipped:\n%s", out)
	}

	rc, out = runWith(t, probeEnv(mirrors, robot, ""), "ghcr.io/healthy/app:16\n", fast...)
	want(t, "no credential: nothing is read", rc, 2, out, `(?m)^IMAGE-PATH FAIL 0/0: no credential`)

	rc, out = runWith(t, probeEnv(mirrors, robot, "wrong"), "ghcr.io/healthy/app:16\n", fast...)
	want(t, "a refused credential", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/healthy/app:16 \(CREDENTIAL REFUSED\)$`)

	rc, out = runWith(t, env, "", fast...)
	want(t, "an empty list", rc, 2, out, `(?m)^IMAGE-PATH FAIL 0/0: no images extracted$`)

	rc, out = runWith(t, env, "quay.io/cilium/cilium:v1\n", fast...)
	want(t, "a host no mirror serves before the fire", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: quay.io/cilium/cilium:v1 \(NO NODE PATH\)$`)
}

func TestOriginHas(t *testing.T) {
	ghcr := httptest.NewServer(newStub(stubOptions{}))
	t.Cleanup(ghcr.Close)
	base, _ := origin(t, stubOptions{user: "robot$openova-warmup", password: "pw",
		absent: map[string]bool{"openova-io/openova/continuum-controller": true},
		stale:  map[string]bool{"openova-io/openova/catalyst-ui": true}})
	env := map[string]string{"ORIGIN_USER": "robot$openova-warmup", "ORIGIN_TOKEN": "pw", "GHCR_USER": "", "GHCR_TOKEN": ""}
	args := []string{"origin-has", "-origin", base, "-ghcr", ghcr.URL}

	rc, out := runWith(t, env, "ghcr.io/openova-io/openova/catalyst-api:1\nghcr.io/openova-io/openova/sovereign-ops:fe65309\n", args...)
	want(t, "every image at the origin", rc, 0, out, `(?m)^ORIGIN-HAS PASS 2 first-party image\(s\) at `)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/catalyst-api:1\nghcr.io/openova-io/openova/continuum-controller:920753e\n", args...)
	want(t, "one image absent", rc, 1, out, `(?m)^MISSING AT THE ORIGIN ghcr.io/openova-io/openova/continuum-controller:920753e: .*/openova-io/openova/continuum-controller:920753e is absent \(ghcr has sha256:`)
	want(t, "  and the cut is called incoherent", rc, 1, out, `(?m)^ORIGIN-HAS FAIL 1/2 `)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/catalyst-ui:1\n", args...)
	want(t, "the tag carries another digest", rc, 1, out, `(?m)^MISSING AT THE ORIGIN ghcr.io/openova-io/openova/catalyst-ui:1: .* carries sha256:[0-9a-f]+, ghcr has sha256:`)

	// ghcr publishes an index; the warm-up's `skopeo copy` (no --all) writes
	// its linux/amd64 child under the tag, so the origin's tag is the child.
	idx := map[string]bool{"openova-io/openova/sovereign-ops": true}
	ghcrIdx := httptest.NewServer(newStub(stubOptions{indexed: idx}))
	t.Cleanup(ghcrIdx.Close)
	argsIdx := []string{"origin-has", "-origin", base, "-ghcr", ghcrIdx.URL}
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/sovereign-ops:fe65309\n", argsIdx...)
	want(t, "an index on ghcr, its linux/amd64 child at the origin as the warm-up copies it", rc, 0, out, `(?m)^ORIGIN-HAS PASS 1 `)
	whole, _ := origin(t, stubOptions{user: "robot$openova-warmup", password: "pw", indexed: idx})
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/sovereign-ops:fe65309\n", "origin-has", "-origin", whole, "-ghcr", ghcrIdx.URL)
	want(t, "an index on ghcr, the same index at the origin (a copy with --all)", rc, 0, out, `(?m)^ORIGIN-HAS PASS 1 `)
	half, _ := origin(t, stubOptions{user: "robot$openova-warmup", password: "pw", childless: idx})
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/sovereign-ops:fe65309\n", "origin-has", "-origin", half, "-ghcr", ghcrIdx.URL)
	want(t, "an index at the origin whose child is absent", rc, 1, out, `(?m)^MISSING AT THE ORIGIN ghcr.io/openova-io/openova/sovereign-ops:fe65309: .* is an index whose linux/amd64 child sha256:[0-9a-f]+ is absent: publish it again$`)
	staleIdx, _ := origin(t, stubOptions{user: "robot$openova-warmup", password: "pw", stale: idx})
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/sovereign-ops:fe65309\n", "origin-has", "-origin", staleIdx, "-ghcr", ghcrIdx.URL)
	want(t, "an index on ghcr, another image at the origin", rc, 1, out, `(?m)^MISSING AT THE ORIGIN ghcr.io/openova-io/openova/sovereign-ops:fe65309: .* carries sha256:[0-9a-f]+, ghcr has sha256:[0-9a-f]+ \(the linux/amd64 image a node pulls\)$`)

	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/catalyst-api:1\n", "origin-has", "-origin", "http://127.0.0.1:1", "-ghcr", ghcr.URL)
	want(t, "the origin cannot be reached", rc, 2, out, `(?m)^ORIGIN-HAS CANNOT READ 1/1`)

	env["ORIGIN_TOKEN"] = ""
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/catalyst-api:1\n", args...)
	want(t, "no origin credential", rc, 2, out, `(?m)^ORIGIN-HAS CANNOT READ: no origin credential`)
}

func TestResolve(t *testing.T) {
	_, mirrors := origin(t, stubOptions{})
	var m map[string]mirror
	if err := json.Unmarshal([]byte(mirrors), &m); err != nil {
		t.Fatal(err)
	}
	for ref, wantRepo := range map[string]string{
		"ghcr.io/openova-io/openova/catalyst-api:1": "openova-io/openova/catalyst-api",
		"ghcr.io/openova-io/other:1":                "proxy-ghcr/openova-io/other",
		"ghcr.io/cloudnative-pg/postgresql:16":      "proxy-ghcr/cloudnative-pg/postgresql",
		"busybox:1.36":                              "proxy-dockerhub/library/busybox",
		"busybox":                                   "proxy-dockerhub/library/busybox",
	} {
		if r := resolve(ref, m); r.kind != "ok" || r.repo != wantRepo {
			t.Errorf("%s -> %+v, want repo %s", ref, r, wantRepo)
		}
	}
	if r := resolve("registry.k8s.io/pause:3.9@sha256:0123", m); r.kind != "fail" {
		t.Errorf("a host only * serves, with no endpoint, must fail: %+v", r)
	}
}

func TestSubst(t *testing.T) {
	dir := t.TempDir()
	vars := filepath.Join(dir, "v.env")
	if err := os.WriteFile(vars, []byte("# comment\nON=true\nEMPTY=\nNAME=a=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := "a: ${ON:-false}\nb: ${EMPTY:-d}\nc: ${EMPTY-d}\nd: ${UNSET}\ne: ${UNSET:=${NAME}}\nf: $${ON}\ng: $ON\n"
	rc, out := runWith(t, nil, in, "subst", "-vars", vars)
	if rc != 0 || out != "a: true\nb: d\nc: \nd: \ne: a=b\nf: ${ON}\ng: $ON\n" {
		t.Errorf("rc=%d out=%q", rc, out)
	}
	rc, _ = runWith(t, nil, "x: ${ON:?required}\n", "subst", "-vars", vars)
	if rc != 2 {
		t.Errorf("an unsupported form must stop the substitution, rc=%d", rc)
	}
}
