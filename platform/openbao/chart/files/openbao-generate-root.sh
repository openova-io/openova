# openbao-generate-root.sh — sourced by the auth-bootstrap hook (bp-openbao,
# ADR-0015, Refs #7067). Defines bao_generate_root.
#
# The init root token is revoked by the first successful auth-bootstrap, so a
# later install/upgrade has no privileged token. It needs one for exactly
# two writes: the openbao-config-admin policy and role, which let the
# declarative openbao-config reconciler own every other policy/role/mount.
# The unseal-key shares already live in the cluster (Secret
# openbao-unseal-keys, written by the init Job; the unseal reconciler and the
# init Job's resume path read it). Generating a root from them adds no new
# exposure: whoever can read that Secret can already unseal and therefore
# generate a root. The token is revoked at the end of the same hook run.
#
# bao_generate_root prints the decoded root token on stdout and returns 0, or
# returns 1 (nothing printed) when the Secret, a key or a step is missing —
# any started attempt is cancelled first and on failure.
#
# Inputs (env): NAMESPACE; optional APISERVER, SA_TOKEN_FILE,
# UNSEAL_SECRET_NAME. Needs `bao` + `wget` + `base64` + `sed`.

# generate-root is an unauthenticated API: never send the (revoked) init token.
_gr_bao() { BAO_TOKEN="" bao "$@"; }

_gr_field() {  # _gr_field <json> <name> — first string value of "name"
  printf '%s' "$1" | tr -d '\n' | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p"
}

bao_generate_root() {
  _api="${APISERVER:-https://kubernetes.default.svc}"
  _sat="$(cat "${SA_TOKEN_FILE:-/var/run/secrets/kubernetes.io/serviceaccount/token}" 2>/dev/null)"
  _resp="$(wget -qO- --no-check-certificate --header="Authorization: Bearer ${_sat}" \
    "${_api}/api/v1/namespaces/${NAMESPACE}/secrets/${UNSEAL_SECRET_NAME:-openbao-unseal-keys}" 2>/dev/null)" || return 1
  _keys="$(_gr_field "${_resp}" unseal-keys-b64)"
  _thr="$(_gr_field "${_resp}" unseal-threshold)"
  [ -n "${_keys}" ] || return 1
  _thr="$( [ -n "${_thr}" ] && printf '%s' "${_thr}" | base64 -d 2>/dev/null )"
  case "${_thr}" in ''|*[!0-9]*|0) _thr=1 ;; esac
  _kf="$(mktemp)"
  printf '%s' "${_keys}" | base64 -d > "${_kf}" 2>/dev/null || { rm -f "${_kf}"; return 1; }

  _gr_bao operator generate-root -cancel >/dev/null 2>&1 || true
  _init="$(_gr_bao operator generate-root -init -format=json 2>/dev/null)" || { rm -f "${_kf}"; return 1; }
  _nonce="$(_gr_field "${_init}" nonce)"
  _otp="$(_gr_field "${_init}" otp)"
  if [ -z "${_nonce}" ] || [ -z "${_otp}" ]; then
    rm -f "${_kf}"; _gr_bao operator generate-root -cancel >/dev/null 2>&1; return 1
  fi
  _enc=""
  _i=0
  while [ "${_i}" -lt "${_thr}" ]; do
    _i=$((_i+1))
    _key="$(sed -n "${_i}p" "${_kf}")"
    _out=""
    if [ -z "${_key}" ] || ! _out="$(_gr_bao operator generate-root -nonce="${_nonce}" -format=json "${_key}" 2>/dev/null)"; then
      rm -f "${_kf}"; _gr_bao operator generate-root -cancel >/dev/null 2>&1; return 1
    fi
    _enc="$(_gr_field "${_out}" encoded_token)"
  done
  rm -f "${_kf}"
  if [ -z "${_enc}" ]; then _gr_bao operator generate-root -cancel >/dev/null 2>&1; return 1; fi
  _tok="$(_gr_bao operator generate-root -decode="${_enc}" -otp="${_otp}" 2>/dev/null | tail -n 1 | awk '{print $NF}')"
  [ -n "${_tok}" ] || return 1
  printf '%s' "${_tok}"
}
