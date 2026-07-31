#!/usr/bin/env bash
# check-live-nodeports.sh — CLUSTER-side §854 guard (#5348).
#
# Founder, 2026-07-03: "YOU CAN NEVER EVER USE NODEPORT EVEN FOR TESTING
# PURPOSE!!! ... FUCK THE NODEPORTS!!!" (#4765).
#
# WHY THIS EXISTS SEPARATELY FROM scripts/check-no-nodeports.sh
#
# That script scans repo sources + `helm template` output. It is the right
# guard for "did someone write a NodePort into the catalog", and it cannot —
# structurally — catch a live Service that drifted away from a spec which
# still renders correctly.
#
# Proven on the mothership 2026-08-01 (#5348). openova-system/powerdns-anycast:
#
#     type                         : LoadBalancer
#     allocateLoadBalancerNodePorts: False     <- the anti-nodePort flag SET
#       port 53/UDP  nodePort=32015            <- and the ports open anyway
#       port 53/TCP  nodePort=31425
#
# The chart set the flag, omitted nodePort, and even failed the render on
# serviceType=NodePort. check-no-nodeports.sh passed. node:32015/udp was open
# the whole time. Two Kubernetes behaviours produce that:
#
#   1. allocateLoadBalancerNodePorts:false only stops the apiserver allocating
#      NEW node ports — ports already assigned are NOT deallocated.
#   2. A declarative apply that OMITS nodePort does not clear an existing
#      allocation; the field reads as unmanaged and the value survives every
#      reconcile.
#
# So a reviewer reading the spec flag scores it compliant while the datapath
# is in violation. Only a live read of spec.ports[].nodePort sees it.
#
# WHAT COUNTS AS A VIOLATION HERE
#
# A Service is in scope if `spec.type == "NodePort"` OR **any**
# `spec.ports[].nodePort` is non-zero — NOT type alone. Counting by type is
# exactly the mistake that made #5348 read as "3 live NodePort services" when
# a full sweep of the same cluster found 13 Services carrying a node port.
# `nodePort: 0` is the k8s "unspecified/released" sentinel and is inert.
#
# Usage:
#   scripts/check-live-nodeports.sh                     # current kube context
#   scripts/check-live-nodeports.sh --context <ctx>
#   scripts/check-live-nodeports.sh --kubeconfig <path>
#   scripts/check-live-nodeports.sh --self-test         # no cluster needed
#
# Exit: 0 = clean, 1 = a node port is live in an OWNED namespace, 2 = setup.

set -euo pipefail

CONTEXT=""
KUBECONFIG_ARG=""
SELF_TEST_ONLY=0

# Namespaces this platform does NOT own. A node port here is reported for
# visibility and does NOT fail the guard — we cannot fix someone else's chart,
# and failing on it would train people to ignore the guard.
#
#   cinova          — ⛔ never-touch (fix belongs in foundrylab-app/cinova)
#   iogrid / ping / ping-marketing — separate projects on shared infra
#   stalwart        — ⛔ founder mailbox, never touched from here
#   kube-system     — shared cluster infra not authored by this repo
NOT_OURS_RE='^(cinova|iogrid|ping|ping-marketing|stalwart|kube-system)$'

# cert-manager HTTP-01 challenge solvers are created and reaped per Challenge.
# They are genuinely transient, so a point-in-time hit is noise rather than a
# standing violation — reported, never fatal. (A solver that lingers for days
# means a STUCK Challenge; that is a cert problem, not a §854 problem.)
EPHEMERAL_RE='^cm-acme-http-solver-'

while [ $# -gt 0 ]; do
  case "$1" in
    --context)    CONTEXT="$2"; shift 2 ;;
    --kubeconfig) KUBECONFIG_ARG="$2"; shift 2 ;;
    --self-test)  SELF_TEST_ONLY=1; shift ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--context <ctx>] [--kubeconfig <path>] [--self-test]" >&2
       exit 2 ;;
  esac
done

# ─── Phase 0 — detector self-test (vacuity guard) ────────────────────────
#
# An absence-assertion reports "clean" both when the thing is absent AND when
# the check stopped working. This guard therefore proves its own detector
# against a fixture before it is allowed to report on a real cluster:
#   - a NodePort-typed Service must be caught
#   - a LoadBalancer carrying a nodePort must be caught  <- the #5348 shape
#   - `nodePort: 0` must NOT be caught (inert sentinel)
#   - a plain ClusterIP must NOT be caught
# Without the negative cases a detector that flags everything would also pass.
detect() {
  # stdin: a Service-list JSON. stdout: one "ns/name type ports" line per hit.
  python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
except Exception as e:
    print("PARSE-ERROR: %s" % e, file=sys.stderr); sys.exit(2)
for s in d.get("items",[]):
    sp=s.get("spec",{}) or {}
    nps=[p.get("nodePort") for p in (sp.get("ports") or []) if p.get("nodePort")]
    if sp.get("type")=="NodePort" or nps:
        m=s.get("metadata",{}) or {}
        print("%s/%s\t%s\t%s" % (m.get("namespace","?"), m.get("name","?"),
                                 sp.get("type","?"), ",".join(str(n) for n in nps) or "-"))
'
}

FIXTURE='{"items":[
 {"metadata":{"namespace":"t","name":"typed-nodeport"},"spec":{"type":"NodePort","ports":[{"nodePort":30001}]}},
 {"metadata":{"namespace":"t","name":"lb-with-nodeport"},"spec":{"type":"LoadBalancer","ports":[{"nodePort":32015}]}},
 {"metadata":{"namespace":"t","name":"lb-zero-sentinel"},"spec":{"type":"LoadBalancer","ports":[{"nodePort":0}]}},
 {"metadata":{"namespace":"t","name":"plain-clusterip"},"spec":{"type":"ClusterIP","ports":[{"port":80}]}}
]}'

ST="$(printf '%s' "$FIXTURE" | detect || true)"
st_fail() { echo "SELF-TEST FAIL: $1" >&2; echo "detector output was:" >&2; printf '%s\n' "$ST" >&2; exit 2; }

printf '%s' "$ST" | grep -q 'typed-nodeport'   || st_fail "a type=NodePort Service was NOT detected"
printf '%s' "$ST" | grep -q 'lb-with-nodeport' || st_fail "a LoadBalancer carrying a nodePort was NOT detected (the #5348 shape)"
printf '%s' "$ST" | grep -q 'lb-zero-sentinel' && st_fail "nodePort:0 was flagged — the inert sentinel must be ignored"
printf '%s' "$ST" | grep -q 'plain-clusterip'  && st_fail "a plain ClusterIP was flagged — the detector is too broad"
echo "OK — detector self-test passed (catches typed + LB-carried, ignores 0 and ClusterIP)."

if [ "${SELF_TEST_ONLY}" -eq 1 ]; then
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

# ─── Phase 1 — live cluster sweep ────────────────────────────────────────
if ! command -v kubectl >/dev/null 2>&1; then
  echo "WARN: kubectl not on PATH — cannot sweep a live cluster." >&2
  echo "      This run proves the DETECTOR only, not any cluster." >&2
  exit 0
fi

KCTL=(kubectl)
[ -n "${KUBECONFIG_ARG}" ] && KCTL+=(--kubeconfig "${KUBECONFIG_ARG}")
[ -n "${CONTEXT}" ] && KCTL+=(--context "${CONTEXT}")

echo ""
echo "== live Service sweep (${CONTEXT:-current context}) =="
RAW="$("${KCTL[@]}" get svc -A -o json 2>/dev/null || true)"
if [ -z "${RAW}" ]; then
  echo "WARN: could not read Services (cluster unreachable / no permission)." >&2
  echo "      Reporting NOTHING rather than a false clean." >&2
  exit 0
fi

TOTAL="$(printf '%s' "${RAW}" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("items",[])))')"
HITS="$(printf '%s' "${RAW}" | detect || true)"

OWNED=""; FOREIGN=""; EPHEMERAL=""
if [ -n "${HITS}" ]; then
  while IFS= read -r line; do
    [ -z "${line}" ] && continue
    nsname="${line%%$'\t'*}"; ns="${nsname%%/*}"; name="${nsname##*/}"
    if printf '%s' "${name}" | grep -qE "${EPHEMERAL_RE}"; then
      EPHEMERAL="${EPHEMERAL}  ${line}"$'\n'
    elif printf '%s' "${ns}" | grep -qE "${NOT_OURS_RE}"; then
      FOREIGN="${FOREIGN}  ${line}"$'\n'
    else
      OWNED="${OWNED}  ${line}"$'\n'
    fi
  done <<< "${HITS}"
fi

echo "Services scanned: ${TOTAL}"

if [ -n "${EPHEMERAL}" ]; then
  echo ""
  echo "TRANSIENT (cert-manager HTTP-01 solvers — reaped per Challenge, not fatal):"
  printf '%s' "${EPHEMERAL}"
fi

if [ -n "${FOREIGN}" ]; then
  echo ""
  echo "NOT OURS (reported, not fatal — the fix belongs in their repo):"
  printf '%s' "${FOREIGN}"
fi

if [ -n "${OWNED}" ]; then
  echo ""
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "FAIL: a live node port in an OWNED namespace (§854, #4765/#5348):" >&2
  printf '%s' "${OWNED}" >&2
  echo "" >&2
  echo "Do NOT kubectl patch this away — these Services are Helm/Flux managed" >&2
  echo "and the next reconcile restores the port. Fix the CHART: set" >&2
  echo "allocateLoadBalancerNodePorts:false AND state nodePort:0 explicitly on" >&2
  echo "each port, because omitting the field does not clear an existing" >&2
  echo "allocation (that is exactly how #5348 stayed open behind a clean" >&2
  echo "render). See platform/powerdns/chart/templates/anycast-endpoint.yaml." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi

echo ""
echo "OK: zero live node ports in owned namespaces across ${TOTAL} Service(s) (#4765)."
exit 0
