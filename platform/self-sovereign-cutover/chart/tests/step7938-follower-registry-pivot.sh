#!/usr/bin/env bash
# #7938 — cutover step 04 (registry-pivot) pivots every node of EVERY region.
#
# Measured on hw307 (18 Nov 2026, gate runs 16 and 17): region b had no
# Sovereign object (NET-G08, SOV-G17). Its writer pod could not pull
# ghcr.io/openova-io/openova/sovereign-ops:0b1157d: step 03 had copied that image
# into the Sovereign's registry, but region b's nodes still sent ghcr.io to the
# mirror their first boot wrote. Step 04 is a DaemonSet the chart installs where
# it runs, the primary region; the follower's cutover release is suspended, and
# no leg of the chain ran step 04 there. Region a: six nodes acked v2; region b:
# no registry-pivot DaemonSet, no ack.
#
# Evidence, each part able to fail:
#   (a) the two-region render carries step 04 for the followers: the DaemonSet
#       the primary runs (the same pod, but for its account and status
#       ConfigMap), a ServiceAccount, a role bound in every namespace pivot.sh
#       calls, and a status ConfigMap that asks for v2; the single-region render
#       carries none of it;
#   (b) step 03 mounts that ConfigMap and the primary's pivot.sh, and runs the
#       follower leg last, right after its run guard, failing with it;
#   (c) EXECUTION of the rendered follower-registry-pivot.sh against a model of
#       two follower regions (a stub kubectl whose DaemonSet acks v2 on each node
#       once its objects are applied): both regions get the render's DaemonSet
#       and pivot.sh byte for byte, every Ready node acks, a re-run is idempotent;
#       a node that never acks, an API server that refuses and a NotReady node are
#       each named; no follower kubeconfig is a no-op;
#   (d) REMOVING A REGION TURNS IT RED: the same verdict over regions b and c,
#       with region c's kubeconfig gone, reports region c unpivoted; and with the
#       follower call taken out of the rendered step 03 (the tree before the
#       change) no region outside the primary is pivoted.
#
# Usage: bash tests/step7938-follower-registry-pivot.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
NS="catalyst"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --namespace "${NS}" --set sovereign.fqdn="${FQDN}" --set secondaryRegions.replicaRegion=region-b \
  >"${TMP}/two.yaml" 2>"${TMP}/render.err" || { echo "helm template (two regions) failed:"; cat "${TMP}/render.err"; exit 1; }
helm template ssc "${CHART_DIR}" --namespace "${NS}" --set sovereign.fqdn="${FQDN}" \
  >"${TMP}/one.yaml" 2>"${TMP}/render.err" || { echo "helm template (one region) failed:"; cat "${TMP}/render.err"; exit 1; }

echo "[step7938-pivot] (a) the two-region render carries step 04 for every follower; the single-region render none of it"
python3 - "${TMP}" <<'PY' >"${TMP}/a.out" 2>&1
import json, sys, yaml
t = sys.argv[1]
def docs(p): return [d for d in yaml.safe_load_all(open(p)) if d]
two, one = docs(t + "/two.yaml"), docs(t + "/one.yaml")
def find(ds, kind, name): return next((d for d in ds if d.get("kind") == kind and d["metadata"]["name"] == name), None)
bad = []
prim = find(two, "DaemonSet", "registry-pivot")
cm = find(two, "ConfigMap", "cutover-follower-registry-pivot")
script = find(two, "ConfigMap", "registry-pivot-script")
s03 = find(two, "ConfigMap", "cutover-step-03-harbor-prewarm")
if not (prim and cm and script and s03):
    print("FAIL the two-region render lacks the primary DaemonSet, cutover-follower-registry-pivot, registry-pivot-script or step 03"); sys.exit(0)
open(t + "/follower.sh", "w").write(cm["data"]["follower-registry-pivot.sh"])
open(t + "/objects.yaml", "w").write(cm["data"]["objects.yaml"])
open(t + "/pivot.sh", "w").write(script["data"]["pivot.sh"])
open(t + "/s03.sh", "w").write(s03["data"]["run.sh"])
open(t + "/s03-pod.yaml", "w").write(s03["data"]["podSpec"])
json.dump(prim, open(t + "/primary-ds.json", "w"))
objs = docs(t + "/objects.yaml")
fds = [d for d in objs if d.get("kind") == "DaemonSet"]
if len(fds) != 1: bad.append("objects.yaml holds %d DaemonSets, want 1" % len(fds))
else:
    f = fds[0]
    env = lambda d: {e["name"]: e.get("value") for e in d["spec"]["template"]["spec"]["containers"][0]["env"] if "value" in e}
    fe, pe = env(f), env(prim)
    sa = f["spec"]["template"]["spec"]["serviceAccountName"]
    st = fe.get("STATUS_CONFIGMAP_NAME")
    a, b = json.loads(json.dumps(f)), json.loads(json.dumps(prim))
    for d in (a, b):
        d["spec"]["template"]["spec"].pop("serviceAccountName")
        for e in d["spec"]["template"]["spec"]["containers"][0]["env"]:
            if e["name"] == "STATUS_CONFIGMAP_NAME": e["value"] = "X"
    if a != b: bad.append("the follower DaemonSet differs from the primary's beyond its account and status ConfigMap")
    if st == pe.get("STATUS_CONFIGMAP_NAME"): bad.append("the follower DaemonSet writes the primary's status ConfigMap name")
    if not find(objs, "ServiceAccount", sa): bad.append("no ServiceAccount %s for the follower DaemonSet" % sa)
    scm = find(objs, "ConfigMap", st)
    if not scm or (scm.get("data") or {}).get("registriesYamlActive") != "v2": bad.append("the status ConfigMap %s does not ask for v2" % st)
    rbs = [d for d in objs if d.get("kind") == "RoleBinding"]
    for key in ("STATUS_CONFIGMAP_NAMESPACE", "GATEWAY_LB_SVC_NAMESPACE", "HARBOR_CA_SRC_NAMESPACE", "DF_NAMESPACE"):
        ns = fe.get(key)
        if not ns: continue
        if not any(r["metadata"]["namespace"] == ns and any(s["name"] == sa for s in r["subjects"]) for r in rbs):
            bad.append("no RoleBinding of %s in namespace %s (%s), which pivot.sh calls" % (sa, ns, key))
    cr = [d for d in objs if d.get("kind") == "ClusterRole"]
    have = {(g, r, v) for c in cr for rule in c["rules"] for g in rule["apiGroups"] for r in rule["resources"] for v in rule["verbs"]}
    for need in [("", "configmaps", "get"), ("", "configmaps", "patch"), ("", "services", "get"), ("", "secrets", "get"), ("", "secrets", "create"),
                 ("", "secrets", "patch"), ("", "secrets", "delete"), ("apps", "daemonsets", "patch"), ("apps", "statefulsets", "patch")]:
        if need not in have: bad.append("the follower role lacks %s" % (need,))
    for r in rbs:
        if r["roleRef"]["name"] not in [c["metadata"]["name"] for c in cr]: bad.append("RoleBinding %s names a role objects.yaml lacks" % r["metadata"]["name"])
    open(t + "/status-name", "w").write(st)
for n in ("cutover-follower-registry-pivot",):
    if find(one, "ConfigMap", n): bad.append("the single-region render carries " + n)
s1 = find(one, "ConfigMap", "cutover-step-03-harbor-prewarm")
if s1 and "follower-registry-pivot" in s1["data"]["run.sh"] + s1["data"]["podSpec"]: bad.append("the single-region step 03 names the follower leg")
for b in bad: print("FAIL " + b)
print("DONE")
PY
grep -q '^DONE$' "${TMP}/a.out" || { echo "  the render check did not run:"; cat "${TMP}/a.out"; exit 1; }
if grep -q '^FAIL ' "${TMP}/a.out"; then
  while IFS= read -r l; do fail "${l#FAIL }"; done < <(grep '^FAIL ' "${TMP}/a.out")
else
  pass "the follower DaemonSet is the primary's pod (but for its account and status ConfigMap), with its account, a role bound in each namespace pivot.sh calls, and v2 asked"
  pass "the single-region render carries none of it"
fi
[ -s "${TMP}/follower.sh" ] || { echo "cannot go on without the rendered follower script"; exit 1; }

echo "[step7938-pivot] (b) step 03 mounts the follower objects and pivot.sh, and runs the follower leg last, after its run guard"
tail2=$(grep -v '^[[:space:]]*$' "${TMP}/s03.sh" | tail -n 2)
[ "$(printf '%s\n' "${tail2}" | sed -n 1p)" = 'prewarm_run_guard /tmp/prewarm-run-guard "${run_images_file}" || exit 1' ] \
  && [ "$(printf '%s\n' "${tail2}" | sed -n 2p)" = 'sh /follower-registry-pivot/follower-registry-pivot.sh || exit 1' ] \
  && pass "the run guard, then the follower leg, each failing the step" || fail "step 03 does not end with the guard and the follower leg: $(printf '%s' "${tail2}" | tr '\n' '|')"
python3 - "${TMP}/s03-pod.yaml" <<'PY' >"${TMP}/b.out" 2>&1
import sys, yaml
p = yaml.safe_load(open(sys.argv[1]))
vols = {v["name"]: v for v in p.get("volumes", [])}
mounts = {m["mountPath"]: m["name"] for m in p["containers"][0].get("volumeMounts", [])}
for path, cm in (("/follower-registry-pivot", "cutover-follower-registry-pivot"), ("/registry-pivot-script", "registry-pivot-script")):
    v = vols.get(mounts.get(path, ""), {})
    print("ok" if (v.get("configMap") or {}).get("name") == cm else "FAIL %s is not ConfigMap %s" % (path, cm))
PY
grep -q FAIL "${TMP}/b.out" && fail "$(grep FAIL "${TMP}/b.out" | tr '\n' ' ')" || pass "the follower objects and the primary's pivot.sh are mounted where the leg reads them"

# ── the model: a stub kubectl over per-region state ──
BIN="${TMP}/bin"; ST="${TMP}/state"; KC="${TMP}/kc"; mkdir -p "${BIN}" "${ST}" "${KC}"
cat >"${TMP}/split.py" <<'PY'
import json, os, sys, yaml
src, d = sys.argv[1], sys.argv[2]
for doc in yaml.safe_load_all(open(src)):
    if doc:
        json.dump(doc, open(os.path.join(d, "%s.%s.json" % (doc["kind"], doc["metadata"]["name"])), "w"))
PY
cat >"${TMP}/status.py" <<'PY'
import json, os, sys
d, name = sys.argv[1], sys.argv[2]
p = os.path.join(d, "ConfigMap.%s.json" % name)
if not os.path.exists(p): sys.exit(1)
cm = json.load(open(p)); data = dict(cm.get("data") or {})
runs = all(os.path.exists(os.path.join(d, f)) for f in ("DaemonSet.registry-pivot.json", "ConfigMap.registry-pivot-script.json"))
if runs and data.get("registriesYamlActive") == "v2":
    noack = open(os.path.join(d, "no-ack")).read().split() if os.path.exists(os.path.join(d, "no-ack")) else []
    for n in json.load(open(os.path.join(d, "nodes.json")))["items"]:
        if n["metadata"]["name"] not in noack: data["node.%s.registriesYaml" % n["metadata"]["name"]] = "v2"
print(json.dumps({"data": data}))
PY
cat >"${BIN}/kubectl" <<'SH'
#!/usr/bin/env bash
kc=""; [ "${1:-}" = --kubeconfig ] && { kc="$2"; shift 2; }
r=$(basename "${kc:-primary}" .yaml); d="${STUB_STATE}/${r}"; mkdir -p "${d}"
echo "${r} $*" >>"${STUB_STATE}/calls"
ns=""; verb=""; args=()
while [ $# -gt 0 ]; do case "$1" in -n) ns="$2"; shift 2 ;; *) args+=("$1"); shift ;; esac; done
case "${args[0]}" in
  apply)
    [ -e "${d}/refuse" ] && { echo 'Error from server (Forbidden): daemonsets.apps is forbidden: User "x" cannot create resource'; exit 1; }
    f=""; for i in "${!args[@]}"; do [ "${args[$i]}" = -f ] && f="${args[$((i+1))]}"; done
    python3 "${STUB_TMP}/split.py" "${f}" "${d}" ;;
  get)
    case "${args[1]}" in
      nodes) cat "${d}/nodes.json" ;;
      configmap) python3 "${STUB_TMP}/status.py" "${d}" "${args[2]}" ;;
      *) exit 9 ;;
    esac ;;
  *) echo "unexpected kubectl ${args[*]}" >&2; exit 9 ;;
esac
SH
chmod +x "${BIN}/kubectl"
nodes() { python3 -c 'import json,sys; print(json.dumps({"items":[{"metadata":{"name":n.split(":")[0]},"status":{"conditions":[{"type":"Ready","status":"False" if n.endswith(":notready") else "True"}]}} for n in sys.argv[1:]]}))' "$@"; }
region() { mkdir -p "${ST}/$1"; : >"${KC}/$1.yaml"; shift_r="$1"; shift; nodes "$@" >"${ST}/${shift_r}/nodes.json"; }
leg() { # leg <name> [env...]: the rendered follower script, once; exit status in <name>.rc
  ( export PATH="${BIN}:${PATH}" STUB_STATE="${ST}" STUB_TMP="${TMP}" RELEASE_NAMESPACE="${NS}" SECONDARY_KUBECONFIGS="${KC}" \
      FOLLOWER_PIVOT_DIR="${TMP}/fdir" PIVOT_SCRIPT="${TMP}/pivot.sh" FOLLOWER_PIVOT_WORK="${TMP}/work-$1" FOLLOWER_PIVOT_POLL_SECONDS=0 FOLLOWER_PIVOT_WAIT_SECONDS=3 \
      FOLLOWER_PIVOT_STATUS_CM="$(cat "${TMP}/status-name")"
    shift; for e in "$@"; do export "${e?}"; done
    sh "${TMP}/fdir/follower-registry-pivot.sh" ) >"${TMP}/$1.out" 2>&1
  echo $? >"${TMP}/$1.rc"
}
mkdir -p "${TMP}/fdir"; cp "${TMP}/follower.sh" "${TMP}/fdir/follower-registry-pivot.sh"; cp "${TMP}/objects.yaml" "${TMP}/fdir/objects.yaml"
# pivoted <region>: the region runs the render's follower DaemonSet with the primary's pivot.sh, and every Ready node acked v2.
pivoted() {
  python3 - "${ST}/$1" "${TMP}" "$(cat "${TMP}/status-name")" <<'PY'
import json, os, subprocess, sys, yaml
d, t, st = sys.argv[1:4]
want = next(x for x in yaml.safe_load_all(open(t + "/objects.yaml")) if x and x["kind"] == "DaemonSet")
p = os.path.join(d, "DaemonSet.registry-pivot.json")
if not os.path.exists(p) or json.load(open(p)) != want: sys.exit(1)
s = os.path.join(d, "ConfigMap.registry-pivot-script.json")
if not os.path.exists(s) or json.load(open(s))["data"]["pivot.sh"] != open(t + "/pivot.sh").read(): sys.exit(2)
out = subprocess.run([sys.executable, t + "/status.py", d, st], stdout=subprocess.PIPE, text=True)
acks = json.loads(out.stdout or "{}").get("data", {}) if out.returncode == 0 else {}
for n in json.load(open(os.path.join(d, "nodes.json")))["items"]:
    ready = any(c["type"] == "Ready" and c["status"] == "True" for c in n["status"]["conditions"])
    if ready and acks.get("node.%s.registriesYaml" % n["metadata"]["name"]) != "v2": sys.exit(3)
PY
}
every_region_pivoted() { local r bad=""; for r in "$@"; do pivoted "${r}" || bad="${bad} ${r}"; done; [ -z "${bad}" ] || { echo "not pivoted:${bad}"; return 1; }; }

echo "[step7938-pivot] (c) the rendered follower leg pivots every node of every follower region"
region region-b b-w1 b-w2 b-cp1
region region-c c-w1 c-w2
leg two
[ "$(cat "${TMP}/two.rc")" = 0 ] && pass "the leg ends 0 over two follower regions" || fail "the leg failed: $(tail -3 "${TMP}/two.out" | tr '\n' ' ')"
every_region_pivoted region-b region-c >"${TMP}/v.out" && pass "regions b and c run the render's DaemonSet with the primary's pivot.sh byte for byte, and all 5 nodes ack v2" || fail "$(cat "${TMP}/v.out")"
grep -q 'region region-b: every Ready node acks v2 (3 node(s))' "${TMP}/two.out" && grep -q 'region region-c: every Ready node acks v2 (2 node(s))' "${TMP}/two.out" \
  && pass "the Job log says so per region, with its node count" || fail "the Job log does not count the acked nodes per region: $(grep region "${TMP}/two.out" | tr '\n' ' ')"
leg again
[ "$(cat "${TMP}/again.rc")" = 0 ] && every_region_pivoted region-b region-c >/dev/null && pass "a second run (the next delivery) is idempotent" || fail "the re-run failed: $(tail -2 "${TMP}/again.out")"

echo "[step7938-pivot] (c) a node that never acks, a refusing API server and a NotReady node are each named"
region region-d d-w1 d-w2; echo d-w2 >"${ST}/region-d/no-ack"
leg noack FOLLOWER_PIVOT_WAIT_SECONDS=0
[ "$(cat "${TMP}/noack.rc")" != 0 ] && grep -q 'FATAL: region region-d: .* d-w2' "${TMP}/noack.out" && ! grep -q 'FATAL: region region-d: .* d-w1' "${TMP}/noack.out" \
  && pass "a node without the v2 ack fails the leg, named with its region (and only that node)" || fail "a node that never acked did not fail the leg by name: $(grep -m1 region-d "${TMP}/noack.out")"
rm -f "${ST}/region-d/no-ack"; touch "${ST}/region-d/refuse"
leg refuse
[ "$(cat "${TMP}/refuse.rc")" != 0 ] && grep -q "FATAL: region region-d: the API server refused step 04's objects: .*forbidden" "${TMP}/refuse.out" \
  && pass "a refused apply fails the leg with the API server's answer" || fail "a refused apply did not fail by name: $(grep -m1 region-d "${TMP}/refuse.out")"
every_region_pivoted region-b region-c >/dev/null && pass "the other regions are still pivoted (one region's failure does not stop the others)" || fail "one region's failure left another unpivoted"
rm -rf "${ST}/region-d" "${KC}/region-d.yaml"
region region-e e-w1 e-w2:notready
leg notready
[ "$(cat "${TMP}/notready.rc")" = 0 ] && grep -q 'WARNING: region region-e: node(s) not Ready, pivoted by the DaemonSet when they return: e-w2' "${TMP}/notready.out" \
  && pass "a NotReady node is a WARNING naming it; the Ready ones are waited for" || fail "a NotReady node: rc $(cat "${TMP}/notready.rc"): $(grep region-e "${TMP}/notready.out" | tr '\n' ' ')"
rm -rf "${ST}/region-e" "${KC}/region-e.yaml"
mkdir -p "${TMP}/nokc"; : >"${ST}/calls"
leg none SECONDARY_KUBECONFIGS="${TMP}/nokc"
[ "$(cat "${TMP}/none.rc")" = 0 ] && [ ! -s "${ST}/calls" ] && pass "no follower kubeconfig: exit 0, kubectl never called" || fail "a single-region Sovereign: rc $(cat "${TMP}/none.rc"), calls $(wc -l <"${ST}/calls")"

echo "[step7938-pivot] (d) removing a region turns it red"
rm -rf "${ST}" && mkdir -p "${ST}" && rm -f "${KC}"/*.yaml
region region-b b-w1 b-w2; region region-c c-w1
rm -f "${KC}/region-c.yaml"
leg removed
if every_region_pivoted region-b region-c >"${TMP}/v2.out"; then
  fail "region c's kubeconfig removed, and the verdict still says every region is pivoted — it cannot fail"
else
  grep -qx 'not pivoted: region-c' "${TMP}/v2.out" && pass "the verdict names region c, and only region c, unpivoted" || fail "the verdict: $(cat "${TMP}/v2.out")"
fi
# tail_of <script>: what step 03 runs after its run guard, the leg's path pointed at this test's copy.
tail_of() { awk 'f {print} /^prewarm_run_guard \/tmp\/prewarm-run-guard / {f=1}' "$1" | sed "s#/follower-registry-pivot/#${TMP}/fdir/#g"; }
run_tail() { # run_tail <name> <script>: the tail of a rendered step 03, executed
  tail_of "$2" >"${TMP}/$1.tail.sh"
  ( export PATH="${BIN}:${PATH}" STUB_STATE="${ST}" STUB_TMP="${TMP}" RELEASE_NAMESPACE="${NS}" SECONDARY_KUBECONFIGS="${KC}" \
      FOLLOWER_PIVOT_DIR="${TMP}/fdir" PIVOT_SCRIPT="${TMP}/pivot.sh" FOLLOWER_PIVOT_WORK="${TMP}/work-$1" FOLLOWER_PIVOT_POLL_SECONDS=0 FOLLOWER_PIVOT_WAIT_SECONDS=3 \
      FOLLOWER_PIVOT_STATUS_CM="$(cat "${TMP}/status-name")"
    sh -eu "${TMP}/$1.tail.sh" ) >"${TMP}/$1.out" 2>&1
}
rm -rf "${ST}" && mkdir -p "${ST}" && rm -f "${KC}"/*.yaml; region region-b b-w1; region region-c c-w1
run_tail after "${TMP}/s03.sh"
every_region_pivoted region-b region-c >/dev/null && pass "the tail of the rendered step 03 (after its run guard) pivots both followers" || fail "the rendered step 03's tail does not pivot the followers: $(tail -2 "${TMP}/after.out" | tr '\n' ' ')"
rm -rf "${ST}" && mkdir -p "${ST}"; region region-b b-w1; region region-c c-w1
grep -v -F 'sh /follower-registry-pivot/follower-registry-pivot.sh || exit 1' "${TMP}/s03.sh" >"${TMP}/s03-before.sh"
run_tail before "${TMP}/s03-before.sh"
every_region_pivoted region-b region-c >"${TMP}/v3.out" && fail "control: step 03 without its follower call still reads as pivoting the followers" \
  || pass "control: the same tail without the follower call (the tree before the change) leaves both followers unpivoted, and the verdict names them ($(cat "${TMP}/v3.out"))"

sh -n "${TMP}/fdir/follower-registry-pivot.sh" 2>"${TMP}/syn.err" && pass "the rendered follower script parses as POSIX sh" || fail "the follower script does not parse: $(head -2 "${TMP}/syn.err")"
sh -n "${TMP}/s03.sh" 2>"${TMP}/syn2.err" && pass "the rendered step-03 script parses as POSIX sh" || fail "the step-03 script does not parse: $(head -2 "${TMP}/syn2.err")"

if [ "${FAILURES}" -ne 0 ]; then echo "[step7938-pivot] ${FAILURES} failure(s)"; exit 1; fi
echo "[step7938-pivot] all cases pass"
