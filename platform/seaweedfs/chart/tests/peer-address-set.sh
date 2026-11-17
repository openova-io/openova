#!/usr/bin/env bash
# bp-seaweedfs peer address render test.
#
# Every address a SeaweedFS process announces (`-ip=`) or dials (`-master=`,
# `-peers=`, `-filer=`, `-mserver=`, `-a=`, `-b=`, WEED_CLUSTER_*_{FILER,MASTER})
# must be a name that keeps pointing at a live Pod for the life of the
# release:
#   - a Service of this render (`<svc>.<ns>`), or
#   - a StatefulSet Pod's own name under its governing headless Service
#     (`<sts>-<i>.<svc>.<ns>`, or `${POD_NAME}.<svc>.<ns>` as announced).
# A Pod IP (`${POD_IP}` or a literal) or a name nothing in the render serves
# (a peer region that is not configured) fails.
#
# Why: the S3 gateway of SeaweedFS 4.22 adds every filer the master names to
# its filer list and never removes one (weed/wdclient/filer_client.go:382-383).
# Upstream announces the filer as ${POD_IP}; after the filer Pod moved, each
# gateway kept the dead IP and, on every lookup of a key that does not exist
# yet (every upload), dialled it and waited ~20 s for the TCP timeout
# (hw307, 16-17 Nov 2026: `dial tcp 10.42.1.131:18888: i/o timeout`, journey
# J2 upload 21 s instead of ~1 s).
#
# Assertions, per render:
#   1. the set of announced names, expanded over replicas, EQUALS the set of
#      per-Pod names of the StatefulSets that run a weed server;
#   2. every dialled address is a Service or a per-Pod name of the render, and
#      its HTTP port and gRPC port (explicit `.N`, else port+10000) are ports
#      of that Service;
#   3. the S3 gateway Pod template carries checksum/filer-advertise = sha256 of
#      the filer's announced form, so a change of that form restarts the
#      gateways (they never forget an address);
#   4. no NodePort.
# Then the checker is run on two broken copies (filer announced as ${POD_IP};
# filer.sync pointed at a region the render does not serve) and must refuse
# both, naming the address.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

A=hw-me-east-215-a-rtz-prod
B=hw-me-east-215-b-rtz-prod

render() { # out-file, extra helm args...
  local out="$1"; shift
  helm template seaweedfs . -n seaweedfs "$@" > "$out"
}

render "$TMP/default.yaml"
render "$TMP/region-a.yaml" --set crossRegion.enabled=true --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=[\"$A\",\"$B\"]"
render "$TMP/region-b.yaml" --set crossRegion.enabled=true --set crossRegion.self="$B" \
  --set-json "crossRegion.configuredRegions=[\"$A\",\"$B\"]"
render "$TMP/ha.yaml" --set seaweedfs.filer.replicas=2 --set seaweedfs.master.replicas=3

cat > "$TMP/check.py" <<'PY'
import hashlib, os, re, sys, yaml

NS = "seaweedfs"
ADDR_FLAGS = ("ip", "master", "mserver", "peers", "filer", "a", "b")
WORKLOADS = ("Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod")


def load(path):
    with open(path) as f:
        return [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]


def pod_spec(d):
    if d["kind"] == "Pod":
        return d["spec"]
    if d["kind"] == "CronJob":
        return d["spec"]["jobTemplate"]["spec"]["template"]["spec"]
    return d["spec"]["template"]["spec"]


def check(path):
    docs = load(path)
    fails = []
    svcs = {d["metadata"]["name"]: d for d in docs
            if d["kind"] == "Service" and d["metadata"].get("namespace", NS) == NS}
    stss = {d["metadata"]["name"]: d for d in docs if d["kind"] == "StatefulSet"}

    def svc_ports(name):
        return {int(p["port"]) for p in svcs[name]["spec"].get("ports", [])}

    def headless(name):
        return name in svcs and svcs[name]["spec"].get("clusterIP") == "None"

    # Real per-Pod names: a StatefulSet Pod resolves only under its governing headless Service.
    pod_names = {}
    for name, s in stss.items():
        svc = s["spec"].get("serviceName", "")
        if headless(svc):
            for i in range(int(s["spec"].get("replicas", 1))):
                pod_names[f"{name}-{i}.{svc}.{NS}"] = svc

    announced, server_pods, dialled = {}, set(), []
    filer_form = None
    for d in docs:
        if d["kind"] not in WORKLOADS:
            continue
        wname = f'{d["kind"]}/{d["metadata"]["name"]}'
        spec = pod_spec(d)
        for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
            cmd = " ".join((c.get("command") or []) + (c.get("args") or []))
            if "weed" not in cmd:
                continue
            for e in c.get("env") or []:
                if re.fullmatch(r"WEED_CLUSTER_[A-Z0-9_]+_(FILER|MASTER)", e["name"]) and "value" in e:
                    dialled.append((wname, e["name"], e["value"]))
            for flag in ADDR_FLAGS:
                for v in re.findall(r"(?:^|\s)-" + re.escape(flag) + r"=(\S+)", cmd):
                    if flag == "ip":
                        announced.setdefault(wname, []).append(v)
                    else:
                        dialled.append((wname, "-" + flag, v))
            # The weed subcommand: the first word after the weed binary that is not a flag.
            words = [w for w in re.split(r"[\s\\]+", cmd) if w]
            at = next((i for i, w in enumerate(words) if w == "weed" or w.endswith("/weed")), None)
            sub = next((w for w in words[at + 1:] if not w.startswith("-")), "") if at is not None else ""
            if sub in ("master", "volume", "filer") and d["kind"] == "StatefulSet":
                n, s = d["metadata"]["name"], d
                svc = s["spec"].get("serviceName", "")
                for i in range(int(s["spec"].get("replicas", 1))):
                    server_pods.add(f"{n}-{i}.{svc}.{NS}")
                if sub == "filer":
                    filer_form = (announced.get(wname) or [None])[-1]

    # 1. announced names, expanded, == the per-Pod names of the weed server StatefulSets.
    expanded = set()
    for wname, values in announced.items():
        kind, name = wname.split("/", 1)
        for v in values:
            m = re.fullmatch(r"\$\{POD_NAME\}\.([a-z0-9-]+)\." + NS, v)
            if not m:
                fails.append(f"{wname} announces -ip={v}: not the Pod's own stable name "
                             f"(a Pod IP dies with the Pod; clients that learned it keep dialling it)")
                continue
            svc = m.group(1)
            if kind != "StatefulSet" or stss[name]["spec"].get("serviceName") != svc or not headless(svc):
                fails.append(f"{wname} announces -ip={v}: {svc} is not this StatefulSet's governing headless Service, so the name resolves to nothing")
                continue
            for i in range(int(stss[name]["spec"].get("replicas", 1))):
                expanded.add(f"{name}-{i}.{svc}.{NS}")
    if expanded != server_pods:
        fails.append(f"announced set != real endpoints: announced-only {sorted(expanded - server_pods)}, "
                     f"never announced {sorted(server_pods - expanded)}")

    # 2. every dialled address is a Service or a per-Pod name of this render, on its ports.
    for wname, where, value in dialled:
        for part in value.split(","):
            host, _, port = part.partition(":")
            if re.fullmatch(r"\$\{POD_IP\}|[0-9.]+", host):
                fails.append(f"{wname} {where}={value}: {host} is a Pod address")
                continue
            if host in pod_names:
                svc = pod_names[host]
            elif host.endswith("." + NS) and host[: -len(NS) - 1] in svcs:
                svc = host[: -len(NS) - 1]
            else:
                fails.append(f"{wname} {where}={value}: {host} names no Service or StatefulSet Pod of this render")
                continue
            if port:
                http, _, grpc = port.partition(".")
                want = {int(http), int(grpc) if grpc else int(http) + 10000}
                missing = want - svc_ports(svc)
                if missing:
                    fails.append(f"{wname} {where}={value}: Service {svc} has no port {sorted(missing)}")

    # 3. the gateways restart when the filer's announced form changes.
    s3 = [d for d in docs if d["kind"] == "Deployment" and d["metadata"]["name"] == "seaweedfs-s3"]
    if filer_form is None:
        fails.append("no filer StatefulSet announcing an address was rendered")
    elif not s3:
        fails.append("no seaweedfs-s3 Deployment was rendered")
    else:
        ann = (s3[0]["spec"]["template"]["metadata"].get("annotations") or {}).get("checksum/filer-advertise")
        want = hashlib.sha256(filer_form.encode()).hexdigest()
        if ann != want:
            fails.append(f"seaweedfs-s3 Pod template checksum/filer-advertise={ann}, want sha256({filer_form})={want}: "
                         f"a change of the filer's announced form would not restart the gateways")

    # 4. no NodePort.
    for name, s in svcs.items():
        if s["spec"].get("type", "ClusterIP") == "NodePort" or any(p.get("nodePort") for p in s["spec"].get("ports", [])):
            fails.append(f"Service {name} carries a NodePort")

    return fails, sorted(expanded), len(dialled)


mode, path = sys.argv[1], sys.argv[2]
fails, names, n = check(path)
label = os.path.basename(path)
if mode == "pass":
    if fails:
        print(f"FAIL {label}:")
        for f in fails:
            print("  - " + f)
        sys.exit(1)
    print(f"PASS {label}: announced {names}; {n} dialled address(es), each a Service or Pod name of the render")
else:
    want = sys.argv[3]
    hit = [f for f in fails if want in f]
    if not hit:
        print(f"FAIL {label}: the checker accepted a broken render (expected a refusal naming {want!r}); refusals: {fails}")
        sys.exit(1)
    print(f"PASS {label}: refused as expected: {hit[0]}")
PY

python3 "$TMP/check.py" pass "$TMP/default.yaml"
python3 "$TMP/check.py" pass "$TMP/region-a.yaml"
python3 "$TMP/check.py" pass "$TMP/region-b.yaml"
python3 "$TMP/check.py" pass "$TMP/ha.yaml"
# Broken copies: the checker must refuse them.
sed 's#-ip=${POD_NAME}.seaweedfs-filer.seaweedfs#-ip=${POD_IP}#' "$TMP/default.yaml" > "$TMP/broken-podip.yaml"
cmp -s "$TMP/default.yaml" "$TMP/broken-podip.yaml" && { echo "FAIL: could not build the \${POD_IP} control (the filer -ip line changed shape)" >&2; exit 1; }
sed "s#-a=seaweedfs-filer-mesh-$B.seaweedfs#-a=seaweedfs-filer-mesh-hw-gone-rtz-prod.seaweedfs#" "$TMP/region-a.yaml" > "$TMP/broken-peer.yaml"
cmp -s "$TMP/region-a.yaml" "$TMP/broken-peer.yaml" && { echo "FAIL: could not build the dead-peer control (the filer.sync -a line changed shape)" >&2; exit 1; }

python3 "$TMP/check.py" refuse "$TMP/broken-podip.yaml" 'announces -ip=${POD_IP}'
python3 "$TMP/check.py" refuse "$TMP/broken-peer.yaml" 'seaweedfs-filer-mesh-hw-gone-rtz-prod.seaweedfs names no Service'
echo "[peer-address-set] all cases PASS"
