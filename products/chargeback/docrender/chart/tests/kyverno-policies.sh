#!/usr/bin/env bash
# docrender — the platform's OWN Kyverno baseline, applied to this chart's
# render (EPIC #6867).
#
# WHY THIS EXISTS
# ───────────────
# `helm lint` and `helm template` both pass on a chart that the cluster will
# refuse. The Sovereign runs bp-kyverno-policies with six ClusterPolicies at
# validationFailureAction: Enforce, and a workload that trips one of them is
# not "audited" — it is DENIED at admission, which is how bp-alloy shipped a
# DaemonSet that could never start (#3988) and how bp-newapi's upgrade wedged
# on a CronJob path (#3374). Those are caught only by evaluating the render
# against the policies themselves, which is what this does: it renders
# platform/kyverno-policies at its canonical (non-bootstrap) actions and runs
# the real `kyverno` CLI over the real manifests.
#
# HOW THE FLUX LABELS ARE HANDLED — read this before changing it
# ──────────────────────────────────────────────────────────────
# The `flux-managed` policy denies a workload that carries neither
# `app.kubernetes.io/managed-by: flux` nor a Flux ownership label. Helm's own
# render cannot carry those: helm-controller stamps
# `helm.toolkit.fluxcd.io/name` + `-namespace` onto every object in a
# HelmRelease AFTER templating, and bp-chargeback is installed by exactly such
# a release (bootstrap-kit slot 13f). So the object that reaches admission has
# them and the object `helm template` prints does not.
#
# This gate therefore stamps those two labels before applying the policies —
# modelling what the apiserver actually sees, not excusing a failure. Two
# things keep that honest:
#   * the stamp is applied to EVERY rendered object, never to a chosen few,
#     so it cannot be used to quiet one inconvenient resource;
#   * the self-test at the bottom proves the gate still goes red on a real
#     defect, so a green here means "checked", not "did not look".
# Verified 2026-09-11: without the stamp the ONLY failures are the two
# flux-managed ones, and the pre-existing chargeback Deployment fails the
# identical way — this is a property of render time, not of this chart.
#
# Usage: tests/kyverno-policies.sh [chart_dir]
# Exit:  0 every rendered object is admitted · 1 at least one is denied
#        2 the gate itself could not run (missing tool, empty render)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${1:-$(cd "$SCRIPT_DIR/.." && pwd)}"
chart_dir="$(cd "$chart_dir" && pwd)"
# docrender/chart/tests -> docrender/chart -> docrender -> chargeback -> products -> repo
REPO="$(cd "$chart_dir/../../../.." && pwd)"
PARENT_CHART="$REPO/products/chargeback/chart"
POLICY_CHART="$REPO/platform/kyverno-policies/chart"

helm="${HELM_BIN:-helm}"
kyverno="${KYVERNO_BIN:-kyverno}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

die() { echo "GATE BROKEN: $*" >&2; exit 2; }
fail() { echo "FAIL: $*" >&2; exit 1; }

command -v "$helm" >/dev/null 2>&1 || die "helm is not on PATH"
command -v "$kyverno" >/dev/null 2>&1 || die "the kyverno CLI is not on PATH. This gate does not silently skip: a compliance check that reports green because it did not run is the fail-open family this repo keeps finding (#6235). Install it (https://github.com/kyverno/kyverno/releases) or set KYVERNO_BIN."
command -v python3 >/dev/null 2>&1 || die "python3 is not on PATH"

# ── the filter, shared by every invocation below ──────────────────────────
cat > "$work/filter.py" <<'PYEOF'
"""Split a helm render into the documents this gate reasons about.

  mode=policies  keep every ClusterPolicy (the kyverno CLI ignores a file
                 carrying anything else, and reports "0 policy rule(s)" —
                 a silent no-op this gate must never ship on).
  mode=workload  keep the objects the docrender sub-chart owns, and stamp
                 the Flux ownership labels helm-controller applies in the
                 cluster (see the header). Stamping is unconditional.
"""
import sys, yaml

mode, src, dst = sys.argv[1], sys.argv[2], sys.argv[3]
docs = [d for d in yaml.safe_load_all(open(src)) if d]

if mode == "policies":
    keep = [d for d in docs if d.get("kind") == "ClusterPolicy"]
else:
    keep = [d for d in docs
            if str(d.get("metadata", {}).get("name", "")).startswith("chargeback-docrender")]
    for d in keep:
        labels = d.setdefault("metadata", {}).setdefault("labels", {})
        labels["helm.toolkit.fluxcd.io/name"] = "bp-chargeback"
        labels["helm.toolkit.fluxcd.io/namespace"] = "flux-system"

yaml.safe_dump_all(keep, open(dst, "w"))
print(len(keep))
PYEOF

# ── the policies, at their CANONICAL actions ──────────────────────────────
# bootstrapMode=false is the post-handover posture: probes-present,
# resource-requests, cilium-l7-mtls, flux-managed, harbor-proxy-pull,
# image-tag-pinned, substrate-stays-on-host, forbid-local-path-storage and
# forbid-nodeport-service all sit at Enforce. Rendering with the default
# (bootstrapMode=true) would force every one of them to Audit and this gate
# would pass on anything at all.
"$helm" template kyverno-policies "$POLICY_CHART" \
  --set compliancePolicies.bootstrapMode=false > "$work/policies-all.yaml" 2>/dev/null \
  || die "could not render $POLICY_CHART"
policy_count="$(python3 "$work/filter.py" policies "$work/policies-all.yaml" "$work/policies.yaml")"

# Vacuity control 1: an empty or truncated policy render makes every
# assertion below pass trivially.
[ "$policy_count" -ge 20 ] || die "only $policy_count ClusterPolicies rendered; the baseline has 20+. Refusing to report a pass against a policy set this thin."
for enforced in probes-present resource-requests cilium-l7-mtls flux-managed \
                harbor-proxy-pull image-tag-pinned forbid-nodeport-service; do
  grep -q "name: $enforced" "$work/policies.yaml" \
    || die "the Enforce policy '$enforced' is not in the render — this gate cannot judge a policy set that is missing it."
done
echo "[kyverno] $policy_count ClusterPolicies rendered at canonical actions"

# ── the workload, as the Sovereign installs it ────────────────────────────
render_workload() { # <output> [extra helm args...]
  local out="$1"; shift
  "$helm" template chargeback "$PARENT_CHART" \
    --namespace chargeback \
    --api-versions cilium.io/v2 --api-versions postgresql.cnpg.io/v1 \
    --set sovereignFqdn=hw307.omani.works \
    --set docrender.enabled=true \
    "$@" > "$work/render.yaml" 2>/dev/null || return 1
  python3 "$work/filter.py" workload "$work/render.yaml" "$out"
}

count="$(render_workload "$work/workload.yaml")" || die "could not render $PARENT_CHART with docrender.enabled=true"

# Vacuity control 2: the gate must be looking at the objects it claims to.
[ "$count" -ge 5 ] || die "only $count docrender objects rendered; expected the Deployment, Service, NetworkPolicy, PodDisruptionBudget and ServiceAccount."
for kind in Deployment Service NetworkPolicy PodDisruptionBudget ServiceAccount; do
  grep -q "^kind: $kind$" "$work/workload.yaml" \
    || fail "the render carries no $kind — the chart is not shipping what this gate is meant to check."
done
echo "[kyverno] $count docrender objects rendered (Deployment, Service, NetworkPolicy, PDB, ServiceAccount)"

# ── the gate ──────────────────────────────────────────────────────────────
run_gate() { # <workload file> -> prints "pass fail warn error skip"
  local out
  out="$("$kyverno" apply "$work/policies.yaml" --resource "$1" 2>&1 || true)"
  printf '%s' "$out" > "$work/last-gate-output.txt"
  printf '%s\n' "$out" \
    | sed -n 's/^pass: \([0-9]*\), fail: \([0-9]*\), warn: \([0-9]*\), error: \([0-9]*\), skip: \([0-9]*\).*/\1 \2 \3 \4 \5/p' \
    | tail -1
}

read -r pass nfail nwarn nerror nskip <<<"$(run_gate "$work/workload.yaml")"
[ -n "${pass:-}" ] || die "could not read a result line out of the kyverno CLI. Output:
$(cat "$work/last-gate-output.txt")"

echo "[kyverno] pass=$pass fail=$nfail warn=$nwarn error=$nerror skip=$nskip"

# Vacuity control 3: zero failures out of zero evaluations is not a pass.
[ "$pass" -gt 0 ] || die "the CLI evaluated 0 rules against the render. A 'fail: 0' from a run that checked nothing is exactly the shape this repo keeps mistaking for green."

if [ "$nerror" -ne 0 ]; then
  echo "--- kyverno output ---" >&2
  cat "$work/last-gate-output.txt" >&2
  fail "$nerror policy evaluation(s) errored. An error is not a pass: the policy did not reach a verdict."
fi

if [ "$nfail" -ne 0 ]; then
  echo "--- kyverno output ---" >&2
  cat "$work/last-gate-output.txt" >&2
  fail "$nfail rule(s) DENIED a rendered object. On a post-handover Sovereign these are admission denials, not warnings: the workload would never start."
fi

echo "[kyverno] PASS — every rendered docrender object is admitted by the platform baseline ($pass rule evaluations, 0 fails)"

# ── self-test: prove the gate can go red ──────────────────────────────────
# A compliance gate nobody has SEEN fail is a gate nobody has proven works
# (#5473 / #5517 / A18). Each case below re-renders with one compliant
# property removed and asserts the gate notices. If any of them comes back
# clean, the gate is decorative and this script exits non-zero.
echo "[kyverno] self-test — each case must be DENIED"
selftest_rc=0
check_denied() { # <label> <expected policy name> <helm args...>
  local label="$1" policy="$2"; shift 2
  local f="$work/broken.yaml"
  if ! render_workload "$f" "$@" >/dev/null; then
    echo "  FAIL [$label]: the broken variant would not even render" >&2
    selftest_rc=1
    return
  fi
  local p n
  read -r p n _ _ _ <<<"$(run_gate "$f")"
  if [ "${n:-0}" -eq 0 ]; then
    echo "  FAIL [$label]: the gate reported 0 failures on a render that violates '$policy' — it is not actually checking." >&2
    selftest_rc=1
    return
  fi
  if ! grep -q "$policy" "$work/last-gate-output.txt"; then
    echo "  FAIL [$label]: the gate went red, but not on '$policy'. It may be failing for an unrelated reason, which would mask the real one." >&2
    selftest_rc=1
    return
  fi
  echo "  ok   [$label]: denied by $policy"
}

check_denied "single replica"   multi-replica-drainability --set docrender.replicaCount=1
check_denied "no OTel injection" otel-injected             --set docrender.observability.otelInstrumentation=""
check_denied "not scrape-targeted" prometheus-scrape       --set docrender.observability.scrape=false
check_denied "no hostname spread" topology-spread          --set docrender.topologySpread.enabled=false
check_denied "floating image tag" image-tag-pinned         --set docrender.image.tag=latest

[ "$selftest_rc" -eq 0 ] || die "the self-test could not make this gate fail. A gate that cannot go red proves nothing about the chart."
echo "[kyverno] self-test PASS — the gate goes red on every seeded defect"
echo "[kyverno] ALL PASS"
