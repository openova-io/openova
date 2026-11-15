#!/usr/bin/env bash
# The install hold of kured and its release by the last step of the chain.
#
# bp-kured (bootstrap-kit slot 04b) creates kured's DaemonSet with kured's own
# manual lock: a node restart during the installation cut the Helm installs in
# flight, and the region's only control-plane node went first. The hold stays
# until the hand-over chain is complete. Step egress-block-test, the last step
# (cutover-order 11), releases it past its verdict, in every region, by
# creating the ConfigMap that slot 04b reads as values.
#
# Evidence, each part able to fail:
#   (a) the step is the last of the chain, and the release is called at the two
#       exits with status 0 and nowhere before a failing exit;
#   (b) EXECUTION of the rendered release against a recording kubectl: one
#       create per region (the primary and each mounted kubeconfig), into the
#       namespace, name and key of values.yaml; an existing ConfigMap is left
#       and is no error; a region where the write fails is named in a WARNING
#       and the step still ends with status 0;
#   (c) slot 04b reads exactly that ConfigMap and key, optional;
#   (d) bp-kured rendered with the released values makes kured lock in an
#       annotation that holds nothing, and rendered without them in the one
#       that holds the install hold. Needs the dependency of
#       platform/kured/chart; without network access this part says so and fails.
#
# Usage: bash tests/reboot-hold-release.sh [CHART_DIR]
# REPO_ROOT (env) overrides where clusters/ and platform/kured are read from.
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
REPO_ROOT="${REPO_ROOT:-$(cd "${CHART_DIR}/../../.." && pwd)}"
SLOT="${REPO_ROOT}/clusters/_template/bootstrap-kit/04b-bp-kured.yaml"
KURED="${REPO_ROOT}/platform/kured/chart"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn=t01.omani.works >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# The script of step egress-block-test, and the release function out of it.
python3 - "${TMP}" "${CHART_DIR}" <<'PY' || { echo "  FAIL — the step or its release cannot be read from the render"; exit 1; }
import re, sys, yaml
tmp, chart = sys.argv[1], sys.argv[2]
docs = [d for d in yaml.safe_load_all(open(tmp + "/render.yaml")) if isinstance(d, dict)]
steps = [d for d in docs if d.get("metadata", {}).get("labels", {}).get("app.kubernetes.io/component") == "cutover-step"]
orders = {d["metadata"]["name"]: int(d["metadata"]["labels"]["bp.openova.io/cutover-order"]) for d in steps}
[step] = [d for d in steps if d["metadata"]["name"] == "cutover-step-08-egress-block-test"]
open(tmp + "/orders.txt", "w").write("%d %d\n" % (orders[step["metadata"]["name"]], max(orders.values())))
texts = [v for v in step.get("data", {}).values() if "reboot_hold_release" in v]
assert len(texts) == 1, "the step holds the release in %d data keys" % len(texts)
spec = yaml.safe_load(texts[0])
def scripts(node):
    if isinstance(node, dict):
        for v in node.values():
            yield from scripts(v)
    elif isinstance(node, list):
        for v in node:
            yield from scripts(v)
    elif isinstance(node, str) and "reboot_hold_release() {" in node:
        yield node
[script] = list(scripts(spec))
open(tmp + "/step.sh", "w").write(script)
m = re.search(r"^reboot_hold_release\(\) \{\n.*?^\}\n", script, re.S | re.M)
assert m, "no function reboot_hold_release"
open(tmp + "/release.sh", "w").write(m.group(0))
values = yaml.safe_load(open(chart + "/values.yaml"))["rebootHold"]
open(tmp + "/values.txt", "w").write("%s\n%s\n%s\n" % (values["namespace"], values["configMapName"], values["valuesKey"]))
open(tmp + "/release-values.yaml", "w").write(values["releaseValues"] + "\n")
PY
{ read -r NS; read -r NAME; read -r KEY; } <"${TMP}/values.txt"

echo "[reboot-hold] (a) the release runs only where the chain ends well"
read -r order last <"${TMP}/orders.txt"
if [ "${order}" = "${last}" ]; then pass "egress-block-test has cutover-order ${order}, the highest of the chain"; else fail "egress-block-test has cutover-order ${order}, the chain goes to ${last}: the release would run before the chain is complete"; fi
python3 - "${TMP}/step.sh" <<'PY' && pass "the release is called twice: before the 'exit 0' of the opt-out path and as the last command, after every 'exit 1'" || fail "the calls of reboot_hold_release are not where the chain ends well (see above)"
import re, sys
lines = open(sys.argv[1]).read().rstrip("\n").split("\n")
calls = [i for i, l in enumerate(lines) if l.strip() == "reboot_hold_release"]
fails = [i for i, l in enumerate(lines) if re.match(r"\s*exit 1\b", l)]
ok = True
def check(cond, text):
    global ok
    if not cond:
        ok = False
        print("    " + text)
check(len(calls) == 2, "calls: %d, want 2" % len(calls))
if len(calls) == 2:
    check(lines[calls[0] + 1].strip() == "exit 0", "the first call is not followed by 'exit 0': %r" % lines[calls[0] + 1])
    check(calls[1] == len(lines) - 1, "the second call is not the last command of the step")
    check("sovereignty proof PASSED" in lines[calls[1] - 1], "the second call does not follow the PASSED line")
    check(all(f < calls[1] for f in fails), "an 'exit 1' follows the last call")
sys.exit(0 if ok else 1)
PY

echo "[reboot-hold] (b) the rendered release, run against a recording kubectl"
mkdir -p "${TMP}/bin" "${TMP}/kc"
: >"${TMP}/kc/me-east-b-1.yaml"; : >"${TMP}/kc/me-east-c-1.yaml"
# The stub records every call. CREATE_FAILS / GET_FAILS hold the regions where that verb fails.
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/bin/sh
region=primary
[ "$1" = "--kubeconfig" ] && { region=$(basename "$2" .yaml); shift 2; }
printf '%s|%s\n' "${region}" "$*" >>"${RECORD}"
case " $* " in
  *" create configmap "*) case " ${CREATE_FAILS:-} " in *" ${region} "*) echo "error: configmaps already exists or the API is away" >&2; exit 1;; esac;;
  *" get configmap "*) case " ${GET_FAILS:-} " in *" ${region} "*) echo "Error from server (NotFound)" >&2; exit 1;; esac;;
esac
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"
sed "s#/secondary-kubeconfigs/#${TMP}/kc/#g" "${TMP}/release.sh" >"${TMP}/release-local.sh"
printf 'reboot_hold_release\n' >>"${TMP}/release-local.sh"
run_release() { # $1 = regions whose create fails, $2 = regions whose get fails
  : >"${TMP}/record.txt"
  RECORD="${TMP}/record.txt" CREATE_FAILS="$1" GET_FAILS="$2" PATH="${TMP}/bin:${PATH}" sh -eu "${TMP}/release-local.sh" >"${TMP}/out.txt" 2>&1
}

if run_release "" ""; then pass "the release ends with status 0"; else fail "the release ended non-zero: $(cat "${TMP}/out.txt")"; fi
want="primary|-n ${NS} create configmap ${NAME} --from-literal=${KEY}=$(cat "${TMP}/release-values.yaml")
me-east-b-1|-n ${NS} create configmap ${NAME} --from-literal=${KEY}=$(cat "${TMP}/release-values.yaml")
me-east-c-1|-n ${NS} create configmap ${NAME} --from-literal=${KEY}=$(cat "${TMP}/release-values.yaml")"
if [ "$(cat "${TMP}/record.txt")" = "${want}" ]; then pass "one create per region: the primary and both mounted kubeconfigs, ${NS}/${NAME} key ${KEY}"; else fail "the calls differ. got:
$(cat "${TMP}/record.txt")
want:
${want}"; fi
[ ! -s "${TMP}/out.txt" ] && pass "nothing is printed when every region is written" || fail "output on the good path: $(cat "${TMP}/out.txt")"

if run_release "primary me-east-b-1 me-east-c-1" "" && [ ! -s "${TMP}/out.txt" ] && [ "$(grep -c ' get configmap ' "${TMP}/record.txt")" = 3 ]; then
  pass "a re-run finds the ConfigMap in every region: status 0, no warning"
else fail "a re-run with the ConfigMap present: $(cat "${TMP}/out.txt")"; fi

if run_release "me-east-b-1" "me-east-b-1"; then
  if grep -q "WARNING: the reboot hold of region me-east-b-1 is NOT released" "${TMP}/out.txt" && [ "$(wc -l <"${TMP}/out.txt")" = 1 ] \
     && grep -q '^me-east-c-1|.* create configmap ' "${TMP}/record.txt"; then
    pass "a region that cannot be written is named in one WARNING, the next region is still written, status 0"
  else fail "a failed region: output: $(cat "${TMP}/out.txt")"; fi
else fail "a failed region made the step fail after its verdict"; fi

echo "[reboot-hold] (c) slot 04b reads the ConfigMap the step writes"
python3 - "${SLOT}" "${NS}" "${NAME}" "${KEY}" <<'PY' && pass "slot 04b: valuesFrom ConfigMap ${NS}/${NAME}, key ${KEY}, optional" || fail "slot 04b does not read ${NS}/${NAME} key ${KEY} as optional values"
import sys, yaml
slot, ns, name, key = sys.argv[1:5]
[hr] = [d for d in yaml.safe_load_all(open(slot)) if d and d.get("kind") == "HelmRelease"]
want = {"kind": "ConfigMap", "name": name, "valuesKey": key, "optional": True}
ok = hr["metadata"]["namespace"] == ns and want in hr["spec"].get("valuesFrom", [])
# Inline values win over valuesFrom: the slot must not set the lock itself.
ok = ok and "dsAnnotations" not in hr["spec"]["values"].get("kured", {})
sys.exit(0 if ok else 1)
PY

echo "[reboot-hold] (d) bp-kured with and without the released values"
if [ ! -d "${KURED}/charts" ] || [ -z "$(ls -A "${KURED}/charts" 2>/dev/null)" ]; then
  (cd "${KURED}" && HELM_CONFIG_HOME="${TMP}/hc" HELM_CACHE_HOME="${TMP}/hk" HELM_DATA_HOME="${TMP}/hd" helm dependency update >/dev/null 2>"${TMP}/dep.err") \
    || fail "the dependency of ${KURED} cannot be fetched: $(tail -n 1 "${TMP}/dep.err")"
fi
python3 - "${SLOT}" "${TMP}" <<'PY'
import sys, yaml
slot, tmp = sys.argv[1], sys.argv[2]
[hr] = [d for d in yaml.safe_load_all(open(slot)) if d and d.get("kind") == "HelmRelease"]
inline = hr["spec"]["values"]
inline["nodePatchConfig"]["image"] = "example/shell:1"
yaml.safe_dump(inline, open(tmp + "/inline.yaml", "w"))
PY
lock_of() { # prints "<the annotation kured locks in> <what the rendered DaemonSet kured holds there, or none>"
  python3 -c '
import sys, yaml
[ds] = [d for d in yaml.safe_load_all(sys.stdin) if d and d["kind"] == "DaemonSet" and d["metadata"]["name"] == "kured"]
flags = [a.split("=", 1)[1] for a in ds["spec"]["template"]["spec"]["containers"][0]["args"] if a.startswith("--lock-annotation=")]
key = flags[0] if len(flags) == 1 else "weave.works/kured-node-lock"
print(key, (ds["metadata"].get("annotations") or {}).get(key) or "none")'
}
# Flux merges valuesFrom first and the inline values of the slot over it.
held="$(helm template kured "${KURED}" -n kube-system -f "${TMP}/inline.yaml" 2>"${TMP}/k.err" | lock_of)"
released="$(helm template kured "${KURED}" -n kube-system -f "${TMP}/release-values.yaml" -f "${TMP}/inline.yaml" 2>>"${TMP}/k.err" | lock_of)"
case "${held}" in
  *'"nodeID":"catalyst-install-hold"'*) pass "without the ConfigMap kured locks where the hold is: ${held}" ;;
  *) fail "without the ConfigMap the DaemonSet kured has no hold: '${held}' $(tail -n 1 "${TMP}/k.err" 2>/dev/null)" ;;
esac
if [ "${released}" = "weave.works/kured-node-lock none" ]; then pass "with the values the step writes kured locks in ${released%% *}, where the DaemonSet holds nothing"; else fail "with the values the step writes kured is still held: '${released}'"; fi

echo
if [ "${FAILURES}" -eq 0 ]; then echo "reboot-hold-release: all checks passed"; exit 0; fi
echo "reboot-hold-release: ${FAILURES} check(s) FAILED"; exit 1
