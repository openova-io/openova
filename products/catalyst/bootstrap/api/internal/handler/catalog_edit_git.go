// Package handler — catalog_edit_git.go: #3648 (train/hw150) — the
// catalog edit becomes a git commit to the LOCAL catalog repo.
//
// Founder review #8 (2026-06-16): "IaC is always the single source of
// truth. If an advanced user changes something from the code, our UI
// gets synced to it because it always treats there as the single source
// of truth. … For the ease of user, we allow catalog to provide an edit
// feature so updating the IaC from the catalog UI as well."
//
// BEFORE this file the catalog edit (UI saveCatalogEdit → PUT/POST
// /api/v1/sme/commerce/apps → proxyCommerce → the SME commerce catalog's
// /catalog/admin/apps) persisted to a SEPARATE commerce-overlay store
// (core/services/catalog store.App). That store is NOT IaC: an edit
// never reached the catalog git, and an out-of-band git edit never
// reached the UI. The two diverged.
//
// AFTER: every `apps` edit additionally COMMITS the edited card fields
// (name, tagline, supported_topologies, icon_light, icon_dark) into the
// per-Sovereign **catalog-sovereign** Gitea Org as a Blueprint CR
// (`catalog-sovereign/<bp-name>/blueprint.yaml`) — the exact same repo +
// shape the /blueprints/curate handler already writes (blueprints.go),
// and the same source the cutover makes authoritative (the customer-sync
// catalog mirror). The catalog projector reads that source at a HIGHER
// priority than the public seed (resolver order PRIVATE > SOVEREIGN >
// PUBLIC, per docs/catalog-seed/_README.txt + ADR-0001 §4.3), so a
// committed edit wins on the next catalog read.
//
// git is therefore the SOURCE. The commerce store remains a pure cache /
// projection (the existing proxyCommerce write is left intact so the API
// response shape is byte-identical and the store stays warm), but the
// READ overlay (fetchCatalogEdits) now prefers the git source so an
// out-of-band edit to `catalog-sovereign/<bp>/blueprint.yaml` round-trips
// to the UI exactly like a UI edit does.
//
// Graceful degradation: when the Gitea client is unwired (chroot
// Sovereign pre-cutover / CI without a Gitea backend) the git write is a
// best-effort no-op — the store write already succeeded, so the API call
// still returns the catalog service's real result. The catalog edit is
// never failed by a missing local catalog git; it merely isn't yet IaC
// until Gitea is reachable.

package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// catalogEditGitBranch is the branch the catalog-sovereign Blueprint CRs
// live on. Same default branch blueprints.go's publish/curate use.
const catalogEditGitBranch = blueprintBranch // "main"

// catalogEditCommitAuthor / Email stamp the commit so a sovereign-admin
// scanning the catalog-sovereign repo history can tell a UI-originated
// catalog edit from a hand-authored git push. Per Inviolable Principle #4
// these are overridable via the canonical CATALYST_GITOPS_COMMITTER_*
// env (shared with the tenant-gitops writer); default to the catalyst-api
// system identity.
func catalogEditCommitAuthor() gitea.PutFileOpts {
	return gitea.PutFileOpts{
		AuthorName:  envOr("CATALYST_GITOPS_COMMITTER_NAME", "catalyst-api"),
		AuthorEmail: envOr("CATALYST_GITOPS_COMMITTER_EMAIL", "ops@openova.io"),
	}
}

// catalogEditBlueprintName maps a bare commerce slug (`grafana`) to the
// canonical `bp-`-prefixed Blueprint repo name (`bp-grafana`) the
// catalog-sovereign Org keys on. Mirrors the normalizeCatalogKey ↔
// `bp-` convention used by the read overlay.
func catalogEditBlueprintName(slug string) string {
	s := strings.TrimSpace(strings.ToLower(slug))
	s = strings.TrimPrefix(s, "bp-")
	if s == "" {
		return ""
	}
	return "bp-" + s
}

// writeCatalogEditToGit commits one catalog entry's edited card fields
// into catalog-sovereign/<bp-name>/blueprint.yaml.
//
// It is a READ-MODIFY-WRITE: when a Blueprint CR already exists for this
// entry (curated earlier, or written by a prior edit) the edit is MERGED
// onto it so non-card fields (spec.source, spec.version, spec.manifests…)
// survive; when none exists a minimal CR carrying just the edited card +
// supported topologies is created. Either way the result is a single git
// commit to the local catalog repo — the IaC source of truth.
//
// Returns (committed, err): committed=false + err=nil when the client is
// unwired (best-effort no-op) OR the bytes were byte-equal (PutFile
// short-circuit). A non-nil err means the git write genuinely failed and
// the caller decides whether to surface it (we log + continue so the
// store-backed API response is unaffected — git is additive here).
func (h *Handler) writeCatalogEditToGit(ctx context.Context, edit catalogEdit) (bool, error) {
	if h.giteaClient == nil {
		return false, nil // unwired — best-effort no-op (pre-cutover / CI)
	}
	bpName := catalogEditBlueprintName(edit.Slug)
	if bpName == "" {
		return false, fmt.Errorf("catalog-edit-git: empty slug")
	}
	if !edit.hasOverlay() {
		// Nothing to persist as IaC (the proxyCommerce store write may
		// still carry non-card columns; git only owns the card overlay).
		return false, nil
	}

	// Ensure the catalog-sovereign Org + per-Blueprint repo exist
	// (idempotent — same EnsureOrg/EnsureRepo blueprints.go's curate uses).
	if _, err := h.giteaClient.EnsureOrg(ctx, catalogSovereignOrg,
		"Sovereign-curated catalog",
		"Curated Blueprints visible to every Org", "public"); err != nil {
		return false, fmt.Errorf("ensure org %q: %w", catalogSovereignOrg, err)
	}
	if _, err := h.giteaClient.EnsureRepo(ctx, catalogSovereignOrg, bpName,
		"Sovereign-curated Blueprint", false); err != nil {
		return false, fmt.Errorf("ensure repo %s/%s: %w", catalogSovereignOrg, bpName, err)
	}

	// READ the existing CR (if any) so the edit is a merge, not a clobber.
	var existing []byte
	if f, err := h.giteaClient.GetFile(ctx, catalogSovereignOrg, bpName,
		catalogEditGitBranch, catalogEditBlueprintPath); err == nil {
		if b, derr := f.Decoded(); derr == nil {
			existing = b
		}
	} else if !gitea.IsNotFound(err) {
		// A real transport error reading the source (not a plain
		// missing-file/repo) — surface it rather than silently overwriting
		// with a fresh minimal CR.
		return false, fmt.Errorf("get %s/%s/%s: %w", catalogSovereignOrg, bpName, catalogEditBlueprintPath, err)
	}

	merged, err := mergeCatalogEditIntoBlueprintYAML(existing, bpName, edit)
	if err != nil {
		return false, fmt.Errorf("merge blueprint.yaml: %w", err)
	}

	msg := fmt.Sprintf("catalog: edit %s card via catalyst-api (#3648)", bpName)
	_, committed, err := h.giteaClient.PutFile(ctx, catalogSovereignOrg, bpName,
		catalogEditGitBranch, catalogEditBlueprintPath, merged, msg, catalogEditCommitAuthor())
	if err != nil {
		return false, fmt.Errorf("put %s/%s/%s: %w", catalogSovereignOrg, bpName, catalogEditBlueprintPath, err)
	}
	return committed, nil
}

// fetchCatalogEditsFromGit reads every Blueprint CR in the
// catalog-sovereign Org and indexes the card-overlay fields by normalized
// slug — the GIT SOURCE of catalog edits. Returns an empty (non-nil) map
// on any failure (unwired client, missing Org, unreadable file) so the
// overlay degrades to the commerce-store projection / the seed.
//
// This is the read counterpart of writeCatalogEditToGit: an out-of-band
// `git push` to catalog-sovereign/<bp>/blueprint.yaml is picked up here
// identically to a UI edit, so the IaC round-trips to the UI.
func (h *Handler) fetchCatalogEditsFromGit(ctx context.Context) map[string]catalogEdit {
	edits := map[string]catalogEdit{}
	if h.giteaClient == nil {
		return edits
	}
	repos, err := h.giteaClient.ListOrgRepos(ctx, catalogSovereignOrg)
	if err != nil {
		return edits
	}
	for _, repo := range repos {
		name := strings.TrimSpace(repo.Name)
		if !strings.HasPrefix(name, "bp-") {
			continue
		}
		f, ferr := h.giteaClient.GetFile(ctx, catalogSovereignOrg, name,
			catalogEditGitBranch, catalogEditBlueprintPath)
		if ferr != nil {
			continue
		}
		raw, derr := f.Decoded()
		if derr != nil || len(raw) == 0 {
			continue
		}
		edit, ok := catalogEditFromBlueprintYAML(name, raw)
		if !ok || !edit.hasOverlay() {
			continue
		}
		edits[normalizeCatalogKey(name)] = edit
	}
	return edits
}

// catalogEditBlueprintPath is the canonical path of the Blueprint CR
// inside its catalog-sovereign per-Blueprint repo. Matches the path
// blueprints.go's curate writes ("blueprint.yaml").
const catalogEditBlueprintPath = "blueprint.yaml"
