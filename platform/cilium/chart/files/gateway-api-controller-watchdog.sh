#!/usr/bin/env bash
# bp-cilium — gateway-api-controller-watchdog (#6255)
#
# ─── THE DEFECT THIS EXISTS FOR ────────────────────────────────────────────────
#
# cilium-operator decides ONCE, at process start, whether to run its Gateway-API
# controller. Upstream cilium v1.19.3,
# `operator/pkg/gateway-api/cell.go:135`:
#
#     ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
#
# That 30 seconds is a hardcoded literal. It is not a flag — `gatewayApiConfig.
# Flags()` (cell.go:182-192) registers `enable-gateway-api-secrets-sync`,
# `gateway-api-xff-num-trusted-hops`, `gateway-api-hostnetwork-enabled` and
# friends, and NOTHING that widens this budget — so no value in
# `operator.extraArgs` can reach it. When the budget expires, `isTransientError`
# (cell.go:323-346) does not classify the resulting `context deadline exceeded`
# as transient, so control falls into cell.go:148-155:
#
#     params.Logger.Error("Required GatewayAPI resources are not found, ...")
#     return &gatewayAPIPreconditions{Enabled: false}, nil
#
# …which returns **nil error**. The hive cell therefore SUCCEEDS. The operator
# stays alive and reports healthy on /healthz, and `initGatewayAPIController`
# (cell.go:210-213) returns immediately on `!Preconditions.Enabled` without ever
# registering the controller. There is no re-entry for the process lifetime.
#
# Measured on Sovereign hw296 (dep e689e3b34a75fdec, 2-region me-east-215-a/-b),
# 2026-08-13. Region B, at boot:
#
#     18:32:05 warn  Failed to check GatewayAPI CRDs due to transient error, will retry
#                    error="... dial tcp 10.179.1.83:6443: connect: connection refused"
#     18:32:35 error Required GatewayAPI resources are not found, ...
#     18:32:35 info  Invoked duration=30.024306733s function="gateway-api.initGatewayAPIController"
#
# Region A, same chart, same version, same boot: `Invoked duration=16.84419141s`,
# no error, controller running. 16.8s vs 30.0s is the ENTIRE difference. Region
# B's own apiserver was simply slower to answer, which on a fresh 2-region prov
# — and on every region-kill recovery, where the apiserver comes back cold — is
# a NORMAL condition, not an error.
#
# Downstream, region B published 6 of the console Gateway's 10 listeners and
# stopped reconciling it. The console EIP pool spans both regions, so every
# per-Org customer door answered on exactly 50% of fresh TCP connections
# (measured 15/30 served, against a 27/30 control on the same EIP). That is what
# holds UAT rows R16 / 87 / 90 / 95 red.
#
# ─── WHY THIS IS A DEPLOYMENT-LEVEL FIX ───────────────────────────────────────
#
# The bounded retry is upstream Go we do not own, and it is not reachable from
# any chart value or operator flag. So our fix is at the deployment level: keep
# an unbounded retry OUTSIDE the operator process, and re-invoke the operator
# when the precondition it gave up on is finally satisfiable. That is a watch,
# not a one-shot poll — exactly what cell.go lacks.
#
# The bootstrap-kit already orders `bp-cilium dependsOn bp-gateway-api` (#2614),
# and that is still correct and still necessary — but ordering only constrains
# the FIRST install. It does nothing for the case this watchdog exists for: the
# operator pod restarting (node reboot, eviction, OOM, region-kill recovery)
# while its own apiserver is still cold.
#
# ─── WHAT IT DOES ─────────────────────────────────────────────────────────────
#
#   1. Waits — UNBOUNDED, with capped exponential backoff — for the local
#      apiserver to answer AND the gateway.networking.k8s.io CRDs to be
#      Established. No attempt ceiling, no deadline. This is the retry the
#      operator does not have.
#   2. Then evaluates, on every pass, whether THIS cluster's Gateway-API
#      controller is actually reconciling.
#   3. If it is not, for `WATCHDOG_FAILURE_THRESHOLD` consecutive passes, it
#      deletes the local cilium-operator Pod. The replacement boots with the
#      CRDs already Established, so cell.go's 30s budget is satisfied on the
#      first poll — region A's fast path.
#   4. Loops forever, so a later apiserver flap that restarts the operator into
#      the same race is healed too.
#
# ─── HOW LIVENESS IS DETECTED WITHOUT TRUSTING STALE STATUS ───────────────────
#
# Region B's Gateway carried 6 status listeners written by an EARLIER operator
# instance and kept them after the controller died — a bare `Accepted=True` or a
# non-empty `.status.listeners` proves nothing. Every check here is a LIVE
# COMPARISON that a stale write cannot satisfy:
#
#   * GatewayClass/cilium — `Accepted=True` AND
#     `observedGeneration == metadata.generation`.
#   * every Gateway on that class — `len(status.listeners) == len(spec.listeners)`
#     AND the Accepted condition's `observedGeneration == metadata.generation`.
#
# `Programmed` is deliberately NOT asserted. Under the §854 hostPort / Local-ETP
# model BOTH regions legitimately report `Programmed=False / AddressNotAssigned`
# — #5511 already recorded that, and asserting on it would fire on every healthy
# Sovereign.
#
# A pass that cannot reach the apiserver returns UNKNOWN and is NOT counted
# toward the failure threshold. Restarting the operator during an apiserver
# outage would only re-run the exact race this fixes.

set -uo pipefail

INTERVAL="${WATCHDOG_INTERVAL_SECONDS:-30}"
CRD_BACKOFF_MIN="${WATCHDOG_CRD_BACKOFF_MIN_SECONDS:-1}"
CRD_BACKOFF_MAX="${WATCHDOG_CRD_BACKOFF_MAX_SECONDS:-30}"
THRESHOLD="${WATCHDOG_FAILURE_THRESHOLD:-3}"
RESTART_COOLDOWN="${WATCHDOG_RESTART_COOLDOWN_SECONDS:-300}"
GWC="${WATCHDOG_GATEWAY_CLASS:-cilium}"
OPERATOR_NS="${WATCHDOG_OPERATOR_NAMESPACE:-kube-system}"
OPERATOR_SELECTOR="${WATCHDOG_OPERATOR_SELECTOR:-io.cilium/app=operator,name=cilium-operator}"

# The three kinds cell.go's `requiredGVKs` insists on. If any is not Established
# the operator's own check would fail, so there is nothing to restart it into.
REQUIRED_CRDS="${WATCHDOG_REQUIRED_CRDS:-gatewayclasses.gateway.networking.k8s.io gateways.gateway.networking.k8s.io httproutes.gateway.networking.k8s.io}"

LAST_RESTART=0

log() { printf '[gw-api-watchdog] %s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# echoes exactly one of: established | missing | unreachable
crd_probe() {
  local c out rc
  for c in $REQUIRED_CRDS; do
    out="$(kubectl get crd "$c" -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>&1)"
    rc=$?
    if [ "$rc" -ne 0 ]; then
      case "$out" in
        *"not found"*|*NotFound*|*"the server doesn't have a resource type"*) echo missing; return 0 ;;
        *) echo unreachable; return 0 ;;
      esac
    fi
    [ "$out" = "True" ] || { echo missing; return 0; }
  done
  echo established
}

# UNBOUNDED. There is deliberately no attempt cap and no deadline here — that
# ceiling IS the defect (cell.go:135). A slow apiserver at operator boot is a
# normal condition on a fresh 2-region prov and on every region-kill recovery.
wait_for_crds() {
  local attempt=0 backoff="$CRD_BACKOFF_MIN" state
  while true; do
    attempt=$((attempt + 1))
    state="$(crd_probe)"
    if [ "$state" = established ]; then
      log "crd-wait satisfied attempt=${attempt} (gateway.networking.k8s.io CRDs Established)"
      return 0
    fi
    log "crd-wait attempt=${attempt} state=${state} — retrying in ${backoff}s (no ceiling)"
    sleep "$backoff"
    backoff=$((backoff * 2))
    [ "$backoff" -gt "$CRD_BACKOFF_MAX" ] && backoff="$CRD_BACKOFF_MAX"
  done
}

# echoes "<LIVE|DEAD|UNKNOWN> <detail>"
controller_verdict() {
  local gc_out rc gen obs st gws_out drift

  gc_out="$(kubectl get gatewayclass "$GWC" -o json 2>&1)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    case "$gc_out" in
      *"not found"*|*NotFound*|*"the server doesn't have a resource type"*)
        # gatewayClass.create=false, or Gateway API simply unused here. Nothing
        # to assert — never restart the operator on this.
        echo "LIVE gatewayclass/${GWC}-absent"; return 0 ;;
      *) echo "UNKNOWN gatewayclass-read-failed"; return 0 ;;
    esac
  fi

  gen="$(jq -r '.metadata.generation // 0' <<<"$gc_out" 2>/dev/null)"
  st="$(jq -r  '[.status.conditions[]? | select(.type=="Accepted")][0].status // "Missing"' <<<"$gc_out" 2>/dev/null)"
  obs="$(jq -r '[.status.conditions[]? | select(.type=="Accepted")][0].observedGeneration // -1' <<<"$gc_out" 2>/dev/null)"
  if [ -z "$gen" ] || [ -z "$st" ]; then
    echo "UNKNOWN gatewayclass-unparseable"; return 0
  fi
  if [ "$st" != "True" ] || [ "$obs" != "$gen" ]; then
    # `Accepted: Missing` with observedGeneration -1 is the exact signature of a
    # controller that never started: the chart created the GatewayClass
    # (gatewayClass.create="true", #503) and nothing ever wrote its status.
    echo "DEAD gatewayclass/${GWC} accepted=${st} observedGeneration=${obs} generation=${gen}"
    return 0
  fi

  gws_out="$(kubectl get gateways.gateway.networking.k8s.io --all-namespaces -o json 2>&1)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "UNKNOWN gateway-list-failed"; return 0
  fi

  drift="$(jq -r --arg gwc "$GWC" '
    [ .items[]?
      | select(.spec.gatewayClassName == $gwc)
      | { ns: .metadata.namespace,
          name: .metadata.name,
          gen: (.metadata.generation // 0),
          spec_listeners:   ((.spec.listeners   // []) | length),
          status_listeners: ((.status.listeners // []) | length),
          obs: ([ .status.conditions[]? | select(.type=="Accepted") | (.observedGeneration // -1) ][0] // -1) }
      | select( .spec_listeners != .status_listeners or .obs != .gen )
      | "\(.ns)/\(.name) spec=\(.spec_listeners) status=\(.status_listeners) generation=\(.gen) observedGeneration=\(.obs)"
    ] | join("; ")' <<<"$gws_out" 2>/dev/null)"
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "UNKNOWN gateway-list-unparseable"; return 0
  fi
  if [ -n "$drift" ]; then
    echo "DEAD $drift"; return 0
  fi

  echo "LIVE all-current"
}

restart_operator() {
  local now
  now="$(date +%s)"
  if [ "$LAST_RESTART" -ne 0 ] && [ "$((now - LAST_RESTART))" -lt "$RESTART_COOLDOWN" ]; then
    log "restart SUPPRESSED — last restart was $((now - LAST_RESTART))s ago, cooldown is ${RESTART_COOLDOWN}s. A controller that stays dead across a restart is a different fault; restart-storming it would only hide that."
    return 0
  fi
  log "restarting cilium-operator (-n ${OPERATOR_NS} -l ${OPERATOR_SELECTOR}) — the Gateway-API controller is not reconciling and the CRDs ARE Established, so the replacement will satisfy cell.go's 30s budget on its first poll"
  if kubectl -n "$OPERATOR_NS" delete pod -l "$OPERATOR_SELECTOR" --ignore-not-found >/dev/null 2>&1; then
    LAST_RESTART="$now"
    log "restart issued"
  else
    log "restart FAILED — could not delete cilium-operator Pod(s); will retry on the next threshold breach"
  fi
}

log "starting — #6255 unbounded Gateway-API CRD gate for cilium-operator (interval=${INTERVAL}s threshold=${THRESHOLD} cooldown=${RESTART_COOLDOWN}s class=${GWC})"
wait_for_crds

consecutive=0
while true; do
  verdict_line="$(controller_verdict)"
  verdict="${verdict_line%% *}"
  detail="${verdict_line#* }"
  case "$verdict" in
    LIVE)
      if [ "$consecutive" -ne 0 ]; then
        log "recovered after ${consecutive} consecutive DEAD pass(es)"
      fi
      consecutive=0
      log "pass verdict=LIVE detail=${detail}"
      ;;
    UNKNOWN)
      # Explicitly NOT counted. Restarting during an apiserver outage re-runs
      # the very race being fixed.
      log "pass verdict=UNKNOWN detail=${detail} — not counted toward the failure threshold"
      ;;
    DEAD)
      consecutive=$((consecutive + 1))
      log "pass verdict=DEAD consecutive=${consecutive}/${THRESHOLD} detail=${detail}"
      if [ "$consecutive" -ge "$THRESHOLD" ]; then
        restart_operator
        consecutive=0
      fi
      ;;
    *)
      # DEAD is the only verdict that may restart anything, so it must be named
      # explicitly. A catch-all `*)` that fell through to DEAD would turn any
      # future unrecognised verdict — or an empty line from a truncated
      # controller_verdict — into a Pod deletion. Unrecognised is UNKNOWN.
      log "pass verdict=UNRECOGNISED line='${verdict_line}' — treated as UNKNOWN, not counted"
      ;;
  esac
  sleep "$INTERVAL"
done
