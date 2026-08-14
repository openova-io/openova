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
if printf '%s\n' "${MUT}" | grep -q '^MISSING namespaces$'; then
  pass "mutation control: removing the namespaces rule makes this guard report it missing"
else
  fail "mutation control did NOT go red — this guard cannot detect the very defect it exists for"
  printf '%s\n' "${MUT}" | sed 's/^/         /'
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "RESULT: PASS"
else
  echo "RESULT: FAIL (${FAILURES})"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
