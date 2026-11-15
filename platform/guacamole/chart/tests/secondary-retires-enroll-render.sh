#!/usr/bin/env bash
# bp-guacamole — the mesh secondary retires the hooks a pre-mesh revision left (#7922).
#
# THE DEFECT THIS GUARDS
# ──────────────────────────────────────────────────────────────────────────
# Region b of a two-region Sovereign always installs this chart TWICE over:
#   revision 1  crossRegion.enabled=false — slot 52 takes `enabled` from
#               SOVEREIGN_ENABLE_CNPG_PAIR, which the parent flips only after
#               the full ClusterMesh, never at bootstrap. So revision 1 is a
#               full install: webapp, guacd, the guacamole-pg Cluster and the
#               seed hooks, among them the per-minute admin-enroll CronJob.
#   revision 2  crossRegion.enabled=true, role=secondary — the mesh stub only.
# Helm prunes revision 1's release objects (the Cluster goes, and the
# guacamole-pg-app Secret with it) but NEVER deletes a hook object: hooks are
# in no release manifest. So the CronJob stayed and fired every minute into a
# Pod that can never start.
#
# Measured on hw307 region b (16 Nov 2026): release revision 1 values
# crossRegion {enabled: false, role: secondary} carried the hooks ConfigMap
# guacamole-bp-guacamole-jdbc-seed-scripts, Job …-jdbc-seed and CronJob
# …-admin-enroll; revision 2 {enabled: true} carried none; the CronJob created
# by revision 1 at 16:47:41 kept starting Pods that stayed Pending on
#   MountVolume.SetUp failed for volume "cnpg" : secret "guacamole-pg-app" not found
# and acceptance check FND-G01.6 failed ("1 containers have no image digest").
#
# The chart's design puts the CronJob only where its database is (values.yaml
# crossRegion: "role=secondary → NO webapp/guacd/JDBC workloads at all"). The
# repair keeps that, and makes the secondary render carry RETIREMENT hooks
# under the same names: before-hook-creation deletes what revision 1 left,
# hook-succeeded deletes the retirement once the hook phase ends.
#
# WHY NOT "the secondary render has no CronJob"
# ──────────────────────────────────────────────────────────────────────────
# That assertion already PASSES on the defective chart — the secondary render
# carried no CronJob, and the live CronJob came from the EARLIER revision. The
# defect is the transition, so this test renders both revisions from the real
# slot file, exactly as region b's Flux substitutes it, and asserts that every
# long-lived hook of revision 1 is retired by revision 2.
#
# VACUITY
# ──────────────────────────────────────────────────────────────────────────
# Case 1 requires revision 1 to carry the admin-enroll CronJob hook, so a render
# that produces nothing cannot pass the rest. Case 2 fails on origin/main (the
# secondary render carries no hook at all). Case 4 is the paired control: the
# PRIMARY render's CronJob must stay live — not suspended, not hook-succeeded —
# so a chart that always retires cannot pass either.
#
# Cases:
#   1. revision 1 (pre-mesh, region b) renders the long-lived hooks, CronJob included
#   2. revision 2 (secondary) retires each of them: same kind+name, post-upgrade,
#      before-hook-creation + hook-succeeded
#   3. revision 2 runs nothing: every CronJob suspended, no Job, no Deployment
#   4. control — region a (primary) keeps the real, live CronJob
set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
repo_root="$(cd "${chart_dir}/../../.." && pwd)"
slot="${repo_root}/clusters/_template/bootstrap-kit/52-bp-guacamole.yaml"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fails=0
pass() { printf '  ok   — %s\n' "$1"; }
fail() { printf '  FAIL — %s\n' "$1"; fails=$((fails + 1)); }

[ -f "$slot" ] || { echo "FAIL: slot file not found: $slot"; exit 1; }

# slot_values <out> KEY=VALUE... — the bp-guacamole HelmRelease's spec.values
# from the slot file after Flux-style postBuild substitution of ${VAR} /
# ${VAR:=default} / ${VAR:-default} over the TEXT (which is what Flux does).
slot_values() {
  local out="$1"; shift
  python3 - "$slot" "$out" "$@" <<'PY'
import re, sys, yaml
slot, out, *pairs = sys.argv[1:]
env = dict(p.split("=", 1) for p in pairs)
def sub(m):
    name, default = m.group(1), m.group(3)
    if name in env:
        return env[name]
    return default if default is not None else ""
text = re.sub(r"\$\{([A-Za-z_][A-Za-z0-9_]*)(:[=-]([^}]*))?\}", sub, open(slot).read())
for doc in yaml.safe_load_all(text):
    if doc and doc.get("kind") == "HelmRelease" and doc["metadata"]["name"] == "bp-guacamole":
        yaml.safe_dump(doc["spec"]["values"], open(out, "w"))
        break
else:
    raise SystemExit("no bp-guacamole HelmRelease in " + slot)
PY
}

render() {  # render <values-file> <out>
  "$helm" template guacamole "$chart_dir" -n guacamole -f "$1" \
    --api-versions cilium.io/v2 --api-versions postgresql.cnpg.io/v1 >"$2" 2>"$2.err" \
    || { echo "helm template failed:"; cat "$2.err"; exit 1; }
}

common=(SOVEREIGN_FQDN=t1.omani.works SOVEREIGN_CNPG_STORAGE_CLASS=evs-ssd)
# Region b before the parent's mesh flip, and after it.
slot_values "${tmp}/b-rev1.values" "${common[@]}" SOVEREIGN_REGION_ROLE=secondary \
  SOVEREIGN_ENABLE_CNPG_PAIR=false 'SOVEREIGN_PEER_CLUSTERMESH_NAMES=[]'
slot_values "${tmp}/b-rev2.values" "${common[@]}" SOVEREIGN_REGION_ROLE=secondary \
  SOVEREIGN_ENABLE_CNPG_PAIR=true 'SOVEREIGN_PEER_CLUSTERMESH_NAMES=[t1-mesh-a]'
# Region a after the flip (the control).
slot_values "${tmp}/a.values" "${common[@]}" SOVEREIGN_REGION_ROLE=primary \
  SOVEREIGN_ENABLE_CNPG_PAIR=true 'SOVEREIGN_PEER_CLUSTERMESH_NAMES=[t1-mesh-b]'
render "${tmp}/b-rev1.values" "${tmp}/b-rev1.yaml"
render "${tmp}/b-rev2.values" "${tmp}/b-rev2.yaml"
render "${tmp}/a.values" "${tmp}/a.yaml"

echo "[bp-guacamole] secondary retires the pre-mesh hooks (#7922)"
python3 - "${tmp}/b-rev1.yaml" "${tmp}/b-rev2.yaml" "${tmp}/a.yaml" >"${tmp}/verdict" <<'PY'
import sys, yaml

def docs(path):
    return [d for d in yaml.safe_load_all(open(path)) if d]

def ann(d):
    return (d.get("metadata") or {}).get("annotations") or {}

def hook_events(d):
    return {e.strip() for e in (ann(d).get("helm.sh/hook") or "").split(",") if e.strip()}

def delete_policies(d):
    return {p.strip() for p in (ann(d).get("helm.sh/hook-delete-policy") or "").split(",") if p.strip()}

def key(d):
    return (d["kind"], d["metadata"]["name"])

rev1, rev2, prim = docs(sys.argv[1]), docs(sys.argv[2]), docs(sys.argv[3])
# A Job or Pod hook is awaited and carries its own TTL; the objects Helm can
# orphan for good are the hooks that are simply created and then live on.
lived = sorted(key(d) for d in rev1 if hook_events(d) and d["kind"] not in ("Job", "Pod"))

def out(ok, msg):
    print(("PASS " if ok else "FAIL ") + msg)

out(("CronJob", "guacamole-bp-guacamole-admin-enroll") in lived,
    "case 1: revision 1 of region b renders the long-lived hooks %s" % lived)

by_key = {key(d): d for d in rev2 if hook_events(d)}
for k in lived:
    d = by_key.get(k)
    if d is None:
        out(False, "case 2: revision 2 of region b carries no hook %s/%s — Helm leaves revision 1's object in place forever" % k)
        continue
    ev, pol = hook_events(d), delete_policies(d)
    out("post-upgrade" in ev and {"before-hook-creation", "hook-succeeded"} <= pol,
        "case 2: %s/%s is retired on upgrade (hook=%s, delete-policy=%s)" % (k + (sorted(ev), sorted(pol))))

crons = [d for d in rev2 if d["kind"] == "CronJob"]
live = [d["metadata"]["name"] for d in crons if not (d.get("spec") or {}).get("suspend")]
out(not live, "case 3: no CronJob of region b's secondary render can start a Job (unsuspended: %s)" % live)
runners = [key(d) for d in rev2 if d["kind"] in ("Job", "Pod", "Deployment", "StatefulSet")]
out(not runners, "case 3: region b's secondary render runs no workload (found: %s)" % runners)

real = [d for d in prim if key(d) == ("CronJob", "guacamole-bp-guacamole-admin-enroll")]
out(len(real) == 1 and not (real[0].get("spec") or {}).get("suspend")
    and "hook-succeeded" not in delete_policies(real[0]),
    "case 4 (control): region a keeps one live admin-enroll CronJob, not suspended, not deleted on success")
PY
while IFS= read -r line; do
  case "$line" in
    PASS\ *) pass "${line#PASS }" ;;
    *)       fail "${line#FAIL }" ;;
  esac
done <"${tmp}/verdict"
[ -s "${tmp}/verdict" ] || fail "the verdict script printed nothing — nothing was asserted"

if [ "$fails" -ne 0 ]; then
  echo "[bp-guacamole] ${fails} failure(s) — region b keeps an admin-enroll CronJob it can never run (#7922)"
  exit 1
fi
echo "[bp-guacamole] secondary-retires-enroll: ALL PASS"
