#!/usr/bin/env bash
# check-orphaned-admission-webhooks — an admission webhook whose target Service
# or namespace does not resolve is a latent cluster-wide wedge.
#
# WHY (#5567). The mothership carried TEN Kyverno admission-webhook
# configurations for 98 days pointing at `kyverno-smoke/…` — a namespace and a
# Service that no longer exist. Six of them are `failurePolicy: Fail`. They
# match only Kyverno's own CRD types, so ordinary workloads were unaffected,
# but they are SELF-BLOCKING: `kyverno-policy-validating-webhook-cfg` AND
# `kyverno-policy-mutating-webhook-cfg` both intercept clusterpolicies on
# CREATE and fail closed against a Service that cannot resolve, so re-installing
# Kyverno wedges on its first ClusterPolicy. The §854 admission floor
# (forbid-nodeport-service, #5088) therefore cannot be restored on that cluster.
#
# Nothing detected this. It was found by hand while walking a different issue,
# and the original census undercounted 10 as 7 because it looked only at
# ValidatingWebhookConfiguration — which is exactly the kind of partial read
# this script exists to replace.
#
# READ-ONLY. Reports; never deletes. Deleting admission webhooks on a live
# control plane is an operator decision.
#
# Usage:
#   scripts/check-orphaned-admission-webhooks.sh [--kubeconfig PATH] [--strict]
#   scripts/check-orphaned-admission-webhooks.sh --self-test
#
# Exit: 0 clean (or self-test passed), 1 orphan found, 2 usage/precondition.
set -euo pipefail

KUBECONFIG_ARG=""
STRICT=0
SELF_TEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig) KUBECONFIG_ARG="--kubeconfig=$2"; shift 2 ;;
    --kubeconfig=*) KUBECONFIG_ARG="$1"; shift ;;
    --strict) STRICT=1; shift ;;
    --self-test|--selftest) SELF_TEST=1; shift ;;
    -h|--help) sed -n '1,30p' "$0"; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
done

# ─── the classifier, shared by --self-test and the live sweep ───────────────
#
# stdin: one record per line, TAB-separated:
#   <kind>\t<config-name>\t<webhook-name>\t<failurePolicy>\t<ns>/<svc>\t<resolves:0|1>
# stdout: VERDICT lines + one ORPHAN line per finding.
classify_webhooks() {
  # NOTE: `python3 - <<'PY'` would make the HEREDOC stdin, so the piped records
  # would never be read and every estate would classify CLEAN. Caught by this
  # file's own self-test on first run. The script goes in via -c; stdin stays
  # the data.
  python3 -c "$(cat <<'PY'
import sys
orphans, total = [], 0
for line in sys.stdin:
    line = line.rstrip("\n")
    if not line:
        continue
    kind, cfg, wh, fp, target, resolves = line.split("\t")
    total += 1
    if resolves == "0":
        orphans.append((kind, cfg, wh, fp, target))

print("TOTAL\t%d" % total)
if not orphans:
    print("VERDICT\tCLEAN")
    sys.exit(0)

# A fail-closed orphan is strictly worse: it REJECTS rather than bypasses.
closed = [o for o in orphans if o[3] == "Fail"]
print("VERDICT\tORPHANED")
print("FAILCLOSED\t%d" % len(closed))
for kind, cfg, wh, fp, target in orphans:
    print("ORPHAN\t%s\t%s\t%s\t%s\t%s" % (kind, cfg, wh, fp, target))
PY
)"
}

verdict_of() { grep '^VERDICT' | cut -f2; }

# ─── self-test — the classifier must go red on the real hw-mothership shape ──
if [ "${SELF_TEST}" -eq 1 ]; then
  st_fail() { echo "SELF-TEST FAIL: $1" >&2; exit 2; }

  # The #5567 shape, verbatim from the live mothership read (2026-08-07),
  # including the MUTATING config the original census missed.
  ORPHANED="$(printf '%s\n' \
    "ValidatingWebhookConfiguration	kyverno-policy-validating-webhook-cfg	validate-policy.kyverno.svc	Fail	kyverno-smoke/smoke-kyverno-svc	0" \
    "MutatingWebhookConfiguration	kyverno-policy-mutating-webhook-cfg	mutate-policy.kyverno.svc	Fail	kyverno-smoke/smoke-kyverno-svc	0")"
  V="$(printf '%s\n' "${ORPHANED}" | classify_webhooks | verdict_of)"
  [ "${V}" = "ORPHANED" ] || st_fail "the live #5567 shape (2 configs, dead target) read '${V}', expected ORPHANED"

  n="$(printf '%s\n' "${ORPHANED}" | classify_webhooks | grep '^FAILCLOSED' | cut -f2)"
  [ "${n}" = "2" ] || st_fail "fail-closed count was '${n}', expected 2 — a Fail-policy orphan REJECTS, it does not merely bypass"

  # CONTROL: a healthy webhook whose Service resolves must read CLEAN, so the
  # check is selective rather than "any webhook is an orphan".
  HEALTHY="ValidatingWebhookConfiguration	cert-manager-webhook	webhook.cert-manager.io	Fail	cert-manager/cert-manager-webhook	1"
  V="$(printf '%s\n' "${HEALTHY}" | classify_webhooks | verdict_of)"
  [ "${V}" = "CLEAN" ] || st_fail "a RESOLVING webhook read '${V}', expected CLEAN — the check must not flag every webhook"

  # CONTROL: mixed estate — one healthy, one orphan — must still report ORPHANED
  # and must count only the orphan.
  MIXED="$(printf '%s\n' "${HEALTHY}" \
    "MutatingWebhookConfiguration	kyverno-verify-mutating-webhook-cfg	verify.kyverno.svc	Ignore	kyverno-smoke/smoke-kyverno-svc	0")"
  V="$(printf '%s\n' "${MIXED}" | classify_webhooks | verdict_of)"
  [ "${V}" = "ORPHANED" ] || st_fail "mixed estate read '${V}', expected ORPHANED"
  n="$(printf '%s\n' "${MIXED}" | classify_webhooks | grep '^FAILCLOSED' | cut -f2)"
  [ "${n}" = "0" ] || st_fail "mixed-estate fail-closed count was '${n}', expected 0 — the orphan there is failurePolicy: Ignore"

  # VACUITY: an empty estate must not read CLEAN by accident of having no rows
  # to inspect — it must report TOTAL 0 so a silent no-op is visible.
  t="$(printf '' | classify_webhooks | grep '^TOTAL' | cut -f2)"
  [ "${t}" = "0" ] || st_fail "empty input reported TOTAL '${t}', expected 0"

  echo "OK — classifier self-test passed (5 assertions: the live #5567 two-config shape"
  echo "   is ORPHANED with 2 fail-closed, a resolving webhook is CLEAN, a mixed estate"
  echo "   is ORPHANED counting only the orphan, and an empty estate is visibly empty)."
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

command -v kubectl >/dev/null 2>&1 || { echo "kubectl not found" >&2; exit 2; }

# ─── live sweep ─────────────────────────────────────────────────────────────
# BOTH kinds. The #5567 census undercounted precisely because it read only
# ValidatingWebhookConfiguration; a mutating orphan wedges the same install.
# Same -c form as classify_webhooks, and for the same reason: a heredoc here
# would BE stdin, so the kubectl JSON would never reach the parser. The first
# version of this script had that bug in both places; the live sweep failed
# loudly (JSONDecodeError) rather than reporting CLEAN, which is the behaviour
# to preserve — a reader that cannot read must never look like a clean estate.
export OAW_KUBECONFIG_ARG="${KUBECONFIG_ARG}"
RECORDS="$(kubectl ${KUBECONFIG_ARG} get validatingwebhookconfiguration,mutatingwebhookconfiguration -o json 2>/dev/null | python3 -c "$(cat <<'PY'
import json, subprocess, sys, os
data = json.load(sys.stdin)
kubeconfig = os.environ.get("OAW_KUBECONFIG_ARG", "")
seen = {}

def resolves(ns, name):
    key = (ns, name)
    if key in seen:
        return seen[key]
    cmd = ["kubectl"]
    if kubeconfig:
        cmd.append(kubeconfig)
    cmd += ["get", "service", "-n", ns, name, "-o", "name"]
    ok = subprocess.run(cmd, stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL).returncode == 0
    seen[key] = ok
    return ok

for item in data.get("items", []):
    kind = item.get("kind", "")
    cfg = item.get("metadata", {}).get("name", "")
    for wh in (item.get("webhooks") or []):
        svc = (wh.get("clientConfig", {}) or {}).get("service") or {}
        ns, name = svc.get("namespace"), svc.get("name")
        if not ns or not name:
            # url-based webhook: no in-cluster Service to resolve. Out of scope.
            continue
        print("\t".join([kind, cfg, wh.get("name", ""),
                         wh.get("failurePolicy", ""), f"{ns}/{name}",
                         "1" if resolves(ns, name) else "0"]))
PY
)")" || { echo "failed to read webhook configurations" >&2; exit 2; }

RESULT="$(printf '%s\n' "${RECORDS}" | classify_webhooks)"
TOTAL="$(printf '%s' "${RESULT}" | grep '^TOTAL' | cut -f2)"
VERDICT="$(printf '%s' "${RESULT}" | verdict_of)"

if [ "${VERDICT}" = "CLEAN" ]; then
  echo "OK: all ${TOTAL} Service-backed admission webhook(s) resolve to a live Service."
  exit 0
fi

CLOSED="$(printf '%s' "${RESULT}" | grep '^FAILCLOSED' | cut -f2)"
echo "ORPHANED ADMISSION WEBHOOKS — target Service does not resolve:"
printf '%s\n' "${RESULT}" | grep '^ORPHAN' | while IFS=$'\t' read -r _ kind cfg wh fp target; do
  printf '  %-32s %-46s %-8s -> %s\n' "${kind}" "${cfg}" "${fp}" "${target}"
done
echo
echo "  ${CLOSED} of them are failurePolicy: Fail — those REJECT matching admissions"
echo "  rather than bypassing, so whatever they intercept cannot be created (#5567)."
echo
echo "  Read-only check: nothing was deleted. Removing admission webhooks on a live"
echo "  control plane is an operator decision."

[ "${STRICT}" -eq 1 ] && exit 1
exit 1
