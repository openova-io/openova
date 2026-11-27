#!/usr/bin/env bash
# bp-catalyst-platform: the conformance account exists on every Sovereign with
# the six rules of the suite's self selection and nothing more (FND-156).
# The render, in the namespace slot 13 of the kit installs the chart into, holds
#   1. one ServiceAccount catalyst-conformance in that namespace;
#   2. one ClusterRole catalyst-conformance with exactly the six rules below;
#   3. one binding of that ClusterRole, the ClusterRoleBinding of that name, to
#      that ServiceAccount only, and no other binding of the account;
#   4. a CATALYST_AGENT_TOKEN_CALLERS entry of catalyst-api for the account,
# and check AIP-G07 asks a token for that account. Each rule can fail.
# Usage: bash tests/conformance-account-rbac.sh [CHART_DIR]
set -euo pipefail
CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
# The chart depends on platform/secrets-lib (SEC-216): build it into charts/ before the first render.
helm dependency build --skip-refresh "$CHART_DIR" > /dev/null
REPO_ROOT="${REPO_ROOT:-$(cd "$CHART_DIR/../../.." && pwd)}"
CHECK="$REPO_ROOT/tests/acceptance/sovereign/internal/suite/check_aip_g07.go"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
NS="$(python3 -c 'import sys, yaml
print(next(d["spec"]["targetNamespace"] for d in yaml.safe_load_all(open(sys.argv[1])) if d and d["kind"] == "HelmRelease"))' \
  "$REPO_ROOT/clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml")"
CHECK_ACCOUNT="$(sed -n 's/^[[:space:]]*aipG07\(NS\|SA\)[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\2/p' "$CHECK" | paste -sd/)"
helm template conformancegate "$CHART_DIR" --namespace "$NS" > "$TMP/render.yaml"

cat > "$TMP/judge.py" <<'PY'
import json, sys, yaml
render, ns, check_account = sys.argv[1:4]
NAME = "catalyst-conformance"
WANT = sorted([
    (("helm.toolkit.fluxcd.io",), ("helmreleases",), (), ("get", "list", "watch")),
    (("",), ("nodes", "pods", "services"), (), ("get", "list", "watch")),
    (("",), ("namespaces",), (), ("get", "list")),
    (("apiextensions.k8s.io",), ("customresourcedefinitions",), ("clusterpolicies.kyverno.io", "validatingpolicies.policies.kyverno.io"), ("get",)),
    (("kyverno.io", "policies.kyverno.io"), ("clusterpolicies", "validatingpolicies"), ("forbid-nodeport-service",), ("get",)),
    (("wgpolicyk8s.io",), ("clusterpolicyreports",), (), ("create", "get", "patch", "update"))])
docs = [d for d in yaml.safe_load_all(open(render)) if isinstance(d, dict)]
named = lambda kind: [d for d in docs if d.get("kind") == kind and d["metadata"]["name"] == NAME]
me = {"kind": "ServiceAccount", "name": NAME, "namespace": ns}
bad = []
sas = named("ServiceAccount")
if [d["metadata"].get("namespace") for d in sas] != [ns]:
    bad.append("ServiceAccounts %s in namespaces %r, want one in %s" % (NAME, [d["metadata"].get("namespace") for d in sas], ns))
crs = named("ClusterRole")
if len(crs) != 1:
    bad.append("%d ClusterRoles %s, want one" % (len(crs), NAME))
for d in crs:
    got = sorted(tuple(tuple(sorted(r.get(k) or [])) for k in ("apiGroups", "resources", "resourceNames", "verbs")) for r in d.get("rules") or [])
    if got != WANT or d.get("aggregationRule"):
        bad.append("ClusterRole %s is not exactly the six rules: extra %r, missing %r%s"
                   % (NAME, [r for r in got if r not in WANT], [r for r in WANT if r not in got], ", aggregates" if d.get("aggregationRule") else ""))
binds = [d for d in docs if d.get("kind") in ("ClusterRoleBinding", "RoleBinding")]
of_role = [d for d in binds if d["roleRef"]["kind"] == "ClusterRole" and d["roleRef"]["name"] == NAME]
if [(d["kind"], d["metadata"]["name"], d.get("subjects")) for d in of_role] != [("ClusterRoleBinding", NAME, [me])]:
    bad.append("ClusterRole %s is bound by %r, want only ClusterRoleBinding %s to %s/%s"
               % (NAME, [(d["kind"], d["metadata"]["name"], d.get("subjects")) for d in of_role], NAME, ns, NAME))
for d in binds:
    if d not in of_role and any(s.get("kind") == "ServiceAccount" and s.get("name") == NAME for s in d.get("subjects") or []):
        bad.append("%s %s also binds the account to %s" % (d["kind"], d["metadata"]["name"], d["roleRef"]["name"]))
callers = [x.get("caller") for d in docs if d.get("kind") == "Deployment" for c in d["spec"]["template"]["spec"]["containers"]
           for e in c.get("env") or [] if e.get("name") == "CATALYST_AGENT_TOKEN_CALLERS" for x in json.loads(e.get("value", "[]"))]
if "system:serviceaccount:%s:%s" % (ns, NAME) not in callers:
    bad.append("no CATALYST_AGENT_TOKEN_CALLERS entry names %s/%s: %r" % (ns, NAME, callers))
if check_account != "%s/%s" % (ns, NAME):
    bad.append("check AIP-G07 asks a token for %s, the chart creates %s/%s" % (check_account, ns, NAME))
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY
python3 "$TMP/judge.py" "$TMP/render.yaml" "$NS" "$CHECK_ACCOUNT" > "$TMP/out.txt" || { echo "FAIL:"; sed 's/^/  /' "$TMP/out.txt"; exit 1; }
echo "  ok   — $NS/catalyst-conformance: six rules, one binding, a caller of catalyst-api, the account of AIP-G07"

mutant() { # <label> <expected text> <python edit of docs> [account of the check]
  python3 -c 'import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d, dict)]
one = lambda k: [d for d in docs if d["kind"] == k and d["metadata"]["name"] == "catalyst-conformance"][0]
exec(sys.argv[3]); yaml.safe_dump_all(docs, open(sys.argv[2], "w"))' "$TMP/render.yaml" "$TMP/m.yaml" "$3"
  if python3 "$TMP/judge.py" "$TMP/m.yaml" "$NS" "${4:-$CHECK_ACCOUNT}" > "$TMP/m.txt"; then echo "FAIL: passes when $1"; exit 1; fi
  grep -qF "$2" "$TMP/m.txt" || { echo "FAIL: '$1' failed for another reason: $(cat "$TMP/m.txt")"; exit 1; }
  echo "  ok   — fails when $1"
}
SA='{"kind":"ServiceAccount","name":"catalyst-conformance","namespace":"'"$NS"'"}'
mutant "the ServiceAccount is gone" "want one in" 'docs.remove(one("ServiceAccount"))'
mutant "the ServiceAccount is in another namespace" "['default']" 'one("ServiceAccount")["metadata"]["namespace"] = "default"'
mutant "the ClusterRole is gone" "0 ClusterRoles" 'docs.remove(one("ClusterRole"))'
mutant "the ClusterRole reads Secrets" "not exactly the six rules" 'one("ClusterRole")["rules"].append({"apiGroups": [""], "resources": ["secrets"], "verbs": ["get"]})'
mutant "a rule has a wildcard verb" "not exactly the six rules" 'one("ClusterRole")["rules"][0]["verbs"] = ["*"]'
mutant "the policy read loses its name" "not exactly the six rules" '[r.pop("resourceNames") for r in one("ClusterRole")["rules"] if r["resources"] == ["clusterpolicies", "validatingpolicies"]]'
mutant "the ClusterRole aggregates" "aggregates" 'one("ClusterRole")["aggregationRule"] = {"clusterRoleSelectors": []}'
mutant "the binding is gone" "is bound by []" 'docs.remove(one("ClusterRoleBinding"))'
mutant "the binding has a second subject" "want only ClusterRoleBinding" 'one("ClusterRoleBinding")["subjects"].append({"kind": "Group", "name": "system:serviceaccounts"})'
mutant "another binding gives the account cluster-admin" "also binds the account to cluster-admin" \
  'docs.append({"kind": "ClusterRoleBinding", "metadata": {"name": "x"}, "roleRef": {"kind": "ClusterRole", "name": "cluster-admin"}, "subjects": ['"$SA"']})'
mutant "catalyst-api names no caller for the account" "no CATALYST_AGENT_TOKEN_CALLERS entry" \
  '[e.__setitem__("value", "[]") for d in docs if d["kind"] == "Deployment" for c in d["spec"]["template"]["spec"]["containers"] for e in c.get("env") or [] if e.get("name") == "CATALYST_AGENT_TOKEN_CALLERS"]'
mutant "check AIP-G07 asks for another account" "check AIP-G07 asks a token for" 'pass' "$NS/other"
echo "PASS: $NS/catalyst-conformance has the six rules of the self selection and nothing more"
