#!/usr/bin/env bash
# bp-openbao — #5829 (UAT row 183): a failed ACL-policy write must GATE the tick.
#
# THE DEFECT. The two `sys/policies/acl/...` writes in the sso-configure
# reconcile loop were the only writes whose failure was swallowed —
# `if bao_write …; then log …; fi` with no else — while the `auth/oidc/role/…`
# write six lines below already did `else log WARN; continue`.
#
# So a failed policy write did not stop the tick. The role was then written
# referencing `sso-operator-read`, a policy that does not exist. OpenBao accepts
# that role happily; the token it mints carries a dangling policy name and
# therefore NO capabilities, and every request 403s — including
# `sys/internal/ui/mounts`, which is what the UI calls to populate the Secrets
# Engines panel.
#
# That is row 183's symptom precisely: the session is genuine (final URL
# /ui/vault/secrets, no token form, identity badge present) while the panel
# renders "Not authorized". Nothing in the chain errors — the login SUCCEEDS —
# so the only visible trace is a 403 on one XHR.
#
# WHY THE ASSERTION IS ON `continue` AND NOT ON A LOG LINE. A louder warning
# would not fix it: the failure mode is ORDERING. A policy must exist before a
# role names it, and letting the tick proceed writes them in the wrong order,
# leaving a window where the operator is authenticated and powerless. Only
# restarting the tick preserves policy-then-role on every pass.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

render="${tmpdir}/rendered.yaml"
"${helm}" template bp-openbao "${chart_dir}" \
  --set enabled=true \
  --set sso.enabled=true \
  >"${render}"

fail() { echo "[openbao-sso-policy-gate] FAIL: $*" >&2; exit 1; }

# ── Vacuity control, first ────────────────────────────────────────────────
# Every assertion below is "this text appears near that text". If the reconcile
# script did not render at all, a naive grep would report absence and the test
# would pass while checking nothing. Prove the subject is present before
# asserting anything about it.
grep -q 'sys/policies/acl/sso-operator-admin' "${render}" \
  || fail "the sso-configure reconcile script did not render — every assertion below would pass on an empty scan"
grep -q 'sys/policies/acl/sso-operator-read' "${render}" \
  || fail "the read-policy write is absent from the render — subject missing, assertions meaningless"

# ── The gate itself ───────────────────────────────────────────────────────
# Extract the block from each policy write to the following `fi`, and require a
# `continue` inside it. Matching the literal `else`+`continue` pairing rather
# than merely "the file contains continue" — the file contains several, and a
# whole-file grep is a check that cannot go red.
for policy in sso-operator-admin sso-operator-read; do
  block="$(awk -v pat="sys/policies/acl/${policy}" '
    index($0, pat) && index($0, "bao_write") { inblock = 1 }
    inblock { print }
    inblock && /^      fi$/ { exit }
  ' "${render}")"

  [ -n "${block}" ] \
    || fail "could not isolate the ${policy} write block — the awk extraction is broken, not the chart"

  printf '%s\n' "${block}" | grep -q 'continue' \
    || fail "the ${policy} ACL-policy write does not gate the tick.
A failed write now lets the loop proceed to auth/oidc/role/operator, whose
token_policies names a policy that does not exist. OpenBao accepts that role;
the tokens it mints carry no capabilities and 403 on everything, including
sys/internal/ui/mounts — so the OpenBao UI renders 'Not authorized' on a
session that authenticated perfectly (#5829, UAT row 183)."
done

# ── The invariant the gate exists to protect ──────────────────────────────
# policy-then-role, in that order, in the source. If the role write ever moves
# above the policy writes, every `continue` above becomes decorative.
admin_line="$(grep -n 'bao_write sys/policies/acl/sso-operator-admin' "${render}" | head -1 | cut -d: -f1)"
read_line="$(grep -n 'bao_write sys/policies/acl/sso-operator-read' "${render}" | head -1 | cut -d: -f1)"
role_line="$(grep -n 'bao_write auth/oidc/role/operator' "${render}" | head -1 | cut -d: -f1)"

[ -n "${role_line}" ] || fail "the auth/oidc/role/operator write is absent — the ordering invariant has no subject"

if [ "${admin_line}" -ge "${role_line}" ] || [ "${read_line}" -ge "${role_line}" ]; then
  fail "the role is written BEFORE a policy write (admin=${admin_line} read=${read_line} role=${role_line}).
Order matters more than the gate: a role may not name a policy that does not yet
exist, and the tick-gate above only preserves the order it is given."
fi

echo "[openbao-sso-policy-gate] PASS (both ACL-policy writes gate the tick; policy-then-role order held)"
