#!/usr/bin/env bash
# apicall-rbac-correspondence.sh — #6832 (generalises #5505).
#
# A Kyverno policy that resolves a context via `apiCall` needs RBAC for the
# TARGET apiGroup. Kyverno's built-in aggregated roles cover core + a short
# list; anything else must be granted by this chart, or the context never
# resolves and the rule reports `error` forever — pass/fail never happens.
#
# That failure is SILENT at `action: Audit` (the request proceeds) and becomes
# a cluster-wide outage the moment the policy is flipped to Enforce behind the
# fail-closed webhook. #5505 was exactly that on cilium.io: harmless at Audit,
# then every Namespace create denied. #6832 was the same defect on
# storage.k8s.io, unnoticed for as long as it shipped.
#
# So this asserts correspondence: for every apiGroup an apiCall targets, some
# ClusterRole in this chart (or Kyverno's own defaults) grants read on it.
set -euo pipefail
chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
render="$("$helm" template kp "$chart_dir" --api-versions kyverno.io/v1 2>/dev/null)"
fail() { echo "FAIL: $*" >&2; exit 1; }

# apiGroups Kyverno's own aggregated roles already cover (no chart grant needed).
BUILTIN='^(|v1|apps|batch|networking\.k8s\.io|rbac\.authorization\.k8s\.io|apiextensions\.k8s\.io)$'

# 1. every apiCall urlPath -> its apiGroup
groups="$(grep -oE 'urlPath: *"?/apis/[a-z0-9.-]+/' <<<"$render" \
          | sed -E 's|.*/apis/([a-z0-9.-]+)/|\1|' | sort -u)"
# a /api/v1/... call is core; record it as core so the loop below skips it
core_calls="$(grep -cE 'urlPath: *"?/api/v1/' <<<"$render" || true)"

# 2. every apiGroup this chart grants
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
printf '%s' "$render" > "$tmp/render.yaml"
granted="$(python3 "$(dirname "$0")/_apicall_groups.py" "$tmp/render.yaml")"

# 3. vacuity control — the check must be looking at something
[ -n "$groups" ] || fail "no apiCall urlPath found in the rendered chart — this check is inspecting nothing (did the render break?)"

missing=""
while read -r g; do
  [ -z "$g" ] && continue
  if [[ "$g" =~ $BUILTIN ]]; then continue; fi
  grep -qx "$g" <<<"$granted" || missing="$missing $g"
done <<<"$groups"

[ -z "$missing" ] || fail "#6832: apiCall targets these apiGroups with NO ClusterRole in this chart granting read:$missing — the context will never resolve and the rule will report 'error' forever (silent at Audit, cluster-wide denial at Enforce)"

echo "PASS: every apiCall apiGroup has a matching grant (apiCall groups:$(tr '\n' ' ' <<<"$groups")| core calls: $core_calls)"
