#!/usr/bin/env bash
# creds-resync-probes.sh — prove the creds-resync sidecar carries BOTH a
# livenessProbe and a readinessProbe, so the per-Org agenity StatefulSet is
# ADMITTED by the kyverno probes-present ClusterPolicy.
#
# WHY THIS TEST EXISTS (#6513)
#
#   0.5.31 introduced the creds-resync sidecar (templates/statefulset.yaml,
#   under anthropic.credentialResync.enabled — default true) with NEITHER
#   probe. The probes-present policy (platform/kyverno-policies K1) DENIES a
#   Deployment/StatefulSet/DaemonSet unless EVERY container declares both a
#   livenessProbe AND a readinessProbe — its rule is a JMESPath count equality:
#       containers[?livenessProbe]  | length(@)  ==  containers | length(@)
#       containers[?readinessProbe] | length(@)  ==  containers | length(@)
#   So one probe-less sidecar denied the whole StatefulSet at admission and a
#   per-Org agenity install never came up (proven live, hw302). None of that is
#   visible from "the chart renders" — the render is fine; admission is not.
#
#   This test renders the chart and asserts the SAME invariant the policy
#   evaluates: with the sidecar enabled, every container in the StatefulSet
#   carries both probes. Case 5 is a VACUITY control — it strips the sidecar's
#   probes back out of the render and proves the invariant then FAILS, so a
#   future edit that drops a probe cannot pass this test green.
#
# Runs under the release/PR toolchain (helm + mikefarah yq, both pinned). It
# renders ONLY templates/statefulset.yaml (a single document), so it never
# touches the multi-document `// "alt"` yq-version skew that cost a published
# chart (#6235).
#
# Refs #6513 #6317 #3988
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
yq="${YQ_BIN:-yq}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0
note() { printf '  %s\n' "$*"; }
ok()   { printf 'PASS  %s\n' "$*"; }
bad()  { printf 'FAIL  %s\n' "$*"; fail=1; }

# Render the StatefulSet only (single doc). credentialsKey is what makes the
# init container + the anthropic-creds mount + the resync sidecar render at
# all; credentialResync.enabled is the sidecar's own gate (default true, set
# explicitly here so the test states its own preconditions).
render() { # render <extra --set args...> -> stdout
  "$helm" template ag "$chart_dir" \
    --set anthropic.credentialsKey=credentialsJson \
    --api-versions "cilium.io/v2" \
    --api-versions "external-secrets.io/v1beta1" \
    --show-only templates/statefulset.yaml \
    "$@" 2>/dev/null
}

# The exact count the kyverno policy computes. Prints "total liveness readiness".
counts() { # counts <file>
  local f="$1"
  printf '%s %s %s' \
    "$("$yq" '.spec.template.spec.containers | length' "$f")" \
    "$("$yq" '[.spec.template.spec.containers[] | select(has("livenessProbe"))]  | length' "$f")" \
    "$("$yq" '[.spec.template.spec.containers[] | select(has("readinessProbe"))] | length' "$f")"
}

# ── 1. with the sidecar enabled, the creds-resync container renders ────────
ENABLED="$WORK/enabled.yaml"
render --set anthropic.credentialResync.enabled=true > "$ENABLED"

if [ "$("$yq" '[.spec.template.spec.containers[] | select(.name == "creds-resync")] | length' "$ENABLED")" = "1" ]; then
  ok "the creds-resync sidecar renders when credentialResync.enabled=true"
else
  bad "the creds-resync sidecar did NOT render — this test would be asserting on a container the chart does not ship"
  echo "── rendered containers ──"; "$yq" '.spec.template.spec.containers[].name' "$ENABLED" || true
  echo; [ "$fail" -eq 0 ] || exit 1
fi

# ── 2. the sidecar carries BOTH probes ─────────────────────────────────────
# This is the actual fix: the container the kyverno policy rejected now has
# each probe. `has()` is handler-agnostic (exec/httpGet/tcpSocket all count).
cr_live="$("$yq"  '.spec.template.spec.containers[] | select(.name == "creds-resync") | has("livenessProbe")'  "$ENABLED")"
cr_ready="$("$yq" '.spec.template.spec.containers[] | select(.name == "creds-resync") | has("readinessProbe")' "$ENABLED")"
[ "$cr_live"  = "true" ] && ok "creds-resync declares a livenessProbe"  || bad "creds-resync has NO livenessProbe — probes-present would DENY the StatefulSet"
[ "$cr_ready" = "true" ] && ok "creds-resync declares a readinessProbe" || bad "creds-resync has NO readinessProbe — probes-present would DENY the StatefulSet"

# The sidecar is a headless copy-forward loop with NO HTTP port, so an httpGet
# probe would point at nothing. Assert both probes use the exec handler — a
# regression to httpGet/tcpSocket here would pass Case 2 (has() is true) while
# probing a port that does not exist.
cr_live_exec="$("$yq"  '.spec.template.spec.containers[] | select(.name == "creds-resync") | .livenessProbe  | has("exec")' "$ENABLED")"
cr_ready_exec="$("$yq" '.spec.template.spec.containers[] | select(.name == "creds-resync") | .readinessProbe | has("exec")' "$ENABLED")"
[ "$cr_live_exec"  = "true" ] && ok "creds-resync livenessProbe is exec (no HTTP port on this sidecar)"  || bad "creds-resync livenessProbe is NOT exec — this sidecar has no HTTP/TCP port to probe"
[ "$cr_ready_exec" = "true" ] && ok "creds-resync readinessProbe is exec (no HTTP port on this sidecar)" || bad "creds-resync readinessProbe is NOT exec — this sidecar has no HTTP/TCP port to probe"

# ── 3. the kyverno probes-present invariant holds (EVERY container) ─────────
# The policy does not single out the sidecar — it requires ALL containers to
# carry both probes. Assert the count equality directly.
read -r total live ready <<<"$(counts "$ENABLED")"
note "containers=$total  with-livenessProbe=$live  with-readinessProbe=$ready"
if [ "$total" = "$live" ] && [ "$total" = "$ready" ]; then
  ok "every container carries both probes — kyverno probes-present would ADMIT the StatefulSet"
else
  bad "not every container carries both probes ($live/$ready of $total) — kyverno probes-present would DENY the StatefulSet"
fi

# ── 4. the sidecar-DISABLED path still admits ──────────────────────────────
# credentialResync.enabled=false drops the sidecar entirely; the sole chepherd
# container already carries /healthz probes, so the invariant must still hold.
DISABLED="$WORK/disabled.yaml"
render --set anthropic.credentialResync.enabled=false > "$DISABLED"
if [ "$("$yq" '[.spec.template.spec.containers[] | select(.name == "creds-resync")] | length' "$DISABLED")" = "0" ]; then
  ok "credentialResync.enabled=false drops the creds-resync sidecar (probes render iff the sidecar does)"
else
  bad "the creds-resync sidecar rendered even with credentialResync.enabled=false"
fi
read -r d_total d_live d_ready <<<"$(counts "$DISABLED")"
if [ "$d_total" = "$d_live" ] && [ "$d_total" = "$d_ready" ]; then
  ok "the sidecar-disabled StatefulSet also satisfies probes-present ($d_total/$d_total)"
else
  bad "the sidecar-disabled StatefulSet fails probes-present ($d_live/$d_ready of $d_total)"
fi

# ── 5. VACUITY — the invariant check must be able to FAIL ───────────────────
# If Case 3 passed only because the counter can never diverge, it proves
# nothing. Strip the sidecar's probes back out of the enabled render and
# require the SAME count equality to now report a mismatch.
STRIPPED="$WORK/stripped.yaml"
"$yq" '(.spec.template.spec.containers[] | select(.name == "creds-resync")) |= (del(.livenessProbe) | del(.readinessProbe))' "$ENABLED" > "$STRIPPED"
read -r s_total s_live s_ready <<<"$(counts "$STRIPPED")"
if [ "$s_total" != "$s_live" ] || [ "$s_total" != "$s_ready" ]; then
  ok "vacuity: removing the sidecar's probes makes the invariant FAIL ($s_live/$s_ready of $s_total) — the check is real"
else
  bad "vacuity: the invariant still passed after the sidecar's probes were removed — the check is decorative"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "FAIL — the creds-resync sidecar does not satisfy the probes-present invariant."
  exit 1
fi
echo "PASS — creds-resync carries both exec probes; every container satisfies kyverno probes-present."
