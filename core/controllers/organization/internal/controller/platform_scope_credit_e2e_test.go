package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// TestE2E_ClasslessCreditRender runs the controller's OWN credit computation
// (classlessPlatformCredit, livePlanQuotaHard) against a REAL apiserver and
// writes the plan-quota gitops.Render produces from it to
// $ORG_PLATFORM_SCOPE_CREDIT_OUT — the step the org-controller performs on
// every reconcile, for the real-apiserver preflight
// (tests/e2e/org-platform-scope/run.sh) to apply. Skipped unless
// $ORG_PLATFORM_SCOPE_KUBECONFIG is set. With $ORG_PLATFORM_SCOPE_IMPERSONATE
// set (system:serviceaccount:<ns>:<name>) the pod read runs as that identity,
// which is how the preflight proves the RBAC-missing path.
func TestE2E_ClasslessCreditRender(t *testing.T) {
	kc := os.Getenv("ORG_PLATFORM_SCOPE_KUBECONFIG")
	out := os.Getenv("ORG_PLATFORM_SCOPE_CREDIT_OUT")
	if kc == "" || out == "" {
		t.Skip("ORG_PLATFORM_SCOPE_KUBECONFIG / _CREDIT_OUT unset — preflight only")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	reader := client.Reader(admin)
	if who := os.Getenv("ORG_PLATFORM_SCOPE_IMPERSONATE"); who != "" {
		imp := *cfg
		imp.Impersonate.UserName = who
		c, err := client.New(&imp, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		reader = c
	}
	r := &Reconciler{Client: admin, PodReader: reader, Log: logr.Discard()}
	ctx := context.Background()
	slug := "acme"
	credit, known, why := r.classlessPlatformCredit(ctx, slug)
	var live map[string]string
	if !known {
		live = r.livePlanQuotaHard(ctx, slug)
	}
	t.Logf("credit known=%v %s %s live=%v", known, credit.String(), why, live)
	files, err := gitops.Render(gitops.Inputs{Slug: slug, DisplayName: "Acme", Tier: "org", PlanSlug: "m",
		SovereignFQDN: "x.example", HostCluster: "kind", VClusterChartVersion: "0.33.*",
		ClasslessPlatformCredit: credit, CreditUnknown: !known, LivePlanQuotaHard: live})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, files["vcluster/resourcequota.yaml"], 0o644); err != nil {
		t.Fatal(err)
	}
}
