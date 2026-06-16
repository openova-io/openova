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

	yamlv3 "gopkg.in/yaml.v3"

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

	// #3668 §5A — the FIRST console edit on a blueprint with no Gitea file
	// must SEED from the live in-cluster CR's FULL spec, not synthesise a
	// `version: 0.0.0` stub that drops spec.source / spec.manifests /
	// spec.sso / spec.placementSchema / spec.endpoints / shareable /
	// contextSchema (the §3A(b) lossy-stub violation). The seed is the same
	// full Blueprint the install path reads — fetched via the catalog client
	// (chainedCatalogClient resolves Gitea → in-cluster CR and returns the
	// entire object in .Raw). With a full seed the existing setIfAbsent merge
	// preserves every non-card field; the edit only overlays the card fields.
	if len(existing) == 0 {
		if seed := h.seedBlueprintYAMLFromLiveCR(ctx, bpName); len(seed) > 0 {
			existing = seed
		}
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

// seedBlueprintYAMLFromLiveCR resolves a blueprint's FULL definition via the
// catalog client and renders it as the Blueprint CR YAML to seed the
// catalog-sovereign Gitea file on the first console edit (#3668 §5A). The
// catalog client is the chainedCatalogClient — it resolves Gitea →
// in-cluster CR and returns the entire object in CatalogBlueprint.Raw, which
// is the same full spec (source / version / manifests / sso / placementSchema
// / endpoints / shareable / contextSchema) the install path consumes.
//
// Returns nil (the caller then falls back to mergeCatalogEditIntoBlueprintYAML
// synthesising a minimal CR) when the client is unwired, the blueprint cannot
// be resolved, or Raw is empty — seeding is best-effort: it upgrades the first
// edit from a lossy stub to a full CR when the source is available, and never
// fails the edit when it isn't.
//
// Server-managed + ownership metadata is stripped so the committed file is a
// clean, hand-authorable source of truth that a later `helm upgrade` will not
// fight over: status, resourceVersion, uid, generation, creationTimestamp,
// managedFields, and the Helm/Flux ownership labels+annotations
// (`app.kubernetes.io/managed-by: Helm`, `helm.toolkit.fluxcd.io/*`, the
// release-tracking annotations) — leaving apiVersion/kind/metadata.name + the
// full spec that the Blueprint projector and validateBlueprintYAML accept.
func (h *Handler) seedBlueprintYAMLFromLiveCR(ctx context.Context, bpName string) []byte {
	if h.catalogClient == nil {
		return nil
	}
	bp, err := h.catalogClient.Get(ctx, bpName, "")
	if err != nil || bp == nil || len(bp.Raw) == 0 {
		return nil
	}
	doc := deepCopyYAMLMap(bp.Raw)
	if doc == nil {
		return nil
	}

	// Canonical envelope — the projector requires apiVersion/kind; the
	// in-cluster CR carries them, but a Gitea-sourced Raw might not.
	setIfAbsent(doc, "apiVersion", "catalyst.openova.io/v1")
	setIfAbsent(doc, "kind", "Blueprint")

	// metadata: keep only name; drop server-managed + ownership fields so a
	// helm upgrade can't claim the committed file (DoD §9.4).
	if meta, ok := doc["metadata"].(map[string]interface{}); ok {
		name := asString(meta["name"])
		if strings.TrimSpace(name) == "" {
			name = bpName
		}
		cleanMeta := map[string]interface{}{"name": name}
		doc["metadata"] = cleanMeta
	} else {
		doc["metadata"] = map[string]interface{}{"name": bpName}
	}

	// status is server-derived — never part of the source file.
	delete(doc, "status")

	out, err := yamlv3.Marshal(doc)
	if err != nil {
		return nil
	}
	return out
}

// deepCopyYAMLMap returns a deep copy of a JSON/YAML-decoded map so mutating
// the seed never aliases the catalog client's cached Raw. Values are scalars,
// []interface{}, or map[string]interface{} (json.Unmarshal output), so a
// straightforward recursive copy is exact.
func deepCopyYAMLMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = deepCopyYAMLValue(v)
	}
	return out
}

func deepCopyYAMLValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return deepCopyYAMLMap(t)
	case []interface{}:
		cp := make([]interface{}, len(t))
		for i := range t {
			cp[i] = deepCopyYAMLValue(t[i])
		}
		return cp
	default:
		return t
	}
}
