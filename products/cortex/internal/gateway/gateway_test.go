package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fake is a gateway that records the last request and answers with status and body.
type fake struct {
	auth, route string
	in          map[string]any
	status      int
	body        string
}

func start(t *testing.T, f *fake) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth, f.route, f.in = r.Header.Get("Authorization"), r.URL.Path, nil
		_ = json.NewDecoder(r.Body).Decode(&f.in)
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(key, []byte("vk-cortex-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(srv.URL+"/v1/", key, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEmbed(t *testing.T) {
	f := &fake{status: 200, body: `{"data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}]}`}
	e := start(t, f).Embedder("embed-default", 2)
	got, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil || !reflect.DeepEqual(got, [][]float32{{1, 2}, {3, 4}}) || e.Dimensions() != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.route != "/v1/embeddings" || f.auth != "Bearer vk-cortex-1" || f.in["model"] != "embed-default" || !reflect.DeepEqual(f.in["input"], []any{"a", "b"}) {
		t.Fatalf("the gateway got %s %q %v", f.route, f.auth, f.in)
	}
	for _, body := range []string{
		`{"data":[{"index":0,"embedding":[1,2]}]}`,                                 // one vector for two texts
		`{"data":[{"index":0,"embedding":[1,2]},{"index":0,"embedding":[3,4]}]}`,   // an index twice
		`{"data":[{"index":0,"embedding":[1,2]},{"index":1,"embedding":[3,4,5]}]}`, // a vector of another length
		`{"data":[{"index":0,"embedding":[1,2]},{"index":2,"embedding":[3,4]}]}`,   // an index out of range
		`not json`,
	} {
		f.body = body
		if _, err := e.Embed(context.Background(), []string{"a", "b"}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: got %v, want ErrUnavailable", body, err)
		}
	}
}

func TestAFailedCallNamesNoText(t *testing.T) {
	f := &fake{status: 400, body: `{"error":"bad input: the secret quarterly figures"}`}
	_, err := start(t, f).Embedder("embed-default", 2).Embed(context.Background(), []string{"the secret quarterly figures"})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("got %v", err)
	}
}

func TestTheKeyIsReadOnEveryCall(t *testing.T) {
	f := &fake{status: 200, body: `{"data":[{"index":0,"embedding":[1]}]}`}
	c := start(t, f)
	if err := os.WriteFile(c.keyFile, []byte("vk-cortex-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embedder("e", 1).Embed(context.Background(), []string{"a"}); err != nil || f.auth != "Bearer vk-cortex-2" {
		t.Fatalf("after a rotation the gateway got %q (%v)", f.auth, err)
	}
	if err := os.Remove(c.keyFile); err != nil {
		t.Fatal(err)
	}
	f.auth = ""
	if _, err := c.Embedder("e", 1).Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrUnavailable) || f.auth != "" {
		t.Fatalf("without a key file: %v, and the gateway was called with %q", err, f.auth)
	}
}

func TestRerank(t *testing.T) {
	f := &fake{status: 200, body: `{"results":[{"index":2,"relevance_score":0.2},{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.5}]}`}
	r := start(t, f).Reranker("rerank-default")
	got, err := r.Rerank(context.Background(), "q", []string{"a", "b", "c"}, 2)
	if err != nil || !reflect.DeepEqual(got, []Ranked{{0, 0.9}, {1, 0.5}}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.route != "/v1/rerank" || f.in["model"] != "rerank-default" || f.in["query"] != "q" || f.in["top_n"] != float64(2) {
		t.Fatalf("the gateway got %s %v", f.route, f.in)
	}
	for _, body := range []string{`{"results":[{"index":3,"relevance_score":1}]}`, `{"results":[{"index":0,"relevance_score":1},{"index":0,"relevance_score":1}]}`} {
		f.body = body
		if _, err := r.Rerank(context.Background(), "q", []string{"a", "b", "c"}, 2); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: got %v", body, err)
		}
	}
}

func TestNewRefusesABadAddress(t *testing.T) {
	for _, base := range []string{"", "model-gateway:8080/v1", "ftp://gw/v1", "http://user@gw/v1", "http://gw/v1?x=1"} {
		if _, err := New(base, DefaultKeyFile, nil); err == nil {
			t.Errorf("%q was accepted", base)
		}
	}
	if _, err := New(DefaultBaseURL, "", nil); err == nil {
		t.Error("no key file was accepted")
	}
}
