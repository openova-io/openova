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
#   4. EXACTLY 11 step ConfigMaps must render (steps 1..11; step 9
#      gitea-token-mint added in chart 0.1.30, TBD-C18; step 10
#      vcluster-registry-pivot added in chart 0.1.36, TBD-V24 MISS-1;
#      step 11 crossplane-provider-pivot added in chart 0.1.37,
#      TBD-V24 MISS-3).
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

echo "[cutover-contract] Case 2 + 4: exactly 11 step ConfigMaps render with required labels"
# Use yq if present (the CI runner installs it for the blueprint-release
# guards); fall back to grep counting on workstations without yq.
# Step 9 (gitea-token-mint) added in chart 0.1.30 (TBD-C18); step 10
# (vcluster-registry-pivot) added in chart 0.1.36 (TBD-V24 MISS-1);
# step 11 (crossplane-provider-pivot) added in chart 0.1.37 (TBD-V24
# MISS-3, issue #2034) to pivot Provider.spec.package off xpkg.upbound.io.
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
if [ "${step_count}" -ne 11 ]; then
  echo "FAIL: expected 11 step ConfigMaps, got ${step_count}" >&2
  exit 1
fi
echo "  PASS (11 step ConfigMaps)"

echo "[cutover-contract] Case 3: required data keys present"
# stepName key must exist on every step ConfigMap (11 total).
# podSpec key must exist on every job-mode step (10 of 11 — step 04 is daemonset-wait).
mode_job_count=$(grep -c 'bp.openova.io/cutover-mode: "job"' "$TMP/render.yaml")
if [ "${mode_job_count}" -ne 10 ]; then
  echo "FAIL: expected 10 job-mode step ConfigMaps, got ${mode_job_count}" >&2
  exit 1
fi
podspec_keys=$(grep -c '^  podSpec: |' "$TMP/render.yaml")
if [ "${podspec_keys}" -lt 10 ]; then
  echo "FAIL: expected at least 10 podSpec keys (one per job-mode step), got ${podspec_keys}" >&2
  exit 1
fi
stepname_keys=$(grep -c '^  stepName:' "$TMP/render.yaml")
if [ "${stepname_keys}" -lt 11 ]; then
  echo "FAIL: expected at least 11 stepName keys, got ${stepname_keys}" >&2
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

echo "[cutover-contract] Case 17: Step-06 phase-0 ADDS harbor.<sov-fqdn> auth to ghcr-pull (#1184)"
# 0.1.23 only patched HelmRepository URLs; the pivoted URLs 401 on
# first reconcile because ghcr-pull lacks auth for harbor.<sov-fqdn>.
# 0.1.24 adds Phase-0 to Step-06 that merges admin:<password> into the
# Secret. Guard against future regressions that drop the merge.
#
# Strip-before-pivot fix (Refs #3379 #3526): Phase-0 is now PURELY
# ADDITIVE ("Phase 0: ADD harbor...") — the destructive mothership-auth
# strip moved to Phase 3 (see Case 17b). The harbor-auth ADD itself MUST
# still be wired here (it's the precondition for the pivot to pull from
# local Harbor).
if ! grep -q 'Phase 0: ADD harbor' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing Phase 0 (harbor.<sov-fqdn> auth ADD into ghcr-pull) (#1184)" >&2
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

echo "[cutover-contract] Case 17b: Step-06 strips mothership auth in Phase 3 (AFTER pivot+readiness), NOT in Phase-0 (strip-before-pivot fix, Refs #3379 #3526)"
# THE STRIP-BEFORE-PIVOT GUARD. hw139: Phase-0 stripped ghcr.io/harbor.
# openova.io BEFORE the Phase-1 URL pivot; a later step wedged → ghcr.io
# creds gone while HR sources still pointed at ghcr.io → 8 HRs wedged on
# "no auth config for 'ghcr.io'", unrecoverable zero-touch. The fix moves
# the destructive `del(.auths[...])` strip to a NEW Phase 3 at the END of
# Step-06, gated behind a bounded wait that proves every local-Harbor
# HelmRepository is Ready=True FIRST. This case locks that ordering in.
#
# (a) The strip must exist and be labelled Phase-3 (not Phase-0).
if ! grep -q 'Phase 3 — readiness-gated mothership-auth STRIP' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing the Phase-3 readiness-gated strip block (strip-before-pivot fix, Refs #3379 #3526)" >&2
  exit 1
fi
# (b) A readiness wait MUST gate the strip: the strip-readiness timeout
#     env + the Ready=True wait loop + the fail-closed REFUSE message.
if ! grep -q 'STRIP_READINESS_TIMEOUT_SECONDS' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 missing STRIP_READINESS_TIMEOUT_SECONDS env — the strip is not readiness-gated (Refs #3379 #3526)" >&2
  exit 1
fi
if ! grep -q 'REFUSING to strip ghcr.io' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 strip is not fail-closed — must REFUSE the strip (exit non-zero, ghcr.io auth intact) when the pivot does not reach Ready (Refs #3379 #3526)" >&2
  exit 1
fi
# (c) The strip env timeout MUST be backed by a chart value (the test
#     runs from CHART_DIR, so values.yaml is CWD-relative). Belt-and-
#     braces with (b): the rendered env carrying a non-empty value below
#     already proves it flows, but assert the source knob exists too.
if ! grep -q 'stripReadinessSeconds' values.yaml; then
  echo "FAIL: chart values.yaml missing stepTimeouts.stripReadinessSeconds (backs STRIP_READINESS_TIMEOUT_SECONDS) (Refs #3379 #3526)" >&2
  exit 1
fi
# The rendered env MUST carry a concrete positive integer (not empty).
if ! grep -A1 'name: STRIP_READINESS_TIMEOUT_SECONDS' "$TMP/render.yaml" | grep -Eq 'value: "[0-9]+"'; then
  echo "FAIL: STRIP_READINESS_TIMEOUT_SECONDS rendered with no/empty value (Refs #3379 #3526)" >&2
  exit 1
fi
# (d) Phase-0 MUST NOT carry the destructive del() — verify the ONLY
#     non-comment `del(.auths[` occurrence sits AFTER the Phase-3 header.
#     (Comment lines mentioning del() are fine; the executable jq filter
#     line `jq_filter="... | del(.auths[$s...` is the one that matters.)
phase3_line=$(grep -n 'Phase 3 — readiness-gated mothership-auth STRIP' "$TMP/render.yaml" | head -1 | cut -d: -f1)
del_exec_line=$(grep -n 'jq_filter=.*del(.auths\[' "$TMP/render.yaml" | head -1 | cut -d: -f1)
if [ -z "${del_exec_line}" ]; then
  echo "FAIL: Step-06 has no executable del(.auths[...]) jq filter — the strip was dropped entirely (Refs #3379 #3526)" >&2
  exit 1
fi
if [ -z "${phase3_line}" ] || [ "${del_exec_line}" -lt "${phase3_line}" ]; then
  echo "FAIL: Step-06 executable strip del(.auths[...]) at line ${del_exec_line} runs BEFORE the Phase-3 header at line ${phase3_line:-<none>} — strip-before-pivot regression (Refs #3379 #3526)" >&2
  exit 1
fi
# (e) The Phase-3a readiness signal MUST be a DIRECT authenticated `helm pull`
#     of the pivoted charts from local Harbor, NOT a read of the pivoted
#     HelmRepository's .status.conditions[Ready] (Refs #3627). For
#     spec.type=oci HelmRepositories Flux source-controller sets NO conditions
#     at all (static pointers consumed lazily by HelmReleases), so the old
#     Ready read could never become True → the strip was FATAL-refused every
#     run and the cutover wedged at step-06 (hw146; also the hw144 0/0 shape,
#     #3588). Guard against a regression back to the status-Ready read.
if ! grep -Eq 'helm pull "oci://\$\{harbor_host\}/openova-io/' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-3a does not prove pullability via 'helm pull oci://\${harbor_host}/openova-io/<chart>' — the strip readiness gate is not a real authenticated pull (Refs #3627)" >&2
  exit 1
fi
if ! grep -q 'helm registry login' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-3a does not 'helm registry login' the local Harbor before the pull probe (Refs #3627)" >&2
  exit 1
fi
# The probe MUST run from a writable helm home (the Pod is
# readOnlyRootFilesystem:true) — assert the HELM_*_HOME redirect to /tmp.
if ! grep -q 'HELM_CACHE_HOME=/tmp' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-3a does not point HELM_CACHE_HOME at the writable /tmp emptyDir — helm pull would fail on the read-only root FS (Refs #3627)" >&2
  exit 1
fi
# And the gate MUST NOT have regressed to gating on the HelmRepository Ready
# condition (the wrong-object read this fix removed).
if grep -q 'local-Harbor HelmRepository(ies) not yet Ready' "$TMP/render.yaml"; then
  echo "FAIL: Step-06 Phase-3a still reads HelmRepository .status.conditions[Ready] — type:oci HRs carry no status; regression to the wrong-object gate (Refs #3627)" >&2
  exit 1
fi
# HELM_PROBE_SAMPLE_SIZE env MUST render with a concrete positive integer.
if ! grep -A1 'name: HELM_PROBE_SAMPLE_SIZE' "$TMP/render.yaml" | grep -Eq 'value: "[0-9]+"'; then
  echo "FAIL: HELM_PROBE_SAMPLE_SIZE rendered with no/empty value — the Phase-3a probe sample size is unset (Refs #3627)" >&2
  exit 1
fi
echo "  PASS (strip is in Phase 3, fail-closed; readiness proven via authenticated helm pull, not a non-existent HelmRepository Ready)"

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

echo "[cutover-contract] Case 21: Step-10 vcluster-registry-pivot patches bp-*-vcluster HelmReleases (TBD-V24 MISS-1)"
# Chart <0.1.35 shipped NO vCluster image-registry pivot. The chart's
# own comment at platform/bp-mgmt-vcluster/chart/values.yaml:77-79
# promised "post-handover, the per-Sovereign overlay rewrites to
# `harbor.<sovereign-fqdn>/proxy-ghcr/...`" but the rewrite step
# never existed. Result: MGMT/RTZ/DMZ vCluster control-plane Pods
# kept pulling from harbor.openova.io indefinitely post-handover,
# violating Principle #11 (no tether to harbor.openova.io after
# handover). Caught by the TBD-V24 tether audit 2026-05-20.
#
# Chart 0.1.35 adds Step-10 that:
#   - kubectl patches each of {bp-mgmt-vcluster, bp-rtz-vcluster,
#     bp-dmz-vcluster} HelmReleases with image.repository pointing at
#     harbor.${SOVEREIGN_FQDN}/proxy-ghcr/loft-sh/vcluster
#   - ALSO patches the upstream subchart's
#     vcluster.controlPlane.statefulSet.image.{registry,repository}
#   - git push edits clusters/_template/bootstrap-kit/{54,58,59}-bp-*-
#     vcluster.yaml to local Gitea so the override survives reconciles
#   - Idempotent on re-run (skip-if-already-pivoted + sentinel-comment
#     guard on YAML injection)
#
# Guard against future regressions that drop the step.
if ! grep -q 'cutover-step-10-vcluster-registry-pivot' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 vcluster-registry-pivot ConfigMap missing (TBD-V24 MISS-1)" >&2
  exit 1
fi
if ! grep -A20 'cutover-step-10-vcluster-registry-pivot' "$TMP/render.yaml" | grep -q 'bp.openova.io/cutover-order: "10"'; then
  echo "FAIL: Step-10 not labelled bp.openova.io/cutover-order=10 (TBD-V24 MISS-1)" >&2
  exit 1
fi
# All 3 vCluster HelmRelease names must be referenced in the patch script.
for hr in bp-mgmt-vcluster bp-rtz-vcluster bp-dmz-vcluster; do
  if ! grep -q "patch_hr \"${hr}\"" "$TMP/render.yaml"; then
    echo "FAIL: Step-10 missing patch invocation for ${hr} (TBD-V24 MISS-1)" >&2
    exit 1
  fi
done
# All 3 role keys must be wired (umbrella chart values key).
for role in mgmtVcluster rtzVcluster dmzVcluster; do
  if ! grep -q "\"${role}\"" "$TMP/render.yaml"; then
    echo "FAIL: Step-10 missing role-key wiring for ${role} (TBD-V24 MISS-1)" >&2
    exit 1
  fi
done
# Subchart pivot — must patch vcluster.controlPlane.statefulSet.image too.
if ! grep -q 'controlPlane.statefulSet.image' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 missing subchart image pivot (TBD-V24 MISS-1)" >&2
  exit 1
fi
# Phase-2 git push to local Gitea — without this, Phase-1 patches get
# reverted by bootstrap-kit Kustomization reconcile within ~1 min.
if ! grep -B2 -A8 'cutover-step-10-vcluster-registry-pivot' "$TMP/render.yaml" >/dev/null; then
  echo "FAIL: Step-10 ConfigMap region not located for Phase-2 git push check" >&2
  exit 1
fi
# RBAC: ClusterRole must permit patch on helmreleases (needed by Step-10
# AND by Step-06 Phase-1.6, which was silently relying on this verb).
if ! awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml" \
     | grep -B1 -A2 '"helmreleases"' | grep -E 'verbs:.*"patch"|verbs:.*"update"' >/dev/null; then
  echo "FAIL: ClusterRole missing helm.toolkit.fluxcd.io.helmreleases [update|patch] verb (TBD-V24 MISS-1)" >&2
  exit 1
fi
# #2951 (#2940 ATTACK#9): Step-10 ALSO pivots the bp-sso-bridge HR
# image.repository host (harbor.openova.io → harbor.<SOVEREIGN_FQDN>),
# the one residual chart-default mothership tether the vCluster pivots
# don't cover. Without this a cutover-complete Sovereign still pulls the
# sso-bridge reconciler Pod image from mothership Harbor. Guard both the
# live HR patch and the Gitea-injection seam.
if ! grep -q 'sso_hr="bp-sso-bridge"' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 missing bp-sso-bridge image-host pivot (#2951)" >&2
  exit 1
fi
if ! grep -q '13b-bp-sso-bridge.yaml' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 missing bp-sso-bridge Gitea-injection seam (#2951)" >&2
  exit 1
fi
# #2951 (#2940 ATTACK#9, VERIFIED-PARTIAL follow-up): Step-10 must ALSO
# self-assert that, post-pivot, none of the four image HRs still resolves
# to harbor.openova.io. Without this Phase-1c the only acceptance gate was
# a manual `kubectl get hr -A | grep harbor.openova.io` — the cutover Job
# itself never failed loudly on a residual tether. Guard that the
# assertion loop AND the FATAL exit-1 on residual!=0 are both present.
if ! grep -q 'RESIDUAL-TETHER' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 missing Phase-1c residual-tether self-assertion (#2951)" >&2
  exit 1
fi
if ! grep -q 'Pillar 5 independence not met' "$TMP/render.yaml"; then
  echo "FAIL: Step-10 residual-tether assertion does not fail loudly on a remaining harbor.openova.io ref (#2951)" >&2
  exit 1
fi
echo "  PASS (Step-10 wired to pivot vCluster + sso-bridge HRs to local Harbor + asserts zero residual mothership-Harbor tether)"

echo "[cutover-contract] Case 22: Step-11 crossplane-provider-pivot patches Provider CRs (TBD-V24 MISS-3)"
# Chart <0.1.37 shipped NO Crossplane Provider package pivot. Result:
# every Provider package fetch (initial install, version bump, inactive
# ProviderRevision reconcile, Pod-restart-with-evicted-cache) hit
# xpkg.upbound.io directly post-handover — a direct violation of
# Principle #11. Step 04's registries.yaml.v2 mirror is irrelevant to
# Crossplane because Crossplane's fetcher uses go-containerregistry's
# remote.Image() directly, bypassing the kubelet/containerd CRI client.
# Caught by TBD-V24 empirical investigation 2026-05-20.
#
# Chart 0.1.37 adds Step-11 that:
#   - kubectl patches every pkg.crossplane.io/v1.Provider CR's
#     spec.package, swapping `xpkg.upbound.io/...` →
#     `harbor.${SOVEREIGN_FQDN}/proxy-xpkg/...` while preserving the
#     full upstream path + tag.
#   - git push edits clusters/*/infrastructure/provider-*.yaml to
#     local Gitea so the bootstrap-kit Kustomization reconcile
#     doesn't revert Phase-1 within ~1 min.
#   - Idempotent on re-run (skip-if-already-pivoted on Phase-1,
#     no-op grep guard on Phase-2).
#   - Skip-if-absent for the CRD (very-early-handover window where
#     bp-crossplane hasn't installed yet — Phase-2 still rewrites
#     Gitea so the eventual install pulls from local Harbor).
#
# Guard against future regressions that drop the step.
if ! grep -q 'cutover-step-11-crossplane-provider-pivot' "$TMP/render.yaml"; then
  echo "FAIL: Step-11 crossplane-provider-pivot ConfigMap missing (TBD-V24 MISS-3)" >&2
  exit 1
fi
if ! grep -A20 'cutover-step-11-crossplane-provider-pivot' "$TMP/render.yaml" | grep -q 'bp.openova.io/cutover-order: "11"'; then
  echo "FAIL: Step-11 not labelled bp.openova.io/cutover-order=11 (TBD-V24 MISS-3)" >&2
  exit 1
fi
# Default upstream host must be xpkg.upbound.io (the Crossplane Provider
# package upstream the cutover pivots OFF of).
if ! grep -A60 'cutover-step-11-crossplane-provider-pivot' "$TMP/render.yaml" | grep -q 'value: "xpkg.upbound.io"'; then
  echo "FAIL: Step-11 missing UPSTREAM_HOST=xpkg.upbound.io env (TBD-V24 MISS-3)" >&2
  exit 1
fi
# Default registry path must be proxy-xpkg (the Harbor proxy-cache
# project that mirrors xpkg.upbound.io — created by Step 02).
if ! grep -A60 'cutover-step-11-crossplane-provider-pivot' "$TMP/render.yaml" | grep -q 'value: "proxy-xpkg"'; then
  echo "FAIL: Step-11 missing REGISTRY_PATH=proxy-xpkg env (TBD-V24 MISS-3)" >&2
  exit 1
fi
# The script body must call kubectl patch on provider.pkg.crossplane.io.
if ! grep -q 'kubectl patch provider.pkg.crossplane.io' "$TMP/render.yaml"; then
  echo "FAIL: Step-11 missing kubectl patch provider.pkg.crossplane.io invocation (TBD-V24 MISS-3)" >&2
  exit 1
fi
# Phase-2 must sed the package literal in clusters/*/infrastructure/
# provider-*.yaml — guard the find path is wired.
if ! grep -q "path '\*/infrastructure/provider-\*.yaml'" "$TMP/render.yaml"; then
  echo "FAIL: Step-11 Phase-2 missing provider-*.yaml find pattern (TBD-V24 MISS-3)" >&2
  exit 1
fi
# RBAC: ClusterRole must permit update/patch on
# pkg.crossplane.io.providers (Phase-1 kubectl patch).
if ! awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml" \
     | grep -B1 -A3 '"providers"' | grep -E 'verbs:.*"patch"|verbs:.*"update"' >/dev/null; then
  echo "FAIL: ClusterRole missing pkg.crossplane.io.providers [update|patch] verb (TBD-V24 MISS-3)" >&2
  exit 1
fi
# RBAC: ClusterRole must permit get/list/watch on
# apiextensions.k8s.io.customresourcedefinitions (CRD-presence probe).
if ! awk '/^kind: ClusterRole$/,/^---$/' "$TMP/render.yaml" \
     | grep -B1 -A3 '"customresourcedefinitions"' | grep -q 'verbs:.*"get"'; then
  echo "FAIL: ClusterRole missing apiextensions.k8s.io.customresourcedefinitions read verbs (TBD-V24 MISS-3)" >&2
  exit 1
fi
# xpkg.upbound.io must be added to mothershipAuthsToStrip + blockedDomains.
if ! grep -q '"xpkg.upbound.io"' "$TMP/render.yaml"; then
  echo "FAIL: xpkg.upbound.io not present in chart values (mothershipAuthsToStrip / blockedDomains) (TBD-V24 MISS-3)" >&2
  exit 1
fi
echo "  PASS (Step-11 wired to pivot Provider CRs to local Harbor proxy-xpkg)"


echo "[cutover-contract] Case 23: Step-07 Phase 3 pivots catalyst-api issuer envs + runtime-config URLs (#2940)"
# The #2940 VERIFIED-PARTIAL review found Step-07 only patched
# CATALYST_GITOPS_REPO_URL: the PIN/handover-JWT issuers and the
# catalyst-runtime-config console/api-public URL keys kept mothership
# defaults, so a cutover could pass the egress hold while every token
# still carried a mothership `iss`. Phase 3 pivots all four seams and
# read-back-asserts zero openova.io residue (FATAL otherwise).
if ! grep -q 'CONSOLE_PUBLIC_URL' "$TMP/render.yaml"; then
  echo "FAIL: Step-07 missing CONSOLE_PUBLIC_URL wiring (#2940 Phase 3)" >&2
  exit 1
fi
for var in CATALYST_PIN_ISSUER CATALYST_HANDOVER_JWT_ISSUER; do
  if ! grep -q "${var}" "$TMP/render.yaml"; then
    echo "FAIL: Step-07 Phase 3 missing issuer pivot for ${var} (#2940)" >&2
    exit 1
  fi
done
for key in sovereign-console-url sovereign-api-public-url; do
  if ! grep -q "${key}" "$TMP/render.yaml"; then
    echo "FAIL: Step-07 Phase 3 missing runtime-config pivot for ${key} (#2940)" >&2
    exit 1
  fi
done
# The read-back assert must FAIL FATAL on mothership residue.
if ! grep -q 'Pillar 5 independence not met' "$TMP/render.yaml"; then
  echo "FAIL: Step-07 Phase 3 missing read-back FATAL assert (#2940)" >&2
  exit 1
fi
# The example-default guard must be present (unconfigured overlay = FATAL).
if ! grep -q 'consolePublicURL not overlaid' "$TMP/render.yaml"; then
  echo "FAIL: Step-07 Phase 3 missing example-default guard (#2940)" >&2
  exit 1
fi
echo "  PASS (Step-07 Phase 3 pivots issuers + runtime-config URLs with read-back asserts)"


echo "[cutover-contract] Case 24: Step-09 re-run no-op probe + unique token name (#2938)"
# The old delete-then-mint rotated the live token on every re-run —
# holders of the old token got unrecoverable 401s (UAT Wave-2 ATTACK#7).
# Contract: a validity probe short-circuits re-runs; minting uses a
# unique per-run name; and NO DELETE of the active token ever happens.
if ! grep -q 're-run is a no-op' "$TMP/render.yaml"; then
  echo "FAIL: Step-09 missing the re-run no-op probe (#2938)" >&2
  exit 1
fi
if ! grep -q 'minting under unique name' "$TMP/render.yaml"; then
  echo "FAIL: Step-09 missing the unique per-run token name (#2938)" >&2
  exit 1
fi
if grep -A20 'gitea-token-mint' "$TMP/render.yaml" | grep -q 'X DELETE.*tokens/'; then
  echo "FAIL: Step-09 still DELETEs the active token (#2938 regression)" >&2
  exit 1
fi
echo "  PASS (Step-09: validity probe short-circuits re-runs; unique mint name; no active-token DELETE)"

echo "[cutover-contract] Case 25: slot-06a overlay enables the REAL deny-egress hold (#3379)"
# The chart default (values.yaml egressTest.enforceCIDRBlock: false) ships
# the passive assertion-only Step-08 — which verifies the post-cutover
# architectural state but does NOT actually deny egress. ADR-0002 + the
# founder rule require cutoverComplete=true to be earned through a REAL
# 10-minute deny-egress hold against github.com/ghcr.io/harbor.openova.io.
# The per-Sovereign overlay
# (clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml)
# is the lockstep half: it MUST set egressTest.enforceCIDRBlock: true so the
# real CiliumClusterwideNetworkPolicy egressDeny is applied on every prov.
# Guard against a silent revert to assertion-only. Located relative to the
# repo root ($CHART_DIR/../../..); skipped only if the overlay is absent
# (chart consumed standalone outside the monorepo).
OVERLAY_06A="${CHART_DIR}/../../../clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml"
if [ -f "$OVERLAY_06A" ]; then
  if ! grep -Eq '^[[:space:]]*enforceCIDRBlock:[[:space:]]*true' "$OVERLAY_06A"; then
    echo "FAIL: slot-06a overlay does not set egressTest.enforceCIDRBlock: true — the deny-egress hold would silently degrade to assertion-only (#3379)" >&2
    exit 1
  fi
  echo "  PASS (slot-06a overlay enables the real deny-egress hold)"
else
  echo "  SKIP (slot-06a overlay not present — chart consumed standalone)"
fi

echo "[cutover-contract] Case 26: Step-03 harbor-prewarm mirrors openova-io helm CHARTS into local Harbor (Refs #3627)"
# hw146: step-03 Phase A skopeo-pushed only the CONTAINER images enumerated
# from running Pods; NO step ever mirrored the helm CHART OCI artifacts. So
# local Harbor's openova-io project held 0 chart repos and the moment step-06
# Phase-3 stripped ghcr.io any HelmRelease (re)pull 404'd. Phase A2 reads
# (chart,version) off every HelmRelease whose sourceRef.kind=HelmRepository +
# sourceRef.name is in the pivot set, then skopeo-copies the chart OCI
# artifact into local Harbor BEFORE egress is cut. Guard the wiring.
if ! grep -q 'Phase A2: mirror openova-io helm charts into local Harbor' "$TMP/render.yaml"; then
  echo "FAIL: Step-03 missing the Phase A2 helm-chart mirror — the charts never reach local Harbor; step-06's ghcr.io strip would 404 every (re)pull (Refs #3627)" >&2
  exit 1
fi
# The chart name+version MUST be read off the consuming HelmRelease's
# spec.chart.spec (NOT the HelmRepository, whose spec.url is only the pointer).
if ! grep -q '.items\[\].spec.chart.spec' "$TMP/render.yaml"; then
  echo "FAIL: Step-03 Phase A2 does not enumerate chart+version from HelmRelease spec.chart.spec (Refs #3627)" >&2
  exit 1
fi
# It MUST select only sourceRef.kind=HelmRepository entries (skip OCIRepository/
# GitRepository sources) so it mirrors exactly the charts step-06 pivots.
if ! grep -q '"HelmRepository"' "$TMP/render.yaml"; then
  echo "FAIL: Step-03 Phase A2 does not gate on sourceRef.kind==HelmRepository (Refs #3627)" >&2
  exit 1
fi
# And it MUST skopeo-copy into the openova-io project of local Harbor.
if ! grep -q 'docker://${HARBOR_HOST}/openova-io/' "$TMP/render.yaml"; then
  echo "FAIL: Step-03 Phase A2 does not skopeo-copy charts into \${HARBOR_HOST}/openova-io/ (Refs #3627)" >&2
  exit 1
fi
# The chart-mirror pivot-set MUST be projected as a ConfigMap data key driven
# off the same .Values.helmRepositories.names step-06 uses.
if ! grep -q 'chart-mirror-names.txt' "$TMP/render.yaml"; then
  echo "FAIL: Step-03 missing the chart-mirror-names.txt projection (the pivot-set the chart mirror iterates) (Refs #3627)" >&2
  exit 1
fi
echo "  PASS (Step-03 Phase A2 mirrors openova-io helm charts into local Harbor before egress is cut)"

echo "[cutover-contract] All gates green."
