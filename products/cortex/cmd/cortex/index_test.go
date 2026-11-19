package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/jobs"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// fakeIndex reports the definitions it is given, each claim made of it, and its close.
type fakeIndex struct {
	ingest.IndexStore
	jobs.Queue
	applied chan []store.SourceDefinition
	claimed chan struct{}
	closed  chan struct{}
}

func (f *fakeIndex) Ping(context.Context) error { return nil }
func (f *fakeIndex) Close()                     { close(f.closed) }
func (f *fakeIndex) ApplyDefinitions(_ context.Context, defs []store.SourceDefinition) error {
	f.applied <- defs
	return nil
}
func (f *fakeIndex) Claim(context.Context, time.Duration) (*store.Job, error) {
	select {
	case f.claimed <- struct{}{}:
	default:
	}
	return nil, nil
}
func (f *fakeIndex) NextDue(context.Context) (time.Duration, bool, error) { return 0, false, nil }
func (f *fakeIndex) Listen(ctx context.Context, fn func(string)) error {
	fn("")
	<-ctx.Done()
	return nil
}

func indexFlags(t *testing.T, more ...string) []string {
	return append([]string{"index", "--database-url-file", dbFile(t, "postgres://cortex@db/cortex\n"), "--embed-alias", "embed-default",
		"--embed-dimensions", "1024", "--originals-dir", t.TempDir(), "--namespace", "acme-ai", "--health-listen", "127.0.0.1:0"}, more...)
}

// TestIndexServesAndStops runs the index process against a fake cluster that holds the
// definitions: it applies them, runs a claim pass and answers its health paths.
func TestIndexServesAndStops(t *testing.T) {
	db := &fakeIndex{applied: make(chan []store.SourceDefinition, 4), claimed: make(chan struct{}, 1), closed: make(chan struct{})}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "acme-ai", Name: "cortex-definitions"}, Data: map[string]string{
		"definitions.yaml": "sources: [{id: hr, title: HR, connector: {type: upload}, access: {readers: [], managers: ['group:a']}}]"}}
	ctx, cancel := context.WithCancel(context.Background())
	health := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		done <- run(ctx, indexFlags(t), &out, deps{openBucket: bucket,
			kube:      func() (kubernetes.Interface, error) { return fake.NewSimpleClientset(cm), nil },
			openIndex: func(context.Context, string, int) (indexDB, error) { return db, nil },
			listening: func(_, h net.Addr) { health <- h }})
	}()
	select {
	case defs := <-db.applied:
		if len(defs) != 1 || defs[0].ID != "hr" || defs[0].Connector != "upload" {
			t.Fatalf("applied %+v", defs)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the definitions were not applied:\n%s", &out)
	}
	h := <-health
	for path, want := range map[string]int{"/readyz": 200, "/healthz": 200, "/v1/sources": 404} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+h.String()+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	select {
	case <-db.claimed:
	case <-time.After(10 * time.Second):
		t.Fatal("no claim pass ran")
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d:\n%s", code, &out)
	}
	select {
	case <-db.closed:
	default:
		t.Fatal("the database was not closed")
	}
}
