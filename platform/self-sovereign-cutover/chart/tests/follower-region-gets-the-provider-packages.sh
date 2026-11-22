#!/usr/bin/env bash
# follower-region-gets-the-provider-packages — why step 03 copies the OpenTofu provider
# packages in the primary region only, and the guarantees that make that enough.
#
# Step 03, Phase A5 copies every package the ConfigMap crossplane-system/opentofu-providers
# lists into the registry (offlinemirror_copy). The follower's prewarm script
# (secondary-harbor-prewarm-script-configmap.yaml) has no such leg and the follower has no
# such ConfigMap. For a pack whose kit has a Harbor that is enough, for two reasons; the
# first holds in every configuration, the second only with the default enable_shared_pg:
#
#   LIST   the follower needs no package. The list rides in the Flux Kustomization
#          infrastructure-providers together with its consumers (provider-opentofu, the CLI
#          configuration, the Workspaces' layer). Every primary-role root applies it, every
#          secondary-role root deletes it, and the follower region boots with the role
#          secondary and a secondary-role root (RENDER). No code rewrites a region's role:
#          Continuum moves Application placements and the D8 data pairs (ADR-0015; shared-pg
#          and the harbor slot are not moved), and the control plane stays in the primary
#          region (docs/ARCHITECTURE.md §10.7). A follower runs no Workspace.
#   SHARE  with enable_shared_pg "true" (the default of every pack's variables.tf and of
#          catalyst-api's request) the follower's Harbor serves the packages anyway: every
#          region's Harbor uses ONE database, the ClusterMesh write alias shared-pg-mesh-rw
#          (the value catalyst-api re-stamps on followers, clustermesh.go) with no harbor-pg
#          of its own, and ONE object store (the same bucket, endpoint and region, which
#          slot 19 reads from flux-system/object-storage with storage type s3). With
#          enable_shared_pg "false", a supported input, each region's Harbor has a database
#          of its own and the follower's holds nothing step 03 copied: LIST alone carries it.
#
# What checks what:
#   RENDER (tofu test, not this script) each pack's real two-region render, from the pack's
#          own expressions: the follower's role and root, Harbor's database seams for both
#          values of enable_shared_pg, the object-storage seed of both regions. Runs
#          follower_harbor_shares_the_primarys_registry_store and
#          follower_harbor_own_database_when_shared_pg_is_off in
#          infra/providers/<pack>/tests/multi_region.tftest.hcl (workflow infra-<pack>-tofu).
#          This script checks that every pack whose kit has a Harbor carries both runs, and
#          names the packs whose kit leaves out bp-harbor (no registry in any region).
#   DB     the template turns enable_shared_pg into Harbor's seams in a two-region render,
#          the alias equals clustermesh.go's, slot 19 turns the seams into the database host
#          and a harbor-pg of its own or none.
#   STORE  flux-system/object-storage carries the seeded values, slot 19 reads them, type s3.
#   LIST   as above, from the root folders.
# A control run of every part on the unmutated mirror comes first; then a mutant per part
# switches one guarantee off and must turn that part red with its own failure line. If this
# test fails, the follower's registry may lack what step 03 copies while the follower needs
# it: give the follower's prewarm script the Phase A5 leg (the same offlinemirror_copy, and
# the list) or restore what failed.
#
# CI: .github/workflows/test-follower-region-provider-packages.yaml runs this script on a
# change to any file it reads; chart-tests-pr runs it on a change to the chart.
#
# Usage: bash tests/follower-region-gets-the-provider-packages.sh [CHART_DIR]
# REPO_ROOT (env) overrides where clusters/, infra/ and products/ are read from.
# Needs: bash, awk, sed, yq v4.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
TPL=infra/providers/_shared/cloudinit-control-plane.tftpl
SLOT=clusters/_template/bootstrap-kit/19-harbor.yaml
OBJ=clusters/_template/bootstrap-secrets/object-storage.yaml
MESH_GO=products/catalyst/bootstrap/api/internal/handler/clustermesh.go
LAYER=clusters/_template/root/base-primary/infrastructure-providers.yaml
RUNS="follower_harbor_shares_the_primarys_registry_store follower_harbor_own_database_when_shared_pg_is_off"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }
command -v yq >/dev/null 2>&1 || { echo "yq is required"; exit 1; }

# Evaluates EXPR (a template expression) with <name>\t<value> bindings: string literals, bound
# names, == !=, ?:, parentheses, base64encode(x) kept as x. Anything else is exit 3.
EVAL_AWK='
function die(m) { print "cannot evaluate: " m > "/dev/stderr"; failed = 1; exit 3 }
function lex(s,   n) {
  n = 0
  while (s != "") {
    if (match(s, /^[ \t]+/)) { s = substr(s, RLENGTH + 1); continue }
    if (match(s, /^"[^"]*"/) || match(s, /^(==|!=)/) || match(s, /^[?:()]/) || match(s, /^[A-Za-z_][A-Za-z0-9_]*/)) { tk[++n] = substr(s, 1, RLENGTH); s = substr(s, RLENGTH + 1); continue }
    die("the text " s)
  }
  tk[n + 1] = ""; return n
}
function want(t) { if (tk[p] != t) die("expected " t " at " tk[p]); p++ }
function prim(   t, v) {
  t = tk[p++]
  if (t == "(") { v = cond(); want(")"); return v }
  if (substr(t, 1, 1) == "\"") { if (index(t, "${")) die("the interpolated string " t); return substr(t, 2, length(t) - 2) }
  if (t ~ /^[A-Za-z_]/) {
    if (tk[p] == "(") { if (t != "base64encode") die("the function " t); p++; v = cond(); want(")"); return v }
    if (!(t in val)) die("the name " t ", an input this test does not bind")
    return val[t]
  }
  die("the token " t)
}
function cmp(   a, op) { a = prim(); op = tk[p]; if (op != "==" && op != "!=") return a; p++; return ((a == prim()) == (op == "==")) ? "\001T" : "\001F" }
function cond(   c, a, b) {
  c = cmp(); if (tk[p] != "?") return c
  if (c != "\001T" && c != "\001F") die("a condition that is not a comparison")
  p++; a = cond(); want(":"); b = cond(); return c == "\001T" ? a : b
}
{ i = index($0, "\t"); val[substr($0, 1, i - 1)] = substr($0, i + 1) }
END {
  if (failed) exit 3
  n = lex(EXPR); p = 1; v = cond()
  if (p != n + 1) die("text after the expression")
  if (v == "\001T" || v == "\001F") die("a bare comparison")
  print v
}'

# Flux postBuild substitution: ${NAME}, ${NAME:=d}, ${NAME:-d}, ${NAME=d}, ${NAME-d}; a bound
# NAME takes its binding, an unbound one its default. Bindings: <name>\t<value> lines in BF.
SUBST_AWK='
BEGIN { while ((getline l < BF) > 0) { i = index(l, "\t"); val[substr(l, 1, i - 1)] = substr(l, i + 1) } }
{
  s = $0; out = ""
  while (match(s, /\$\{[A-Za-z_][A-Za-z0-9_]*(:?[-=][^}]*)?\}/)) {
    st = RSTART; ln = RLENGTH; ref = substr(s, st + 2, ln - 3); nm = ref; df = ""
    if (match(ref, /:?[-=]/)) { nm = substr(ref, 1, RSTART - 1); df = substr(ref, RSTART + RLENGTH) }
    out = out substr(s, 1, st - 1) ((nm in val) ? val[nm] : df); s = substr(s, st + ln)
  }
  print out s
}'

evaluate() { awk -v EXPR="$1" "${EVAL_AWK}" "$2"; }
flux_subst() { awk -v BF="$2" "${SUBST_AWK}" "$1"; }
# The expression of KEY in the template: `KEY: "${...}"` or `KEY=${...}`, exactly one line.
key_expr() {
  awk -v k="$2" '
    index($0, k ": \"${") { s = $0; sub(/^[^"]*"\$\{/, "", s); sub(/\}"[ \t]*$/, "", s); print s; n++ }
    index($0, k "=${") { s = $0; sub(/^[^$]*\$\{/, "", s); sub(/\}[ \t]*$/, "", s); print s; n++ }
    END { exit n == 1 ? 0 : 4 }' "$1"
}
# Every provider pack under ROOT: a folder whose main.tf renders the shared template.
packs() {
  local d
  for d in "$1"/infra/providers/*/; do
    d="${d%/}"
    grep -qF '../_shared/cloudinit-control-plane.tftpl", {' "${d}/main.tf" 2>/dev/null && basename "${d}"
  done
  return 0
}

# ── RENDER: every pack whose kit has a Harbor has its two-region render evaluated by tofu ──
check_render() {
  local root="$1" bad=0 pack ex at t r miss all
  all=$(packs "${root}")
  [ -n "${all}" ] || { echo "  FAIL — RENDER: no folder under infra/providers renders ${TPL}"; return 1; }
  for pack in ${all}; do
    ex="infra/providers/${pack}/kit-exclude.txt"; at=""
    [ -f "${root}/${ex}" ] && at=$(grep -n -x 'bp-harbor' "${root}/${ex}" | head -1 | cut -d: -f1)
    if [ -n "${at}" ]; then
      echo "  info — RENDER ${pack}: its kit leaves out bp-harbor (${ex}:${at}): no region has a registry, so step 03 copies into none and no follower is served by one"
      continue
    fi
    t="infra/providers/${pack}/tests/multi_region.tftest.hcl"; miss=""
    for r in ${RUNS}; do grep -qx "run \"${r}\" {" "${root}/${t}" 2>/dev/null || miss="${miss} ${r}"; done
    if [ -n "${miss}" ]; then echo "  FAIL — RENDER ${pack}: its kit has a Harbor, but ${t} has no run${miss}: nothing evaluates the follower's role and root, Harbor's database and the object store of this pack's render"; bad=1
    else echo "  ok   — RENDER ${pack}: its kit has a Harbor, and ${t} evaluates the follower's role and root, Harbor's database and the object store (runs ${RUNS// / and })"; fi
  done
  return "${bad}"
}

# ── DB: the seams of Harbor's database in a two-region render, and what slot 19 makes of them ──
check_db() { # $1=root  ->  prints the lines; returns 1 on any failure
  local root="$1" bad=0 mesh shared role b host own p pown
  mesh=$(sed -n 's/^[[:space:]]*clusterMeshSharedPGMeshRWHost[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "${root}/${MESH_GO}")
  [ -n "${mesh}" ] || { echo "  FAIL — DB: ${MESH_GO} names no clusterMeshSharedPGMeshRWHost"; return 1; }
  for shared in true false; do
    for role in primary secondary; do
      # A two-region render: its replica label is not empty. Which label, role and
      # enable_shared_pg each pack passes to each region is the RENDER runs' part.
      b="${TMP}/bind.${shared}.${role}.$$"
      printf 'enable_shared_pg\t%s\nreplica_region_canonical_label\t%s\nsovereign_region_role\t%s\n' "${shared}" "two-region" "${role}" >"${b}"
      host=$(evaluate "$(key_expr "${root}/${TPL}" SOVEREIGN_HARBOR_PG_HOST)" "${b}" 2>&1) || { echo "  FAIL — DB enable_shared_pg=${shared} ${role}: SOVEREIGN_HARBOR_PG_HOST ${host}"; bad=1; continue; }
      own=$(evaluate "$(key_expr "${root}/${TPL}" SOVEREIGN_HARBOR_PG_OWN_CLUSTER)" "${b}" 2>&1) || { echo "  FAIL — DB enable_shared_pg=${shared} ${role}: SOVEREIGN_HARBOR_PG_OWN_CLUSTER ${own}"; bad=1; continue; }
      printf 'SOVEREIGN_HARBOR_PG_HOST\t%s\nSOVEREIGN_HARBOR_PG_OWN_CLUSTER\t%s\n' "${host}" "${own}" >"${b}.sub"
      flux_subst "${root}/${SLOT}" "${b}.sub" >"${b}.yaml"
      p=$(yq 'select(.kind == "HelmRelease") | .spec.values.harbor.database.external.host' "${b}.yaml" 2>/dev/null)
      pown=$(yq 'select(.kind == "HelmRelease") | .spec.values.postgres.cluster.enabled' "${b}.yaml" 2>/dev/null)
      if [ "${p}" != "${host}" ] || [ "${pown}" != "${own}" ]; then
        echo "  FAIL — DB enable_shared_pg=${shared} ${role}: slot 19 renders the database host ${p:-none} and a harbor-pg of its own=${pown:-none}, not the seams ${host} and ${own}"; bad=1; continue
      fi
      if [ "${shared}" = true ]; then
        if [ "${host}" != "${mesh}" ] || [ "${own}" != "false" ]; then
          echo "  FAIL — DB enable_shared_pg=true ${role}: Harbor's database is ${host} (own cluster ${own}), not the one write alias ${mesh}: this region's Harbor keeps records of its own"; bad=1; continue
        fi
        echo "  ok   — DB enable_shared_pg=true ${role}: Harbor's database is ${mesh}, no harbor-pg of its own (template + slot 19)"
      else
        echo "  info — DB enable_shared_pg=false ${role}: Harbor's database is ${host}, a harbor-pg of its own=${own} (template + slot 19): the follower's Harbor holds nothing step 03 copied; LIST alone carries the guarantee"
      fi
    done
  done
  return "${bad}"
}

# ── STORE: slot 19 stores blobs in the bucket that flux-system/object-storage carries ──
check_store() {
  local root="$1" bad=0 t k want
  for k in bucket endpoint region; do
    t=$(yq ".data[\"s3-${k}\"]" "${root}/${OBJ}")
    want="\${OBJECT_STORAGE_$(printf '%s' "${k}" | tr '[:lower:]' '[:upper:]')}"
    [ "${t}" = "${want}" ] || { echo "  FAIL — STORE: ${OBJ} s3-${k} is '${t}', not ${want}"; bad=1; }
  done
  t=$(yq 'select(.kind == "HelmRelease") | .spec.values.harbor.persistence.imageChartStorage.type' "${root}/${SLOT}")
  [ "${t}" = "s3" ] || { echo "  FAIL — STORE: slot 19 storage type is '${t}', not s3: each region's registry keeps blobs on its own volume"; bad=1; }
  for k in "s3-bucket bucket" "s3-endpoint regionendpoint" "s3-region region"; do
    set -- ${k}
    t=$(yq "select(.kind == \"HelmRelease\") | .spec.valuesFrom[] | select(.kind == \"Secret\" and .name == \"object-storage\" and .valuesKey == \"$1\") | .targetPath" "${root}/${SLOT}")
    [ "${t}" = "harbor.persistence.imageChartStorage.s3.$2" ] || { echo "  FAIL — STORE: slot 19 does not read object-storage $1 into harbor.persistence.imageChartStorage.s3.$2 (reads '${t}')"; bad=1; }
  done
  [ "${bad}" = 0 ] && echo "  ok   — STORE: slot 19 stores blobs in the s3 bucket of flux-system/object-storage, which carries the seeded bucket, endpoint and region"
  return "${bad}"
}

# ── LIST: the list goes where its consumers go, and only primary-role roots apply them ──
kfiles() { # $1=dir: every file its kustomization.yaml reaches through resources, one per line
  local d="$1" r
  [ -f "${d}/kustomization.yaml" ] || return 0
  while IFS= read -r r; do
    [ -n "${r}" ] || continue
    if [ -d "${d}/${r}" ]; then kfiles "$(cd "${d}/${r}" && pwd)"; elif [ -f "${d}/${r}" ]; then echo "$(cd "$(dirname "${d}/${r}")" && pwd)/$(basename "${r}")"; fi
  done < <(yq '.resources[]' "${d}/kustomization.yaml")
}
flux_children() { # $1=root dir: the Flux Kustomization names it applies, after its delete patches
  local d="$1" r p id="${TMP}/fk.${BASHPID}.${RANDOM}"
  [ -f "${d}/kustomization.yaml" ] || return 0
  {
    while IFS= read -r r; do
      [ -n "${r}" ] || continue
      if [ -d "${d}/${r}" ]; then flux_children "$(cd "${d}/${r}" && pwd)"
      elif [ -f "${d}/${r}" ]; then yq 'select(.kind == "Kustomization" and .apiVersion == "kustomize.toolkit.fluxcd.io/v1") | .metadata.name' "${d}/${r}"; fi
    done < <(yq '.resources[]' "${d}/kustomization.yaml")
  } | grep -v -e '^---$' -e '^$' | sort -u >"${id}.names"
  : >"${id}.deleted"
  while IFS= read -r p; do
    [ -n "${p}" ] || continue
    printf '%s\n' "${p}" | yq 'select(.["$patch"] == "delete" and .kind == "Kustomization") | .metadata.name' >>"${id}.deleted"
  done < <(yq '(.patches // [])[] | .patch' "${d}/kustomization.yaml")
  grep -v -x -F -f "${id}.deleted" "${id}.names" || true
  rm -f "${id}.names" "${id}.deleted"
}
check_list() {
  local root="$1" bad=0 ns cm path files f d role has
  ns=$(yq '.tofuProviders.namespace' "${CHART_DIR}/values.yaml"); cm=$(yq '.tofuProviders.listConfigMapName' "${CHART_DIR}/values.yaml")
  path=$(yq 'select(.kind == "Kustomization" and .metadata.name == "infrastructure-providers") | .spec.path' "${root}/${LAYER}")
  [ -n "${path}" ] && [ -d "${root}/${path#./}" ] || { echo "  FAIL — LIST: ${LAYER} names no folder (${path:-none})"; return 1; }
  files=$(kfiles "$(cd "${root}/${path#./}" && pwd)")
  has=""
  for f in ${files}; do
    [ "$(yq "select(.kind == \"ConfigMap\" and .metadata.name == \"${cm}\" and .metadata.namespace == \"${ns}\") | .metadata.name" "${f}")" = "${cm}" ] && has="${has} list"
    [ -n "$(yq 'select(.kind == "Provider" and .metadata.name == "provider-opentofu") | .metadata.name' "${f}")" ] && has="${has} provider"
  done
  case "${has}" in *list*provider*|*provider*list*) echo "  ok   — LIST: ${ns}/${cm} and provider-opentofu are applied by the one Kustomization infrastructure-providers" ;;
    *) echo "  FAIL — LIST: infrastructure-providers (${path}) does not carry both ${ns}/${cm} and provider-opentofu (found:${has:- nothing})"; bad=1 ;; esac
  for d in "${root}"/clusters/_template/root/* "${root}"/clusters/_template/providers/*/root/*; do
    [ -f "${d}/kustomization.yaml" ] || continue
    case "$(basename "${d}")" in *primary) role=primary ;; *secondary) role=secondary ;; *) continue ;; esac
    if flux_children "$(cd "${d}" && pwd)" | grep -qx infrastructure-providers; then has=yes; else has=no; fi
    if [ "${role}:${has}" = "primary:yes" ] || [ "${role}:${has}" = "secondary:no" ]; then echo "  ok   — LIST: ${d#"${root}"/} (${role}) applies infrastructure-providers: ${has}"
    else echo "  FAIL — LIST: ${d#"${root}"/} (${role}) applies infrastructure-providers: ${has}"; bad=1; fi
  done
  return "${bad}"
}

PARTS="check_render check_db check_store check_list"
echo "[follower-packages] the follower's Harbor serves what step 03 copies into the primary's, or the follower needs none of it"
for c in ${PARTS}; do "${c}" "${REPO_ROOT}" || FAILURES=$((FAILURES + 1)); done

# ── Control, then mutants: each switches one guarantee off and must turn its part red ──
mirror() { # $1=dest: the files the four parts read, copied from REPO_ROOT
  local p pack
  for p in "${TPL}" "${SLOT}" "${OBJ}" "${MESH_GO}" clusters/_template/root clusters/_template/infrastructure/providers; do
    mkdir -p "$1/$(dirname "${p}")"; cp -R "${REPO_ROOT}/${p}" "$1/${p}"
  done
  for p in "${REPO_ROOT}"/clusters/_template/providers/*/root; do
    mkdir -p "$1/${p#"${REPO_ROOT}"/}"; cp -R "${p}/." "$1/${p#"${REPO_ROOT}"/}/"
  done
  for pack in $(packs "${REPO_ROOT}"); do
    for p in main.tf kit-exclude.txt tests/multi_region.tftest.hcl; do
      [ -f "${REPO_ROOT}/infra/providers/${pack}/${p}" ] || continue
      mkdir -p "$1/infra/providers/${pack}/$(dirname "${p}")"; cp "${REPO_ROOT}/infra/providers/${pack}/${p}" "$1/infra/providers/${pack}/${p}"
    done
  done
}
control() { # every part green on the unmutated mirror, else no mutant's red means anything
  local m="${TMP}/control" c
  mirror "${m}"
  for c in ${PARTS}; do
    if ! "${c}" "${m}" >"${m}.${c}.out" 2>&1; then
      fail "control: ${c#check_} is red on the unmutated mirror (the mirror misses a file the part reads), so the mutants are not run"; sed 's/^/      /' "${m}.${c}.out"; return 1
    fi
  done
  pass "control: every part is green on the unmutated mirror"
}
mutant() { # $1=label $2=check $3=file (relative) $4=sed program $5=the start of the FAIL line it must cause
  local m="${TMP}/m$((++MUT))"
  mirror "${m}"
  sed -i "$4" "${m}/$3"
  if cmp -s "${REPO_ROOT}/$3" "${m}/$3"; then fail "mutant '$1' changed nothing in $3 (the mutant is stale)"; return; fi
  if "$2" "${m}" >"${m}.out" 2>&1; then fail "mutant '$1' stays green: ${2#check_} cannot see it"; sed 's/^/      /' "${m}.out"; return; fi
  if grep -qF -- "  FAIL — $5" "${m}.out"; then pass "mutant '$1' turns ${2#check_} red where it aims: $(grep -m1 -F -- "  FAIL — $5" "${m}.out" | sed 's/^  FAIL — //')"
  else fail "mutant '$1' turns ${2#check_} red, but not with a line starting '$5'"; sed 's/^/      /' "${m}.out"; fi
}
MUT=0
echo "[follower-packages] control and mutants"
if control; then
  mutant "the follower's Harbor gets a database of its own" check_db "${TPL}" \
    's|SOVEREIGN_HARBOR_PG_HOST: "\${\(.*\)}"$|SOVEREIGN_HARBOR_PG_HOST: "${sovereign_region_role == "secondary" ? "harbor-pg-rw.harbor.svc.cluster.local" : (\1)}"|' \
    "DB enable_shared_pg=true secondary: Harbor's database is harbor-pg-rw"
  mutant "slot 19 ignores the host seam" check_db "${SLOT}" \
    's|host: \${SOVEREIGN_HARBOR_PG_HOST:=harbor-pg-rw.harbor.svc.cluster.local}|host: harbor-pg-rw.harbor.svc.cluster.local|' \
    "DB enable_shared_pg=true primary: slot 19 renders the database host harbor-pg-rw"
  mutant "catalyst-api re-stamps followers with another host" check_db "${MESH_GO}" \
    's|^\([[:space:]]*clusterMeshSharedPGMeshRWHost[[:space:]]*=[[:space:]]*"\)shared-pg-mesh-rw|\1shared-pg-mesh-rw-b|' \
    "DB enable_shared_pg=true primary: Harbor's database is shared-pg-mesh-rw.shared-data.svc.cluster.local (own cluster false), not the one write alias shared-pg-mesh-rw-b"
  mutant "registry blobs on a volume per region" check_store "${SLOT}" \
    's|^          type: s3$|          type: filesystem|' \
    "STORE: slot 19 storage type is 'filesystem'"
  mutant "object-storage names a bucket of another seed key" check_store "${OBJ}" \
    's|s3-bucket: "\${OBJECT_STORAGE_BUCKET}"|s3-bucket: "${OBJECT_STORAGE_REGION_BUCKET}"|' \
    "STORE: ${OBJ} s3-bucket"
  mutant "a follower root applies the provider layer" check_list clusters/_template/root/multi-region-follower-secondary/kustomization.yaml \
    '/name: infrastructure-providers/d' \
    "LIST: clusters/_template/root/multi-region-follower-secondary (secondary) applies infrastructure-providers: yes"
  mutant "the list leaves the provider layer" check_list clusters/_template/infrastructure/providers/base/kustomization.yaml \
    '/^  - opentofu-providers.yaml$/d' \
    "LIST: infrastructure-providers ("
  mutant "the local kit gains a Harbor" check_render infra/providers/local/kit-exclude.txt \
    '/^bp-harbor$/d' \
    "RENDER local: its kit has a Harbor"
  mutant "the huawei pack drops its shared-pg-off run" check_render infra/providers/huawei/tests/multi_region.tftest.hcl \
    's|^run "follower_harbor_own_database_when_shared_pg_is_off" {|run "renamed" {|' \
    "RENDER huawei: its kit has a Harbor"
fi

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[follower-packages] FAILED: ${FAILURES}"; exit 1; fi
echo "[follower-packages] PASS"
