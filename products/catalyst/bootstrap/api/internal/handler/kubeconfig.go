// GET + PUT /api/v1/deployments/{id}/kubeconfig — the cloud-init
// kubeconfig postback contract (issue #183, Option D).
//
// Producer of the bytes: the new Sovereign's cloud-init runs k3s
// install, waits for /healthz, rewrites /etc/rancher/k3s/k3s.yaml's
// `https://127.0.0.1:6443` to the load-balancer's public IP, then
// PUTs the rewritten YAML to this endpoint with an
// `Authorization: Bearer <token>` header. The token was templated
// into cloud-init by the OpenTofu module from the
// `kubeconfig_bearer_token` tfvars key the catalyst-api stamped onto
// the provisioner.Request at CreateDeployment time.
//
// Bearer verification:
//
//   - The handler reads the bearer from `Authorization: Bearer ...`
//     (case-insensitive), computes SHA-256, hex-encodes, and uses
//     `subtle.ConstantTimeCompare` against the persisted
//     `Deployment.kubeconfigBearerHash`. A mismatch returns 403 with
//     `{"error":"invalid-bearer"}`.
//   - The bearer is single-use: once `Result.KubeconfigPath` is set
//     a subsequent PUT returns 403 with `{"error":"already-set"}`.
//     This defends against a replay where an attacker captured the
//     bearer (e.g. by reading user_data) and tries to swap in a
//     hostile kubeconfig later.
//
// File handling:
//
//   - Plaintext is written to `<kubeconfigsDir>/<id>.yaml` with
//     mode 0600. The directory is pre-created at handler startup
//     (mode 0700). The plaintext NEVER lands in the JSON record on
//     disk — the record carries only the path pointer.
//   - After the file write, `Result.KubeconfigPath` is populated and
//     persistDeployment is called so a Pod restart between PUT and
//     the helmwatch goroutine launching still finds the path on
//     disk.
//   - The helmwatch goroutine is then kicked off via
//     `runPhase1Watch` so the per-component SSE events start flowing
//     to the wizard. The phase1Started guard on Deployment ensures
//     runProvisioning's later (synchronous) call is a no-op so we
//     don't run two informers.
//
// Failure modes:
//
//   - 401 — Authorization header missing or not `Bearer ...`
//   - 403 — bearer hash mismatch OR kubeconfig already set OR no
//     bearer hash on record
//   - 404 — deployment id unknown
//   - 422 — body empty or > 1 MiB (a valid k3s kubeconfig is ~3 KB)
//   - 503 — kubeconfigs directory unwritable (PVC unmounted)
//
// GET semantics (unchanged contract for operator break-glass /
// wizard "Download kubeconfig"):
//
//   - 200 application/yaml when KubeconfigPath is set and readable.
//     The bytes returned are the ON-DISK file with the cluster /
//     context / user names rewritten from k3s's hardcoded `default`
//     to the Sovereign's subdomain (e.g. `otech94`) so the operator
//     can run `KUBECONFIG=$HOME/.kube/config:<file> kubectl config
//     view --flatten > $HOME/.kube/config` and then
//     `k9s --context=otech94` immediately, with no manual sed
//     pipeline. Issue #765. The rename is idempotent (re-applying
//     to an already-renamed file is a no-op) and preserves the
//     certificate-authority-data + token bytes verbatim. If the
//     YAML is not parseable as a kubeconfig we serve the bytes
//     unmodified so an operator's hand-edited file is never
//     corrupted by the rewriter.
//   - 409 {"error":"not-implemented"} when the postback hasn't
//     happened yet — preserves the StepSuccess.test.tsx fallback.
//   - 409 {"error":"kubeconfig-file-missing"} when the path pointer
//     is set but the file is gone (PVC drift).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): the
// catalyst-api never logs the kubeconfig bytes, never logs the
// bearer token, never logs the bearer hash. It logs deployment id,
// byte length, and outcome class.
//
// The GET endpoint remains intentionally NOT authenticated at the
// catalyst-api edge — it inherits whatever auth the franchise
// console attaches. PUT is bearer-protected because the cloud-init
// caller has no SSO context. Adding edge-auth to GET is out of
// scope for this issue (separate ticket if needed).
package handler

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// maxKubeconfigBytes — upper bound on a PUT body. A real k3s
// kubeconfig is roughly 3 KB; capping at 1 MiB preserves headroom
// for hand-edited configs while making oversize-bomb attacks
// against the PVC harmless.
const maxKubeconfigBytes = 1 << 20

// GetKubeconfig — GET /api/v1/deployments/{id}/kubeconfig.
//
// Returns the kubeconfig as application/yaml by reading the file
// at Result.KubeconfigPath. An absent or unreadable path yields
// HTTP 409 with body {"error": "not-implemented"} so the wizard's
// existing StepSuccess.test.tsx fallback path keeps working.
func (h *Handler) GetKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)
	// Issue #689 — ownership check before serving the kubeconfig (which
	// is the most sensitive artefact a Sovereign produces). 404 on
	// mismatch so existence is never leaked.
	if !h.checkOwnership(w, r, dep) {
		return
	}

	// Multi-region: honour ?region=<key> so the operator can fetch any
	// secondary region's kubeconfig (e.g. ?region=nbg1-1). The PUT
	// endpoint stores secondary CPs at <kubeconfigsDir>/<id>-<key>.yaml
	// (see PutKubeconfig L520+); without this branch, GET always
	// returned the primary's kubeconfig regardless of the query param,
	// blocking D-gate validation against secondary clusters on
	// multi-region Sovereigns. Caught on prov t117.omani.works
	// (7152ad51e7838836, 2026-05-16) — every per-region kubeconfig
	// fetch returned 89.167.22.182:6443 (primary), defeating D8/D9/D12
	// per-region inspection from the CLI.
	region := strings.TrimSpace(r.URL.Query().Get("region"))

	dep.mu.Lock()
	var path string
	if dep.Result != nil {
		path = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()

	// Per-region override: derive the path from kubeconfigsDir +
	// id-region.yaml. Empty region falls through to the primary path
	// stamped on Result.KubeconfigPath (preserves backwards-compat for
	// every caller that doesn't pass the query).
	if region != "" {
		if h.kubeconfigsDir == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":  "kubeconfigs-dir-unconfigured",
				"detail": "catalyst-api has no kubeconfigs directory configured; cannot serve per-region kubeconfig.",
			})
			return
		}
		path = filepath.Join(h.kubeconfigsDir, id+"-"+region+".yaml")
	}

	if path == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-implemented",
			"detail": "kubeconfig has not been captured for this deployment yet. Operator can fetch it via SSH per docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH; programmatic capture happens when the new Sovereign's cloud-init PUTs to this endpoint.",
		})
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		// File pointer present but file gone — the "kubeconfig file
		// lost" case. Surface 409 so the wizard renders the SSH-
		// fetch fallback rather than a server error.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "kubeconfig-file-missing",
			"detail": "deployment record points at a kubeconfig file that no longer exists on disk: " + err.Error(),
		})
		return
	}

	// Issue #765 — rewrite cluster / context / user names from
	// k3s's hardcoded `default` to the Sovereign's subdomain so the
	// operator can `k9s --context=otech94` after a single
	// `kubectl config view --flatten` merge without manually
	// sed-ing the YAML. The on-disk file is left as the cloud-init
	// produced it; the rewrite happens on the response only so the
	// PUT idempotency invariant holds. If the rewrite fails we serve
	// the raw bytes unchanged — better to give the operator the
	// k3s-default-context kubeconfig than fail the download.
	contextName := preferredContextName(&dep.Request)
	served := raw
	if rewritten, rewriteErr := rewriteKubeconfigContext(raw, contextName); rewriteErr == nil {
		served = rewritten
	} else {
		h.log.Warn("kubeconfig context rewrite failed; serving raw file",
			"id", id,
			"sovereignFQDN", dep.Request.SovereignFQDN,
			"contextName", contextName,
			"err", rewriteErr,
		)
	}

	// Filename uses the subdomain when available so a freshly-
	// downloaded file lands in ~/Downloads as `otech94.yaml` rather
	// than `otech94.omantel.omani.works-kubeconfig.yaml` which is
	// awkward to type in a `KUBECONFIG=...` invocation.
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+kubeconfigDownloadFilename(&dep.Request)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(served)

	h.log.Info("kubeconfig served",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"contextName", contextName,
		"bytesRaw", len(raw),
		"bytesServed", len(served),
	)
}

// preferredContextName returns the kubeconfig context name to use
// when serving GET /kubeconfig. Issue #765 spec: "context name =
// otechN (not default)". Order of preference:
//
//  1. Request.SovereignSubdomain — set by the wizard for every
//     pool-mode deployment (e.g. "otech94"). Sanitised to a safe
//     k8s context name (RFC 1123-ish: lowercase alnum + dash).
//  2. First label of SovereignFQDN — for BYO-domain deployments
//     where SovereignSubdomain is empty (e.g. "k8s.foo.example"
//     → "k8s").
//  3. The literal string "sovereign" — last-resort fallback so the
//     YAML never contains an empty context name (which kubectl
//     refuses to parse).
//
// The returned value is always non-empty.
func preferredContextName(req *provisioner.Request) string {
	if req == nil {
		return "sovereign"
	}
	if name := sanitiseContextName(req.SovereignSubdomain); name != "" {
		return name
	}
	if fqdn := strings.TrimSpace(req.SovereignFQDN); fqdn != "" {
		first := fqdn
		if dot := strings.IndexByte(first, '.'); dot > 0 {
			first = first[:dot]
		}
		if name := sanitiseContextName(first); name != "" {
			return name
		}
	}
	return "sovereign"
}

// sanitiseContextName strips characters kubectl rejects in a
// context name. kubeconfig technically accepts any string, but
// $HOME/.kube/config consumers (k9s, kubectl config use-context,
// kubeconform) treat it as a CLI argument so we restrict to the
// RFC-1123 lowercase label charset (alphanumeric + dash) and trim
// leading / trailing dashes.
func sanitiseContextName(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	if in == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

// kubeconfigDownloadFilename builds the Content-Disposition
// filename. Prefers the subdomain (operator's mental model — they
// know "otech94" not "otech94.omantel.omani.works"); falls back to
// the FQDN when subdomain is empty.
func kubeconfigDownloadFilename(req *provisioner.Request) string {
	if req == nil {
		return "sovereign-kubeconfig.yaml"
	}
	if sub := sanitiseContextName(req.SovereignSubdomain); sub != "" {
		return sub + ".yaml"
	}
	if fqdn := strings.TrimSpace(req.SovereignFQDN); fqdn != "" {
		return fqdn + "-kubeconfig.yaml"
	}
	return "sovereign-kubeconfig.yaml"
}

// rewriteKubeconfigContext rewrites every reference to the k3s
// default cluster / context / user name to `target` so a downloaded
// kubeconfig merges into $HOME/.kube/config under a stable, human-
// readable context name. Specifically rewrites:
//
//   - clusters[].name
//   - contexts[].name
//   - contexts[].context.cluster
//   - contexts[].context.user
//   - users[].name
//   - current-context
//
// Only renames the entry whose existing name == "default" (the
// k3s seed). Other named entries are left alone so an operator's
// hand-merged file isn't clobbered. A round-trip through yaml.v3
// preserves field ordering + comments so the file diffs cleanly.
//
// A nil/empty target is rejected (the caller computes a non-empty
// fallback). A YAML parse failure or a structurally unexpected
// document (no `kind: Config` root, no `clusters` key) returns the
// input unchanged with no error so a hand-edited or future-format
// kubeconfig is never silently corrupted — the GET handler logs
// the warning and serves raw on rewrite-error path.
func rewriteKubeconfigContext(in []byte, target string) ([]byte, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("rewriteKubeconfigContext: target context name is empty")
	}
	if len(in) == 0 {
		return nil, errors.New("rewriteKubeconfigContext: input is empty")
	}

	var root yaml.Node
	if err := yaml.Unmarshal(in, &root); err != nil {
		return nil, fmt.Errorf("yaml parse: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, errors.New("rewriteKubeconfigContext: not a YAML document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, errors.New("rewriteKubeconfigContext: root is not a mapping")
	}

	// Validate kind: Config (the kubeconfig sentinel). If absent
	// or wrong, refuse to rewrite — caller will serve raw.
	if kind := mapStringValue(doc, "kind"); kind != "" && kind != "Config" {
		return nil, fmt.Errorf("rewriteKubeconfigContext: unexpected kind %q", kind)
	}

	// rename rewrites a string scalar field on a mapping node from
	// "default" to target. No-op if the field is missing or the
	// value is not the k3s default (idempotent re-apply).
	rename := func(n *yaml.Node, key string) {
		if n == nil || n.Kind != yaml.MappingNode {
			return
		}
		v := mapChild(n, key)
		if v == nil || v.Kind != yaml.ScalarNode {
			return
		}
		if v.Value == "default" {
			v.Value = target
			v.Tag = "!!str"
			v.Style = 0
		}
	}

	// Iterate clusters[], contexts[], users[] sequences.
	renameSeq := func(seqKey string, perItem func(*yaml.Node)) {
		seq := mapChild(doc, seqKey)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		for _, item := range seq.Content {
			if item.Kind == yaml.MappingNode {
				perItem(item)
			}
		}
	}

	renameSeq("clusters", func(item *yaml.Node) {
		rename(item, "name")
	})
	renameSeq("contexts", func(item *yaml.Node) {
		rename(item, "name")
		ctx := mapChild(item, "context")
		if ctx != nil && ctx.Kind == yaml.MappingNode {
			rename(ctx, "cluster")
			rename(ctx, "user")
		}
	})
	renameSeq("users", func(item *yaml.Node) {
		rename(item, "name")
	})

	// Top-level current-context.
	if cc := mapChild(doc, "current-context"); cc != nil && cc.Kind == yaml.ScalarNode && cc.Value == "default" {
		cc.Value = target
		cc.Tag = "!!str"
		cc.Style = 0
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("yaml encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("yaml encoder close: %w", err)
	}
	return out.Bytes(), nil
}

// PutKubeconfig — PUT /api/v1/deployments/{id}/kubeconfig[?region=<k>].
//
// The cloud-init postback endpoint. See file header for the full
// contract.
//
// Multi-region (2026-05-12): when `?region=<k>` is present the body
// is stored at `<kubeconfigsDir>/<id>-<k>.yaml` and the single-use
// guard fires PER REGION rather than per deployment. This lets each
// secondary CP's cloud-init PUT its own kubeconfig without colliding
// with the primary's PUT. The primary's path (no ?region=) is
// unchanged.
func (h *Handler) PutKubeconfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	region := strings.TrimSpace(r.URL.Query().Get("region"))
	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	// Extract bearer token. RFC 6750 §2.1 — case-insensitive scheme,
	// single space separator. We trim aggressively to tolerate
	// trailing whitespace from curl variants.
	bearer := extractBearer(r.Header.Get("Authorization"))
	if bearer == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error":  "missing-bearer",
			"detail": "Authorization: Bearer <token> header is required",
		})
		return
	}

	// Snapshot the persisted hash + already-set state under the
	// lock so a concurrent retry/double-PUT can't observe the old
	// hash while we write the new file.
	//
	// Multi-region: `alreadySet` is region-scoped. For the primary
	// (region=="") it means Result.KubeconfigPath is populated. For a
	// secondary region it means the secondaryKubeconfigPaths[region]
	// entry is already populated.
	dep.mu.Lock()
	persistedHash := dep.kubeconfigBearerHash
	alreadySet := false
	if region == "" {
		alreadySet = dep.Result != nil && dep.Result.KubeconfigPath != ""
	} else {
		if dep.secondaryKubeconfigPaths != nil {
			_, alreadySet = dep.secondaryKubeconfigPaths[region]
		}
	}
	dep.mu.Unlock()

	if persistedHash == "" {
		// CreateDeployment always mints a hash, so a missing one
		// means this deployment was created before #183 landed (or
		// some upstream bug). Refuse — accepting a kubeconfig
		// against an unverifiable bearer would silently accept
		// arbitrary YAML.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "no-bearer-hash",
			"detail": "this deployment has no bearer hash on record; refusing to accept a kubeconfig",
		})
		return
	}

	// Constant-time compare on the SHA-256 hex strings. We hash
	// the inbound bearer first so timing differences in the
	// comparison can't leak the prefix of the persisted hash.
	inboundHash := hashBearerToken(bearer)
	if subtle.ConstantTimeCompare([]byte(inboundHash), []byte(persistedHash)) != 1 {
		h.log.Warn("kubeconfig PUT rejected: bearer mismatch",
			"id", id,
			"sovereignFQDN", dep.Request.SovereignFQDN,
		)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "invalid-bearer",
			"detail": "bearer token does not match the deployment's expected hash",
		})
		return
	}

	if alreadySet {
		// Single-use replay defence — once the kubeconfig has been
		// captured, the bearer is consumed. A second PUT (network
		// retry, attacker replay) is rejected.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "already-set",
			"detail": "kubeconfig has already been captured for this deployment; bearer is single-use",
		})
		return
	}

	// Read the body. http.MaxBytesReader caps at maxKubeconfigBytes
	// so a misbehaving cloud-init can't exhaust the PVC.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxKubeconfigBytes))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "body-too-large",
			"detail": fmt.Sprintf("kubeconfig body exceeds %d bytes", maxKubeconfigBytes),
		})
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "empty-body",
			"detail": "kubeconfig body is empty",
		})
		return
	}

	// Persist to disk. The directory is created in handler.New() at
	// startup; we MkdirAll here defensively so a delete-and-retry
	// flow (operator manually removing the kubeconfig to reissue)
	// recreates the directory automatically.
	if h.kubeconfigsDir == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unconfigured",
			"detail": "catalyst-api has no kubeconfigs directory configured (CATALYST_KUBECONFIGS_DIR)",
		})
		return
	}
	if err := os.MkdirAll(h.kubeconfigsDir, 0o700); err != nil {
		h.log.Error("kubeconfigs dir create failed", "dir", h.kubeconfigsDir, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "kubeconfigs-dir-unwritable",
			"detail": "catalyst-api cannot create the kubeconfigs directory: " + err.Error(),
		})
		return
	}
	// Multi-region: secondary CPs land at <dir>/<id>-<region>.yaml so
	// the primary's <dir>/<id>.yaml stays the canonical entry the rest
	// of catalyst-api (and the GET /kubeconfig endpoint) reads.
	filename := id + ".yaml"
	if region != "" {
		filename = id + "-" + region + ".yaml"
	}
	target := filepath.Join(h.kubeconfigsDir, filename)
	if err := writeFileAtomic0600(target, body); err != nil {
		h.log.Error("kubeconfig file write failed", "id", id, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "write-failed",
			"detail": "kubeconfig could not be persisted: " + err.Error(),
		})
		return
	}

	// Stamp the path on Result + persist. If Result is nil
	// (cloud-init beat `tofu apply` to the finish line — possible
	// when k3s healthz comes up before hcloud_load_balancer_target
	// reconciles READY), allocate a minimal Result with the known
	// SovereignFQDN; the runProvisioning goroutine merges in
	// ControlPlaneIP / LoadBalancerIP / ConsoleURL / GitOpsRepoURL
	// when Phase 0 finishes.
	//
	// Issue #538 belt-and-braces: if a previous Phase-1 watch
	// already terminated with OutcomeKubeconfigMissing (the
	// pre-#538 short-circuit, or a #538 wait-loop that timed out
	// before this PUT arrived), reset the terminal markers and
	// the phase1Started guard so the goroutine kicked off below
	// can actually run a fresh watch. Without this, the running
	// catalyst-api would have a kubeconfig on disk + a terminal
	// "failed" deployment record forever — exactly the live bug
	// PutKubeconfig is supposed to defend against.
	dep.mu.Lock()
	if dep.Result == nil {
		dep.Result = &provisioner.Result{
			SovereignFQDN: dep.Request.SovereignFQDN,
		}
	}
	if region == "" {
		dep.Result.KubeconfigPath = target
	} else {
		if dep.secondaryKubeconfigPaths == nil {
			dep.secondaryKubeconfigPaths = make(map[string]string)
		}
		dep.secondaryKubeconfigPaths[region] = target
	}
	relaunchAfterTerminalKubeconfigMissing := false
	// Secondary-region PUTs skip the Phase-1 relaunch / channel
	// re-init machinery — that's owned by the primary's first PUT.
	// A secondary kubeconfig arriving later just makes a new file on
	// disk available for the per-region helmwatch.Bridge spawned by
	// runPhase1Watch (which scans the kubeconfigsDir).
	if region == "" && dep.Result.Phase1Outcome == helmwatch.OutcomeKubeconfigMissing {
		// Clear the terminal markers so markPhase1Done writes the
		// new outcome cleanly when the relaunched watch finishes,
		// and clear phase1Started so the goroutine below isn't
		// short-circuited by the at-most-once guard.
		dep.Result.Phase1Outcome = ""
		dep.Result.Phase1FinishedAt = nil
		dep.Result.ComponentStates = nil
		dep.Status = "phase1-watching"
		dep.Error = ""
		dep.FinishedAt = time.Time{}
		dep.phase1Started = false
		// The done/eventsCh channels were closed by runProvisioning
		// when it called close(dep.eventsCh) / close(dep.done) at
		// the end of the original run — see deployments.go after
		// the runPhase1Watch invocation. Allocate fresh ones so the
		// new watch's emits + SSE consumers + the close(...) in our
		// goroutine below all see a live channel pair (mirrors
		// resumePhase1Watch).
		dep.eventsCh = make(chan provisioner.Event, 256)
		dep.done = make(chan struct{})
		relaunchAfterTerminalKubeconfigMissing = true
	}
	dep.mu.Unlock()
	h.persistDeployment(dep)

	h.log.Info("kubeconfig received from cloud-init",
		"id", id,
		"sovereignFQDN", dep.Request.SovereignFQDN,
		"path", target,
		"bytes", len(body),
		"relaunchAfterKubeconfigMissing", relaunchAfterTerminalKubeconfigMissing,
	)

	// SMTP seed + Phase-1 watch launch happen ONCE per deployment, on
	// the primary CP's PUT. Secondary-region PUTs just deposit the
	// per-region kubeconfig file; runPhase1Watch's per-region
	// helmwatch.Bridge spawner picks them up at its next refresh.
	if region == "" {
		// Issue #883 — seed the Sovereign-side
		// catalyst-system/sovereign-smtp-credentials Secret with the
		// mothership's SMTP submission credentials BEFORE Phase-1 watch
		// launches.
		seedOutcome := h.seedSovereignSMTPCredentials(r.Context(), dep, string(body))
		h.emitSovereignSMTPSeedEvent(dep, seedOutcome)

		// Launch the helmwatch goroutine in the background.
		go func() {
			h.runPhase1Watch(dep)
			if relaunchAfterTerminalKubeconfigMissing {
				dep.mu.Lock()
				select {
				case <-dep.done:
					// Already closed (defensive).
				default:
					close(dep.eventsCh)
					close(dep.done)
				}
				dep.mu.Unlock()
			}
		}()
	}

	w.WriteHeader(http.StatusNoContent)
}

// extractBearer returns the bearer plaintext from an Authorization
// header value. Empty string indicates "no bearer present" — every
// caller treats that as 401 unauthenticated.
func extractBearer(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if authHeader == "" {
		return ""
	}
	const prefix = "bearer "
	if len(authHeader) <= len(prefix) {
		return ""
	}
	if !strings.EqualFold(authHeader[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(authHeader[len(prefix):])
}

// writeFileAtomic0600 writes data to path atomically with mode 0600.
// Same temp-file + rename pattern store.Save uses for the JSON
// records, applied to the kubeconfig file.
func writeFileAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod 0600: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
