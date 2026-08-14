#!/usr/bin/env bash
# check-gitrepository-clone-timeout.sh — the bootstrap GitRepository must
# declare a clone budget that a real clone of THIS repo can actually finish in
# (issue #6336).
#
# THE DEFECT THIS LOCKS OUT
# ------------------------
# `infra/providers/_shared/cloudinit-control-plane.tftpl` writes the Flux
# `GitRepository/openova` in `flux-system` that every fresh Sovereign bootstraps
# from. It carried NO `spec.timeout`, so the source-controller CRD default of
# 60s applied. Measured on hw297 region-b (2026-08-14) every reconcile died:
#
#   failed to checkout and determine revision: unable to clone
#   'https://github.com/openova-io/openova': context deadline exceeded
#
# -> bootstrap-kit / bootstrap-kit-crs / sovereign-tls all `Source artifact not
# found` -> `kubectl get helmrelease -A` empty -> Phase-1 watch saw 0
# HelmReleases -> the prov froze at `phase1-watching`. Patching ONLY this field
# to 10m produced `ready=True` after 651s (10m51s). The clone needed ~11
# minutes and had been given 60 seconds.
#
# WHY A TIMEOUT AND NOT A SHALLOW-CLONE KNOB
# ------------------------------------------
# source-controller v1.4.1 (the pin: platform/flux -> flux2 2.14.1 -> Flux
# v2.4.0) already shallow-clones unconditionally —
# `internal/controller/gitrepository_controller.go` builds
# `repository.CloneConfig{... ShallowClone: true}` with no conditional — and the
# `source.toolkit.fluxcd.io/v1` CRD exposes no depth / shallowClone /
# sparseCheckout field to tune. There is no lever here beyond the deadline.
# `spec.ignore` does not help either: it filters at ARTIFACT-BUILD time, after
# checkout, so the bytes still cross the wire.
#
# WHAT IS ASSERTED
# ----------------
#   1. The `GitRepository` named `openova` exists in the template.
#   2. It declares an OPERATIVE (non-comment) `spec.timeout`. Absence is a
#      failure in itself — absence means the 60s CRD default, which is the
#      exact defect.
#   3. That timeout is >= MIN (default 15m). The floor sits above the 651s
#      measured clone with headroom for tree growth / a slower link.
#   4. That timeout is <= MAX (default 60m) — the `DefaultWatchTimeout` of the
#      Phase-1 HelmRelease watch. A budget larger than the watch window turns an
#      unreachable origin into one silent endless attempt instead of a visible
#      failure inside the watch.
#
# Exit codes:
#   0 — the GitRepository's clone budget is present and within [MIN, MAX].
#   1 — missing, unparseable, too small, or too large.
#   2 — usage / setup error.
#
# Dependencies: bash, awk. No cluster, no network, no cloud call.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

TFTPL="${REPO_ROOT}/infra/providers/_shared/cloudinit-control-plane.tftpl"
GITREPO_NAME="openova"
MIN_SECONDS="${MIN_SECONDS:-900}"    # 15m — above the 651s measured clone
MAX_SECONDS="${MAX_SECONDS:-3600}"   # 60m — helmwatch DefaultWatchTimeout
SELF_TEST=0

usage() {
  cat <<EOF
Usage: $(basename "$0") [--tftpl FILE] [--name NAME] [--self-test]

  --tftpl FILE   control-plane cloud-init template to scan
                 (default: ${TFTPL})
  --name NAME    GitRepository metadata.name to check (default: ${GITREPO_NAME})
  --self-test    run the cluster-free fixture suite and exit
  -h, --help     show this message

Env: MIN_SECONDS (default 900), MAX_SECONDS (default 3600).
See issue #6336.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --tftpl)     TFTPL="$2"; shift 2 ;;
    --name)      GITREPO_NAME="$2"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

_err() { printf 'ERROR: %s\n' "$*" >&2; }
_ok()  { printf 'OK: %s\n' "$*"; }

# ---------------------------------------------------------------------------
# extract_timeout <file> <gitrepository-name>
#
# Prints the operative `timeout:` value declared inside the GitRepository
# document whose metadata.name matches, or nothing if there is none. Prints
# `__NO_SUCH_DOC__` when no GitRepository with that name exists at all, so the
# caller can tell "guard looked and found nothing to check" (a setup error, and
# the vacuity control for this guard) apart from "found the doc, no timeout"
# (the #6336 defect).
#
# The documents are embedded in a cloud-init `write_files` `content: |` block
# interleaved with HCL directives, so this is an indentation-agnostic scan
# rather than a YAML parse — the same technique
# scripts/check-cloudinit-kustomization-deps.sh uses. Comment lines are skipped
# so prose that quotes `timeout:` can never satisfy the check.
# ---------------------------------------------------------------------------
extract_timeout() {
  awk -v want="$2" '
    function trim(s){ sub(/^[ \t]+/,"",s); sub(/[ \t]+$/,"",s); return s }
    BEGIN{ in_doc=0; meta_seen=0; name=""; timeout=""; found=0 }
    {
      t=trim($0)
      if (t ~ /^#/) next          # comments are never operative

      if (t=="kind: GitRepository") {
        if (in_doc && name==want) { found=1; print timeout; exit }
        in_doc=1; meta_seen=0; name=""; timeout=""
        next
      }
      if (!in_doc) next

      if (t=="---" || t ~ /^apiVersion:/) {
        if (name==want) { found=1; print timeout; exit }
        in_doc=0; meta_seen=0; name=""; timeout=""
        next
      }

      # metadata.name is the FIRST `name:` in the document; later `name:` keys
      # (sourceRef.name, secretRef.name) must not shadow it.
      if (!meta_seen && t ~ /^name:[ ]*/) {
        v=t; sub(/^name:[ ]*/,"",v); gsub(/["'\'']/,"",v)
        name=trim(v); meta_seen=1
        next
      }

      if (t ~ /^timeout:[ ]*/) {
        v=t; sub(/^timeout:[ ]*/,"",v); gsub(/["'\'']/,"",v)
        timeout=trim(v)
        next
      }
    }
    END{ if (!found) { if (in_doc && name==want) print timeout; else print "__NO_SUCH_DOC__" } }
  ' "$1"
}

# ---------------------------------------------------------------------------
# duration_seconds <go-duration>
#   Converts a Go/Flux duration ("60s", "20m", "1h", "10m30s") to seconds on
#   stdout. Returns 1 and prints nothing when the string is not a duration.
# ---------------------------------------------------------------------------
duration_seconds() {
  local d="$1" total=0 num unit rest
  [ -n "${d}" ] || return 1
  rest="${d}"
  while [ -n "${rest}" ]; do
    num="${rest%%[smh]*}"
    case "${num}" in
      ''|*[!0-9]*) return 1 ;;
    esac
    rest="${rest#"${num}"}"
    unit="${rest:0:1}"
    rest="${rest:1}"
    case "${unit}" in
      s) total=$(( total + num )) ;;
      m) total=$(( total + num * 60 )) ;;
      h) total=$(( total + num * 3600 )) ;;
      *) return 1 ;;
    esac
  done
  printf '%s' "${total}"
}

# ---------------------------------------------------------------------------
# check_file <file> <name> -> 0 pass / 1 fail   (prints its own diagnostics)
# ---------------------------------------------------------------------------
check_file() {
  local file="$1" name="$2" raw secs
  if [ ! -f "${file}" ]; then
    _err "template not found: ${file}"
    return 1
  fi

  raw="$(extract_timeout "${file}" "${name}")"

  if [ "${raw}" = "__NO_SUCH_DOC__" ]; then
    _err "no GitRepository named '${name}' found in ${file}."
    _err "  The bootstrap source moved or was renamed. This guard has nothing to"
    _err "  assert and must not pass silently — point it at the new site (--name/--tftpl)."
    return 1
  fi

  if [ -z "${raw}" ]; then
    _err "GitRepository/${name} declares NO operative 'spec.timeout' in ${file}."
    _err "  Absence means the source-controller CRD default of 60s applies. On this"
    _err "  repo a depth-1 clone measured 651s (hw297, 2026-08-14), so every"
    _err "  reconcile fails 'context deadline exceeded', no artifact is ever built,"
    _err "  and a fresh Sovereign converges to ZERO HelmReleases. Issue #6336."
    return 1
  fi

  if ! secs="$(duration_seconds "${raw}")"; then
    _err "GitRepository/${name} timeout '${raw}' is not a parseable Go duration (want e.g. 20m)."
    return 1
  fi

  if [ "${secs}" -lt "${MIN_SECONDS}" ]; then
    _err "GitRepository/${name} timeout '${raw}' (${secs}s) is below the ${MIN_SECONDS}s floor."
    _err "  A full depth-1 clone of this repo measured 651s on hw297 (2026-08-14);"
    _err "  the floor keeps headroom for tree growth and slower links. Issue #6336."
    _err "  NOTE: the 5m of issue #492 is the kustomize-controller revision-lock"
    _err "  budget — it does not apply to a GitRepository, which holds no lock."
    return 1
  fi

  if [ "${secs}" -gt "${MAX_SECONDS}" ]; then
    _err "GitRepository/${name} timeout '${raw}' (${secs}s) exceeds the ${MAX_SECONDS}s ceiling."
    _err "  That is longer than helmwatch DefaultWatchTimeout (60m), so an"
    _err "  unreachable origin would never surface as a failure inside the Phase-1"
    _err "  watch window — it would look like one silent endless attempt."
    return 1
  fi

  _ok "GitRepository/${name} clone budget = ${raw} (${secs}s), within [${MIN_SECONDS}s, ${MAX_SECONDS}s] — issue #6336."
  return 0
}

# ---------------------------------------------------------------------------
# --self-test — fixtures only, no repo file, no cluster.
#
# Every fixture that MUST fail is asserted to fail, so a future edit that makes
# this guard structurally unable to fail (the classic dead-guard shape) is
# caught here. The `__NO_SUCH_DOC__` fixture is the vacuity control: a file with
# no matching GitRepository must NOT be reported as a pass.
# ---------------------------------------------------------------------------
run_self_test() {
  local tmp rc fails=0
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/gitrepo-timeout-selftest.XXXXXX")"
  # shellcheck disable=SC2064
  trap "rm -rf '${tmp}'" EXIT

  _fixture() { printf '%s\n' "$2" > "${tmp}/$1"; }

  _fixture absent.yaml '      apiVersion: source.toolkit.fluxcd.io/v1
      kind: GitRepository
      metadata:
        name: openova
        namespace: flux-system
      spec:
        interval: 1m
        url: https://github.com/openova-io/openova
        ref:
          branch: main'

  _fixture sixty.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        interval: 1m
        timeout: 60s'

  _fixture five-min.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        timeout: 5m'

  _fixture good.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        interval: 1m
        timeout: 20m
        url: https://github.com/openova-io/openova'

  _fixture too-long.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        timeout: 2h'

  _fixture wrong-name.yaml '      kind: GitRepository
      metadata:
        name: some-other-source
      spec:
        timeout: 20m'

  # Prose that merely quotes an operative-looking key must not satisfy the check.
  _fixture comment-only.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        # timeout: 20m   <- commentary, not a declaration
        interval: 1m'

  # A later sourceRef-style `name:` must not shadow metadata.name.
  _fixture shadowed-name.yaml '      kind: GitRepository
      metadata:
        name: openova
      spec:
        timeout: 20m
        secretRef:
          name: not-the-doc-name'

  # Two GitRepository docs in one file — the wanted one is second.
  _fixture two-docs.yaml '      kind: GitRepository
      metadata:
        name: other-source
      spec:
        timeout: 1h
      ---
      apiVersion: source.toolkit.fluxcd.io/v1
      kind: GitRepository
      metadata:
        name: openova
      spec:
        timeout: 20m'

  _expect() { # <fixture> <want-rc> <why>
    local out
    out="$(check_file "${tmp}/$1" "${GITREPO_NAME}" 2>&1)"; rc=$?
    if [ "${rc}" -ne "$2" ]; then
      printf 'SELF-TEST FAIL: %-20s expected rc=%s got rc=%s (%s)\n' "$1" "$2" "${rc}" "$3" >&2
      printf '%s\n' "${out}" | sed 's/^/    /' >&2
      fails=$((fails+1))
    else
      printf 'SELF-TEST ok:   %-20s rc=%s (%s)\n' "$1" "${rc}" "$3"
    fi
  }

  _expect absent.yaml        1 'no timeout at all => 60s CRD default => the #6336 defect'
  _expect sixty.yaml         1 'explicit 60s is the measured-failing value'
  _expect five-min.yaml      1 '5m is the #492 Kustomization budget, still under the 651s clone'
  _expect good.yaml          0 '20m clears the 651s measurement and stays under the watch window'
  _expect too-long.yaml      1 '2h outruns the 60m Phase-1 watch'
  _expect wrong-name.yaml    1 'vacuity control: nothing named openova => must not pass'
  _expect comment-only.yaml  1 'commented-out timeout is not a declaration'
  _expect shadowed-name.yaml 0 'metadata.name survives a later secretRef.name'
  _expect two-docs.yaml      0 'the wanted doc is found even as the second document'

  # Duration parser unit checks (the arithmetic the verdicts rest on).
  _dur() {
    local got; got="$(duration_seconds "$1")" || got="ERR"
    if [ "${got}" != "$2" ]; then
      printf 'SELF-TEST FAIL: duration_seconds(%s) = %s, want %s\n' "$1" "${got}" "$2" >&2
      fails=$((fails+1))
    else
      printf 'SELF-TEST ok:   duration_seconds(%-7s) = %s\n' "$1" "${got}"
    fi
  }
  _dur 60s 60
  _dur 5m 300
  _dur 20m 1200
  _dur 1h 3600
  _dur 10m51s 651
  _dur banana ERR
  _dur 20 ERR

  if [ "${fails}" -gt 0 ]; then
    printf '\nSELF-TEST: %d failure(s) — this guard cannot be trusted until they are fixed.\n' "${fails}" >&2
    return 1
  fi
  printf '\nSELF-TEST: all fixtures behaved as specified (failing cases DID fail).\n'
  return 0
}

if [ "${SELF_TEST}" -eq 1 ]; then
  run_self_test
  exit $?
fi

check_file "${TFTPL}" "${GITREPO_NAME}"
exit $?
