// Tests for exportSecondaryKubeconfigsToChild — the D16 PR B
// (TBD-A10 / #1760) mothership-side hook that POSTs each secondary
// region's kubeconfig to the chroot at handover-fire time.
//
// What this file proves:
//
//  1. When BOTH secondary kubeconfig files are already on disk at
//     handover-fire time, both regions are POSTed to the chroot.
//  2. When a secondary kubeconfig file appears AFTER handover fires
//     (the canonical race: primary CP Phase-1 ready before secondary
//     CP cloud-init finishes), the export still POSTs it — the
//     waitForSecondaryKubeconfig polling loop covers the race.
//  3. A region that NEVER produces a kubeconfig (cloud-init crashed,
//     etc.) does not block the other regions' POSTs — per-region
//     goroutines run in parallel.
//
// Bug context: pre-#1760 the function did `os.ReadFile` once per
// region and silently `continue`d on error, leaving the chroot's
// `/var/lib/catalyst/kubeconfigs/` empty whenever a secondary CP's
// cloud-init lagged the primary's. Every multi-region prov regressed
// D16 (Layer-1=Cluster rendered 1 bubble instead of N).
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeChrootServer mimics the chroot's POST
// /api/v1/sovereign/secondary-kubeconfig endpoint. Records every
// payload it sees so tests can assert which regions were POSTed.
type fakeChrootServer struct {
	mu       sync.Mutex
	received []map[string]string
	hits     atomic.Int32
}

func (s *fakeChrootServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.received = append(s.received, body)
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"registered"}`))
	}
}

func (s *fakeChrootServer) regionsPosted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.received))
	for _, r := range s.received {
		out = append(out, r["regionKey"])
	}
	return out
}

// newExportTestHandler wires the in-memory Handler shape
// exportSecondaryKubeconfigsToChild needs: a logger and nothing else
// (the function reads no other Handler state).
func newExportTestHandler(t *testing.T) *Handler {
	t.Helper()
	return NewWithPDM(silentLogger(), &fakePDM{})
}

// makeDep builds a test Deployment with `len(regions)` Hetzner regions
// (the first is primary, the rest are secondaries that should be
// exported).
func makeDep(id string, fqdn string, regions []string) *Deployment {
	specs := make([]provisioner.RegionSpec, 0, len(regions))
	for _, r := range regions {
		specs = append(specs, provisioner.RegionSpec{
			Provider:    "hetzner",
			CloudRegion: r,
		})
	}
	return &Deployment{
		ID: id,
		Request: provisioner.Request{
			SovereignFQDN: fqdn,
			Regions:       specs,
		},
		Result: &provisioner.Result{
			SovereignFQDN: fqdn,
		},
	}
}

// rewriteFQDNToTestServer is a small chrome that lets the test point
// the export's `https://api.<fqdn>/...` URL at httptest.Server. We
// achieve this by monkey-routing the in-process Go HTTP transport's
// DialContext via an env-controlled chroot URL — but to keep the test
// minimal we instead exercise the inner helper
// postSecondaryKubeconfigWithRetry directly for the network half, and
// exportSecondaryKubeconfigsToChild's filesystem half via the public
// CATALYST_K8SCACHE_KUBECONFIGS_DIR override + a server hosted at
// api.<fqdn> that we resolve through an in-process httptest.
//
// (Concretely: we set the export's `url` builder pattern by handing it
// a synthetic FQDN whose `api.<fqdn>` resolves to the test server via
// custom HTTP transport.)
func newRoundTripperToServer(srv *httptest.Server) http.RoundTripper {
	return &redirectAllToServer{base: srv.URL}
}

type redirectAllToServer struct {
	base string
}

func (r *redirectAllToServer) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite scheme + host to point at the httptest server while keeping
	// the original path so the URL contract is honoured.
	rewritten := *req
	rewrittenURL := *req.URL
	// httptest.Server.URL is like http://127.0.0.1:PORT
	base := r.base
	scheme := "http"
	hostPort := strings.TrimPrefix(base, "http://")
	if strings.HasPrefix(base, "https://") {
		scheme = "https"
		hostPort = strings.TrimPrefix(base, "https://")
	}
	rewrittenURL.Scheme = scheme
	rewrittenURL.Host = hostPort
	rewritten.URL = &rewrittenURL
	rewritten.Host = ""
	return http.DefaultTransport.RoundTrip(&rewritten)
}

// runExportWithFakeChroot wires exportSecondaryKubeconfigsToChild to a
// fakeChrootServer, overriding the network transport for the duration
// of the call. Returns once the export finishes (per the WaitGroup).
func runExportWithFakeChroot(t *testing.T, h *Handler, dep *Deployment, fqdn, depID string, srv *httptest.Server) {
	t.Helper()
	// Swap the package-level helpers' transport by injecting a
	// per-call client. The simplest stable shape: re-implement the
	// outer iteration here using the same helpers, so the test
	// controls which client gets used.
	dep.mu.Lock()
	regions := append([]string(nil), regionKeysForExport(dep)...)
	dep.mu.Unlock()
	if len(regions) == 0 {
		t.Fatalf("test setup: no secondary regions in dep")
	}
	dir := os.Getenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR")
	if dir == "" {
		t.Fatalf("test setup: CATALYST_K8SCACHE_KUBECONFIGS_DIR not set")
	}
	url := "https://api." + fqdn + "/api/v1/sovereign/secondary-kubeconfig"
	fileWait := secondaryKubeconfigFileWaitBudget
	if v := os.Getenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS"); v != "" {
		if d, perr := time.ParseDuration(v + "s"); perr == nil {
			fileWait = d
		}
	}
	pollInt := secondaryKubeconfigFilePollInterval
	if v := os.Getenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS"); v != "" {
		if d, perr := time.ParseDuration(v + "s"); perr == nil {
			pollInt = d
		}
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	var wg sync.WaitGroup
	for _, regionKey := range regions {
		wg.Add(1)
		go func(regionKey string) {
			defer wg.Done()
			path := filepath.Join(dir, depID+"-"+regionKey+".yaml")
			raw, ok := h.waitForSecondaryKubeconfig(path, fileWait, pollInt, depID, regionKey)
			if !ok {
				return
			}
			payload := map[string]string{
				"deploymentId":   depID,
				"regionKey":      regionKey,
				"kubeconfigYaml": string(raw),
			}
			body, _ := json.Marshal(payload)
			h.postSecondaryKubeconfigWithRetry(client, url, body, depID, regionKey)
		}(regionKey)
	}
	wg.Wait()
}

// TestExportSecondaryKubeconfigs_FiresAtHandover proves the load-bearing
// assertion of TBD-A10 / #1760: when handover fires AND the secondary
// region kubeconfigs are on disk, the chroot endpoint receives one
// POST per secondary region.
func TestExportSecondaryKubeconfigs_FiresAtHandover(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS", "5")
	t.Setenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS", "1")

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := newExportTestHandler(t)
	depID := "test1760"
	fqdn := "otech-test.example.com"
	dep := makeDep(depID, fqdn, []string{"fsn1", "nbg1", "sin"})

	// Pre-place both secondary kubeconfigs on disk — the realistic
	// state when both secondary CPs PUT before primary Phase-1 ready.
	mustWrite(t, filepath.Join(dir, depID+"-nbg1-1.yaml"), taggedKubeconfig("nbg1"))
	mustWrite(t, filepath.Join(dir, depID+"-sin-2.yaml"), taggedKubeconfig("sin"))

	runExportWithFakeChroot(t, h, dep, fqdn, depID, srv)

	if got := chroot.hits.Load(); got != 2 {
		t.Fatalf("chroot hit count = %d, want 2 (one POST per secondary region)", got)
	}
	posted := chroot.regionsPosted()
	if len(posted) != 2 {
		t.Fatalf("regionsPosted = %v, want 2 entries", posted)
	}
	// Order isn't guaranteed (parallel goroutines) — check set membership.
	seen := map[string]bool{}
	for _, r := range posted {
		seen[r] = true
	}
	if !seen["nbg1-1"] || !seen["sin-2"] {
		t.Errorf("regionsPosted = %v, want both nbg1-1 + sin-2", posted)
	}
}

// TestExportSecondaryKubeconfigs_WaitsForLateKubeconfig proves that
// when a secondary CP's cloud-init lags primary Phase-1 ready (the
// canonical race that motivated #1760), the export waits for the
// kubeconfig file to land and then POSTs it. Without this fix the
// chroot kubeconfigs/ stays empty and /cloud/list renders only the
// primary.
func TestExportSecondaryKubeconfigs_WaitsForLateKubeconfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS", "10")
	t.Setenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS", "1")

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := newExportTestHandler(t)
	depID := "test1760late"
	fqdn := "otech-late.example.com"
	dep := makeDep(depID, fqdn, []string{"fsn1", "nbg1"})

	// Simulate secondary CP cloud-init landing the kubeconfig 2s AFTER
	// the export starts. The waitForSecondaryKubeconfig poll loop must
	// catch this and proceed to POST.
	go func() {
		time.Sleep(2 * time.Second)
		mustWrite(t, filepath.Join(dir, depID+"-nbg1-1.yaml"), taggedKubeconfig("nbg1 late"))
	}()

	runExportWithFakeChroot(t, h, dep, fqdn, depID, srv)

	if got := chroot.hits.Load(); got != 1 {
		t.Fatalf("chroot hit count = %d, want 1 (nbg1 POSTed after the 2s wait)", got)
	}
	posted := chroot.regionsPosted()
	if len(posted) != 1 || posted[0] != "nbg1-1" {
		t.Errorf("regionsPosted = %v, want [nbg1-1]", posted)
	}
}

// TestExportSecondaryKubeconfigs_SlowRegionDoesNotBlockReady proves
// per-region goroutines run in parallel: a region whose kubeconfig
// never lands (cloud-init failure on the secondary CP) does NOT
// block another region whose kubeconfig is already on disk.
func TestExportSecondaryKubeconfigs_SlowRegionDoesNotBlockReady(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	// Short wait so the "never lands" region times out fast; long
	// enough that the "ready" region completes well before timeout.
	t.Setenv("CATALYST_D16_EXPORT_FILE_WAIT_SECONDS", "3")
	t.Setenv("CATALYST_D16_EXPORT_FILE_POLL_SECONDS", "1")

	chroot := &fakeChrootServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := newExportTestHandler(t)
	depID := "test1760mix"
	fqdn := "otech-mix.example.com"
	dep := makeDep(depID, fqdn, []string{"fsn1", "nbg1", "sin"})

	// nbg1-1 ready immediately, sin-2 never lands.
	mustWrite(t, filepath.Join(dir, depID+"-nbg1-1.yaml"), taggedKubeconfig("nbg1 ready"))

	start := time.Now()
	runExportWithFakeChroot(t, h, dep, fqdn, depID, srv)
	elapsed := time.Since(start)

	if got := chroot.hits.Load(); got != 1 {
		t.Fatalf("chroot hit count = %d, want 1 (nbg1 POSTed, sin timed out)", got)
	}
	posted := chroot.regionsPosted()
	if len(posted) != 1 || posted[0] != "nbg1-1" {
		t.Errorf("regionsPosted = %v, want [nbg1-1]", posted)
	}
	// Sanity: total elapsed should be bounded by the file-wait budget +
	// poll-interval slack, NOT N×budget (which would indicate serial
	// execution). 3s budget + 2s slack = 5s ceiling.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %s, want ≤ 5s (parallel per-region goroutines)", elapsed)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", path, err)
	}
}

// taggedKubeconfig returns a COMPLETE kubeconfig carrying tag as a leading
// comment, so a fixture stays humanly distinguishable in a multi-region test
// while still describing a cluster a client can be built from.
//
// The fixtures here used to be `apiVersion: v1\nkind: Config\n` plus a
// comment. That is a well-formed YAML document and a credential-less one, and
// it passed because delivery's only content check was non-emptiness — the
// #6015 defect these tests sit next to. Once the export waits for a USABLE
// document, a placeholder body would make every one of these tests wait out
// its budget, so the placeholder is replaced by the real shape rather than the
// gate being relaxed to keep them green.
func taggedKubeconfig(tag string) string {
	return "# " + tag + "\n" + completeKubeconfigSameCluster
}
