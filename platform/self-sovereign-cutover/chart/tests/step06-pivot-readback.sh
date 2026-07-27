#!/usr/bin/env bash
# bp-self-sovereign-cutover — step-06 region-A pivot READ-BACK guard (#5436).
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────
# Step-06 used to report `success` whenever every `kubectl patch` CALL
# returned zero. It never read the cluster back. So a HelmRepository that
# Phase-0.9's enumeration never saw (the catalyst-api org-tenant pipeline
# seeds one AFTER the enumeration ran) or whose patch was accepted by the
# API without changing anything was INVISIBLE: zero patch calls failed,
# `fail` stayed 0, the Job exited 0, and catalyst-api recorded
# `step.helmrepository-patches.result=success` on a Sovereign whose charts
# still resolved to the mothership. Live on hw290 2026-07-27: SIX
# flux-system HelmRepositories on oci://ghcr.io/openova-io with the step
# green and the Job succeeded=1 — which makes `cutoverComplete=true`
# reachable on a still-tethered environment.
#
# WHY THIS TEST EXECUTES THE SCRIPT INSTEAD OF GREPPING THE RENDER
# ───────────────────────────────────────────────────────────────
# The rendered log text was ALREADY correct before the fix — the step
# printed per-HR results and step-08 printed the OFFENDER list. Log text is
# exactly what made this ship. So the assertions below are on the SCRIPT'S
# EXIT CODE, driven by a fake kubectl that reproduces the live shape:
# `kubectl patch` returns 0 and the object does not change.
#
# WHAT IS EXECUTED
# ────────────────
# The REAL rendered step-06 script bytes, truncated immediately after the
# Phase-1.5 read-back call (everything past it — the Gitea clone/push, the
# secondary-region legs, the helm pull probe — needs a live cluster and is
# out of this guard's reach). Two container mount paths are relocated into
# the test tmpdir because the harness is not a container:
#   /work/helmrepository-names.txt   (the step ConfigMap projection)
# Nothing else is rewritten; the logic under test is verbatim.
#
# Usage: bash tests/step06-pivot-readback.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

pass=0; fail=0
ok()  { echo "  ok: $1"; pass=$((pass+1)); }
bad() { echo "  FAIL: $1"; fail=$((fail+1)); }

mkdir -p "${TMP}/bin" "${TMP}/work"

echo "[step06-pivot-readback] rendering chart"
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
    # Export every literal-value env the render carries, so this test tracks
    # values.yaml rather than a hardcoded copy of it. secretKeyRef-form envs
    # (HARBOR_USERNAME / HARBOR_PASSWORD / GITEA_*) are supplied by the harness.
    def shquote(v):
        return "'" + str(v).replace("'", "'\\''") + "'"
    with open(out_env, "w") as fh:
        for e in c.get("env", []):
            if "value" in e:
                fh.write("export %s=%s\n" % (e["name"], shquote(e["value"])))
    # Also emit the projected names list so /work is faithful.
    open(out_sh + ".names", "w").write(d["data"]["helmrepository-names.txt"])
    sys.exit(0)
sys.exit("step-06 ConfigMap not found in render")
PY

[ -s "${TMP}/step06.sh" ] && ok "extracted the rendered step-06 script" \
                          || bad "could not extract the step-06 script"

cp "${TMP}/step06.sh.names" "${TMP}/work/helmrepository-names.txt"

# ── A. render/wiring assertions ───────────────────────────────────────────
echo "== A. read-back is present and wired at both call sites =="

if grep -q 'assert_region_a_pivot()' "${TMP}/step06.sh"; then
  ok "step-06 defines assert_region_a_pivot"
else
  bad "step-06 has no assert_region_a_pivot function — the region-A read-back was dropped (#5436)"
fi

# Both call sites must be fail-wired. `|| exit 1` (or a bare call under
# set -e) is acceptable; `|| true` / `|| echo` is the regression.
if grep -nE 'assert_region_a_pivot "[^"]+"[^|]*\|\|[[:space:]]*(true|echo|:)' "${TMP}/step06.sh" >/dev/null; then
  bad "an assert_region_a_pivot call site swallows its verdict (|| true / || echo) — the exact #5436 shape"
else
  ok "no assert_region_a_pivot call site swallows its verdict"
fi

callsites=$(grep -cE '^[[:space:]]*assert_region_a_pivot "' "${TMP}/step06.sh" || true)
if [ "${callsites}" -ge 2 ]; then
  ok "read-back is called at ${callsites} sites (post-Phase-1 + pre-strip)"
else
  bad "read-back called at ${callsites} site(s); expected >= 2 (post-Phase-1 fail-fast + pre-strip gate)"
fi

# The pre-strip call MUST precede the executable mothership-auth strip, or
# the hw139 half-pivot (ghcr.io creds gone, HRs still on ghcr.io) is
# reachable again.
prestrip_ln=$(awk 'index($0,"assert_region_a_pivot \"Phase-3"){print NR; exit}' "${TMP}/step06.sh")
del_ln=$(awk 'index($0,"jq_filter=\"${jq_filter} | del(.auths["){print NR; exit}' "${TMP}/step06.sh")
[ -n "${del_ln}" ] || del_ln=$(awk '/jq_filter=.*del\(\.auths\[/{print NR; exit}' "${TMP}/step06.sh")
if [ -n "${prestrip_ln}" ] && [ -n "${del_ln}" ] && [ "${prestrip_ln}" -lt "${del_ln}" ]; then
  ok "pre-strip read-back (line ${prestrip_ln}) runs BEFORE the executable strip (line ${del_ln})"
else
  bad "pre-strip read-back at ${prestrip_ln:-<none>} does not precede the strip at ${del_ln:-<none>} — the strip could fire on a half-pivoted region-A (hw139 shape)"
fi

# ── B. execution harness ──────────────────────────────────────────────────
echo "== B. exit-code assertions against a fake kubectl =="

# Truncate the real script immediately after the Phase-1.5 read-back call.
cut_ln=$(awk 'index($0,"assert_region_a_pivot \"Phase-1.5"){print NR; exit}' "${TMP}/step06.sh")
if [ -z "${cut_ln}" ]; then
  bad "no Phase-1.5 read-back call to truncate at — cannot run the exit-code cases"
  echo; echo "RESULT: ${pass} passed, ${fail} failed"; [ "${fail}" -eq 0 ]; exit
fi
head -n "${cut_ln}" "${TMP}/step06.sh" > "${TMP}/step06.phase15.sh"

# Relocate the container mount path (the harness is not a container). Assert
# the substitution actually applied so a template rename cannot silently turn
# this into a no-op test.
work_hits=$(grep -c '/work/helmrepository-names.txt' "${TMP}/step06.phase15.sh" || true)
if [ "${work_hits}" -lt 1 ]; then
  bad "step-06 no longer reads /work/helmrepository-names.txt — harness mount relocation is stale"
fi
sed -i "s,/work/helmrepository-names.txt,${TMP}/work/helmrepository-names.txt,g" "${TMP}/step06.phase15.sh"

# ── fake kubectl ──
# Models the LIVE shape: `kubectl patch helmrepository` returns 0 and the
# object does not change when the name is in FAKE_STUBBORN.
cat > "${TMP}/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
# fake kubectl. State = ${FAKE_STATE}, one `name=url` per line.
args="$*"
verb="$1"; shift

listing_name_url() {
  # Phase-0.9 sees the state as-is; every LATER `name=url` listing (the
  # Phase-1.5 read-back) additionally sees FAKE_LATE_ARRIVAL — modelling a
  # HelmRepository the org-tenant pipeline seeded after the enumeration ran.
  n=0
  [ -f "${FAKE_LIST_COUNT}" ] && n=$(cat "${FAKE_LIST_COUNT}")
  echo $((n+1)) > "${FAKE_LIST_COUNT}"
  cat "${FAKE_STATE}"
  if [ -n "${FAKE_LATE_ARRIVAL:-}" ] && [ "${n}" -ge 1 ]; then
    printf '%s\n' "${FAKE_LATE_ARRIVAL}"
  fi
}

case "${verb}" in
  get)
    res="$1"
    case "${res}" in
      gateway.gateway.networking.k8s.io)
        case "${args}" in *jsonpath*) printf 'True' ;; esac
        exit 0 ;;
      secret)
        name="$2"
        if [ "${name}" = "${GHCR_PULL_SECRET_NAME}" ]; then
          cat "${FAKE_GHCR_SECRET}"
          exit 0
        fi
        # wildcard TLS secret absent -> Phase-0.5 WARN path (trust_secret="")
        exit 1 ;;
      helmrepositories|helmrepositories.source.toolkit.fluxcd.io)
        case "${args}" in
          *'{.metadata.namespace}{" "}{.metadata.name}'*)
            sed -e 's/=.*//' -e 's/^/flux-system /' "${FAKE_STATE}" ;;
          *'{.metadata.name}={.spec.url}'*)
            listing_name_url ;;
          *'{.metadata.name}{" "}{.spec.url}'*)
            sed 's/=/ /' "${FAKE_STATE}" ;;
        esac
        exit 0 ;;
      helmrepository)
        name="$2"
        case "${args}" in
          *'{.spec.url}'*)
            grep "^${name}=" "${FAKE_STATE}" | head -n1 | cut -d= -f2- ;;
          *'{.spec.certSecretRef.name}'*) : ;;
        esac
        # not-found => empty stdout, non-zero (the script's SKIP path)
        grep -q "^${name}=" "${FAKE_STATE}" || exit 1
        exit 0 ;;
      *) exit 1 ;;
    esac ;;
  patch)
    res="$1"
    case "${res}" in
      secret) exit 0 ;;
      helmrepository)
        name="$2"
        case " ${FAKE_PATCH_ERRORS:-} " in
          *" ${name} "*) exit 1 ;;   # the API itself rejects the patch
        esac
        case " ${FAKE_STUBBORN:-} " in
          *" ${name} "*) exit 0 ;;   # accepted, object does NOT change
        esac
        newurl=$(printf '%s' "${args}" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
        if [ -n "${newurl}" ]; then
          grep -v "^${name}=" "${FAKE_STATE}" > "${FAKE_STATE}.n" || true
          printf '%s=%s\n' "${name}" "${newurl}" >> "${FAKE_STATE}.n"
          sort -o "${FAKE_STATE}" "${FAKE_STATE}.n"
        fi
        exit 0 ;;
      *) exit 0 ;;
    esac ;;
  annotate|apply|delete) exit 0 ;;
  *) exit 0 ;;
esac
FAKE
chmod +x "${TMP}/bin/kubectl"

# ghcr-pull Secret fixture: upstream auth present, local Harbor auth absent
# (so Phase-0 takes the ADD path rather than the idempotent skip).
dockercfg=$(printf '{"auths":{"ghcr.io":{"username":"x","auth":"eA=="}}}' | base64 -w0)
printf '{"data":{".dockerconfigjson":"%s"}}\n' "${dockercfg}" > "${TMP}/ghcr-secret.json"

run_case() {
  # $1 label  $2 initial state (newline-separated name=url)  $3 stubborn names
  # $4 expected exit (0|nonzero)  $5 late-arrival line (optional)
  local label="$1" state="$2" stubborn="$3" expect="$4" late="${5:-}"
  printf '%s\n' "${state}" | grep -v '^$' > "${TMP}/state.txt"
  : > "${TMP}/listcount"
  local rc=0
  set +e
  # env -i so the step script sees ONLY the rendered env + the harness fakes —
  # a stray inherited HELMREPO_* would make the assertions meaningless. The
  # ambient PATH is carried through (behind ${TMP}/bin, so the fake kubectl
  # still wins) because jq/base64/coreutils live in different prefixes on
  # different runners.
  env -i \
    PATH="${TMP}/bin:${PATH}" \
    HOME="${TMP}" \
    FAKE_STATE="${TMP}/state.txt" \
    FAKE_LIST_COUNT="${TMP}/listcount" \
    FAKE_GHCR_SECRET="${TMP}/ghcr-secret.json" \
    FAKE_STUBBORN="${stubborn}" \
    FAKE_PATCH_ERRORS="${FAKE_PATCH_ERRORS:-}" \
    FAKE_LATE_ARRIVAL="${late}" \
    HARBOR_USERNAME=admin HARBOR_PASSWORD=notasecret \
    bash -c "set -a; . '${TMP}/step06.env'; set +a; bash '${TMP}/step06.phase15.sh'" \
    > "${TMP}/out.log" 2>&1
  rc=$?
  set -e
  if [ "${expect}" = "0" ]; then
    if [ "${rc}" -eq 0 ]; then ok "${label}: exit ${rc} (expected 0)"
    else bad "${label}: exit ${rc} (expected 0)"; sed 's/^/      /' "${TMP}/out.log" | tail -20; fi
  else
    if [ "${rc}" -ne 0 ]; then ok "${label}: exit ${rc} (expected non-zero)"
    else bad "${label}: exit 0 — step-06 reported SUCCESS with an un-pivoted HelmRepository (#5436)"; sed 's/^/      /' "${TMP}/out.log" | tail -20; fi
  fi
}

UP="oci://ghcr.io/openova-io"

# B1 — happy path: every HR pivots. Proves the assert is not a blanket fail.
run_case "B1 all HelmRepositories pivot" \
  "bp-cilium=${UP}
bp-keycloak=${UP}
bp-agenity=${UP}" \
  "" 0

# B2 — THE #5436 CASE: the API accepts the patch, the object does not change.
#      This is the hw290 shape: patch call returns 0, HR still on ghcr.io.
run_case "B2 one HelmRepository refuses to pivot (patch OK, state unchanged)" \
  "bp-cilium=${UP}
bp-keycloak=${UP}
bp-agenity=${UP}" \
  "bp-agenity" 1

# B3 — six offenders, mirroring the hw290 live set exactly.
run_case "B3 the hw290 six (agenity/keycloak/newapi/openclaw/stalwart/wordpress)" \
  "bp-cilium=${UP}
bp-agenity=${UP}
bp-keycloak=${UP}
bp-openclaw=${UP}
bp-stalwart-tenant=${UP}
bp-wordpress-tenant=${UP}" \
  "bp-agenity bp-keycloak bp-openclaw bp-stalwart-tenant bp-wordpress-tenant" 1

# B4 — an HR the Phase-0.9 enumeration never saw (org-tenant pipeline seeds
#      it mid-step). Phase-1 patches nothing wrong; only a read-back catches it.
run_case "B4 HelmRepository seeded after the Phase-0.9 enumeration" \
  "bp-cilium=${UP}
bp-keycloak=${UP}" \
  "" 1 "bp-openclaw=${UP}"

# B5 — the read-back must NOT be stricter than step-08's offender scan: the
#      slot-managed self-refs step-08 whitelists must not fail step-06.
run_case "B5 whitelisted self-ref left upstream does not fail the step" \
  "bp-cilium=${UP}
bp-newapi=${UP}
bp-self-sovereign-cutover=${UP}" \
  "bp-newapi bp-self-sovereign-cutover" 0

# B6 — regression anchor for the pre-existing signal: a patch the API itself
#      rejects must still fail via the Phase-1 fail counter.
FAKE_PATCH_ERRORS="bp-keycloak" run_case "B6 kubectl patch returns non-zero" \
  "bp-cilium=${UP}
bp-keycloak=${UP}" \
  "" 1

# ── C. sibling step-11: same class, same wiring requirement ───────────────
echo "== C. step-11 crossplane-provider-pivot patch verdicts are wired (#5436) =="
python3 - "${TMP}/render.yaml" "${TMP}/step11.sh" <<'PY'
import sys, yaml
for d in yaml.safe_load_all(open(sys.argv[1])):
    if not d or d.get("kind") != "ConfigMap":
        continue
    if d["metadata"]["name"] != "cutover-step-11-crossplane-provider-pivot":
        continue
    spec = yaml.safe_load(d["data"]["podSpec"])
    c = spec["containers"][0]
    open(sys.argv[2], "w").write(c["args"][0])
    sys.exit(0)
sys.exit("step-11 ConfigMap not found in render")
PY
# Step-11's Provider patch loop runs inside a `printf | while` SUBSHELL, so a
# shell variable cannot carry the verdict out — the marker FILE the #5204
# verify misses already use is the only working seam. Every `FAIL <name>`
# branch in the patch loop must touch it, or the step exits 0 with the
# Provider still on xpkg.upbound.io / the mothership proxy.
unwired=$(awk '
  /echo "\[crossplane-provider-pivot\]   FAIL /{ n=NR }
  n && NR>n && NR<=n+2 && /touch \/tmp\/xpkg-verify-failed/ { n=0 }
  n && NR==n+2 { print n; n=0 }
' "${TMP}/step11.sh" | wc -l | tr -d ' ')
if [ "${unwired}" = "0" ]; then
  ok "every step-11 Provider-patch FAIL branch records the fail-loud marker"
else
  bad "${unwired} step-11 Provider-patch FAIL branch(es) print a verdict without recording /tmp/xpkg-verify-failed — the step exits 0 on a failed pivot (#5436)"
fi
if grep -q 'if \[ -e /tmp/xpkg-verify-failed \]; then' "${TMP}/step11.sh"; then
  ok "step-11 still checks the marker and exits non-zero"
else
  bad "step-11 lost the /tmp/xpkg-verify-failed fail-loud check"
fi

echo
echo "RESULT: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
