#!/usr/bin/env bash
# follower-region-gets-the-provider-packages — why step 03 copies the OpenTofu provider
# packages in the primary region only, and the guarantee that makes that enough.
#
# Step 03, Phase A5 copies every package the ConfigMap crossplane-system/opentofu-providers
# lists into the registry (offlinemirror_copy). The follower's prewarm script
# (secondary-harbor-prewarm-script-configmap.yaml) has no such leg and the follower has no
# such ConfigMap. On a real two-region install the packages were served by both regions.
# That is by construction, not by luck:
#
#   DB     every region's Harbor uses ONE database. Cloud-init gives each region the same
#          SOVEREIGN_HARBOR_PG_HOST, the ClusterMesh write alias shared-pg-mesh-rw (the value
#          catalyst-api re-stamps on followers, clustermesh.go), and
#          SOVEREIGN_HARBOR_PG_OWN_CLUSTER=false, so no region renders a harbor-pg of its own.
#          The alias follows the writable member, so a promotion of shared-pg keeps the
#          records (platform/postgres/chart/tests/postgres-render.sh tests the alias).
#   STORE  every region's Harbor uses ONE object store: each provider pack gives every
#          region's render the same bucket, endpoint and region, and slot 19 reads them from
#          flux-system/object-storage with storage type s3.
#   LIST   the list rides in the Flux Kustomization infrastructure-providers together with
#          its consumers (provider-opentofu, the CLI configuration, the Workspaces' layer).
#          Every primary-role root applies it; every secondary-role root deletes it. No code
#          rewrites a region's role: Continuum moves Application placements and the D8 data
#          pairs (ADR-0015; shared-pg and the harbor slot are not moved), and the control plane
#          stays in the primary region (docs/ARCHITECTURE.md §10.7). A follower runs no
#          Workspace, so it needs no list.
#
# So a package copied once into the primary's Harbor is a record in the one database and blobs
# in the one bucket: the follower's Harbor serves it too. Each part below is computed for the
# primary and the follower of a two-region Sovereign from the files that configure them, and a
# mutant per part switches the sharing off and must turn that part red. If this test fails,
# the follower's registry no longer holds what step 03 copies: give the follower's prewarm
# script the Phase A5 leg (the same offlinemirror_copy, and the list) or restore the sharing.
#
# Usage: bash tests/follower-region-gets-the-provider-packages.sh [CHART_DIR]
# REPO_ROOT (env) overrides where clusters/, infra/ and products/ are read from.
# Needs: bash, awk, sed, yq v4.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
PACKS="huawei hetzner local"
TPL=infra/providers/_shared/cloudinit-control-plane.tftpl
SLOT=clusters/_template/bootstrap-kit/19-harbor.yaml
OBJ=clusters/_template/bootstrap-secrets/object-storage.yaml
MESH_GO=products/catalyst/bootstrap/api/internal/handler/clustermesh.go
LAYER=clusters/_template/root/base-primary/infrastructure-providers.yaml
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }
command -v yq >/dev/null 2>&1 || { echo "yq is required"; exit 1; }

# The arguments of every render of the shared template in a pack's main.tf, one per line:
# <call>\t<loop variables or ->\t<name>\t<expression on one line>.
ARGS_AWK='
function opens(s,   t) { t = s; return gsub(/[[({]/, "", t) }
function closes(s,   t) { t = s; return gsub(/[])}]/, "", t) }
function flush() { if (name != "") printf "%d\t%s\t%s\t%s\n", call, loops, name, expr; name = "" }
{
  line = $0; sub(/^[ \t]+/, "", line)
  if (!inside) {
    if (index($0, "../_shared/cloudinit-control-plane.tftpl\", {")) {
      call++; inside = 1; depth = opens($0) - closes($0); base = depth; name = ""; loops = "-"
      src = $0; if (src !~ /for [A-Za-z_]/) src = prev
      if (match(src, /for [A-Za-z_][A-Za-z0-9_]*(, *[A-Za-z_][A-Za-z0-9_]*)? in /)) { loops = substr(src, RSTART + 4, RLENGTH - 8); gsub(/ /, "", loops) }
    }
    if (line != "" && line !~ /^#/) prev = $0
    next
  }
  if (line ~ /^#/ || line == "") next
  at = depth; depth += opens($0) - closes($0)
  if (depth < base) { flush(); inside = 0; next }
  if (at == base && match(line, /^[a-z_][a-z0-9_]* *= */)) { flush(); name = substr(line, 1, RLENGTH); sub(/ *= *$/, "", name); expr = substr(line, RLENGTH + 1) }
  else if (name != "") expr = expr " " line
  sub(/ +#.*$/, "", expr)
}
END { if (inside) exit 2 }'

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

render_args() { awk "${ARGS_AWK}" "$1"; }
evaluate() { awk -v EXPR="$1" "${EVAL_AWK}" "$2"; }
flux_subst() { awk -v BF="$2" "${SUBST_AWK}" "$1"; }
# The expression of KEY in the template: `KEY: "${...}"` or `KEY=${...}`, exactly one line.
key_expr() {
  awk -v k="$2" '
    index($0, k ": \"${") { s = $0; sub(/^[^"]*"\$\{/, "", s); sub(/\}"[ \t]*$/, "", s); print s; n++ }
    index($0, k "=${") { s = $0; sub(/^[^$]*\$\{/, "", s); sub(/\}[ \t]*$/, "", s); print s; n++ }
    END { exit n == 1 ? 0 : 4 }' "$1"
}
# The expression of argument NAME in render CALL of an args file.
arg_of() { awk -F'\t' -v c="$2" -v n="$3" '$1 == c && $3 == n { print $4; f = 1 } END { exit f ? 0 : 4 }' "$1"; }
loops_of() { awk -F'\t' -v c="$2" '$1 == c { print $2; exit }' "$1"; }
# The value of EXPR in region INSTANCE of a render: each loop variable becomes <var>#<instance>,
# so an expression that reads the loop variable differs between regions.
region_value() {
  awk -v e="$1" -v l="$2" -v i="$3" 'BEGIN {
    n = split(l, vs, ","); for (j = 1; j <= n; j++) { if (vs[j] == "-") continue; o = ""; s = e
      while (match(s, "(^|[^A-Za-z0-9_.])" vs[j] "([^A-Za-z0-9_]|$)")) { pre = substr(s, RSTART, RLENGTH); k = index(pre, vs[j]); o = o substr(s, 1, RSTART - 1) substr(pre, 1, k - 1) vs[j] "#" i substr(pre, k + length(vs[j])); s = substr(s, RSTART + RLENGTH) }
      e = o s }
    print e }'
}
# The string default of variable NAME in a variables.tf.
var_default() {
  awk -v n="$2" '$0 ~ "^variable \"" n "\"" { f = 1; next } f && /^}/ { exit } f && /^ *default *= *"/ { s = $0; sub(/^[^"]*"/, "", s); sub(/".*$/, "", s); print s; exit }' "$1"
}
# The primary's and the follower's render of a pack: "<call> <instance>" twice. One looped
# render: its instances 0 and 1. Two renders: the unlooped one, then the looped one's instance 1.
regions_of() {
  local calls; calls=$(cut -f1 "$1" | sort -u | wc -l); calls=$((calls))
  if [ "${calls}" = 1 ] && [ "$(loops_of "$1" 1)" != "-" ]; then echo "1 0"; echo "1 1"; return 0; fi
  if [ "${calls}" = 2 ] && [ "$(loops_of "$1" 1)" = "-" ] && [ "$(loops_of "$1" 2)" != "-" ]; then echo "1 0"; echo "2 1"; return 0; fi
  return 1
}
# Region values of NAME in a pack: prints "<primary>\n<follower>".
pair_of() { # $1=args file $2=name
  local c i e
  while read -r c i; do
    e=$(arg_of "$1" "${c}" "$2") || return 1
    region_value "${e}" "$(loops_of "$1" "${c}")" "${i}"
  done < <(regions_of "$1")
}

# ── DB: one Harbor database for every region ─────────────────────────────────────────────
check_db() { # $1=root  ->  prints ok/FAIL lines; returns 1 on any failure
  local root="$1" bad=0 mesh pack args pe pf ep sp role host own b p
  mesh=$(sed -n 's/^[[:space:]]*clusterMeshSharedPGMeshRWHost[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "${root}/${MESH_GO}")
  [ -n "${mesh}" ] || { echo "  FAIL — DB: ${MESH_GO} names no clusterMeshSharedPGMeshRWHost"; return 1; }
  for pack in ${PACKS}; do
    args="${TMP}/args.${pack}.$$"; render_args "${root}/infra/providers/${pack}/main.tf" >"${args}" || { echo "  FAIL — DB ${pack}: the render arguments cannot be read"; bad=1; continue; }
    regions_of "${args}" >/dev/null || { echo "  FAIL — DB ${pack}: no primary and follower render found"; bad=1; continue; }
    for b in enable_shared_pg replica_region_canonical_label; do
      pe=""; pf=""; { read -r pe; read -r pf; } < <(pair_of "${args}" "${b}")
      if [ -z "${pe:-}" ] || [ "${pe}" != "${pf}" ]; then echo "  FAIL — DB ${pack}: ${b} differs between the primary's render (${pe:-none}) and the follower's (${pf:-none})"; bad=1; fi
    done
    ep=$(arg_of "${args}" 1 enable_shared_pg)
    case "${ep}" in
      var.*) sp=$(var_default "${root}/infra/providers/${pack}/variables.tf" "${ep#var.}") ;;
      \"*\") sp=${ep//\"/} ;;
      *) sp="" ;;
    esac
    [ -n "${sp}" ] || { echo "  FAIL — DB ${pack}: enable_shared_pg (${ep}) has no default this test can read"; bad=1; continue; }
    for role in primary secondary; do
      b="${TMP}/bind.${pack}.${role}.$$"
      printf 'enable_shared_pg\t%s\nreplica_region_canonical_label\t%s\nsovereign_region_role\t%s\n' "${sp}" "the follower's label" "${role}" >"${b}"
      host=$(evaluate "$(key_expr "${root}/${TPL}" SOVEREIGN_HARBOR_PG_HOST)" "${b}" 2>&1) || { echo "  FAIL — DB ${pack} ${role}: SOVEREIGN_HARBOR_PG_HOST ${host}"; bad=1; continue; }
      own=$(evaluate "$(key_expr "${root}/${TPL}" SOVEREIGN_HARBOR_PG_OWN_CLUSTER)" "${b}" 2>&1) || { echo "  FAIL — DB ${pack} ${role}: SOVEREIGN_HARBOR_PG_OWN_CLUSTER ${own}"; bad=1; continue; }
      if [ "${host}" != "${mesh}" ] || [ "${own}" != "false" ]; then
        echo "  FAIL — DB ${pack} ${role}: Harbor's database is ${host} (own cluster ${own}), not the one write alias ${mesh}: this region's Harbor keeps records of its own"; bad=1; continue
      fi
      printf 'SOVEREIGN_HARBOR_PG_HOST\t%s\nSOVEREIGN_HARBOR_PG_OWN_CLUSTER\t%s\n' "${host}" "${own}" >"${b}.sub"
      flux_subst "${root}/${SLOT}" "${b}.sub" >"${b}.yaml"
      p=$(yq 'select(.kind == "HelmRelease") | .spec.values.harbor.database.external.host' "${b}.yaml" 2>/dev/null)
      own=$(yq 'select(.kind == "HelmRelease") | .spec.values.postgres.cluster.enabled' "${b}.yaml" 2>/dev/null)
      if [ "${p}" != "${mesh}" ] || [ "${own}" != "false" ]; then
        echo "  FAIL — DB ${pack} ${role}: slot 19 renders the database host ${p:-none} and a harbor-pg of its own=${own:-none}, not ${mesh} and false"; bad=1; continue
      fi
      echo "  ok   — DB ${pack} ${role}: Harbor's database is ${mesh}, no harbor-pg of its own (cloud-init + slot 19)"
    done
  done
  return "${bad}"
}

# ── STORE: one object store for every region ─────────────────────────────────────────────
check_store() {
  local root="$1" bad=0 pack args n pe pf key v t k want b0 b1 r0 r1
  for pack in ${PACKS}; do
    args="${TMP}/sargs.${pack}.$$"; render_args "${root}/infra/providers/${pack}/main.tf" >"${args}" || { echo "  FAIL — STORE ${pack}: the render arguments cannot be read"; bad=1; continue; }
    regions_of "${args}" >/dev/null || { echo "  FAIL — STORE ${pack}: no primary and follower render found"; bad=1; continue; }
    b0="${TMP}/sb0.${pack}.$$"; b1="${TMP}/sb1.${pack}.$$"; : >"${b0}"; : >"${b1}"
    for n in object_storage_bucket_name object_storage_endpoint object_storage_region; do
      pe=""; pf=""; { read -r pe; read -r pf; } < <(pair_of "${args}" "${n}")
      printf '%s\t%s\n' "${n}" "${pe:-}" >>"${b0}"; printf '%s\t%s\n' "${n}" "${pf:-}" >>"${b1}"
    done
    for key in OBJECT_STORAGE_BUCKET OBJECT_STORAGE_ENDPOINT OBJECT_STORAGE_REGION; do
      v=$(key_expr "${root}/${TPL}" "${key}") || { echo "  FAIL — STORE: the template has no single ${key} line"; bad=1; continue; }
      r0=$(evaluate "${v}" "${b0}" 2>&1) || { echo "  FAIL — STORE ${pack}: ${key} ${r0}"; bad=1; continue; }
      r1=$(evaluate "${v}" "${b1}" 2>&1) || { echo "  FAIL — STORE ${pack}: ${key} ${r1}"; bad=1; continue; }
      if [ -z "${r0}" ] || [ "${r0}" != "${r1}" ]; then echo "  FAIL — STORE ${pack}: ${key} is '${r0}' in the primary and '${r1}' in the follower: the follower's Harbor stores its blobs elsewhere"; bad=1
      else echo "  ok   — STORE ${pack}: ${key} is the same in both regions (${r0})"; fi
    done
  done
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
  [ "${bad}" = 0 ] && echo "  ok   — STORE: slot 19 stores blobs in the s3 bucket of flux-system/object-storage"
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

echo "[follower-packages] the follower's Harbor serves what step 03 copies into the primary's"
check_db "${REPO_ROOT}" || FAILURES=$((FAILURES + 1))
check_store "${REPO_ROOT}" || FAILURES=$((FAILURES + 1))
check_list "${REPO_ROOT}" || FAILURES=$((FAILURES + 1))

# ── Mutants: each switches the sharing off one way and must turn its part red ─────────────
mirror() { # $1=dest: the files the three parts read, copied from REPO_ROOT
  local p
  for p in "${TPL}" "${SLOT}" "${OBJ}" "${MESH_GO}" infra/providers/huawei infra/providers/hetzner infra/providers/local \
           clusters/_template/root clusters/_template/infrastructure/providers; do
    mkdir -p "$1/$(dirname "${p}")"; cp -R "${REPO_ROOT}/${p}" "$1/${p}"
  done
  for p in "${REPO_ROOT}"/clusters/_template/providers/*/root; do
    mkdir -p "$1/${p#"${REPO_ROOT}"/}"; cp -R "${p}/." "$1/${p#"${REPO_ROOT}"/}/"
  done
}
mutant() { # $1=label $2=check $3=file (relative) $4=sed program
  local m="${TMP}/m$((++MUT))"
  mirror "${m}"
  sed -i "$4" "${m}/$3"
  if cmp -s "${REPO_ROOT}/$3" "${m}/$3"; then fail "mutant '$1' changed nothing in $3 (the mutant is stale)"; return; fi
  if "$2" "${m}" >"${m}.out" 2>&1; then fail "mutant '$1' stays green: ${2#check_} cannot see it"; sed 's/^/      /' "${m}.out"
  else pass "mutant '$1' turns ${2#check_} red: $(grep -m1 'FAIL' "${m}.out" | sed 's/^  FAIL — //')"; fi
}
MUT=0
echo "[follower-packages] mutants"
mutant "the follower's Harbor gets a database of its own" check_db "${TPL}" \
  's|SOVEREIGN_HARBOR_PG_HOST: "\${\(.*\)}"$|SOVEREIGN_HARBOR_PG_HOST: "${sovereign_region_role == "secondary" ? "harbor-pg-rw.harbor.svc.cluster.local" : (\1)}"|'
mutant "shared-pg off by default (huawei)" check_db infra/providers/huawei/variables.tf \
  '/^variable "enable_shared_pg"/,/^}/ s/default *= *"true"/default = "false"/'
mutant "slot 19 ignores the host seam" check_db "${SLOT}" \
  's|host: \${SOVEREIGN_HARBOR_PG_HOST:=harbor-pg-rw.harbor.svc.cluster.local}|host: harbor-pg-rw.harbor.svc.cluster.local|'
mutant "a bucket per region (huawei)" check_store infra/providers/huawei/main.tf \
  's|object_storage_bucket_name = var.obs_bucket_name|object_storage_bucket_name = "${var.obs_bucket_name}-${r.code}"|'
mutant "the follower's render names another endpoint (hetzner)" check_store infra/providers/hetzner/main.tf \
  '/k => replace(templatefile/,/^  }$/ s|object_storage_endpoint    = local.object_storage_endpoint|object_storage_endpoint    = local.follower_object_storage_endpoint|'
mutant "registry blobs on a volume per region" check_store "${SLOT}" \
  's|^          type: s3$|          type: filesystem|'
mutant "a follower root applies the provider layer" check_list clusters/_template/root/multi-region-follower-secondary/kustomization.yaml \
  '/name: infrastructure-providers/d'
mutant "the list leaves the provider layer" check_list clusters/_template/infrastructure/providers/base/kustomization.yaml \
  '/^  - opentofu-providers.yaml$/d'

echo
if [ "${FAILURES}" -gt 0 ]; then echo "[follower-packages] FAILED: ${FAILURES}"; exit 1; fi
echo "[follower-packages] PASS"
