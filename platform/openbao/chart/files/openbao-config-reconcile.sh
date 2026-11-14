#!/bin/sh
# openbao-config reconciler (bp-openbao, ADR-0015, Refs #7067).
#
# Applies the chart-rendered desired state ($CONFIG_DIR/manifest + one JSON
# payload per op) to OpenBao, forever, idempotently:
#
#   write <api-path> [probe]   POST $CONFIG_DIR/<api-path with / -> .>.json
#   mount <sys/auth/x> [probe] enable auth mount x unless sys/auth lists it
#   secrets-mount <sys/mounts/x> [probe]
#                              enable secrets mount x unless sys/mounts lists it
#
# - Before a payload is posted, every AUTH_ACCESSOR_KUBERNETES in it is
#   replaced by the accessor of the auth mount $BAO_K8S_AUTH_PATH (read once
#   per tick from the sys/auth listing); the result is posted from $PAYLOAD.
#   No accessor while a payload needs one ends the tick.
# - Ops run top to bottom; a failed POLICY write ends the tick before any
#   role that names it is written (#5829 ordering lesson).
# - An op with a probe URL is skipped (and retried next tick) while the probe
#   does not answer — a peer region not reachable yet never blocks the rest.
# - The login token is reused while auth/token/lookup-self accepts it.
# POSIX sh + wget only (busybox in the alpine image; GNU wget in the tests).
set -u
: "${BAO_ADDR:?}" "${BAO_K8S_AUTH_PATH:?}" "${BAO_ROLE:?}" "${INTERVAL_SECONDS:?}"
D="${CONFIG_DIR:-/etc/openbao-config/desired}"
SA_TOKEN_FILE="${SA_TOKEN_FILE:-/var/run/secrets/kubernetes.io/serviceaccount/token}"
HEARTBEAT="${HEARTBEAT_FILE:-/tmp/openbao-config-heartbeat}"
PAYLOAD="${PAYLOAD_FILE:-/tmp/openbao-config-payload.json}"
MAX_TICKS="${MAX_TICKS:-0}"
BAO_TOKEN=""

log() { printf '%s | %s\n' "$(date -u +%FT%TZ)" "$*"; }
get() { wget -qO- --header="X-Vault-Token: ${BAO_TOKEN}" "${BAO_ADDR%/}/v1/$1" 2>/dev/null; }
post() {
  wget -qO- --header="X-Vault-Token: ${BAO_TOKEN}" --header='Content-Type: application/json' \
    --post-file "$2" "${BAO_ADDR%/}/v1/$1" >/dev/null 2>&1
}
login() {
  if [ -n "${BAO_TOKEN}" ] && get auth/token/lookup-self >/dev/null; then return 0; fi
  sa="$(cat "${SA_TOKEN_FILE}" 2>/dev/null)"
  BAO_TOKEN="$(wget -qO- --header='Content-Type: application/json' \
    --post-data "{\"role\":\"${BAO_ROLE}\",\"jwt\":\"${sa}\"}" \
    "${BAO_ADDR%/}/v1/auth/${BAO_K8S_AUTH_PATH}/login" 2>/dev/null \
    | sed -n 's/.*"client_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "${BAO_TOKEN}" ]
}

# tick — one pass over the manifest. 0 = fully converged, 1 = something left.
tick() {
  if ! login; then
    log "WARN: login as role ${BAO_ROLE} on auth/${BAO_K8S_AUTH_PATH} failed (auth-bootstrap has not bound it yet?)"
    return 1
  fi
  if ! mounts="$(get sys/auth)"; then
    log "WARN: cannot list auth mounts"; BAO_TOKEN=""; return 1
  fi
  if ! smounts="$(get sys/mounts)"; then
    log "WARN: cannot list secrets mounts"; BAO_TOKEN=""; return 1
  fi
  acc="$(printf '%s' "${mounts}" | tr -d '\n' | sed -n "s/.*\"${BAO_K8S_AUTH_PATH}\/\":{\"accessor\":\"\([^\"]*\)\".*/\1/p")"
  pending=0
  n=0
  while read -r kind path probe; do
    case "${kind}" in ''|'#'*) continue ;; esac
    key="${D}/$(printf '%s' "${path}" | tr / .).json"
    if [ -n "${probe:-}" ] && ! wget -qO- "${probe}" >/dev/null 2>&1; then
      log "pending ${path}: ${probe} does not answer yet"; pending=1; continue
    fi
    if grep -q AUTH_ACCESSOR_KUBERNETES "${key}" 2>/dev/null; then
      if [ -z "${acc}" ]; then log "WARN: no accessor for ${BAO_K8S_AUTH_PATH}"; return 1; fi
      sed "s/AUTH_ACCESSOR_KUBERNETES/${acc}/g" "${key}" > "${PAYLOAD}"
      key="${PAYLOAD}"
    fi
    case "${kind}" in
      mount)
        m="${path#sys/auth/}"
        case "${mounts}" in *"\"${m}/\""*) continue ;; esac
        if post "${path}" "${key}"; then log "enabled auth mount ${m}/"; n=$((n+1)); else log "WARN: enable ${m}/ failed"; pending=1; fi
        ;;
      secrets-mount)
        m="${path#sys/mounts/}"
        case "${smounts}" in *"\"${m}/\""*) continue ;; esac
        if post "${path}" "${key}"; then log "enabled secrets mount ${m}/"; n=$((n+1)); else log "WARN: enable secrets mount ${m}/ failed"; pending=1; fi
        ;;
      write)
        if post "${path}" "${key}"; then
          n=$((n+1))
        else
          log "WARN: write ${path} failed"; pending=1
          case "${path}" in sys/policies/*) log "a policy write failed — ending the tick before its roles"; return 1 ;; esac
        fi
        ;;
      *) log "WARN: unknown op '${kind}' for ${path}"; pending=1 ;;
    esac
  done < "${D}/manifest"
  [ "${pending}" = 0 ] && log "converged (${n} op(s) applied)"
  return "${pending}"
}

log "openbao-config starting; addr=${BAO_ADDR} mount=${BAO_K8S_AUTH_PATH} role=${BAO_ROLE}"
ticks=0
rc=0
while true; do
  touch "${HEARTBEAT}"
  tick; rc=$?
  ticks=$((ticks+1))
  if [ "${MAX_TICKS}" -gt 0 ] && [ "${ticks}" -ge "${MAX_TICKS}" ]; then exit "${rc}"; fi
  sleep "${INTERVAL_SECONDS}"
done
