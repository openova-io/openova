#!/usr/bin/env bash
# check-cloudinit-primary-kubeconfig-capture.sh — the PRIMARY region's
# kubeconfig PUT-back is emitted, is fail-loud, and every control plane keeps
# its own cloud-init log. Refs #6339.
#
# ─────────────────────────────────────────────────────────────────────────────
# WHY THIS GUARD EXISTS
#
# Measured on hw297 and hw298, two consecutive 2-region Huawei provs:
#
#   GET /sovereign/api/v1/deployments/{id}/kubeconfig                    -> 409
#   GET .../kubeconfig?region=me-east-215-a   (the PRIMARY region)       -> 409
#   GET .../kubeconfig?region=me-east-215-b-1 (the SECONDARY region)     -> 200
#
# The secondary's capture works. The primary's never landed, so there is no
# route to the primary cluster at all — which is why the failed
# `cutover-egress-block-test` Job on hw298 could not be read and UAT rows G11 /
# 166 / 227 cannot be root-caused.
#
# TWO surfaces had to hold for that failure to be INVISIBLE, and this guard
# pins both:
#
#  1. WHICH BRANCH THE PRIMARY REGION RUNS. Pre-#6339 both the primary PUT and
#     the secondary POST shipped in EVERY region's user_data and a runtime
#     `[ "$HOSTNAME" = "$PRIMARY_HOST" ]` compare picked one at boot. Any drift
#     between the ECS `name` formula and the booted hostname flipped region 0
#     into the SECONDARY branch — and `lifecycle.ignore_changes=[name]` on the
#     CP resource deliberately pins an ACTIVE CP's name across a retry_attempt
#     bump while PRIMARY_HOST is recomputed from the CURRENT one, so the drift
#     is reachable. A region-0 REPLICA CP hit it unconditionally. Worse, no CI
#     check could ever assert "the primary region carries the primary PUT-back"
#     because which branch fired was a RUNTIME fact. It is a RENDER-time fact
#     now (`%{ if idx == 0 }`) and this guard asserts it on the RENDERED text.
#
#  2. WHETHER THE FAILURE LEAVES A TRACE. The PUT loop echoed one line per
#     attempt and fell through in silence, and the only sink was
#     `<id>-cloudinit.log` — ONE key that every CP in every region overwrites
#     every 30s for ~50 minutes. Measured across the 13 archived captures in
#     docs/sessions/**/prov-diagnostics/ that carry a PUT-back line: each holds
#     exactly ONE hostname, 7 region-a and 6 region-b, never both. So on a
#     2-region prov the primary's log — the only place its PUT-back outcome is
#     written — is destroyed by the secondary's uploader.
#
# ─────────────────────────────────────────────────────────────────────────────
# HOW IT MEASURES (and why it is not the stale-fixture trap)
#
# scripts/lint-cloudinit.sh and scripts/cloudinit-size-check.sh both render the
# SHARED template with a HAND-WRITTEN `kubeconfig_put_block` fixture that has
# drifted years away from the real provider block — no primary/secondary split,
# 6 attempts instead of 12, no hostname gate. Neither guard has ever read the
# real subject. This one extracts the REAL heredoc out of
# infra/providers/huawei/main.tf and renders it with REAL `tofu`, substituting
# ONLY the one expression tofu cannot know without an apply (the CP EIP
# address). The `%{ if idx == 0 }` directive and every `${…}` ternary are
# evaluated by tofu itself, not re-implemented here.
#
# USAGE
#   scripts/check-cloudinit-primary-kubeconfig-capture.sh
#   scripts/check-cloudinit-primary-kubeconfig-capture.sh --self-test
#   scripts/check-cloudinit-primary-kubeconfig-capture.sh --render   # evidence
#
# EXIT: 0 = every assertion holds. 1 = an assertion failed. 2 = the harness
#       could not measure (missing tofu, missing file, render error) — never
#       reported as a pass.
#
# REQUIREMENTS: tofu (render), python3 (JSON decode). No cluster, no network,
#               no cloud credentials. Nothing is provisioned or modified.
set -uo pipefail

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TOFU_BIN="${TOFU_BIN:-tofu}"
PY_BIN="${PY_BIN:-python3}"

HUAWEI_TF="infra/providers/huawei/main.tf"
SHARED_TPL="infra/providers/_shared/cloudinit-control-plane.tftpl"
LOG_HANDLER="products/catalyst/bootstrap/api/internal/handler/cloudinit_log.go"

C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_RST=$'\033[0m'
[ -t 1 ] || { C_RED=; C_GRN=; C_RST=; }

FAILURES=0
pass() { printf '%s  PASS%s %s\n' "${C_GRN}" "${C_RST}" "$1"; }
fail() { printf '%s  FAIL%s %s\n' "${C_RED}" "${C_RST}" "$1"; FAILURES=$((FAILURES + 1)); }
die()  { printf '%sFATAL%s %s\n' "${C_RED}" "${C_RST}" "$1" >&2; exit 2; }

# ─────────────────────────────────────────────────────────────────────────────
# render_put_blocks <root> — extract the real kubeconfig_put_block heredoc from
# the Huawei provider and render it, per region, through tofu. Prints the
# rendered blocks as `=== <region-key>` sections on stdout.
# ─────────────────────────────────────────────────────────────────────────────
render_put_blocks() {
  local root="$1" tf="$1/${HUAWEI_TF}" work body
  [ -f "${tf}" ] || die "not found: ${tf}"

  work="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '${work}'" RETURN

  # Extract the heredoc BODY: everything strictly between the
  # `kubeconfig_put_block = <<-PUT` line and the closing `PUT` marker.
  body="$(awk '
    /kubeconfig_put_block = <<-PUT$/ { grab = 1; next }
    grab && /^[[:space:]]*PUT[[:space:]]*$/ { grab = 0; next }
    grab { print }
  ' "${tf}")"
  [ -n "${body}" ] || die "could not extract the kubeconfig_put_block heredoc from ${tf} (anchor 'kubeconfig_put_block = <<-PUT' moved?)"

  # The ONLY substitution: the CP EIP is a resource attribute that has no value
  # until apply. Everything else — local.name_prefix, local.region_keys, idx,
  # r.code, substr(), sha256(), var.* — is evaluated by tofu below.
  # Greedy `.+` on purpose: the index expression itself contains a `]`
  # (`local.region_keys[0]`), so a non-greedy/negated-class match stops early
  # and leaves the resource reference behind — which then fails to evaluate.
  body="$(printf '%s\n' "${body}" |
    sed -E 's/huaweicloud_vpc_eip\.cp\[(.+)\]\.publicip\.0\.ip_address/local.cp_eip[\1]/g')"
  case "${body}" in
    *huaweicloud_vpc_eip*)
      die "the CP-EIP substitution did not fully apply — a resource reference survived into the render harness" ;;
  esac

  {
    cat <<'HCL'
variable "regions" {
  type    = list(object({ code = string }))
  default = [{ code = "me-east-215-a" }, { code = "me-east-215-b" }]
}
variable "deployment_id" { default = "6339cafe6339cafe" }
variable "kubeconfig_bearer_token" { default = "bearerFIXTURE0001" }
variable "catalyst_api_url" { default = "https://console.openova.io/sovereign" }
variable "retry_attempt" { default = 0 }

locals {
  name_prefix = "catalyst-t99-omani-works-6339cafe"
  region_keys = [for r in var.regions : r.code]
  cp_eip      = { "me-east-215-a" = "203.0.113.10", "me-east-215-b" = "203.0.113.20" }

  blocks = {
    for idx, r in var.regions :
    r.code => <<-PUT
HCL
    printf '%s\n' "${body}"
    cat <<'HCL'
    PUT
  }
}

output "blocks_json" { value = jsonencode(local.blocks) }
HCL
  } > "${work}/main.tf"

  ( cd "${work}" && "${TOFU_BIN}" init -backend=false -input=false -no-color >/dev/null 2>&1 ) ||
    die "tofu init failed in the render harness"
  local out
  out="$( cd "${work}" && "${TOFU_BIN}" apply -auto-approve -input=false -no-color 2>&1 )" || {
    printf '%s\n' "${out}" >&2
    die "tofu apply (render) failed — the extracted heredoc did not evaluate"
  }

  printf '%s\n' "${out}" | "${PY_BIN}" -c '
import json, sys
raw = None
for line in sys.stdin:
    if line.startswith("blocks_json = "):
        raw = line.split(" = ", 1)[1].strip()
        break
if raw is None:
    sys.stderr.write("no blocks_json output in tofu apply result\n")
    sys.exit(2)
blocks = json.loads(json.loads(raw))
for key in sorted(blocks):
    print("=== " + key)
    print(blocks[key])
' || die "could not decode the rendered blocks"
}

# section <rendered> <region-key> — the rendered text for one region.
section() {
  printf '%s\n' "$1" | awk -v want="=== $2" '
    $0 == want { grab = 1; next }
    /^=== / { grab = 0 }
    grab { print }
  '
}

want_in()    { case "$2" in *"$3"*) pass "$1";; *) fail "$1";; esac; }
want_notin() { case "$2" in *"$3"*) fail "$1";; *) pass "$1";; esac; }

file_has() {
  local label="$1" path="$2" needle="$3"
  [ -f "${path}" ] || { fail "${label} — file missing: ${path}"; return; }
  if grep -qF -- "${needle}" "${path}"; then pass "${label}"; else fail "${label}"; fi
}

run_checks() {
  local root="$1" rendered primary secondary
  rendered="$(render_put_blocks "${root}")" || return 2

  primary="$(section "${rendered}" "me-east-215-a")"
  secondary="$(section "${rendered}" "me-east-215-b")"
  [ -n "${primary}" ]   || { fail "primary region rendered an EMPTY kubeconfig PUT-back block"; return 1; }
  [ -n "${secondary}" ] || { fail "secondary region rendered an EMPTY kubeconfig PUT-back block"; return 1; }

  # ── (1) the primary region actually captures the primary kubeconfig ──
  want_in "primary render: PUTs to /deployments/<id>/kubeconfig" \
    "${primary}" "/api/v1/deployments/6339cafe6339cafe/kubeconfig"
  want_in "primary render: uses -X PUT (the capture verb)" \
    "${primary}" "-X PUT"
  want_in "primary render: labels its attempts 'primary kubeconfig PUT-back'" \
    "${primary}" "primary kubeconfig PUT-back"

  # The mis-registration path #6339 closed: region 0 must NOT also carry the
  # SECONDARY BRANCH, or a hostname mismatch silently registers the PRIMARY
  # region under a bare region key and GET /kubeconfig stays 409 forever.
  # Discriminate on the branch's own artefacts, NOT on the endpoint URL: the
  # primary's fail-loud path deliberately reuses that endpoint as a fallback,
  # so a URL match is true either way and the assertion could not fail. (It
  # read as PASS on the pre-fix tree during the red run that proved this guard
  # — the exact "guard tested a surface that cannot fail" shape.)
  want_notin "primary render: does NOT carry the 'secondary kubeconfig PUT-back' label" \
    "${primary}" "secondary kubeconfig PUT-back"
  want_notin "primary render: does NOT carry the secondary branch body (kubeconfig-secondary.yaml)" \
    "${primary}" "/tmp/kubeconfig-secondary.yaml"

  # ── (2) failure is LOUD, not a silent fall-through ──
  want_in "primary render: prints PRIMARY-KUBECONFIG-CAPTURE-FAILED on exhaustion" \
    "${primary}" "PRIMARY-KUBECONFIG-CAPTURE-FAILED"
  want_in "primary render: carries the last HTTP code into the fail-loud line" \
    "${primary}" 'lastHTTP=${LAST_CODE}'
  want_in "primary render: drops the kubeconfig-put-FAILED sentinel (pushes this node's log)" \
    "${primary}" "kubeconfig-put-FAILED"
  want_in "primary render: falls back to the per-region channel the secondaries prove works" \
    "${primary}" "PRIMARY-KUBECONFIG-FALLBACK"

  # ── (3) the secondary path is untouched ──
  want_in "secondary render: POSTs to /sovereign/secondary-kubeconfig" \
    "${secondary}" "/api/v1/sovereign/secondary-kubeconfig"
  want_in "secondary render: labels its attempts 'secondary kubeconfig PUT-back'" \
    "${secondary}" "secondary kubeconfig PUT-back"
  want_notin "secondary render: does NOT carry the primary PUT branch" \
    "${secondary}" "primary kubeconfig PUT-back"

  # ── (4) every CP keeps its OWN cloud-init log ──
  # Without this the fail-loud line above lands in a file the other region
  # overwrites 30 seconds later, and the guard would be pinning a message
  # nobody can ever read.
  file_has "huawei prelude uploader is per-node (?node=)" \
    "${root}/${HUAWEI_TF}" 'cloudinit-log?node=$(hostname)'
  file_has "shared-template fail-loud 'mark' pusher is per-node (?node=)" \
    "${root}/${SHARED_TPL}" 'cloudinit-log?node=$(hostname)'
  file_has "catalyst-api keeps a per-node capture alongside the shared one" \
    "${root}/${LOG_HANDLER}" 'id+"-cloudinit-"+node+".log"'

  # ── (5) the OTHER cloud-init gates are not lying about their subject ──
  # scripts/lint-cloudinit.sh (dash -n) and scripts/cloudinit-size-check.sh
  # (byte cap) both render the SHARED template with a HAND-WRITTEN
  # `kubeconfig_put_block` fixture. Those copies had drifted so far from the
  # real provider block that neither gate had ever parsed or weighed the shell
  # the control plane actually runs — Huawei's byte measurement was ~1.2 KB
  # optimistic and the dash lint was checking a block that does not exist.
  # Pin the load-bearing tokens so the next drift fails here instead of on a
  # lost provision.
  local fixture
  for fixture in scripts/lint-cloudinit.sh scripts/cloudinit-size-check.sh; do
    file_has "${fixture} fixture carries the real primary PUT-back label" \
      "${root}/${fixture}" "primary kubeconfig PUT-back"
    file_has "${fixture} fixture carries the real fail-loud banner" \
      "${root}/${fixture}" "PRIMARY-KUBECONFIG-CAPTURE-FAILED"
  done

  [ "${FAILURES}" -eq 0 ]
}

# ─────────────────────────────────────────────────────────────────────────────
# --self-test — regress a COPY of the tree to each pre-#6339 shape and prove
# the guard goes RED on it. A guard nobody has watched fail is not evidence.
# ─────────────────────────────────────────────────────────────────────────────
self_test() {
  local tmp rc st_fail=0
  printf '[self-test] positive control — the real tree must PASS\n'
  FAILURES=0
  run_checks "${ROOT}" >/dev/null 2>&1
  rc=$?
  if [ "${rc}" -eq 0 ]; then
    pass "self-test: real tree passes"
  else
    fail "self-test: real tree FAILS its own guard (rc=${rc})"
    st_fail=1
  fi

  # Regression 1 — the pre-#6339 runtime-gated shape: region 0's render also
  # carries the secondary POST branch.
  tmp="$(mktemp -d)"
  cp -r "${ROOT}/infra" "${ROOT}/products" "${ROOT}/scripts" "${tmp}/" 2>/dev/null
  "${PY_BIN}" - "${tmp}/${HUAWEI_TF}" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
# Drop the render-time split so BOTH branches ship in every region again.
# Whitespace-tolerant: `tofu fmt` rewrites `%{~ if x ~}` to `%{~if x~}`, so a
# literal-string mutation would silently no-op and the regression would go
# unmeasured — the mutation MUST be asserted to have changed the file.
s2 = re.sub(r"(?m)^%\{~\s*(if [^}]*|else|endif)\s*~\}\n", "", s)
if s2 == s:
    sys.exit("self-test mutation 1 changed NOTHING — the directive anchor moved")
open(p, "w").write(s2)
PY
  [ $? -eq 0 ] || { fail "self-test: could not apply the both-branches mutation"; st_fail=1; }
  FAILURES=0
  run_checks "${tmp}" >/dev/null 2>&1
  rc=$?
  if [ "${rc}" -ne 0 ]; then
    pass "self-test: RED on the pre-#6339 both-branches-in-every-region shape"
  else
    fail "self-test: guard PASSED the both-branches shape it exists to catch"
    st_fail=1
  fi
  rm -rf "${tmp}"

  # Regression 2 — fail-loud stripped: the PUT loop falls through in silence.
  tmp="$(mktemp -d)"
  cp -r "${ROOT}/infra" "${ROOT}/products" "${ROOT}/scripts" "${tmp}/" 2>/dev/null
  "${PY_BIN}" - "${tmp}/${HUAWEI_TF}" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
s2 = s.replace("PRIMARY-KUBECONFIG-CAPTURE-FAILED", "primary put exhausted")
if s2 == s:
    sys.exit("self-test mutation 2 changed NOTHING — the fail-loud banner moved")
open(p, "w").write(s2)
PY
  [ $? -eq 0 ] || { fail "self-test: could not apply the fail-loud-stripped mutation"; st_fail=1; }
  FAILURES=0
  run_checks "${tmp}" >/dev/null 2>&1
  rc=$?
  if [ "${rc}" -ne 0 ]; then
    pass "self-test: RED when the fail-loud banner is removed"
  else
    fail "self-test: guard PASSED a silent-fall-through PUT loop"
    st_fail=1
  fi
  rm -rf "${tmp}"

  # Regression 3 — per-node capture stripped: the loud line lands in a file
  # the other region overwrites, i.e. the hw297/hw298 blindness returns.
  tmp="$(mktemp -d)"
  cp -r "${ROOT}/infra" "${ROOT}/products" "${ROOT}/scripts" "${tmp}/" 2>/dev/null
  "${PY_BIN}" - "${tmp}/${HUAWEI_TF}" "${tmp}/${SHARED_TPL}" <<'PY'
import sys
changed = 0
for p in sys.argv[1:]:
    s = open(p).read()
    s2 = s.replace("cloudinit-log?node=$(hostname)", "cloudinit-log")
    if s2 != s:
        changed += 1
    open(p, "w").write(s2)
if changed != 2:
    sys.exit("self-test mutation 3 changed %d of 2 files — a per-node uploader moved" % changed)
PY
  [ $? -eq 0 ] || { fail "self-test: could not apply the single-key-log mutation"; st_fail=1; }
  FAILURES=0
  run_checks "${tmp}" >/dev/null 2>&1
  rc=$?
  if [ "${rc}" -ne 0 ]; then
    pass "self-test: RED when the cloud-init log stops being per-node"
  else
    fail "self-test: guard PASSED a single-key cloud-init log on a multi-region prov"
    st_fail=1
  fi
  rm -rf "${tmp}"

  if [ "${st_fail}" -eq 0 ]; then
    printf '\n[self-test] PASS — cluster-free; every regression above was watched RED.\n'
    return 0
  fi
  printf '\n[self-test] FAIL\n'
  return 1
}

command -v "${TOFU_BIN}" >/dev/null 2>&1 || die "required tool not found on PATH: ${TOFU_BIN}"
command -v "${PY_BIN}"  >/dev/null 2>&1 || die "required tool not found on PATH: ${PY_BIN}"

case "${1:-}" in
  --self-test)
    self_test
    exit $?
    ;;
  --render)
    # Print the RENDERED per-region PUT-back blocks and exit. This is the
    # evidence surface: it is the same render the assertions run against, so a
    # reviewer reads the real emitted shell, not the template source.
    render_put_blocks "${ROOT}"
    exit $?
    ;;
  "")
    printf '[check-cloudinit-primary-kubeconfig-capture] rendering the REAL Huawei kubeconfig_put_block with tofu\n\n'
    run_checks "${ROOT}"
    rc=$?
    printf '\n'
    if [ "${rc}" -eq 0 ]; then
      printf 'RESULT: the primary region captures its kubeconfig, fails loudly, and every CP keeps its own log.\n'
    elif [ "${rc}" -eq 2 ]; then
      printf '%sRESULT: COULD NOT MEASURE (rc=2) — this is NOT a pass. See the FATAL line above.%s\n' "${C_RED}" "${C_RST}"
    else
      printf '%sRESULT: %d assertion(s) failed — a primary-region kubeconfig capture gap is back (Refs #6339).%s\n' "${C_RED}" "${FAILURES}" "${C_RST}"
    fi
    exit "${rc}"
    ;;
  *)
    die "unknown argument: $1 (expected no args, or --self-test)"
    ;;
esac
