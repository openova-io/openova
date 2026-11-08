package wordpress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsConfiguredHome(t *testing.T) {
	ok := &Page{Status: 200, FinalURL: "https://x/", Body: []byte(`<link href="/wp-content/themes/a.css">`)}
	if c, d := IsConfiguredHome(ok); !c {
		t.Fatalf("configured home refused: %s", d)
	}
	inst := &Page{Status: 200, FinalURL: "https://x/wp-admin/install.php", Body: []byte(`wp-content`)}
	if c, _ := IsConfiguredHome(inst); c {
		t.Fatal("installer accepted as a configured site")
	}
	bad := &Page{Status: 502, FinalURL: "https://x/"}
	if c, _ := IsConfiguredHome(bad); c {
		t.Fatal("502 accepted")
	}
	notwp := &Page{Status: 200, FinalURL: "https://x/", Body: []byte(`<h1>default backend</h1>`)}
	if c, _ := IsConfiguredHome(notwp); c {
		t.Fatal("a non-WordPress 200 accepted")
	}
}

func TestRandomPNGIsUniqueAndValid(t *testing.T) {
	a, ha := RandomPNG("m1")
	b, hb := RandomPNG("m2")
	if ha == hb || bytes.Equal(a, b) {
		t.Fatal("two markers produced the same bytes — byte equality would prove nothing")
	}
	if _, err := png.Decode(bytes.NewReader(a)); err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	s := sha256.Sum256(a)
	if hex.EncodeToString(s[:]) != ha {
		t.Fatal("returned hash is not sha256(bytes)")
	}
}

func TestParseAppPassword(t *testing.T) {
	if p, err := ParseAppPassword("noise\nAPPPW:abcd efgh\n"); err != nil || p != "abcd efgh" {
		t.Fatalf("got %q %v", p, err)
	}
	if _, err := ParseAppPassword("Warning: x"); err == nil {
		t.Fatal("missing password accepted")
	}
}

func TestEnsureAuthorScriptQuotes(t *testing.T) {
	s := EnsureAuthorScript("h", "u'x", `p\'q`, "r")
	if strings.Contains(s, "'u'x'") {
		t.Fatal("single quote not escaped")
	}
}

func TestRESTWritesUseBasicAuthAndRestRoute(t *testing.T) {
	var gotRoute, gotUser string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRoute = r.URL.Query().Get("rest_route")
		gotUser, _, _ = r.BasicAuth()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7,"status":"publish","link":"https://x/?p=7"}`))
	}))
	defer srv.Close()
	s := New(strings.TrimPrefix(srv.URL, "https://"), true)
	s.User, s.AppPassword = "bot", "pw"
	p, _, err := s.CreatePost(context.Background(), "t", "c")
	if err != nil || p.ID != 7 {
		t.Fatalf("%v %v", p, err)
	}
	if gotRoute != "/wp/v2/posts" || gotUser != "bot" {
		t.Fatalf("route=%q user=%q", gotRoute, gotUser)
	}
}
