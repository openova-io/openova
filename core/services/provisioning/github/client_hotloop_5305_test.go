package github

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

// #5305 (hw282) — the funnel customer's PURCHASED app never deployed because the
// organization-controller was a CONTINUOUS writer on the per-Org
// `<slug>/catalyst-tenant` repo's `main` HEAD: an unconditional status write
// (Ready condition stamped now() every pass) re-enqueued the reconcile from its
// own output, hot-looping sub-second and re-firing its `vcluster/apps/`
// PutFile burst forever. Against an UNBOUNDED writer the funnel's finite
// compare-and-swap budget can never win — that is exactly what
// TestCommitFiles_GiteaTarget_PermanentlyAdvancingRefExhausts_5234 pins
// (advanceRemaining=-1 → exhausts). The #5305 fix removes the hot loop on the
// controller side, so the controller's writes collapse to a FINITE seed burst.
//
// This test pins the OTHER half of the contract at the funnel client level: a
// finite MULTI-COMMIT burst (a seed burst of several sequential controller
// commits, each landing between the funnel's probe and its push) CONVERGES
// within the widened attempt budget. It is the realistic post-#5305 shape —
// several rapid controller commits at Org-create, then quiet — and proves the
// funnel lands its purchased-app commit as soon as the controller stops.
func TestCommitFiles_GiteaTarget_ConvergesOnFiniteSeedBurst_5305(t *testing.T) {
	shrinkCommitRetryDelays(t)

	// Six sequential competing commits (a full org-controller seed burst:
	// namespace / HR / quota / limitrange / NP / index), then the branch goes
	// quiet — well within commitAttemptsMax (10).
	const burst = 6
	fake := &casFakeGitea{t: t, fileSHA: 1, advanceRemaining: burst}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := NewClientWithAPIURL("token", "hw282org", "catalyst-tenant", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "day-2: install wordpress", map[string]string{
		casTrackedFile: "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - app-wordpress.yaml\n",
	})
	if err != nil {
		t.Fatalf("CommitFiles must converge once the finite seed burst quiets (the #5305 post-fix shape), got: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	// One losing POST per competing commit, then one winning POST.
	if fake.postCount != burst+1 {
		t.Errorf("expected %d POSTs (%d CAS losses over the seed burst + 1 win), got %d", burst+1, burst, fake.postCount)
	}
	if fake.postCount > commitAttemptsMax {
		t.Errorf("a %d-commit seed burst exhausted the %d-attempt budget — the funnel would drop the purchased app (#5305)", burst, commitAttemptsMax)
	}
	if want := fmt.Sprintf("filesha-%d", fake.fileSHA); fake.lastAcceptedSHA != want {
		t.Errorf("winning batch carried sha %q, want the CURRENT head's %q — the retry did not rebuild against the advanced head", fake.lastAcceptedSHA, want)
	}
}
