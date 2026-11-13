#!/usr/bin/env bash
# bp-catalyst-platform: the intent lock (templates/intent-lock/).
#
# Render cases (helm and python3 with PyYAML, no cluster):
#   1. Defaults: one ValidatingAdmissionPolicy catalyst-intent-lock with
#      failurePolicy Fail; one binding catalyst-intent-lock-<stage> for each
#      of the eight stages; every binding ["Audit", "Warn"], none Deny; every
#      binding rule is a rule of the policy; the audit annotation's false
#      branch is '' and not null.
#   2. intentLock.stages.applications=deny: exactly one binding carries Deny,
#      catalyst-intent-lock-applications, and it carries Deny alone.
#   3. The render fails for an action other than audit or deny, and for a
#      stage that has no entry in intentLock.resources.
#
# With --apiserver as the second argument it then applies the lock to a
# throwaway API server, reached with the command in $KUBECTL (default
# kubectl; .github/workflows/intent-lock-apiserver.yaml sets it to a kubectl
# inside a k3s container). Files go to it on standard input.
#   4. The API server accepts the policy and the eight bindings.
#   5. At audit, a cluster-admin create of an Application is admitted with
#      the lock's warning, and the metric
#      apiserver_validating_admission_policy_check_total counts it with
#      enforcement_action="audit".
#   6. With stage applications at deny, a cluster-admin create is refused by
#      the lock, and a label change, which leaves spec unchanged, passes.
#
# Usage: bash tests/intent-lock.sh [CHART_DIR] [--apiserver]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
MODE="${2:-}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
STAGES="access applications blueprints environments flux organizations policy topology"
render() { helm template lock . --show-only templates/intent-lock/policy.yaml --show-only templates/intent-lock/bindings.yaml "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

# judge FILE DENY_STAGE: prints one line per finding; DENY_STAGE is "" or the
# one stage whose binding must carry Deny.
judge() {
  python3 - "$1" "$2" "$STAGES" <<'PY'
import sys, yaml
path, deny, stages = sys.argv[1], sys.argv[2], sorted(sys.argv[3].split())
docs = [d for d in yaml.safe_load_all(open(path)) if d]
pols = [d for d in docs if d["kind"] == "ValidatingAdmissionPolicy"]
binds = [d for d in docs if d["kind"] == "ValidatingAdmissionPolicyBinding"]
bad = []
if [p["metadata"]["name"] for p in pols] != ["catalyst-intent-lock"]:
    bad.append("want one policy catalyst-intent-lock, got %s" % [p["metadata"]["name"] for p in pols])
else:
    spec = pols[0]["spec"]
    if spec.get("failurePolicy") != "Fail":
        bad.append("failurePolicy is %r, want Fail" % spec.get("failurePolicy"))
    expr = spec["auditAnnotations"][0]["valueExpression"]
    if not expr.endswith(": ''") or "null" in expr:
        bad.append("the audit annotation's false branch must be '' and not null: " + expr)
    have = {(g, r) for rule in spec["matchConstraints"]["resourceRules"] for g in rule["apiGroups"] for r in rule["resources"]}
    for b in binds:
        for rule in b["spec"]["matchResources"]["resourceRules"]:
            for g in rule["apiGroups"]:
                for r in rule["resources"]:
                    if (g, r) not in have:
                        bad.append("%s matches %s/%s, which the policy does not" % (b["metadata"]["name"], g, r))
names = sorted(b["metadata"]["name"] for b in binds)
if names != ["catalyst-intent-lock-" + s for s in stages]:
    bad.append("want the bindings of %s, got %s" % (stages, names))
for b in binds:
    stage = b["metadata"]["name"][len("catalyst-intent-lock-"):]
    want = ["Deny"] if stage == deny else ["Audit", "Warn"]
    if b["spec"]["validationActions"] != want or b["spec"]["policyName"] != "catalyst-intent-lock":
        bad.append("%s: validationActions %s, want %s" % (b["metadata"]["name"], b["spec"]["validationActions"], want))
print("\n".join(bad))
PY
}

echo "[intent-lock] case 1: defaults"
render > "$TMP/default.yaml"
out="$(judge "$TMP/default.yaml" "")"
[ -z "$out" ] || fail "$out"
[ "$(grep -c 'Deny' "$TMP/default.yaml" || true)" = 0 ] || fail "the default render holds Deny"
echo "  PASS (one policy, eight bindings, none Deny)"

echo "[intent-lock] case 2: intentLock.stages.applications=deny"
render --set intentLock.stages.applications=deny > "$TMP/deny.yaml"
out="$(judge "$TMP/deny.yaml" applications)"
[ -z "$out" ] || fail "$out"
[ "$(grep -c '"Deny"' "$TMP/deny.yaml")" = 1 ] || fail "want exactly one binding with Deny"
echo "  PASS (exactly one binding with Deny: catalyst-intent-lock-applications)"

echo "[intent-lock] case 3: the render refuses wrong stage data"
if render --set intentLock.stages.applications=enforce > /dev/null 2>&1; then fail "an action other than audit or deny rendered"; fi
if render --set intentLock.stages.extra=audit > /dev/null 2>&1; then fail "a stage without resources rendered"; fi
echo "  PASS (both refused)"

[ "$MODE" = "--apiserver" ] || { echo "[intent-lock] all render cases passed"; exit 0; }

read -r -a KUBE <<<"${KUBECTL:-kubectl}"
k() { "${KUBE[@]}" "$@"; }
APP='{"apiVersion":"apps.openova.io/v1","kind":"Application","metadata":{"name":"NAME","namespace":"lock-test"},"spec":{"blueprintRef":{"name":"bp-wordpress","version":"1.0.0"},"environmentRef":"acme-prod","regions":["region-a"]}}'
app() { printf '%s\n' "${APP/NAME/$1}"; }
LOCK_MSG="desired state of Application is owned by Git"
# until_dry_run TEXT: a server-side dry-run create until its answer holds TEXT.
until_dry_run() {
  local out=""
  for _ in $(seq 1 60); do
    out="$(app probe | k create --dry-run=server -f - 2>&1 || true)"
    if grep -qF "$1" <<<"$out"; then return 0; fi
    sleep 2
  done
  fail "after 120 s the answer still lacks '$1': $out"
}

echo "[intent-lock] case 4: the API server accepts the policy and the bindings"
k apply --server-side -f - < crds/application.yaml
k wait --for=condition=Established crd/applications.apps.openova.io --timeout=60s
k create namespace lock-test
k apply -f - < "$TMP/default.yaml"
[ "$(k get validatingadmissionpolicybindings -l app.kubernetes.io/name=catalyst-intent-lock -o name | wc -l)" = 8 ] || fail "want eight bindings on the API server"
echo "  PASS"

echo "[intent-lock] case 5: audit admits with a warning, and the audit series counts it"
until_dry_run "$LOCK_MSG"
out="$(app audited | k create -f - 2>&1)" || fail "the create at audit was refused: $out"
grep -qF "$LOCK_MSG" <<<"$out" || fail "the create at audit carried no warning of the lock: $out"
k get --raw /metrics > "$TMP/metrics.txt"
awk '/^apiserver_validating_admission_policy_check_total[{]/ && /policy="catalyst-intent-lock"/ && /policy_binding="catalyst-intent-lock-applications"/ && /enforcement_action="audit"/ { f = 1 } END { exit !f }' "$TMP/metrics.txt" \
  || fail "no audit series of the lock in /metrics"
echo "  PASS"

echo "[intent-lock] case 6: deny refuses a create and lets a label change pass"
k apply -f - < "$TMP/deny.yaml"
until_dry_run "denied request"
if out="$(app denied | k create -f - 2>&1)"; then fail "the create at deny was admitted: $out"; fi
grep -qF "$LOCK_MSG" <<<"$out" || fail "the create at deny was refused, but not by the lock: $out"
k -n lock-test label applications.apps.openova.io audited intent-lock-test=label-only
echo "  PASS"
echo "[intent-lock] all cases passed"
