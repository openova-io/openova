#!/usr/bin/env bash
# Shared settings and helpers of lab test ACC-020 (throwaway, never merged).
set -uo pipefail

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EV="${GITHUB_WORKSPACE:-$PWD}/evidence"
mkdir -p "$EV"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

# Upstream guacd 1.6.0, pinned by the digest of its multi-platform index.
GUACD_DIGEST=sha256:8974eaa9ba32f713daf311e7cc8cd7e4cdfba1edea39eed75524e78ef4b08f4f
GUACD_IMAGE=docker.io/guacamole/guacd@${GUACD_DIGEST}
K3S_VERSION=v1.31.4+k3s1
OPENBAO_IMAGE=quay.io/openbao/openbao:2.1.0
GCJS_BASE=https://repo1.maven.org/maven2/org/apache/guacamole/guacamole-common-js/1.6.0
BAO=http://127.0.0.1:8200
CREDS=/tmp/acc020-creds
PKI=/tmp/acc020-pki
SSH_ROLE=acc.acme.target

note() { echo "[$(date -u +%H:%M:%S)] $*" | tee -a "$EV/timeline.txt"; }
bao() { curl -sS -H "X-Vault-Token: root" "$@"; }

# The state file carries values from one workflow step to the next.
touch "$EV/state.env"
# shellcheck disable=SC1091
source "$EV/state.env"

render_pod() { # name [relaxed]
  local name=$1 relaxed=${2:-0}
  sed -e "s#@@NAME@@#${name}#g" -e "s#@@GUACD_IMAGE@@#${GUACD_IMAGE}#g" -e "s#@@GUACD_BIN@@#${GUACD_BIN:-/opt/guacamole/sbin/guacd}#g" \
    "$SPIKE_DIR/k8s/session-pod.yaml" | if [ "$relaxed" = 1 ]; then grep -v '#strict'; else cat; fi
}

# time_to_ready NAME [relaxed]: create a session pod and measure the seconds
# from the create call to the Ready condition (polled every 100 ms), plus the
# API's own timestamps. Appends one line to k-ready.txt.
time_to_ready() {
  local name=$1 relaxed=${2:-0} st t0 t1 deadline
  t0=$(date +%s.%N)
  render_pod "$name" "$relaxed" | kubectl apply -f - >/dev/null
  deadline=$(( $(date +%s) + 300 ))
  while :; do
    st=$(kubectl -n guacamole get pod "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
    [ "$st" = "True" ] && break
    if [ "$(date +%s)" -gt "$deadline" ]; then
      echo "$name relaxed=$relaxed NOT-READY-AFTER-300s" | tee -a "$EV/k-ready.txt"
      return 1
    fi
    sleep 0.1
  done
  t1=$(date +%s.%N)
  local created ready guacd_started agent_started
  created=$(kubectl -n guacamole get pod "$name" -o jsonpath='{.metadata.creationTimestamp}')
  ready=$(kubectl -n guacamole get pod "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}')
  guacd_started=$(kubectl -n guacamole get pod "$name" -o jsonpath='{.status.containerStatuses[?(@.name=="guacd")].state.running.startedAt}')
  agent_started=$(kubectl -n guacamole get pod "$name" -o jsonpath='{.status.containerStatuses[?(@.name=="agent")].state.running.startedAt}')
  echo "$name relaxed=$relaxed create_to_ready_s=$(python3 -c "print(round($t1-$t0,2))") api_created=$created api_ready=$ready guacd_started=$guacd_started agent_started=$agent_started" | tee -a "$EV/k-ready.txt"
}

# guacd_cat FILE: print a file of the recording volume (the guacd image has a shell).
guacd_cat() { kubectl -n guacamole exec access-spike -c guacd -- cat "/rec/$1" 2>/dev/null; }
