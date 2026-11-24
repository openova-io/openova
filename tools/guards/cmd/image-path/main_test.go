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
	_, base, mirrors := originStub(t, opt)
	return base, mirrors
}

// originStub is origin that also returns the stand-in, to read what it was asked.
func originStub(t *testing.T, opt stubOptions) (*stub, string, string) {
	t.Helper()
	st := newStub(opt)
	srv := httptest.NewServer(st)
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
	return st, srv.URL, string(b)
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

// blobGets lists the blob GETs the stand-in received, "GET <path>".
func blobGets(st *stub) []string {
	var gets []string
	_, log := st.record()
	for _, l := range log {
		if strings.HasPrefix(l, "GET ") && strings.Contains(l, "/blobs/") {
			gets = append(gets, l)
		}
	}
	return gets
}

// blobMethods lists, in the order received, the methods of the requests the
// stand-in received for one blob.
func blobMethods(st *stub, digest string) []string {
	var methods []string
	_, log := st.record()
	for _, l := range log {
		if method, path, _ := strings.Cut(l, " "); strings.HasSuffix(path, "/blobs/"+digest) {
			methods = append(methods, method)
		}
	}
	return methods
}

func fillEnv(mirrors, origin string) map[string]string {
	e := probeEnv(mirrors, robot, "node-token")
	e["PROBE_ORIGIN"] = origin
	return e
}

// TestProbeFill: a proxied blob the origin stores only once it is asked for
// it by GET, behind a node path that asks by HEAD (20 November 2026: eight
// blobs of two cilium images, 67 to 351 bytes, warmed by hand before a fire).
func TestProbeFill(t *testing.T) {
	opt := stubOptions{user: robot, password: "node-token"}
	fast := []string{"probe", "-fill-wait", "300ms", "-fill-every", "100ms"}

	// At once: the blob is asked for by GET right after the HEAD that found it
	// missing, not after -fill-wait (1m here). Its requests are HEAD, GET,
	// HEAD, and the run ends long before the wait. The node path and the
	// origin are one registry here: what the origin stores, the relay in
	// front of it serves.
	st, base, mirrors := originStub(t, opt)
	began := time.Now()
	rc, out := runWith(t, fillEnv(mirrors, base), "ghcr.io/getfill/app:1\n", "probe", "-fill-wait", "1m", "-fill-every", "100ms")
	took := time.Since(began)
	want(t, "a blob only a GET fills is filled and passes", rc, 0, out, `(?m)^IMAGE-PATH PASS 1 images resolved through the origin`)
	want(t, "  the image says which way and after how long", rc, 0, out, `(?m)^PASS ghcr.io/getfill/app:1 -> .*/proxy-ghcr/getfill/app:1 child=- blobs=3 \(was not cached yet: filled by a GET at the origin, 1 object\(s\), 67 bytes, after \d+s\)$`)
	want(t, "  the run counts 1 filled", rc, 0, out, `(?m)^  ℹ filled 1 object\(s\), 67 bytes, by a GET through the origin's proxy project at http://127\.0\.0\.1:\d+ \(the bound: 16777216 bytes a run, 67 read\)$`)
	want(t, "  and names it with its size, the way and the time", rc, 0, out, `(?m)^    filled proxy-ghcr/getfill/app@`+stubTiny+` 67 bytes by a GET at the origin after \d+s$`)
	want(t, "  and says what is left unfilled by design", rc, 0, out, `(?m)^  ℹ left unfilled by design: a blob over the fill bound that only a GET fills \(NEVER FILLED, .*\); and every image this list does not hold \(never probed, never filled: .* its Pod stays in ImagePullBackOff at the fire\)$`)
	if m := strings.Join(blobMethods(st, stubTiny), " "); m != "HEAD GET HEAD" {
		t.Errorf("the GET comes at once after the HEAD that found the blob missing, then one HEAD reads it: got %q", m)
	}
	if took >= 30*time.Second {
		t.Errorf("the fill waited for -fill-wait (1m) before its GET: the run took %s", took)
	}
	if g := blobGets(st); len(g) != 1 || g[0] != "GET /v2/proxy-ghcr/getfill/app/blobs/"+stubTiny {
		t.Errorf("one GET of the one blob that was missing, at the proxy project, got %q", g)
	}

	// The origin stores it, the node path still cannot read it.
	_, _, nodeOnly := originStub(t, opt)
	far, farBase, _ := originStub(t, opt)
	rc, out = runWith(t, fillEnv(nodeOnly, farBase), "ghcr.io/getfill/app:1\n", fast...)
	want(t, "still unreadable through the node path after the fill", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/getfill/app:1 \(NEVER FILLED\)$`)
	want(t, "  naming the blob", rc, 1, out, `(?m)^FAIL ghcr.io/getfill/app:1 NEVER FILLED .*/proxy-ghcr/getfill/app:1 \[1 blob\(s\) not cached yet and still missing after 300ms, first sha256:111111111111; the fill: 1 fetched at once by a GET at the origin and still not read through the node path after 300ms\]$`)
	want(t, "  and the run says it is not filled", rc, 1, out, `(?m)^  ℹ filled 0 object\(s\), 0 bytes, .*\n  ⚠ fetched proxy-ghcr/getfill/app@`+stubTiny+` 67 bytes at the origin, and the node path still does not read it$`)
	if g := blobGets(far); len(g) != 1 {
		t.Errorf("the origin is asked once by GET, got %q", g)
	}

	// A damaged cache entry is a registry repair: nothing is fetched for it.
	dmg, _, dmgMirrors := originStub(t, opt)
	far, farBase, _ = originStub(t, opt)
	rc, out = runWith(t, fillEnv(dmgMirrors, farBase), "ghcr.io/damaged/app:16\n", fast...)
	want(t, "a damaged entry still fails", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/damaged/app:16 \(DAMAGED CACHE ENTRY\)$`)
	if hits, _ := far.record(); hits != 0 || len(blobGets(dmg)) != 0 {
		t.Errorf("a damaged entry is never fetched: origin requests=%d, node blob GETs=%q", hits, blobGets(dmg))
	}

	// The bound: a 3 GiB layer is never read by the pre-flight. It is left to
	// the origin's own pull for -fill-wait; a layer only a GET fills stays
	// NEVER FILLED by design, and the line says so.
	st, base, mirrors = originStub(t, opt)
	rc, out = runWith(t, fillEnv(mirrors, base), "ghcr.io/bigfill/app:1\n", fast...)
	want(t, "the bound stops the fill of a large layer", rc, 1, out, `(?m)^IMAGE-PATH FAIL 1/1: ghcr.io/bigfill/app:1 \(NEVER FILLED\)$`)
	want(t, "  and the line says so", rc, 1, out, `(?m)^FAIL ghcr.io/bigfill/app:1 NEVER FILLED .*\[1 blob\(s\) not cached yet and still missing after 300ms, first sha256:222222222222; the fill: 1 not fetched and not filled by the origin's own pull within 300ms either, left unfilled by design \(a GET of it at the origin by hand, or a larger -fill-max-bytes\), first sha256:222222222222 of 3221225472 bytes is over the fill bound of 16777216 bytes a run \(67 used\); 1 other\(s\) of the image filled by a GET at the origin\]$`)
	want(t, "  and the run", rc, 1, out, `(?m)^  ⚠ not fetched proxy-ghcr/bigfill/app@`+stubHuge+` 3221225472 bytes, and the origin's own pull did not fill it: `)
	want(t, "  the small one is filled all the same", rc, 1, out, `(?m)^    filled proxy-ghcr/bigfill/app@`+stubTiny+` 67 bytes by a GET at the origin after \d+s$`)
	for _, g := range blobGets(st) {
		if strings.Contains(g, stubHuge) {
			t.Errorf("the layer over the bound was asked for: %s", g)
		}
	}
	st, base, mirrors = originStub(t, opt)
	rc, out = runWith(t, fillEnv(mirrors, base), "ghcr.io/getfill/app:1\n", append(fast, "-fill-max-bytes", "66")...)
	want(t, "a bound below the blob stops it", rc, 1, out, `(?m)^FAIL ghcr.io/getfill/app:1 NEVER FILLED .*; the fill: 1 not fetched and not filled by the origin's own pull within 300ms either, left unfilled by design \(a GET of it at the origin by hand, or a larger -fill-max-bytes\), first sha256:111111111111 of 67 bytes is over the fill bound of 66 bytes a run \(0 used\)\]$`)
	if g := blobGets(st); len(g) != 0 {
		t.Errorf("nothing over the bound is fetched, got %q", g)
	}

	// A GET that does not fill (here the origin refuses the node's login) is
	// followed by the wait for the origin's own pull, and the run says which
	// way each object was filled.
	_, _, filling := originStub(t, stubOptions{user: robot, password: "node-token", fillAfter: 300 * time.Millisecond})
	_, refusing, _ := originStub(t, stubOptions{user: "someone-else", password: "other"})
	rc, out = runWith(t, fillEnv(filling, refusing), "ghcr.io/filling/app:1\n", "probe", "-fill-wait", "10s", "-fill-every", "100ms")
	want(t, "filled by the origin's own pull", rc, 0, out, `(?m)^PASS ghcr.io/filling/app:1 .*\(was not cached yet: filled after \d+s\)$`)
	want(t, "  the run says which way", rc, 0, out, `(?m)^  ℹ filled 2 object\(s\) by the origin's own pull \(over the bound, or the GET did not fill it\)$`)
	want(t, "  for each object", rc, 0, out, `(?m)^    filled proxy-ghcr/filling/app@`+stubLayer1+` 1 bytes by the origin's own pull after \d+s$`)

	// The fill's GET carries the node's credential. The document of every
	// fresh install names ghcr.io as ORIGIN_REGISTRY_HOST, a registry the
	// origin proxies: an origin whose host the node map rewrites into a proxy
	// project is refused before any request, and is asked nothing. The node
	// path is a fresh stand-in, so without the refusal the fill would ask it.
	_, base, mirrors = originStub(t, opt)
	far, farBase, _ = originStub(t, opt)
	var m map[string]mirror
	if err := json.Unmarshal([]byte(mirrors), &m); err != nil {
		t.Fatal(err)
	}
	m[hostOf(farBase)] = mirror{Endpoint: []string{base}, Rewrite: map[string]string{"(.*)": "proxy-ghcr/$1"}}
	proxied, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	rc, out = runWith(t, fillEnv(string(proxied), farBase), "ghcr.io/getfill/app:1\n", fast...)
	want(t, "an origin the node map proxies is refused", rc, 2, out, `(?m)^IMAGE-PATH FAIL 0/0: PROBE_ORIGIN http://127\.0\.0\.1:\d+ is a registry the origin proxies \(the node map rewrites 127\.0\.0\.1:\d+ into the proxy project proxy-ghcr\), not the origin: `)
	if hits, _ := far.record(); hits != 0 {
		t.Errorf("a registry the origin proxies is never sent the node's credential, got %d request(s)", hits)
	}
	if p := proxiedBy("https://ghcr.io", map[string]mirror{"ghcr.io": {Rewrite: map[string]string{"(.*)": "proxy-ghcr/$1"}}, "harbor.openova.io": {}}, map[string]bool{"proxy-ghcr": true}); p != "proxy-ghcr" {
		t.Errorf("ghcr.io is a registry the origin proxies, got %q", p)
	}
	if p := proxiedBy("https://harbor.openova.io", map[string]mirror{"ghcr.io": {Rewrite: map[string]string{"(.*)": "proxy-ghcr/$1"}}, "harbor.openova.io": {}}, map[string]bool{"proxy-ghcr": true}); p != "" {
		t.Errorf("the origin itself, served at the root, is not proxied, got %q", p)
	}
}

// TestProbeFillShared: two images meet the same object (the GET-only blob of
// getfill/app at tags 1 and 2, walked by two workers): one GET, and each
// image counts the object with its size, not one of them as 0 bytes. The
// stand-in answers the GET only once both images have read the blob by HEAD
// (holdGet 2), so both find it missing whichever worker runs first: the
// order is set by the stand-in, not by timing.
func TestProbeFillShared(t *testing.T) {
	st, base, mirrors := originStub(t, stubOptions{user: robot, password: "node-token", holdGet: 2})
	rc, out := runWith(t, fillEnv(mirrors, base), "ghcr.io/getfill/app:1\nghcr.io/getfill/app:2\n", "probe", "-fill-wait", "1m", "-fill-every", "100ms", "-parallel", "2")
	for _, tag := range []string{"1", "2"} {
		want(t, "a shared object counts for image :"+tag, rc, 0, out, `(?m)^PASS ghcr.io/getfill/app:`+tag+` .*\(was not cached yet: filled by a GET at the origin, 1 object\(s\), 67 bytes, after \d+s\)$`)
	}
	want(t, "  both images say so", rc, 0, out, `(?m)^  ℹ 2 image\(s\) were not cached yet at the origin and filled by a GET there$`)
	want(t, "  and the run counts it once", rc, 0, out, `(?m)^  ℹ filled 1 object\(s\), 67 bytes, `)
	if g := blobGets(st); len(g) != 1 {
		t.Errorf("a shared object is asked for once, got %q", g)
	}
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

	// ghcr publishes an index; the origin's tag holds only its linux/amd64
	// child, as the warm-up's `skopeo copy` at its default wrote it before
	// 24 Nov 2026: the Sovereign's registry serves the index for the same tag,
	// so the tag has two digests, and that fails.
	idx := map[string]bool{"openova-io/openova/sovereign-ops": true}
	ghcrIdx := httptest.NewServer(newStub(stubOptions{indexed: idx}))
	t.Cleanup(ghcrIdx.Close)
	argsIdx := []string{"origin-has", "-origin", base, "-ghcr", ghcrIdx.URL}
	rc, out = runWith(t, env, "ghcr.io/openova-io/openova/sovereign-ops:fe65309\n", argsIdx...)
	want(t, "an index on ghcr, only its linux/amd64 child at the origin", rc, 1, out, `(?m)^MISSING AT THE ORIGIN ghcr.io/openova-io/openova/sovereign-ops:fe65309: .* carries only ghcr's linux/amd64 child sha256:[0-9a-f]+, ghcr has the index sha256:[0-9a-f]+: one tag, two digests \(warm the ref again\)$`)
	want(t, "  and the cut is called incoherent", rc, 1, out, `(?m)^ORIGIN-HAS FAIL 1/1 `)
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
