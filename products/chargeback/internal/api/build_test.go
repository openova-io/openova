package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
)

// The shell ui/index.html ships, placeholder and all.
const shellFixture = `<!doctype html>
<html lang="en">
  <head>
    <meta name="chargeback-build" content="__CHARGEBACK_BUILD__" />
    <script type="module" crossorigin src="/assets/index-abc123.js"></script>
  </head>
  <body><div id="root"></div></body>
</html>
`

func uiFixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte(shellFixture)},
		"assets/index-abc123.js": {Data: []byte("console.log('bundle')")},
	}
}

func getBody(t *testing.T, hh http.Handler, method, path string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	hh.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	res := rec.Result()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	res.Body.Close()
	return res, string(b)
}

// The page has to know WHICH BUILD IT IS, or a tab left open across a deploy
// cannot be told it is stale. The shell carries it; the hashed bundle the
// shell names is the same build, so stamping the shell stamps the code.
func TestShellCarriesTheBuild(t *testing.T) {
	h := &Handler{Deps: Deps{UI: uiFixture(), Version: "0.1.42", Config: config.Config{}}}
	ui := h.uiHandler()

	// Every route a browser can land on gets the stamped shell: the root,
	// index.html by name, and any deep link the router owns.
	for _, p := range []string{"/", "/index.html", "/capacity", "/customers/abc"} {
		res, body := getBody(t, ui, "GET", p)
		if !strings.Contains(body, `content="0.1.42"`) {
			t.Fatalf("%s: shell does not name the build: %s", p, body)
		}
		if strings.Contains(body, "__CHARGEBACK_BUILD__") {
			t.Fatalf("%s: placeholder survived: %s", p, body)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: Content-Type = %q", p, ct)
		}
		if cc := res.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache — a cached shell can never report a new build", p, cc)
		}
	}

	// The hashed bundle is untouched and still immutable: the whole scheme
	// rests on the shell being fresh and the asset being permanent.
	res, body := getBody(t, ui, "GET", "/assets/index-abc123.js")
	if body != "console.log('bundle')" {
		t.Fatalf("asset body rewritten: %q", body)
	}
	if cc := res.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", cc)
	}

	// HEAD answers the headers and no body.
	if res, body := getBody(t, ui, "HEAD", "/"); res.StatusCode != 200 || body != "" {
		t.Fatalf("HEAD / = %d %q", res.StatusCode, body)
	}

	// A binary that does not know its own version must not accuse anyone of
	// running a stale page: the placeholder survives and the console stays
	// dormant on it.
	quiet := (&Handler{Deps: Deps{UI: uiFixture(), Config: config.Config{}}}).uiHandler()
	if _, body := getBody(t, quiet, "GET", "/"); !strings.Contains(body, "__CHARGEBACK_BUILD__") {
		t.Fatalf("unversioned build stamped something: %s", body)
	}

	// The version is escaped into the attribute, never able to close it.
	sneaky := (&Handler{Deps: Deps{UI: uiFixture(), Version: `1"><script>x()</script>`, Config: config.Config{}}}).uiHandler()
	if _, body := getBody(t, sneaky, "GET", "/"); strings.Contains(body, "<script>x()") {
		t.Fatalf("version escaped its attribute: %s", body)
	}
}

// Detection has to cost nothing: the build rides on every API response the
// console already asks for, including the one that just failed.
func TestAPIResponsesNameTheBuild(t *testing.T) {
	h := &Handler{Deps: Deps{Config: config.Config{}, Metrics: metrics.New(), Now: time.Now, Version: "0.1.42"}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	chained := h.chain(inner)

	res, _ := getBody(t, chained, "GET", "/api/v1/capacity/overview")
	if got := res.Header.Get(BuildHeader); got != "0.1.42" {
		t.Fatalf("%s on a read = %q", BuildHeader, got)
	}
	// A FAILED write is exactly the response that has to carry it — that is
	// the case where the console must say why the save did not take.
	res, _ = getBody(t, chained, "PUT", "/api/v1/capacity/pools/p1")
	if got := res.Header.Get(BuildHeader); got != "0.1.42" {
		t.Fatalf("%s on a failed write = %q", BuildHeader, got)
	}

	// Not on the immutable static assets, where a stamped header would be
	// cached for a year and lie.
	res, _ = getBody(t, chained, "GET", "/assets/index-abc123.js")
	if got := res.Header.Get(BuildHeader); got != "" {
		t.Fatalf("static asset carries %s = %q", BuildHeader, got)
	}

	quiet := (&Handler{Deps: Deps{Config: config.Config{}, Metrics: metrics.New(), Now: time.Now}}).chain(inner)
	res, _ = getBody(t, quiet, "GET", "/api/v1/capacity/overview")
	if got := res.Header.Get(BuildHeader); got != "" {
		t.Fatalf("unversioned binary sent %s = %q", BuildHeader, got)
	}
}
