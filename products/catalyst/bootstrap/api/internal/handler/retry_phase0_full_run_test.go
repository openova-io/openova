// Tests for "a retry of the first phase is a full retry": a retry of a phase
// is the same code path as its first run (same deployment id, same working
// directory, same bounds, same follow-up phases), and two writers of one
// record's result merge their fields, never replace each other.
//
// Phase 0 is replaced by a stub (Handler.provision); the Phase-1 watch runs
// for real against a fake cluster whose HelmReleases are all ready, so a watch
// that starts ends ready within milliseconds.
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// phase0Stub stands in for Phase 0: it records each request and the time left
// until its context's deadline, and answers result (copied) and err.
type phase0Stub struct {
	mu     sync.Mutex
	reqs   []provisioner.Request
	left   []time.Duration
	result *provisioner.Result
	err    error
}

func (s *phase0Stub) provision(ctx context.Context, req provisioner.Request, _ chan<- provisioner.Event) (*provisioner.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if d, ok := ctx.Deadline(); ok {
		s.left = append(s.left, time.Until(d))
	}
	if s.result == nil {
		return nil, s.err
	}
	r := *s.result
	return &r, s.err
}

func (s *phase0Stub) calls() ([]provisioner.Request, []time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]provisioner.Request(nil), s.reqs...), append([]time.Duration(nil), s.left...)
}

// phase0Result is what a successful Phase 0 returns. No load-balancer address,
// so the parent-zone write skips instead of calling PowerDNS.
func phase0Result(id string) *provisioner.Result {
	fqdn := "hw307." + id + ".example"
	return &provisioner.Result{SovereignFQDN: fqdn, ControlPlaneIP: "192.0.2.10", ConsoleURL: "https://console." + fqdn, GitOpsRepoURL: "https://gitea." + fqdn}
}

// wireRerun points h's Phase 0 at stub and its Phase-1 watch at a ready cluster.
func wireRerun(h *Handler, stub *phase0Stub) {
	h.provision = stub.provision
	h.dynamicFactory = fakeDynamicFactoryFromObjects(makeReadyHR("bp-cilium"), makeReadyHR("bp-flux"))
	h.phase1WatchTimeout = 5 * time.Second
	h.kubeconfigArrivalTimeout = 300 * time.Millisecond
	h.kubeconfigArrivalPollInterval = 20 * time.Millisecond
	h.suppressPostHandoverHooks = true
}

// awaitRun waits until the run that owns dep's done channel has finished.
func awaitRun(t *testing.T, dep *Deployment) {
	t.Helper()
	dep.mu.Lock()
	done := dep.done
	dep.mu.Unlock()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the run of %s did not finish within 30s", dep.ID)
	}
}

// Claims 1 and 3: the Phase-0 retry is runProvisioning. It has the first run's
// 180-minute bound (the retry had 30), and after Phase 0 it starts the Phase-1
// watch again although the first run's watch ran in this process
// (phase1Started), so the record leaves "provisioning" and the watch flips it.
func TestRetryPhase0_RunsTheFirstRunPath(t *testing.T) {
	stub := &phase0Stub{result: phase0Result("p0-rerun")}
	h := NewWithPDM(silentLogger(), &fakePDM{})
	wireRerun(h, stub)
	dep := makeTimedOutDeployment(t, h, "p0-rerun", true)
	dep.Request.DeploymentID = dep.ID
	dep.phase1Started = true // the first run's watch ran here and timed out
	kubeconfig := dep.Result.KubeconfigPath
	since := time.Now()

	rr, body := postRetryPhase(t, h, dep.ID, "tofu-apply")
	if rr.Code != http.StatusOK || body["status"] != "provisioning" {
		t.Fatalf("want 200 and status provisioning, got %d %s", rr.Code, rr.Body.String())
	}
	awaitRun(t, dep)

	reqs, left := stub.calls()
	if len(reqs) != 1 || len(left) != 1 {
		t.Fatalf("the retry ran Phase 0 through runProvisioning %d times, want once", len(reqs))
	}
	if reqs[0].DeploymentID != dep.ID {
		t.Errorf("the retry's request has deployment id %q, want %q", reqs[0].DeploymentID, dep.ID)
	}
	if left[0] < 179*time.Minute {
		t.Errorf("the retry's Phase 0 had %s until its deadline, want the first run's 180 minutes", left[0].Round(time.Minute))
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" || dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		t.Fatalf("after the retry: status %q, phase1Outcome %q (error %q); want ready from this run's watch", dep.Status, dep.Result.Phase1Outcome, dep.Error)
	}
	if dep.Result.Phase1FinishedAt == nil || dep.Result.Phase1FinishedAt.Before(since) {
		t.Errorf("Phase1FinishedAt %v is not this run's", dep.Result.Phase1FinishedAt)
	}
	if dep.Result.ConsoleURL != "https://console.hw307.p0-rerun.example" || dep.Result.KubeconfigPath != kubeconfig {
		t.Errorf("merged result: consoleURL %q, kubeconfigPath %q; want Phase 0's outputs and the kept path %q", dep.Result.ConsoleURL, dep.Result.KubeconfigPath, kubeconfig)
	}
}

// A second run never starts beside one that is still in flight in this process.
func TestRetryPhase0_RefusedWhileARunOrAWatchIsInFlight(t *testing.T) {
	stub := &phase0Stub{err: errors.New("must not run")}
	h := NewWithPDM(silentLogger(), &fakePDM{})
	wireRerun(h, stub)
	open := makeTimedOutDeployment(t, h, "p0-open", true)
	open.done = make(chan struct{}) // the previous run still owns the channels
	watching := makeTimedOutDeployment(t, h, "p0-watching", true)
	watching.Status, watching.phase1Started = "phase1-watching", true
	for _, dep := range []*Deployment{open, watching} {
		if rr, _ := postRetryPhase(t, h, dep.ID, "tofu-apply"); rr.Code != http.StatusConflict {
			t.Errorf("%s: want 409, got %d %s", dep.ID, rr.Code, rr.Body.String())
		}
	}
	if reqs, _ := stub.calls(); len(reqs) != 0 {
		t.Fatalf("Phase 0 ran %d times beside a run in flight", len(reqs))
	}
}

// Claim 2: the request of a restored record keeps its deployment id. The id is
// json:"-", so the record holds it as its own ID only; without it a Phase-0
// retry keyed a new working directory by the FQDN, with empty state.
func TestFromRecord_RequestKeepsItsDeploymentID(t *testing.T) {
	full := provisioner.Request{SovereignFQDN: "hw307.omani.works", Provider: "huawei", DeploymentID: "a1b2c3d4e5f60718"}
	dep := fromRecord(store.Record{ID: full.DeploymentID, Status: "failed", Request: store.Redact(full)})
	if dep.Request.DeploymentID != full.DeploymentID {
		t.Fatalf("restored request: deployment id %q, want %q", dep.Request.DeploymentID, full.DeploymentID)
	}
}

// Claim 2: a Phase-0 retry after an engine restart runs with the deployment's
// id, marked as rebuilt from the record so Provision re-applies the working
// directory's variables file (provisioner.stageTfvars).
func TestRetryPhase0_AfterRestart_KeepsTheDeploymentID(t *testing.T) {
	stub := &phase0Stub{err: errors.New("tofu apply: stop here")}
	h := NewWithPDM(silentLogger(), &fakePDM{})
	wireRerun(h, stub)
	full := provisioner.Request{SovereignFQDN: "hw307.p0-restart.example", Provider: "huawei", DeploymentID: "p0-restart"}
	dep := fromRecord(store.Record{ID: "p0-restart", Status: "failed", Error: "tofu apply: quota", Request: store.Redact(full)})
	h.deployments.Store(dep.ID, dep)

	if rr, _ := postRetryPhase(t, h, dep.ID, "tofu-apply"); rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	awaitRun(t, dep)
	reqs, _ := stub.calls()
	if len(reqs) != 1 || reqs[0].DeploymentID != "p0-restart" || !reqs[0].FromRecord {
		t.Fatalf("Phase 0 after a restart: want one run with id p0-restart and FromRecord, got %+v", reqs)
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" || !strings.Contains(dep.Error, "stop here") {
		t.Fatalf("a failed Phase 0 ends the run failed: status %q, error %q", dep.Status, dep.Error)
	}
}

// Claim 4: a kubeconfig that arrives after Phase 0 failed starts no watch, so
// no watch turns the failure into 'ready'.
func TestPutKubeconfig_AfterPhase0Failed_KeepsTheFailure(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "failed")
	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)
	dep.mu.Lock()
	dep.Error = "tofu apply: Common.0021"
	dep.mu.Unlock()

	w := httptest.NewRecorder()
	h.PutKubeconfig(w, putReq(t, id, bearer, []byte(validKubeconfigYAML)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT: want 204, got %d %s", w.Code, w.Body.String())
	}
	h.phase1WatchWG.Wait()
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" || dep.Error != "tofu apply: Common.0021" {
		t.Fatalf("the Phase-0 failure was replaced: status %q, error %q", dep.Status, dep.Error)
	}
	if dep.Result.KubeconfigPath == "" || len(dep.Result.ComponentStates) != 0 {
		t.Fatalf("want the path stamped and no watch verdict, got path %q, states %v", dep.Result.KubeconfigPath, dep.Result.ComponentStates)
	}
}

// Claim 4: the kubeconfig arrives while Phase 0 still runs. No watch judges the
// run then (it could end ready before Phase 0 ends, or over its failure).
func TestPutKubeconfig_DuringPhase0_JudgesNothing(t *testing.T) {
	h, _, id, bearer := makePutFixture(t, "provisioning")
	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)
	w := httptest.NewRecorder()
	h.PutKubeconfig(w, putReq(t, id, bearer, []byte(validKubeconfigYAML)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT: want 204, got %d %s", w.Code, w.Body.String())
	}
	h.phase1WatchWG.Wait()
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result.KubeconfigPath == "" || dep.Result.Phase1Outcome != "" || dep.Status != "provisioning" {
		t.Fatalf("during Phase 0: want the path stamped and no verdict, got path %q, phase1Outcome %q, status %q", dep.Result.KubeconfigPath, dep.Result.Phase1Outcome, dep.Status)
	}
}

// Claim 4: Phase 0's success merges its outputs onto the record's result and
// keeps the path an early upload stamped; the run's own watch reads that path.
func TestPhase0Success_KeepsTheKubeconfigPathAnEarlyUploadStamped(t *testing.T) {
	h, kubeconfigs, id, _ := makePutFixture(t, "provisioning")
	stub := &phase0Stub{result: phase0Result(id)}
	wireRerun(h, stub)
	val, _ := h.deployments.Load(id)
	dep := val.(*Deployment)
	path := filepath.Join(kubeconfigs, id+".yaml")
	if err := os.WriteFile(path, []byte(validKubeconfigYAML), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	dep.mu.Lock()
	dep.Result.KubeconfigPath = path // what PutKubeconfig stamps while Phase 0 runs
	dep.mu.Unlock()

	h.runProvisioning(dep)
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result.KubeconfigPath != path || dep.Result.ConsoleURL == "" {
		t.Fatalf("Phase 0's result replaced the record's: kubeconfigPath %q (want %q), consoleURL %q", dep.Result.KubeconfigPath, path, dep.Result.ConsoleURL)
	}
	if dep.Status != "ready" || dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		t.Fatalf("want ready from the run's own watch, got status %q, outcome %q, error %q", dep.Status, dep.Result.Phase1Outcome, dep.Error)
	}
}

// Claim 4, after a restart: a failed record whose Phase 1 never finished failed
// in Phase 0, and the restore does not resume a watch over it.
func TestShouldResumePhase1_Phase0FailureStands(t *testing.T) {
	kubeconfigs := t.TempDir()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	h := NewWithStoreAndKubeconfigsDir(silentLogger(), &fakePDM{}, st, kubeconfigs)
	path := filepath.Join(kubeconfigs, "p0-failed.yaml")
	if err := os.WriteFile(path, []byte(validKubeconfigYAML), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	for _, status := range []string{"failed", "partial-failure"} {
		dep := &Deployment{ID: "p0-failed", Result: &provisioner.Result{KubeconfigPath: path}}
		if h.shouldResumePhase1(dep, store.Record{Status: status}) {
			t.Errorf("status %s: a watch would be resumed over the Phase-0 failure", status)
		}
	}
}

// A failed retry releases no pool name that an earlier run committed; a first
// run's failure still releases its reservation.
func TestPhase0Failure_ReleasesOnlyAReservationNoRunCommitted(t *testing.T) {
	pdmc := &fakePDM{}
	stub := &phase0Stub{err: errors.New("tofu apply: VPC.2003")}
	h := NewWithPDM(silentLogger(), pdmc)
	wireRerun(h, stub)
	dep := makeTimedOutDeployment(t, h, "p0-committed", true)
	dep.Result.ConsoleURL = "https://console.hw307.p0-committed.example" // an earlier Phase 0 reached its outputs
	dep.pdmReservationToken, dep.pdmPoolDomain, dep.pdmSubdomain = "tok", "omani.works", "hw307"
	if rr, _ := postRetryPhase(t, h, dep.ID, "tofu-apply"); rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rr.Code, rr.Body.String())
	}
	awaitRun(t, dep)
	if got := pdmc.snapshotReleases(); len(got) != 0 {
		t.Fatalf("a failed retry released the committed name: %+v", got)
	}

	first := &Deployment{ID: "p0-first", Status: "provisioning", eventsCh: make(chan provisioner.Event, 256), done: make(chan struct{}),
		pdmReservationToken: "tok2", pdmPoolDomain: "omani.works", pdmSubdomain: "hw308"}
	h.runProvisioning(first)
	if got := pdmc.snapshotReleases(); len(got) != 1 || got[0].sub != "hw308" {
		t.Fatalf("a first run's failure must release its reservation, got %+v", got)
	}
}

// Claim 5: the parent's Flux-CRD probe may not give up before first boot does.
// The waits are read from the template: everything first boot runs after the
// kubeconfig PUT until the Flux CRD exists — the gateway-api CRD loop and wait,
// the cilium rollout under rt(), the CoreDNS wait and the flux-install loop.
// Downloads are not counted, so this is the least first boot allows itself.
func TestFluxCRDProbeBudget_OutlastsFirstBoot(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers", "_shared", "cloudinit-control-plane.tftpl"))
	if err != nil {
		t.Fatalf("read the control-plane template: %v", err)
	}
	put := strings.Index(string(raw), "KUBECONFIG PUT-BACK")
	if put < 0 {
		t.Fatalf("the template has no kubeconfig PUT-back block")
	}
	after := string(raw)[put:]
	group := func(pattern string) string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(after)
		if m == nil {
			t.Fatalf("the template after the PUT does not match %q", pattern)
		}
		return m[1]
	}
	num := func(pattern string) int {
		n, err := strconv.Atoi(group(pattern))
		if err != nil {
			t.Fatalf("%q: %v", pattern, err)
		}
		return n
	}
	gwTries := len(strings.Fields(group(`for i in ([0-9 ]+); do sh /var/lib/catalyst/oci_fetch gateway-api`)))
	gwPause := num(`oci_fetch gateway-api[^\n]*sleep (\d+); done`)
	gwWait := num(`crd/tlsroutes\.gateway\.networking\.k8s\.io --timeout=(\d+)s`)
	rtTries := len(strings.Fields(group(`rt\(\)\{ for i in ([0-9 ]+);do`)))
	rtPause := num(`rt\(\)\{ for i in [0-9 ]+;do "\$@"&&return;sleep (\d+);done`)
	rollout := num(`rt kubectl -n kube-system rollout status ds/cilium --timeout=(\d+)s`)
	dnsTries := num(`for n in \$\(seq (\d+)\); do kubectl -n kube-system get deploy coredns`)
	dnsPause := num(`get deploy coredns >/dev/null 2>&1 && break; sleep (\d+); done`)
	fluxTries := num(`\[ "\$n" -gt (\d+) \] && \{ \$\{fail_mark_cmd\}flux-install-FAILED`)
	fluxStep := num(`sleep \$\(\(n\*(\d+)\)\)`)
	waits := (gwTries-1)*gwPause + gwWait + rtTries*(rollout+rtPause) + dnsTries*dnsPause + fluxStep*fluxTries*(fluxTries+1)/2
	firstBoot := time.Duration(waits) * time.Second
	if DefaultFluxCRDProbeBudget < firstBoot {
		t.Fatalf("the parent fails the install %s after the kubeconfig arrives (DefaultFluxCRDProbeBudget), but first boot's own waits after the PUT add up to %s", DefaultFluxCRDProbeBudget, firstBoot)
	}
}
