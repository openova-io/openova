#!/usr/bin/env bash
# bp-self-sovereign-cutover — step-06 SECONDARY-region DURABLE pivot guard (#5596).
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────
# #5359 gave the secondary leg a read-back ("ZERO upstream-prefixed HRs may
# remain"). It was SINGLE-SHOT and fired within seconds of the patch loop, so
# it measured the PATCH and never the PIVOT. region-B runs its OWN
# kustomize-controller against its OWN artifact of the gitops repo; when that
# artifact is the pre-cutover one, the next re-apply puts every URL back on
# oci://ghcr.io/openova-io — minutes after the step recorded success.
#
# Live on hw292 (dep 1c56518035a83e03, 2-region me-east-215-a/-b-1), all
# read-only from the cluster on 2026-08-06:
#   07:31:11Z  region-b GitRepository artifact frozen at main@sha1:4cc85ad9…
#              (Ready=False GitOperationFailed, "unable to list remote … EOF")
#   07:41:13Z  step.helmrepository-patches.region.me-east-215-b-1.result=success
#   07:42:08Z  region-b kustomize-controller Apply (sole field manager,
#              generation 3) — all 64 HelmRepositories back on ghcr.io
#   08:12:30Z  cutoverComplete=true
#   +3d        region-b: 64/65 HelmRepositories still on oci://ghcr.io/openova-io
# 55 seconds is the entire width of the window the old assert sampled in.
#
# WHY THIS TEST EXECUTES THE SCRIPT INSTEAD OF GREPPING THE RENDER
# ───────────────────────────────────────────────────────────────
# The rendered log text was ALREADY correct before the fix — the leg printed
# "all HelmRepositories on <local>" and recorded result=success. Log text is
# exactly what made this ship. So the assertions are on the SCRIPT'S EXIT CODE,
# driven by a fake kubectl that reproduces the live shape: the patches land,
# and then the region's Kustomization re-applies its stale artifact.
#
# WHAT IS EXECUTED
# ────────────────
# The REAL rendered step-06 script bytes, sliced from `sync_secondary_ghcr_pull`
# through the `if ! pivot_secondary_regions` call site — the whole secondary
# leg, verbatim, including whatever read-back the tree under test carries. The
# ambient variables the earlier phases would have set (harbor_host,
# local_prefix, trust_secret) and the `read_secret_key` helper are supplied by
# a preamble; /secondary-kubeconfigs is relocated into the test tmpdir.
#
# BOTH-DIRECTIONS CONTRACT (vacuity)
# ──────────────────────────────────
#   C1/C2 must PASS on origin/main AND on the fixed tree (no blanket failure).
#   C5    must FAIL on origin/main only — it is the #5596 revert.
#   C6    must FAIL on origin/main only — an unreadable region recorded skipped.
#   C3/C4 are the pre-existing #5359 signals and must hold on both trees.
#
# Usage: bash tests/step06-secondary-durable.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1"; fail=$((fail+1)); }

mkdir -p "${TMP}/bin" "${TMP}/fake" "${TMP}/kc"

echo "[step06-secondary-durable] rendering chart: ${CHART_DIR}"
helm template smoke "${CHART_DIR}" > "${TMP}/render.yaml"

# ── Extract the step-06 script + its rendered env ─────────────────────────
python3 - "${TMP}/render.yaml" "${TMP}/step06.sh" "${TMP}/step06.env" <<'PY'
import sys, yaml
render, out_sh, out_env = sys.argv[1], sys.argv[2], sys.argv[3]
for d in yaml.safe_load_all(open(render)):
    if not d or d.get("kind") != "ConfigMap":
        continue
    if d["metadata"]["name"] != "cutover-step-06-helmrepository-patches":
        continue
    spec = yaml.safe_load(d["data"]["podSpec"])
    c = [x for x in spec["containers"] if x["name"] == "helmrepository-patches"][0]
    open(out_sh, "w").write(c["args"][0])
    def shquote(v):
        return "'" + str(v).replace("'", "'\\''") + "'"
    with open(out_env, "w") as fh:
        for e in c.get("env", []):
            if "value" in e:
                fh.write("export %s=%s\n" % (e["name"], shquote(e["value"])))
    sys.exit(0)
sys.exit("step-06 ConfigMap not found in render")
PY

[ -s "${TMP}/step06.sh" ] && ok "extracted the rendered step-06 script" \
                          || bad "could not extract the step-06 script"

# ── Slice the secondary leg out of the real script ────────────────────────
start_ln=$(awk 'index($0,"sync_secondary_ghcr_pull() {"){print NR; exit}' "${TMP}/step06.sh")
call_ln=$(awk 'index($0,"if ! pivot_secondary_regions; then"){print NR; exit}' "${TMP}/step06.sh")
if [ -z "${start_ln}" ] || [ -z "${call_ln}" ]; then
  bad "cannot locate the secondary leg (sync_secondary_ghcr_pull / pivot_secondary_regions call site)"
  echo; echo "RESULT: ${pass} passed, ${fail} failed"; [ "${fail}" -eq 0 ]; exit
fi
end_ln=$(awk -v s="${call_ln}" 'NR>s && $1=="fi" {print NR; exit}' "${TMP}/step06.sh")
[ -n "${end_ln}" ] || end_ln=$((call_ln+3))
sed -n "${start_ln},${end_ln}p" "${TMP}/step06.sh" > "${TMP}/leg.body.sh"
ok "sliced the secondary leg (lines ${start_ln}-${end_ln} of the rendered script)"

# The slice must still contain the pivot loop, or the harness is testing air.
if grep -q 'pivot_secondary_regions() {' "${TMP}/leg.body.sh"; then
  ok "slice contains pivot_secondary_regions"
else
  bad "slice lost pivot_secondary_regions — the harness boundaries are stale"
fi

# Relocate the container mount path; assert the substitution applied so a
# template rename cannot silently turn this into a no-op test.
mount_hits=$(grep -c '/secondary-kubeconfigs/\*.yaml' "${TMP}/leg.body.sh" || true)
if [ "${mount_hits}" -lt 1 ]; then
  bad "the leg no longer globs /secondary-kubeconfigs/*.yaml — harness mount relocation is stale"
fi
sed -i "s,/secondary-kubeconfigs/,${TMP}/kc/,g" "${TMP}/leg.body.sh"

# ── Preamble: what the earlier phases would have set ──────────────────────
cat > "${TMP}/preamble.sh" <<'PRE'
set -eu
harbor_host="registry.hw292.omani.works"
local_prefix="oci://registry.hw292.omani.works/openova-io"
trust_secret=""
# Phase-0's reader — the secondary ghcr-pull sync calls it. Not under test here.
read_secret_key() { printf '%s' "ZmFrZS1kb2NrZXJjb25maWc="; }
# Keep the guard fast: a real region converges in seconds, and the cases that
# must FAIL do so on their first or second sample.
export SECONDARY_READBACK_BUDGET_SECONDS=4
export SECONDARY_READBACK_INTERVAL_SECONDS=1
PRE

# ── fake kubectl ──────────────────────────────────────────────────────────
# Models the live shape per region:
#   state-<region>.txt   name=url, one per line
#   token-<region>       status.lastHandledReconcileAt of every Kustomization
# FAKE_REVERT_ON_POKE   a Kustomization annotate re-applies the region's STALE
#                       artifact: every HelmRepository goes back upstream.
#                       This is hw292 07:42:08Z.
# FAKE_KS_HANDLES=0     the poke is accepted but never handled (kustomize-
#                       controller wedged) — durability is unprovable.
# FAKE_KS_ABSENT=1      the region has no HR-owning Kustomizations at all.
# FAKE_LIST_FAIL=1      `get helmrepository` fails (unreachable region).
cat > "${TMP}/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
region="region-a"
if [ "${1:-}" = "--kubeconfig" ]; then
  region="$(basename "$2" .yaml)"
  shift 2
fi
state="${FAKE_DIR}/state-${region}.txt"
tokenf="${FAKE_DIR}/token-${region}"
verb="${1:-}"; shift || true
args="$*"

case "${verb}" in
  get)
    res="${1:-}"
    case "${res}" in
      helmrepository|helmrepositories|helmrepositories.source.toolkit.fluxcd.io)
        if [ "${FAKE_LIST_FAIL:-0}" = "1" ] && [ "${region}" != "region-a" ]; then
          echo "Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout" >&2
          exit 1
        fi
        [ -f "${state}" ] && cat "${state}"
        exit 0 ;;
      kustomization|kustomizations)
        [ "${FAKE_KS_ABSENT:-0}" = "1" ] && exit 1
        case "${args}" in
          *lastHandledReconcileAt*)
            [ -f "${tokenf}" ] && cat "${tokenf}"
            exit 0 ;;
        esac
        exit 0 ;;
      gitrepository|gitrepositories)
        case "${args}" in
          *'@.type=="Ready"'*) printf 'False' ;;
          *revision*) printf 'main@sha1:4cc85ad9' ;;
        esac
        exit 0 ;;
      secret) exit 1 ;;
      *) exit 0 ;;
    esac ;;
  patch)
    res="${1:-}"
    case "${res}" in
      helmrepository)
        name="$2"
        newurl=$(printf '%s' "${args}" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
        case " ${FAKE_PATCH_ERRORS:-} " in *" ${name} "*) exit 1 ;; esac
        case " ${FAKE_STUBBORN:-} " in *" ${name} "*) exit 0 ;; esac
        if [ -n "${newurl}" ] && [ -f "${state}" ]; then
          grep -v "^${name}=" "${state}" > "${state}.n" || true
          printf '%s=%s\n' "${name}" "${newurl}" >> "${state}.n"
          sort -o "${state}" "${state}.n"
        fi
        exit 0 ;;
      *) exit 0 ;;   # configmap status writes
    esac ;;
  annotate)
    # kubectl annotate --overwrite <kind> <name> -n <ns> key=value
    kind=""
    for a in "$@"; do
      case "${a}" in
        --overwrite) continue ;;
        kustomization|gitrepository|helmrepository) kind="${a}"; break ;;
      esac
    done
    tok=$(printf '%s' "${args}" | sed -n 's/.*reconcile\.fluxcd\.io\/requestedAt=\([0-9]*\).*/\1/p')
    if [ "${kind}" = "kustomization" ] && [ -n "${tok}" ]; then
      [ "${FAKE_KS_ABSENT:-0}" = "1" ] && exit 0
      if [ "${FAKE_REVERT_ON_POKE:-0}" = "1" ] && [ -f "${state}" ]; then
        # The region re-applies its PRE-CUTOVER artifact.
        sed -e "s,=.*,=${UPSTREAM_PREFIX}," "${state}" > "${state}.n"
        mv "${state}.n" "${state}"
      fi
      [ "${FAKE_KS_HANDLES:-1}" = "1" ] && printf '%s' "${tok}" > "${tokenf}"
    fi
    exit 0 ;;
  apply|delete|create) exit 0 ;;
  *) exit 0 ;;
esac
FAKE
chmod +x "${TMP}/bin/kubectl"

UP="oci://ghcr.io/openova-io"

run_case() {
  # $1 label  $2 expected exit (0|1)  $3 region state (empty = single-region)
  # env knobs are passed through the environment by the caller.
  local label="$1" expect="$2" state="$3"
  rm -f "${TMP}/kc"/*.yaml "${TMP}/fake"/* 2>/dev/null || true
  if [ -n "${state}" ]; then
    printf 'apiVersion: v1\nkind: Config\n' > "${TMP}/kc/me-east-215-b-1.yaml"
    printf '%s\n' "${state}" | grep -v '^$' > "${TMP}/fake/state-me-east-215-b-1.txt"
  fi
  : > "${TMP}/fake/state-region-a.txt"
  cat "${TMP}/preamble.sh" "${TMP}/leg.body.sh" > "${TMP}/run.sh"
  local rc=0
  set +e
  env -i \
    PATH="${TMP}/bin:${PATH}" HOME="${TMP}" \
    FAKE_DIR="${TMP}/fake" \
    FAKE_REVERT_ON_POKE="${FAKE_REVERT_ON_POKE:-0}" \
    FAKE_KS_HANDLES="${FAKE_KS_HANDLES:-1}" \
    FAKE_KS_ABSENT="${FAKE_KS_ABSENT:-0}" \
    FAKE_LIST_FAIL="${FAKE_LIST_FAIL:-0}" \
    FAKE_STUBBORN="${FAKE_STUBBORN:-}" \
    FAKE_PATCH_ERRORS="${FAKE_PATCH_ERRORS:-}" \
    bash -c "set -a; . '${TMP}/step06.env'; set +a; bash '${TMP}/run.sh'" \
    > "${TMP}/out.log" 2>&1
  rc=$?
  set -e
  if [ "${expect}" = "0" ]; then
    if [ "${rc}" -eq 0 ]; then ok "${label}: exit ${rc} (expected 0)"
    else bad "${label}: exit ${rc} (expected 0)"; sed 's/^/      /' "${TMP}/out.log" | tail -15; fi
  else
    if [ "${rc}" -ne 0 ]; then ok "${label}: exit ${rc} (expected non-zero)"
    else bad "${label}: exit 0 — the secondary leg reported SUCCESS without a durable pivot (#5596)"; sed 's/^/      /' "${TMP}/out.log" | tail -15; fi
  fi
}

echo "== exit-code cases against a fake kubectl =="

# C1 — single-region: no kubeconfigs mounted. MUST pass on both trees.
run_case "C1 single-region (no secondary kubeconfigs) is a legitimate no-op" 0 ""

# C2 — happy path: the pivot lands and SURVIVES the region's re-apply.
#      MUST pass on both trees (proves the guard is not a blanket fail).
run_case "C2 secondary pivots and the pivot survives its Kustomization re-apply" 0 \
  "bp-cilium=${UP}
bp-keycloak=${UP}
bp-agenity=${UP}"

# C3 — #5359 regression anchor: a patch the API rejects still fails.
FAKE_PATCH_ERRORS="bp-keycloak" run_case "C3 kubectl patch returns non-zero (#5359 fail counter)" 1 \
  "bp-cilium=${UP}
bp-keycloak=${UP}"

# C4 — #5359 regression anchor: patch accepted, object unchanged. The
#      immediate read-back catches this on BOTH trees.
FAKE_STUBBORN="bp-agenity" run_case "C4 patch accepted but the HelmRepository does not change" 1 \
  "bp-cilium=${UP}
bp-agenity=${UP}"

# C5 — THE #5596 CASE. Every patch lands; the region's own kustomize-controller
#      then re-applies its stale pre-cutover artifact and every URL goes back
#      upstream. origin/main exits 0 here (single-shot read-back sampled the
#      55-second window); the fixed tree pokes that re-apply into its own
#      window and fails loud.
FAKE_REVERT_ON_POKE=1 run_case "C5 pivot reverted by the region's own re-apply (hw292 07:42:08Z)" 1 \
  "bp-cilium=${UP}
bp-keycloak=${UP}
bp-agenity=${UP}"

# C6 — the region cannot be read at all. origin/main records `skipped` and
#      exits 0; an unreadable region is not a pivoted region.
FAKE_LIST_FAIL=1 run_case "C6 secondary HelmRepository enumeration fails (recorded skipped on main)" 1 \
  "bp-cilium=${UP}"

# C7 — the poke is accepted but never handled (wedged kustomize-controller).
#      Durability is unprovable, so the leg must not certify. On main this is
#      indistinguishable from success.
FAKE_KS_HANDLES=0 run_case "C7 reconcile poke never handled — durability unprovable" 1 \
  "bp-cilium=${UP}
bp-keycloak=${UP}"

# C8 — a region with NO HR-owning Kustomizations: nothing there re-applies
#      HelmRepositories, so a clean read-back IS the whole truth. MUST pass on
#      both trees — the fix must not fail a topology that cannot revert.
FAKE_KS_ABSENT=1 run_case "C8 region has no HR-owning Kustomizations (nothing can revert)" 0 \
  "bp-cilium=${UP}
bp-keycloak=${UP}"

echo
echo "RESULT: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
