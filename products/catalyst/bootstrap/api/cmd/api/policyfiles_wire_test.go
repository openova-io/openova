package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
)

const policyFilesFQDN = "t2.example.org"

type countingStarter struct {
	mu sync.Mutex
	n  int
}

func (s *countingStarter) Start(context.Context) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

// TestPolicyFilesWireAligns: at start-up the policy files of both regions
// receive the empty values document in one commit, and the outcome is
// logged.
func TestPolicyFilesWireAligns(t *testing.T) {
	w := fake.New()
	defer w.Close()
	for _, r := range []string{"r1", "r2"} {
		w.Put(compliance.FleetRepo, "clusters/"+policyFilesFQDN+"/regions/"+r+"/orgs/acme.yaml", []byte("kind: Kustomization\n"))
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	res, err := alignPolicyFiles(context.Background(), log, w, policyFilesFQDN)
	if err != nil || !res.Committed {
		t.Fatalf("alignPolicyFiles: %+v %v", res, err)
	}
	commits := w.Commits()
	if len(commits) != 1 || len(commits[0].Paths) != 2 {
		t.Fatalf("commits = %+v, want one commit of two files", commits)
	}
	for i, r := range []string{"r1", "r2"} {
		if want := "clusters/" + policyFilesFQDN + "/regions/" + r + "/platform/policy/policy-values.yaml"; commits[0].Paths[i] != want {
			t.Fatalf("path %d = %s, want %s", i, commits[0].Paths[i], want)
		}
	}
	if !strings.Contains(buf.String(), "compliance: policy files aligned") || !strings.Contains(buf.String(), "committed=true") {
		t.Fatalf("log = %q", buf.String())
	}
}

// TestPolicyFilesWireFailureLogged: a Sovereign with no region folder is
// logged as not aligned and commits nothing; missing Git settings are
// logged and call nothing.
func TestPolicyFilesWireFailureLogged(t *testing.T) {
	w := fake.New()
	defer w.Close()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := alignPolicyFiles(context.Background(), log, w, policyFilesFQDN); !errors.Is(err, compliance.ErrNoRegion) {
		t.Fatalf("no region: got %v, want ErrNoRegion", err)
	}
	if len(w.Commits()) != 0 || !strings.Contains(buf.String(), "compliance: policy files not aligned at start-up") {
		t.Fatalf("commits %d, log = %q", len(w.Commits()), buf.String())
	}
	buf.Reset()
	empty := func(string) string { return "" }
	if _, err := alignPolicyFilesFromEnv(context.Background(), log, empty); err == nil || !strings.Contains(err.Error(), "CATALYST_GITEA_URL") {
		t.Fatalf("no settings: got %v, want the missing settings named", err)
	}
	if !strings.Contains(buf.String(), "compliance: policy files not aligned at start-up") {
		t.Fatalf("log = %q", buf.String())
	}
}

// TestPolicyFilesWireStartsAggregator: startCompliance starts the
// aggregator once and returns while the alignment runs in the background.
// The environment the alignment reads blocks until the test releases it, so
// startCompliance can return only when the alignment is not on its own
// path, and the read must still arrive: the alignment is started.
func TestPolicyFilesWireStartsAggregator(t *testing.T) {
	s := &countingStarter{}
	called := make(chan string, 1)
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	getenv := func(k string) string {
		select {
		case called <- k:
		default:
		}
		<-release
		return ""
	}
	returned := make(chan struct{})
	go func() {
		startCompliance(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), s, getenv)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("startCompliance did not return while the alignment's getenv was blocked")
	}
	s.mu.Lock()
	n := s.n
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("Start called %d times, want 1", n)
	}
	releaseOnce()
	select {
	case k := <-called:
		if k != "CATALYST_GITEA_URL" {
			t.Fatalf("the alignment first read %q, want CATALYST_GITEA_URL", k)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startCompliance never ran the alignment: getenv was not called")
	}
}
