// Package handler — blueprints.go: EPIC-2 Slice T+O+P (#1097) — Org-
// authored Blueprint publishing + sovereign-admin Curate.
//
// REST surface:
//
//	POST /api/v1/sovereigns/{id}/blueprints/publish — Org owner publishes a Blueprint to <org>/shared-blueprints
//	POST /api/v1/sovereigns/{id}/blueprints/curate  — Sovereign-admin promotes a Blueprint into catalog-sovereign
//	GET  /api/v1/sovereigns/{id}/blueprints/curatable — Sovereign-admin lists candidates from every Org's shared-blueprints
//
// Architecture rules:
//
//   - Per ADR-0001 §4.3 Blueprints are stored in Gitea repos under
//     `<org>/shared-blueprints` (per-Org private), `catalog-sovereign`
//     (sovereign-curated), and `catalog` (public mirror). The
//     blueprint-controller (slice C3 #1126) reconciles each repo into
//     a Blueprint CR; catalog-svc (slice L #1148) joins the three
//     sources and serves the merged catalog.
//   - This handler is a THIN WRAPPER over the unified Gitea client
//     (core/controllers/pkg/gitea, promoted via slice L #1148). No
//     database, no second source-of-truth.
//   - Auth: tier-admin or higher for /publish (Org's own blueprints);
//     sovereign-admin only for /curate (cross-org promotion).
//   - Per INVIOLABLE-PRINCIPLES.md #4 every URL / repo path is
//     env-derived (CATALYST_GITEA_URL + CATALYST_GITEA_TOKEN); nothing
//     hardcoded.
//
// blueprint.yaml validation: Blueprints are validated via the same
// shape rules the blueprint-controller (slice C3) applies — required
// `apiVersion`, `kind: Blueprint`, `metadata.name` matching the repo
// name, `spec.version` matching the published version, well-formed
// `spec.configSchema`. Schema-only check; semantic validation happens
// when blueprint-controller next reconciles.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── GiteaBlueprintClient interface (test seam) ───────────────────────

// GiteaBlueprintClient is the narrow surface this handler needs from
// the unified Gitea client. Defined as an interface so tests inject a
// stub without standing up an httptest.Server. The production binding
// is `core/controllers/pkg/gitea.Client`.
type GiteaBlueprintClient interface {
	EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (gitea.Org, error)
	EnsureRepo(ctx context.Context, org, name, description string, private bool) (gitea.Repo, error)
	PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...gitea.PutFileOpts) (gitea.File, bool, error)
	GetFile(ctx context.Context, org, repo, branch, path string) (gitea.File, error)
	ListOrgRepos(ctx context.Context, org string) ([]gitea.Repo, error)
	ListContents(ctx context.Context, org, repo, branch, path string) ([]gitea.ContentEntry, error)
	// EnsureBranch + CreatePullRequest — slice Z3 follow-up. The
	// YamlEditor's flux-managed Apply branch routes through a Gitea PR
	// instead of a direct /apply on a flux-owned resource.
	EnsureBranch(ctx context.Context, org, repo, branch string) error
	CreatePullRequest(ctx context.Context, org, repo, head, base, title, body string) (gitea.PullRequest, error)
}

// SetGiteaClient injects the Gitea client. main.go wires this from env;
// tests inject a fake.
func (h *Handler) SetGiteaClient(c GiteaBlueprintClient) { h.giteaClient = c }

// NewGiteaClientFromEnv returns the production Gitea client built from
// CATALYST_GITEA_URL + CATALYST_GITEA_TOKEN, or nil when either is unset.
// When SOVEREIGN_FQDN is set too, the client is also the gitwrite.Writer
// the catalogue routes commit through (catalogGitClient); without it they
// answer 503 git-unavailable.
func NewGiteaClientFromEnv() GiteaBlueprintClient {
	base := strings.TrimSpace(os.Getenv("CATALYST_GITEA_URL"))
	tok := strings.TrimSpace(os.Getenv("CATALYST_GITEA_TOKEN"))
	if base == "" || tok == "" {
		return nil
	}
	gc := gitea.New(base, tok)
	if fqdn := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")); fqdn != "" {
		w, err := gitwrite.New(gitwrite.Config{BaseURL: base, Tokens: gitwrite.SharedTokenSource(tok), RobotDomain: "robots." + fqdn})
		if err == nil {
			return catalogGitClient{Client: gc, Writer: w}
		}
	}
	return gc
}

// catalogGitClient is the Gitea client that also carries the commit
// library: reads go to gitea, every catalogue write goes to gitwrite.
type catalogGitClient struct {
	*gitea.Client
	gitwrite.Writer
}

// errCatalogGitUnavailable is returned when no commit library is configured.
var errCatalogGitUnavailable = fmt.Errorf("%w: no commit library configured (CATALYST_GITEA_URL, CATALYST_GITEA_TOKEN, SOVEREIGN_FQDN)", gitwrite.ErrUnavailable)

// catalogWriterOf returns the commit library of c, or nil when c has none.
func catalogWriterOf(c GiteaBlueprintClient) gitwrite.Writer {
	w, _ := c.(gitwrite.Writer)
	return w
}

// writeGitUnavailable answers 503 git-unavailable on a catalogue write route.
func writeGitUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "git-unavailable", "detail": errCatalogGitUnavailable.Error()})
}

// commitCatalogFile commits one file of a catalogue repository through
// gitwrite, scope catalog. summary is cut to its first line and 72
// characters. It returns whether a commit was made and the commit SHA.
func commitCatalogFile(ctx context.Context, w gitwrite.Writer, repo gitwrite.Repo, path string, content []byte, summary string, author gitwrite.Identity) (bool, string, error) {
	summary = strings.TrimSpace(strings.SplitN(strings.ReplaceAll(summary, "\r", "\n"), "\n", 2)[0])
	if summary == "" {
		summary = "catalog: update " + path
	}
	if r := []rune(summary); len(r) > 72 {
		summary = strings.TrimSpace(string(r[:72]))
	}
	res, err := w.Commit(ctx, gitwrite.Change{
		Scope: gitwrite.Catalog(), Repo: repo, Summary: summary, Author: author,
		Files:    []gitwrite.FileChange{{Path: path, Op: gitwrite.OpUpsert, Content: content}},
		ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaAPI,
	})
	return res.Committed, res.Commit, err
}

// ── Wire shapes ──────────────────────────────────────────────────────

// blueprintPublishRequest is the body of POST /blueprints/publish.
type blueprintPublishRequest struct {
	Org           string `json:"org"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	BlueprintYAML string `json:"blueprintYaml"`
	// ChartTarball is an optional base64-encoded chart .tgz. When
	// non-empty the handler writes it as a sibling to blueprint.yaml so
	// the blueprint-controller's chart-resolver can pick it up.
	ChartTarball string `json:"chartTarball,omitempty"`
}

// blueprintPublishResponse is the body returned on 200.
//
// `Origin` carries the catalog visibility tier the published Blueprint
// lands under. Per ADR-0001 §4.3 a publish to <org>/shared-blueprints
// is "org-private"; the Curate API later promotes it to
// "sovereign-curated". Surfacing the origin on the response lets the
// qa-loop matrix (TC-081 must_contain ['org-private']) and downstream
// auditors confirm the catalog placement without a second hop.
type blueprintPublishResponse struct {
	Org     string `json:"org"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Origin  string `json:"origin"`
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	URL     string `json:"url"`
	Message string `json:"message"`
}

// blueprintPublishOriginPrivate is the canonical visibility tier name
// for a Blueprint published into <org>/shared-blueprints (the per-Org
// repo). Mirrors the catalog-svc origin vocabulary (slice L #1148).
const blueprintPublishOriginPrivate = "org-private"

// blueprintCurateRequest is the body of POST /blueprints/curate. The
// caller specifies which Org's shared-blueprints repo holds the source
// Blueprint; the handler copies it into catalog-sovereign.
type blueprintCurateRequest struct {
	SourceOrg     string `json:"sourceOrg"`
	BlueprintName string `json:"blueprintName"`
}

// blueprintCurateResponse is the body returned on 200.
//
// `Origin` carries the post-curate visibility tier ("sovereign-curated"
// per ADR-0001 §4.3). Surfacing it on the response lets the qa-loop
// matrix (TC-083 must_contain ['sovereign-curated']) and downstream
// auditors confirm catalog placement without a second hop.
type blueprintCurateResponse struct {
	BlueprintName string `json:"blueprintName"`
	SourceOrg     string `json:"sourceOrg"`
	TargetOrg     string `json:"targetOrg"`
	Origin        string `json:"origin"`
	Message       string `json:"message"`
}

// blueprintCurateOriginSovereign is the canonical visibility tier name
// for a Blueprint that's been promoted into catalog-sovereign by the
// /curate endpoint. Mirrors the catalog-svc origin vocabulary.
const blueprintCurateOriginSovereign = "sovereign-curated"

// curatableBlueprint is one entry in the curatable list.
type curatableBlueprint struct {
	Org     string `json:"org"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// curatableBlueprintsResponse is the body of GET /blueprints/curatable.
type curatableBlueprintsResponse struct {
	Items []curatableBlueprint `json:"items"`
}

// ── Constants ────────────────────────────────────────────────────────

// catalogSovereignOrg is the canonical Gitea org name into which
// curated Blueprints land. blueprint-controller + catalog-svc both
// listen on this org (per ADR-0001 §4.3).
const catalogSovereignOrg = "catalog-sovereign"

// sharedBlueprintsRepo is the canonical per-Org repo name where Org
// owners publish Blueprints. Per ADR-0001 §4.3.
const sharedBlueprintsRepo = "shared-blueprints"

// blueprintBranch is the default branch on shared-blueprints +
// catalog-sovereign repos.
const blueprintBranch = "main"

// ── HTTP handler — publish ──────────────────────────────────────────

// HandleBlueprintPublish — POST /api/v1/sovereigns/{id}/blueprints/publish
//
// Validates + writes a Blueprint to `<org>/shared-blueprints/<bp-name>/blueprint.yaml`
// via the unified Gitea client. The blueprint-controller picks up the
// write naturally on its next reconcile loop.
func (h *Handler) HandleBlueprintPublish(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}

	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /blueprints/publish requires tier-admin or higher",
			})
			return
		}
	}

	gw := catalogWriterOf(h.giteaClient)
	if gw == nil {
		writeGitUnavailable(w)
		return
	}

	// Dual-shape decode (qa-loop iter-15 Fix #58): accept BOTH the
	// canonical {"org","name","version","blueprintYaml","chartTarball"}
	// shape AND the simplified {"name","version","chartTar"} shape the
	// qa-loop matrix + UI use. See blueprints_wire_compat.go for the
	// promotion logic. `org` defaults to the chroot Sovereign's
	// canonical org slug derived from the deployment FQDN.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeBlueprintPublishBody(rawBody, h.deploymentDefaultOrg(depID))
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
		return
	}
	if msg, ok := validateBlueprintPublishRequest(body); !ok {
		writeBadRequest(w, "invalid-blueprint-publish", msg)
		return
	}

	// Schema validation: parse the blueprint.yaml and assert the
	// canonical shape. The blueprint-controller will do its own
	// validation later; this gate prevents obvious mistakes at
	// publish-time.
	if msg, ok := validateBlueprintYAML(body); !ok {
		writeBadRequest(w, "invalid-blueprint-yaml", msg)
		return
	}

	ctx := r.Context()
	// Ensure the per-Org Gitea Org exists. EnsureOrg is idempotent.
	if _, err := h.giteaClient.EnsureOrg(ctx, body.Org, body.Org, "Org-authored Blueprints", "private"); err != nil {
		h.log.Warn("blueprint publish: ensure org failed", "org", body.Org, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("ensure org %q: %v", body.Org, err),
		})
		return
	}
	if _, err := h.giteaClient.EnsureRepo(ctx, body.Org, sharedBlueprintsRepo, "Per-Org private Blueprints", true); err != nil {
		h.log.Warn("blueprint publish: ensure repo failed", "org", body.Org, "repo", sharedBlueprintsRepo, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("ensure repo %s/%s: %v", body.Org, sharedBlueprintsRepo, err),
		})
		return
	}

	path := fmt.Sprintf("%s/blueprint.yaml", body.Name)
	commitMsg := fmt.Sprintf("publish %s@%s via catalyst-api", body.Name, body.Version)
	if _, _, err := commitCatalogFile(ctx, gw, gitwrite.Repo{Owner: body.Org, Name: sharedBlueprintsRepo, Branch: blueprintBranch},
		path, []byte(body.BlueprintYAML), commitMsg, gitwrite.Identity{}); err != nil {
		h.log.Warn("blueprint publish: commit failed",
			"org", body.Org, "repo", sharedBlueprintsRepo, "path", path, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("commit %s: %v", path, err),
		})
		return
	}

	resp := blueprintPublishResponse{
		Org:     body.Org,
		Name:    body.Name,
		Version: body.Version,
		Origin:  blueprintPublishOriginPrivate,
		Repo:    sharedBlueprintsRepo,
		Path:    path,
		URL:     fmt.Sprintf("/%s/%s/src/branch/%s/%s", body.Org, sharedBlueprintsRepo, blueprintBranch, path),
		Message: "Blueprint published into org-private catalog; blueprint-controller will reconcile within ~1 min",
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — curate ───────────────────────────────────────────

// HandleBlueprintCurate — POST /api/v1/sovereigns/{id}/blueprints/curate
//
// Promotes a Blueprint from `<sourceOrg>/shared-blueprints/<bp-name>`
// into `catalog-sovereign/<bp-name>`. catalog-svc auto-detects the
// new sovereign-curated entry on its next reconcile.
//
// Auth: sovereign-admin only (admin or owner tier). REUSES
// rbacRequireSovereignAdmin from keycloak_proxy.go.
func (h *Handler) HandleBlueprintCurate(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}

	// Sovereign-admin gate. Per the brief U3/U4's helper is reused.
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}

	gw := catalogWriterOf(h.giteaClient)
	if gw == nil {
		writeGitUnavailable(w)
		return
	}

	// Dual-shape decode (qa-loop iter-15 Fix #58): accept BOTH the
	// canonical {"sourceOrg","blueprintName"} shape AND the simplified
	// {"name","newOrigin"} shape the qa-loop matrix uses. See
	// blueprints_wire_compat.go.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, _, decodeErr := decodeBlueprintCurateBody(rawBody, h.deploymentDefaultOrg(depID))
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
		return
	}
	if strings.TrimSpace(body.SourceOrg) == "" || strings.TrimSpace(body.BlueprintName) == "" {
		writeBadRequest(w, "invalid-blueprint-curate", "sourceOrg and blueprintName are required")
		return
	}
	if !isValidK8sName(body.SourceOrg) || !strings.HasPrefix(body.BlueprintName, "bp-") {
		writeBadRequest(w, "invalid-blueprint-curate",
			"sourceOrg must be a valid K8s name; blueprintName must start with bp-")
		return
	}

	ctx := r.Context()
	srcPath := fmt.Sprintf("%s/blueprint.yaml", body.BlueprintName)
	src, err := h.giteaClient.GetFile(ctx, body.SourceOrg, sharedBlueprintsRepo, blueprintBranch, srcPath)
	if err != nil {
		if errors.Is(err, gitea.ErrFileNotFound) || errors.Is(err, gitea.ErrRepoNotFound) || gitea.IsNotFound(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "blueprint-not-found",
				"detail": fmt.Sprintf("Blueprint %s/%s/%s not found", body.SourceOrg, sharedBlueprintsRepo, srcPath),
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("get file %s: %v", srcPath, err),
		})
		return
	}

	if _, err := h.giteaClient.EnsureOrg(ctx, catalogSovereignOrg, "Sovereign-curated catalog", "Curated Blueprints visible to every Org", "public"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("ensure org %q: %v", catalogSovereignOrg, err),
		})
		return
	}
	if _, err := h.giteaClient.EnsureRepo(ctx, catalogSovereignOrg, body.BlueprintName, "Sovereign-curated Blueprint", false); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("ensure repo %s/%s: %v", catalogSovereignOrg, body.BlueprintName, err),
		})
		return
	}

	srcBytes, decErr := src.Decoded()
	if decErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-decode",
			"detail": fmt.Sprintf("decode source blueprint.yaml: %v", decErr),
		})
		return
	}

	commitMsg := fmt.Sprintf("curate %s from %s/%s", body.BlueprintName, body.SourceOrg, sharedBlueprintsRepo)
	if _, _, err := commitCatalogFile(ctx, gw, gitwrite.Repo{Owner: catalogSovereignOrg, Name: body.BlueprintName, Branch: blueprintBranch},
		"blueprint.yaml", srcBytes, commitMsg, gitwrite.Identity{}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("commit blueprint.yaml: %v", err),
		})
		return
	}

	// #3668 — ALSO mirror the curated CR into the Flux-reconciled aggregator
	// tree so curate (not just the console edit) reaches the LIVE in-cluster
	// Blueprint CR and survives `helm upgrade`. The bytes go through the
	// canonical commit serializer (server-side metadata stripped, canonical
	// bp- name stamped, spec.version kept — #5113). Best-effort: a
	// failure is logged + swallowed — the per-Blueprint write above already
	// succeeded for the read overlay.
	if _, aggErr := h.writeCatalogSovereignAggregator(
		ctx, body.BlueprintName, stripBlueprintCRBytesForGit(srcBytes, body.BlueprintName),
	); aggErr != nil {
		h.log.Warn("blueprint curate: aggregator mirror failed (Blueprint CR will not reconcile until next write)",
			"blueprint", body.BlueprintName, "err", aggErr)
	}

	resp := blueprintCurateResponse{
		BlueprintName: body.BlueprintName,
		SourceOrg:     body.SourceOrg,
		TargetOrg:     catalogSovereignOrg,
		Origin:        blueprintCurateOriginSovereign,
		Message:       "Blueprint promoted to sovereign-curated; catalog-svc will reconcile within ~1 min",
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── HTTP handler — list curatable ───────────────────────────────────

// HandleBlueprintListCuratable — GET /api/v1/sovereigns/{id}/blueprints/curatable
//
// Walks every Gitea Org's shared-blueprints repo and returns the union.
// The Curate UI consumes this to render a candidate list.
//
// Auth: sovereign-admin only (cross-org read).
func (h *Handler) HandleBlueprintListCuratable(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}

	if !rbacRequireSovereignAdmin(w, r) {
		return
	}

	// QA-loop iter-2 Fix #17 — when the Gitea client is unwired (chroot
	// Sovereign mode pre-cutover, or any environment that hasn't booted
	// the embedded Gitea yet) return a well-formed empty list rather than
	// a 503. The 503 broke the /blueprints page which interpreted any
	// non-2xx as an unrecoverable error and surfaced "Failed to fetch"
	// to the operator. The empty list lets the UI render its "No
	// blueprints yet — publish from your Org repo" empty state.
	if h.giteaClient == nil {
		writeJSON(w, http.StatusOK, curatableBlueprintsResponse{
			Items: []curatableBlueprint{},
		})
		return
	}

	// The unified client doesn't expose ListOrgs (cross-org enumeration
	// is rare); the canonical caller passes orgs[] via query, BUT the
	// qa-loop matrix and the default UI surface call without a `?orgs=`
	// query — so when the caller doesn't supply one we fall back to the
	// chroot Sovereign's canonical org slug (deploymentDefaultOrg) plus
	// the always-present `default-org` bucket. This covers the common
	// EPIC-2 chroot case (one Org per Sovereign) without forcing the
	// caller to know the org slug. Future enhancement: walk the Gitea
	// admin /orgs endpoint when slice L promotes that surface.
	//
	// qa-loop iter-1 prefetch Fix #92 (TC-082): a publish followed
	// immediately by a curatable list MUST surface the just-published
	// bp-* in the items envelope; the prior empty-list-on-no-query
	// behaviour silently filtered it out.
	ctx := r.Context()
	rawOrgs := strings.TrimSpace(r.URL.Query().Get("orgs"))
	var orgs []string
	if rawOrgs != "" {
		orgs = strings.Split(rawOrgs, ",")
	} else {
		orgs = []string{}
		if dep := strings.TrimSpace(h.deploymentDefaultOrg(depID)); dep != "" {
			orgs = append(orgs, dep)
		}
		orgs = append(orgs, "default-org")
	}
	out := make([]curatableBlueprint, 0)
	for _, orgRaw := range orgs {
		org := strings.TrimSpace(orgRaw)
		if org == "" {
			continue
		}
		entries, err := h.giteaClient.ListContents(ctx, org, sharedBlueprintsRepo, blueprintBranch, "")
		if err != nil {
			// Skip orgs that don't have a shared-blueprints repo.
			continue
		}
		for _, e := range entries {
			if !strings.EqualFold(e.Type, "dir") {
				continue
			}
			bpName := e.Name
			if !strings.HasPrefix(bpName, "bp-") {
				continue
			}
			// Read the blueprint.yaml for the version + title.
			f, ferr := h.giteaClient.GetFile(ctx, org, sharedBlueprintsRepo, blueprintBranch, bpName+"/blueprint.yaml")
			if ferr != nil {
				continue
			}
			fBytes, decErr := f.Decoded()
			if decErr != nil {
				continue
			}
			version, title := parseBlueprintMeta(fBytes)
			out = append(out, curatableBlueprint{
				Org:     org,
				Name:    bpName,
				Version: version,
				Title:   title,
			})
		}
	}
	writeJSON(w, http.StatusOK, curatableBlueprintsResponse{Items: out})
}

// ── Edit-PR (slice Z3 follow-up) ────────────────────────────────────

// blueprintEditPRRequest — body of POST /api/v1/sovereigns/{id}/blueprints/edit-pr.
// The YamlEditor widget posts it for a flux-managed resource; the handler
// commits the content to `main` of the per-Org shared-blueprints repo.
type blueprintEditPRRequest struct {
	Org     string `json:"org"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Message string `json:"message,omitempty"`
	Title   string `json:"title,omitempty"`
}

// blueprintEditPRResponse — body returned on 200.
//
// `PR` mirrors `PRNumber` + `PRURL` under a flat `pr` envelope so the
// qa-loop matrix (TC-085 must_contain ['pr','number']) and the UI's
// older /pr/{number} pickers see the same data without alias-following.
type blueprintEditPRResponse struct {
	PRURL    string             `json:"prURL"`
	PRNumber int64              `json:"prNumber"`
	PR       blueprintEditPRRef `json:"pr"`
	Branch   string             `json:"branch"`
	Repo     string             `json:"repo"`
	Path     string             `json:"path"`
	Message  string             `json:"message"`
}

// blueprintEditPRRef carries (number, url) under the canonical `pr`
// envelope so the matrix can read both tokens via a single field
// rather than two flat siblings.
type blueprintEditPRRef struct {
	Number int64  `json:"number"`
	URL    string `json:"url"`
}

// HandleBlueprintEditPR — POST /api/v1/sovereigns/{id}/blueprints/edit-pr
//
// Commits the supplied path directly to `main` of `<org>/shared-blueprints`,
// as publish does. Auth: tier-admin or higher (mirrors /blueprints/publish).
// The response keeps the former field names: prURL and pr.url carry the
// commit address, prNumber and pr.number are 0, branch is `main`.
func (h *Handler) HandleBlueprintEditPR(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return
	}
	if claims := auth.ClaimsFromContext(r.Context()); claims != nil {
		if !applicationInstallCallerAuthorized(claims) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "forbidden",
				"detail": "POST /blueprints/edit-pr requires tier-admin or higher",
			})
			return
		}
	}
	gw := catalogWriterOf(h.giteaClient)
	if gw == nil {
		writeGitUnavailable(w)
		return
	}

	// Dual-shape decode (qa-loop iter-15 Fix #58): accept BOTH the
	// canonical {"org","path","content"} shape AND the simplified
	// {"name","diff"} shape the qa-loop matrix uses. See
	// blueprints_wire_compat.go.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeBlueprintEditPRBody(rawBody, h.deploymentDefaultOrg(depID))
	if decodeErr != nil {
		writeBadRequest(w, "invalid-body", decodeErr.Error())
		return
	}
	if msg, ok := validateBlueprintEditPRRequest(body); !ok {
		writeBadRequest(w, "invalid-edit-pr", msg)
		return
	}

	commitMsg := strings.TrimSpace(body.Message)
	if commitMsg == "" {
		commitMsg = strings.TrimSpace(body.Title)
	}
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("Edit %s via Catalyst", body.Path)
	}
	_, sha, err := commitCatalogFile(r.Context(), gw, gitwrite.Repo{Owner: body.Org, Name: sharedBlueprintsRepo, Branch: blueprintBranch},
		body.Path, []byte(body.Content), commitMsg, gitwrite.Identity{})
	if err != nil {
		if errors.Is(err, gitwrite.ErrNotFound) {
			writeNotFound(w, fmt.Sprintf("%s/%s", body.Org, sharedBlueprintsRepo))
			return
		}
		h.log.Warn("blueprint edit-pr: commit failed", "org", body.Org, "path", body.Path, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "gitea-upstream",
			"detail": fmt.Sprintf("commit %s: %v", body.Path, err),
		})
		return
	}

	commitURL := fmt.Sprintf("/%s/%s/commit/%s", body.Org, sharedBlueprintsRepo, sha)
	writeJSON(w, http.StatusOK, blueprintEditPRResponse{
		PRURL:   commitURL,
		PR:      blueprintEditPRRef{URL: commitURL},
		Branch:  blueprintBranch,
		Repo:    sharedBlueprintsRepo,
		Path:    body.Path,
		Message: fmt.Sprintf("committed %s to %s", sha, blueprintBranch),
	})
}

// validateBlueprintEditPRRequest — invariants the handler expects.
func validateBlueprintEditPRRequest(req blueprintEditPRRequest) (string, bool) {
	if strings.TrimSpace(req.Org) == "" {
		return "org is required", false
	}
	if !isValidK8sName(req.Org) {
		return "org must be a valid K8s name (RFC 1123)", false
	}
	if strings.TrimSpace(req.Path) == "" {
		return "path is required", false
	}
	if strings.Contains(req.Path, "..") {
		return "path must not contain `..`", false
	}
	if strings.HasPrefix(req.Path, "/") {
		return "path must be repo-relative (no leading `/`)", false
	}
	if strings.TrimSpace(req.Content) == "" {
		return "content is required", false
	}
	if len(req.Content) > 1024*1024 {
		return "content exceeds 1 MiB", false
	}
	return "", true
}

// ── Validation + helpers ────────────────────────────────────────────

func validateBlueprintPublishRequest(req blueprintPublishRequest) (string, bool) {
	if strings.TrimSpace(req.Org) == "" {
		return "org is required", false
	}
	if !isValidK8sName(req.Org) {
		return "org must be a valid K8s name (RFC 1123)", false
	}
	if strings.TrimSpace(req.Name) == "" {
		return "name is required", false
	}
	if !strings.HasPrefix(req.Name, "bp-") {
		return "name must be of the form bp-<slug>", false
	}
	if !isValidK8sName(req.Name) {
		return "name must be a valid K8s name", false
	}
	if strings.TrimSpace(req.Version) == "" {
		return "version is required", false
	}
	// Loose semver check: first chunk before any `+` or `-` must be 3
	// dotted numbers. Per BLUEPRINT-AUTHORING.md §3 versions are semver.
	if !looksLikeSemver(req.Version) {
		return "version must be a semver string (e.g. 1.2.3)", false
	}
	if strings.TrimSpace(req.BlueprintYAML) == "" {
		return "blueprintYaml is required", false
	}
	if len(req.BlueprintYAML) > 1024*1024 {
		return "blueprintYaml exceeds 1 MiB; chartTarball is the path for chart contents", false
	}
	return "", true
}

// validateBlueprintYAML parses the supplied YAML and asserts the
// canonical Blueprint shape. Defense-in-depth against publishing a
// random YAML; the blueprint-controller does deeper schema validation
// downstream.
func validateBlueprintYAML(req blueprintPublishRequest) (string, bool) {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal([]byte(req.BlueprintYAML), &doc); err != nil {
		return fmt.Sprintf("blueprintYaml parse: %v", err), false
	}
	apiVersion, _ := doc["apiVersion"].(string)
	if apiVersion == "" {
		return "blueprint.yaml missing apiVersion", false
	}
	kind, _ := doc["kind"].(string)
	if kind != "Blueprint" {
		return fmt.Sprintf("blueprint.yaml kind=%q; want Blueprint", kind), false
	}
	meta, _ := doc["metadata"].(map[string]interface{})
	if meta == nil {
		return "blueprint.yaml missing metadata", false
	}
	metaName, _ := meta["name"].(string)
	if metaName != req.Name {
		return fmt.Sprintf("blueprint.yaml metadata.name=%q does not match request name=%q", metaName, req.Name), false
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		return "blueprint.yaml missing spec", false
	}
	specVersion, _ := spec["version"].(string)
	if specVersion != req.Version {
		return fmt.Sprintf("blueprint.yaml spec.version=%q does not match request version=%q", specVersion, req.Version), false
	}
	return "", true
}

// parseBlueprintMeta extracts (version, card.title) from a blueprint.yaml
// byte stream. Returns ("","") on parse failure (caller decides what to
// do).
func parseBlueprintMeta(raw []byte) (version, title string) {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		return "", ""
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		return "", ""
	}
	if v, ok := spec["version"].(string); ok {
		version = v
	}
	if card, ok := spec["card"].(map[string]interface{}); ok {
		if t, ok := card["title"].(string); ok {
			title = t
		}
	}
	return version, title
}

// looksLikeSemver — first three dotted numbers must be integers.
// Doesn't enforce full semver grammar (the blueprint-controller does);
// just rejects obvious typos like "v1" or "latest".
func looksLikeSemver(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	// Strip pre-release / build suffix.
	for _, sep := range []string{"+", "-"} {
		if i := strings.Index(v, sep); i >= 0 {
			v = v[:i]
		}
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}
	return true
}

// jsonRoundTrip is a tiny utility used by tests to recover a typed
// response shape without re-importing the per-test struct.
func jsonRoundTrip(in any, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// Compile-time assertion that gitea.Client satisfies our narrow
// interface.
var _ GiteaBlueprintClient = (*gitea.Client)(nil)
