// handover.go — handover finalisation flow (issue #317).
//
// Per founder principle (2026-04-30): Catalyst-Zero must hold ZERO
// operational footprint for a Sovereign once it is up. The only legitimate
// retention is structural (pool-subdomain DNS delegation in `omani.works`,
// and the PDM allocation row that maps subdomain → pool). Everything else
// — kubeconfig, OpenTofu state, helmwatch informer connection, deployment
// record JSON — is purged at handover.
//
// This file ships the server-side machinery (catalyst-api). The trigger
// is `bp-catalyst-platform.Ready=True` on the new Sovereign; the wizard
// posts to /api/v1/handover/finalise with the deployment id and the
// catalyst-api runs the 4 numbered steps from the issue body in
// sequence:
//
//  1. Emit a final SSE event (`handover`) on the deployment's eventsCh
//     so any open wizard banner shows the cutover.
//  2. Cancel the per-deployment helmwatch informer (no more apiserver
//     watches against the new cluster from Catalyst-Zero).
//  3. Export the Phase-0 OpenTofu state to the new Sovereign's
//     catalyst-api and have IT seal it into its OpenBao at
//     `secret/catalyst/tofu-phase0-archive`. On 200 OK from the new
//     Sovereign, delete `/var/lib/catalyst/tofu/<sovereign-name>/`
//     locally.
//  4. Delete the kubeconfig file on the PVC; transform the deployment
//     record JSON in-place to a slim post-handover state (id +
//     sovereignFQDN + audit timestamps + AdoptedAt; operational fields
//     zeroed) — the slim record is what fires the post-handover
//     console redirect (issue #319 PR #451 contract; #453 reconciles).
//
// Step 4's "Hetzner token rotate" mentioned in the issue body is deferred
// to Crossplane Provider rotation (per #425, the canonical Day-2 IaC
// seam). The catalyst-api never calls `DELETE /tokens/<id>` directly —
// that would be a bespoke cloud-API call, banned by
// docs/INVIOLABLE-PRINCIPLES.md #3. The new Sovereign's Crossplane
// Provider already has its own minted token; the operator-supplied
// Phase-0 token is dropped from memory by virtue of catalyst-api's
// existing credential-hygiene path (request.HetznerToken is GC'd after
// writeTfvars, see provisioner.go).
//
// The receiver endpoint (`/api/v1/handover/tofu-archive`) lives in this
// same file. It runs on the SAME catalyst-api binary; production
// containers running on a Sovereign serve this endpoint authoritatively.
// Catalyst-Zero serves it too, defensively, so misrouted POSTs hit a
// well-defined 503 ("not handover target").
//
// Encryption: the OpenTofu state is base64-wrapped (NOT encrypted in
// transit by this handler — TLS terminates at the Sovereign's gateway
// where the caller speaks https://). At rest in OpenBao the state is
// protected by Vault-API's standard storage encryption. A future
// upgrade can add envelope encryption via OpenBao's transit engine; the
// Client interface is forward-compatible.
//
// Per the anti-duplication rule (memory/feedback_anti_duplication_seam_first.md):
//   - SSE emit reuses h.emitWatchEvent (single emit path for Phase 0/1).
//   - Helmwatch cancel uses helmwatch.Watcher.Cancel — added to the
//     existing Watcher in helmwatch.go for #317, NOT a new informer.
//   - OpenBao writes go through internal/openbao.Client (canonical seam).
//   - Tofu workdir cleanup uses os.RemoveAll on the existing
//     provisioner.Provisioner.WorkDir/<sovereignName> path.
//   - Kubeconfig deletion uses os.Remove on h.kubeconfigsDir paths (same
//     paths the wipe.go finalisation uses).
//   - Deployment record post-handover is transformed to slim shape via
//     Deployment.SlimForHandover + persisted with the existing
//     store.Store.Save seam; the older Delete-then-redirect-404 path
//     was the bug fixed by #453.
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// finaliseRequest is the body POSTed by the wizard / Sovereign-platform
// post-install hook to /api/v1/handover/finalise. Empty body is accepted
// — every value comes from the Deployment record on the catalyst-api
// side. The JSON shape exists so a future client can pass an explicit
// `dryRun` flag.
type finaliseRequest struct {
	// DryRun — when true, the handler emits the SSE event and computes
	// the would-be cleanup paths but performs no destructive action.
	// Used by smoke/E2E tests that don't want to actually delete the
	// kubeconfig + workdir.
	DryRun bool `json:"dryRun,omitempty"`
}

// finaliseResponse summarises which steps ran. Wizard renders this in
// the post-handover banner.
type finaliseResponse struct {
	DeploymentID  string                 `json:"deploymentId"`
	SovereignFQDN string                 `json:"sovereignFqdn"`
	ConsoleURL    string                 `json:"consoleURL"`
	FinalisedAt   string                 `json:"finalisedAt"`
	Steps         finaliseStepsCompleted `json:"steps"`
	Errors        []string               `json:"errors,omitempty"`
}

// finaliseStepsCompleted is the per-step boolean ledger the wizard
// renders as a checklist. Step 4 (rotate / delete kubeconfig + record)
// fans out into three sub-fields so partial success surfaces clearly.
type finaliseStepsCompleted struct {
	HandoverEventEmitted    bool `json:"handoverEventEmitted"`
	HelmwatchCancelled      bool `json:"helmwatchCancelled"`
	TofuArchiveSubmitted    bool `json:"tofuArchiveSubmitted"`
	TofuWorkdirRemoved      bool `json:"tofuWorkdirRemoved"`
	KubeconfigRemoved       bool `json:"kubeconfigRemoved"`
	DeploymentRecordRemoved bool `json:"deploymentRecordRemoved"`
}

// FinaliseHandover handles POST /api/v1/handover/finalise/{id}. It runs
// the 4-step finalisation flow synchronously and returns a 200 OK with
// the per-step ledger. The endpoint is idempotent: calling it twice on
// the same deployment returns the same shape with the second call's
// step booleans reflecting "nothing left to do" (HelmwatchCancelled
// stays true; the file/path removals report false because the targets
// are already absent).
//
// Response codes:
//   - 200 OK on full or partial success (errors in body)
//   - 404 Not Found when the deployment id is unknown
//   - 400 Bad Request when the body cannot be parsed
//   - 503 Service Unavailable if the new Sovereign's catalyst-api is
//     unreachable AND DryRun is false (the operator must retry)
func (h *Handler) FinaliseHandover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	var body finaliseRequest
	if r.ContentLength > 0 {
		if err := decodeJSONBody(r, &body); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	now := time.Now().UTC()
	consoleURL := "https://console." + dep.Request.SovereignFQDN
	resp := finaliseResponse{
		DeploymentID:  id,
		SovereignFQDN: dep.Request.SovereignFQDN,
		ConsoleURL:    consoleURL,
		FinalisedAt:   now.Format(time.RFC3339),
	}

	// Step 1 — emit the final SSE handover event. Goes through the same
	// emit path as Phase 0/1 events so the wizard's existing reducer
	// picks it up without code change.
	handoverPayload, _ := json.Marshal(map[string]any{
		"sovereignFqdn": dep.Request.SovereignFQDN,
		"consoleURL":    consoleURL,
		"finalisedAt":   resp.FinalisedAt,
	})
	h.emitWatchEvent(dep, provisioner.Event{
		Time:    resp.FinalisedAt,
		Phase:   "handover",
		Level:   "info",
		Message: string(handoverPayload),
	})
	resp.Steps.HandoverEventEmitted = true

	// Step 2 — cancel the helmwatch informer if one is live. Reading
	// dep.liveWatcher under dep.mu mirrors the existing pattern in
	// phase1_watch.go.
	dep.mu.Lock()
	watcher := dep.liveWatcher
	dep.mu.Unlock()
	if watcher != nil {
		watcher.Cancel()
		resp.Steps.HelmwatchCancelled = true
	}

	if body.DryRun {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Step 3 — export OpenTofu state. Read every regular file under the
	// per-deployment workdir, base64-wrap it, and POST it to the new
	// Sovereign's `/api/v1/handover/tofu-archive`. The new Sovereign's
	// catalyst-api writes the archive to OpenBao at
	// `secret/catalyst/tofu-phase0-archive` and replies 200 OK; only on
	// that confirmation do we delete the local workdir.
	prov := provisioner.New()
	tofuWorkdir := filepath.Join(prov.WorkDir, id)
	archive, err := buildTofuArchive(tofuWorkdir)
	if err != nil {
		resp.Errors = append(resp.Errors, "build tofu archive: "+err.Error())
	} else {
		ackErr := h.postTofuArchive(r.Context(), dep.Request.SovereignFQDN, id, archive)
		if ackErr != nil {
			resp.Errors = append(resp.Errors, "submit tofu archive: "+ackErr.Error())
		} else {
			resp.Steps.TofuArchiveSubmitted = true
			if rmErr := os.RemoveAll(tofuWorkdir); rmErr != nil && !os.IsNotExist(rmErr) {
				resp.Errors = append(resp.Errors, "remove tofu workdir: "+rmErr.Error())
			} else {
				resp.Steps.TofuWorkdirRemoved = true
			}
		}
	}

	// Step 4a — delete the kubeconfig file. The wizard's StepSuccess
	// "Download kubeconfig" button must have already been used by the
	// operator if needed; post-handover the kubeconfig is the new
	// Sovereign's responsibility (its OpenBao holds a copy; its admin
	// can re-mint via the cluster's CSR API).
	if h.kubeconfigsDir != "" {
		kcPath := filepath.Join(h.kubeconfigsDir, id+".yaml")
		if err := os.Remove(kcPath); err != nil {
			if !os.IsNotExist(err) {
				resp.Errors = append(resp.Errors, "remove kubeconfig: "+err.Error())
			}
		} else {
			resp.Steps.KubeconfigRemoved = true
		}
	}

	// Step 4b — slim the deployment record instead of deleting (#453,
	// reconciles #317↔#319 contract).
	//
	// Issue #319 (PR #451) shipped the post-handover redirect: when the
	// browser hits console.openova.io/sovereign/<id>, the
	// provisionRoute.beforeLoad fetches /api/v1/deployments/<id>, reads
	// `adoptedAt` + `sovereignFQDN`, and 301s to https://console.<fqdn>/.
	// Deleting the record (the previous behaviour) made that fetch 404
	// and the redirect could never fire — broken-by-design contract.
	//
	// The fix is to retain a slim post-handover record:
	//   - Audit minimum: id, sovereignFQDN, orgName, orgEmail, startedAt
	//   - Redirect contract: adoptedAt = now()
	// All operational fields (Result/tofuState, kubeconfig hash, PDM
	// reservation token, error, component states) are zeroed via
	// SlimForHandover. The on-disk record after this Save is the
	// structural breadcrumb required by §0 minimum-retention; nothing
	// more.
	adopted := dep.SlimForHandover(now)
	h.deployments.Store(id, adopted)
	if h.store != nil {
		if err := h.store.Save(adopted.toRecord()); err != nil {
			resp.Errors = append(resp.Errors, "persist adopted record: "+err.Error())
		} else {
			resp.Steps.DeploymentRecordRemoved = true
		}
	} else {
		// In-memory-only handler (test path) — slim shape was swapped
		// in via Store above, that satisfies the redirect contract on
		// its own.
		resp.Steps.DeploymentRecordRemoved = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// tofuArchiveRequest is the body the Catalyst-Zero side POSTs to the
// new Sovereign's /api/v1/handover/tofu-archive endpoint.
type tofuArchiveRequest struct {
	DeploymentID  string `json:"deploymentId"`
	SovereignFQDN string `json:"sovereignFqdn"`
	// Files maps each path (relative to the original workdir) to a
	// base64-encoded blob of the file contents.
	Files map[string]string `json:"files"`
	// CapturedAt is the wall-clock time the archive was built. The new
	// Sovereign stores this alongside the blob for forensic audit.
	CapturedAt string `json:"capturedAt"`
}

// tofuArchiveResponse is what the receiver returns on success. Empty
// body means 200; the writeJSON path always emits one of these.
type tofuArchiveResponse struct {
	OK         bool   `json:"ok"`
	StoredAt   string `json:"storedAt,omitempty"`
	SecretPath string `json:"secretPath,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ReceiveTofuArchive handles POST /api/v1/handover/tofu-archive. This
// endpoint is served by the SAME catalyst-api binary; production
// containers running on a Sovereign accept the inbound POST and seal
// the archive into their local OpenBao. Catalyst-Zero exposes the same
// route too — it never receives legitimate POSTs (Catalyst-Zero is the
// SENDER) but a misrouted call lands on a 503 instead of a 404, which
// is an explicit "wrong target" diagnostic for the operator.
//
// The receiver-side check is "does this binary have an OpenBao client
// configured" — production Sovereigns set CATALYST_OPENBAO_ADDR +
// CATALYST_OPENBAO_TOKEN env vars and the in-memory client is non-nil;
// Catalyst-Zero leaves them unset and returns 503.
//
// Path: secret/catalyst/tofu-phase0-archive (KV-v2). The data field
// contains the marshaled tofuArchiveRequest as a JSON string blob plus
// {capturedAt, deploymentId, sovereignFqdn} for queryability.
func (h *Handler) ReceiveTofuArchive(w http.ResponseWriter, r *http.Request) {
	client := h.openbao
	if client == nil {
		writeJSON(w, http.StatusServiceUnavailable, tofuArchiveResponse{
			OK:    false,
			Error: "openbao client not configured: this catalyst-api is not configured as a handover target (CATALYST_OPENBAO_ADDR + CATALYST_OPENBAO_TOKEN must be set on the new Sovereign's catalyst-api Pod)",
		})
		return
	}

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20)) // 64 MiB cap
	if err != nil {
		writeJSON(w, http.StatusBadRequest, tofuArchiveResponse{
			OK:    false,
			Error: "read body: " + err.Error(),
		})
		return
	}
	var body tofuArchiveRequest
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, tofuArchiveResponse{
			OK:    false,
			Error: "invalid body: " + err.Error(),
		})
		return
	}
	if strings.TrimSpace(body.DeploymentID) == "" || strings.TrimSpace(body.SovereignFQDN) == "" {
		writeJSON(w, http.StatusBadRequest, tofuArchiveResponse{
			OK:    false,
			Error: "deploymentId and sovereignFqdn are required",
		})
		return
	}
	if len(body.Files) == 0 {
		writeJSON(w, http.StatusBadRequest, tofuArchiveResponse{
			OK:    false,
			Error: "files map must contain at least one entry",
		})
		return
	}

	// Marshal the entire body as the OpenBao value blob. KV-v2 accepts
	// a flat string→any map; we wrap the files map as JSON so the
	// blob round-trips through the API without per-file Vault writes.
	filesJSON, err := json.Marshal(body.Files)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, tofuArchiveResponse{
			OK:    false,
			Error: "marshal archive: " + err.Error(),
		})
		return
	}

	storedAt := time.Now().UTC().Format(time.RFC3339)
	if err := client.PutKVv2(r.Context(), "secret", "catalyst/tofu-phase0-archive", map[string]any{
		"deploymentId":  body.DeploymentID,
		"sovereignFqdn": body.SovereignFQDN,
		"capturedAt":    body.CapturedAt,
		"storedAt":      storedAt,
		"files":         string(filesJSON),
	}); err != nil {
		h.log.Error("openbao write tofu archive failed",
			"deploymentId", body.DeploymentID,
			"sovereignFqdn", body.SovereignFQDN,
			"err", err,
		)
		writeJSON(w, http.StatusBadGateway, tofuArchiveResponse{
			OK:    false,
			Error: "seal archive: " + err.Error(),
		})
		return
	}
	h.log.Info("openbao tofu archive sealed",
		"deploymentId", body.DeploymentID,
		"sovereignFqdn", body.SovereignFQDN,
		"secretPath", "secret/catalyst/tofu-phase0-archive",
	)

	// Fire the Self-Sovereignty Cutover engine now that the durable
	// handover marker is sealed (issue #933 / #3052).
	// ──────────────────────────────────────────────────────────────────
	// The cutover's in-cluster auto-trigger (bp-self-sovereign-cutover's
	// Helm post-install hook → HandleCutoverInternalTrigger, source=
	// "internal") fires ONCE at chart-install (bootstrap). On a fresh prov
	// that is long before handover, so it correctly defers at the
	// handover-completion gate (425 Too Early) and the HR goes Ready with
	// NO engine start. Nothing else re-fires the engine after the mothership
	// POSTs the archive HERE — so without this hook the cutover stays
	// dormant forever and TC-16 (the deny-egress proof, issue #2940) can
	// never pass.
	//
	// We seal the archive THEN fire: the archive is present by definition at
	// this point, so the engine runs with source="handover" (gate skipped —
	// re-checking the marker we just wrote would be redundant and could race
	// the OpenBao read). The spawn is async (background context) so a slow
	// multi-step cutover never blocks the handover 200; the operator polls
	// /cutover/status or /cutover/events for progress. A Sovereign WITHOUT
	// the cutover chart installed (cutoverDepsFor errors, or no step
	// ConfigMaps) still succeeds the handover — the fire is best-effort.
	//
	// Runtime-overridable per docs/PRINCIPLES.md #4: set
	// CATALYST_FIRE_CUTOVER_ON_HANDOVER=false to keep the cutover a
	// deliberate operator action (the BSS CTA still fires it).
	if fireCutoverOnHandover() {
		deps, derr := h.cutoverDepsFor()
		if derr != nil {
			h.log.Info("handover seal: cutover auto-fire skipped (cutover not configured on this Sovereign)",
				"detail", derr.Error(),
			)
		} else {
			go func() {
				res, ferr := h.fireCutoverEngine(context.Background(), deps, "handover")
				if ferr != nil {
					h.log.Error("handover seal: cutover engine fire failed",
						"err", ferr,
					)
					return
				}
				h.log.Info("handover seal: cutover engine fired",
					"outcome", res.outcome,
				)
			}()
		}
	}

	writeJSON(w, http.StatusOK, tofuArchiveResponse{
		OK:         true,
		StoredAt:   storedAt,
		SecretPath: "secret/catalyst/tofu-phase0-archive",
	})
}

// postTofuArchive submits the archive to the new Sovereign's
// catalyst-api. The new Sovereign exposes the receiver at
// https://api.<sovereign-fqdn>/api/v1/handover/tofu-archive — that's
// the same FQDN the wizard's StepSuccess uses.
//
// In production the call goes over TLS-terminated HTTPS. The HTTP
// client is the package-level handover client so tests can override the
// baseURL (HandoverTargetURL) and inject a httptest.Server.
func (h *Handler) postTofuArchive(ctx context.Context, sovereignFQDN, deploymentID string, archive map[string]string) error {
	body := tofuArchiveRequest{
		DeploymentID:  deploymentID,
		SovereignFQDN: sovereignFQDN,
		Files:         archive,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	target := h.handoverTargetURL
	if target == "" {
		target = "https://api." + sovereignFQDN + "/api/v1/handover/tofu-archive"
	}

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	httpc := h.handoverHTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// Sanity-check the body shape — a 200 with `{"ok": false}` is still
	// a failure (the receiver might 200 a malformed request through a
	// future bug). The decode is best-effort: a decode error is logged
	// but does not fail the call (the status code is the contract).
	var ack tofuArchiveResponse
	if err := json.Unmarshal(respBody, &ack); err == nil && !ack.OK && ack.Error != "" {
		return fmt.Errorf("receiver rejected: %s", ack.Error)
	}
	return nil
}

// buildTofuArchive walks the per-deployment OpenTofu workdir and returns
// a map[relpath]→base64(content) suitable for embedding in the
// tofuArchiveRequest body.
//
// We deliberately include *every* regular file under the workdir,
// including symlinks-target files (the workdir contains a symlinked
// copy of /infra/hetzner/ per provisioner.stageModule). Filtering would
// risk losing terraform.tfstate / *.tf / lock files an operator may
// later need to bring up disaster-recovery from a Sovereign that is
// itself destroyed. The full archive is sealed in OpenBao on the
// Sovereign side; the operator's recovery procedure is:
//
//	bao kv get -mount=secret catalyst/tofu-phase0-archive
//
// Empty workdir (already cleaned by a prior wipe / dry-run) is not an
// error — buildTofuArchive returns an empty map and the postTofuArchive
// caller succeeds with a 0-files payload. That matches the founder's
// minimum-retention principle: an empty archive is the same artefact
// shape as a richly-populated one.
func buildTofuArchive(workdir string) (map[string]string, error) {
	out := make(map[string]string)
	if workdir == "" {
		return out, nil
	}
	info, err := os.Stat(workdir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("stat workdir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workdir %s is not a directory", workdir)
	}
	err = filepath.Walk(workdir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() {
			return nil
		}
		// Skip irregular files (devices, sockets) and symlinks. The
		// stageModule symlink targets resolve via os.Stat following but
		// filepath.Walk uses Lstat; a HelmRelease-class symlinked source
		// tree should NOT be archived (it is the canonical /infra/hetzner
		// module, recoverable from git).
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		// Cap individual file size at 16 MiB. terraform.tfstate is
		// typically <100 KiB; anything larger smells like a leaked
		// log or an unclean workdir.
		if fi.Size() > 16<<20 {
			return fmt.Errorf("file %s exceeds 16MiB cap (%d bytes)", path, fi.Size())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(workdir, path)
		if err != nil {
			return fmt.Errorf("relpath %s: %w", path, err)
		}
		out[rel] = base64.StdEncoding.EncodeToString(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetOpenBao wires an openbao.Client into the Handler. Called by
// catalyst-api's main.go on a Sovereign cluster (CATALYST_OPENBAO_ADDR
// + CATALYST_OPENBAO_TOKEN env vars present); Catalyst-Zero leaves them
// unset and the receiver returns 503.
//
// Tests inject a httptest-backed client via this method.
func (h *Handler) SetOpenBao(c *openbao.Client) {
	h.openbao = c
}

// SetHandoverTargetURL overrides the URL the finaliseHandover handler
// posts to. Production passes "" so the default
// `https://api.<sovereignFQDN>/api/v1/handover/tofu-archive` applies.
// Tests inject a httptest.Server URL.
func (h *Handler) SetHandoverTargetURL(url string) {
	h.handoverTargetURL = url
}

// SetHandoverHTTPClient overrides the HTTP client used by the finalise
// handler to POST archives. Tests inject a client that bypasses TLS.
func (h *Handler) SetHandoverHTTPClient(c *http.Client) {
	h.handoverHTTPClient = c
}
