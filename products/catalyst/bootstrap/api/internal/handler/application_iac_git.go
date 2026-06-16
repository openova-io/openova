// Package handler — application_iac_git.go: #3687 (fold #3694) — the
// running-Application spine's authoring home becomes Gitea (IaC), not an
// etcd-only object the UI mutated directly.
//
// Founder law (#3687 §2): "IaC lives in Gitea; the Application CR's
// authoring home is Git" (ARCHITECTURE §5.1 rule 10). "Anything must be
// working perfectly even if you shut down Catalyst" (PRINCIPLES §3). An
// Application CR born in etcd that the controller later mirrors to Git was
// never IaC.
//
// BEFORE this file the create/update seams ended in a dynamic-client
// `.Create`/`.Update` (endpoint_handler.go HandleCreateInstance,
// applications_update.go HandleApplicationUpdate). The CR lived in etcd;
// Gitea held nothing; a hand `git push` of an Application change had
// nowhere to land. `kubectl get applications -A` could only ever reflect
// what the API wrote directly — never a Git-resident estate.
//
// AFTER: every Application create/update/Context-append ALSO COMMITS the
// desired-state CR into the per-Org `iac` repo (ADR-0009, the same repo +
// org the org-controller bootstraps and the endpoint PR pipeline writes)
// at `applications/<name>.yaml`. Flux on the host cluster reconciles that
// path; a hand `git push` to the same file round-trips. The Application
// CR's source of truth is therefore Git.
//
// Transitional, not big-bang (#3687 §8 — "a CONSTRAINT-driven refactor
// enforced incrementally on each write seam as it is touched, not a
// big-bang rewrite"): the existing etcd write is left in place as a warm
// projection so the API response shape stays byte-identical and the
// app-list informers keep serving while the Flux loop catches up. What
// changes is that the bytes now ALSO exist in Git — the gap #3687 §3.4
// measured ("Gitea IaC holds nothing") is closed for every touched seam.
//
// Graceful degradation (the catalog_edit_git.go precedent): when the
// Gitea client is unwired (chroot Sovereign pre-cutover / CI without a
// Gitea backend) the commit is a best-effort no-op — the etcd write
// already succeeded, so the create/update still returns its real result.
// The Application is never failed by a missing local Gitea; it merely
// isn't yet Git-resident until Gitea is reachable.

package handler

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
)

// applicationIaCBranch is the branch the per-Org `iac` Application CRs
// live on. Same default branch the endpoint PR pipeline targets
// (giteapr.DefaultBranch).
const applicationIaCBranch = giteapr.DefaultBranch // "main"

// applicationManifestPath is the canonical in-repo path for an
// Application CR's IaC. Siblings the endpoint manifests the PR pipeline
// writes under `apps/<app>/endpoints/<name>.yaml` (giteapr
// .EndpointManifestPath); the Application's own desired state lives at
// `applications/<name>.yaml` so Flux's per-Org Kustomization fans it out.
func applicationManifestPath(name string) string {
	return fmt.Sprintf("applications/%s.yaml", name)
}

// applicationIaCCommitAuthor stamps the commit so a sovereign-admin
// scanning the `iac` repo history can tell a console-originated
// Application write from a hand-authored git push. Per Inviolable
// Principle #4 these are overridable via the canonical
// CATALYST_GITOPS_COMMITTER_* env (shared with the tenant-gitops + catalog
// writers); default to the catalyst-api identity.
func applicationIaCCommitAuthor() gitea.PutFileOpts {
	return gitea.PutFileOpts{
		AuthorName:  envOr("CATALYST_GITOPS_COMMITTER_NAME", "catalyst-api"),
		AuthorEmail: envOr("CATALYST_GITOPS_COMMITTER_EMAIL", "ops@openova.io"),
	}
}

// applicationCRToYAML renders the desired-state Application CR to YAML for
// Git. It strips server-populated / runtime fields (status, managedFields,
// resourceVersion, uid, creationTimestamp, generation) so the committed
// manifest is clean declarative IaC — what an operator would hand-author —
// not an etcd snapshot. sigs.k8s.io/yaml honours the unstructured JSON
// tags so the output is canonical Kubernetes YAML.
func applicationCRToYAML(obj *unstructured.Unstructured) ([]byte, error) {
	clean := obj.DeepCopy()
	unstructured.RemoveNestedField(clean.Object, "status")
	unstructured.RemoveNestedField(clean.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(clean.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(clean.Object, "metadata", "uid")
	unstructured.RemoveNestedField(clean.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(clean.Object, "metadata", "generation")
	unstructured.RemoveNestedField(clean.Object, "metadata", "selfLink")
	return sigyaml.Marshal(clean.Object)
}

// commitApplicationCRToGit writes the Application CR's desired state into
// the per-Org `iac` repo at `applications/<name>.yaml` (#3687 fold #3694).
// Best-effort: a nil Gitea client (chroot/CI) or a byte-equal short-circuit
// returns committed=false, err=nil; only a real write failure returns an
// error, which the caller logs without failing the create/update (the
// etcd projection already succeeded).
//
// Idempotent: EnsureOrg/EnsureRepo are find-or-create, and PutFile is a
// byte-equal short-circuit (committed=false when the bytes are unchanged),
// so a no-op re-commit of a steady-state CR makes zero Git writes.
func (h *Handler) commitApplicationCRToGit(ctx context.Context, org string, obj *unstructured.Unstructured) (committed bool, err error) {
	if h.giteaClient == nil {
		return false, nil
	}
	org = strings.TrimSpace(org)
	name := strings.TrimSpace(obj.GetName())
	if org == "" || name == "" {
		return false, fmt.Errorf("commitApplicationCRToGit: empty org or name")
	}

	manifest, mErr := applicationCRToYAML(obj)
	if mErr != nil {
		return false, fmt.Errorf("marshal Application CR: %w", mErr)
	}

	// Ensure the per-Org IaC repo exists (idempotent — the same
	// EnsureOrg/EnsureRepo the endpoint PR pipeline + org-controller use).
	if _, eErr := h.giteaClient.EnsureOrg(ctx, org,
		org, fmt.Sprintf("Catalyst Organization %s", org), "private"); eErr != nil {
		return false, fmt.Errorf("ensure gitea org %q: %w", org, eErr)
	}
	if _, eErr := h.giteaClient.EnsureRepo(ctx, org, giteapr.IaCRepoName,
		"per-Org IaC — Application CRs + endpoint manifests reconciled by Flux", true); eErr != nil {
		return false, fmt.Errorf("ensure gitea repo %s/%s: %w", org, giteapr.IaCRepoName, eErr)
	}

	path := applicationManifestPath(name)
	msg := fmt.Sprintf("catalyst-api: commit Application %s/%s (console create/update)", org, name)
	_, committed, pErr := h.giteaClient.PutFile(ctx, org, giteapr.IaCRepoName,
		applicationIaCBranch, path, manifest, msg, applicationIaCCommitAuthor())
	if pErr != nil {
		return false, fmt.Errorf("put %s: %w", path, pErr)
	}
	return committed, nil
}
