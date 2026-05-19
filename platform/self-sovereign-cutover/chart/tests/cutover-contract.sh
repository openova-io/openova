#!/usr/bin/env bash
# bp-self-sovereign-cutover — consumer-contract gate.
#
# Verifies that the chart's rendered output complies with the contract
# the catalyst-api cutover endpoint (issue #792, products/catalyst/
# bootstrap/api/internal/handler/cutover.go) depends on:
#
#   1. Chart MUST render with default values (smoke).
#   2. Each step ConfigMap MUST carry labels:
#        app.kubernetes.io/part-of=self-sovereign-cutover
#        app.kubernetes.io/component=cutover-step
#        bp.openova.io/cutover-order=<integer>
#        bp.openova.io/cutover-mode=<job|daemonset-wait>
#   3. Each step ConfigMap MUST carry data keys:
#        stepName  (always)
#        podSpec   (mode=job only)
#   4. EXACTLY 9 step ConfigMaps must render (steps 1..9; step 9
#      gitea-token-mint added in chart 0.1.30, TBD-C18).
#   5. Step 04 must be mode=daemonset-wait.
#   6. The status ConfigMap (default name self-sovereign-cutover-status)
#      MUST render with helm.sh/resource-policy: keep so a chart
#      uninstall doesn't lose mid-cutover state.
#   7. RBAC: the runner ClusterRole MUST split create verbs into a Rule
#      WITHOUT resourceNames (feedback_rbac_create_no_resourcenames.md
#      auto-memory anchor — combining create + resourceNames produces
#      403 every POST).
#   20. Step-06 Phase -1 (#1871, chart 0.1.32) MUST wait for the
#       Cilium Gateway in kube-system to report Programmed=True before
#       rewriting any HelmRepository URL. RBAC MUST grant get/list/watch
#       on gateway.networking.k8s.io/gateways so the poll resolves.
#
# Usage: bash tests/cutover-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

echo "[cutover-contract] Case 1: chart renders with default values"
helm template smoke . > "$TMP/render.yaml"
echo "  PASS ($(wc -l < "$TMP/render.yaml") lines)"

echo "[cutover-contract] Case 2 + 4: exactly 9 step ConfigMaps render with required labels"
# Use yq if present (the CI runner installs it for the blueprint-release
# guards); fall back to grep counting on workstations without yq.
# Step 9 (gitea-token-mint) added in chart 0.1.30 (TBD-C18) to bootstrap
# the Gitea API token the SME provisioning service uses; without it,
# tenant voucher checkout fails at journey step 16 because the
# catalyst-platform chart mirrors the Gitea admin PASSWORD verbatim
# into sme/provisioning-github-token, which 401s when sent as a Bearer
# token.
if command -v yq >/dev/null 2>&1; then
  # yq emits `---` separators between matched docs; filter those out
  # before counting names. `grep -E '^cutover-step-'` matches only the
  # actual ConfigMap names emitted by the projection.
  step_count=$(yq 'select(.kind == "ConfigMap" and .metadata.labels."app.kubernetes.io/component" == "cutover-step") | .metadata.name' "$TMP/render.yaml" | grep -cE '^cutover-step-')
else
  # Each step has labels block including `bp.openova.io/cutover-order: "N"`
  # — count distinct order values, which equals step count.
  step_count=$(grep -c 'bp.openova.io/cutover-order:' "$TMP/render.yaml")
fi
if [ "${step_count}" -ne 9 ]; then
  echo "FAIL: expected 9 step ConfigMaps, got ${step_count}" >&2
  exit 1
fi
echo "  PASS (9 step ConfigMaps)"

echo "[cutover-contract] Case 3: required data keys present"
# stepName key must exist on every step ConfigMap (9 total).
# podSpec key must exist on every job-mode step (8 of 9 — step 04 is daemonset-wait).
mode_job_count=$(grep -c 'bp.openova.io/cutover-mode: "job"' "$TMP/render.yaml")
if [ "${mode_job_count}" -ne 8 ]; then
  echo "FAIL: expected 8 job-mode step ConfigMaps, got ${mode_job_count}" >&2
  exit 1
fi
podspec_keys=$(grep -c '^  podSpec: |' "$TMP/render.yaml")
if [ "${podspec_keys}" -lt 8 ]; then
  echo "FAIL: expected at least 8 podSpec keys (one per job-mode step), got ${podspec_keys}" >&2
  exit 1
fi
stepname_keys=$(grep -c '^  stepName:' "$TMP/render.yaml")
if [ "${stepname_keys}" -lt 9 ]; then
  echo "FAIL: expected at least 9 stepName keys, got ${stepname_keys}" >&2
  exit 1
fi
echo "  PASS (data keys present on every step)"

echo "[cutover-contract] Case 5: step 04 is mode=daemonset-wait"
if ! grep -B5 'bp.openova.io/cutover-order: "4"' "$TMP/render.yaml" | grep -q 'cutover-mode: "daemonset-wait"'; then
  # `grep -B5 cutover-order:"4"` finds the metadata block that has the order
  # label; the mode label appears below order, not above. Switch to a forward
  # search.
  if ! grep -A3 'bp.openova.io/cutover-order: "4"' "$TMP/render.yaml" | grep -q 'cutover-mode: "daemonset-wait"'; then
    echo "FAIL: step 04 must have bp.openova.io/cutover-mode=daemonset-wait" >&2
    exit 1
  fi
fi
echo "  PASS (step 04 daemonset-wait)"

echo "[cutover-contract] Case 6: status ConfigMap has helm.sh/resource-policy: keep"
if ! awk '/kind: ConfigMap/,/^---$/' "$TMP/render.yaml" | grep -B2 'name: self-sovereign-cutover-status' | grep -q 'self-sovereign-cutover-status' ; then
  echo "FAIL: self-sovereign-cutover-status ConfigMap not found" >&2
  exit 1
fi
if ! grep -B5 'name: self-sovereign-cutover-status' "$TMP/render.yaml" | grep -q 'helm.sh/resource-policy: keep'; then
  # Resource-policy annotation may render after the metadata block; do a
  # post-block sweep.
  if ! awk '/name: self-sovereign-cutover-status/,/^---$/' "$TMP/render.yaml" | grep -q 'helm.sh/resource-policy: keep' ; then
    echo "FAIL: status ConfigMap missing helm.sh/resource-policy: keep" >&2
    exit 1
  fi
fi
echo "  PASS (status ConfigMap retained on uninstall)"

echo "[cutover-contract] Case 7: RBAC splits create verbs into a Rule WITHOUT resourceNames"
# Extract the ClusterRole block and confirm no rule has both verbs:[create]
# and resourceNames. The chart's templates/rbac.yaml structures the rules
# so create lives in its own Rule; this gate guards against future edits
# that accidentally combine them and reproduce the bp-openbao 403 loop.
clusterrole_block=$(awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml")
# Crude but reliable: check that no rule has 'create' immediately followed
# (within 5 lines) by 'resourceNames'.
if printf '%s\n' "${clusterrole_block}" | awk '
  /verbs:/ { in_verbs=1; verbs_line=NR; line=$0; verb_block=line; next }
  in_verbs && (NR <= verbs_line + 1) { verb_block=verb_block"\n"$0 }
  in_verbs && (NR > verbs_line + 1) { in_verbs=0 }
  /resourceNames:/ && (NR <= verbs_line + 6) {
    if (verb_block ~ /create/) {
      print "FAIL: create + resourceNames in same rule (line " NR ")"
      exit 1
    }
  }
'; then
  echo "  PASS (create verbs split out)"
else
  echo "FAIL: RBAC has create verbs combined with resourceNames" >&2
  exit 1
fi

echo "[cutover-contract] Case 8: auto-trigger Job renders by default (#933)"
# Founder rule: handover is not done until cutover has run. The chart MUST
# auto-fire by default. trigger.auto=false is the operator override, NOT
# the default.
if ! grep -q 'cutover-auto-trigger' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job missing from default render — handover gate broken" >&2
  exit 1
fi
# It MUST be a post-install + post-upgrade Helm hook so chart upgrades
# also re-fire (catalyst-api handles idempotency).
if ! grep -q '"helm.sh/hook": "post-install,post-upgrade"' "$TMP/render.yaml" ; then
  echo "FAIL: auto-trigger Job missing post-install + post-upgrade hook annotations" >&2
  exit 1
fi
echo "  PASS (auto-trigger Job present + hook-annotated)"

echo "[cutover-contract] Case 9: auto-trigger absent when trigger.auto=false"
# Operator override path — the chart MUST install dormant when an overlay
# disables auto-trigger.
helm template smoke-noauto . --set trigger.auto=false > "$TMP/render-noauto.yaml"
if grep -q 'cutover-auto-trigger' "$TMP/render-noauto.yaml"; then
  echo "FAIL: auto-trigger Job rendered despite trigger.auto=false" >&2
  exit 1
fi
echo "  PASS (auto-trigger gated on trigger.auto)"

echo "[cutover-contract] Case 10: auto-trigger uses /internal/cutover/trigger (#935 Bug 2)"
# Chart 0.1.16 POSTed /api/v1/sovereign/cutover/start which sat behind
# RequireSession middleware and 401'd forever on otech113 2026-05-05.
# 0.1.17 must route through /api/v1/internal/cutover/trigger (lives
# OUTSIDE RequireSession, validates the bearer SA token via TokenReview).
if ! grep -q '/api/v1/internal/cutover/trigger' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT POST /api/v1/internal/cutover/trigger" >&2
  exit 1
fi
echo "  PASS (auto-trigger uses internal endpoint)"

echo "[cutover-contract] Case 11: auto-trigger sends SA bearer token (#935 Bug 2)"
# The Job must mount its projected ServiceAccount token AND send it as
# Authorization: Bearer. Without this the /internal/cutover/trigger
# endpoint will reject the request with 401 missing-bearer.
if ! grep -q '/var/run/secrets/kubernetes.io/serviceaccount/token' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT read its projected SA token" >&2
  exit 1
fi
if ! grep -q 'Authorization: Bearer' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT send Authorization: Bearer header" >&2
  exit 1
fi
echo "  PASS (auto-trigger authenticates via SA token)"

echo "[cutover-contract] Case 12: harbor.adminSecretRef.name is harbor-admin (#935 Bug 1)"
# Chart 0.1.16 referenced `harbor-core` for Step 02 (harbor-projects);
# the upstream Harbor `harbor-core` Secret only exists in `harbor` ns
# and K8s forbids cross-namespace secretKeyRef, so Step 02 hit
# `secret "harbor-core" not found` indefinitely on otech113. 0.1.17
# uses the Catalyst-curated `harbor-admin` Secret which bp-harbor 1.2.14
# emits with Reflector annotations into the `catalyst` namespace.
if ! grep -A3 'name: HARBOR_PASSWORD' "$TMP/render.yaml" | grep -q 'name: harbor-admin'; then
  echo "FAIL: Step 02 PodSpec does NOT reference the Reflector-mirrored harbor-admin Secret" >&2
  exit 1
fi
if grep -A3 'name: HARBOR_PASSWORD' "$TMP/render.yaml" | grep -q 'name: harbor-core$'; then
  echo "FAIL: Step 02 PodSpec still references harbor-core (the broken cross-ns Secret)" >&2
  exit 1
fi
echo "  PASS (Step 02 references harbor-admin)"

echo "[cutover-contract] Case 13: readiness probe targets /healthz, NOT /sovereign/cutover/status (#957)"
# Chart 0.1.17 polled /api/v1/sovereign/cutover/status to test
# "is catalyst-api up yet?" That endpoint sits inside RequireSession
# and returns 401 to every unauthenticated probe from the in-cluster
# Job — the probe treated 401 as "not ready" → loop never broke →
# /internal/cutover/trigger was never called → cutover never fired
# (caught live on otech113 2026-05-05).
#
# 0.1.18 polls /healthz instead (unauthenticated, always 200 when
# the process is up). This gate guards against future regressions
# that re-introduce an auth-gated readiness probe.
if ! grep -q 'healthz_url=' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job missing healthz_url= readiness probe target" >&2
  exit 1
fi
if ! grep -q '/healthz' "$TMP/render.yaml"; then
  echo "FAIL: auto-trigger Job does NOT include the /healthz probe path" >&2
  exit 1
fi
# The readiness loop must NOT poll /sovereign/cutover/status. The
# auto-trigger code intentionally uses the unauthenticated /healthz
# probe; any future re-introduction of a session-cookie-gated probe
# would silently re-break the same way 0.1.17 did. We assert no
# poll-loop variable is set to a /sovereign/cutover/* URL by checking
# that no shell-line of the form `<id>=...sovereign/cutover/status...`
# appears in the rendered Job. The string itself is allowed in
# documentation comments — that's why we anchor on `=` (assignment),
# not just substring presence.
if grep -E '^\s*[a-zA-Z_][a-zA-Z0-9_]*=.*/api/v1/sovereign/cutover/status' "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: auto-trigger Job polls /sovereign/cutover/status as a readiness probe (auth-gated, 401-loops)" >&2
  exit 1
fi
echo "  PASS (readiness probe is /healthz)"

echo "[cutover-contract] Case 14: no early cutoverComplete short-circuit on auth-gated /status (#957)"
# 0.1.17 had a pre-flight 'if cutoverComplete=true exit 0' check that
# parsed the response body of /sovereign/cutover/status. Even if a
# future change moves that check elsewhere, it must NOT depend on a
# response body fetched from an auth-gated endpoint. The /internal/
# cutover/trigger endpoint is itself idempotent (returns 200 with the
# existing snapshot when cutoverComplete=true; cutover_internal.go
# line 279) so no separate pre-read is needed.
if grep -E "grep.*cutoverComplete.*/tmp/status\.json" "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: auto-trigger Job retains the 0.1.17 cutoverComplete pre-read off /tmp/status.json — that file no longer exists" >&2
  exit 1
fi
echo "  PASS (no stale cutoverComplete pre-read)"

echo "[cutover-contract] Case 15: Step-01 gitea-mirror has DNS-readiness probe (#968)"
# 0.1.18 Step-01 fired wget against gitea-http.gitea.svc.cluster.local
# the moment the auto-trigger fired, racing the gitea Pod's endpoint
# publication. One DNS miss returned `wget: bad address` and (combined
# with catalyst-api's backoffLimit=0) terminated the Job permanently
# — which the cutover engine surfaced as a hard cutover failure (caught
# live on otech115 2026-05-05).
#
# 0.1.19 Step-01 prefixes its wget calls with an `nslookup` readiness
# loop (30 x 5s) so the Job tolerates the ~10s endpoint-publish lag
# without burning Pod-restart budget. This gate guards against future
# regressions that drop the loop.
if ! grep -q 'nslookup "${gitea_host}"' "$TMP/render.yaml"; then
  echo "FAIL: Step-01 gitea-mirror missing nslookup readiness probe (#968)" >&2
  exit 1
fi
if ! grep -q 'gitea_host=' "$TMP/render.yaml"; then
  echo "FAIL: Step-01 gitea-mirror missing gitea_host= variable extraction (#968)" >&2
  exit 1
fi
echo "  PASS (Step-01 has DNS readiness probe)"

echo "[cutover-contract] Case 17: Step-06 phase-0 merges harbor.<sov-fqdn> auth into ghcr-pull (#1184)"
# 0.1.23 only patched HelmRepository URLs; the pivoted URLs 401 on
# first reconcile because ghcr-pull lacks auth for harbor.<sov-fqdn>.
# 0.1.24 adds Phase-0 to Step-06 that merges admin:<password> into the
# Secret. Guard against future regressions that drop the merge.
if ! grep -q 'Phase 0: merge harbor' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing Phase 0 (harbor.<sov-fqdn> auth merge into ghcr-pull) (#1184)" >&2
  exit 1
fi
if ! grep -q 'GHCR_PULL_SECRET_NAME' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 PodSpec missing GHCR_PULL_SECRET_NAME env (#1184)" >&2
  exit 1
fi
if ! grep -q 'GHCR_PULL_SECRET_NAMESPACE' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 PodSpec missing GHCR_PULL_SECRET_NAMESPACE env (#1184)" >&2
  exit 1
fi
if ! grep -q 'HARBOR_PASSWORD' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 PodSpec missing HARBOR_PASSWORD env (#1184)" >&2
  exit 1
fi
# RBAC must allow patching Secrets cluster-wide for the merge.
if ! awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml" \
     | grep -B1 -A2 '"secrets"' | grep -E 'verbs:.*"patch"|verbs:.*"update"' >/dev/null; then
  echo "FAIL: ClusterRole missing secrets [update|patch] (Phase-0 will 403) (#1184)" >&2
  exit 1
fi
echo "  PASS (Step-06 Phase-0 ghcr-pull merge wired)"

echo "[cutover-contract] Case 16: Step-06 helmrepository-patches pushes YAML edit to local Gitea (#970)"
# 0.1.19 Step-06 only ran kubectl patch against live HelmRepository
# objects. bootstrap-kit Kustomization reconciled YAML from local
# Gitea every 1m and reverted each patch within ~30s — Step-08
# verify caught 38/38 OFFENDERS (caught live on otech116 2026-05-05).
#
# 0.1.20 Step-06 has a Phase-2 that clones local Gitea, sed-rewrites
# every clusters/_template/bootstrap-kit/*.yaml that declares the
# upstream URL, commits, and pushes. This gate guards against future
# regressions that drop the git-push.
if ! grep -q 'git clone' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing git clone (no Gitea push — patches will be reverted by Flux) (#970)" >&2
  exit 1
fi
if ! grep -q 'git push origin main' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing git push origin main (#970)" >&2
  exit 1
fi
if ! grep -q 'clusters/_template/bootstrap-kit' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing target_dir reference (#970)" >&2
  exit 1
fi
echo "  PASS (Step-06 pushes YAML edit to local Gitea)"

echo "[cutover-contract] Case 18: Step-06 Phase-0 probe is escape-free + has wait-loop (Fix #77, Gap A)"
# 0.1.24 used kubectl jsonpath `{.data['.dockerconfigjson']}` which
# silently returns EMPTY because the leading dot inside bracket-form
# is interpreted as a child accessor. 0.1.25 replaces the probe with
# `kubectl get -o json | jq -r --arg k` and adds a wait-loop +
# flux-system fallback for Reflector lag.
#
# Guard against future regressions that re-introduce the brittle
# jsonpath form against the dockerconfigjson key. The check anchors on
# `-o "jsonpath=` so the documentation example in the inline-comment
# block (which appears verbatim as `-o "jsonpath={.data['.dockerconfigjson']}"`
# in the comment for posterity, prefixed with a `#` and three leading
# spaces of comment indentation) is excluded — comment-prefixed lines
# never start with `kubectl`/`-o` at any indent level recognized by
# this check.
if grep -E "^[[:space:]]*-o[[:space:]]+\"jsonpath=\{\.data\['.dockerconfigjson'\]\}\"" "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: Step-06 Phase-0 still uses brittle jsonpath bracket-form for .dockerconfigjson (Fix #77, Gap A)" >&2
  exit 1
fi
# Must use jq-based read OR escaped jsonpath. Assert the new pattern.
if ! grep -E "jq -r --arg k .* GHCR_PULL_SECRET_KEY|jq -r --arg k \"\\\$\\{GHCR_PULL_SECRET_KEY\\}\"" "$TMP/render.yaml" >/dev/null; then
  # Looser fallback: any `jq -r --arg k` reading .data[$k] is acceptable.
  if ! grep -E "jq -r --arg k .* '\.data\[\\\$k\]" "$TMP/render.yaml" >/dev/null; then
    echo "FAIL: Step-06 Phase-0 missing jq-based read of .data[\$k] (Fix #77, Gap A)" >&2
    exit 1
  fi
fi
# Wait-loop + fallback presence — assert Phase-0 explicitly handles
# Reflector lag without cratering on first miss.
if ! grep -q 'Reflector lag suspected' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-0 missing Reflector-lag fallback to flux-system (Fix #77, Gap A)" >&2
  exit 1
fi
if ! grep -q 'sleep 5' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-0 missing wait-loop sleep (Fix #77, Gap A)" >&2
  exit 1
fi
echo "  PASS (Phase-0 probe escape-free + wait-loop + fallback)"

echo "[cutover-contract] Case 19: Step-09 gitea-token-mint mints a real API token (TBD-C18)"
# Chart <0.1.30 had no Step-09; the catalyst-platform chart's
# `provisioning-github-token.yaml` template mirrored the Gitea admin
# PASSWORD verbatim into Secret sme/provisioning-github-token under key
# GITHUB_TOKEN. The SME provisioning service then sent
# `Authorization: token <PWD>` to Gitea, which 401'd because Gitea
# resolves the Bearer credential as an API access token (sha1 lookup),
# not a password. Result on t22 (2026-05-18): voucher checkout returned
# 200 and redirected to /jobs, but NO Organization CR was ever created
# (every Gitea API call from provisioning 401'd).
#
# Chart 0.1.30 adds Step-09 that:
#   - DELETEs any stale catalyst-platform-bootstrap token (idempotent on
#     re-run; 404 swallowed)
#   - POSTs /api/v1/users/gitea_admin/tokens with scope "all"
#   - Captures the returned .sha1
#   - Validates by calling GET /api/v1/user with `Authorization: token <X>`
#   - kubectl-patches Secret sme/provisioning-github-token.GITHUB_TOKEN
#   - rollout-restarts the provisioning Deployment (best-effort)
#
# Guard against future regressions that drop the token-mint Job.
if ! grep -q 'cutover-step-09-gitea-token-mint' "$TMP/render.yaml"; then
  echo "FAIL: Step-09 gitea-token-mint ConfigMap missing (TBD-C18)" >&2
  exit 1
fi
if ! grep -F '/users/${GITEA_USERNAME}/tokens' "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: Step-09 missing POST /api/v1/users/.../tokens (TBD-C18)" >&2
  exit 1
fi
if ! grep -q '"sha1":"' "$TMP/render.yaml"; then
  echo "FAIL: Step-09 missing sha1 capture from token-mint response (TBD-C18)" >&2
  exit 1
fi
if ! grep -q 'Authorization: token ' "$TMP/render.yaml"; then
  echo "FAIL: Step-09 missing token validation (Authorization: token header) (TBD-C18)" >&2
  exit 1
fi
if ! grep -F 'patch secret "${DEST_SECRET_NAME}"' "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: Step-09 missing kubectl patch of dest Secret (TBD-C18)" >&2
  exit 1
fi
# Step-09 must be order=9 so it lands LAST in the cutover sequence.
# (Order doesn't actually matter for correctness — no other step reads
# the token — but order 9 avoids renumbering 01..08 which would
# invalidate operator history.)
if ! grep -A15 'cutover-step-09-gitea-token-mint' "$TMP/render.yaml" | grep -q 'bp.openova.io/cutover-order: "9"'; then
  echo "FAIL: Step-09 not labelled bp.openova.io/cutover-order=9 (TBD-C18)" >&2
  exit 1
fi
echo "  PASS (Step-09 mints Gitea API token + patches provisioning-github-token)"

echo "[cutover-contract] Case 20: Step-06 Phase -1 waits for Cilium Gateway Programmed=True (#1871)"
# Chart <0.1.32 fired the Step-06 URL rewrite the instant catalyst-api
# stamped the Job. If the Cilium Gateway in kube-system had not yet
# reached Programmed=True (= listener bound + wildcard cert wired),
# every subsequent source-controller pull from `registry.<sov-fqdn>`
# hit TLS handshake EOF, bp-catalyst-platform flipped Ready=False,
# bootstrap-kit + sovereign-tls Kustomizations deadlocked, and the
# Gateway was never installed → infinite chicken-and-egg loop. Hit
# live on t26 zero-touch 2026-05-18 (99bb823cb0513f4b, A55 diag).
#
# Chart 0.1.32 inserts a Phase -1 gateway-wait at the top of the
# Step-06 Job's args script. The Job polls
# `gateway.networking.k8s.io/v1.Gateway cilium-gateway` in
# `kube-system` until status.conditions[Programmed]=True, with a
# 30 min default deadline. If the Gateway never programs, the Job
# exits 1 (surfacing the block to the operator) rather than rewriting
# URLs into a Gateway that won't answer.
#
# The previous fix attempt (PR #1875) added `sovereign-tls` to
# bp-self-sovereign-cutover.dependsOn but Flux HR.dependsOn can ONLY
# reference HelmReleases; sovereign-tls is a Kustomization, so
# helm-controller logged `helmreleases.helm.toolkit.fluxcd.io
# "sovereign-tls" not found` forever (A84 test on t27). Guard against
# any future revival of the cross-kind dependsOn shape.
if ! grep -q 'Phase -1: waiting for Gateway' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase -1 gateway-wait block missing (#1871)" >&2
  exit 1
fi
if ! grep -q 'gateway.gateway.networking.k8s.io' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase -1 missing Gateway CR get/poll (#1871)" >&2
  exit 1
fi
if ! grep -q 'GATEWAY_NAMESPACE' "$TMP/render.yaml" \
   || ! grep -q 'GATEWAY_WAIT_TIMEOUT_SECONDS' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase -1 missing configurable namespace/name/timeout env (#1871)" >&2
  exit 1
fi
# RBAC: ClusterRole must allow get/list/watch on
# gateway.networking.k8s.io/gateways so the in-Job poll resolves.
if ! grep -B0 -A2 'apiGroups: \["gateway.networking.k8s.io"\]' "$TMP/render.yaml" \
     | grep -q 'resources: \["gateways"\]'; then
  echo "FAIL: ClusterRole missing gateway.networking.k8s.io.gateways read verbs (#1871)" >&2
  exit 1
fi
echo "  PASS (Step-06 Phase -1 gateway-wait + RBAC wired)"

echo "[cutover-contract] All gates green."
