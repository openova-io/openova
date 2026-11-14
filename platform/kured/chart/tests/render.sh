#!/usr/bin/env bash
# bp-kured render and behaviour test; needs bash, helm, python3 with PyYAML and
# cmp, no cluster. CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
# 1: the default render works and leaves nodePatchConfig.image empty.
# 2: with an image set, the file bodies, node-patch-config and the kured
#    arguments are as values.yaml states, and the function test meets the
#    contract of docs/RUNBOOKS.md section 4.5 (scripts/check-function-test.py).
# 3: the node-patch-config loop writes a file only when it differs.
# 4: the function test passes and fails when it should, against a stub kubectl.
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
python3 - "$TMP" <<'PY' || fail "cases 1 and 2"
import sys, yaml
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
        "--end-time=22:00", "--period=15m", "--drain-timeout=15m"] + \
       ["--reboot-days=" + d for d in ("su", "mo", "tu", "we", "th", "fr", "sa")] + \
       ["--reboot-sentinel=/sentinel/reboot-required", "--reboot-command=/bin/systemctl reboot",
        "--start-time=00:30", "--time-zone=UTC", "--annotate-nodes=true", "--log-format=text", "--concurrency=1"]
args = kured["containers"][0]["args"]
assert args == want, "kured arguments:\n  got  %r\n  want %r" % (args, want)
assert kured["tolerations"] == [{"operator": "Exists"}], "kured tolerations: %r" % kured["tolerations"]
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
local_copy "$TMP/test.sh" > "$TMP/test-local.sh"
run_test() { # <what the stub kubectl prints> <expected exit code>
  printf '#!/bin/sh\necho "%s"\n' "$1" > "$TMP/bin/kubectl"
  chmod +x "$TMP/bin/kubectl"
  code=0
  PATH="$TMP/bin:$PATH" TEST_NAMESPACE=kube-system NODE_NAME=n1 sh "$TMP/test-local.sh" > "$TMP/out.txt" 2>&1 || code=$?
  [ "$code" = "$2" ] || fail "kubectl '$1': exit $code, expected $2: $(cat "$TMP/out.txt")"
  verdict="$([ "$2" = 0 ] && echo pass || echo fail)"
  tail -n 1 "$TMP/out.txt" | grep -q "^FUNCTION-TEST $verdict bp-kured " || fail "kubectl '$1': last line: $(tail -n 1 "$TMP/out.txt")"
}
run_test "3 3" 0
run_test "3 2" 1
run_test "0 0" 1
printf 'Unattended-Upgrade::Automatic-Reboot "true";\n' > "$TMP/host/52unattended-upgrades-catalyst"
run_test "3 3" 1
rm "$TMP/host/52unattended-upgrades-catalyst"
run_test "3 3" 1
echo "render: all cases passed"
