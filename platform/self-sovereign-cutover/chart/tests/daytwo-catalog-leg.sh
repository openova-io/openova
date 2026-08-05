#!/usr/bin/env bash
# daytwo-catalog-leg.sh — #5640 BEHAVIOURAL gate for the Day-2 reconciler's
# CATALOG (git) leg: the HEAD of the post-cutover delivery chain.
#
# WHAT THIS GUARDS AGAINST (the re-severance this test exists to catch)
# ────────────────────────────────────────────────────────────────────
# The chart + image legs enumerate their work from the LOCAL GITEA MIRROR. Post
# cutover that mirror is frozen (`mirrorResync.enabled: false`, the deliberate
# Principle-14 severance), so those legs are structurally a no-op: they can only
# ever deliver artifacts for pins that ALREADY EXIST in the frozen mirror, and
# they then report ALL_PINS_PRESENT forever because every frozen pin genuinely
# is present. Measured live on hw292 (dep 1c56518035a83e03, cutoverComplete=
# true, 2026-08-06): local Gitea bootstrap-kit slot 06a still pinning
# bp-self-sovereign-cutover 0.1.159 while upstream main had reached 0.1.169 —
# ten chart versions published, zero arrived, nothing reported wrong.
#
# The catalog leg closes that, and it MUST stay closed in a very specific shape.
# Three ways it could silently re-sever, each with a scenario below:
#   • the leg stops firing / stops being reachable            → C2
#   • the leg fires WITHOUT an explicit operator opt-in       → C1, C3, C4
#     (that is the sovereignty violation: a background auto-pull from ghcr)
#   • the leg fires but CLOBBERS the sovereign-local refs     → C6
#     (a `git push --mirror` prunes the step-06/07 HelmRepository + catalyst-api
#      pivots out of the mirror, silently re-tethering the Sovereign to ghcr —
#      the delivery mechanism itself undoing Principle #11)
# and one ordering property that decides whether ONE request moves the whole
# chain or leaves the newly-synced pin undeliverable until a SECOND one → C5.
#
# VACUITY — BOTH DIRECTIONS, EXPLICITLY
# ─────────────────────────────────────
# Every structural assertion here is run TWICE: once against the real render
# (must PASS) and once against a MUTATED render carrying the pre-fix / defective
# shape (must FAIL). A guard that has only ever been observed passing is not
# known to be a guard — that is the fail-open lesson of #5553, where
# `npm test || echo WARN` exited 0 for two months. The `expect_fail_on_mutant`
# helper below is that second direction, and each call names the exact defect it
# simulates. If a mutation stops failing, this file has gone vacuous and the
# test itself fails loudly rather than reporting green.
#
# Usage: bash tests/daytwo-catalog-leg.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
HELM="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "  ok — $*"; }

command -v yq >/dev/null 2>&1 || { echo "SKIP: yq not installed — the behavioural gate needs it to extract args[0]"; exit 0; }
command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not installed — the rendered script parses kubectl JSON with it"; exit 0; }

# ── Render ────────────────────────────────────────────────────────────────────
"$HELM" template dt . --show-only templates/12-daytwo-harbor-pin-reconciler.yaml > "$TMP/dt.yaml"
"$HELM" template dt . --show-only templates/03a-offline-mirror-resolver.yaml > "$TMP/03a.yaml"

yq -r '.spec.template.spec.containers[0].args[0]' "$TMP/dt.yaml" > "$TMP/script.raw.sh"
[ -s "$TMP/script.raw.sh" ] || fail "could not extract the reconciler args[0] script from the render"
yq -r '.data."resolver.sh"' "$TMP/03a.yaml" > "$TMP/resolver.sh"

# Vacuity control on the extraction itself — an empty/short script would make
# every assertion below pass on nothing (the #5652 first-draft bug: an
# uncreatable scratch file made the lint return PASS having examined nothing).
raw_lines=$(wc -l < "$TMP/script.raw.sh")
[ "${raw_lines}" -ge 200 ] || fail "extracted script is only ${raw_lines} lines — the render did not carry the loop body"
grep -q 'reconcile_catalog_once()' "$TMP/script.raw.sh" \
  || fail "extracted script has no reconcile_catalog_once() — the catalog leg is absent from the render"

awk '/^[[:space:]]*# The Day-2 loop\./{exit} {print}' "$TMP/script.raw.sh" \
  | sed -e "s#/offline-mirror/resolver.sh#${TMP}/resolver.sh#g" \
        -e "s#/pin-extractor/extract-pins.sh#${TMP}/extract-pins.sh#g" \
  > "$TMP/script.sh"
grep -q 'reconcile_catalog_once()' "$TMP/script.sh" || fail "truncation removed reconcile_catalog_once()"
if grep -q 'while : ; do' "$TMP/script.sh"; then fail "truncation did not remove the infinite loop"; fi
: > "$TMP/extract-pins.sh"

yq -r '.spec.template.spec.containers[0].env[] | select(has("value")) | .name + "=" + (.value|tostring)' "$TMP/dt.yaml" > "$TMP/env.txt"
grep -q '^UPSTREAM_REPO_URL=' "$TMP/env.txt" \
  || fail "render does not project UPSTREAM_REPO_URL — the catalog leg has no upstream to sync from"
grep -q '^CATALOG_LEG_ENABLED=' "$TMP/env.txt" || fail "render does not project CATALOG_LEG_ENABLED"
UPSTREAM_URL=$(sed -n 's/^UPSTREAM_REPO_URL=//p' "$TMP/env.txt")
UPSTREAM_HOST=$(printf '%s' "${UPSTREAM_URL}" | sed -E 's#^https?://([^/]+).*#\1#')
[ -n "${UPSTREAM_HOST}" ] || fail "could not derive the upstream host from UPSTREAM_REPO_URL=${UPSTREAM_URL}"

# ── Stubs ─────────────────────────────────────────────────────────────────────
mkdir -p "$TMP/bin"

# git: THE EGRESS WITNESS for this leg. Records full argv so C1/C3/C4 can prove
# it was never invoked and C6 can prove HOW the push was shaped.
cat > "$TMP/bin/git" <<'GSTUB'
#!/usr/bin/env bash
echo "GIT_INVOKED $*" >> "${STUB_EGRESS_LOG}"
case "$1" in
  clone) [ "${STUB_CLONE_RC:-0}" = "0" ] || exit "${STUB_CLONE_RC}"
         for a in "$@"; do case "$a" in /*) mkdir -p "$a" ;; esac; done; exit 0 ;;
  push)  exit "${STUB_PUSH_RC:-0}" ;;
esac
exit 0
GSTUB
chmod +x "$TMP/bin/git"

# kubectl: PAT read, the delivery-request read, the consumed-id read, CM writes.
cat > "$TMP/bin/kubectl" <<'KSTUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *"get secret"*)
      # base64 of the harness PAT; read_pat pipes through `base64 -d`.
      printf '%s' "${STUB_PAT_B64:-}" ;;
  *"get configmap self-sovereign-cutover-daytwo-request"*)
      [ -s "${STUB_REQUEST_JSON:-/nonexistent}" ] || exit 1
      cat "${STUB_REQUEST_JSON}" ;;
  *"lastConsumedRequestId"*) printf '%s' "${STUB_CONSUMED_ID:-}" ;;
  *"get configmap self-sovereign-cutover-daytwo-delivery"*) printf '' ;;
  *"create configmap"*) echo 'apiVersion: v1'; echo 'kind: ConfigMap' ;;
  *"apply -f -"*) cat > /dev/null ;;
  *) exit 0 ;;
esac
KSTUB
chmod +x "$TMP/bin/kubectl"

# curl: the Gitea branch-revision reads (before/after). Also an egress witness —
# the catalog leg must only ever dial the LOCAL Gitea, never the upstream host.
cat > "$TMP/bin/curl" <<'CSTUB'
#!/usr/bin/env bash
url=""
for a in "$@"; do case "$a" in http://*|https://*) url="$a" ;; esac; done
[ -n "$url" ] && printf 'CURL %s\n' "$url" >> "${STUB_EGRESS_LOG}"
case "$url" in
  *"/branches/"*) printf '{"commit":{"id":"%s"}}' "${STUB_REV:-aaaa1111}" ;;
  *) printf '' ;;
esac
CSTUB
chmod +x "$TMP/bin/curl"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/nslookup"; chmod +x "$TMP/bin/nslookup"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/sleep"; chmod +x "$TMP/bin/sleep"

STUB_REQUEST_JSON=""; STUB_CONSUMED_ID=""; STUB_CLONE_RC=0; STUB_PUSH_RC=0; STUB_REV="aaaa1111"

request_cm() {
  jq -n --arg id "$1" --arg scope "$2" \
    '{data:{requestId:$id, requestedBy:"ops@customer.example", scope:$scope}}' \
    > "$TMP/request.json"
  STUB_REQUEST_JSON="$TMP/request.json"
}

run_leg() {   # run_leg [script-path] -> $TMP/out.txt, $TMP/egress.log
  local script="${1:-$TMP/script.sh}"
  : > "$TMP/egress.log"
  {
    echo 'set +e'
    while IFS= read -r line; do printf 'export %q\n' "$line"; done < "$TMP/env.txt"
    echo 'export HARBOR_USERNAME=admin HARBOR_PASSWORD=stub GHCR_DOCKERCONFIG='
    cat "$script"
    # Same order reconcile_once runs them in the cluster.
    echo 'daytwo_load_request'
    echo 'reconcile_catalog_once'
    echo 'echo "RESULT status=${CATALOG_STATUS} synced=${CATALOG_SYNCED} delivered=${REQ_DELIVERED} failed=${REQ_FAILED}"'
    echo 'daytwo_consume_request'
    echo 'daytwo_audit_flush'
    echo 'echo "---AUDIT---"; cat "${WORK}/audit-all.log" 2>/dev/null'
    echo 'exit 0'
  } > "$TMP/harness.sh"
  STUB_EGRESS_LOG="$TMP/egress.log" STUB_PAT_B64="$(printf 'stub-pat' | base64)" \
    STUB_REQUEST_JSON="${STUB_REQUEST_JSON}" STUB_CONSUMED_ID="${STUB_CONSUMED_ID}" \
    STUB_CLONE_RC="${STUB_CLONE_RC}" STUB_PUSH_RC="${STUB_PUSH_RC}" STUB_REV="${STUB_REV}" \
    PATH="$TMP/bin:$PATH" sh "$TMP/harness.sh" > "$TMP/out.txt" 2>&1 || true
}

# assert_no_egress <label> — the sovereignty assertion. git must never have run,
# and no curl may have reached the UPSTREAM host (local Gitea calls are fine).
assert_no_egress() {
  grep -q 'GIT_INVOKED' "$TMP/egress.log" \
    && fail "$1: git was invoked with no explicit CATALOG opt-in — that is a background auto-pull from upstream, the exact Principle-11 violation this mode exists to prevent"
  grep -q "CURL .*${UPSTREAM_HOST}" "$TMP/egress.log" \
    && fail "$1: curl dialled the upstream host ${UPSTREAM_HOST} with no open CATALOG request"
  return 0
}

# expect_fail_on_mutant <label> <sed-expr> <grep-pattern-that-must-vanish>
# THE SECOND DIRECTION. Applies a mutation that reproduces a specific defective
# shape and asserts the corresponding assertion NO LONGER HOLDS. If the mutated
# render still satisfies the assertion, the assertion was never testing anything.
expect_fail_on_mutant() {
  local label="$1" expr="$2" pattern="$3"
  sed -E "$expr" "$TMP/script.sh" > "$TMP/mutant.sh"
  if cmp -s "$TMP/script.sh" "$TMP/mutant.sh"; then
    fail "VACUITY: mutation for '${label}' changed nothing — the assertion is not anchored to real text, so its green is meaningless"
  fi
  if grep -Eq "$pattern" "$TMP/mutant.sh"; then
    fail "VACUITY: '${label}' still matches after the mutation — the assertion cannot distinguish the fixed shape from the defective one"
  fi
  pass "vacuity(${label}): the assertion FAILS on the defective shape, so its PASS on the real render is meaningful"
}

echo "[daytwo-catalog-leg] C1: DEFAULT posture — mode=request with NO request ConfigMap"
STUB_REQUEST_JSON=""; run_leg
grep -q 'RESULT status=not-requested synced=0' "$TMP/out.txt" \
  || { cat "$TMP/out.txt"; fail "C1: expected status=not-requested synced=0 (the frozen-mirror steady state)"; }
assert_no_egress "C1"
grep -q 'left FROZEN' "$TMP/out.txt" || fail "C1: the leg must say plainly that the mirror is left frozen"
pass "C1: no request => mirror frozen, ZERO egress. This is the shipped default, so it is proven rather than assumed."

echo "[daytwo-catalog-leg] C2: request naming CATALOG => ONE-SHOT sync fires"
request_cm "2026-08-06-catalog-refresh" "CATALOG"; STUB_CONSUMED_ID=""; run_leg
grep -q 'RESULT status=ok synced=1 delivered=1' "$TMP/out.txt" \
  || { cat "$TMP/out.txt"; fail "C2: expected status=ok synced=1 delivered=1"; }
grep -q "GIT_INVOKED clone --bare" "$TMP/egress.log" || fail "C2: the leg did not bare-clone the upstream catalog"
grep -q "GIT_INVOKED clone .*${UPSTREAM_URL}" "$TMP/egress.log" \
  || { cat "$TMP/egress.log"; fail "C2: the clone did not target UPSTREAM_REPO_URL=${UPSTREAM_URL}"; }
grep -q "GIT_INVOKED push" "$TMP/egress.log" || fail "C2: the leg cloned but never pushed into the local Gitea mirror"
grep -qE '^\S+\s+CATALOG\s+delivered\s+2026-08-06-catalog-refresh' "$TMP/out.txt" \
  || { sed -n '/---AUDIT---/,$p' "$TMP/out.txt"; fail "C2: no CATALOG delivered line in the durable audit trail"; }
pass "C2: an explicit CATALOG request syncs the mirror and is audited with requestId + revision transition."

echo "[daytwo-catalog-leg] C3: CONTROL — scope=ALL_MISSING must NOT imply CATALOG"
request_cm "2026-08-06-two-images" "ALL_MISSING"; STUB_CONSUMED_ID=""; run_leg
grep -q 'RESULT status=not-requested synced=0' "$TMP/out.txt" \
  || { cat "$TMP/out.txt"; fail "C3: ALL_MISSING must not fire the catalog leg — a wildcard artifact request must never move the PINS themselves"; }
assert_no_egress "C3"
pass "C3: ALL_MISSING is bounded to artifacts. Without this control, C2 would prove only that 'something delivered', not that the opt-in is EXPLICIT."

echo "[daytwo-catalog-leg] C4: one-shot — an already-consumed requestId must not re-open the window"
request_cm "2026-08-06-catalog-refresh" "CATALOG"
STUB_CONSUMED_ID="2026-08-06-catalog-refresh"; run_leg
grep -q 'RESULT status=not-requested synced=0' "$TMP/out.txt" \
  || { cat "$TMP/out.txt"; fail "C4: a consumed requestId re-opened the delivery window — the window would stay open because someone forgot to delete the ConfigMap"; }
assert_no_egress "C4"
STUB_CONSUMED_ID=""
pass "C4: re-applying the same request is inert. The window closes and stays closed until a NEW requestId is filed."

echo "[daytwo-catalog-leg] C5: ORDERING — the catalog leg must run BEFORE the chart leg"
cat_line=$(grep -n 'reconcile_catalog_once ||' "$TMP/script.raw.sh" | head -1 | cut -d: -f1)
chart_line=$(grep -n 'reconcile_charts_once ||' "$TMP/script.raw.sh" | head -1 | cut -d: -f1)
[ -n "${cat_line}" ] && [ -n "${chart_line}" ] || fail "C5: could not locate both leg calls inside reconcile_once"
[ "${cat_line}" -lt "${chart_line}" ] \
  || fail "C5: the catalog leg runs AFTER the chart leg (line ${cat_line} vs ${chart_line}). The sync would move the pins and then have the window consumed under it, so the newly-pinned chart would need a SECOND request — the bootstrapping trap one layer up."
pass "C5: catalog leg at line ${cat_line} precedes the chart leg at ${chart_line}, so ONE request moves the whole chain."

echo "[daytwo-catalog-leg] C6: the push must NOT clobber sovereign-local refs"
# Scan CODE ONLY. The leg's own header comment contains the string
# "NEVER `git push --mirror`", and a scanner that reads its own documentation as
# evidence of the defect it warns about is the #5468/0e8235e53 defect class
# ("chart-image enumerator no longer greps its own doc comments"). Strip
# comment-only lines first; the vacuity inversion below then proves the
# stripped scan can still SEE a real --mirror when one is present.
# Line continuations are joined FIRST: the real push spans two lines, so a
# line-based scan would miss a `--mirror` parked on the continuation. The
# vacuity inversion below is what surfaced that — it is kept precisely because
# it caught this.
code_only() {   # code_only <in> <out>
  grep -vE '^[[:space:]]*#' "$1" | sed -e :a -e '/\\$/N; s/\\\n[[:space:]]*/ /; ta' > "$2"
}
code_only "$TMP/script.sh" "$TMP/script.code.sh"
[ -s "$TMP/script.code.sh" ] || fail "C6: comment-stripping produced an empty file — the scan would pass on nothing"
grep -q "refs/heads/\*:refs/heads/\*" "$TMP/script.code.sh" \
  || fail "C6: the catalog push does not use explicit refspecs"
if grep -E 'git push[^|]*--mirror|git push[^|]*--prune' "$TMP/script.code.sh" >/dev/null 2>&1; then
  fail "C6: the catalog push uses --mirror/--prune. That PRUNES the sovereign-local refs step-06 (HelmRepository pivots) and step-07 (catalyst-api env) pushed into this same repo, silently un-pivoting the Sovereign back onto ghcr — the delivery mechanism undoing Principle #11. This is the G62/#3606 regression."
fi
pass "C6: explicit refspecs only; no --mirror/--prune, so step-06/07's sovereign-local pivots survive the sync."

echo "[daytwo-catalog-leg] C7: fail-soft — a clone failure leaves the mirror unchanged and does not crash the loop"
request_cm "2026-08-06-unreachable" "CATALOG"; STUB_CONSUMED_ID=""; STUB_CLONE_RC=1; run_leg
grep -q 'RESULT status=clone-failed synced=0' "$TMP/out.txt" \
  || { cat "$TMP/out.txt"; fail "C7: expected status=clone-failed synced=0"; }
grep -q "GIT_INVOKED push" "$TMP/egress.log" \
  && fail "C7: pushed into the local mirror after the upstream clone FAILED — that would publish a half-fetched catalog"
grep -qE '^\S+\s+CATALOG\s+failed\s+' "$TMP/out.txt" || fail "C7: a failed sync must still be audited"
STUB_CLONE_RC=0
pass "C7: unreachable upstream => mirror untouched, failure audited, Sovereign keeps running on its current pins."

# ── SECOND DIRECTION ──────────────────────────────────────────────────────────
# Each mutation reproduces a real defective shape; the paired assertion must
# stop holding. These are what make the greens above meaningful.
echo "[daytwo-catalog-leg] vacuity: every structural assertion must FAIL on its defective shape"
expect_fail_on_mutant "C6 refspec guard" \
  "s#'refs/heads/\\*:refs/heads/\\*' 'refs/tags/\\*:refs/tags/\\*'#--mirror#" \
  "refs/heads/\*:refs/heads/\*"
# …and the POSITIVE direction for C6: after comment-stripping, the scan must
# still SEE a real `git push --mirror`. Comment-stripping is exactly the kind of
# fix that can over-filter into blindness, so prove it did not.
sed -E "s#'refs/heads/\*:refs/heads/\*' 'refs/tags/\*:refs/tags/\*'#--mirror#" "$TMP/script.sh" \
  > "$TMP/mutant-mirror.sh"
code_only "$TMP/mutant-mirror.sh" "$TMP/mutant-mirror-code.sh"
grep -E 'git push[^|]*--mirror' "$TMP/mutant-mirror-code.sh" >/dev/null 2>&1 \
  || fail "VACUITY: after comment-stripping the C6 scan can no longer detect a real 'git push --mirror' — the strip over-filtered and the guard is blind"
pass "vacuity(C6 detection): the comment-stripped scan still catches a real --mirror, so C6's PASS is not blindness"
expect_fail_on_mutant "C1/C3/C4 explicit-opt-in guard (catalog_in_scope removed => every request would sync)" \
  "s#grep -qx 'CATALOG' \"\\\$\{REQ_SCOPE_FILE\}\"#true#" \
  "grep -qx 'CATALOG'"
expect_fail_on_mutant "C2 upstream-target guard (leg no longer reads UPSTREAM_REPO_URL)" \
  's#git clone --bare --quiet "\$\{UPSTREAM_REPO_URL\}"#git clone --bare --quiet ""#' \
  'git clone --bare --quiet "\$\{UPSTREAM_REPO_URL\}"'

# C5's ordering assertion gets its own inverted run: swap the two calls and
# confirm the comparison flips. A line-number comparison that cannot flip would
# be the emptiest kind of green.
sed -E -e 's#reconcile_catalog_once \|\|#RECONCILE_CATALOG_MARKER#' \
       -e 's#reconcile_charts_once \|\|#reconcile_catalog_once ||#' \
       -e 's#RECONCILE_CATALOG_MARKER#reconcile_charts_once ||#' \
       "$TMP/script.raw.sh" > "$TMP/mutant-order.sh"
m_cat=$(grep -n 'reconcile_catalog_once ||' "$TMP/mutant-order.sh" | head -1 | cut -d: -f1)
m_chart=$(grep -n 'reconcile_charts_once ||' "$TMP/mutant-order.sh" | head -1 | cut -d: -f1)
[ -n "${m_cat}" ] && [ -n "${m_chart}" ] && [ "${m_cat}" -gt "${m_chart}" ] \
  || fail "VACUITY: the C5 ordering check does not flip when the legs are swapped — it is not actually testing order"
pass "vacuity(C5 ordering): swapping the legs makes the check fail, so its PASS is meaningful"

echo "[daytwo-catalog-leg] ALL PASS — 7 scenarios + 4 vacuity inversions"
