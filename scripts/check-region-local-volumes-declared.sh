#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# ADR-0015 D6 CLASS GATE — every PVC a multi-region chart renders is DECLARED
# in its blueprint.yaml spec.regionLocalVolumes, with a replication mechanism.
# ─────────────────────────────────────────────────────────────────────────────
#
# THE DEFECT THIS EXISTS TO PREVENT
# ---------------------------------
# bp-wordpress-tenant kept uploaded media on a region-local `wp-content` PVC.
# Under a multi-region placement each region's release binds its OWN PVC and
# nothing copies it, so after a primary move the new primary served posts
# whose media existed only in the old region. ADR-0015 D6: region-local
# volumes are declared and replicated, or the Blueprint is refused for
# multi-region placement. The refusal itself (catalyst-api 422 +
# application-controller Ready=False, package core/controllers/pkg/regionlocal)
# can only read what the Blueprint DECLARES — a chart that renders a PVC the
# blueprint never mentions slips past it. This gate closes that gap.
#
# WHAT IT CHECKS
# --------------
# Charts with multi-region support = every Blueprint folder whose chart
# depends on `topology-lib` (the ADR-0015 D2 `openova.topology` contract — one
# HelmRelease per region), plus any blueprint that already declares
# spec.regionLocalVolumes. Each is rendered three ways: singleton (contract
# absent), multi-region self=primary, multi-region self=standby. In every
# render:
#   V1  every PersistentVolumeClaim and every StatefulSet volumeClaimTemplate
#       carries the label `openova.io/volume-name`, and its value names a
#       spec.regionLocalVolumes entry;
#   V2  in a MULTI-REGION render no such PVC belongs to an entry whose
#       replication is `none`/unset/unknown (the admission gate would refuse
#       the placement; a chart that renders it anyway contradicts itself);
#   V3  every entry's `template:` (when given) is a file that exists.
# A render error FAILS (register the chart's required values below) — an
# unrendered chart is unchecked, never "clean".
#
# Usage:
#   bash scripts/check-region-local-volumes-declared.sh             # the repo
#   bash scripts/check-region-local-volumes-declared.sh --self-test # fixtures
# Renders templates locally only — no cluster, no cloud API.
# Refs #7067
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"

# Per-Blueprint render args (values a chart REQUIRES to render at all).
render_args() {
  case "$1" in
    platform/wordpress-tenant)
      echo "--set smeDomain=a.example --set adminUser.email=a@b.c --set database.topology.promotion.mechanism=manual --api-versions postgresql.cnpg.io/v1 --api-versions cilium.io/v2" ;;
    # ADR-0015 D8 platform pairs: default-off charts; the region pins are the
    # bootstrap-kit slot's cloud-init values (16b / 16c+16d).
    platform/cnpg-pair)
      echo "--set cnpgPair.enabled=true --set cnpgPair.primary.region=$A --set cnpgPair.replica.region=$B --set cnpgPair.image.tag=16.4-1" ;;
    platform/postgres)
      echo "--set enabled=true --set topology.primary.region=$A --set topology.replica.region=$B --api-versions postgresql.cnpg.io/v1" ;;
    *) echo "" ;;
  esac
}

cat > "$TMP/check.py" <<'PYEOF'
import os, sys, yaml
render, bpfile, multi = sys.argv[1], sys.argv[2], sys.argv[3] == "multi"
OK = {"s3-replicated", "volume-replication", "none-needed"}
bp = yaml.safe_load(open(bpfile)) or {}
decl = {}
errs = []
for e in ((bp.get("spec") or {}).get("regionLocalVolumes") or []):
    if not isinstance(e, dict) or not e.get("name"):
        errs.append(f"V1 malformed regionLocalVolumes entry: {e!r}")
        continue
    decl[e["name"]] = str(e.get("replication") or "").strip().lower()
    t = e.get("template")
    if t and not os.path.isfile(os.path.join(os.path.dirname(bpfile), t)):
        errs.append(f"V3 regionLocalVolumes[{e['name']}].template {t!r} does not exist")
docs = [d for d in yaml.safe_load_all(open(render)) if isinstance(d, dict)]
claims = []
for d in docs:
    md = d.get("metadata") or {}
    if d.get("kind") == "PersistentVolumeClaim":
        claims.append((f"PersistentVolumeClaim/{md.get('name')}", md.get("labels") or {}))
    if d.get("kind") == "StatefulSet":
        for vct in ((d.get("spec") or {}).get("volumeClaimTemplates") or []):
            vmd = vct.get("metadata") or {}
            claims.append((f"StatefulSet/{md.get('name')} volumeClaimTemplate {vmd.get('name')}", vmd.get("labels") or {}))
for what, labels in claims:
    name = labels.get("openova.io/volume-name")
    if not name:
        errs.append(f"V1 {what} has no `openova.io/volume-name` label — declare it in spec.regionLocalVolumes and label the claim")
    elif name not in decl:
        errs.append(f"V1 {what} is volume {name!r}, which spec.regionLocalVolumes does not declare (declared: {sorted(decl)})")
    elif multi and decl[name] not in OK:
        errs.append(f"V2 {what} renders in a MULTI-REGION render but volume {name!r} declares replication {decl[name] or '<unset>'!r} — a primary move strands it")
if not docs:
    errs.append("render is EMPTY — nothing was checked")
for e in errs:
    print("        " + e)
print(f"        ({len(claims)} claim(s) checked)")
sys.exit(1 if errs else 0)
PYEOF

cat > "$TMP/discover.py" <<'PYEOF'
import glob, os, sys, yaml
root = sys.argv[1]
for bpf in sorted(glob.glob(os.path.join(root, "platform/*/blueprint.yaml")) + glob.glob(os.path.join(root, "products/*/blueprint.yaml"))):
    d = os.path.dirname(bpf)
    chart = os.path.join(d, "chart")
    if not os.path.isfile(os.path.join(chart, "Chart.yaml")):
        continue
    try:
        c = yaml.safe_load(open(os.path.join(chart, "Chart.yaml"))) or {}
        b = yaml.safe_load(open(bpf)) or {}
    except Exception as e:
        print(f"ERR {os.path.relpath(d, root)} {e}")
        continue
    deps = {x.get("name") for x in (c.get("dependencies") or []) if isinstance(x, dict)}
    if c.get("type") == "library":
        continue
    if "topology-lib" in deps or (b.get("spec") or {}).get("regionLocalVolumes"):
        print(os.path.relpath(d, root))
PYEOF

fails=0
checked=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

# Extra single-region-only variants (label suffix|helm args) — shapes a chart
# can only render in one region (e.g. a legacy PVC mode the chart itself
# refuses under a multi-region topology), checked so their PVCs stay declared.
render_variants() {
  case "$1" in
    platform/wordpress-tenant) echo "media.store=pvc|--set media.store=pvc" ;;
  esac
}

# check_chart <label> <blueprint-dir> <extra helm args string> [modes]
check_chart() {
  local label="$1" dir="$2" extra="$3" modes="${4:-single primary standby}"
  local chart="$dir/chart" bpf="$dir/blueprint.yaml"
  if grep -q '^dependencies:' "$chart/Chart.yaml" 2>/dev/null; then
    (cd "$chart" && helm dependency build >/dev/null 2>&1) || true
  fi
  local mode
  for mode in $modes; do
    local out="$TMP/$(echo "$label-$mode" | tr -c 'a-zA-Z0-9' '_').yaml" kind=single
    # shellcheck disable=SC2086
    case "$mode" in
      single)  helm template rlv "$chart" --namespace rlv $extra > "$out" 2> "$out.err" ;;
      primary) kind=multi; helm template rlv "$chart" --namespace rlv $extra --set openova.topology.mode=active-passive \
                 --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" > "$out" 2> "$out.err" ;;
      standby) kind=multi; helm template rlv "$chart" --namespace rlv $extra --set openova.topology.mode=active-passive \
                 --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}" > "$out" 2> "$out.err" ;;
    esac
    if [ $? -ne 0 ]; then
      fail "$label [$mode]: render errored: $(grep -v '^\s*$' "$out.err" | tail -1)"
      continue
    fi
    checked=$((checked + 1))
    if python3 "$TMP/check.py" "$out" "$bpf" "$kind"; then
      pass "$label [$mode]: every PVC is a declared, replicated region-local volume"
    else
      fail "$label [$mode]: undeclared / unreplicated region-local volume"
    fi
  done
}

self_test() {
  echo "══ self-test (fixture charts) ══"
  local F="$TMP/fixtures"
  mkfix() { # <name> <pvc-label|-> <decl-yaml>
    local d="$F/$1"
    mkdir -p "$d/chart/templates"
    printf 'apiVersion: v2\nname: %s\nversion: 0.1.0\n' "$1" > "$d/chart/Chart.yaml"
    printf 'openova:\n  topology: {}\n' > "$d/chart/values.yaml"
    {
      echo 'apiVersion: v1'
      echo 'kind: PersistentVolumeClaim'
      echo 'metadata:'
      echo '  name: data'
      if [ "$2" != "-" ]; then echo '  labels:'; echo "    openova.io/volume-name: $2"; fi
      echo 'spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}'
    } > "$d/chart/templates/pvc.yaml"
    printf 'apiVersion: catalyst.openova.io/v1alpha1\nkind: Blueprint\nmetadata: {name: bp-%s}\nspec:\n  version: 0.1.0\n%s\n' "$1" "$3" > "$d/blueprint.yaml"
  }
  mkfix good data '  regionLocalVolumes:
    - {name: data, template: chart/templates/pvc.yaml, replication: s3-replicated}'
  mkfix unlabelled - '  regionLocalVolumes:
    - {name: data, replication: s3-replicated}'
  mkfix undeclared data '  regionLocalVolumes:
    - {name: other, replication: s3-replicated}'
  mkfix unreplicated data '  regionLocalVolumes:
    - {name: data, replication: none}'
  mkfix badtemplate data '  regionLocalVolumes:
    - {name: data, template: chart/templates/missing.yaml, replication: s3-replicated}'
  local before
  before=$fails; check_chart fixture-good "$F/good" "" >/dev/null
  if [ "$fails" -eq "$before" ]; then pass "self-test: a declared, replicated, labelled PVC passes"; else fails=$before; fail "self-test: the good fixture was rejected"; fi
  local f
  for f in unlabelled undeclared unreplicated badtemplate; do
    before=$fails; check_chart "fixture-$f" "$F/$f" "" >/dev/null
    if [ "$fails" -gt "$before" ]; then fails=$before; pass "self-test: the $f fixture is rejected"
    else fail "self-test: VACUITY — the $f fixture passed"; fi
  done
  # Discovery must find a topology-lib consumer (bp-wordpress-tenant today).
  if python3 "$TMP/discover.py" "$REPO_ROOT" | grep -qx 'platform/wordpress-tenant'; then
    pass "self-test: discovery finds the topology-lib consumer platform/wordpress-tenant"
  else
    fail "self-test: discovery does not find platform/wordpress-tenant"
  fi
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  echo
  if [ "$fails" -gt 0 ]; then echo "FAIL: $fails self-test assertion(s)"; exit 1; fi
  echo "PASS: self-test (fixtures only, no cluster)"
  exit 0
fi

cd "$REPO_ROOT"
mapfile -t CHARTS < <(python3 "$TMP/discover.py" "$REPO_ROOT")
if [ "${#CHARTS[@]}" -eq 0 ]; then
  echo "FAIL: discovered ZERO multi-region charts — the gate checked nothing"
  exit 1
fi
for c in "${CHARTS[@]}"; do
  case "$c" in ERR*) fail "blueprint/Chart.yaml unparsable: $c"; continue ;; esac
  echo "══ $c ══"
  check_chart "$c" "$c" "$(render_args "$c")"
  while IFS='|' read -r vlabel vargs; do
    [ -n "$vlabel" ] || continue
    check_chart "$c ($vlabel)" "$c" "$(render_args "$c") $vargs" single
  done < <(render_variants "$c")
done
echo
if [ "$fails" -gt 0 ]; then
  echo "FAIL: $fails region-local-volume assertion(s) across ${#CHARTS[@]} chart(s) (ADR-0015 D6)"
  exit 1
fi
echo "PASS: $checked render(s) of ${#CHARTS[@]} multi-region chart(s) — every PVC is a declared, replicated region-local volume"
