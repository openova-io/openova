// PUT + GET /api/v1/deployments/{id}/cloudinit-log — the cloud-init
// diagnostic-log capture contract (#3132, founder directive 2026-06-08).
//
// WHY THIS EXISTS: a Phase-1 cloud-init failure ("the new Sovereign
// cluster did not PUT its kubeconfig") leaves the control-plane VM
// running but the cluster never converges. To fix it we need the
// cloud-init log — but on kom4dc HCS that log CANNOT be PULLED:
//   - the ECS SDK exposes no os-getConsoleOutput (only interactive
//     VNC/serial consoles), and
//   - the control-plane has no reachable sshd (Connection refused even
//     after opening :22 in the security group — verified live on
//     hw106, 2026-06-08).
//
// So the log must be PUSHED. The control-plane cloud-init uploads
// /var/log/cloud-init-output.log to this endpoint on a 30s loop
// (cloudinit-control-plane.tftpl), so the latest snapshot is always on
// the mothership PVC even when cloud-init aborts before the kubeconfig
// PUT. This is the ONLY diagnosis path for the recurring Phase-1
// failure (#3129) AND the mechanical "capture-before-wipe" guard the
// founder asked for — the log survives the wipe regardless of operator
// discipline.
//
// AUTH: the same bearer as the kubeconfig PUT (the cloud-init already
// carries kubeconfig_bearer_token). Verified by SHA-256 +
// ConstantTimeCompare against Deployment.kubeconfigBearerHash, exactly
// like PutKubeconfig.
//
// NOT single-use: unlike the kubeconfig, the log is uploaded
// repeatedly and each PUT OVERWRITES the prior snapshot (latest wins).
// It never triggers Phase-1 watch / SMTP seed — it is pure diagnostics.
//
// PER-NODE CAPTURE (#6339). Latest-wins was written for a single-CP
// Sovereign. On a multi-region one EVERY control plane uploads to this
// same `{id}` every 30s for ~50 minutes, so the surviving
// `<id>-cloudinit.log` is whichever node wrote last and the other
// region's log is destroyed. Measured on the 13 archived captures in
// docs/sessions/**/prov-diagnostics/ that carry a kubeconfig PUT-back
// line: each holds exactly ONE hostname — 7 from region a, 6 from
// region b, never both. That is the whole reason "the primary never
// PUT its kubeconfig" was undiagnosable on hw297/hw298: the primary
// echoes its PUT-back outcome only into ITS log, and the surviving
// capture was the secondary's.
//
// So the cloud-init now sends `?node=<hostname>` and this handler
// ALSO writes `<id>-cloudinit-<node>.log`. The shared
// `<id>-cloudinit.log` keeps its exact old latest-wins semantics so
// every existing reader (GET with no query, the wipe-forensics flow,
// docs/sessions archives) is untouched. GET gains `?node=` to fetch a
// specific node's capture and advertises what it holds in the
// `X-Catalyst-Cloudinit-Nodes` response header.
//
// Credential hygiene (INVIOLABLE-PRINCIPLES #10): logs only id + byte
// length + outcome, never the body or the bearer.
package handler

import (
	"crypto/subtle"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

// maxCloudInitLogBytes — upper bound on a cloud-init log PUT. A real
// /var/log/cloud-init-output.log is ~100-500 KB; 4 MiB leaves generous
// headroom while keeping a runaway upload from filling the PVC.
const maxCloudInitLogBytes = 4 << 20

// unsafeNodeKeyChars — anything outside [a-z0-9-] in a `?node=` value.
// The node key is a hostname (`catalyst-<slug>-<dep8>-<region>-cp1-<6hex>`,
// 60 chars on kom4dc) that is composed into a filename on a shared PVC,
// so it is folded to lowercase and every other byte — `/`, `.`, `..`,
// NUL, unicode — is replaced rather than rejected: a node whose hostname
// carries an odd byte must still get its log captured, it just gets a
// sanitised key. An all-unsafe value collapses to "" and the per-node
// write is skipped (the shared latest-wins file still lands).
var unsafeNodeKeyChars = regexp.MustCompile(`[^a-z0-9-]+`)

// maxNodeKeyLen bounds the composed filename. HOST_NAME_MAX is 64 on
// Linux; 96 leaves room without letting a hostile caller push the path
// toward NAME_MAX.
const maxNodeKeyLen = 96

// sanitiseNodeKey folds a `?node=` value to a filename-safe key.
// Returns "" when nothing usable survives, which callers treat as
// "no per-node capture for this upload".
func sanitiseNodeKey(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	s = unsafeNodeKeyChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxNodeKeyLen {
		s = strings.Trim(s[:maxNodeKeyLen], "-")
	}
	return s
}

// cloudInitNodeKeys lists the per-node captures on disk for a
// deployment, newest-agnostic and sorted for a deterministic answer.
// A read error yields an empty list — this is a diagnostics aid, never
// a gate.
func cloudInitNodeKeys(dir, id string) []string {
	if dir == "" || id == "" {
		return nil
	}
	prefix := filepath.Join(dir, id+"-cloudinit-")
	matches, err := filepath.Glob(prefix + "*.log")
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(matches))
	for _, m := range matches {
		keys = append(keys, strings.TrimSuffix(strings.TrimPrefix(m, prefix), ".log"))
	}
	sort.Strings(keys)
	return keys
}

// PutCloudInitLog — PUT /api/v1/deployments/{id}/cloudinit-log.
//
// Bearer-protected (same token + hash as PutKubeconfig). Idempotent
// overwrite — the cloud-init loop re-uploads the growing log; the
// latest snapshot wins. No single-use guard, no Phase-1/SMTP launch.
func (h *Handler) PutCloudInitLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <token> header is required",
		})
		return
	}
	dep.mu.Lock()
	persistedHash := dep.kubeconfigBearerHash
	dep.mu.Unlock()
	if persistedHash == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "no-bearer-hash",
			"detail": "this deployment has no bearer hash on record; refusing the upload",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(hashBearerToken(bearer)), []byte(persistedHash)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "invalid-bearer",
			"detail": "bearer token does not match the deployment's expected hash",
		})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCloudInitLogBytes))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "body-too-large",
			"detail": "cloud-init log exceeds the size cap",
		})
		return
	}
	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured (CATALYST_KUBECONFIGS_DIR)",
		})
		return
	}
	if err := os.MkdirAll(h.kubeconfigsDir, 0o700); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unwritable",
			"detail": "catalyst-api cannot create the diagnostics directory: " + err.Error(),
		})
		return
	}
	target := filepath.Join(h.kubeconfigsDir, id+"-cloudinit.log")
	if err := writeFileAtomic0600(target, body); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "write-failed",
			"detail": "cloud-init log could not be persisted: " + err.Error(),
		})
		return
	}

	// #6339 — additionally keep THIS node's own capture so a second
	// region's uploader cannot destroy it. Best-effort by design: the
	// shared file above is already durable, so a per-node write failure
	// must never turn a diagnostics upload into a 5xx the cloud-init
	// would retry for six minutes.
	node := sanitiseNodeKey(r.URL.Query().Get("node"))
	if node != "" {
		perNode := filepath.Join(h.kubeconfigsDir, id+"-cloudinit-"+node+".log")
		if err := writeFileAtomic0600(perNode, body); err != nil {
			h.log.Warn("per-node cloud-init log write failed; shared capture still landed",
				"id", id, "node", node, "err", err)
		}
	}

	h.log.Info("cloud-init log received from control-plane",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"node", node,
		"bytes", len(body),
	)
	w.WriteHeader(http.StatusNoContent)
}

// GetCloudInitLog — GET /api/v1/deployments/{id}/cloudinit-log.
//
// Returns the last-uploaded cloud-init log as text/plain for
// post-mortem diagnosis of a Phase-1 failure.
//
// FALSE-NEGATIVE FIX (#3380 row D / post-mortem-survives-the-wipe): the
// log file is written to OUTLIVE the deployment record — its entire
// reason to exist is "the log survives the wipe regardless of operator
// discipline" (see the PUT-side header above). But the original
// implementation gated the GET on `h.deployments.Load(id)` first and
// returned 404 "deployment not found" the moment the in-memory record
// was gone (wiped, GC'd, or simply not restored after a mothership
// roll) — EVEN WHEN `<id>-cloudinit.log` was sitting right there on the
// PVC. That defeated the only Phase-1 forensic path on kom4dc (no
// console-output API, no reachable sshd). The contract the founder
// asked for is: if the log file exists, serve it.
//
// So we DECOUPLE the file lookup from the record lookup:
//   - `id` is sanitised with safeIDPattern (defense-in-depth — the
//     filename is composed from it and read off a shared PVC, so a
//     traversal attempt like `../../etc/...` must never reach ReadFile,
//     even for an authenticated caller).
//   - If the record IS still in the map, we run the ownership check so
//     a live deployment's log stays scoped to its creator (unchanged).
//   - If the record is GONE, we fall through to the file directly —
//     this is the post-mortem case the endpoint exists for. The route
//     already sits behind RequireSession, so only authenticated
//     operators reach here; a wiped deployment has no OwnerEmail left to
//     scope against, and the log is pure diagnostics (no secrets — the
//     PUT-side strips nothing but also logs only byte-length).
//
// 404 fires ONLY when the file genuinely does not exist on disk.
func (h *Handler) GetCloudInitLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Sanitise BEFORE composing any filesystem path. The deployment id
	// is 16 lowercase-hex chars (newID = hex of 8 random bytes), which
	// safeIDPattern accepts; anything carrying `/`, `.` or `..` is
	// rejected outright so the fall-through file read below can never
	// escape kubeconfigsDir.
	if !safeIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-id",
			"detail": "deployment id must match ^[a-z0-9][a-z0-9-]{0,62}$",
		})
		return
	}

	// Ownership scoping applies ONLY while the record is live. A wiped
	// deployment whose log survived has no record to scope against — the
	// post-mortem read is the whole point. (RequireSession still gates
	// the route, so this is an authenticated-operator surface either
	// way.)
	if val, ok := h.deployments.Load(id); ok {
		dep := val.(*Deployment)
		if !h.checkOwnership(w, r, dep) {
			return
		}
	}

	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured",
		})
		return
	}
	// #6339 — per-node selection. `?node=<hostname>` serves THAT control
	// plane's capture; without it the shared latest-wins file is served
	// exactly as before. Either way the response advertises which
	// per-node captures exist, so an operator who fetched the secondary's
	// log by accident can see the primary's key without a second contract
	// to learn.
	nodes := cloudInitNodeKeys(h.kubeconfigsDir, id)
	target := filepath.Join(h.kubeconfigsDir, id+"-cloudinit.log")
	if node := sanitiseNodeKey(r.URL.Query().Get("node")); node != "" {
		target = filepath.Join(h.kubeconfigsDir, id+"-cloudinit-"+node+".log")
		if _, statErr := os.Stat(target); statErr != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error":     "no-cloudinit-log-for-node",
				"detail":    "no cloud-init log has been uploaded by that node",
				"node":      node,
				"available": nodes,
			})
			return
		}
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "no-cloudinit-log",
			"detail": "no cloud-init log has been uploaded for this deployment yet",
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(nodes) > 0 {
		w.Header().Set("X-Catalyst-Cloudinit-Nodes", strings.Join(nodes, ","))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
