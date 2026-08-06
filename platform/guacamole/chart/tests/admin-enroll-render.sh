#!/usr/bin/env bash
# admin-enroll-render — the SSO permission-enrollment path must ship ARMED.
#
# WHY THIS FILE EXISTS (#5598). Guacamole's OIDC login creates a guacamole_user
# row (POSTGRESQL_AUTO_CREATE_ACCOUNTS=true) but does NOT bind it to any
# permission set. The ADMINISTER / CREATE_* grants live on the <adminGroup>
# USER_GROUP, and the binding row is written only by enroll_admins() — which
# runs from exactly two places: the one-shot seed Job (seed.sh) and a CronJob
# (promote.sh). Delete either leg and an SSO-authenticated owner carries ZERO
# Guacamole permissions, so /api/session/data/postgresql/self/effectivePermission
# returns 403 PERMISSION_DENIED and Guacamole's own SPA renders the generic
# "An error has occurred" — indistinguishable from an outage. That is #5598.
#
# Measured before this file: platform/guacamole/chart/tests/ carried four
# scripts (render, crossregion-render, httproute-render, g117-w3d1-sso-secret)
# and NONE referenced enrollment. Deleting the CronJob template entirely left
# the whole guacamole chart suite green.
#
# The chart is default-OFF (tests/render.sh assertion 1), so the render below
# must enable it — but it sets ONLY the identity/enable inputs the chart cannot
# infer. Nothing under jdbc/adminGroup/promoteSchedule is overridden, because a
# --set on the enrollment values is exactly what would defeat this file.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
fails=0
pass() { printf '  ok   — %s\n' "$1"; }
fail() { printf '  FAIL — %s\n' "$1"; fails=$((fails + 1)); }

cd "${CHART_DIR}"
# Same dependency preamble as tests/render.sh. NOTE: that pattern exits 0 when
# `helm dependency update` fails, which is a skip that reads as a pass. Kept for
# consistency with the sibling tests, but the skip is announced loudly so a
# green CI line that actually asserted nothing is visible in the log.
if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "[admin-enroll] SKIPPED — helm dependency update failed (no network);"
    echo "[admin-enroll] NOTHING WAS ASSERTED. Re-run after \`helm dep build\`."
    exit 0
  }
fi

echo "[admin-enroll] rendering bp-guacamole (enable-only --set; enrollment values untouched)"
# --api-versions is REQUIRED, not decoration: the seed Job + enroll CronJob are
# gated on `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"`, which a bare
# `helm template` does not carry. Without it the render is silently empty and
# every assertion below "fails" for a reason that has nothing to do with the
# chart — which is precisely what the vacuity check at the end exists to expose.
helm template bp-guacamole . \
  --api-versions postgresql.cnpg.io/v1 \
  --set guacamole.enabled=true \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/c \
  >"${TMP}/all.yaml" 2>"${TMP}/err" || { echo "helm template failed:"; cat "${TMP}/err"; exit 1; }

# 1. The CronJob must render. Parsed as YAML docs, not grepped, so a comment
#    mentioning "admin-enroll" cannot satisfy it.
cron_names="$(python3 - "${TMP}/all.yaml" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
print("\n".join(
    d.get("metadata", {}).get("name", "")
    for d in docs if d.get("kind") == "CronJob"))
PY
)"
if printf '%s' "${cron_names}" | grep -q -- '-admin-enroll$'; then
  pass "the -admin-enroll CronJob renders by default"
else
  fail "no -admin-enroll CronJob in the default render — SSO users never gain permissions (#5598); CronJobs found: [${cron_names//$'\n'/, }]"
fi

# 2. Its schedule must fire at least HOURLY.
#
#    Deliberately NOT "schedule is non-empty": the template reads
#    `$db.promoteSchedule | default "* * * * *"`, and Helm's `default` treats ""
#    as falsy, so an empty values entry falls back to the every-minute default.
#    A non-empty assertion is therefore UNFALSIFIABLE — I wrote it that way
#    first, emptied values.yaml, and the guard stayed green. It could not have
#    gone red.
#
#    What CAN regress is the schedule's FREQUENCY. `promoteSchedule` is an
#    operator-facing value; setting it to "0 3 * * *" renders a perfectly valid
#    CronJob and leaves every first-login principal unenrolled — carrying the
#    exact 403 in #5598 — for up to 24 hours. The enrollment window IS the
#    defect, so the guard has to assert on the window.
sched="$(python3 - "${TMP}/all.yaml" <<'PY'
import sys, yaml
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    if d.get("kind") == "CronJob" and d.get("metadata", {}).get("name", "").endswith("-admin-enroll"):
        print((d.get("spec", {}).get("schedule") or "").strip())
        break
PY
)"
sched_minute="${sched%% *}"
case "${sched_minute}" in
  '*'|'*/'[0-9]*)
    pass "enrollment fires at least hourly (schedule: ${sched:-<empty>})" ;;
  *)
    fail "enrollment schedule \"${sched:-<empty>}\" does not fire every hour (minute field \"${sched_minute}\") — a first-login principal carries the #5598 403 until the next tick" ;;
esac

# 3. BOTH legs must call enroll_admins. The CronJob alone leaves a fresh
#    Sovereign unenrolled until the first tick; the seed Job alone leaves every
#    LATER first-login principal unenrolled forever.
scripts="$(python3 - "${TMP}/all.yaml" <<'PY'
import sys, yaml
out = []
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    if d.get("kind") == "ConfigMap":
        for k, v in (d.get("data") or {}).items():
            out.append((k, v or ""))
print(repr(out))
PY
)"
for leg in seed.sh promote.sh; do
  if printf '%s' "${scripts}" | grep -q "'${leg}'" && \
     python3 - "${TMP}/all.yaml" "${leg}" <<'PY'
import sys, yaml
leg = sys.argv[2]
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    if d.get("kind") == "ConfigMap":
        body = (d.get("data") or {}).get(leg)
        if body and "enroll_admins" in body:
            raise SystemExit(0)
raise SystemExit(1)
PY
  then
    pass "${leg} calls enroll_admins"
  else
    fail "${leg} does not call enroll_admins — that enrollment leg is dead (#5598)"
  fi
done

# 4. VACUITY. Assertions 2 and 3 read from parsed structures that return empty
#    on no match. Prove the render really contains the enrollment SQL, so a
#    silently-empty render cannot pass the checks above.
if python3 - "${TMP}/all.yaml" <<'PY'
import sys, yaml
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    if d.get("kind") == "ConfigMap":
        for v in (d.get("data") or {}).values():
            if v and "guacamole_user_group_member" in v:
                raise SystemExit(0)
raise SystemExit(1)
PY
then
  pass "vacuity: the rendered manifest carries the guacamole_user_group_member INSERT"
else
  fail "vacuity: no enrollment SQL anywhere in the render — the checks above proved nothing"
fi

if [ "${fails}" -ne 0 ]; then
  echo "[admin-enroll] ${fails} failure(s) — SSO permission enrollment is not armed"
  exit 1
fi
echo "[admin-enroll] all assertions passed — enrollment armed on both legs, schedule live"
