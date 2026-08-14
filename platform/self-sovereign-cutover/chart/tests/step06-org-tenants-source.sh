#!/usr/bin/env bash
# #6293 — step-06 Phase-2b: the per-Org tenant HelmRepository DURABLE-SOURCE pivot.
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────
# The six shared bp-* HelmRepositories the per-Org overlays sourceRef are owned
# by the `org-tenants` Flux Kustomization: path ./clusters/<fqdn>/org-tenants,
# interval 1m, prune=true, sourceRef GitRepository/openova-org-tenants whose
# spec.ref.branch is `org-tenants`. Step-06 Phase-1 patches those objects LIVE,
# and Flux reverts every patch on its next reconcile.
#
# #4885 knew that and added a Gitea rewrite for them — inside the working tree
# of a clone opened `--branch main`. The org-tenants tree does not exist on
# main (TBD-C18e split it onto its own branch precisely so the step-01 mirror's
# force-push of main could not clobber per-Org overlays), so the rewrite matched
# nothing, printed "sed edited 0 org-tenant HelmRepository file(s)", and read as
# a clean no-op for every Sovereign that ever ran it. Behind it sat a second
# defect: the commit stages `git add "${target_dir}"` — clusters/_template/
# bootstrap-kit only — and pushes `origin main`, so a matched tenant edit would
# have been counted into ${edited}, logged as edited, then dropped unstaged onto
# the "git diff empty after sed" path, which also reads as success.
#
# Measured on hw296 (dep 2026-08-14, cutover terminal-failed at step 6):
# bp-keycloak and bp-newapi at metadata.generation 101, all six HelmRepositories
# back on oci://ghcr.io/openova-io with certSecretRef absent, and the Job log
# containing no `org-tenant`, no `tenant-HR`, no `sed edited`, no commit/push
# line at all.
#
# WHY THIS TEST USES REAL GIT AND NOT A STUB
# ──────────────────────────────────────────
# A stubbed `git` cannot be on the wrong branch — the defect IS the branch. So
# every case below runs the REAL rendered phase2b.sh bytes against REAL local
# git repositories with a real `main` and a real `org-tenants` branch, and reads
# its verdict out of the bare origin (`git show org-tenants:<path>`), never out
# of a working tree the script itself left behind. The only stub is `kubectl`,
# used solely by the trailing `annotate … || true` reconcile poke, which no
# assertion depends on.
#
# EVERY ASSERTION IS VACUITY-PROVED INDIVIDUALLY. Section M re-runs the suite
# against single-behaviour mutants of the real script — one mutation each — and
# requires the matching case to go RED. A case with no mutant partner is a
# CONTROL, and Section M includes mutants that turn the controls red too, so the
# suite cannot be satisfied by deleting, disabling or blanket-widening Phase-2b.
#
# Usage: bash tests/step06-org-tenants-source.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

FQDN="hw296.omani.works"
UPSTREAM="oci://ghcr.io/openova-io"
LOCAL="oci://registry.${FQDN}/openova-io"
TRUST="cutover-harbor-ca"
OT_PATH="clusters/${FQDN}/org-tenants/helmrepositories.yaml"
BK_PATH="clusters/_template/bootstrap-kit/19-harbor.yaml"

pass=0; fail=0
ok()  { echo "  ok   — $1"; pass=$((pass+1)); }
bad() { echo "  FAIL — $1"; fail=$((fail+1)); }

mkdir -p "${TMP}/bin"

# ── Render ─────────────────────────────────────────────────────────────────
helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" \
  >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "FAIL — helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# ── Extract the bytes under test straight from the render ──────────────────
# Plain awk over the render text, deliberately NO PyYAML: the same dependency
# surface as the Job (sh + awk + git), so this suite cannot skip itself on a
# runner missing a python module.
awk '/# ---- Phase 2b \(#6293\)/,/# ---- end Phase 2b \(#6293\) ----/' "${TMP}/render.yaml" \
  | sed 's/^    //' >"${TMP}/phase2b.orig.sh"
awk '/# ---- inject_trust_ref \(#3379 #6293\) ----/,/# ---- end inject_trust_ref ----/' "${TMP}/render.yaml" \
  | sed 's/^            //' >"${TMP}/injector.sh"

if grep -q 'ORG_TENANTS_BRANCH' "${TMP}/phase2b.orig.sh"; then
  ok "extracted phase2b.sh from the render"
else
  echo "FAIL — phase2b.sh did not extract (marker comments changed?)"; exit 1
fi
# Truncation guard: a short extraction would still `sh` cleanly and exit 0 —
# the fail-open shape this suite exists to eliminate.
if ! tail -3 "${TMP}/phase2b.orig.sh" | grep -q 'end Phase 2b'; then
  echo "FAIL — phase2b.sh extraction was truncated"; exit 1
fi
if grep -q 'inject_trust_ref()' "${TMP}/injector.sh"; then
  ok "extracted the shared inject_trust_ref function from the render"
else
  echo "FAIL — inject_trust_ref did not extract"; exit 1
fi
sh -n "${TMP}/phase2b.orig.sh" || { echo "FAIL — phase2b.sh is not valid POSIX sh"; exit 1; }

# Relocate the container's literal /tmp paths into the test tmpdir. Assert the
# substitution actually applied, so a path rename in the template cannot
# silently turn every case below into a no-op.
tmp_hits=$(grep -c '/tmp' "${TMP}/phase2b.orig.sh" || true)
if [ "${tmp_hits}" -lt 3 ]; then
  echo "FAIL — phase2b.sh no longer uses /tmp paths; harness relocation is stale"; exit 1
fi
relocate() { sed "s,/tmp,${TMP}/t,g" "$1" >"$2"; }
relocate "${TMP}/phase2b.orig.sh" "${TMP}/phase2b.sh"

# ── Stub kubectl (reconcile poke only; no assertion depends on it) ─────────
cat >"${TMP}/bin/kubectl" <<'FAKE'
#!/usr/bin/env sh
echo "kubectl $*" >>"${FAKE_KUBECTL_LOG}"
exit 0
FAKE
chmod +x "${TMP}/bin/kubectl"

# ── Fixtures: a real bare origin with a real main and a real org-tenants ───
hr_doc() {
  # $1 = name, $2 = url
  printf -- '---\napiVersion: source.toolkit.fluxcd.io/v1beta2\nkind: HelmRepository\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  type: oci\n  interval: 15m\n  url: %s\n  secretRef:\n    name: ghcr-pull\n' "$1" "$2"
}

build_origin() {
  # $1 = "with-branch" | "no-branch"
  rm -rf "${TMP}/origin.git" "${TMP}/seed"
  git init --bare -q "${TMP}/origin.git"
  git init -q -b main "${TMP}/seed"
  (
    cd "${TMP}/seed"
    git config user.name t; git config user.email t@example.invalid
    mkdir -p "$(dirname "${BK_PATH}")"
    hr_doc bp-harbor "${UPSTREAM}" >"${BK_PATH}"
    git add -A; git commit -qm seed
    git remote add origin "${TMP}/origin.git"
    git push -q origin main
    if [ "$1" = "with-branch" ]; then
      git checkout -q -b org-tenants
      mkdir -p "$(dirname "${OT_PATH}")" "clusters/${FQDN}/org-tenants/org-abc"
      {
        # Multi-document, exactly the shape organization_gitops.go emits.
        hr_doc bp-keycloak         "${UPSTREAM}"
        hr_doc bp-newapi           "${UPSTREAM}"
        hr_doc bp-wordpress-tenant "${UPSTREAM}"
        hr_doc bp-openclaw         "${UPSTREAM}"
        hr_doc bp-stalwart-tenant  "${UPSTREAM}"
        hr_doc bp-agenity          "${UPSTREAM}"
      } >"${OT_PATH}"
      printf 'resources:\n  - org-abc\n' >"clusters/${FQDN}/org-tenants/kustomization.yaml"
      # A per-Org overlay HelmRelease: carries a sourceRef but NO url. Nothing
      # here may be rewritten; it is why the collector greps `url:` lines.
      printf 'apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: bp-keycloak\nspec:\n  chart:\n    spec:\n      sourceRef:\n        kind: HelmRepository\n        name: bp-keycloak\n' \
        >"clusters/${FQDN}/org-tenants/org-abc/bp-keycloak.yaml"
      git add -A; git commit -qm tenants
      git push -q origin org-tenants
    fi
  ) >/dev/null 2>&1
}

# Read a path out of the BARE origin — never out of a working tree the script
# left behind. Pushed-or-it-did-not-happen.
origin_show() { git --git-dir="${TMP}/origin.git" show "$1:$2" 2>/dev/null; }
origin_has_branch() { git --git-dir="${TMP}/origin.git" rev-parse --verify -q "$1" >/dev/null 2>&1; }

# ── Runner: execute the real phase2b.sh with the parent-shell contract ────
# push_url / redacted / UPSTREAM_PREFIX / local_prefix / trust_secret and
# inject_trust_ref() are supplied by the step args in production; here they are
# supplied identically, with the injector sourced from the render.
run_phase2b() {
  # $1 = script path   $2 = push_url   $3 = ORG_TENANTS_PIVOT_ENABLED
  : >"${TMP}/kubectl.log"
  mkdir -p "${TMP}/t/repo"
  cat >"${TMP}/driver.sh" <<DRIVER
push_url='$2'
redacted='gitea.example.invalid/openova/openova.git'
UPSTREAM_PREFIX='${UPSTREAM}'
local_prefix='${LOCAL}'
trust_secret='${TRUST}'
. '${TMP}/injector.sh'
. '$1'
DRIVER
  env -i \
    PATH="${TMP}/bin:${PATH}" \
    HOME="${TMP}" \
    FAKE_KUBECTL_LOG="${TMP}/kubectl.log" \
    GIT_AUTHOR_NAME=cutover GIT_AUTHOR_EMAIL=cutover@example.invalid \
    GIT_COMMITTER_NAME=cutover GIT_COMMITTER_EMAIL=cutover@example.invalid \
    GIT_TERMINAL_PROMPT=0 \
    ORG_TENANTS_PIVOT_ENABLED="$3" \
    ORG_TENANTS_BRANCH=org-tenants \
    ORG_TENANTS_GITREPO=openova-org-tenants \
    ORG_TENANTS_GITREPO_NAMESPACE=flux-system \
    sh "${TMP}/driver.sh" >"${TMP}/out.log" 2>&1
  echo $?
}

count_upstream() { origin_show "$1" "$2" | grep -c "url: ${UPSTREAM}$" || true; }
count_local()    { origin_show "$1" "$2" | grep -c "url: ${LOCAL}$" || true; }
count_trust()    { origin_show "$1" "$2" | grep -c "name: ${TRUST}$" || true; }

# ═══════════════════════════════════════════════════════════════════════════
echo "== C. behaviour against real git repositories =="

# ── C1/C2/C3/C7: the happy path, read back out of the bare origin ─────────
build_origin with-branch
rc=$(run_phase2b "${TMP}/phase2b.sh" "${TMP}/origin.git" true)

if [ "${rc}" -eq 0 ]; then ok "C0 happy path exits 0"
else bad "C0 happy path exit ${rc}"; sed 's/^/      /' "${TMP}/out.log" | tail -20; fi

c1_up=$(count_upstream org-tenants "${OT_PATH}")
c1_lo=$(count_local org-tenants "${OT_PATH}")
if [ "${c1_up}" -eq 0 ] && [ "${c1_lo}" -eq 6 ]; then
  ok "C1 all 6 org-tenant HelmRepositories pivoted to local Harbor ON THE org-tenants BRANCH"
else
  bad "C1 org-tenants branch has ${c1_up} still on ${UPSTREAM} and ${c1_lo} on local (want 0 / 6) — the per-Org durable source was not rewritten (#6293)"
fi

if [ "$(count_trust org-tenants "${OT_PATH}")" -eq 6 ]; then
  ok "C2 certSecretRef=${TRUST} injected beside every pivoted url"
else
  bad "C2 certSecretRef present on $(count_trust org-tenants "${OT_PATH}") of 6 — pivoted tenant HRs would x509-fail the in-cluster Harbor"
fi

if [ "$(count_upstream org-tenants "${BK_PATH}")" -eq 1 ]; then
  ok "C3 CONTROL: the bootstrap-kit file on this branch is untouched (scoping holds)"
else
  bad "C3 Phase-2b rewrote a non-org-tenants file — the /org-tenants/ scope was widened"
fi

# The overlay legitimately contains the string `kind: HelmRepository` inside its
# sourceRef, so this compares the pushed bytes to the seeded bytes rather than
# grepping for a token that is expected to be present.
if origin_show org-tenants "clusters/${FQDN}/org-tenants/org-abc/bp-keycloak.yaml" \
   | cmp -s - "${TMP}/seed/clusters/${FQDN}/org-tenants/org-abc/bp-keycloak.yaml"; then
  ok "C4 CONTROL: the per-Org HelmRelease overlay (sourceRef, no url) is byte-identical"
else
  bad "C4 CONTROL: the per-Org HelmRelease overlay was modified — Phase-2b touched a file with no url: line"
fi

if [ "$(count_upstream main "${BK_PATH}")" -eq 1 ]; then
  ok "C5 CONTROL: Phase-2b did not push anything to main"
else
  bad "C5 Phase-2b modified main — it must only ever write the org-tenants branch"
fi

if grep -q "gitrepository openova-org-tenants" "${TMP}/kubectl.log"; then
  ok "C6 the openova-org-tenants GitRepository is re-poked after the push"
else
  bad "C6 no reconcile poke of openova-org-tenants — the pivot waits out the polling interval"
fi

# ── C8: idempotent re-run ─────────────────────────────────────────────────
rc=$(run_phase2b "${TMP}/phase2b.sh" "${TMP}/origin.git" true)
if [ "${rc}" -eq 0 ] && [ "$(count_trust org-tenants "${OT_PATH}")" -eq 6 ] \
   && [ "$(count_local org-tenants "${OT_PATH}")" -eq 6 ]; then
  ok "C8 re-run is idempotent (exit 0, still 6 local urls, still 6 certSecretRefs — no duplicates)"
else
  bad "C8 re-run was not idempotent: exit ${rc}, $(count_local org-tenants "${OT_PATH}") local urls, $(count_trust org-tenants "${OT_PATH}") certSecretRefs"
fi

# ── C9: absent branch is a SKIP, not a failure ────────────────────────────
build_origin no-branch
rc=$(run_phase2b "${TMP}/phase2b.sh" "${TMP}/origin.git" true)
if [ "${rc}" -eq 0 ] && grep -q 'Phase-2b SKIP' "${TMP}/out.log" && ! origin_has_branch org-tenants; then
  ok "C9 CONTROL: a Sovereign with no org-tenants branch skips cleanly (exit 0, nothing created)"
else
  bad "C9 absent-branch handling wrong: exit ${rc}"; sed 's/^/      /' "${TMP}/out.log" | tail -10
fi

# ── C10: an UNREADABLE remote must FAIL, never present as the same SKIP ───
build_origin with-branch
rc=$(run_phase2b "${TMP}/phase2b.sh" "${TMP}/origin.git/does-not-exist" true)
if [ "${rc}" -ne 0 ] && grep -q 'FATAL' "${TMP}/out.log"; then
  ok "C10 an unreadable remote FATALs (an auth failure can never masquerade as an absent branch)"
else
  bad "C10 unreadable remote exited ${rc} without FATAL — a verdict from absent evidence"
fi

# ── C11: the disable switch ───────────────────────────────────────────────
build_origin with-branch
rc=$(run_phase2b "${TMP}/phase2b.sh" "${TMP}/origin.git" false)
if [ "${rc}" -eq 0 ] && [ "$(count_upstream org-tenants "${OT_PATH}")" -eq 6 ]; then
  ok "C11 CONTROL: enabled=false is a genuine no-op"
else
  bad "C11 enabled=false did not no-op: exit ${rc}, $(count_upstream org-tenants "${OT_PATH}") still upstream"
fi

# ═══════════════════════════════════════════════════════════════════════════
# M. VACUITY PROOFS — one mutated behaviour each; the named case must go RED.
# ═══════════════════════════════════════════════════════════════════════════
echo "== M. vacuity proofs (single-behaviour mutants must break the named case) =="

mutant() {
  # $1 = label   $2 = sed expression applied to the ORIGINAL extracted script
  sed "$2" "${TMP}/phase2b.orig.sh" >"${TMP}/mut.orig.sh"
  if cmp -s "${TMP}/mut.orig.sh" "${TMP}/phase2b.orig.sh"; then
    bad "M:$1 mutation matched nothing — the vacuity proof itself is vacuous"
    return 1
  fi
  relocate "${TMP}/mut.orig.sh" "${TMP}/mut.sh"
  return 0
}

# M1 — the shipped defect: clone main instead of the org-tenants branch.
if mutant "wrong-branch" 's,--branch "\${ot_branch}",--branch main,'; then
  build_origin with-branch
  run_phase2b "${TMP}/mut.sh" "${TMP}/origin.git" true >/dev/null
  if [ "$(count_upstream org-tenants "${OT_PATH}")" -eq 6 ]; then
    ok "M1 cloning main instead of org-tenants leaves all 6 upstream → C1 goes RED (this is the shipped defect)"
  else
    bad "M1 C1 still passed with the branch mutated to main — C1 is vacuous"
  fi
fi

# M2 — drop the trust injection.
if mutant "no-trust" 's,^\( *\)inject_trust_ref "\${otf}",\1:,'; then
  build_origin with-branch
  run_phase2b "${TMP}/mut.sh" "${TMP}/origin.git" true >/dev/null
  if [ "$(count_trust org-tenants "${OT_PATH}")" -eq 0 ]; then
    ok "M2 removing the injector call drops every certSecretRef → C2 goes RED"
  else
    bad "M2 C2 still passed without the injector call — C2 is vacuous"
  fi
fi

# M3 — widen the collector past /org-tenants/ (breaks the C3 control).
if mutant "wide-scope" "s,| grep '/org-tenants/' >> \"\${ot_list}\",>> \"\${ot_list}\","; then
  build_origin with-branch
  run_phase2b "${TMP}/mut.sh" "${TMP}/origin.git" true >/dev/null
  if [ "$(count_upstream org-tenants "${BK_PATH}")" -eq 0 ]; then
    ok "M3 widening the collector rewrites the bootstrap-kit file → C3 goes RED"
  else
    bad "M3 C3 still passed with the scope widened — C3 is vacuous"
  fi
fi

# M4 — the second shipped defect: stage only the bootstrap-kit directory.
if mutant "narrow-add" 's,git add -A clusters$,git add -A clusters/_template,'; then
  build_origin with-branch
  rc=$(run_phase2b "${TMP}/mut.sh" "${TMP}/origin.git" true)
  if [ "${rc}" -ne 0 ] && [ "$(count_upstream org-tenants "${OT_PATH}")" -eq 6 ]; then
    ok "M4 staging only bootstrap-kit leaves the index empty → the FATAL fires and C1 goes RED"
  else
    bad "M4 a narrowed \`git add\` exited ${rc} and still pivoted — the empty-index guard is vacuous"
  fi
fi

# M5 — swallow the ls-remote verdict (breaks the C10 discrimination).
if mutant "swallow-lsremote" '/ls-remote .* against .* failed/,+2 s,^\( *\)exit 1,\1:,'; then
  build_origin with-branch
  rc=$(run_phase2b "${TMP}/mut.sh" "${TMP}/origin.git/does-not-exist" true)
  if [ "${rc}" -eq 0 ]; then
    ok "M5 swallowing the ls-remote verdict turns an unreadable remote into a silent success → C10 goes RED"
  else
    bad "M5 C10 still passed with the ls-remote guard swallowed — C10 is vacuous"
  fi
fi

# ═══════════════════════════════════════════════════════════════════════════
echo "== R. render wiring =="

# The step args, as the container receives them.
python3 - "${TMP}/render.yaml" "${TMP}/args.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "ConfigMap":
        continue
    if d["metadata"]["name"] != "cutover-step-06-helmrepository-patches":
        continue
    spec = yaml.safe_load(d["data"]["podSpec"])
    c = [x for x in spec["containers"] if x["name"] == "helmrepository-patches"][0]
    open(sys.argv[2], "w").write(c["args"][0])
    sys.exit(0)
sys.exit("step-06 ConfigMap not found")
PY

# R1 — Phase-2b must run AFTER the main-branch push, so its commit can never be
# entangled with the ${target_dir}-scoped `git add` (the M4 shape).
push_ln=$(awk 'index($0,"git push origin main"){print NR; exit}' "${TMP}/args.sh")
p2b_ln=$(awk 'index($0,". /phase2b/phase2b.sh"){print NR; exit}' "${TMP}/args.sh")
if [ -n "${push_ln}" ] && [ -n "${p2b_ln}" ] && [ "${p2b_ln}" -gt "${push_ln}" ]; then
  ok "R1 phase2b.sh is sourced (line ${p2b_ln}) AFTER the main-branch push (line ${push_ln})"
else
  bad "R1 phase2b source at ${p2b_ln:-<none>} does not follow the main push at ${push_ln:-<none>}"
fi

# R2 — the ConfigMap key, the volume and the mount all have to exist together.
r2=0
grep -q '^  phase2b.sh: |' "${TMP}/render.yaml" || r2=1
grep -q 'mountPath: /phase2b' "${TMP}/render.yaml" || r2=1
grep -q 'key: phase2b.sh' "${TMP}/render.yaml" || r2=1
if [ "${r2}" -eq 0 ]; then
  ok "R2 phase2b.sh key + /phase2b mount + volume item are all wired"
else
  bad "R2 phase2b.sh is not fully wired — the source would fail at runtime"
fi

# R3 — #5436 is NOT weakened. Phase-1.5 is a diagnostic; the Phase-3 pre-strip
# call is still the fail-closed gate. Both directions asserted: a future edit
# that deletes the gate, or restores the premature one, fails here.
if grep -q 'assert_region_a_pivot "Phase-1.5 read-back" 0 "DIAGNOSTIC"' "${TMP}/args.sh"; then
  ok "R3a Phase-1.5 read-back is a DIAGNOSTIC (it runs before Phase-2/2b writes the durable source)"
else
  bad "R3a Phase-1.5 read-back is not the diagnostic form — a premature gate no retry can converge (#6293)"
fi
if grep -qE 'assert_region_a_pivot "Phase-1.5 read-back"[^|]*\|\|[[:space:]]*exit 1' "${TMP}/args.sh"; then
  bad "R3b Phase-1.5 still fails closed — this is the hw296 terminal failure"
else
  ok "R3b Phase-1.5 does not fail closed"
fi
if grep -qE 'assert_region_a_pivot "Phase-3 pre-strip read-back"[^|]*\|\|[[:space:]]*exit 1' "${TMP}/args.sh"; then
  ok "R3c the Phase-3 pre-strip read-back IS still fail-closed (#5436 intact)"
else
  bad "R3c the Phase-3 pre-strip gate lost its \`|| exit 1\` — #5436 would be weakened, not relocated"
fi

# R4 — phase2b must CALL the shared injector, never carry a second copy of it.
if grep -q 'inject_trust_ref' "${TMP}/phase2b.orig.sh" \
   && ! grep -q 'awk -v ref=' "${TMP}/phase2b.orig.sh"; then
  ok "R4 phase2b calls the shared injector instead of duplicating the awk (#5646)"
else
  bad "R4 phase2b carries its own copy of the trust injector — the two will drift"
fi

echo
echo "RESULT: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
