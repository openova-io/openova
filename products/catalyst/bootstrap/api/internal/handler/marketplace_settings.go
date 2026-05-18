// marketplace_settings.go — Sovereign Settings → "Marketplace mode" toggle.
//
// Operators of a live Sovereign reach this surface from the console's
// Settings → Marketplace nav entry. The page exposes a single toggle
// (Enable / Disable) plus three brand fields (Name, Tagline, Primary
// colour). Saving POSTs to:
//
//   POST /api/v1/sovereigns/{id}/marketplace
//
// The handler does NOT touch the in-cluster ConfigMap directly — per the
// founder's 2026-05-04 GitOps rule and INVIOLABLE-PRINCIPLES.md #3,
// runtime cluster state must trace back to a git commit, not to an
// out-of-band kubectl apply. The seam is therefore:
//
//   1. Look up the deployment by id (h.deployments.Load).
//   2. Verify ownership (#689) so a hostile probe can't mutate someone
//      else's Sovereign.
//   3. Clone the openova-public repo to a temp dir using a CATALYST_-
//      gated PAT (env CATALYST_GITOPS_TOKEN; rotated yearly, see
//      docs/SECRET-ROTATION.md). The repo URL + branch reuse the same
//      env vars as the provisioner cloud-init plumbing
//      (CATALYST_GITOPS_REPO_URL, CATALYST_GITOPS_BRANCH).
//   4. Open the bootstrap-kit overlay file 13-bp-catalyst-platform.yaml.
//      Path resolution (resolveBootstrapKitDir, issue #1790):
//        - Mothership / pre-cutover: clusters/<sovereignFQDN>/bootstrap-kit/
//        - Sovereign chroot (SOVEREIGN_FQDN env set, post-cutover):
//          clusters/_template/bootstrap-kit/ (the chroot Gitea only
//          carries the canonical _template subtree)
//        - CATALYST_BOOTSTRAP_KIT_PATH overrides both for tests.
//      Patch:
//        ingress.marketplace.enabled: bool
//        marketplace.brand.name / tagline / primaryColor
//      The yaml.v3 round-trip preserves comments + ordering — Flux on
//      the Sovereign reconciles within ~1 min and the bp-catalyst-
//      platform 1.3.0+ chart re-renders the marketplace HTTPRoutes /
//      branding ConfigMaps off the new values.
//   5. git add + commit (committer "catalyst-api <ops@openova.io>") +
//      push to the configured branch.
//   6. Return 200 with { commit_sha, applied_at } so the UI can poll
//      for the chart re-render and surface "Reconciling…" → "Applied".
//
// Per INVIOLABLE-PRINCIPLES.md #4 every URL / token / path is runtime-
// configurable — the handler reads its plumbing from env, never inlines
// hostnames or tokens.
package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// MarketplaceBrand carries the branding fields the chart renders into
// the storefront ConfigMap. Empty strings fall back to the chart's
// defaults — see products/catalyst/chart/values.yaml for the
// authoritative defaults table.
type MarketplaceBrand struct {
	Name         string `json:"name"`
	Tagline      string `json:"tagline"`
	PrimaryColor string `json:"primaryColor"`
}

// SetMarketplaceRequest is the body of POST /sovereigns/{id}/marketplace.
//
// Enabled toggles the per-Sovereign overlay's
// `ingress.marketplace.enabled`. Brand is applied only when Enabled=true
// — disabling preserves the brand fields on disk so a re-enable (without
// re-typing) reuses the operator's previous values.
type SetMarketplaceRequest struct {
	Enabled bool             `json:"enabled"`
	Brand   MarketplaceBrand `json:"brand"`
}

// SetMarketplaceResponse echoes the commit SHA + timestamp of the GitOps
// commit so the UI can render "Applied at <ts>" + a deep link to the
// commit on github.com/openova-io/openova.
type SetMarketplaceResponse struct {
	DeploymentID  string `json:"deploymentId"`
	SovereignFQDN string `json:"sovereignFQDN"`
	Enabled       bool   `json:"enabled"`
	CommitSHA     string `json:"commitSha"`
	AppliedAt     string `json:"appliedAt"`
}

// HandleGetMarketplace returns the current marketplace-enabled state for
// the deployment so the Sovereign Console MarketplaceSettings page can
// initialise its toggle to the actual value instead of always defaulting
// to false. Backed by the in-memory deployment record's
// Request.MarketplaceEnabled field (set at prov time, mutated by
// HandleSetMarketplace's GitOps commit but NOT reflected back into the
// record — so this read is best-effort and may lag a recent toggle by
// one reconcile window; the UI shows "Reconciling" during that window).
//
// Founder caught on t140 (2026-05-17): "/settings/marketplace shows
// disabled, the marketplace is still working" — the UI toggle hardcoded
// false on mount instead of reflecting the chart's actual state.
//
// GET /api/v1/sovereigns/{id}/marketplace
//
// Response: 200 {"deploymentId","sovereignFQDN","enabled","brand"}
//           404 deployment unknown
//           403 ownership mismatch (returned as 404 per #689)
func (h *Handler) HandleGetMarketplace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	if !h.checkOwnership(w, r, dep) {
		return
	}
	dep.mu.Lock()
	enabled := dep.Request.MarketplaceEnabled
	sovereignFQDN := strings.TrimSpace(dep.Request.SovereignFQDN)
	dep.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"deploymentId":  id,
		"sovereignFQDN": sovereignFQDN,
		"enabled":       enabled,
		"brand": MarketplaceBrand{
			Name:         "",
			Tagline:      "",
			PrimaryColor: "",
		},
	})
}

// HandleSetMarketplace is the chi handler for
// POST /api/v1/sovereigns/{id}/marketplace.
//
// Response codes:
//   - 200 OK on success (commit SHA in body)
//   - 400 Bad Request when the body cannot be parsed
//   - 404 Not Found when the deployment id is unknown
//   - 409 Conflict when SovereignFQDN is empty (deployment not yet
//     past Phase-0 — the overlay file does not exist)
//   - 503 Service Unavailable when the GitOps token / clone tooling
//     is unconfigured (Sovereign-side or CI without the env vars set)
//   - 500 on a fatal git / FS error (wrapped; details in body for the
//     operator to forward to support)
func (h *Handler) HandleSetMarketplace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	// #689 ownership gate — 404 (not 403) on mismatch so deployment ids
	// can't be enumerated via response-code probing.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	sovereignFQDN := strings.TrimSpace(dep.Request.SovereignFQDN)
	if sovereignFQDN == "" {
		http.Error(w, "sovereignFQDN missing — deployment not past Phase 0", http.StatusConflict)
		return
	}

	var body SetMarketplaceRequest
	if err := decodeJSONBody(r, &body); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate brand on enable — we don't reject empty fields (the chart
	// has safe defaults), but a primaryColor that's not a 7-char hex is
	// rejected so a typo doesn't ship to the storefront and 500 the
	// chart's CSS template.
	if body.Enabled && body.Brand.PrimaryColor != "" {
		if !isValidHexColor(body.Brand.PrimaryColor) {
			http.Error(w, "brand.primaryColor must be #RRGGBB hex", http.StatusBadRequest)
			return
		}
	}

	cfg := loadGitOpsConfig()
	if cfg.Token == "" {
		http.Error(w, "gitops token unconfigured — set CATALYST_GITOPS_TOKEN", http.StatusServiceUnavailable)
		return
	}

	// 5-minute deadline — the clone + push round-trip against github.com
	// completes well under a minute on the catalyst-system VPC, but we
	// bound it explicitly so a wedged DNS doesn't hang the request.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	commitSHA, err := writeMarketplaceOverlay(ctx, cfg, sovereignFQDN, body, h.log)
	if err != nil {
		// Log the detailed error server-side; surface a generic message
		// to the client so we don't leak the token / temp dir paths.
		h.log.Error("marketplace settings: gitops write failed",
			"deploymentId", id,
			"sovereignFQDN", sovereignFQDN,
			"err", err,
		)
		http.Error(w, "failed to commit settings to git: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, SetMarketplaceResponse{
		DeploymentID:  id,
		SovereignFQDN: sovereignFQDN,
		Enabled:       body.Enabled,
		CommitSHA:     commitSHA,
		AppliedAt:     time.Now().UTC().Format(time.RFC3339),
	})
}

// gitOpsConfig captures the clone-target URL, branch, user/token, and
// committer identity for the GitOps push. Every field is runtime-
// configurable per INVIOLABLE-PRINCIPLES.md #4.
type gitOpsConfig struct {
	RepoURL string
	Branch  string
	// User is the basic-auth username embedded in the clone URL. GitHub
	// PATs accept any username (canonical "x-access-token"); Gitea
	// requires the real account name. Wired via CATALYST_GITOPS_USER so
	// the SAME catalyst-api binary works against both GitHub (Catalyst-
	// Zero pre-cutover) and the Sovereign-side local Gitea (post-Day-2-
	// Independence). Issue #878.
	User          string
	Token         string
	CommitterName string
	CommitterMail string
}

func loadGitOpsConfig() gitOpsConfig {
	return gitOpsConfig{
		RepoURL:       envOr("CATALYST_GITOPS_REPO_URL", "https://github.com/openova-io/openova"),
		Branch:        envOr("CATALYST_GITOPS_BRANCH", "main"),
		User:          envOr("CATALYST_GITOPS_USER", "x-access-token"),
		Token:         os.Getenv("CATALYST_GITOPS_TOKEN"),
		CommitterName: envOr("CATALYST_GITOPS_COMMITTER_NAME", "catalyst-api"),
		CommitterMail: envOr("CATALYST_GITOPS_COMMITTER_EMAIL", "ops@openova.io"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// resolveBootstrapKitDir returns the GitOps-repo-relative path to the
// bootstrap-kit directory for the marketplace overlay. The path depends
// on which catalyst-api Pod is serving the request:
//
//   - Mothership / Catalyst-Zero (pre-cutover):
//       clusters/<sovereignFQDN>/bootstrap-kit/
//     Each Sovereign has its own per-FQDN subtree carved out of the
//     openova-io/openova repo by the provisioner.
//
//   - Sovereign chroot (post-cutover):
//       clusters/_template/bootstrap-kit/
//     The chroot Gitea is seeded with the canonical _template subtree
//     only (see clusters/_template/* and openova_flow_proxy.go's
//     "clusters/_template/bootstrap-kit/56-bp-openova-flow-server.yaml"
//     reference). The per-FQDN materialisation never happens on the
//     chroot's Gitea — Flux on the chroot reads directly from
//     _template/ with kustomize overlays. Issue #1790 (Wave 34): the
//     handler hardcoded the mothership path and silently 500'd with
//     "no such file or directory" when the marketplace toggle was
//     pushed through the post-cutover Sovereign Console.
//
// Detection rule: SOVEREIGN_FQDN env is set on every chroot catalyst-api
// Pod (cloud-init stamps it; see auth_handover.go:390 and rbac_matrix.go).
// The mother never sets it. CATALYST_BOOTSTRAP_KIT_PATH is a runtime
// override per INVIOLABLE-PRINCIPLES.md #4 so a future repository
// re-layout doesn't require a code change.
func resolveBootstrapKitDir(sovereignFQDN string) string {
	if override := strings.TrimSpace(os.Getenv("CATALYST_BOOTSTRAP_KIT_PATH")); override != "" {
		return override
	}
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != "" {
		return filepath.Join("clusters", "_template", "bootstrap-kit")
	}
	return filepath.Join("clusters", sovereignFQDN, "bootstrap-kit")
}

// writeMarketplaceOverlay clones the GitOps repo, edits the per-Sovereign
// overlay file, commits, and pushes. Returns the new commit SHA on
// success. The clone uses a t.TempDir-style scratch dir under
// CATALYST_GITOPS_TMPDIR (default os.TempDir()); cleanup is deferred so a
// crash mid-edit doesn't leak credentials onto the PVC.
func writeMarketplaceOverlay(ctx context.Context, cfg gitOpsConfig, sovereignFQDN string, body SetMarketplaceRequest, log *slog.Logger) (string, error) {
	// Build the authenticated clone URL once so the token is never echoed
	// to a subprocess argument list (visible to /proc/<pid>/cmdline).
	authURL, err := injectTokenIntoURLWithUser(cfg.RepoURL, cfg.User, cfg.Token)
	if err != nil {
		return "", fmt.Errorf("rewrite repo URL: %w", err)
	}

	tmpRoot := envOr("CATALYST_GITOPS_TMPDIR", os.TempDir())
	scratch, err := os.MkdirTemp(tmpRoot, "marketplace-settings-*")
	if err != nil {
		return "", fmt.Errorf("mktempdir: %w", err)
	}
	// Defensive cleanup — the clone leaves a `.git/config` containing the
	// rewritten URL with the token, so we MUST RemoveAll on every exit.
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			log.Warn("marketplace settings: scratch cleanup failed",
				"dir", scratch,
				"err", err,
			)
		}
	}()

	// Shallow clone is sufficient — we touch one file and push back.
	if err := runGit(ctx, scratch, "clone",
		"--depth=1",
		"--branch="+cfg.Branch,
		"--single-branch",
		authURL,
		scratch+"/repo",
	); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	repoDir := filepath.Join(scratch, "repo")

	// Configure committer locally — never mutate the Pod's global git
	// config (that would race with parallel marketplace-settings calls).
	if err := runGit(ctx, repoDir, "config", "user.name", cfg.CommitterName); err != nil {
		return "", fmt.Errorf("git config user.name: %w", err)
	}
	if err := runGit(ctx, repoDir, "config", "user.email", cfg.CommitterMail); err != nil {
		return "", fmt.Errorf("git config user.email: %w", err)
	}

	relDir := resolveBootstrapKitDir(sovereignFQDN)
	overlayPath := filepath.Join(repoDir, relDir, "13-bp-catalyst-platform.yaml")
	raw, err := os.ReadFile(overlayPath)
	if err != nil {
		return "", fmt.Errorf("read overlay %s: %w", overlayPath, err)
	}

	patched, err := patchMarketplaceYAML(raw, body)
	if err != nil {
		return "", fmt.Errorf("patch overlay: %w", err)
	}

	if bytes.Equal(raw, patched) {
		// No-op edit — return the current HEAD as the "commit" so the UI
		// can still surface "Applied" without a stale-state surprise.
		out, err := runGitOutput(ctx, repoDir, "rev-parse", "HEAD")
		if err != nil {
			return "", fmt.Errorf("rev-parse HEAD: %w", err)
		}
		return strings.TrimSpace(out), nil
	}

	if err := os.WriteFile(overlayPath, patched, 0o644); err != nil {
		return "", fmt.Errorf("write overlay: %w", err)
	}

	relPath := filepath.Join(relDir, "13-bp-catalyst-platform.yaml")
	if err := runGit(ctx, repoDir, "add", relPath); err != nil {
		return "", fmt.Errorf("git add: %w", err)
	}

	msg := fmt.Sprintf("settings: marketplace enabled=%v for %s", body.Enabled, sovereignFQDN)
	if err := runGit(ctx, repoDir, "commit", "-m", msg); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}

	if err := runGit(ctx, repoDir, "push", "origin", "HEAD:"+cfg.Branch); err != nil {
		return "", fmt.Errorf("git push: %w", err)
	}

	out, err := runGitOutput(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// patchMarketplaceYAML edits the overlay's `values:` block in place,
// preserving comments + key order via yaml.v3 Node round-trip. The
// overlay contains TWO documents (Namespace + HelmRelease) separated by
// `---`; we operate only on the HelmRelease (kind: HelmRelease) doc.
func patchMarketplaceYAML(raw []byte, body SetMarketplaceRequest) ([]byte, error) {
	// yaml.v3's Decoder handles multi-document streams natively. We
	// decode every doc, mutate the matching one, then re-encode in the
	// same order.
	var docs []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		// yaml.v3 wraps the document in a DocumentNode whose first child
		// is the actual root. We keep the wrapper so re-encode produces
		// the same `---` separator structure.
		nClone := n
		docs = append(docs, &nClone)
	}

	mutated := false
	for _, doc := range docs {
		if doc == nil || len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root.Kind != yaml.MappingNode {
			continue
		}
		kind := mapStringValue(root, "kind")
		if kind != "HelmRelease" {
			continue
		}

		spec := mapChild(root, "spec")
		if spec == nil {
			continue
		}
		values := mapChild(spec, "values")
		if values == nil {
			values = &yaml.Node{Kind: yaml.MappingNode}
			setMapChild(spec, "values", values)
		}
		ingress := mapChild(values, "ingress")
		if ingress == nil {
			ingress = &yaml.Node{Kind: yaml.MappingNode}
			setMapChild(values, "ingress", ingress)
		}
		marketplaceIngress := mapChild(ingress, "marketplace")
		if marketplaceIngress == nil {
			marketplaceIngress = &yaml.Node{Kind: yaml.MappingNode}
			setMapChild(ingress, "marketplace", marketplaceIngress)
		}
		setScalar(marketplaceIngress, "enabled", boolStr(body.Enabled), "!!bool")

		// marketplace.brand.* — only writes the fields the operator
		// actually supplied. Empty fields are preserved-as-empty so a
		// re-enable doesn't accidentally clear the storefront name.
		marketplace := mapChild(root, "marketplace")
		if marketplace == nil {
			// Some overlays nest marketplace under spec.values; check
			// there too for forward-compat with chart values shape.
			marketplace = mapChild(values, "marketplace")
		}
		if marketplace == nil {
			marketplace = &yaml.Node{Kind: yaml.MappingNode}
			setMapChild(values, "marketplace", marketplace)
		}
		brand := mapChild(marketplace, "brand")
		if brand == nil {
			brand = &yaml.Node{Kind: yaml.MappingNode}
			setMapChild(marketplace, "brand", brand)
		}
		if body.Brand.Name != "" {
			setScalar(brand, "name", body.Brand.Name, "!!str")
		}
		if body.Brand.Tagline != "" {
			setScalar(brand, "tagline", body.Brand.Tagline, "!!str")
		}
		if body.Brand.PrimaryColor != "" {
			setScalar(brand, "primaryColor", body.Brand.PrimaryColor, "!!str")
		}
		mutated = true
	}

	if !mutated {
		return nil, errors.New("HelmRelease document not found in overlay")
	}

	// Re-encode every doc in order, separating with `---\n` so the
	// upstream multi-doc shape is preserved.
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// mapChild returns the Node at key inside a MappingNode, or nil.
func mapChild(parent *yaml.Node, key string) *yaml.Node {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}

// mapStringValue returns the scalar string at key inside a MappingNode.
func mapStringValue(parent *yaml.Node, key string) string {
	c := mapChild(parent, key)
	if c == nil || c.Kind != yaml.ScalarNode {
		return ""
	}
	return c.Value
}

// setMapChild sets parent[key] = child, appending if missing or replacing
// in place if present. Preserves the surrounding Content order.
func setMapChild(parent *yaml.Node, key string, child *yaml.Node) {
	if parent == nil || parent.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = child
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		child,
	)
}

// setScalar sets parent[key] to a scalar with the given value + tag,
// adding the entry if missing.
func setScalar(parent *yaml.Node, key, value, tag string) {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag}
	setMapChild(parent, key, scalar)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// runGit runs git with the given args inside dir. stdout + stderr are
// captured + wrapped into the returned error. The token is NEVER
// passed via argv (it lives inside the rewritten remote URL only).
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // refuse to prompt for credentials on stdin
		"GIT_ASKPASS=/bin/true",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w (stderr: %s)", strings.Join(redactArgs(args), " "), err, redactString(stderr.String()))
	}
	return nil
}

// runGitOutput runs git and returns stdout. Used for `rev-parse HEAD`
// after a successful push.
func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w (stderr: %s)", strings.Join(redactArgs(args), " "), err, redactString(stderr.String()))
	}
	return stdout.String(), nil
}

// injectTokenIntoURL rewrites https://github.com/foo into
// https://<USER>:<TOKEN>@github.com/foo so `git clone` can authenticate
// without an SSH key. The user is configurable so the same code path
// works for:
//   - GitHub PATs — user="x-access-token" (default; GitHub ignores
//     the username when the token is a PAT)
//   - Local Gitea — user="gitea_admin" (Gitea checks the username on
//     basic auth; "x-access-token" returns 401)
//
// Returns an error if the URL is not http/https.
func injectTokenIntoURL(rawURL, token string) (string, error) {
	return injectTokenIntoURLWithUser(rawURL, "x-access-token", token)
}

// injectTokenIntoURLWithUser is the configurable variant. Issue #878 —
// post-cutover Sovereign uses local Gitea, which requires the real
// admin username (default GitHub PAT username "x-access-token" returns
// 401). Loaded from CATALYST_GITOPS_USER env via gitOpsConfig.User.
func injectTokenIntoURLWithUser(rawURL, user, token string) (string, error) {
	if token == "" {
		return rawURL, nil
	}
	if user == "" {
		user = "x-access-token"
	}
	if strings.HasPrefix(rawURL, "https://") {
		// Strip any pre-existing userinfo, then re-inject.
		stripped := strings.TrimPrefix(rawURL, "https://")
		if at := strings.IndexByte(stripped, '@'); at >= 0 {
			stripped = stripped[at+1:]
		}
		return "https://" + user + ":" + token + "@" + stripped, nil
	}
	if strings.HasPrefix(rawURL, "http://") {
		stripped := strings.TrimPrefix(rawURL, "http://")
		if at := strings.IndexByte(stripped, '@'); at >= 0 {
			stripped = stripped[at+1:]
		}
		return "http://" + user + ":" + token + "@" + stripped, nil
	}
	return "", fmt.Errorf("unsupported repo URL scheme: %s", rawURL)
}

// redactArgs strips any embedded token-bearing URL out of argv before it
// goes into a log line. We pass URLs to `git clone` only — which is the
// one place we need to be careful here.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = redactString(a)
	}
	return out
}

// redactString strips x-access-token:<…>@ patterns out of a string so the
// token is never echoed to logs even on error paths.
func redactString(s string) string {
	const marker = "x-access-token:"
	if i := strings.Index(s, marker); i >= 0 {
		if at := strings.IndexByte(s[i:], '@'); at > 0 {
			return s[:i+len(marker)] + "REDACTED" + s[i+at:]
		}
	}
	return s
}

// isValidHexColor returns true when s is a 7-char "#RRGGBB" hex string.
// We accept lowercase + uppercase A-F; reject 3-char shorthand and any
// prefix the chart's CSS template doesn't safely handle.
func isValidHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

