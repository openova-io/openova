// Package handler — catalog_iac_edit.go: #3668 §5D — the FULL-CR catalog
// IaC editor endpoint.
//
// The card-edit path (org_commerce.go → catalog_edit_git.go) commits the 7
// card fields to catalog-sovereign/<bp>/blueprint.yaml. The full-CR editor
// (the console's "Edit IaC" mode mounting widgets/cloud-list/YamlEditor)
// must be able to edit the WHOLE blueprint — spec.source / spec.manifests /
// spec.sso / spec.placementSchema / spec.endpoints / shareable /
// contextSchema — fields the 7-field form can never touch (§3D).
//
// Both paths write the SAME Gitea file (§6: "Both paths write the SAME Gitea
// file; the page re-reads after save"). This endpoint commits the full YAML
// the admin supplies directly to catalog-sovereign/<bp>/blueprint.yaml —
// validated as a Blueprint CR first (reusing validateBlueprintYAML), under
// the dedicated catalogEditGitBudget (NOT the commerce probe budget), with
// the git verdict surfaced (committed / reason).
//
// #4896 / #5018 — the commit is a DUAL write, exactly like the card path:
//
//  1. catalog-sovereign/<repo>/blueprint.yaml — the UI read source. <repo> is
//     the repo the READ path actually resolves (resolveCatalogEditRepo —
//     mirrors the catalyst-catalog resolver's bare↔bp- toggle, #4990/#4997),
//     so a resource loaded by the read path always commits back to the SAME
//     file it was loaded from, never a divergent freshly-minted twin repo.
//  2. openova/openova @ catalog-sovereign :
//     clusters/<fqdn>/catalog-sovereign/<bp>.yaml — the Flux-reconciled
//     aggregator leg (writeCatalogSovereignAggregatorBytes). This is what
//     actually reaches the LIVE in-cluster Blueprint CR; without it the
//     commit is silently inert (the exact hw242 #5018 finding — UAT rows
//     148/154). Unlike the card path there is NO #4415 delivery-field strip:
//     an explicit whole-CR commit legitimately edits spec.version (DoD §9.7).
//     A failure of this leg is SURFACED (502 committed:false), never
//     swallowed — "Committed ✓" on an unreconciled edit is the false-green
//     #5018 called out.
//
// Auth: the same tier-admin gate the existing /blueprints/edit-pr uses
// (applicationInstallCallerAuthorized) — this ticket adds no new
// authorization model (§11 excluded).

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// catalogIaCEditRequest is the body of PUT /api/v1/catalog/{name}/iac — the
// full blueprint.yaml the admin edited in the IaC editor.
type catalogIaCEditRequest struct {
	// BlueprintYAML is the complete Blueprint CR YAML to commit. It is
	// validated as a Blueprint before the write so a malformed edit is
	// rejected (400) rather than committed and left for Flux to choke on.
	BlueprintYAML string `json:"blueprintYaml"`
}

// catalogIaCEditResponse mirrors the card-edit envelope (#3668 §5C) so the UI
// reads one shape: committed=true + the resolved slug/path on a durable
// commit, committed=false + reason otherwise.
type catalogIaCEditResponse struct {
	Slug      string `json:"slug"`
	Path      string `json:"path"`
	Committed bool   `json:"committed"`
	Reason    string `json:"reason,omitempty"`
}

// HandleCatalogBlueprintIaCEdit — PUT /api/v1/catalog/{name}/iac.
//
// Commits the supplied FULL blueprint.yaml to
// catalog-sovereign/bp-<name>/blueprint.yaml (the same file the card-edit
// path writes), so the console's full-CR IaC editor edits every blueprint
// field through one Gitea source of truth. Session-gated like the rest of
// /api/v1/*; additionally tier-admin-gated (mirrors /blueprints/edit-pr).
func (h *Handler) HandleCatalogBlueprintIaCEdit(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	bpName := catalogEditBlueprintName(name)
	if bpName == "" {
		writeBadRequest(w, "invalid-blueprint", "name path parameter is required")
		return
	}

	// Tier-admin gate — the existing /blueprints/edit-pr authority. No new
	// authorization model (§11 excluded).
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "PUT /catalog/{name}/iac requires tier-admin or higher",
			})
			return
		}
	}

	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	var req catalogIaCEditRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeBadRequest(w, "invalid-body", "request body must be JSON {blueprintYaml}")
		return
	}
	yamlBytes := []byte(req.BlueprintYAML)
	if len(strings.TrimSpace(req.BlueprintYAML)) == 0 {
		writeBadRequest(w, "empty-blueprint", "blueprintYaml is required")
		return
	}

	// Validate it parses as a Blueprint CR BEFORE committing — a malformed
	// full-CR edit must 400, never land in Gitea for Flux to reject. The
	// metadata.name must address THIS blueprint so an edit can't be
	// retargeted to a different bp's file.
	if msg, ok := validateCatalogBlueprintYAML(yamlBytes, bpName); !ok {
		writeBadRequest(w, "invalid-blueprint-yaml", msg)
		return
	}

	if h.giteaClient == nil {
		// No local catalog git yet (pre-cutover / CI). Report it, never a
		// false success — the IaC editor is meaningless without the source.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "gitea-not-wired",
			"detail": "local catalog git not yet available (pre-cutover); the full-CR IaC edit requires Gitea",
		})
		return
	}

	// Source write under the dedicated git budget (NOT the commerce probe).
	ctx, cancel := context.WithTimeout(r.Context(), catalogEditGitBudgetDuration())
	defer cancel()

	repo, committed, err := h.writeCatalogFullBlueprintToGit(ctx, bpName, yamlBytes)
	if err != nil {
		if h.log != nil {
			h.log.Warn("catalog-iac-edit: commit to catalog-sovereign failed",
				"blueprint", bpName, "err", err)
		}
		writeJSON(w, http.StatusBadGateway, catalogIaCEditResponse{
			Slug:      normalizeCatalogKey(bpName),
			Path:      catalogSovereignOrg + "/" + repo + "/" + catalogEditBlueprintPath,
			Committed: false,
			Reason:    "IaC commit failed: " + err.Error(),
		})
		return
	}

	// #4896 / #5018 — leg 2: mirror the committed document into the Flux-
	// reconciled aggregator tree so the edit actually reaches the LIVE
	// in-cluster Blueprint CR. metadata.name is stamped to the canonical CR
	// identity (bp-<slug> — the same bridge reconcileBlueprintBareName applies
	// on the dry-run path, #4898) so Flux SSA-patches the existing CR instead
	// of minting a bare-named duplicate; no #4415 delivery-field strip — the
	// admin's explicit full-CR edit owns spec.version/spec.source (DoD §9.7).
	var aggCommitted bool
	var aggErr error
	if aggBytes := stampBlueprintCRForFluxAggregator(yamlBytes, bpName); len(aggBytes) == 0 {
		// Unreachable after validateCatalogBlueprintYAML parsed these same
		// bytes — but never silently skip the reconcile leg on a render miss.
		aggErr = fmt.Errorf("render Flux aggregator payload for %s: document did not re-parse", bpName)
	} else {
		aggCommitted, aggErr = h.writeCatalogSovereignAggregatorBytes(ctx, bpName, aggBytes)
	}
	if aggErr != nil {
		if h.log != nil {
			h.log.Warn("catalog-iac-edit: Flux aggregator mirror failed — edit committed to source but will NOT reach the live Blueprint CR",
				"blueprint", bpName, "repo", repo, "err", aggErr)
		}
		writeJSON(w, http.StatusBadGateway, catalogIaCEditResponse{
			Slug:      normalizeCatalogKey(bpName),
			Path:      catalogSovereignOrg + "/" + repo + "/" + catalogEditBlueprintPath,
			Committed: false,
			Reason: "IaC source committed to " + catalogSovereignOrg + "/" + repo +
				" but the Flux reconcile leg failed (the edit will not reach the live Blueprint CR): " + aggErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, catalogIaCEditResponse{
		Slug:      normalizeCatalogKey(bpName),
		Path:      catalogSovereignOrg + "/" + repo + "/" + catalogEditBlueprintPath,
		Committed: true,
		Reason:    iaCEditDurableReason(committed || aggCommitted),
	})
}

// iaCEditDurableReason gives the UI a short note: a brand-new commit vs a
// byte-identical no-op (both durable — the source carries the bytes).
func iaCEditDurableReason(wroteBytes bool) string {
	if wroteBytes {
		return ""
	}
	return "already up to date (no change)"
}

// catalogIaCSeedResponse is the GET /api/v1/catalog/{name}/iac envelope.
// The FE reads only blueprintYaml (catalog.api.ts getCatalogBlueprintIaC);
// path + source are diagnostic — source names which truth the seed came from
// so a walk can tell a verbatim committed read from a re-canonicalized one.
type catalogIaCSeedResponse struct {
	BlueprintYaml string `json:"blueprintYaml"`
	Path          string `json:"path"`
	// Source is one of:
	//   "committed"               — the committed file, already canonical, verbatim;
	//   "committed-canonicalized" — a stale pre-#5115 committed file, re-served
	//                               through the canonical CR serializer;
	//   "live-cr"                 — nothing committed yet; the catalog-resolved
	//                               CR through the same canonical serializer.
	Source string `json:"source"`
}

// HandleGetCatalogBlueprintIaC — GET /api/v1/catalog/{name}/iac.
//
// Returns the Edit-IaC editor SEED: the CURRENTLY-COMMITTED
// catalog-sovereign/<repo>/blueprint.yaml (the same file the PUT and the
// card-form save write, resolved via the same resolveCatalogEditRepo seam),
// always in the #5115 canonical Blueprint CR shape.
//
// #5124 (hw256 V6 walk, UAT row 130): the Edit-IaC editor originally seeded
// from the store's stale CatalogItem.raw — the pre-#5115 shape (bare `alloy`
// metadata.name, no spec.version, no Helm ownership labels, no card fields) —
// so an IaC Commit from that seed silently reverted card-form edits already in
// the committed file. Serving the committed file (first pass of this endpoint)
// closed the main path, but two stale-seed legs remained, both closed here:
//
//  1. A committed file that PREDATES #5115 (bare name / server-side metadata
//     junk / #4415-stripped spec.version) was returned VERBATIM — the editor
//     opened the stale shape. It is now re-canonicalized on read through the
//     SAME serializer every commit leg uses (stampBlueprintCRForFluxAggregator,
//     #5113/#5115), with spec.version refilled from the catalog-resolved
//     blueprint when the stale file lost it, so the served seed always passes
//     the PUT's validateCatalogBlueprintYAML gate. An already-canonical file
//     is returned verbatim (authored fidelity preserved).
//  2. Nothing committed yet returned 404 and the FE fell back to the stale
//     store raw — the exact defect signature. The endpoint now serves the
//     catalog-resolved CR (chainedCatalogClient: Gitea seed → in-cluster live
//     CR) through the same canonical serializer instead; 404 remains only
//     when neither source resolves, so the FE's raw fallback is a last resort
//     for a blueprint the whole platform cannot see.
//
// Read-only — this endpoint never writes; the canonical shape lands in Gitea
// on the next commit (which re-canonicalizes the aggregator leg regardless).
func (h *Handler) HandleGetCatalogBlueprintIaC(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	bpName := catalogEditBlueprintName(name)
	if bpName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing-name"})
		return
	}
	if h.giteaClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "gitea-unwired"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	repo := h.resolveCatalogEditRepo(ctx, bpName)
	path := fmt.Sprintf("catalog-sovereign/%s/%s", repo, catalogEditBlueprintPath)
	f, err := h.giteaClient.GetFile(ctx, catalogSovereignOrg, repo, catalogEditGitBranch, catalogEditBlueprintPath)
	if err == nil {
		raw, derr := f.Decoded()
		if derr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decode-failed", "detail": derr.Error()})
			return
		}
		if catalogIaCSeedIsCanonical(raw, bpName) {
			writeJSON(w, http.StatusOK, catalogIaCSeedResponse{
				BlueprintYaml: string(raw),
				Path:          path,
				Source:        "committed",
			})
			return
		}
		// Stale pre-#5115 committed shape — re-canonicalize on read so the
		// editor opens the current canonical CR, never the stale document.
		if canonical := h.recanonicalizeCommittedIaCSeed(ctx, raw, bpName); len(canonical) > 0 {
			writeJSON(w, http.StatusOK, catalogIaCSeedResponse{
				BlueprintYaml: string(canonical),
				Path:          path,
				Source:        "committed-canonicalized",
			})
			return
		}
		// Un-parsable committed document (out-of-band push) — return it
		// verbatim rather than fabricating a blank; the PUT's validation
		// will name the parse failure if the admin commits it unchanged.
		writeJSON(w, http.StatusOK, catalogIaCSeedResponse{
			BlueprintYaml: string(raw),
			Path:          path,
			Source:        "committed",
		})
		return
	}
	// Nothing committed yet (or the read failed) — #5124: NEVER hand the FE
	// back to its stale store raw when the blueprint resolves. Serve the
	// catalog-resolved CR through the same canonical serializer.
	if seed := h.canonicalIaCSeedFromCatalog(ctx, bpName); len(seed) > 0 {
		writeJSON(w, http.StatusOK, catalogIaCSeedResponse{
			BlueprintYaml: string(seed),
			Path:          path,
			Source:        "live-cr",
		})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not-committed", "detail": err.Error()})
}

// catalogIaCSeedIsCanonical reports whether a committed blueprint.yaml already
// carries the #5115 canonical Blueprint CR shape: the canonical bp-<slug>
// metadata.name, a non-empty spec.version, and none of the server-side fields
// the canonical serializer scrubs (blueprintCRServerSideMetadataFields, status,
// the last-applied annotation). A canonical file is served VERBATIM (authored
// field order / comments preserved); anything else is the stale pre-#5115
// shape and gets re-canonicalized on read.
func catalogIaCSeedIsCanonical(raw []byte, bpName string) bool {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil || doc == nil {
		return false
	}
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil || asString(meta["name"]) != bpName {
		return false
	}
	for _, k := range blueprintCRServerSideMetadataFields {
		if _, present := meta[k]; present {
			return false
		}
	}
	if ann, ok := meta["annotations"].(map[string]interface{}); ok {
		if _, present := ann["kubectl.kubernetes.io/last-applied-configuration"]; present {
			return false
		}
	}
	if _, present := doc["status"]; present {
		return false
	}
	spec, _ := doc["spec"].(map[string]interface{})
	return spec != nil && strings.TrimSpace(asString(spec["version"])) != ""
}

// recanonicalizeCommittedIaCSeed re-serves a STALE pre-#5115 committed
// blueprint.yaml through the canonical CR serializer (the same
// stampBlueprintCRMapForFluxAggregator every commit leg uses): metadata.name
// stamped to the canonical bp-<slug> identity, server-side metadata scrubbed,
// Helm ownership labels preserved. When the stale file lost spec.version (the
// pre-#5115 #4415 delivery-field strip), it is refilled from the
// catalog-resolved blueprint so the served seed passes the PUT's
// validateCatalogBlueprintYAML gate instead of 400ing the admin's commit.
// Returns nil on an un-parsable document (the caller then serves the raw
// bytes verbatim).
func (h *Handler) recanonicalizeCommittedIaCSeed(ctx context.Context, raw []byte, bpName string) []byte {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil
	}
	if blueprintSpecVersionMissing(doc) && h.catalogClient != nil {
		if bp, err := h.catalogClient.Get(ctx, bpName, ""); err == nil && bp != nil {
			fillBlueprintSpecVersion(doc, bp.Version)
		}
	}
	return stampBlueprintCRMapForFluxAggregator(doc, bpName)
}

// canonicalIaCSeedFromCatalog resolves the blueprint via the catalog client
// (chainedCatalogClient: Gitea seed → in-cluster live CR — the same full-spec
// source the card path's first-edit seed uses, #3668 §5A) and renders it
// through the canonical CR serializer, refilling spec.version from the
// resolved catalog version when the Raw document lacks it. Returns nil when
// the client is unwired or the blueprint cannot be resolved — the caller then
// 404s and the FE falls back to its store raw as a true last resort.
func (h *Handler) canonicalIaCSeedFromCatalog(ctx context.Context, bpName string) []byte {
	if h.catalogClient == nil {
		return nil
	}
	bp, err := h.catalogClient.Get(ctx, bpName, "")
	if err != nil || bp == nil || len(bp.Raw) == 0 {
		return nil
	}
	doc := deepCopyYAMLMap(bp.Raw)
	fillBlueprintSpecVersion(doc, bp.Version)
	return stampBlueprintCRMapForFluxAggregator(doc, bpName)
}

// blueprintSpecVersionMissing reports whether a decoded Blueprint CR map lacks
// a non-empty spec.version (the CRD-required field the pre-#5115 #4415 strip
// removed from committed files).
func blueprintSpecVersionMissing(doc map[string]interface{}) bool {
	spec, _ := doc["spec"].(map[string]interface{})
	return spec == nil || strings.TrimSpace(asString(spec["version"])) == ""
}

// fillBlueprintSpecVersion sets spec.version to the supplied version when the
// document lacks one. Never overwrites an authored version — the admin owns
// an explicit spec.version (DoD §9.7).
func fillBlueprintSpecVersion(doc map[string]interface{}, version string) {
	v := strings.TrimSpace(version)
	if v == "" || !blueprintSpecVersionMissing(doc) {
		return
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		spec = map[string]interface{}{}
		doc["spec"] = spec
	}
	spec["version"] = v
}

// writeCatalogFullBlueprintToGit commits the FULL supplied blueprint.yaml to
// catalog-sovereign/<repo>/blueprint.yaml — the same file + repo the read
// path resolves (resolveCatalogEditRepo), so both editors drive the SAME
// Gitea source of truth (§6). Unlike the card path this is NOT a card
// merge: the admin owns the whole document, so it is committed verbatim
// (after the caller's validateBlueprintYAML).
//
// Returns (repo, wroteBytes, err): repo is the per-Blueprint repo actually
// written (bare or bp-prefixed — whichever the read path resolves);
// wroteBytes=false + err=nil when PutFile's byte-equal short-circuit fired
// (the file already carried these exact bytes — still durable). A non-nil
// err is a genuine transport/API failure.
func (h *Handler) writeCatalogFullBlueprintToGit(ctx context.Context, bpName string, blueprintYAML []byte) (string, bool, error) {
	if h.giteaClient == nil {
		return bpName, false, fmt.Errorf("gitea client unwired")
	}
	if _, err := h.giteaClient.EnsureOrg(ctx, catalogSovereignOrg,
		"Sovereign-curated catalog",
		"Curated Blueprints visible to every Org", "public"); err != nil {
		return bpName, false, fmt.Errorf("ensure org %q: %w", catalogSovereignOrg, err)
	}
	repo := h.resolveCatalogEditRepo(ctx, bpName)
	if _, err := h.giteaClient.EnsureRepo(ctx, catalogSovereignOrg, repo,
		"Sovereign-curated Blueprint", false); err != nil {
		return repo, false, fmt.Errorf("ensure repo %s/%s: %w", catalogSovereignOrg, repo, err)
	}
	msg := fmt.Sprintf("catalog: edit %s full IaC via catalyst-api (#3668)", bpName)
	_, committed, err := h.giteaClient.PutFile(ctx, catalogSovereignOrg, repo,
		catalogEditGitBranch, catalogEditBlueprintPath, blueprintYAML, msg, catalogEditCommitAuthor())
	if err != nil {
		return repo, false, fmt.Errorf("put %s/%s/%s: %w", catalogSovereignOrg, repo, catalogEditBlueprintPath, err)
	}
	return repo, committed, nil
}

// resolveCatalogEditRepo returns the catalog-sovereign per-Blueprint repo the
// READ path resolves for this blueprint, so the full-CR write lands in the
// SAME file the editor loaded (#4896 — single naming source of truth shared
// by read+write).
//
// The catalyst-catalog resolver (core/services/catalyst-catalog/internal/
// source, fetchBlueprintYAML/altBPName — #4990/#4997) serves the UI's GET
// with a bare↔bp- prefix toggle: the frontend strips a leading `bp-`
// (CatalogDetail.tsx), so the resolver tries the BARE repo first (deployed
// Blueprints live in bare-named repos, e.g. `agenity`) and falls back to the
// `bp-` form (catalog-seed-only entries keep the raw `bp-<x>` repo name,
// e.g. `bp-alloy`). That package is module-internal, so the convention is
// MIRRORED here — probe in the resolver's order and write to the first repo
// whose blueprint.yaml exists:
//
//  1. bare  <slug>    — matches a deployed Blueprint's repo (read hit #1);
//  2. bp-<slug>       — matches a seed-only / curated repo (read hit #2);
//  3. neither exists  — default to the canonical bp-<slug> (fresh curate,
//     same repo the card-edit path mints).
//
// Before this resolution the write unconditionally minted `bp-<slug>`, so a
// commit against a bare-repo blueprint created a divergent twin the read
// path never consults — half of the #5018 "commit is inert" split-brain.
func (h *Handler) resolveCatalogEditRepo(ctx context.Context, bpName string) string {
	bare := strings.TrimPrefix(bpName, "bp-")
	for _, repo := range []string{bare, bpName} {
		if repo == "" {
			continue
		}
		if _, err := h.giteaClient.GetFile(ctx, catalogSovereignOrg, repo,
			catalogEditGitBranch, catalogEditBlueprintPath); err == nil {
			return repo
		}
	}
	return bpName
}

// stampBlueprintCRForFluxAggregator is the CANONICAL Blueprint-CR commit
// serializer, shared by ALL THREE catalog commit legs — the full-CR IaC
// editor (this file), the card-form save (writeCatalogEditToGit), and the
// curate mirror (writeCatalogSovereignAggregator) — per #5113's "card-save
// must reuse the SAME canonical serializer as the Edit-IaC leg (#5041)".
//
// It renders a Blueprint document as the committed payload (#4896/#5018):
// metadata.name is FORCED to the canonical live-CR identity (bp-<slug>) so
// the catalog-sovereign Kustomization's force:true SSA patches the existing
// Blueprint CR instead of creating a bare-named duplicate whose inventory
// swap PRUNES the live CR (#5113 Facet B — the hw255 bp-wordpress deletion);
// the same identity bridge the dry-run/apply path applies via
// reconcileBlueprintBareName (#4898). Server-side metadata and status are
// dropped while Helm ownership labels/annotations are preserved
// (stripBlueprintCRMapForGit); spec.version/spec.source are KEPT —
// spec.version is CRD-required, so a version-less file wedges the whole
// Kustomization whenever the CR doesn't already exist (#5113 Facet A), and
// an explicit whole-CR commit owns its delivery fields anyway (DoD §9.7;
// UAT row 154's version round-trip).
//
// Returns nil on an unparsable document — the callers surface that as a
// failed render rather than committing a wrong-named/unparsable CR.
func stampBlueprintCRForFluxAggregator(raw []byte, bpName string) []byte {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil || doc == nil {
		return nil
	}
	return stampBlueprintCRMapForFluxAggregator(doc, bpName)
}

// stampBlueprintCRMapForFluxAggregator is the map-level entry of the canonical
// serializer above, shared with the #5124 Edit-IaC seed paths (which decode
// first to refill a stripped spec.version before canonicalizing). MUTATES doc.
func stampBlueprintCRMapForFluxAggregator(doc map[string]interface{}, bpName string) []byte {
	if doc == nil {
		return nil
	}
	if meta, ok := doc["metadata"].(map[string]interface{}); ok {
		meta["name"] = bpName
	} else {
		doc["metadata"] = map[string]interface{}{"name": bpName}
	}
	return stripBlueprintCRMapForGit(doc, bpName)
}

// validateCatalogBlueprintYAML enforces the structural Blueprint invariants
// the full-CR catalog editor must hold before a commit, WITHOUT requiring a
// version match (the admin may legitimately bump spec.version in the editor —
// that round-trip is DoD §9.7). Unlike blueprints.go's validateBlueprintYAML
// (which pins metadata.name + spec.version to a publish request), this checks:
// the document parses; apiVersion present; kind == Blueprint; metadata.name
// addresses THIS blueprint (so an edit can't be retargeted to another bp's
// file); spec present with a non-empty spec.version. Returns ("", true) when
// valid, else (reason, false).
func validateCatalogBlueprintYAML(raw []byte, bpName string) (string, bool) {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		return fmt.Sprintf("blueprintYaml parse: %v", err), false
	}
	if apiVersion, _ := doc["apiVersion"].(string); strings.TrimSpace(apiVersion) == "" {
		return "blueprint.yaml missing apiVersion", false
	}
	if kind, _ := doc["kind"].(string); kind != "Blueprint" {
		return fmt.Sprintf("blueprint.yaml kind=%q; want Blueprint", kind), false
	}
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil {
		return "blueprint.yaml missing metadata", false
	}
	metaName, _ := meta["name"].(string)
	if strings.TrimSpace(metaName) == "" {
		return "blueprint.yaml missing metadata.name", false
	}
	// The name must address this blueprint (bare or bp-prefixed) so the edit
	// lands in the right catalog-sovereign repo and can't clobber another's.
	if catalogEditBlueprintName(metaName) != bpName {
		return fmt.Sprintf("blueprint.yaml metadata.name=%q does not address %q", metaName, bpName), false
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		return "blueprint.yaml missing spec", false
	}
	if v, _ := spec["version"].(string); strings.TrimSpace(v) == "" {
		return "blueprint.yaml missing spec.version", false
	}
	return "", true
}
