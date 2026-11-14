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
// /api/v1/org/commerce/apps → proxyCommerce → the Organization commerce catalog's
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

// ── #3668 catalog-sovereign Flux reconcile aggregator ──────────────────
//
// The merged #3702 edit (and the curate endpoint) writes each Blueprint CR
// to a PER-BLUEPRINT repo `catalog-sovereign/<bp>/blueprint.yaml`. That is
// the read source the UI overlay round-trips. But a Flux GitRepository
// tracks ONE repo + branch — it cannot reconcile a whole Gitea Org of
// per-Blueprint repos. So #3668 ALSO mirrors the SAME ownership-stripped
// `merged` bytes into a single AGGREGATOR tree on the already-bootstrapped
// local Gitea repo `openova/openova`, on a dedicated `catalog-sovereign`
// branch:
//
//	openova/openova @ catalog-sovereign
//	└── clusters/<sovereignFQDN>/catalog-sovereign/<bp>.yaml   (full CR)
//
// The chart's openova-catalog-sovereign GitRepository + catalog-sovereign
// Kustomization (templates/catalog-sovereign-flux/) reconcile that one tree
// into LIVE Blueprint CRs — so a console edit reaches the in-cluster CR and
// SURVIVES `helm upgrade` (the CR becomes Flux/Git-owned, not Helm-owned;
// see templates/catalog-seed/blueprints.yaml's helm.sh/resource-policy:keep
// + the Kustomization's force:true SSA adoption). This is the load-bearing
// half PR #3702 explicitly deferred.
//
// The aggregator repo + branch reuse the SAME local Gitea repo the Organization
// tenant gitops chain bootstraps (`openova/openova`, seeded pre-cutover by
// the chart's repo-bootstrap hook + clonable by Flux via catalyst-gitea-
// token basic-auth, #3454). A DEDICATED branch is immune to the cutover-
// gitea-mirror Job's `git push --mirror --force` of `main` (same isolation
// rationale as the Organization `org-tenants` branch, TBD-C18e).
const (
	// catalogSovereignAggOrg/Repo — the local Gitea repo holding the Flux-
	// reconciled aggregator tree. Reuses the Organization gitops repo coordinates so
	// the chart's repo-bootstrap + Flux clone-auth are already wired.
	catalogSovereignAggOrg  = "openova"
	catalogSovereignAggRepo = "openova"
	// catalogSovereignAggBranch — dedicated branch the chart's repo-
	// bootstrap hook ensures + the openova-catalog-sovereign GitRepository
	// tracks. MUST match values.catalogSovereign.{gitRepository,repoBootstrap}
	// .branch in products/catalyst/chart/values.yaml.
	catalogSovereignAggBranch = "catalog-sovereign"
)

// catalogSovereignAggPath returns the aggregator tree path for one
// Blueprint, matching the Flux Kustomization's
// `path: ./clusters/<sovereignFQDN>/catalog-sovereign`. The Kustomization
// directory-sweeps every *.yaml beneath it into Blueprint CRs (no
// kustomization.yaml index needed — Flux auto-generates one over the
// directory), so a single flat PUT per Blueprint is all that is required.
func catalogSovereignAggPath(sovereignFQDN, bpName string) string {
	return fmt.Sprintf("clusters/%s/catalog-sovereign/%s.yaml", sovereignFQDN, bpName)
}

// writeCatalogSovereignAggregator mirrors one Blueprint's full, canonical
// CR bytes into the Flux-reconciled aggregator tree (#3668). It is
// best-effort + additive, exactly like the per-Blueprint write: a failure is
// returned for the caller to log or surface (the UI round-trip already
// succeeded via the per-Blueprint repo; the aggregator is what makes the CR
// reconcile — the card-edit path now SURFACES a failure of this leg per
// #5113 Facet D, mirroring the #5018 honesty fix on the full-CR leg).
//
// No-ops (returns committed=false, err=nil) when:
//   - the Gitea client is unwired (chroot pre-cutover / CI), OR
//   - SOVEREIGN_FQDN is empty — i.e. the Catalyst-Zero mothership, which has
//     no local Gitea catalog-sovereign Flux reconcile (it pushes to
//     github.com and renders no openova-catalog-sovereign GitRepository).
//
// The branch is ensured by the chart's catalog-sovereign repo-bootstrap
// hook pre-cutover; PutFile creates the file on first write. The repo
// (openova/openova) is auto-init'd by the Organization repo-bootstrap hook, so the
// branch always has a base commit to write onto.
func (h *Handler) writeCatalogSovereignAggregator(ctx context.Context, bpName string, merged []byte) (bool, error) {
	// Same no-op contract as the Bytes trunk, checked BEFORE the canonical
	// render so an unwired client / mothership never turns into an error.
	if len(merged) == 0 || h.giteaClient == nil ||
		strings.TrimSpace(envOr("SOVEREIGN_FQDN", "")) == "" {
		return false, nil
	}
	// #5113 Facets A+B — the payload goes through the SAME canonical
	// serializer as the full-CR IaC leg (stampBlueprintCRForFluxAggregator):
	// metadata.name stamped to the canonical bp-<slug> live-CR identity and
	// spec.version/spec.source KEPT.
	//
	// This supersedes the former #4415 delivery-field strip: spec.version is
	// CRD-required (chart/crds/blueprint.yaml `required: [version, card]`),
	// so a version-less aggregator file is REJECTED by kustomize-controller's
	// server-side dry-run whenever the named CR does not already exist (bare-
	// name mismatch, post-prune, curate of a non-seed blueprint) — wedging the
	// ENTIRE catalog-sovereign Kustomization (hw255 walk, gitea commits
	// d030078f/552a556c: `Blueprint "alloy" is invalid: spec.version: Required
	// value`). The #4415 zero-touch seed-bump property is traded away: a
	// catalog-seed version bump on a card-edited blueprint is now pinned until
	// the next card edit refreshes the file (noted on #5113).
	stamped := stampBlueprintCRForFluxAggregator(merged, bpName)
	if len(stamped) == 0 {
		// An unparsable document must never land in the Flux tree — that is
		// the same Kustomization-wide wedge class. Surface it instead.
		return false, fmt.Errorf("render Flux aggregator payload for %s: document did not parse", bpName)
	}
	return h.writeCatalogSovereignAggregatorBytes(ctx, bpName, stamped)
}

// writeCatalogSovereignAggregatorBytes commits the SUPPLIED final bytes to the
// Flux-reconciled aggregator tree — the shared trunk of the card-edit path
// (which pre-strips the #4415 chart-delivery fields) and the full-CR IaC edit
// path (#4896/#5018 — which commits the admin's whole document verbatim, name
// stamped to the live CR identity). Same no-op contract as the wrapper above:
// (false, nil) when the Gitea client is unwired or SOVEREIGN_FQDN is empty
// (Catalyst-Zero mothership — no local catalog Flux reconcile exists).
func (h *Handler) writeCatalogSovereignAggregatorBytes(ctx context.Context, bpName string, fluxBytes []byte) (bool, error) {
	if h.giteaClient == nil {
		return false, nil // unwired — best-effort no-op (pre-cutover / CI)
	}
	sovereignFQDN := strings.TrimSpace(envOr("SOVEREIGN_FQDN", ""))
	if sovereignFQDN == "" {
		return false, nil // Catalyst-Zero mothership — no local catalog Flux
	}
	if strings.TrimSpace(bpName) == "" || len(fluxBytes) == 0 {
		return false, nil
	}
	path := catalogSovereignAggPath(sovereignFQDN, bpName)
	msg := fmt.Sprintf("catalog: reconcile %s into Blueprint CR via Flux (#3668)", bpName)
	_, committed, err := h.giteaClient.PutFile(ctx, catalogSovereignAggOrg, catalogSovereignAggRepo,
		catalogSovereignAggBranch, path, fluxBytes, msg, catalogEditCommitAuthor())
	if err != nil {
		return false, fmt.Errorf("put %s/%s@%s/%s: %w",
			catalogSovereignAggOrg, catalogSovereignAggRepo, catalogSovereignAggBranch, path, err)
	}
	return committed, nil
}

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

	// #5113 Facets B+C — canonicalize BEFORE committing, through the SAME
	// serializer the full-CR IaC leg (#5041) uses: metadata.name stamped to
	// the canonical bp-<slug> live-CR identity (a bare `name: wordpress` from
	// a bare-named seed / legacy file made Flux swap its inventory entry and
	// PRUNE the live bp-wordpress CR on hw255), and cluster-populated
	// metadata scrubbed (uid/resourceVersion/… leaked via legacy Gitea files
	// survived the read-modify-write and produced the stale-uid Conflict
	// wedge, gitea commit 7516c865). Both the per-Blueprint read source and
	// the Flux aggregator get the SAME canonical bytes.
	canonical := stampBlueprintCRForFluxAggregator(merged, bpName)
	if len(canonical) == 0 {
		// Unreachable in practice — merged was just produced by yaml.Marshal —
		// but never commit a document the canonical serializer cannot parse.
		return false, fmt.Errorf("canonicalize blueprint.yaml for %s: document did not re-parse", bpName)
	}

	msg := fmt.Sprintf("catalog: edit %s card via catalyst-api (#3648)", bpName)
	_, committed, err := h.giteaClient.PutFile(ctx, catalogSovereignOrg, bpName,
		catalogEditGitBranch, catalogEditBlueprintPath, canonical, msg, catalogEditCommitAuthor())
	if err != nil {
		return false, fmt.Errorf("put %s/%s/%s: %w", catalogSovereignOrg, bpName, catalogEditBlueprintPath, err)
	}

	// #3668 — ALSO mirror the SAME canonical CR bytes into the Flux-
	// reconciled aggregator tree so the edit reaches the LIVE in-cluster
	// Blueprint CR (the per-Blueprint write above is only the UI read
	// source). #5113 Facet D — a failure of this leg is SURFACED, not
	// swallowed: the aggregator is what actually applies, so reporting
	// committed=true while it failed is the silent store/IaC split-brain the
	// hw255 walk caught (same class the #5018 fix closed on the full-CR leg).
	// The genuine no-op cases (unwired client / mothership) still return
	// (false, nil) and do not fail the edit.
	if _, aggErr := h.writeCatalogSovereignAggregatorBytes(ctx, bpName, canonical); aggErr != nil {
		return false, fmt.Errorf("IaC source committed to %s/%s but the Flux reconcile leg failed (the edit will not reach the live Blueprint CR): %w",
			catalogSovereignOrg, bpName, aggErr)
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
// Server-side metadata is stripped so the committed file is a clean,
// hand-authorable source of truth: status, resourceVersion, uid, generation,
// creationTimestamp, managedFields, last-applied — while the Helm ownership
// labels+annotations are PRESERVED (#5113 Facet E; see
// stripBlueprintCRMapForGit) — leaving apiVersion/kind/metadata (name,
// labels, annotations) + the full spec that the Blueprint projector and
// validateBlueprintYAML accept.
func (h *Handler) seedBlueprintYAMLFromLiveCR(ctx context.Context, bpName string) []byte {
	if h.catalogClient == nil {
		return nil
	}
	bp, err := h.catalogClient.Get(ctx, bpName, "")
	if err != nil || bp == nil || len(bp.Raw) == 0 {
		return nil
	}
	return stripBlueprintCRMapForGit(deepCopyYAMLMap(bp.Raw), bpName)
}

// blueprintCRServerSideMetadataFields are the cluster-populated metadata
// fields that must NEVER land in a committed Blueprint source file: they are
// minted by the API server per live object, so pinning them in git either
// wedges the next apply (a pruned-then-recreated CR makes the pinned uid a
// `dry-run failed (Conflict): uid mismatch` — hw255 walk, #5113 Facet C) or
// is plain noise (managedFields / generation / creationTimestamp / …).
var blueprintCRServerSideMetadataFields = []string{
	"uid", "resourceVersion", "generation", "creationTimestamp",
	"managedFields", "selfLink", "finalizers", "ownerReferences",
	// Blueprint is a CLUSTER-scoped CRD (chart/crds/blueprint.yaml
	// scope: Cluster) — a namespace on the committed file is invalid.
	"namespace",
}

// stripBlueprintCRMapForGit renders a decoded Blueprint CR map as the clean
// YAML source committed to Gitea (the catalog-sovereign per-Blueprint repo
// AND the Flux aggregator tree). It keeps apiVersion/kind + the full spec +
// the user/ownership metadata (name, labels, annotations), and DROPS ONLY
// the server-side fields: status, plus the cluster-populated metadata listed
// in blueprintCRServerSideMetadataFields and the apply-bookkeeping
// `kubectl.kubernetes.io/last-applied-configuration` annotation.
//
// #5113 Facet E (supersedes the former strip-all-metadata behavior): the
// Helm ownership labels+annotations (`app.kubernetes.io/managed-by: Helm`,
// `meta.helm.sh/*`) and the catalog-seed/alias labels are PRESERVED. When
// Flux prunes-and-recreates a CR from a file that lost them, the recreated
// CR carries no Helm ownership and the next `helm upgrade` of catalyst-
// platform hits an ownership conflict with NO console repair path (hw255
// repair needed direct gitea commits 956574c1/ba06d510). With them in the
// file, a Flux-materialized CR stays helm-adoptable. Returns nil on a
// nil/un-marshalable map.
func stripBlueprintCRMapForGit(doc map[string]interface{}, bpName string) []byte {
	if doc == nil {
		return nil
	}

	// Canonical envelope — the projector requires apiVersion/kind; an
	// in-cluster CR carries them, but a Gitea-sourced Raw might not.
	setIfAbsent(doc, "apiVersion", "catalyst.openova.io/v1")
	setIfAbsent(doc, "kind", "Blueprint")

	// metadata: keep name + labels + annotations; drop server-side fields.
	meta, ok := doc["metadata"].(map[string]interface{})
	if !ok {
		meta = map[string]interface{}{}
	}
	if strings.TrimSpace(asString(meta["name"])) == "" {
		meta["name"] = bpName
	}
	for _, k := range blueprintCRServerSideMetadataFields {
		delete(meta, k)
	}
	if ann, ok := meta["annotations"].(map[string]interface{}); ok {
		delete(ann, "kubectl.kubernetes.io/last-applied-configuration")
		if len(ann) == 0 {
			delete(meta, "annotations")
		}
	}
	if lbl, ok := meta["labels"].(map[string]interface{}); ok && len(lbl) == 0 {
		delete(meta, "labels")
	}
	doc["metadata"] = meta

	// status is server-derived — never part of the source file.
	delete(doc, "status")

	out, err := yamlv3.Marshal(doc)
	if err != nil {
		return nil
	}
	return out
}

// stripBlueprintCRBytesForGit is the bytes-in/bytes-out form used by the
// curate path (blueprints.go), where the source `blueprint.yaml` is read
// from `<sourceOrg>/shared-blueprints` and may carry ownership labels. It
// decodes, applies stripBlueprintCRMapForGit, and returns the cleaned
// bytes; on any decode failure it returns the input unchanged (best-effort
// — a curate must never be blocked by a strip, and an org-authored source
// usually carries no Helm ownership labels anyway).
func stripBlueprintCRBytesForGit(raw []byte, bpName string) []byte {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil || doc == nil {
		return raw
	}
	if out := stripBlueprintCRMapForGit(doc, bpName); len(out) > 0 {
		return out
	}
	return raw
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
