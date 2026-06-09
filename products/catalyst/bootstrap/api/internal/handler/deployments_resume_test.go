package handler

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// TestShouldResumePhase1_ConventionalKubeconfigFallback is the #3153 guard:
// after a mothership roll the rehydrated record can lose Result.KubeconfigPath
// (json `omitempty`), but the kubeconfig FILE survives on the PVC. The Phase-1
// watch MUST still resume off the conventional path — otherwise a single roll
// blinds the operator to convergence for the rest of the prov.
func TestShouldResumePhase1_ConventionalKubeconfigFallback(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)), kubeconfigsDir: dir}
	rec := store.Record{Status: "phase1-watching"}

	// (1) Result == nil (path omitted entirely) but the kubeconfig file exists
	//     on the PVC → MUST resume, and must stamp the resolved path so the
	//     watch + StreamLogs can use it.
	id := "90a98a78f35846a3"
	want := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(want, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dep := &Deployment{ID: id}
	if !h.shouldResumePhase1(dep, rec) {
		t.Fatalf("#3153 regression: expected resume via conventional kubeconfig fallback, got false")
	}
	if dep.Result == nil || dep.Result.KubeconfigPath != want {
		t.Errorf("resume must stamp the resolved kubeconfig path; got %+v", dep.Result)
	}

	// (2) No kubeconfig file anywhere → must NOT resume.
	if h.shouldResumePhase1(&Deployment{ID: "deadbeefdeadbeef"}, rec) {
		t.Errorf("must NOT resume when no kubeconfig file exists")
	}

	// (3) Phase-0 in-flight → never resumable, even if a file happens to exist.
	p0 := "p0p0p0p0p0p0p0p0"
	if err := os.WriteFile(filepath.Join(dir, p0+".yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h.shouldResumePhase1(&Deployment{ID: p0}, store.Record{Status: "tofu-applying"}) {
		t.Errorf("must NOT resume a phase-0-in-flight deployment")
	}
}
