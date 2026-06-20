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
// the git verdict surfaced (committed / reason). It is the IaC source write;
// Flux reconciles that file into the in-cluster Blueprint CR.
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

	committed, err := h.writeCatalogFullBlueprintToGit(ctx, bpName, yamlBytes)
	if err != nil {
		if h.log != nil {
			h.log.Warn("catalog-iac-edit: commit to catalog-sovereign failed",
				"blueprint", bpName, "err", err)
		}
		writeJSON(w, http.StatusBadGateway, catalogIaCEditResponse{
			Slug:      normalizeCatalogKey(bpName),
			Path:      catalogSovereignOrg + "/" + bpName + "/" + catalogEditBlueprintPath,
			Committed: false,
			Reason:    "IaC commit failed: " + err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, catalogIaCEditResponse{
		Slug:      normalizeCatalogKey(bpName),
		Path:      catalogSovereignOrg + "/" + bpName + "/" + catalogEditBlueprintPath,
		Committed: true,
		Reason:    iaCEditDurableReason(committed),
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

// writeCatalogFullBlueprintToGit commits the FULL supplied blueprint.yaml to
// catalog-sovereign/<bpName>/blueprint.yaml — the same file + repo the
// card-edit path (writeCatalogEditToGit) writes, so both editors drive the
// SAME Gitea source of truth (§6). Unlike the card path this is NOT a card
// merge: the admin owns the whole document, so it is committed verbatim
// (after the caller's validateBlueprintYAML).
//
// Returns (wroteBytes, err): wroteBytes=false + err=nil when PutFile's
// byte-equal short-circuit fired (the file already carried these exact bytes
// — still durable). A non-nil err is a genuine transport/API failure.
func (h *Handler) writeCatalogFullBlueprintToGit(ctx context.Context, bpName string, blueprintYAML []byte) (bool, error) {
	if h.giteaClient == nil {
		return false, fmt.Errorf("gitea client unwired")
	}
	if _, err := h.giteaClient.EnsureOrg(ctx, catalogSovereignOrg,
		"Sovereign-curated catalog",
		"Curated Blueprints visible to every Org", "public"); err != nil {
		return false, fmt.Errorf("ensure org %q: %w", catalogSovereignOrg, err)
	}
	if _, err := h.giteaClient.EnsureRepo(ctx, catalogSovereignOrg, bpName,
		"Sovereign-curated Blueprint", false); err != nil {
		return false, fmt.Errorf("ensure repo %s/%s: %w", catalogSovereignOrg, bpName, err)
	}
	msg := fmt.Sprintf("catalog: edit %s full IaC via catalyst-api (#3668)", bpName)
	_, committed, err := h.giteaClient.PutFile(ctx, catalogSovereignOrg, bpName,
		catalogEditGitBranch, catalogEditBlueprintPath, blueprintYAML, msg, catalogEditCommitAuthor())
	if err != nil {
		return false, fmt.Errorf("put %s/%s/%s: %w", catalogSovereignOrg, bpName, catalogEditBlueprintPath, err)
	}
	return committed, nil
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
