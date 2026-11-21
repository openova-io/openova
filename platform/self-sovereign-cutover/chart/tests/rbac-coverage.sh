#!/usr/bin/env bash
# #6280 — every resource the cutover step scripts READ must be GRANTED.
#
# WHY THIS EXISTS. 0.1.185 (#6273) added `kubectl get namespaces -l
# openova.io/organization` to harbor-prewarm's Phase A0 and did not add a
# `namespaces` rule to the runner ClusterRole. The API server refused the list,
# and the gate — correctly refusing to fail open on a roll state it cannot read
# — stopped the cutover at the very step the release was written to unblock.
#
# WHY THE EXISTING GUARD DID NOT CATCH IT. tests/cutover-contract.sh Case 95
# executes the rendered Phase A0 block against a STUBBED kubectl. The stub
# answers every call, including the namespace listing, so all eight assertion
# directions went green while the real ServiceAccount was being refused. That
# suite proves the gate's LOGIC. Nothing proved the gate's GRANTS. A stub can
# never be refused by RBAC, so this class of defect is structurally invisible to
# it — which is exactly the "guard tested a surface that cannot fail" shape.
#
# WHAT THIS ASSERTS. Render the chart, extract every resource the embedded step
# scripts actually pass to `kubectl get` (and to the srp_kubectl_json wrapper),
# and require each one to appear in the ClusterRole's resource lists. Both
# inputs come from the RENDER, never from a copy kept in this file — #5646
# showed a suite validating a hand-kept copy that had silently drifted from the
# shipped source and went on passing after the real thing changed.
#
# PROVEN ABLE TO FAIL. The last case deletes the `namespaces` rule from the
# rendered ClusterRole and requires this guard to go RED. A coverage check that
# has only ever been observed passing is not yet known to be a check.
#
# GROUP AND RESOURCE TOGETHER (Cases 6 and 7). A name-only match let the read of
# workspaces.opentofu.m.upbound.io pass on the rule of opentofu.upbound.io. Case 6
# judges every read as a group/resource pair, Case 7 drops that one group and requires RED.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw296.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

echo "== #6280 RBAC coverage — every resource read must be granted =="

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# ── The extractor, shared by the live run and the mutation control ───────────
cat >"${TMP}/extract.py" <<'PYEOF'
import re, sys, yaml

render_path, role_path = sys.argv[1], sys.argv[2]
render = open(render_path, encoding="utf-8", errors="replace").read()

# Tokens that follow `get`/the wrapper but are not resources. Each is listed
# with its reason; the list is deliberately tiny and must stay that way — a
# growing ignore list is how a coverage guard quietly stops covering.
IGNORE = {
    "-o", "-n", "-A", "-l", "--raw", "--all-namespaces",   # flags
    "po", "no",                                            # short aliases; long form is asserted
}

def resources_from(text):
    found = set()
    # `kubectl [flags] get <resource[,resource...]>` and the Phase A0 wrapper
    # `srp_kubectl_json <resource>`.
    pats = [
        r"kubectl\s+(?:-[^\s]+\s+|--[^\s]+\s+|[a-z-]+=[^\s]+\s+)*get\s+([A-Za-z][A-Za-z0-9.,-]*)",
        r"srp_kubectl_json\s+([A-Za-z][A-Za-z0-9.,-]*)",
    ]
    for p in pats:
        for m in re.finditer(p, text):
            for tok in m.group(1).split(","):
                tok = tok.strip()
                if not tok or tok in IGNORE or "$" in tok or "{" in tok:
                    continue
                # providers.pkg.crossplane.io -> providers
                found.add(tok.split(".")[0].lower())
    return found

read = resources_from(render)

granted = set()
for doc in yaml.safe_load_all(open(role_path, encoding="utf-8", errors="replace")):
    if not doc or doc.get("kind") not in ("ClusterRole", "Role"):
        continue
    for rule in doc.get("rules") or []:
        for r in rule.get("resources") or []:
            granted.add(r.split("/")[0].lower())

# kubectl accepts singular and short forms; RBAC only ever names the plural.
# Normalising here is what keeps the guard honest in BOTH directions: without
# it every `kubectl get pod` reads as a missing grant (noise that would get the
# guard muted), and with a blanket ignore list instead, a genuinely missing
# grant would be muted too.
ALIAS = {
    "po": "pods", "no": "nodes", "cm": "configmaps", "svc": "services",
    "deploy": "deployments", "ds": "daemonsets", "sts": "statefulsets",
    "rs": "replicasets", "pvc": "persistentvolumeclaims",
    "pv": "persistentvolumes", "ns": "namespaces", "sa": "serviceaccounts",
    "hr": "helmreleases", "crd": "customresourcedefinitions",
    "ep": "endpoints", "netpol": "networkpolicies", "cnp": "ciliumnetworkpolicies",
}

def is_granted(tok):
    for cand in (tok, ALIAS.get(tok), tok + "s", tok + "es",
                 (tok[:-1] + "ies") if tok.endswith("y") else None):
        if cand and cand in granted:
            return True
    return False

missing = sorted(r for r in read if not is_granted(r))
print("READ=%d GRANTED=%d MISSING=%d" % (len(read), len(granted), len(missing)))
for r in missing:
    print("MISSING %s" % r)
PYEOF

# Isolate the ClusterRole/Role documents from the render.
python3 - "${TMP}/render.yaml" "${TMP}/role.yaml" <<'PYEOF'
import sys, yaml
src, dst = sys.argv[1], sys.argv[2]
out = [d for d in yaml.safe_load_all(open(src, encoding="utf-8", errors="replace"))
       if d and d.get("kind") in ("ClusterRole", "Role")]
yaml.safe_dump_all(out, open(dst, "w", encoding="utf-8"))
print("extracted %d Role/ClusterRole document(s)" % len(out))
PYEOF

# ── Case 1 — the render must carry a ClusterRole at all (vacuity control) ────
if [ -s "${TMP}/role.yaml" ]; then
  pass "render carries at least one Role/ClusterRole document"
else
  fail "render carries NO Role/ClusterRole — the guard would pass on nothing"
  echo "RESULT: FAIL (${FAILURES})"; exit 1
fi

# ── Case 2 — the extractor must actually find reads (vacuity control) ────────
OUT="$(python3 "${TMP}/extract.py" "${TMP}/render.yaml" "${TMP}/role.yaml")"
READ_N="$(printf '%s\n' "${OUT}" | sed -n 's/^READ=\([0-9]*\).*/\1/p')"
if [ "${READ_N:-0}" -ge 10 ]; then
  pass "extractor found ${READ_N} distinct resources read by the step scripts"
else
  fail "extractor found only ${READ_N:-0} resources — it is not reading the scripts, so a pass would be meaningless"
fi

# ── Case 3 — `namespaces` specifically must be both read and granted (#6280) ─
if grep -q 'srp_kubectl_json namespaces -l' "${TMP}/render.yaml"; then
  pass "Phase A0 does list Organization namespaces (srp_kubectl_json namespaces -l ...)"
else
  fail "Phase A0 no longer lists Organization namespaces — #6273 partition is gone"
fi
if grep -q '"namespaces"' "${TMP}/role.yaml" || grep -q '^\s*- namespaces$' "${TMP}/role.yaml"; then
  pass "ClusterRole grants: namespaces"
else
  fail "ClusterRole does NOT grant namespaces — this is the #6280 defect"
fi

# ── Case 4 — no resource read anywhere may be ungranted ─────────────────────
MISSING="$(printf '%s\n' "${OUT}" | grep '^MISSING ' | sed 's/^MISSING //' || true)"
if [ -z "${MISSING}" ]; then
  pass "every resource the step scripts read is granted by the ClusterRole"
else
  fail "resources read but NOT granted:"
  printf '%s\n' "${MISSING}" | sed 's/^/         - /'
fi

# ── Case 5 — MUTATION CONTROL: strip the namespaces rule, demand RED ────────
python3 - "${TMP}/role.yaml" "${TMP}/role-mutated.yaml" <<'PYEOF'
import sys, yaml
src, dst = sys.argv[1], sys.argv[2]
docs = list(yaml.safe_load_all(open(src, encoding="utf-8", errors="replace")))
for d in docs:
    if not d:
        continue
    for rule in d.get("rules") or []:
        rule["resources"] = [r for r in (rule.get("resources") or []) if r != "namespaces"]
    d["rules"] = [r for r in (d.get("rules") or []) if r.get("resources")]
yaml.safe_dump_all([d for d in docs if d], open(dst, "w", encoding="utf-8"))
PYEOF

MUT="$(python3 "${TMP}/extract.py" "${TMP}/render.yaml" "${TMP}/role-mutated.yaml")"
if grep -q '^MISSING namespaces$' <<<"${MUT}"; then
  pass "mutation control: removing the namespaces rule makes this guard report it missing"
else
  fail "mutation control did NOT go red — this guard cannot detect the very defect it exists for"
  printf '%s\n' "${MUT}" | sed 's/^/         /'
fi

# ── Case 6 — every read is granted in its API GROUP, not only by its name ──
# Cases 4 and 5 compare resource names: a rule for workspaces in opentofu.upbound.io
# also "covered" the read of workspaces.opentofu.m.upbound.io, so dropping that group
# from the Role kept this suite green while the API server refused the list (SOV-344).
# Here each read is a group/resource pair: a qualified token names its group; a bare
# name takes the group kubectl resolves it to (the table below); a bare name outside
# the table fails, because a read whose group is not known cannot be judged. A rule
# grants a pair when its apiGroups hold the group (or *) and its resources the
# resource (or *).
plural() { case "$1" in *s) echo "$1" ;; *[aeiou]y) echo "$1s" ;; *y) echo "${1%y}ies" ;; *) echo "$1s" ;; esac; }
bare_pair() {
  local r
  case "$1" in
    cm) r=configmaps ;; svc) r=services ;; pvc) r=persistentvolumeclaims ;; pv) r=persistentvolumes ;;
    ns) r=namespaces ;; sa) r=serviceaccounts ;; ep) r=endpoints ;; deploy) r=deployments ;;
    ds) r=daemonsets ;; sts) r=statefulsets ;; rs) r=replicasets ;; hr) r=helmreleases ;;
    crd) r=customresourcedefinitions ;; *) r=$(plural "$1") ;;
  esac
  case "${r}" in
    pods|secrets|configmaps|nodes|namespaces|services|events|endpoints|serviceaccounts|persistentvolumeclaims|persistentvolumes) echo "/${r}" ;;
    deployments|daemonsets|statefulsets|replicasets) echo "apps/${r}" ;;
    jobs|cronjobs) echo "batch/${r}" ;;
    helmreleases) echo "helm.toolkit.fluxcd.io/${r}" ;;
    kustomizations) echo "kustomize.toolkit.fluxcd.io/${r}" ;;
    gitrepositories|helmrepositories|ocirepositories|helmcharts) echo "source.toolkit.fluxcd.io/${r}" ;;
    customresourcedefinitions) echo "apiextensions.k8s.io/${r}" ;;
    volumeattachments) echo "storage.k8s.io/${r}" ;;
    *) echo "?/$1" ;;
  esac
}
# read_pairs <render> -> <render>.pairs: one "<group>/<resource>" per resource the scripts read
read_pairs() {
  { tr '\n' ' ' <"$1" \
      | grep -oE 'kubectl[[:space:]]+((-[^[:space:]]+|[a-z-]+=[^[:space:]]+)[[:space:]]+)*get[[:space:]]+[A-Za-z][A-Za-z0-9.,-]*'
    grep -oE 'srp_kubectl_json[[:space:]]+[A-Za-z][A-Za-z0-9.,-]*' "$1"
  } | awk '{print $NF}' | tr ',' '\n' | sort -u >"$1.tokens"
  : >"$1.pairs"
  local t
  while IFS= read -r t; do
    case "${t}" in
      ""|-*|po|no) ;;
      *.*) echo "${t#*.}/$(plural "${t%%.*}")" >>"$1.pairs" ;;
      *) bare_pair "${t}" >>"$1.pairs" ;;
    esac
  done <"$1.tokens"
  sort -u -o "$1.pairs" "$1.pairs"
}
# grant_pairs <render> -> <render>.grants: one "<group>/<resource>" per Role or ClusterRole rule
grant_pairs() {
  yq -N 'select(.kind == "ClusterRole" or .kind == "Role") | .rules[] | .apiGroups[] as $g | .resources[] | $g + "/" + .' "$1" \
    | sort -u >"$1.grants"
}
# missing_pairs <render>: each read pair no rule grants
missing_pairs() {
  local p g r
  while IFS= read -r p; do
    g="${p%%/*}"; r="${p#*/}"
    grep -qxF -e "${g}/${r}" -e "*/${r}" -e "${g}/*" -e "*/*" "$1.grants" || echo "${p}"
  done <"$1.pairs"
}
read_pairs "${TMP}/render.yaml"; grant_pairs "${TMP}/render.yaml"
missing_pairs "${TMP}/render.yaml" >"${TMP}/missing-pairs.txt"
if grep -qxF 'opentofu.m.upbound.io/workspaces' "${TMP}/render.yaml.pairs" && grep -qxF 'apps/deployments' "${TMP}/render.yaml.pairs" \
    && [ "$(grep -c . "${TMP}/render.yaml.grants")" -ge 10 ]; then
  pass "$(grep -c . "${TMP}/render.yaml.pairs") group/resource reads and $(grep -c . "${TMP}/render.yaml.grants") granted pairs, qualified and bare"
else
  fail "the group/resource extraction found no opentofu.m.upbound.io/workspaces or apps/deployments read, or under 10 grants — a pass would be meaningless"
fi
if [ ! -s "${TMP}/missing-pairs.txt" ]; then
  pass "every read is granted in its own API group"
else
  fail "reads with no rule for their group and resource:"
  sed 's/^/         - /' "${TMP}/missing-pairs.txt"
fi

# ── Case 7 — MUTATION CONTROL: drop the group opentofu.m.upbound.io, demand RED ──
yq '(select(.kind == "ClusterRole" or .kind == "Role") | .rules[].apiGroups) |= map(select(. != "opentofu.m.upbound.io"))' \
  "${TMP}/render.yaml" >"${TMP}/render-nogroup.yaml"
cp "${TMP}/render.yaml.pairs" "${TMP}/render-nogroup.yaml.pairs"
grant_pairs "${TMP}/render-nogroup.yaml"
missing_pairs "${TMP}/render-nogroup.yaml" >"${TMP}/missing-nogroup.txt"
if grep -qxF 'opentofu.m.upbound.io/workspaces' "${TMP}/missing-nogroup.txt" && grep -qxF 'opentofu.upbound.io/workspaces' "${TMP}/render-nogroup.yaml.grants"; then
  pass "mutation control: without the group opentofu.m.upbound.io the read workspaces.opentofu.m.upbound.io is reported missing"
else
  fail "mutation control did NOT go red when the group opentofu.m.upbound.io was dropped from the rules"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL (${FAILURES})"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
