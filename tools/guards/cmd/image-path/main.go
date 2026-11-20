// Command image-path is the registry half of two checks of a fresh install.
// The image list is built by scripts/check-mothership-proxy-image-path.sh
// (--list-only), never here; this command reads it on stdin, one ref a line.
//
//	image-path probe        prov pre-flight item 7: walk every image along the
//	                        path a node pulls it, with the credential a node is
//	                        given: the manifest of the tag, the linux/amd64 child,
//	                        then the config and EVERY layer by HEAD.
//	image-path origin-has   check 5 of scripts/check-train-coherent.sh: every
//	                        first-party image is at the origin's hosted project
//	                        with the digest ghcr has.
//	image-path resolve REF  the node's resolution of one ref.
//	image-path subst        Flux's substitution of the kit, for the list builder.
//	image-path stub         the registry stand-in of the self-tests.
//
// probe. The node registry map (the `mirrors` of /etc/rancher/k3s/registries.yaml,
// built by the script the way the provider modules build it) is the JSON of
// PROBE_MIRRORS; the names of the proxy projects of the origin are the JSON list
// PROBE_PROXY_PROJECTS. Every node starts k3s with
// --disable-default-registry-endpoint, so there is no fallback to a public
// registry: what the origin cannot serve, the node cannot pull. The node's
// credential is PROBE_USER and PROBE_TOKEN; without it the probe refuses to run
// (exit 2), because an anonymous read is not what a node does. PROBE_ORIGIN
// (scheme and host) is the origin registry whose proxy projects the node path
// fronts; without it nothing is filled and a blob no wait fills is NEVER
// FILLED, as before the fill existed. Verdicts:
//
//	PASS                    tag, child, config and every layer answered.
//	not cached yet          a blob of a PROXY project answered 404 on HEAD. The
//	                        origin fills a proxied image's blobs by itself some
//	                        minutes after its manifest was first asked for, so
//	                        the probe asks for the manifest once more and reads
//	                        the blobs again until -fill-wait (default 10m) has
//	                        passed. Filled in time is a PASS that says so.
//	filled by a GET         still missing when -fill-wait has passed: the
//	                        origin answers a HEAD of a proxied blob it has not
//	                        stored with 404 and stores it only once that blob
//	                        is asked for by GET, and the node path (the relay
//	                        in front of the origin) asks by HEAD before it
//	                        GETs. So the probe asks the origin (PROBE_ORIGIN,
//	                        the same proxy project, the node's credential: a
//	                        pull, never a push, never a hosted project, never
//	                        the admin API) for each such blob by GET, smallest
//	                        first, at most -fill-max-bytes a run, and reads
//	                        the node path again until -fill-settle has passed.
//	                        Readable then is a PASS that says so; the run
//	                        names every object it filled with its size.
//	NEVER FILLED            still missing through the node path after that
//	                        (or a blob the bound left to the origin's own pull,
//	                        which the second manifest request starts).
//	DAMAGED CACHE ENTRY     the origin serves the index but not its linux/amd64
//	                        child. Only a registry-side repair helps (story
//	                        SOV-162); this command deletes nothing.
//	MISSING AT THE ORIGIN   a first-party image (the hosted project, not a proxy
//	                        project) whose tag, linux/amd64 child or blob the
//	                        origin does not have: the project is written by a
//	                        push, so the remedy is to publish it again.
//	NOT AT THE ORIGIN       a proxy project answers no manifest for the tag.
//	CREDENTIAL REFUSED      the origin refuses the node's credential.
//
// Exit codes: 0 every image passed; 1 at least one failed; 2 nothing could be
// judged (no credential, no image, a usage error).
//
// origin-has reads refs ghcr.io/openova-io/openova/<name>:<tag> and compares,
// for each, what a node pulls from ghcr (GHCR_USER, GHCR_TOKEN) and from the
// origin (-origin, with ORIGIN_USER and ORIGIN_TOKEN) at the same path and tag:
// the node map rewrites ghcr.io/openova-io/openova/<name> to the hosted project
// path openova-io/openova/<name>. What a node pulls is the linux/amd64 image
// manifest: the child when the tag holds an index, else the manifest itself.
// That is also what the warm-up writes: .github/workflows/warmup-bastion-harbor.yaml
// copies with `skopeo copy` at its default (--multi-arch system, on an amd64
// runner), which writes an index's linux/amd64 child, unchanged, under the tag,
// so the origin's tag carries the digest of ghcr's child, never that of ghcr's
// index. An origin that holds the whole index (a copy with --all) passes when
// its linux/amd64 child is ghcr's and is served. Exit 0 every image is there;
// 1 at least one is missing or carries another image (each is named); 2 a
// registry could not be read, so nothing is concluded.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const accept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

const firstParty = "ghcr.io/openova-io/openova/"

// ---- resolution: the node's mirror map

type mirror struct {
	Endpoint []string          `json:"endpoint"`
	Rewrite  map[string]string `json:"rewrite"`
}

type resolved struct {
	kind     string // ok, skip, fail
	why      string
	base     string
	repo     string
	ref      string
	host     string
	upstream string
}

// splitRef gives the host, the path and the tag or digest of an image ref. A
// bare name is docker.io, and Docker Hub's implicit namespace is library/.
func splitRef(ref string) (host, path, reference string) {
	name, digest, hasDigest := strings.Cut(ref, "@")
	tag := "latest"
	last := name[strings.LastIndex(name, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		tag = last[i+1:]
		name = name[:len(name)-len(last)+i]
	}
	first, rest, slash := strings.Cut(name, "/")
	if slash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, path = first, rest
	} else {
		host, path = "docker.io", name
	}
	if (host == "docker.io" || host == "registry-1.docker.io" || host == "index.docker.io") && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	reference = tag
	if hasDigest {
		reference = digest
	}
	return host, path, reference
}

// rewrite applies a containerd rewrite table: the first rule (in key order;
// the tables the modules build hold rules that exclude each other) whose
// pattern matches replaces the path.
func rewrite(rules map[string]string, path string) string {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rx, err := regexp.Compile(k)
		if err != nil || !rx.MatchString(path) {
			continue
		}
		return rx.ReplaceAllString(path, rules[k])
	}
	return path
}

func resolve(ref string, mirrors map[string]mirror) resolved {
	switch {
	case strings.Contains(ref, "{{") || strings.Contains(ref, "${"):
		return resolved{kind: "skip", why: "helm/envsubst placeholder"}
	case strings.ContainsAny(ref, "*?"):
		return resolved{kind: "skip", why: "image GLOB pattern (Kyverno policy match), not a pull"}
	}
	// A bare name (busybox) is a pull like any other: containerd reads it as
	// docker.io/library/<name>:latest and, since SOV-132, pulls it through the
	// docker.io entry of the map.
	host, path, reference := splitRef(ref)
	m, ok := mirrors[host]
	if !ok {
		m, ok = mirrors["*"]
	}
	if !ok {
		return resolved{kind: "fail", why: fmt.Sprintf("host=%s [the node registry map has no entry for it and no \"*\", and the default endpoint is off]", host)}
	}
	if len(m.Endpoint) == 0 {
		return resolved{kind: "fail", why: fmt.Sprintf("host=%s [no mirror of the node map serves it before the fire, and the default endpoint is off]", host)}
	}
	return resolved{kind: "ok", base: strings.TrimRight(m.Endpoint[0], "/"), repo: rewrite(m.Rewrite, path),
		ref: reference, host: host, upstream: path}
}

// ---- the client: Bearer and Basic challenges, redirects, a token cache

type client struct {
	http  *http.Client
	user  string
	pass  string
	mu    sync.Mutex
	token map[string]string
}

func newClient(timeout time.Duration, user, pass string) *client {
	return &client{
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		user: user, pass: pass, token: map[string]string{},
	}
}

type reply struct {
	status int
	header http.Header
	body   []byte
	err    error
}

// maxBody is the most of a reply body a manifest or a token read keeps.
const maxBody = 8 << 20

// raw sends one request and keeps at most limit bytes of the reply body.
func (c *client) raw(method, u string, header http.Header, limit int64) reply {
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return reply{err: err}
	}
	req.Header = header.Clone()
	resp, err := c.http.Do(req)
	if err != nil {
		return reply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return reply{status: resp.StatusCode, header: resp.Header, body: body, err: err}
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (c *client) bearer(challenge string) string {
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return ""
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	u := realm
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	c.mu.Lock()
	tok, ok := c.token[u]
	c.mu.Unlock()
	if ok {
		return tok
	}
	h := http.Header{"Accept": {"application/json"}}
	if c.pass != "" {
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass)))
	}
	r := c.raw(http.MethodGet, u, h, maxBody)
	if r.err != nil || r.status != http.StatusOK {
		return ""
	}
	var d struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(r.body, &d) != nil {
		return ""
	}
	tok = d.Token
	if tok == "" {
		tok = d.AccessToken
	}
	if tok != "" {
		c.mu.Lock()
		c.token[u] = tok
		c.mu.Unlock()
	}
	return tok
}

// do sends a request, answers one 401 challenge and follows up to three
// redirects. A credential never goes to a host other than the one asked.
func (c *client) do(method, u string, header http.Header) reply {
	return c.doLimit(method, u, header, maxBody)
}

// doLimit is do keeping at most limit bytes of the body of the last reply.
func (c *client) doLimit(method, u string, header http.Header, limit int64) reply {
	header = header.Clone()
	if header == nil {
		header = http.Header{}
	}
	host := hostOf(u)
	r := c.raw(method, u, header, limit)
	answered := false
	for hops := 0; hops < 4 && r.err == nil; hops++ {
		if r.status == http.StatusUnauthorized && !answered {
			answered = true
			ch := r.header.Get("WWW-Authenticate")
			switch {
			case strings.HasPrefix(strings.ToLower(ch), "bearer"):
				tok := c.bearer(ch)
				if tok == "" {
					return r
				}
				header.Set("Authorization", "Bearer "+tok)
			case strings.HasPrefix(strings.ToLower(ch), "basic") && c.pass != "":
				header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass)))
			default:
				return r
			}
			r = c.raw(method, u, header, limit)
			continue
		}
		if r.status >= 300 && r.status < 400 && r.header.Get("Location") != "" {
			next, err := url.Parse(u)
			if err != nil {
				return r
			}
			loc, err := next.Parse(r.header.Get("Location"))
			if err != nil {
				return r
			}
			u = loc.String()
			if hostOf(u) != host {
				header.Del("Authorization")
				host = hostOf(u)
			}
			r = c.raw(method, u, header, limit)
			continue
		}
		break
	}
	return r
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Host
}

// ---- the walk

type manifest struct {
	MediaType string `json:"mediaType"`
	Schema    int    `json:"schemaVersion"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS   string `json:"os"`
			Arch string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
	FSLayers []struct {
		BlobSum string `json:"blobSum"`
	} `json:"fsLayers"`
}

type walked struct {
	stage  string // "" when every manifest answered
	status int
	detail string
	child  string
	blobs  []string
	sizes  map[string]int64 // the size each blob's descriptor gives (none for schema 1)
}

func (c *client) manifests(base, repo, reference string) walked {
	r := c.do(http.MethodGet, fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, reference), http.Header{"Accept": {accept}})
	if r.err != nil {
		return walked{stage: "index", detail: r.err.Error()}
	}
	if r.status != http.StatusOK {
		return walked{stage: "index", status: r.status}
	}
	var m manifest
	if json.Unmarshal(r.body, &m) != nil {
		return walked{stage: "index", status: r.status, detail: "the manifest is not JSON"}
	}
	w := walked{}
	if len(m.Manifests) > 0 {
		for _, e := range m.Manifests {
			if e.Platform.OS == "linux" && e.Platform.Arch == "amd64" {
				w.child = e.Digest
				break
			}
		}
		if w.child == "" {
			return walked{stage: "child", detail: "the index carries no linux/amd64 entry"}
		}
		r = c.do(http.MethodGet, fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, w.child), http.Header{"Accept": {accept}})
		if r.err != nil {
			return walked{stage: "child", child: w.child, detail: r.err.Error()}
		}
		if r.status != http.StatusOK {
			return walked{stage: "child", child: w.child, status: r.status}
		}
		m = manifest{}
		if json.Unmarshal(r.body, &m) != nil {
			return walked{stage: "child", child: w.child, detail: "the child manifest is not JSON"}
		}
	}
	w.sizes = map[string]int64{}
	if m.Config.Digest != "" {
		w.blobs = append(w.blobs, m.Config.Digest)
		w.sizes[m.Config.Digest] = m.Config.Size
	}
	for _, l := range m.Layers {
		w.blobs = append(w.blobs, l.Digest)
		w.sizes[l.Digest] = l.Size
	}
	for _, l := range m.FSLayers {
		w.blobs = append(w.blobs, l.BlobSum)
	}
	if len(w.blobs) == 0 {
		return walked{stage: "config", child: w.child, detail: "the manifest names no blob"}
	}
	return w
}

// heads asks for each blob by HEAD and returns the missing ones (404), or the
// first other answer as an error.
func (c *client) heads(base, repo string, blobs []string) ([]string, error) {
	var missing []string
	for _, b := range blobs {
		r := c.do(http.MethodHead, fmt.Sprintf("%s/v2/%s/blobs/%s", base, repo, b), http.Header{})
		switch {
		case r.err != nil:
			return nil, r.err
		case r.status == http.StatusOK:
		case r.status == http.StatusNotFound:
			missing = append(missing, b)
		default:
			return nil, fmt.Errorf("blob %s http=%d", short(b), r.status)
		}
	}
	return missing, nil
}

func short(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

// reread reads the missing blobs through the node path again every `every`
// until none is missing or `bound` has passed, and says how long it took.
func (c *client) reread(base, repo string, missing []string, bound, every time.Duration) ([]string, time.Duration, error) {
	start := time.Now()
	for len(missing) > 0 && time.Since(start) < bound {
		time.Sleep(every)
		var err error
		if missing, err = c.heads(base, repo, missing); err != nil {
			return nil, 0, err
		}
	}
	return missing, time.Since(start), nil
}

// ---- the fill: a GET of a proxied blob at the origin

// defaultFillMaxBytes bounds the bytes one run reads to fill. The case is the
// tiny blob: on 20 November 2026 the eight blobs no wait filled were 67 to 351
// bytes, 1,460 in all, and were filled by hand. 16 MiB is more than 10,000
// times that and covers any config or small layer, while a large layer (a
// multi-gigabyte one) is never read by a pre-flight: it is left to the
// origin's own pull, which the second manifest request starts, and to the
// warm-up (.github/workflows/warmup-bastion-harbor.yaml). The bytes are read
// and dropped, so the bound is also the most the fill ever holds in memory.
const defaultFillMaxBytes = 16 << 20

// fillState is what became of one object the fill met, by <repo>@<digest>.
type fillState struct {
	size     int64
	status   string // asking, fetched, stopped, refused
	why      string
	readable bool // read through the node path after the fill
}

// filler asks the origin, by GET, for a blob of a proxy project that the node
// path still cannot read once -fill-wait has passed: the origin stores such a
// blob only when it is asked for it by GET, and the relay in front of it asks
// by HEAD first, so no wait fills it. The GET goes to the same path of the
// same proxy project, with the node's own credential: a pull. A hosted
// project is never asked (a miss there is MISSING AT THE ORIGIN), nothing is
// pushed, and the registry's admin API is never called.
type filler struct {
	c      *client
	origin string
	max    int64
	mu     sync.Mutex
	used   int64
	state  map[string]*fillState
}

// fillOutcome is what the fill did for one image.
type fillOutcome struct {
	fetched, stopped, refused int
	bytes                     int64
	whyStopped, whyRefused    string
}

// fill asks the origin, smallest first, for each blob of repo in missing. An
// object another image of the run asked for already is not asked again. An
// object whose size the manifest does not give, or that does not fit in what
// is left of -fill-max-bytes, is not asked for: it is stopped.
func (f *filler) fill(repo string, missing []string, sizes map[string]int64) fillOutcome {
	order := append([]string(nil), missing...)
	sort.SliceStable(order, func(i, j int) bool { return sizes[order[i]] < sizes[order[j]] })
	var o fillOutcome
	for _, b := range order {
		key, size := repo+"@"+b, sizes[b]
		f.mu.Lock()
		s, seen := f.state[key]
		if !seen {
			s = &fillState{size: size, status: "asking"}
			switch {
			case size <= 0:
				s.status, s.why = "stopped", fmt.Sprintf("%s: the manifest gives no size", short(b))
			case f.used+size > f.max:
				s.status, s.why = "stopped", fmt.Sprintf("%s of %d bytes is over the fill bound of %d bytes a run (%d used)", short(b), size, f.max, f.used)
			default:
				f.used += size
			}
			f.state[key] = s
		}
		status, why := s.status, s.why
		f.mu.Unlock()
		if !seen && status == "asking" {
			// At most the size the manifest gives: a registry that serves more
			// serves another object, and the bound holds either way.
			r := f.c.doLimit(http.MethodGet, fmt.Sprintf("%s/v2/%s/blobs/%s", f.origin, repo, b), http.Header{}, size)
			f.mu.Lock()
			switch {
			case r.err != nil:
				f.used -= size
				s.status, s.why = "refused", fmt.Sprintf("%s: %v", short(b), r.err)
			case r.status != http.StatusOK:
				f.used -= size
				s.status, s.why = "refused", fmt.Sprintf("%s: http=%d", short(b), r.status)
			default:
				f.used += int64(len(r.body)) - size
				s.status, s.size = "fetched", int64(len(r.body))
				o.bytes += s.size
			}
			status, why = s.status, s.why
			f.mu.Unlock()
		}
		switch status {
		case "stopped":
			o.stopped++
			if o.whyStopped == "" {
				o.whyStopped = why
			}
		case "refused":
			o.refused++
			if o.whyRefused == "" {
				o.whyRefused = why
			}
		default:
			o.fetched++
		}
	}
	return o
}

// settled records which of the objects of repo the fill met the node path
// reads now.
func (f *filler) settled(repo string, asked, missing []string) {
	still := map[string]bool{}
	for _, b := range missing {
		still[b] = true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range asked {
		if s, ok := f.state[repo+"@"+b]; ok && s.status == "fetched" && !still[b] {
			s.readable = true
		}
	}
}

// note says, for a NEVER FILLED line, what the fill did.
func (o fillOutcome) note(settle time.Duration) string {
	var parts []string
	if o.fetched > 0 {
		parts = append(parts, fmt.Sprintf("%d fetched by a GET at the origin and read again for %s", o.fetched, settle))
	}
	if o.stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d not fetched, left to the origin's own pull, first %s", o.stopped, o.whyStopped))
	}
	if o.refused > 0 {
		parts = append(parts, fmt.Sprintf("the origin refused the GET of %d, first %s", o.refused, o.whyRefused))
	}
	return "the fill: " + strings.Join(parts, "; ")
}

// report writes how many objects the run filled, each with its size, and
// what it could not fill.
func (f *filler) report(w io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.state))
	for k := range f.state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type group struct {
		keys  []string
		bytes int64
	}
	var filled, unread, stopped, refused group
	for _, k := range keys {
		s := f.state[k]
		g := &stopped
		switch {
		case s.status == "fetched" && s.readable:
			g = &filled
		case s.status == "fetched":
			g = &unread
		case s.status == "refused":
			g = &refused
		}
		g.keys = append(g.keys, k)
		g.bytes += s.size
	}
	say(w, "  ℹ filled %d object(s), %d bytes, by a GET through the origin's proxy project at %s (the bound: %d bytes a run, %d read)\n",
		len(filled.keys), filled.bytes, f.origin, f.max, f.used)
	for _, k := range filled.keys {
		say(w, "    filled %s %d bytes\n", k, f.state[k].size)
	}
	for _, k := range unread.keys {
		say(w, "  ⚠ fetched %s %d bytes at the origin, and the node path still does not read it\n", k, f.state[k].size)
	}
	for _, k := range stopped.keys {
		say(w, "  ⚠ not fetched %s %d bytes: %s\n", k, f.state[k].size, f.state[k].why)
	}
	for _, k := range refused.keys {
		say(w, "  ⚠ the origin refused the GET of %s: %s\n", k, f.state[k].why)
	}
}

type probeConfig struct {
	mirrors    map[string]mirror
	proxies    map[string]bool
	user       string
	fillWait   time.Duration
	fillEvery  time.Duration
	fillSettle time.Duration
	fill       *filler // nil: no origin is named, nothing is filled
}

// probe walks one ref and returns its line: PASS, FAIL or SKIP first.
func probe(c *client, cfg probeConfig, ref string) string {
	res := resolve(ref, cfg.mirrors)
	switch res.kind {
	case "skip":
		return fmt.Sprintf("SKIP %s %s", ref, res.why)
	case "fail":
		return fmt.Sprintf("FAIL %s NO NODE PATH %s", ref, res.why)
	}
	project, _, _ := strings.Cut(res.repo, "/")
	proxy := cfg.proxies[project]
	where := fmt.Sprintf("%s/%s:%s", res.base, res.repo, res.ref)
	w := c.manifests(res.base, res.repo, res.ref)
	switch {
	case w.stage == "index" && (w.status == http.StatusUnauthorized || w.status == http.StatusForbidden):
		return fmt.Sprintf("FAIL %s CREDENTIAL REFUSED http=%d %s [the origin refuses the node's credential %s]", ref, w.status, where, cfg.user)
	case w.stage == "index" && w.status == http.StatusNotFound && !proxy:
		return fmt.Sprintf("FAIL %s MISSING AT THE ORIGIN %s [the hosted project %s has no such tag: push it there]", ref, where, project)
	case w.stage == "index" && w.status == http.StatusNotFound:
		return fmt.Sprintf("FAIL %s NOT AT THE ORIGIN %s [the proxy project %s answers no manifest for the tag]", ref, where, project)
	case w.stage == "index":
		return fmt.Sprintf("FAIL %s stage=index http=%d %s %s", ref, w.status, where, w.detail)
	case w.stage == "child" && w.status == http.StatusNotFound && !proxy:
		// The hosted project is written by a push, not filled by a cache: an
		// index there whose child is absent was published incompletely.
		return fmt.Sprintf("FAIL %s MISSING AT THE ORIGIN %s [the hosted project %s serves the index, but its linux/amd64 child %s is absent: publish it again]",
			ref, where, project, short(w.child))
	case w.stage == "child" && w.status == http.StatusNotFound:
		return fmt.Sprintf("FAIL %s DAMAGED CACHE ENTRY %s/%s [the index is served, its linux/amd64 child %s is unknown: a registry-side repair (story SOV-162); this check deletes nothing]",
			ref, res.base, res.repo, short(w.child))
	case w.stage != "":
		return fmt.Sprintf("FAIL %s stage=%s http=%d %s %s", ref, w.stage, w.status, where, w.detail)
	}
	missing, err := c.heads(res.base, res.repo, w.blobs)
	if err != nil {
		return fmt.Sprintf("FAIL %s stage=blob %s %v", ref, where, err)
	}
	pass := fmt.Sprintf("PASS %s -> %s child=%s blobs=%d", ref, where, short(orDash(w.child)), len(w.blobs))
	if len(missing) == 0 {
		return pass
	}
	if !proxy {
		return fmt.Sprintf("FAIL %s MISSING AT THE ORIGIN %s [%d of %d blobs of the hosted project are absent, first %s: publish it again]", ref, where, len(missing), len(w.blobs), short(missing[0]))
	}
	// Not cached yet: ask for the manifest once (that starts the origin's own
	// pull), then read the blobs again until the bound has passed.
	_ = c.manifests(res.base, res.repo, res.ref)
	missing, took, err := c.reread(res.base, res.repo, missing, cfg.fillWait, cfg.fillEvery)
	if err != nil {
		return fmt.Sprintf("FAIL %s stage=blob %s %v", ref, where, err)
	}
	if len(missing) == 0 {
		return fmt.Sprintf("%s (was not cached yet: filled after %s)", pass, took.Round(time.Second))
	}
	never := fmt.Sprintf("FAIL %s NEVER FILLED %s [%d blob(s) not cached yet and still missing after %s, first %s", ref, where, len(missing), cfg.fillWait, short(missing[0]))
	if cfg.fill == nil {
		return never + "]"
	}
	// No wait fills a blob the origin stores only on a GET: ask it by GET.
	o := cfg.fill.fill(res.repo, missing, w.sizes)
	if o.fetched > 0 {
		asked := missing
		missing, _, err = c.reread(res.base, res.repo, missing, cfg.fillSettle, cfg.fillEvery)
		if err != nil {
			return fmt.Sprintf("FAIL %s stage=blob %s %v", ref, where, err)
		}
		cfg.fill.settled(res.repo, asked, missing)
		if len(missing) == 0 {
			return fmt.Sprintf("%s (was not cached yet: filled by a GET at the origin, %d object(s), %d bytes)", pass, o.fetched, o.bytes)
		}
		never = fmt.Sprintf("FAIL %s NEVER FILLED %s [%d blob(s) not cached yet and still missing after %s, first %s", ref, where, len(missing), cfg.fillWait, short(missing[0]))
	}
	return fmt.Sprintf("%s; %s]", never, o.note(cfg.fillSettle))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func readRefs(in io.Reader) ([]string, error) {
	var refs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		ref := strings.TrimSpace(sc.Text())
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs, sc.Err()
}

func runProbe(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	parallel := fs.Int("parallel", 4, "images walked at once")
	timeout := fs.Duration("timeout", 25*time.Second, "timeout of one request")
	fillWait := fs.Duration("fill-wait", 10*time.Minute, "how long a blob that is not cached yet may take to fill")
	fillEvery := fs.Duration("fill-every", 20*time.Second, "pause between two reads of a blob that is not cached yet")
	fillSettle := fs.Duration("fill-settle", 2*time.Minute, "how long the origin may take to store a blob the fill asked it for by GET")
	fillMax := fs.Int64("fill-max-bytes", defaultFillMaxBytes, "the most bytes a run reads to fill blobs at the origin (PROBE_ORIGIN)")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *parallel < 1 || *fillMax < 0 {
		return 2
	}
	user, token := os.Getenv("PROBE_USER"), os.Getenv("PROBE_TOKEN")
	if user == "" || token == "" {
		sayln(stdout, "IMAGE-PATH FAIL 0/0: no credential (PROBE_USER and PROBE_TOKEN: the registry login a node is given); an anonymous read is not what a node does")
		return 2
	}
	cfg := probeConfig{user: user, fillWait: *fillWait, fillEvery: *fillEvery, fillSettle: *fillSettle, proxies: map[string]bool{}}
	if err := json.Unmarshal([]byte(os.Getenv("PROBE_MIRRORS")), &cfg.mirrors); err != nil || len(cfg.mirrors) == 0 {
		sayln(stdout, "IMAGE-PATH FAIL 0/0: PROBE_MIRRORS holds no node registry map")
		return 2
	}
	var proxies []string
	if err := json.Unmarshal([]byte(os.Getenv("PROBE_PROXY_PROJECTS")), &proxies); err != nil || len(proxies) == 0 {
		sayln(stdout, "IMAGE-PATH FAIL 0/0: PROBE_PROXY_PROJECTS names no proxy project of the origin")
		return 2
	}
	for _, p := range proxies {
		cfg.proxies[p] = true
	}
	refs, err := readRefs(in)
	if err != nil {
		sayln(stderr, err)
		return 2
	}
	if len(refs) == 0 {
		sayln(stdout, "IMAGE-PATH FAIL 0/0: no images extracted")
		return 2
	}
	c := newClient(*timeout, user, token)
	fillDesc := ", no fill: PROBE_ORIGIN names no origin"
	if origin := strings.TrimRight(os.Getenv("PROBE_ORIGIN"), "/"); origin != "" {
		cfg.fill = &filler{c: c, origin: origin, max: *fillMax, state: map[string]*fillState{}}
		fillDesc = fmt.Sprintf(", a blob still missing after %s filled by a GET at %s, at most %d bytes a run", *fillWait, origin, *fillMax)
	}
	lines := make([]string, len(refs))
	start := time.Now()
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < *parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				lines[j] = probe(c, cfg, refs[j])
			}
		}()
	}
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	sort.Strings(lines)
	var pass, fail, skip, filled, gotten int
	var fails, skips []string
	for _, l := range lines {
		sayln(stdout, l)
		switch {
		case strings.HasPrefix(l, "PASS "):
			pass++
			if strings.Contains(l, "was not cached yet: filled after") {
				filled++
			}
			if strings.Contains(l, "was not cached yet: filled by a GET") {
				gotten++
			}
		case strings.HasPrefix(l, "SKIP "):
			skip++
			skips = append(skips, strings.Fields(l)[1])
		default:
			fail++
			fails = append(fails, l)
		}
	}
	if skip > 0 {
		say(stdout, "  ⚠ %d ref(s) skipped (a placeholder, a policy glob or a server image, not a pull): %s\n", skip, strings.Join(first(skips, 5), " "))
	}
	if filled > 0 {
		say(stdout, "  ℹ %d image(s) were not cached yet at the origin and filled within %s\n", filled, *fillWait)
	}
	if gotten > 0 {
		say(stdout, "  ℹ %d image(s) were not cached yet at the origin and filled by a GET there\n", gotten)
	}
	if cfg.fill != nil {
		cfg.fill.report(stdout)
	}
	probed := pass + fail
	say(stdout, "  ℹ probed %d image(s) in %s (parallel=%d, credential %s, layers by HEAD%s)\n", probed, time.Since(start).Round(time.Second), *parallel, user, fillDesc)
	if probed == 0 {
		sayln(stdout, "IMAGE-PATH FAIL 0/0: no images extracted (every ref was skipped)")
		return 2
	}
	if fail == 0 {
		say(stdout, "IMAGE-PATH PASS %d images resolved through the origin (index+child+config+layers)\n", pass)
		return 0
	}
	var named []string
	for _, l := range first(fails, 5) {
		named = append(named, summary(l))
	}
	say(stdout, "IMAGE-PATH FAIL %d/%d: %s\n", fail, probed, strings.Join(named, "; "))
	return 1
}

// summary turns "FAIL <ref> <CLASS> <where> [...]" into "<ref> (<CLASS>)".
func summary(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 {
		return line
	}
	var class []string
	for _, w := range f[2:] {
		if w != strings.ToUpper(w) || strings.ContainsAny(w, "/:") {
			break
		}
		class = append(class, w)
	}
	if len(class) == 0 {
		class = f[2:3]
	}
	return fmt.Sprintf("%s (%s)", f[1], strings.Join(class, " "))
}

func first(l []string, n int) []string {
	if len(l) > n {
		return l[:n]
	}
	return l
}

// ---- origin-has

// pulled is what a node pulls for a tag. image is the digest of the image
// manifest it runs: the linux/amd64 child when the tag holds an index, else
// the manifest under the tag. index is the digest of that index ("" for a
// single manifest). status is the registry's answer; inChild says that a
// failed answer was the child's, noAmd64 that the index has no linux/amd64
// entry.
type pulled struct {
	image, index     string
	status           int
	inChild, noAmd64 bool
}

// manifestDigest is the Docker-Content-Digest of a reply, else the sha256 of
// its body.
func manifestDigest(r reply) string {
	if d := r.header.Get("Docker-Content-Digest"); d != "" {
		return d
	}
	sum := sha256.Sum256(r.body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// pull reads a tag the way a node does: the manifest under the tag and, when
// that is an index, its linux/amd64 child by digest.
func pull(c *client, base, repo, reference string) (pulled, error) {
	get := func(ref string) reply {
		return c.do(http.MethodGet, fmt.Sprintf("%s/v2/%s/manifests/%s", base, repo, ref), http.Header{"Accept": {accept}})
	}
	r := get(reference)
	if r.err != nil {
		return pulled{}, r.err
	}
	if r.status != http.StatusOK {
		return pulled{status: r.status}, nil
	}
	var m manifest
	if json.Unmarshal(r.body, &m) != nil {
		return pulled{}, fmt.Errorf("the manifest of %s:%s is not JSON", repo, reference)
	}
	if len(m.Manifests) == 0 && !strings.Contains(m.MediaType, "index") && !strings.Contains(m.MediaType, "manifest.list") {
		return pulled{image: manifestDigest(r), status: http.StatusOK}, nil
	}
	p := pulled{index: manifestDigest(r), status: http.StatusOK}
	for _, e := range m.Manifests {
		if e.Platform.OS == "linux" && e.Platform.Arch == "amd64" {
			p.image = e.Digest
			break
		}
	}
	if p.image == "" {
		p.noAmd64 = true
		return p, nil
	}
	if r = get(p.image); r.err != nil {
		return pulled{}, r.err
	}
	if r.status != http.StatusOK {
		p.status, p.inChild = r.status, true
	}
	return p, nil
}

func runOriginHas(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("origin-has", flag.ContinueOnError)
	fs.SetOutput(stderr)
	origin := fs.String("origin", "", "the origin registry, scheme and host (https://harbor.openova.io)")
	ghcr := fs.String("ghcr", "https://ghcr.io", "the registry the first-party images are published to")
	timeout := fs.Duration("timeout", 25*time.Second, "timeout of one request")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *origin == "" {
		sayln(stdout, "ORIGIN-HAS CANNOT READ: usage: image-path origin-has -origin <scheme://host> [-ghcr <scheme://host>] < refs")
		return 2
	}
	if os.Getenv("ORIGIN_USER") == "" || os.Getenv("ORIGIN_TOKEN") == "" {
		sayln(stdout, "ORIGIN-HAS CANNOT READ: no origin credential (ORIGIN_USER and ORIGIN_TOKEN): the hosted project cannot be read, so nothing is concluded")
		return 2
	}
	refs, err := readRefs(in)
	if err != nil || len(refs) == 0 {
		sayln(stdout, "ORIGIN-HAS CANNOT READ: no first-party image to check")
		return 2
	}
	src := newClient(*timeout, os.Getenv("GHCR_USER"), os.Getenv("GHCR_TOKEN"))
	dst := newClient(*timeout, os.Getenv("ORIGIN_USER"), os.Getenv("ORIGIN_TOKEN"))
	base := strings.TrimRight(*origin, "/")
	var missing, unread int
	for _, ref := range refs {
		if !strings.HasPrefix(ref, firstParty) {
			say(stdout, "CANNOT READ %s: not a first-party image (%s...)\n", ref, firstParty)
			unread++
			continue
		}
		_, path, reference := splitRef(ref)
		want, err := pull(src, strings.TrimRight(*ghcr, "/"), path, reference)
		switch {
		case err != nil:
			say(stdout, "CANNOT READ %s: ghcr: %v\n", ref, err)
			unread++
			continue
		case want.status == http.StatusNotFound && want.inChild:
			say(stdout, "MISSING ON GHCR %s: the index names a linux/amd64 child %s that ghcr does not serve\n", ref, want.image)
			missing++
			continue
		case want.status == http.StatusNotFound:
			say(stdout, "MISSING ON GHCR %s: the commit pins a tag that was never published\n", ref)
			missing++
			continue
		case want.status != http.StatusOK:
			say(stdout, "CANNOT READ %s: ghcr answered http=%d\n", ref, want.status)
			unread++
			continue
		case want.noAmd64:
			say(stdout, "MISSING ON GHCR %s: the index %s carries no linux/amd64 image, the one a node pulls\n", ref, want.index)
			missing++
			continue
		}
		got, err := pull(dst, base, path, reference)
		at := fmt.Sprintf("%s/%s:%s", base, path, reference)
		switch {
		case err != nil:
			say(stdout, "CANNOT READ %s: the origin %s: %v\n", ref, base, err)
			unread++
		case got.status == http.StatusNotFound && got.inChild:
			say(stdout, "MISSING AT THE ORIGIN %s: %s is an index whose linux/amd64 child %s is absent: publish it again\n", ref, at, got.image)
			missing++
		case got.status == http.StatusNotFound:
			say(stdout, "MISSING AT THE ORIGIN %s: %s is absent (ghcr has %s)\n", ref, at, want.image)
			missing++
		case got.status != http.StatusOK:
			say(stdout, "CANNOT READ %s: the origin %s answered http=%d\n", ref, base, got.status)
			unread++
		case got.noAmd64:
			say(stdout, "MISSING AT THE ORIGIN %s: %s is an index without a linux/amd64 image (ghcr has %s)\n", ref, at, want.image)
			missing++
		case got.image != want.image:
			say(stdout, "MISSING AT THE ORIGIN %s: %s carries %s, ghcr has %s (the linux/amd64 image a node pulls)\n", ref, at, got.image, want.image)
			missing++
		default:
			say(stdout, "PRESENT %s %s\n", ref, want.image)
		}
	}
	switch {
	case unread > 0:
		say(stdout, "ORIGIN-HAS CANNOT READ %d/%d (and %d missing): nothing is concluded\n", unread, len(refs), missing)
		return 2
	case missing > 0:
		say(stdout, "ORIGIN-HAS FAIL %d/%d first-party image(s) are not at %s with the digest ghcr has\n", missing, len(refs), base)
		return 1
	}
	say(stdout, "ORIGIN-HAS PASS %d first-party image(s) at %s with the digest ghcr has\n", len(refs), base)
	return 0
}

// ---- subst: Flux's substitution of the kit

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*`)

var errForm = errors.New("unsupported substitution")

func closing(text string, i int) (int, error) {
	depth := 0
	for i < len(text) {
		switch {
		case strings.HasPrefix(text[i:], "$$"):
			i += 2
			continue
		case strings.HasPrefix(text[i:], "${"):
			depth++
			i += 2
			continue
		case text[i] == '}':
			if depth == 0 {
				return i, nil
			}
			depth--
		}
		i++
	}
	return 0, fmt.Errorf("%w: a ${ is not closed", errForm)
}

func evaluate(expr string, vars map[string]string) (string, error) {
	name := varName.FindString(expr)
	if name == "" {
		return "", fmt.Errorf("%w: ${%s}", errForm, expr)
	}
	rest := expr[len(name):]
	value, set := vars[name]
	if rest == "" {
		return value, nil
	}
	for _, op := range []string{":-", ":=", "-", "="} {
		if !strings.HasPrefix(rest, op) {
			continue
		}
		use := set
		if strings.HasPrefix(op, ":") {
			use = set && value != ""
		}
		if use {
			return value, nil
		}
		return substitute(rest[len(op):], vars)
	}
	return "", fmt.Errorf("%w: ${%s}", errForm, expr)
}

// substitute is the model of scripts/check-kit-render-equivalence.sh:
// ${NAME}, ${NAME:-d}, ${NAME:=d} (the value when set and not empty, else the
// default), ${NAME-d}, ${NAME=d} (the value when set, else the default); a
// default may hold further ${...}; an unset name gives the empty text; "$$"
// gives "$"; "$NAME" without braces is left as it is.
func substitute(text string, vars map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "$$"):
			b.WriteByte('$')
			i += 2
		case strings.HasPrefix(text[i:], "${"):
			end, err := closing(text, i+2)
			if err != nil {
				return "", err
			}
			v, err := evaluate(text[i+2:end], vars)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end + 1
		default:
			b.WriteByte(text[i])
			i++
		}
	}
	return b.String(), nil
}

func readVars(path string, into map[string]string) error {
	f, err := os.Open(path) // #nosec G304 -- a values file the caller names
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || varName.FindString(k) != k {
			return fmt.Errorf("%s: %q is not KEY=VALUE", path, line)
		}
		into[k] = v
	}
	return sc.Err()
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func runSubst(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subst", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var files listFlag
	fs.Var(&files, "vars", "a KEY=VALUE file; later files win (repeatable)")
	if fs.Parse(args) != nil || fs.NArg() != 0 || len(files) == 0 {
		sayln(stderr, "usage: image-path subst -vars <file> [-vars <file>] < text")
		return 2
	}
	vars := map[string]string{}
	for _, f := range files {
		if err := readVars(f, vars); err != nil {
			sayln(stderr, err)
			return 2
		}
	}
	text, err := io.ReadAll(in)
	if err != nil {
		sayln(stderr, err)
		return 2
	}
	out, err := substitute(string(text), vars)
	if err != nil {
		sayln(stderr, err)
		return 2
	}
	_, _ = fmt.Fprint(stdout, out)
	return 0
}

func runResolve(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		sayln(stderr, "usage: image-path resolve <ref>")
		return 2
	}
	var mirrors map[string]mirror
	if err := json.Unmarshal([]byte(os.Getenv("PROBE_MIRRORS")), &mirrors); err != nil {
		sayln(stderr, "PROBE_MIRRORS holds no node registry map")
		return 2
	}
	r := resolve(args[0], mirrors)
	switch r.kind {
	case "ok":
		say(stdout, "OK %s/%s %s\n", r.base, r.repo, r.ref)
	default:
		say(stdout, "%s %s\n", strings.ToUpper(r.kind), r.why)
	}
	return 0
}

func run(args []string, in io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		sayln(stderr, "usage: image-path probe|origin-has|resolve|subst|stub ...")
		return 2
	}
	switch args[0] {
	case "probe":
		return runProbe(args[1:], in, stdout, stderr)
	case "origin-has":
		return runOriginHas(args[1:], in, stdout, stderr)
	case "resolve":
		return runResolve(args[1:], stdout, stderr)
	case "subst":
		return runSubst(args[1:], in, stdout, stderr)
	case "stub":
		return runStub(args[1:], stderr)
	}
	say(stderr, "unknown command %q\n", args[0])
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// say and sayln write a line of the report; a failed write to the report has
// nowhere else to be reported.
func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func sayln(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }
