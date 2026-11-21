#!/usr/bin/env bash
# bp-kured render and behaviour test; needs bash, helm, python3 with PyYAML and
# cmp, no cluster. CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
# 1: the default render works and leaves nodePatchConfig.image empty.
#    The DaemonSet kured is created with the install hold (kured's own manual
#    lock, no time limit, in the annotation kured is told to lock in) and can
#    run on no control-plane node; node-patch-config runs on every node. With
#    the values the hand-over chain writes, kured locks in its default
#    annotation, which the chart does not write: the hold is gone.
# 2: with an image set, the file bodies, node-patch-config and the kured
#    arguments are as values.yaml states, and the function test meets the
#    contract of docs/RUNBOOKS.md section 4.5 (scripts/check-function-test.py).
# 3: the node-patch-config loop writes a file only when it differs.
# 4: the function test passes and fails when it should, against a stub kubectl.
# 5: functionTest.timeoutSeconds is the bound T: the Pod ends at T, the test
#    waits for a DaemonSet that is ready late and gives up 60 seconds before T.
set -euo pipefail
CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  # A helm home of its own: the build reads no repository list of the host.
  HELM_CONFIG_HOME="$TMP/hc" HELM_CACHE_HOME="$TMP/hk" HELM_DATA_HOME="$TMP/hd" helm dependency update >/dev/null
fi

echo "[kured] cases 1 and 2: renders"
helm template bp-kured . --namespace kube-system > "$TMP/default.yaml" || fail "the default render errored"
helm template bp-kured . --namespace kube-system --set nodePatchConfig.image=example/shell:1 > "$TMP/set.yaml" \
  || fail "the render with an image errored"
python3 "$CHECK" --self-test > "$TMP/self-test.txt" || fail "check-function-test.py --self-test failed"
python3 "$CHECK" --chart bp-kured --release bp-kured < "$TMP/set.yaml" || fail "the function test breaks the contract"
# What the hand-over chain writes to release the hold (bp-self-sovereign-cutover, values rebootHold).
printf '%s\n' '{"kured":{"configuration":{"lockAnnotation":"weave.works/kured-node-lock"}}}' > "$TMP/release-values.yaml"
helm template bp-kured . --namespace kube-system --set nodePatchConfig.image=example/shell:1 -f "$TMP/release-values.yaml" > "$TMP/released.yaml" \
  || fail "the render with the hold released errored"
python3 - "$TMP" <<'PY' || fail "cases 1 and 2"
import json, sys, yaml
tmp = sys.argv[1]
def docs(name):
    return [d for d in yaml.safe_load_all(open("%s/%s.yaml" % (tmp, name))) if d]
def one(ds, kind, name):
    hit = [d for d in ds if d["kind"] == kind and d["metadata"]["name"] == name]
    assert len(hit) == 1, "%s/%s: found %d" % (kind, name, len(hit))
    return hit[0]
img = one(docs("default"), "DaemonSet", "node-patch-config")["spec"]["template"]["spec"]["containers"][0]["image"]
assert img == "", "default render: the node-patch-config image is %r, expected empty" % img
ds = docs("set")
assert "NodePort" not in open(tmp + "/set.yaml").read(), "the render holds the word NodePort"
for d in ds:
    if d["kind"] not in ("ClusterRole", "ClusterRoleBinding"):
        assert d["metadata"].get("namespace") == "kube-system", "%s/%s is not in kube-system" % (d["kind"], d["metadata"]["name"])
AUTO = ('APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n'
        'APT::Periodic::AutocleanInterval "7";\n')
CATALYST = ('Unattended-Upgrade::Allowed-Origins { "${distro_id}:${distro_codename}-security"; '
            '"${distro_id}ESMApps:${distro_codename}-apps-security"; '
            '"${distro_id}ESM:${distro_codename}-infra-security"; };\n'
            'Unattended-Upgrade::Automatic-Reboot "false";\n'
            'Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";\n'
            'Unattended-Upgrade::Remove-Unused-Dependencies "true";\n')
cm = one(ds, "ConfigMap", "node-patch-config")["data"]
assert cm == {"20auto-upgrades": AUTO, "52unattended-upgrades-catalyst": CATALYST}, "the ConfigMap bodies differ: %r" % cm
for name, body in cm.items():
    open("%s/%s" % (tmp, name), "w").write(body)
npc = one(ds, "DaemonSet", "node-patch-config")["spec"]["template"]["spec"]
assert npc["tolerations"] == [{"operator": "Exists"}], "node-patch-config tolerations: %r" % npc["tolerations"]
assert npc.get("automountServiceAccountToken") is False, "node-patch-config mounts an API token"
assert not (npc.get("hostNetwork") or npc.get("hostPID") or npc.get("hostIPC")), "node-patch-config uses a host namespace"
hp = [v["hostPath"]["path"] for v in npc["volumes"] if "hostPath" in v]
assert hp == ["/etc/apt/apt.conf.d"], "node-patch-config host mounts: %r" % hp
[c] = npc["containers"]
sc = c["securityContext"]
assert c["image"] == "example/shell:1", "node-patch-config image: %r" % c["image"]
assert sc["allowPrivilegeEscalation"] is False and sc["capabilities"] == {"drop": ["ALL"]} and not sc.get("privileged"), sc
mounts = {m["name"]: m.get("readOnly", False) for m in c["volumeMounts"]}
assert mounts == {"apt-conf-d": False, "config": True}, "node-patch-config mounts: %r" % mounts
assert "sleep 3600" in c["args"][0] and 'cmp -s "/config/$f" "/host/$f"' in c["args"][0], "not the hourly compare-and-write loop"
open(tmp + "/loop.sh", "w").write(c["args"][0])
kured = one(ds, "DaemonSet", "kured")["spec"]["template"]["spec"]
want = ["--ds-name=kured", "--ds-namespace=kube-system", "--metrics-port=8080", "--lock-ttl=1h",
        "--lock-release-delay=5m",
        "--blocking-pod-selector=upgrade.cattle.io/controller=system-upgrade-controller,upgrade.cattle.io/plan",
        "--end-time=22:00", "--lock-annotation=catalyst.openova.io/kured-install-hold", "--period=30m", "--drain-timeout=10m"] + \
       ["--reboot-days=" + d for d in ("su", "mo", "tu", "we", "th", "fr", "sa")] + \
       ["--reboot-sentinel=/sentinel/reboot-required", "--reboot-command=/bin/systemctl reboot",
        "--start-time=00:30", "--time-zone=UTC", "--annotate-nodes=true", "--log-format=text", "--concurrency=1"]
args = kured["containers"][0]["args"]
assert args == want, "kured arguments:\n  got  %r\n  want %r" % (args, want)
# A drain that cannot finish (#7922): kured's ticks queue while an attempt runs, so a period at
# or below drain timeout + lock release delay starts the next attempt in the same second and the
# node is never schedulable again. The period must exceed their sum, in every render.
import re
def minutes(flags, name):
    hit = [a.split("=", 1)[1] for a in flags if a.startswith(name + "=")]
    assert len(hit) == 1, "%s is given %d times" % (name, len(hit))
    m = re.fullmatch(r"(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?", hit[0])
    assert m and hit[0], "%s=%s is not a duration in h, m and s" % (name, hit[0])
    h, mi, s = (int(g or 0) for g in m.groups())
    return h * 60 + mi + s / 60.0
for name in ("default", "set", "released"):
    flags = one(docs(name), "DaemonSet", "kured")["spec"]["template"]["spec"]["containers"][0]["args"]
    period, drain, release = (minutes(flags, f) for f in ("--period", "--drain-timeout", "--lock-release-delay"))
    assert drain > 0, "%s render: --drain-timeout is 0, so a drain that cannot finish never ends" % name
    assert period > drain + release, (
        "%s render: --period (%gm) is not above --drain-timeout + --lock-release-delay (%gm + %gm): "
        "a drain that cannot finish is retried in the same second and keeps the node cordoned for ever"
        % (name, period, drain, release))
assert kured["tolerations"] == [{"operator": "Exists"}], "kured tolerations: %r" % kured["tolerations"]
# The install hold: the annotation kured is told to lock in holds a lock of something
# that is no node, and a lock with TTL 0 never expires (kured: ttl > 0 && age >= ttl).
def lock_of(name):
    """The annotation kured locks in, and what the rendered DaemonSet holds there."""
    d = one(docs(name), "DaemonSet", "kured")
    flags = [a.split("=", 1)[1] for a in d["spec"]["template"]["spec"]["containers"][0]["args"] if a.startswith("--lock-annotation=")]
    assert len(flags) <= 1, "%s render: --lock-annotation is given %d times" % (name, len(flags))
    key = flags[0] if flags else "weave.works/kured-node-lock"
    return key, (d["metadata"].get("annotations") or {}).get(key)
for name in ("default", "set"):
    key, held = lock_of(name)
    assert held is not None, "%s render: the DaemonSet kured has no install hold in %s, so it restarts nodes from its first minute" % (name, key)
    assert json.loads(held) == {"nodeID": "catalyst-install-hold", "TTL": 0}, "%s render: the install hold is %r" % (name, held)
key, held = lock_of("released")
assert key == "weave.works/kured-node-lock", "with the values of the hand-over chain kured locks in %r, not in its default annotation" % key
assert held is None, "with the values of the hand-over chain the DaemonSet kured still holds a lock in %s: %r" % (key, held)
# No control-plane node: a region has one, and its restart is an outage of the region's API server.
CP = [{"matchExpressions": [{"key": "node-role.kubernetes.io/control-plane", "operator": "DoesNotExist"}]}]
for name in ("default", "set", "released"):
    spec = one(docs(name), "DaemonSet", "kured")["spec"]["template"]["spec"]
    terms = spec.get("affinity", {}).get("nodeAffinity", {}).get("requiredDuringSchedulingIgnoredDuringExecution", {}).get("nodeSelectorTerms")
    assert terms == CP, "%s render: kured can run on a control-plane node and restart it: nodeSelectorTerms %r" % (name, terms)
assert "affinity" not in npc and npc["nodeSelector"] == {"kubernetes.io/os": "linux"}, "node-patch-config does not run on every node: %r" % npc.get("affinity")
open(tmp + "/test.sh", "w").write(one(ds, "Pod", "bp-kured-function-test")["spec"]["containers"][0]["args"][0])
PY

echo "[kured] case 3: the node-patch-config loop"
mkdir -p "$TMP/bin" "$TMP/config" "$TMP/host"
cp "$TMP/20auto-upgrades" "$TMP/52unattended-upgrades-catalyst" "$TMP/config/"
printf 'APT::Periodic::Unattended-Upgrade "0";\n' > "$TMP/host/20auto-upgrades"
# A stub sleep ends the loop after one pass: it sends TERM to the shell, whose trap exits 0.
printf '#!/bin/sh\nkill -TERM "$PPID"\n' > "$TMP/bin/sleep"
chmod +x "$TMP/bin/sleep"
local_copy() { sed -e "s#/config/#$TMP/config/#g" -e "s#/host/#$TMP/host/#g" "$1"; }
local_copy "$TMP/loop.sh" > "$TMP/loop-local.sh"
PATH="$TMP/bin:$PATH" sh "$TMP/loop-local.sh" > "$TMP/run1.txt" 2>&1 || fail "the loop exited non-zero: $(cat "$TMP/run1.txt")"
[ "$(grep -c '^node-patch-config: wrote ' "$TMP/run1.txt")" = 2 ] || fail "first pass: expected two writes: $(cat "$TMP/run1.txt")"
for f in 20auto-upgrades 52unattended-upgrades-catalyst; do
  cmp -s "$TMP/config/$f" "$TMP/host/$f" || fail "first pass: $f was not written"
done
PATH="$TMP/bin:$PATH" sh "$TMP/loop-local.sh" > "$TMP/run2.txt" 2>&1 || fail "the second pass exited non-zero"
[ ! -s "$TMP/run2.txt" ] || fail "second pass: a file was written although it did not differ: $(cat "$TMP/run2.txt")"
[ "$(ls "$TMP/host" | wc -l)" = 2 ] || fail "the loop left another file: $(ls "$TMP/host")"

echo "[kured] case 4: the function test against a stub kubectl"
# The stub sleep of case 3 ends the shell that calls it; the waits of the test need one that returns at once.
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/sleep"
mkdir -p "$TMP/run"
# local_test <script>: the test script with its /tmp under $TMP/run (first: $TMP itself may be under /tmp) and the
# node's and the ConfigMap's folders under $TMP.
local_test() { sed -e "s#/tmp/#$TMP/run/#g" "$1" > "$TMP/test-run.sh"; local_copy "$TMP/test-run.sh"; }
local_test "$TMP/test.sh" > "$TMP/test-local.sh"
# run_test <what the stub kubectl prints> <expected exit code> [<reads answered "1 0" first> [<SECONDS at the start>]]
# The stub answers the first reads of the DaemonSet with one node and no Pod ready, then with the counts given; the
# kube-access-lib lines (kubectl config ...) are not reads. bash takes SECONDS from the environment.
run_test() {
  printf '#!/bin/sh\ncase "$1" in config) exit 0 ;; esac\nn=$(( $(cat "%s" 2>/dev/null || echo 0) + 1 )); echo "$n" > "%s"\nif [ "$n" -le %s ]; then echo "1 0"; else echo "%s"; fi\n' \
    "$TMP/count" "$TMP/count" "${3:-0}" "$1" > "$TMP/bin/kubectl"
  chmod +x "$TMP/bin/kubectl"
  rm -f "$TMP/count"
  code=0
  PATH="$TMP/bin:$PATH" TEST_NAMESPACE=kube-system NODE_NAME=n1 SECONDS="${4:-0}" bash "$TMP/test-local.sh" > "$TMP/out.txt" 2>&1 || code=$?
  [ "$code" = "$2" ] || fail "kubectl '$1' after ${3:-0} reads: exit $code, expected $2: $(cat "$TMP/out.txt")"
  verdict="$([ "$2" = 0 ] && echo pass || echo fail)"
  last="$(tail -n 1 "$TMP/out.txt")"
  case "$last" in "FUNCTION-TEST $verdict bp-kured "*) ;; *) fail "kubectl '$1' after ${3:-0} reads: last line: $last" ;; esac
}
run_test "3 3" 0
run_test "3 3" 0 3
run_test "3 2" 1 0 239
grep -q -x -F 'FUNCTION-TEST fail bp-kured not ready within 240 seconds: the DaemonSet kured has 2 of 3 Pods ready' "$TMP/out.txt" \
  || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300 naming the DaemonSet"
run_test "0 0" 1 0 239
printf 'Unattended-Upgrade::Automatic-Reboot "true";\n' > "$TMP/host/52unattended-upgrades-catalyst"
run_test "3 3" 1 0 239
rm "$TMP/host/52unattended-upgrades-catalyst"
run_test "3 3" 1 0 239
grep -q -F 'node n1 has no file /etc/apt/apt.conf.d/52unattended-upgrades-catalyst' "$TMP/out.txt" || fail "a missing file is not named"
cp "$TMP/config/52unattended-upgrades-catalyst" "$TMP/host/"

echo "[kured] case 5: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
TEST=templates/tests/function-test.yaml
helm template bp-kured . --namespace kube-system --set nodePatchConfig.image=example/shell:1 --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/default-test.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template bp-kured . --namespace kube-system --set nodePatchConfig.image=example/shell:1 --set functionTest.timeoutSeconds=900 \
  --show-only "$TEST" > "$TMP/bound.yaml" || fail "the render with functionTest.timeoutSeconds=900 errored"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
# pod_script <render>: the block under args of the test Pod, as the shell reads it. On the default render it must equal
# the script case 2 read with PyYAML, so the bound render is read the same way.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1"
}
pod_script "$TMP/default-test.yaml" > "$TMP/default-test.sh"
cmp -s "$TMP/default-test.sh" "$TMP/test.sh" || fail "the script read from the default render differs from the one case 2 read"
pod_script "$TMP/bound.yaml" > "$TMP/bound-test.sh"
local_test "$TMP/bound-test.sh" > "$TMP/test-local.sh"
run_test "3 3" 0 3 600
run_test "3 2" 1 0 839
grep -q -x -F 'FUNCTION-TEST fail bp-kured not ready within 840 seconds: the DaemonSet kured has 2 of 3 Pods ready' "$TMP/out.txt" \
  || fail "under T 900 the failure does not name the DaemonSet and the wait of 840 seconds"
echo "render: all cases passed"
