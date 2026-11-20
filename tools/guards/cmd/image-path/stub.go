package main

// The registry stand-in of the self-tests (the Go test of this command and
// scripts/check-mothership-proxy-image-path.sh --self-test). It answers the
// way the origin answered on 20 November 2026:
//
//	*/healthy/app      index -> linux/amd64 child -> config and two layers
//	*/single/app       one manifest, no index
//	*/redirected/app   301 to */healthy/app, tag, digest and blob alike
//	*/damaged/app      the index is served, its linux/amd64 child is unknown
//	*/filling/app      the layers answer 404 to HEAD until -fill-after has
//	                   passed since the first request of a manifest of the repo
//	*/neverfill/app    the layers never fill
//	openova-io/openova/<name>  a first-party image at any tag: one image
//	                   manifest, the way the warm-up's copy writes it; -indexed
//	                   serves it the way ghcr publishes it, an index whose
//	                   linux/amd64 child is that manifest; -childless serves the
//	                   index while its child is unknown; -absent names a
//	                   repository the registry lacks; -stale gives it another
//	                   image
//
// Every /v2/ path but the ping needs a Bearer token from /token, which needs
// the Basic login -user/-password when -user is set.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	okChild     = "sha256:" + strings.Repeat("a", 64)
	brokenChild = "sha256:" + strings.Repeat("b", 64)
	stubConfig  = "sha256:" + strings.Repeat("c", 64)
	stubLayer1  = "sha256:" + strings.Repeat("e", 64)
	stubLayer2  = "sha256:" + strings.Repeat("f", 64)
)

type stubOptions struct {
	user, password                    string
	fillAfter                         time.Duration
	absent, stale, indexed, childless map[string]bool
}

type stub struct {
	opt   stubOptions
	mu    sync.Mutex
	first map[string]time.Time
}

func newStub(opt stubOptions) *stub {
	if opt.absent == nil {
		opt.absent = map[string]bool{}
	}
	if opt.stale == nil {
		opt.stale = map[string]bool{}
	}
	if opt.indexed == nil {
		opt.indexed = map[string]bool{}
	}
	if opt.childless == nil {
		opt.childless = map[string]bool{}
	}
	return &stub{opt: opt, first: map[string]time.Time{}}
}

func stubIndex(child string) []byte {
	b, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": child, "size": 1,
				"platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + strings.Repeat("d", 64), "size": 1,
				"platform": map[string]string{"os": "linux", "architecture": "arm64"}},
		},
	})
	return b
}

func stubManifest(note string, layers ...string) []byte {
	ls := []map[string]any{}
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": l, "size": 1})
	}
	b, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config":      map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": stubConfig, "size": 1},
		"layers":      ls,
		"annotations": map[string]string{"stub": note},
	})
	return b
}

func (s *stub) send(w http.ResponseWriter, code int, ctype string, body []byte) {
	w.Header().Set("Content-Type", ctype)
	if code == http.StatusOK && (strings.Contains(ctype, "manifest") || strings.Contains(ctype, "index")) {
		sum := sha256.Sum256(body)
		w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func (s *stub) unknown(w http.ResponseWriter, code string) {
	s.send(w, http.StatusNotFound, "application/json", []byte(`{"errors":[{"code":"`+code+`"}]}`))
}

func (s *stub) authorized(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer stub-token"
}

func (s *stub) token(w http.ResponseWriter, r *http.Request) {
	if s.opt.user != "" {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.opt.user+":"+s.opt.password))
		if r.Header.Get("Authorization") != want {
			s.send(w, http.StatusUnauthorized, "application/json", []byte(`{"errors":[{"code":"UNAUTHORIZED"}]}`))
			return
		}
	}
	s.send(w, http.StatusOK, "application/json", []byte(`{"token":"stub-token"}`))
}

func (s *stub) filled(repo string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.first[repo]
	return ok && time.Since(t) >= s.opt.fillAfter
}

func (s *stub) asked(repo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.first[repo]; !ok {
		s.first[repo] = time.Now()
	}
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/token":
		s.token(w, r)
		return
	case p == "/v2/" || p == "/v2":
		s.send(w, http.StatusOK, "application/json", []byte("{}"))
		return
	case !strings.HasPrefix(p, "/v2/"):
		s.unknown(w, "NOT_FOUND")
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="stub",scope="repository:x:pull"`, r.Host))
		s.send(w, http.StatusUnauthorized, "application/json", []byte(`{"errors":[{"code":"UNAUTHORIZED"}]}`))
		return
	}
	parts := strings.Split(strings.TrimPrefix(p, "/v2/"), "/")
	if len(parts) < 3 {
		s.unknown(w, "NAME_UNKNOWN")
		return
	}
	kind, ref := parts[len(parts)-2], parts[len(parts)-1]
	repo := strings.Join(parts[:len(parts)-2], "/")
	if strings.HasSuffix(repo, "redirected/app") {
		http.Redirect(w, r, fmt.Sprintf("/v2/%s/%s/%s", strings.TrimSuffix(repo, "redirected/app")+"healthy/app", kind, ref), http.StatusMovedPermanently)
		return
	}
	switch kind {
	case "manifests":
		s.manifest(w, repo, ref)
	case "blobs":
		s.blob(w, repo, ref)
	default:
		s.unknown(w, "NAME_UNKNOWN")
	}
}

func (s *stub) manifest(w http.ResponseWriter, repo, ref string) {
	const index, single = "application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json"
	if strings.HasPrefix(repo, "openova-io/openova/") {
		note := repo
		if s.opt.stale[repo] {
			note += " (stale)"
		}
		image := stubManifest(note, stubLayer1)
		sum := sha256.Sum256(image)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		switch {
		case s.opt.absent[repo]:
			s.unknown(w, "MANIFEST_UNKNOWN")
		case strings.HasPrefix(ref, "sha256:") && (ref != digest || s.opt.childless[repo]):
			s.unknown(w, "MANIFEST_UNKNOWN")
		case !strings.HasPrefix(ref, "sha256:") && (s.opt.indexed[repo] || s.opt.childless[repo]):
			s.send(w, http.StatusOK, index, stubIndex(digest))
		default:
			s.send(w, http.StatusOK, single, image)
		}
		return
	}
	s.asked(repo)
	indexed := false
	for _, k := range []string{"healthy/app", "damaged/app", "filling/app", "neverfill/app"} {
		indexed = indexed || strings.HasSuffix(repo, k)
	}
	switch {
	case strings.HasSuffix(repo, "single/app"):
		s.send(w, http.StatusOK, single, stubManifest(repo, stubLayer1))
	case !indexed:
		s.unknown(w, "MANIFEST_UNKNOWN")
	case !strings.HasPrefix(ref, "sha256:") && strings.HasSuffix(repo, "damaged/app"):
		s.send(w, http.StatusOK, index, stubIndex(brokenChild))
	case !strings.HasPrefix(ref, "sha256:"):
		s.send(w, http.StatusOK, index, stubIndex(okChild))
	case ref == okChild:
		s.send(w, http.StatusOK, single, stubManifest("child", stubLayer1, stubLayer2))
	default:
		s.unknown(w, "MANIFEST_UNKNOWN") // the damaged cache entry
	}
}

func (s *stub) blob(w http.ResponseWriter, repo, digest string) {
	present := digest == stubConfig || digest == stubLayer1 || digest == stubLayer2
	switch {
	case strings.HasPrefix(repo, "openova-io/openova/") && s.opt.absent[repo]:
		present = false
	case digest == stubConfig:
	case strings.HasSuffix(repo, "neverfill/app"):
		present = false
	case strings.HasSuffix(repo, "filling/app"):
		present = present && s.filled(repo)
	}
	if !present {
		s.unknown(w, "BLOB_UNKNOWN")
		return
	}
	s.send(w, http.StatusOK, "application/octet-stream", []byte("{}"))
}

func commaSet(v string) map[string]bool {
	m := map[string]bool{}
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			m[x] = true
		}
	}
	return m
}

func runStub(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	portFile := fs.String("port-file", "", "the file the port is written to once the stand-in listens")
	user := fs.String("user", "", "the Basic login /token requires (none: any)")
	password := fs.String("password", "", "its password")
	fillAfter := fs.Duration("fill-after", 2*time.Second, "how long */filling/app takes to fill its layers")
	absent := fs.String("absent", "", "first-party repositories (openova-io/openova/<name>) this registry lacks, comma separated")
	stale := fs.String("stale", "", "first-party repositories this registry holds with another digest, comma separated")
	indexed := fs.String("indexed", "", "first-party repositories served as an index over their linux/amd64 image (as ghcr publishes them), comma separated")
	childless := fs.String("childless", "", "first-party repositories whose index is served while its linux/amd64 child is unknown, comma separated")
	if fs.Parse(args) != nil || *portFile == "" {
		return 2
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		sayln(stderr, err)
		return 2
	}
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(*portFile+".tmp", []byte(port), 0o600); err != nil {
		sayln(stderr, err)
		return 2
	}
	if err := os.Rename(*portFile+".tmp", *portFile); err != nil {
		sayln(stderr, err)
		return 2
	}
	srv := &http.Server{Handler: newStub(stubOptions{user: *user, password: *password, fillAfter: *fillAfter,
		absent: commaSet(*absent), stale: commaSet(*stale), indexed: commaSet(*indexed), childless: commaSet(*childless)}),
		ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		sayln(stderr, err)
		return 2
	}
	return 0
}
