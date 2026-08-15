// Tests for the #6339 per-node cloud-init capture.
//
// THE DEFECT THESE PIN. PutCloudInitLog wrote every upload to ONE key,
// `<id>-cloudinit.log`. That is correct for a single-CP Sovereign and
// destructive for a multi-region one: every control plane in every
// region pushes to the same `{id}` on a 30s loop for ~50 minutes, so the
// surviving capture is whichever node wrote last and the other region's
// log is gone. Measured across the 13 archived captures in
// docs/sessions/**/prov-diagnostics/ that carry a kubeconfig PUT-back
// line: each holds exactly ONE hostname — 7 region a, 6 region b, never
// both. That is why "the primary never PUT its kubeconfig" could not be
// root-caused on hw297/hw298 — the primary echoes its PUT-back outcome
// only into ITS OWN log, and the capture that survived was the
// secondary's.
//
// The fix keeps the shared latest-wins file byte-for-byte as it was
// (every existing reader is untouched) and ADDS `<id>-cloudinit-<node>.log`
// keyed on the `?node=` the cloud-init now sends. TestTwoRegionUploads…
// is the one that would have failed before the fix.
package handler

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// cloudInitLogFixture builds a Handler with a live deployment record and
// a real bearer, plus the kubeconfigs dir the captures land in.
func cloudInitLogFixture(t *testing.T) (h *Handler, dir, id, bearer string) {
	t.Helper()
	dir = t.TempDir()
	bearer, hash, err := newBearerToken()
	if err != nil {
		t.Fatalf("newBearerToken: %v", err)
	}
	id = "6339cafe6339cafe"
	h = &Handler{log: slog.Default(), kubeconfigsDir: dir}
	h.deployments.Store(id, &Deployment{
		ID:                   id,
		StartedAt:            time.Now(),
		Request:              provisioner.Request{SovereignFQDN: "hw299.omani.works"},
		kubeconfigBearerHash: hash,
	})
	return h, dir, id, bearer
}

// putCloudInitLog fires one upload, optionally carrying ?node=.
func putCloudInitLog(t *testing.T, h *Handler, id, bearer, node, body string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/v1/deployments/" + id + "/cloudinit-log"
	if node != "" {
		url += "?node=" + node
	}
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.PutCloudInitLog(rec, req)
	return rec
}

// TestTwoRegionUploads_BothNodeCapturesSurvive — THE regression test.
// Region a uploads (its log carries the primary PUT-back outcome), then
// region b uploads and, pre-fix, obliterated it. Post-fix both are on
// disk under their own keys and the primary's outcome is readable.
func TestTwoRegionUploads_BothNodeCapturesSurvive(t *testing.T) {
	h, dir, id, bearer := cloudInitLogFixture(t)

	const (
		nodeA = "catalyst-hw299-omani-works-6339cafe-me-east-215-a-cp1-aa1111"
		nodeB = "catalyst-hw299-omani-works-6339cafe-me-east-215-b-cp1-bb2222"
		logA  = "PRIMARY-KUBECONFIG-CAPTURE-FAILED region=me-east-215-a lastHTTP=000\n"
		logB  = "secondary kubeconfig PUT-back attempt 1 -> HTTP 201\n"
	)

	if rec := putCloudInitLog(t, h, id, bearer, nodeA, logA); rec.Code != http.StatusNoContent {
		t.Fatalf("region-a upload: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}
	// Region b's uploader runs LAST — the pre-fix clobber.
	if rec := putCloudInitLog(t, h, id, bearer, nodeB, logB); rec.Code != http.StatusNoContent {
		t.Fatalf("region-b upload: want 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	gotA, err := os.ReadFile(filepath.Join(dir, id+"-cloudinit-"+nodeA+".log"))
	if err != nil {
		t.Fatalf("region-a per-node capture must survive region-b's upload: %v", err)
	}
	if string(gotA) != logA {
		t.Fatalf("region-a capture body:\n want %q\n got  %q", logA, string(gotA))
	}
	gotB, err := os.ReadFile(filepath.Join(dir, id+"-cloudinit-"+nodeB+".log"))
	if err != nil {
		t.Fatalf("region-b per-node capture missing: %v", err)
	}
	if string(gotB) != logB {
		t.Fatalf("region-b capture body:\n want %q\n got  %q", logB, string(gotB))
	}

	// Back-compat: the shared key keeps its exact latest-wins semantics.
	shared, err := os.ReadFile(filepath.Join(dir, id+"-cloudinit.log"))
	if err != nil {
		t.Fatalf("shared latest-wins capture missing: %v", err)
	}
	if string(shared) != logB {
		t.Fatalf("shared key must stay latest-wins:\n want %q\n got  %q", logB, string(shared))
	}
}

// TestGetCloudInitLog_ByNode — the operator can address either region's
// capture, and the default GET is unchanged while advertising what else
// is held.
func TestGetCloudInitLog_ByNode(t *testing.T) {
	h, _, id, bearer := cloudInitLogFixture(t)
	const (
		nodeA = "catalyst-hw299-omani-works-6339cafe-me-east-215-a-cp1-aa1111"
		nodeB = "catalyst-hw299-omani-works-6339cafe-me-east-215-b-cp1-bb2222"
		logA  = "primary kubeconfig PUT-back attempt 1 -> HTTP 204\n"
		logB  = "secondary kubeconfig PUT-back attempt 1 -> HTTP 201\n"
	)
	putCloudInitLog(t, h, id, bearer, nodeA, logA)
	putCloudInitLog(t, h, id, bearer, nodeB, logB)

	get := func(q string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Get("/api/v1/deployments/{id}/cloudinit-log", h.GetCloudInitLog)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id+"/cloudinit-log"+q, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	recA := get("?node=" + nodeA)
	if recA.Code != http.StatusOK || recA.Body.String() != logA {
		t.Fatalf("?node=<region-a>: want 200 %q, got %d %q", logA, recA.Code, recA.Body.String())
	}
	recB := get("?node=" + nodeB)
	if recB.Code != http.StatusOK || recB.Body.String() != logB {
		t.Fatalf("?node=<region-b>: want 200 %q, got %d %q", logB, recB.Code, recB.Body.String())
	}

	// Default GET: unchanged (latest-wins body) + discovery header.
	recDefault := get("")
	if recDefault.Code != http.StatusOK || recDefault.Body.String() != logB {
		t.Fatalf("default GET: want 200 %q, got %d %q", logB, recDefault.Code, recDefault.Body.String())
	}
	hdr := recDefault.Header().Get("X-Catalyst-Cloudinit-Nodes")
	if !strings.Contains(hdr, nodeA) || !strings.Contains(hdr, nodeB) {
		t.Fatalf("X-Catalyst-Cloudinit-Nodes must advertise both captures, got %q", hdr)
	}

	// Unknown node → 404 naming what IS held, so the operator who asked
	// for the wrong region is told the right key instead of nothing.
	recMiss := get("?node=catalyst-hw299-omani-works-6339cafe-me-east-215-z-cp1-zz9999")
	if recMiss.Code != http.StatusNotFound {
		t.Fatalf("unknown node: want 404, got %d", recMiss.Code)
	}
	if !strings.Contains(recMiss.Body.String(), nodeA) {
		t.Fatalf("404 body must list available captures, got %q", recMiss.Body.String())
	}
}

// TestSanitiseNodeKey_NoTraversal — the node key is composed into a path
// on a shared PVC, so traversal and separator bytes must not survive.
func TestSanitiseNodeKey_NoTraversal(t *testing.T) {
	cases := map[string]string{
		"catalyst-hw299-a-cp1-aa1111": "catalyst-hw299-a-cp1-aa1111",
		"CATALYST-HW299-A":            "catalyst-hw299-a",
		"../../etc/shadow":            "etc-shadow",
		"/absolute/path":              "absolute-path",
		"..":                          "",
		"":                            "",
		"   ":                         "",
		"node..name":                  "node-name",
		"node\x00name":                "node-name",
		strings.Repeat("a", 200):      strings.Repeat("a", maxNodeKeyLen),
	}
	for in, want := range cases {
		if got := sanitiseNodeKey(in); got != want {
			t.Errorf("sanitiseNodeKey(%q) = %q, want %q", in, got, want)
		}
		if got := sanitiseNodeKey(in); strings.ContainsAny(got, `/\.`) {
			t.Errorf("sanitiseNodeKey(%q) = %q — leaks a path separator", in, got)
		}
	}
}

// TestPutCloudInitLog_NoNodeParam_UnchangedBehaviour — an older cloud-init
// (or the Hetzner render, which does not push per-node) must keep working
// and must not create a stray per-node file.
func TestPutCloudInitLog_NoNodeParam_UnchangedBehaviour(t *testing.T) {
	h, dir, id, bearer := cloudInitLogFixture(t)
	const body = "cloud-init v24 running modules:final\n"
	if rec := putCloudInitLog(t, h, id, bearer, "", body); rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(dir, id+"-cloudinit.log"))
	if err != nil || string(got) != body {
		t.Fatalf("shared capture: err=%v body=%q", err, string(got))
	}
	if keys := cloudInitNodeKeys(dir, id); len(keys) != 0 {
		t.Fatalf("no ?node= must create no per-node capture, got %v", keys)
	}
}
