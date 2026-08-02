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
# A solver that is SECONDS old is genuinely transient: a point-in-time hit is
# noise rather than a standing violation, so it is reported and never fatal.
#
# The carve-out used to end there, and that was the #5561 defect. It was
# written as if age were unobservable, so a solver 30 seconds old and a solver
# 70 DAYS old were both filed under "TRANSIENT — reaped per Challenge, not
# fatal". The live sweep on 2026-08-01 found the second kind: solvers standing
# since 2026-05-23 behind two Certificates wedged on `IncorrectIssuer`. Nothing
# was going to reap them, and the guard said so in a line that means the
# opposite.
#
# Age is right there in metadata.creationTimestamp. Read it, and the carve-out
# can say what it actually meant: transience is the reason a solver is excused,
# so a solver that is not transient is not excused. Past the threshold the
# NORMAL ownership rule applies — fatal in a namespace we own, reported in one
# we do not — because at that point it is an ordinary standing node port that
# happens to have been opened by cert-manager.
EPHEMERAL_RE='^cm-acme-http-solver-'

# A Challenge that has not completed within an hour is not mid-flight; HTTP-01
# validation is seconds-to-minutes. Overridable so a slow-DNS environment can
# raise it rather than delete the check.
STUCK_AFTER_SECONDS="${STUCK_AFTER_SECONDS:-3600}"

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
  # stdin: a Service-list JSON.
  # stdout: one "ns/name<TAB>type<TAB>ports<TAB>age_seconds" line per hit.
  #
  # age_seconds is -1 when metadata.creationTimestamp is absent or unparseable.
  # -1 must never be treated as "young": an unknown age is exactly the case
  # where the transient carve-out cannot be justified, so the caller treats it
  # as stuck. Failing that direction keeps the guard closed.
  python3 -c '
import json,sys,datetime
try:
    d=json.load(sys.stdin)
except Exception as e:
    print("PARSE-ERROR: %s" % e, file=sys.stderr); sys.exit(2)
now=datetime.datetime.now(datetime.timezone.utc)
def age(m):
    ts=m.get("creationTimestamp")
    if not ts: return -1
    try:
        c=datetime.datetime.strptime(ts,"%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=datetime.timezone.utc)
    except Exception:
        return -1
    return int((now-c).total_seconds())
for s in d.get("items",[]):
    sp=s.get("spec",{}) or {}
    nps=[p.get("nodePort") for p in (sp.get("ports") or []) if p.get("nodePort")]
    if sp.get("type")=="NodePort" or nps:
        m=s.get("metadata",{}) or {}
        print("%s/%s\t%s\t%s\t%d" % (m.get("namespace","?"), m.get("name","?"),
                                     sp.get("type","?"),
                                     ",".join(str(n) for n in nps) or "-", age(m)))
'
}

# seconds -> compact human age, for the report only.
human_age() {
  local s="$1"
  if [ "${s}" -lt 0 ]; then echo "age?"
  elif [ "${s}" -lt 3600 ]; then echo "$((s/60))m"
  elif [ "${s}" -lt 86400 ]; then echo "$((s/3600))h"
  else echo "$((s/86400))d"
  fi
}

# Bucket one hit. Kept as a function so the self-test can exercise the SAME
# code the sweep uses — a classifier tested only through a copy of itself is
# not tested.
#   TRANSIENT     solver younger than the threshold — genuine mid-flight noise
#   STUCK-OURS    solver past the threshold in a namespace we own  -> FATAL
#   STUCK-FOREIGN solver past the threshold in a namespace we do not own
#   FOREIGN       ordinary node port outside our namespaces
#   OURS          ordinary node port in a namespace we own          -> FATAL
classify() {
  local ns="$1" name="$2" age="$3"
  if printf '%s' "${name}" | grep -qE "${EPHEMERAL_RE}"; then
    # age -1 (unknown) deliberately falls through to STUCK, not TRANSIENT.
    if [ "${age}" -ge 0 ] && [ "${age}" -lt "${STUCK_AFTER_SECONDS}" ]; then
      echo "TRANSIENT"; return
    fi
    if printf '%s' "${ns}" | grep -qE "${NOT_OURS_RE}"; then
      echo "STUCK-FOREIGN"; return
    fi
    echo "STUCK-OURS"; return
  fi
  if printf '%s' "${ns}" | grep -qE "${NOT_OURS_RE}"; then echo "FOREIGN"; return; fi
  echo "OURS"
}

FRESH_TS="$(date -u -d '@'"$(( $(date -u +%s) - 60 ))" +%Y-%m-%dT%H:%M:%SZ)"
FIXTURE='{"items":[
 {"metadata":{"namespace":"t","name":"typed-nodeport"},"spec":{"type":"NodePort","ports":[{"nodePort":30001}]}},
 {"metadata":{"namespace":"t","name":"lb-with-nodeport"},"spec":{"type":"LoadBalancer","ports":[{"nodePort":32015}]}},
 {"metadata":{"namespace":"t","name":"lb-zero-sentinel"},"spec":{"type":"LoadBalancer","ports":[{"nodePort":0}]}},
 {"metadata":{"namespace":"t","name":"plain-clusterip"},"spec":{"type":"ClusterIP","ports":[{"port":80}]}},
 {"metadata":{"namespace":"t","name":"cm-acme-http-solver-fresh","creationTimestamp":"'"${FRESH_TS}"'"},"spec":{"type":"NodePort","ports":[{"nodePort":30111}]}},
 {"metadata":{"namespace":"t","name":"cm-acme-http-solver-stuck","creationTimestamp":"2026-05-23T10:00:00Z"},"spec":{"type":"NodePort","ports":[{"nodePort":30222}]}}
]}'

ST="$(printf '%s' "$FIXTURE" | detect || true)"
st_fail() { echo "SELF-TEST FAIL: $1" >&2; echo "detector output was:" >&2; printf '%s\n' "$ST" >&2; exit 2; }

printf '%s' "$ST" | grep -q 'typed-nodeport'   || st_fail "a type=NodePort Service was NOT detected"
printf '%s' "$ST" | grep -q 'lb-with-nodeport' || st_fail "a LoadBalancer carrying a nodePort was NOT detected (the #5348 shape)"
printf '%s' "$ST" | grep -q 'lb-zero-sentinel' && st_fail "nodePort:0 was flagged — the inert sentinel must be ignored"
printf '%s' "$ST" | grep -q 'plain-clusterip'  && st_fail "a plain ClusterIP was flagged — the detector is too broad"

# --- age discrimination (#5561) ------------------------------------------
# Both solvers are byte-identical apart from creationTimestamp, so these two
# assertions can only both hold if age is genuinely being read. A classifier
# that ignored age would put them in the same bucket and fail one of them.
A_FRESH="$(printf '%s' "$ST" | awk -F'\t' '$1=="t/cm-acme-http-solver-fresh"{print $4}')"
A_STUCK="$(printf '%s' "$ST" | awk -F'\t' '$1=="t/cm-acme-http-solver-stuck"{print $4}')"
[ -n "${A_FRESH}" ] && [ -n "${A_STUCK}" ] || st_fail "solver fixtures produced no age column"
[ "${A_FRESH}" -lt "${STUCK_AFTER_SECONDS}" ] || st_fail "a 60s-old solver measured ${A_FRESH}s — clock/parse is wrong"
[ "${A_STUCK}" -gt "${STUCK_AFTER_SECONDS}" ] || st_fail "a 2026-05-23 solver measured ${A_STUCK}s — clock/parse is wrong"

[ "$(classify t cm-acme-http-solver-fresh "${A_FRESH}")" = "TRANSIENT" ] \
  || st_fail "a 60s-old solver was not classed TRANSIENT"
[ "$(classify t cm-acme-http-solver-stuck "${A_STUCK}")" = "STUCK-OURS" ] \
  || st_fail "a 70-day-old solver was excused as transient — this IS #5561"
[ "$(classify iogrid cm-acme-http-solver-stuck "${A_STUCK}")" = "STUCK-FOREIGN" ] \
  || st_fail "a stuck solver in a foreign namespace lost its not-ours classification"
[ "$(classify t cm-acme-http-solver-noage -1)" = "STUCK-OURS" ] \
  || st_fail "an unknown-age solver was excused — unknown must fail closed"
# Vacuity: the classifier must not answer STUCK-OURS to everything.
[ "$(classify iogrid powerdns-anycast 999999)" = "FOREIGN" ] \
  || st_fail "a foreign non-solver was misclassified — the classifier is stuck-on"
[ "$(classify openova-system powerdns-anycast 999999)" = "OURS" ] \
  || st_fail "an owned non-solver was misclassified"

echo "OK — detector self-test passed (catches typed + LB-carried, ignores 0 and"
echo "   ClusterIP, and separates a transient solver from a stuck one by age)."

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

OWNED=""; FOREIGN=""; EPHEMERAL=""; STUCK_OURS=""; STUCK_FOREIGN=""; FATAL=0
if [ -n "${HITS}" ]; then
  while IFS=$'\t' read -r nsname stype ports age; do
    [ -z "${nsname}" ] && continue
    ns="${nsname%%/*}"; name="${nsname##*/}"
    row="  ${nsname}	${stype}	${ports}	$(human_age "${age}")"$'\n'
    case "$(classify "${ns}" "${name}" "${age}")" in
      TRANSIENT)     EPHEMERAL="${EPHEMERAL}${row}" ;;
      STUCK-OURS)    STUCK_OURS="${STUCK_OURS}${row}" ;;
      STUCK-FOREIGN) STUCK_FOREIGN="${STUCK_FOREIGN}${row}" ;;
      FOREIGN)       FOREIGN="${FOREIGN}${row}" ;;
      *)             OWNED="${OWNED}${row}" ;;
    esac
  done <<< "${HITS}"
fi

echo "Services scanned: ${TOTAL}"

if [ -n "${EPHEMERAL}" ]; then
  echo ""
  echo "TRANSIENT (cert-manager HTTP-01 solvers younger than $((STUCK_AFTER_SECONDS/60))m — mid-flight, not fatal):"
  printf '%s' "${EPHEMERAL}"
fi

if [ -n "${STUCK_FOREIGN}" ]; then
  echo ""
  echo "STUCK SOLVER, NOT OURS (past $((STUCK_AFTER_SECONDS/60))m — a wedged Challenge holding a node port"
  echo "open indefinitely; reported, not fatal, because the Certificate is in a namespace"
  echo "this repo does not own. Check its Certificate/Order for IncorrectIssuer):"
  printf '%s' "${STUCK_FOREIGN}"
fi

if [ -n "${FOREIGN}" ]; then
  echo ""
  echo "NOT OURS (reported, not fatal — the fix belongs in their repo):"
  printf '%s' "${FOREIGN}"
fi

if [ -n "${STUCK_OURS}" ]; then
  echo ""
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "FAIL: a cert-manager solver in an OWNED namespace has held a node port" >&2
  echo "open past $((STUCK_AFTER_SECONDS/60))m (§854, #5561). It is NOT transient:" >&2
  printf '%s' "${STUCK_OURS}" >&2
  echo "" >&2
  echo "Do NOT delete the Service — cert-manager recreates it while the Challenge" >&2
  echo "is unresolved. Fix the CHALLENGE: check the Certificate's Order/Challenge" >&2
  echo "for IncorrectIssuer or a failing self-check, or move the issuer to DNS-01," >&2
  echo "which needs no solver Service and therefore no node port at all." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  FATAL=1
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
  FATAL=1
fi

[ "${FATAL}" -eq 1 ] && exit 1

echo ""
echo "OK: zero live node ports in owned namespaces across ${TOTAL} Service(s) (#4765)."
exit 0
