#!/usr/bin/env bash
# #7834 — the three things of a FOLLOWER region that the hand-over chain must
# pivot, and that a fresh two-region Sovereign had to get by hand before:
#
#   H1  flux-system/openova-catalog. A follower's `sovereign-region-bootstrap`
#       Kustomization applies clusters/_template/sovereign-region/
#       multi-region-follower from the region's own Gitea, and its
#       catalog-helmrepository.yaml reads `url: oci://ghcr.io/openova-io`. The
#       step-06 in-region pivot Job rewrote only the bootstrap-kit directory, so
#       the Kustomization put the live object back on ghcr.io and step-08's
#       per-region source-host lint failed the region: cutoverComplete could not
#       become true.
#   H2  bootstrap-kit slot 62 `global.imageRegistry`. Step-07 writes it in the
#       primary region's Gitea only; since ADR-0015 D8 slot 62 runs in every
#       region, so a follower's continuum-controller kept its ghcr.io image name.
#   H3  catalyst-system/catalyst-gitea-token. The follower's continuum-controller
#       reads GITEA_TOKEN from it; only bp-catalyst-platform (primary region
#       only) mints it, and step-01 copied the shared PAT into the follower's
#       gitea namespace only.
#
# Evidence, each part able to fail:
#   (a) render gating — every new byte is absent from the single-region render
#       and present in the two-region one.
#   (b) the values are tied to the tree they act on — the follower path
#       cloud-init renders, the slot files and their seam, the continuum
#       chart's token Secret, namespace and Deployment name.
#   (c) EXECUTION of the real rendered secondary-pivot.sh + kit-rewrite.sh,
#       with the env of the rendered in-region Job, against a bare git origin
#       holding the REAL clusters/_template trees; the verdict is read from the
#       origin's main. Idempotent re-run. Two mutants (each new input emptied)
#       must turn H1 and H2 red.
#   (d) EXECUTION of step-01's real rendered secondary leg with a recording
#       kubectl stub: the Secret lands in the follower's catalyst-system, then
#       continuum-controller is restarted there; a follower without the
#       namespace or the Deployment is handled without a failure.
#
# Usage: bash tests/follower-region-pivots.sh [CHART_DIR]
# REPO_ROOT (env) overrides where clusters/, infra/ and products/ are read from.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
FQDN="hw306.omani.works"
UP="oci://ghcr.io/openova-io"
REG="registry.${FQDN}"
LOCAL="oci://${REG}/openova-io"
TRUST="cutover-harbor-ca"
REGION="me-east-215-b-1"
PAT="deadbeefcafe"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

render() { # $1=extra --set args (space-sep), $2=outfile
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" $1 >"$2" 2>"${TMP}/render.err" || {
    echo "helm template failed ($1):"; cat "${TMP}/render.err"; exit 1
  }
}
render "--set secondaryRegions.mirrorToSecondaryGitea=true" "${TMP}/on.yaml"
render "" "${TMP}/off.yaml"

echo "[follower] (a) render gating — single-region carries none of it, two-region carries all of it"
for tok in 'FOLLOWER_SOURCE_DIRS' 'IMAGE_REGISTRY_SLOT_FILES' 'FOLLOWER_GIT_TOKEN_NAMESPACE' 'follower-git-token' 'pivot_dirs='; do
  if grep -qF "${tok}" "${TMP}/off.yaml"; then fail "OFF render contains '${tok}' (single-region is not a no-op)"; else pass "OFF render is free of '${tok}'"; fi
  if grep -qF "${tok}" "${TMP}/on.yaml"; then pass "ON render contains '${tok}'"; else fail "ON render is missing '${tok}'"; fi
done

# Slice what (b)..(d) need out of the ON render: the pivot Job (after the
# step-06 run-time substitutions), its scripts, and step-01's args + env.
python3 - "${TMP}/on.yaml" "${TMP}" "${REGION}" "${LOCAL}" "${TRUST}" <<'PY' || { echo "slicing the render failed"; exit 1; }
import json, sys, yaml
on, tmp, region, local, trust = sys.argv[1:6]
docs = [d for d in yaml.safe_load_all(open(on)) if d]
def cm(name):
    return [d for d in docs if d.get("kind") == "ConfigMap" and d["metadata"]["name"] == name][0]
piv = cm("cutover-secondary-pivot-script")
raw = piv["data"]["secondary-pivot-job.yaml"].replace("__REGION__", region).replace("__LOCAL_PREFIX__", local).replace("__TRUST_SECRET__", trust)
job = yaml.safe_load(raw)
env = {e["name"]: e["value"] for e in job["spec"]["template"]["spec"]["containers"][0]["env"] if "value" in e}
json.dump(env, open(tmp + "/pivot-env.json", "w"))
open(tmp + "/secondary-pivot.sh", "w").write(piv["data"]["secondary-pivot.sh"])
open(tmp + "/kit-rewrite.sh", "w").write(cm("cutover-step-06-helmrepository-patches")["data"]["kit-rewrite.sh"])
s1 = [d for d in docs if d.get("kind") == "ConfigMap" and d["metadata"]["name"].startswith("cutover-step-01-")][0]
c = yaml.safe_load(s1["data"]["podSpec"])["containers"][0]
open(tmp + "/step01.sh", "w").write(c["args"][0])
json.dump({e["name"]: e["value"] for e in c["env"] if "value" in e}, open(tmp + "/step01-env.json", "w"))
PY
envval() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2], ""))' "$1" "$2"; }
DIRS="$(envval "${TMP}/pivot-env.json" FOLLOWER_SOURCE_DIRS)"
SLOTS="$(envval "${TMP}/pivot-env.json" IMAGE_REGISTRY_SLOT_FILES)"
TOK_NS="$(envval "${TMP}/step01-env.json" FOLLOWER_GIT_TOKEN_NAMESPACE)"
TOK_NAME="$(envval "${TMP}/step01-env.json" FOLLOWER_GIT_TOKEN_SECRET)"
TOK_RESTART="$(envval "${TMP}/step01-env.json" FOLLOWER_GIT_TOKEN_RESTART)"

echo "[follower] (b) the values are tied to the tree they act on"
python3 - "${REPO_ROOT}" "${DIRS}" "${SLOTS}" "${TOK_NS}" "${TOK_NAME}" "${TOK_RESTART}" "${UP}" <<'PY' || FAILURES=$((FAILURES + 1))
import glob, os, re, sys, yaml
root, dirs, slots, tok_ns, tok_name, tok_restart, up = sys.argv[1:8]
dirs, slots, restart = dirs.split(), slots.split(), tok_restart.split()
bad = 0
def say(ok, msg):
    global bad
    print(("  ok   — " if ok else "  FAIL — ") + msg)
    bad += 0 if ok else 1
tf = open(os.path.join(root, "infra/providers/_shared/cloudinit-control-plane.tftpl")).read()
m = re.search(r"path: \./(clusters/_template/sovereign-region/)%\{.*?%\{ else \}([a-z-]+)%\{ endif \}%\{ endif \}", tf)
say(m is not None, "cloud-init renders the follower region's sovereign-region-bootstrap path")
if m:
    follower = m.group(1) + m.group(2)
    say(follower in dirs, f"{follower} (the follower path of cloud-init) is in FOLLOWER_SOURCE_DIRS {dirs}")
ups = []
for d in dirs:
    say(os.path.isdir(os.path.join(root, d)), f"{d} exists in the repository")
    for f in glob.glob(os.path.join(root, d, "*.yaml")):
        if re.search(r"(?m)^\s*url:\s*" + re.escape(up) + r"\s*$", open(f).read()):
            ups.append(os.path.relpath(f, root))
say("clusters/_template/sovereign-region/multi-region-follower/catalog-helmrepository.yaml" in ups,
    f"the follower catalog source is among the files the rewrite has to move off {up} ({ups})")
slot62 = "clusters/_template/bootstrap-kit/62-bp-continuum.yaml"
say(slot62 in slots, f"slot 62 is in IMAGE_REGISTRY_SLOT_FILES {slots}")
for s in slots:
    t = open(os.path.join(root, s)).read()
    say(len(re.findall(r"(?m)^      imageRegistry:", t)) == 1, f"{s} carries exactly one 6-space imageRegistry seam")
    hrs = [d for d in yaml.safe_load_all(t) if d and d.get("kind") == "HelmRelease"]
    susp = str(hrs[0]["spec"].get("suspend", "")) if hrs else "<no HelmRelease>"
    say(bool(hrs) and "SECONDARY_HR_SUSPEND" not in susp and susp.lower() != "true", f"{s} is not suspended in a follower region (spec.suspend={susp!r})")
s62 = [d for d in yaml.safe_load_all(open(os.path.join(root, slot62))) if d and d.get("kind") == "HelmRelease"][0]
cv = yaml.safe_load(open(os.path.join(root, "products/continuum/chart/values.yaml")))
helpers = open(os.path.join(root, "products/continuum/chart/templates/_helpers.tpl")).read()
fm = re.search(r'define "continuum.fullname" -\}\}\s*\n\s*([a-z0-9-]+)', helpers)
say(s62["spec"]["targetNamespace"] == tok_ns, f"the token goes where slot 62 runs Continuum ({tok_ns})")
say(cv["continuum"]["git"]["tokenSecret"]["name"] == tok_name, f"the token Secret is the one the continuum chart reads ({tok_name})")
say(fm is not None and fm.group(1) in restart, f"the restart list {restart} names the continuum Deployment ({fm.group(1) if fm else None})")
sys.exit(1 if bad else 0)
PY

echo "[follower] (c) EXECUTE the real secondary-pivot.sh against a bare origin holding the real kit + sovereign-region trees"
mkdir -p "${TMP}/sp" "${TMP}/home"
cp "${TMP}/secondary-pivot.sh" "${TMP}/kit-rewrite.sh" "${TMP}/sp/"
seed_origin() {
  rm -rf "${TMP}/gitea" "${TMP}/seed"
  mkdir -p "${TMP}/gitea/openova" "${TMP}/seed/clusters/_template"
  git init -q --bare "${TMP}/gitea/openova/openova.git"
  git init -q -b main "${TMP}/seed"
  cp -R "${REPO_ROOT}/clusters/_template/bootstrap-kit" "${REPO_ROOT}/clusters/_template/sovereign-region" "${TMP}/seed/clusters/_template/"
  ( cd "${TMP}/seed" && git add -A && git -c user.name=t -c user.email=t@t commit -q -m seed && git push -q "${TMP}/gitea/openova/openova.git" main:main )
}
run_pivot() { # $1 = FOLLOWER_SOURCE_DIRS, $2 = IMAGE_REGISTRY_SLOT_FILES; prints rc; log at ${TMP}/run.log
  ( cd "${TMP}" && env -i PATH="${PATH}" HOME="${TMP}/home" TMPDIR="${TMP}" \
      REGION="${REGION}" UPSTREAM_BRANCH="$(envval "${TMP}/pivot-env.json" UPSTREAM_BRANCH)" \
      GITEA_INTERNAL_URL="file://${TMP}/gitea" GITEA_ORG="$(envval "${TMP}/pivot-env.json" GITEA_ORG)" \
      GITEA_REPO="$(envval "${TMP}/pivot-env.json" GITEA_REPO)" GITEA_USERNAME=gitea_admin GITEA_PAT="${PAT}" \
      UPSTREAM_PREFIX="$(envval "${TMP}/pivot-env.json" UPSTREAM_PREFIX)" \
      LOCAL_PREFIX="$(envval "${TMP}/pivot-env.json" LOCAL_PREFIX)" TRUST_SECRET="$(envval "${TMP}/pivot-env.json" TRUST_SECRET)" \
      FOLLOWER_SOURCE_DIRS="$1" IMAGE_REGISTRY_SLOT_FILES="$2" SECONDARY_PIVOT_DIR="${TMP}/sp" \
      sh "${TMP}/sp/secondary-pivot.sh" >"${TMP}/run.log" 2>&1 )
  echo $?
}
origin_main() { git --git-dir="${TMP}/gitea/openova/openova.git" rev-parse main; }
origin_tree() { # extract the origin's main into ${TMP}/out
  rm -rf "${TMP}/out"; mkdir -p "${TMP}/out"
  git --git-dir="${TMP}/gitea/openova/openova.git" archive main | tar -x -C "${TMP}/out"
}
# H1 and H2 read from the origin's main. Each prints its verdict and returns 0 only when the follower state is reached.
h1_state() {
  python3 - "${TMP}/out/clusters/_template/sovereign-region/multi-region-follower/catalog-helmrepository.yaml" "${LOCAL}" "${TRUST}" <<'PY'
import sys, yaml
path, local, trust = sys.argv[1:4]
d = [x for x in yaml.safe_load_all(open(path)) if x][0]
s = d["spec"]
ok = d["kind"] == "HelmRepository" and d["metadata"]["name"] == "openova-catalog" and s.get("url") == local \
     and (s.get("certSecretRef") or {}).get("name") == trust and (s.get("secretRef") or {}).get("name") == "ghcr-pull"
print(f"openova-catalog url={s.get('url')} certSecretRef={s.get('certSecretRef')} secretRef={s.get('secretRef')}")
sys.exit(0 if ok else 1)
PY
}
h2_state() {
  python3 - "${TMP}/out/clusters/_template/bootstrap-kit/62-bp-continuum.yaml" "${REG}" <<'PY'
import sys, yaml
path, reg = sys.argv[1:3]
hr = [x for x in yaml.safe_load_all(open(path)) if x and x.get("kind") == "HelmRelease"][0]
v = hr["spec"]["values"]["global"]["imageRegistry"]
print(f"bp-continuum global.imageRegistry={v!r}")
sys.exit(0 if v == reg else 1)
PY
}

seed_origin
before=$(origin_main)
rc=$(run_pivot "${DIRS}" "${SLOTS}")
if [ "${rc}" = "0" ]; then pass "secondary-pivot.sh exits 0"; else fail "secondary-pivot.sh exit ${rc}"; sed 's/^/      /' "${TMP}/run.log" | tail -20; fi
after=$(origin_main)
[ "${after}" != "${before}" ] && pass "a pivot commit landed on the origin's main" || fail "origin main did not move"
origin_tree
if out=$(h1_state); then pass "H1: ${out} — the state of the hand change, reached by the chain"; else fail "H1: ${out} — the follower catalog source is not pivoted"; fi
if out=$(h2_state); then pass "H2: ${out} — the state of the hand change, reached by the chain"; else fail "H2: ${out} — slot 62 keeps the chart default registry in the follower"; fi
left=$(cat "${TMP}/out/clusters/_template/bootstrap-kit/"*.yaml "${TMP}/out/clusters/_template/sovereign-region/multi-region-follower/"*.yaml | grep -cE "^[[:space:]]*url:[[:space:]]*${UP}[[:space:]]*$" || true)
[ "${left}" = "0" ] && pass "no kit or follower file declares ${UP} any more" || fail "${left} line(s) still declare ${UP}"
# Scope: the slots that are not marked followerRegions keep their seam empty in the follower.
others=$(python3 - "${CHART_DIR}/values.yaml" <<'PY'
import sys, yaml
v = yaml.safe_load(open(sys.argv[1]))["imageRegistryPivot"]
print(" ".join(e["slotFile"] for e in v["additionalHRs"] if not e.get("followerRegions")))
PY
)
for s in ${others}; do
  if grep -qxF "      imageRegistry: ''" "${TMP}/out/${s}"; then pass "${s} (not marked followerRegions) is left as it was"; else fail "${s} was edited although it is not marked followerRegions"; fi
done
grep -q "^\[secondary-pivot\] main-sha=${after}$" "${TMP}/run.log" && pass "the Job log reports main-sha=<origin main>" || fail "main-sha line missing or wrong"

rc=$(run_pivot "${DIRS}" "${SLOTS}")
[ "${rc}" = "0" ] && pass "re-run exits 0" || { fail "re-run exit ${rc}"; sed 's/^/      /' "${TMP}/run.log" | tail -20; }
[ "$(origin_main)" = "${after}" ] && pass "re-run pushed nothing (idempotent)" || fail "re-run moved origin main — the transform is not idempotent"
origin_tree
[ "$(grep -c 'certSecretRef:' "${TMP}/out/clusters/_template/sovereign-region/multi-region-follower/catalog-helmrepository.yaml")" = "1" ] \
  && pass "re-run left exactly one certSecretRef on the follower catalog" || fail "re-run duplicated certSecretRef on the follower catalog"

echo "[follower] (M) mutants — each new input emptied MUST turn its state red"
seed_origin
rc=$(run_pivot "" "${SLOTS}")
origin_tree
if out=$(h1_state); then fail "m1: FOLLOWER_SOURCE_DIRS empty and H1 still green (${out}) — the H1 check is vacuous"; else pass "m1: without FOLLOWER_SOURCE_DIRS the follower catalog stays upstream (${out}); H1 detects it"; fi
seed_origin
rc=$(run_pivot "${DIRS}" "")
origin_tree
if out=$(h2_state); then fail "m2: IMAGE_REGISTRY_SLOT_FILES empty and H2 still green (${out}) — the H2 check is vacuous"; else pass "m2: without IMAGE_REGISTRY_SLOT_FILES slot 62 stays on the default (${out}); H2 detects it"; fi

echo "[follower] (d) EXECUTE step-01's secondary leg: the token reaches the follower's readers (H3)"
# The leg (its start echo .. the no-op echo), closed, with the mount and /tmp paths moved under ${TMP}.
awk '/#6490 secondary-region mirror leg/,/secondary mirror is a no-op/' "${TMP}/step01.sh" >"${TMP}/leg.body"
mkdir -p "${TMP}/skc" "${TMP}/smir" "${TMP}/t" "${TMP}/applied"
python3 - "${TMP}/step01-env.json" >"${TMP}/leg.sh" <<'PY'
import json, shlex, sys
for k, v in json.load(open(sys.argv[1])).items():
    print(f"{k}={shlex.quote(v)}")
PY
{ echo 'set -eu'
  echo "MIRROR_JOB_WAIT_SECONDS=5; MIRROR_JOB_POLL_SECONDS=1"
  echo "PAT_SOURCE_NAMESPACE=catalyst-system; PAT_SOURCE_SECRET_NAME=catalyst-gitea-token; GITEA_PAT=${PAT}"
  # /tmp/ first: the later expressions insert ${TMP} paths, which must not be rewritten again.
  sed -e "s#/tmp/#${TMP}/t/#g" -e "s#/secondary-kubeconfigs/#${TMP}/skc/#g" -e "s#/secondary-mirror/#${TMP}/smir/#g" "${TMP}/leg.body"
  echo 'fi'
} >>"${TMP}/leg.sh"
printf 'x\n' >"${TMP}/smir/secondary-mirror.sh"
printf 'kind: Job\n' >"${TMP}/smir/secondary-mirror-job.yaml"
printf 'kubeconfig\n' >"${TMP}/skc/${REGION}.yaml"
# Recording stub: every call to calls.log; `apply -f F` keeps a copy of F; a Job reports Succeeded;
# NS_ABSENT / DEP_ABSENT make `get namespace` / `get deployment` fail.
cat >"${TMP}/kubectl" <<STUB
#!/bin/sh
echo "\$*" >> "${TMP}/calls.log"
for a in "\$@"; do case "\$a" in *succeeded*) echo 1; exit 0;; esac; done
case "\$*" in
  *" get namespace "*) [ -z "\${NS_ABSENT:-}" ] || exit 1 ;;
  *" get deployment "*) [ -z "\${DEP_ABSENT:-}" ] || exit 1 ;;
esac
prev=""
for a in "\$@"; do
  if [ "\${prev}" = "-f" ] && [ -f "\$a" ]; then n=\$(ls "${TMP}/applied" | wc -l); cp "\$a" "${TMP}/applied/\${n}.yaml"; echo "APPLIED \${n}" >> "${TMP}/calls.log"; fi
  prev="\$a"
done
exit 0
STUB
chmod +x "${TMP}/kubectl"
run_leg() { rm -rf "${TMP}/applied" "${TMP}/calls.log"; mkdir -p "${TMP}/applied"; : >"${TMP}/calls.log"
  ( PATH="${TMP}:${PATH}" sh "${TMP}/leg.sh" >"${TMP}/leg.out" 2>&1 ); echo $?; }
h3_state() { # the follower token Secret applied through the follower kubeconfig, then the restart
  python3 - "${TMP}/applied" "${TMP}/calls.log" "${TMP}/skc/${REGION}.yaml" "${TOK_NS:-catalyst-system}" "${TOK_NAME:-catalyst-gitea-token}" "${PAT}" <<'PY'
import glob, os, sys, yaml
applied, calls, skc, ns, name, pat = sys.argv[1:7]
lines = open(calls).read().splitlines()
hit = None
for f in sorted(glob.glob(applied + "/*.yaml"), key=lambda p: int(os.path.basename(p)[:-5])):
    try:
        d = yaml.safe_load(open(f))
    except yaml.YAMLError:
        continue
    if isinstance(d, dict) and d.get("kind") == "Secret" and d["metadata"].get("name") == name and d["metadata"].get("namespace") == ns \
       and (d.get("stringData") or {}).get("token") == pat:
        hit = os.path.basename(f)[:-5]
if hit is None:
    print(f"no Secret {ns}/{name} carrying the shared PAT was applied"); sys.exit(1)
i_apply = lines.index("APPLIED " + hit) - 1
if f"--kubeconfig {skc}" not in lines[i_apply]:
    print(f"the Secret was applied, but not through the follower kubeconfig: {lines[i_apply]}"); sys.exit(1)
restart = [i for i, l in enumerate(lines) if f"--kubeconfig {skc} -n {ns} rollout restart deployment continuum-controller" in l]
if not restart or restart[0] < i_apply:
    print("no restart of continuum-controller after the copy"); sys.exit(1)
print(f"{ns}/{name} applied in the follower, continuum-controller restarted after it")
PY
}
rc=$(run_leg)
[ "${rc}" = "0" ] && pass "the leg exits 0 with one follower region" || { fail "the leg exit ${rc}"; sed 's/^/      /' "${TMP}/leg.out" | tail -20; }
if out=$(h3_state); then pass "H3: ${out}"; else fail "H3: ${out}"; fi
[ ! -e "${TMP}/t/follower-git-token.yaml" ] && pass "the PAT file is removed after the apply" || fail "the PAT file is left on disk"
grep -qF -- "${PAT}" "${TMP}/calls.log" && fail "the PAT appears on a kubectl command line" || pass "the PAT never appears on a kubectl command line"

rc=$(NS_ABSENT=1 run_leg)
if [ "${rc}" = "0" ] && grep -q "no namespace ${TOK_NS}" "${TMP}/leg.out" && ! grep -q 'rollout restart' "${TMP}/calls.log" && ! h3_state >/dev/null; then
  pass "a follower without ${TOK_NS}: nothing is copied or restarted, the leg goes on"
else
  fail "a follower without ${TOK_NS} is not handled (exit ${rc})"; sed 's/^/      /' "${TMP}/leg.out" | tail -8
fi
rc=$(DEP_ABSENT=1 run_leg)
if [ "${rc}" = "0" ] && grep -q 'it reads the token when it starts' "${TMP}/leg.out" && ! grep -q 'rollout restart' "${TMP}/calls.log"; then
  pass "a follower without the Deployment yet: the token is copied, nothing is restarted, the leg goes on"
else
  fail "a follower without the Deployment is not handled (exit ${rc})"; sed 's/^/      /' "${TMP}/leg.out" | tail -8
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "[follower] all assertions passed — a follower region's catalog source, slot-62 image registry and Continuum Git token reach the state the hand changes produced, from the chain alone."
  exit 0
fi
echo "[follower] ${FAILURES} assertion(s) FAILED"
exit 1
