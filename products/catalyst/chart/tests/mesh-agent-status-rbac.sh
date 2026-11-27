#!/usr/bin/env bash
# bp-catalyst-platform: catalyst-api may exec in the namespace of the Cilium
# agents and nowhere else.
#
# The mesh step of catalyst-api reads from every Cilium agent whether it is
# connected to the other regions (internal/meshagents): one exec of
# `cilium-dbg status` per agent Pod. That needs `create` on pods/exec. Given by
# a ClusterRole it would be command execution in every Pod of the Sovereign,
# the secret store included. So:
#
#   1. no ClusterRole this chart renders, and no ClusterRole file of its
#      templates, names pods/exec (or a wildcard that covers it) for the core
#      API group;
#   2. the only objects that name pods/exec are one Role in the agents'
#      namespace with the single verb `create`, bound there to the
#      ServiceAccount of catalyst-api in the release namespace;
#   3. that namespace is the one the reader lists the agent Pods in.
#
# Each rule is proven able to fail on a changed copy of the render.
#
# Usage: bash tests/mesh-agent-status-rbac.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "$CHART_DIR" && pwd)"
# The chart depends on platform/secrets-lib (SEC-216): build it into charts/ before the first render.
helm dependency build --skip-refresh "$CHART_DIR" > /dev/null
REPO_ROOT="${REPO_ROOT:-$(cd "$CHART_DIR/../../.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
helm template rbacgate . --namespace catalyst-system > "$TMP/render.yaml" 2> "$TMP/render.err" || {
  echo "FAIL: helm template errored:"; tail -n 5 "$TMP/render.err"; exit 1
}

# The namespace the reader lists the agents in (meshagents.Namespace = meshstatus.Namespace).
AGENT_NS="$(sed -n 's/^[[:space:]]*Namespace[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' \
  "$REPO_ROOT/products/catalyst/bootstrap/api/internal/meshstatus/meshstatus.go" | head -n 1)"
grep -q 'Namespace = meshstatus.Namespace' "$REPO_ROOT/products/catalyst/bootstrap/api/internal/meshagents/meshagents.go" \
  || { echo "FAIL: meshagents no longer takes its namespace from meshstatus: this test reads the wrong constant."; exit 1; }
[ -n "$AGENT_NS" ] || { echo "FAIL: the namespace of the agents cannot be read from meshstatus.go."; exit 1; }

cat > "$TMP/judge.py" <<'PY'
"""Judges a render. Prints one line per broken rule; exit 1 when any is broken."""
import sys, yaml
render, agent_ns, release_ns = sys.argv[1], sys.argv[2], sys.argv[3]
SA = "catalyst-api-cutover-driver"
docs = [d for d in yaml.safe_load_all(open(render)) if isinstance(d, dict)]
bad = []

def names_exec(rule):
    groups, res = rule.get("apiGroups") or [], rule.get("resources") or []
    return ("" in groups or "*" in groups) and any(r in ("pods/exec", "*", "pods/*", "*/exec") for r in res)

cluster_roles = [d for d in docs if d.get("kind") == "ClusterRole"]
if not any(d["metadata"]["name"] == SA for d in cluster_roles):
    bad.append("the render has no ClusterRole %s: the test would judge nothing" % SA)
for d in cluster_roles:
    if any(names_exec(r) for r in d.get("rules") or []):
        bad.append("ClusterRole %s names pods/exec: command execution in every Pod of the Sovereign" % d["metadata"]["name"])

roles = [d for d in docs if d.get("kind") == "Role" and any(names_exec(r) for r in d.get("rules") or [])]
if len(roles) != 1:
    bad.append("%d Roles name pods/exec, want exactly one: %s" % (len(roles), [(d["metadata"].get("namespace"), d["metadata"]["name"]) for d in roles]))
for d in roles:
    ns, name = d["metadata"].get("namespace"), d["metadata"]["name"]
    if ns != agent_ns:
        bad.append("Role %s/%s gives pods/exec outside the namespace of the agents (%s)" % (ns, name, agent_ns))
    for r in d.get("rules") or []:
        if names_exec(r) and (r.get("resources") != ["pods/exec"] or r.get("verbs") != ["create"] or r.get("apiGroups") != [""]):
            bad.append("Role %s/%s: the rule is %r, want only create on pods/exec of the core group" % (ns, name, r))
    if len(d.get("rules") or []) != 1:
        bad.append("Role %s/%s has %d rules, want the one for pods/exec" % (ns, name, len(d["rules"])))
    binds = [b for b in docs if b.get("kind") == "RoleBinding" and b["roleRef"]["kind"] == "Role" and b["roleRef"]["name"] == name
             and b["metadata"].get("namespace") == ns]
    if len(binds) != 1:
        bad.append("Role %s/%s has %d RoleBindings in its namespace, want one" % (ns, name, len(binds)))
    for b in binds:
        if b.get("subjects") != [{"kind": "ServiceAccount", "name": SA, "namespace": release_ns}]:
            bad.append("RoleBinding %s/%s binds %r, want only ServiceAccount %s/%s" % (ns, b["metadata"]["name"], b.get("subjects"), release_ns, SA))
# No binding of any kind may hand that Role's right out through a ClusterRoleBinding.
for b in docs:
    if b.get("kind") == "ClusterRoleBinding" and b["roleRef"]["name"] in [d["metadata"]["name"] for d in roles]:
        bad.append("ClusterRoleBinding %s refers to the exec Role" % b["metadata"]["name"])
print("\n".join(bad))
sys.exit(1 if bad else 0)
PY

if ! python3 "$TMP/judge.py" "$TMP/render.yaml" "$AGENT_NS" catalyst-system > "$TMP/out.txt"; then
  echo "FAIL:"; sed 's/^/  /' "$TMP/out.txt"; exit 1
fi
echo "  ok   — no ClusterRole of the render names pods/exec; one Role in ${AGENT_NS} gives create on pods/exec to catalyst-system/catalyst-api-cutover-driver"

# The template files themselves: a ClusterRole behind a values switch that this render leaves off must not name it either.
for f in $(grep -rl '^kind: ClusterRole$' templates); do
  if python3 - "$f" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
# The rule text of the file, comment lines dropped.
body = "\n".join(l for l in text.split("\n") if not l.lstrip().startswith("#"))
sys.exit(0 if re.search(r"pods/exec", body) else 1)
PY
  then echo "FAIL: $f is a ClusterRole template and names pods/exec outside a comment."; exit 1; fi
done
echo "  ok   — no ClusterRole template names pods/exec outside a comment"

# Every rule can fail.
mutant() { # <label> <expected text> <python edit of the docs list>
  python3 - "$TMP/render.yaml" "$TMP/mutant.yaml" "$3" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d, dict)]
exec(sys.argv[3])
yaml.safe_dump_all(docs, open(sys.argv[2], "w"))
PY
  if python3 "$TMP/judge.py" "$TMP/mutant.yaml" "$AGENT_NS" catalyst-system > "$TMP/mout.txt"; then
    echo "FAIL: the test passes a render where $1."; exit 1
  fi
  grep -qF "$2" "$TMP/mout.txt" || { echo "FAIL: mutant '$1' failed for another reason: $(cat "$TMP/mout.txt")"; exit 1; }
  echo "  ok   — fails when $1"
}
mutant "the ClusterRole of catalyst-api names pods/exec" "ClusterRole catalyst-api-cutover-driver names pods/exec" \
  '[d for d in docs if d["kind"]=="ClusterRole" and d["metadata"]["name"]=="catalyst-api-cutover-driver"][0]["rules"].append({"apiGroups":[""],"resources":["pods/exec"],"verbs":["create"]})'
mutant "a ClusterRole covers it by a wildcard" "names pods/exec" \
  '[d for d in docs if d["kind"]=="ClusterRole"][0]["rules"].append({"apiGroups":[""],"resources":["*"],"verbs":["*"]})'
mutant "the Role is in another namespace" "outside the namespace of the agents" \
  '[d.__setitem__("metadata", dict(d["metadata"], namespace="openbao")) for d in docs if d["kind"] in ("Role","RoleBinding") and d["metadata"]["name"]=="catalyst-api-mesh-agent-status"]'
mutant "the Role gives another verb" "want only create on pods/exec" \
  '[d for d in docs if d["kind"]=="Role" and d["metadata"]["name"]=="catalyst-api-mesh-agent-status"][0]["rules"][0]["verbs"].append("get")'
mutant "the binding has a second subject" "want only ServiceAccount" \
  '[d for d in docs if d["kind"]=="RoleBinding" and d["metadata"]["name"]=="catalyst-api-mesh-agent-status"][0]["subjects"].append({"kind":"Group","name":"system:serviceaccounts"})'
mutant "the Role is gone" "0 Roles name pods/exec" \
  'docs[:] = [d for d in docs if not (d["kind"]=="Role" and d["metadata"]["name"]=="catalyst-api-mesh-agent-status")]'

echo "PASS: catalyst-api may exec in ${AGENT_NS} only"
