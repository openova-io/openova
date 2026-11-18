#!/usr/bin/env bash
# mesh-test.sh - the hermetic two-cluster mesh test, on the local provider pack.
#
# The two regions of the local provider pack (scripts/local-pack.sh up --regions 2) are the two
# clusters. This script lays the mesh layer over the Cilium of both, as values of the HelmRelease
# flux-system/bp-cilium: clustermesh.config with the peer from the module output network_registry,
# KVStoreMesh, and the certificates of the mesh API server from the cert-manager ClusterIssuer
# sovereign-mesh-ca. It runs three cases:
#
#   A  one self-signed authority; the same key pair backs sovereign-mesh-ca in both clusters;
#      tls.authMode migration.
#   B  one root; sovereign-mesh-ca of each cluster is its own intermediate signed by that root;
#      tls.authMode migration.
#   C  the shape of B when B passed, else the shape of A, with tls.authMode cluster in both.
#
# Each case asserts, in this order, and stops at the first of the first six that fails:
#   issuer        sovereign-mesh-ca is Ready in both clusters;
#   layer         bp-cilium is Ready at the generation that carries the layer, in both clusters;
#   certificates  the four Certificates of the mesh API server are Ready, name sovereign-mesh-ca,
#                 and the ca.crt of each of their Secrets is the case's root, in both clusters;
#                 the server certificate is signed by the cluster's intermediate (B) or the root;
#   mode          the common name of the remote certificate is remote (migration) or
#                 remote-<cluster name> (cluster);
#   mesh          every Cilium agent of each cluster reports the peer cluster ready (cilium-dbg
#                 status, the read of scripts/local-pack.sh), within 10 minutes;
#   global        a global Service whose only backend runs in the other cluster answers from that
#                 backend, from each cluster;
#   pause         the machines of the second cluster are paused (docker pause) for 5 minutes; 1, 3
#                 and 5 minutes in, every dial of the global Service echo (affinity local) from the
#                 first cluster is answered by the first cluster's backend;
#   resume        after the machines resume, every agent of both clusters reports the peer ready
#                 within 10 minutes, and the global Services answer across both ways again;
#   restarts      no pod of the mesh (the Cilium agents and clustermesh-apiserver) and no pod of the
#                 test restarted or was replaced from before the pause to after resume. The
#                 cilium-operator is not judged: it leaves on purpose when it loses its leader lease,
#                 which the pause of its own cluster's API server causes, and it carries no mesh state.
#
# Usage (from the repository root, after scripts/local-pack.sh up --regions 2):
#   bash tests/hermetic/mesh/mesh-test.sh run
#   bash tests/hermetic/mesh/mesh-test.sh --self-test
#
# run reads LOCAL_PACK_DIR (the env file and the kubeconfigs that up writes, and the state of the
# module) and writes under MESH_TEST_DIR (default LOCAL_PACK_DIR/mesh-hermetic): case-A.txt,
# case-B.txt and case-C.txt (one line per assertion) and summary.md (one row per case, also
# appended to GITHUB_STEP_SUMMARY when that is set). Before the first case it suspends, in each
# cluster, the Flux Kustomization that applies bp-cilium, so that the kit does not take the layer
# back, and deletes the Secret kube-system/cilium-clustermesh that up wrote, which the chart writes
# from then on. The authority keys are made with openssl in a folder of mode 700 under
# MESH_TEST_DIR and removed when run ends. When case A fails, B and C are not run.
# Exit codes: 0 case A passed (B and C are reported, whatever their result); 1 case A failed or the
# run could not start; 2 wrong usage. run needs bash, python3, openssl, kubectl, tofu and docker;
# --self-test needs bash and python3 only. Nothing here creates a Service of type NodePort or
# uses a node port, and the peers come from the values of the chart, never from the connect
# command of the Cilium command-line tool.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
DIR="${LOCAL_PACK_DIR:-${RUNNER_TEMP:-/tmp}/local-pack}"
OUT="${MESH_TEST_DIR:-$DIR/mesh-hermetic}"
KEYS="$OUT/keys"
NS=mesh-hermetic
ISSUER=sovereign-mesh-ca
CA_NS=cert-manager
IMAGE="registry.k8s.io/e2e-test-images/agnhost:2.53@sha256:99c6b4bb4a1e1df3f0b3752168c89358794d02258ebebc26bf21c29399011a85"
CERTS="clustermesh-apiserver-admin-cert clustermesh-apiserver-local-cert clustermesh-apiserver-remote-cert clustermesh-apiserver-server-cert"
PAUSE_AT="60 180 300"
DIALS=10
ISSUER_LIMIT=180
HR_LIMIT=900
CERT_LIMIT=300
ROLL_LIMIT=600
MESH_LIMIT=600
DIAL_LIMIT=180
# What a Cilium agent says of its peer clusters: "<cluster name>=<true|false>;" per peer, the read
# of scripts/local-pack.sh.
MESH_STATUS='jsonpath={range .cluster-mesh.clusters[*]}{.name}={.ready};{end}'

log() { echo "[mesh-test $(date -u +%H:%M:%S)] $*"; }
die() { echo "mesh-test: $*" >&2; exit 1; }
usage() { sed -n '/^# Usage/,/--self-test$/p' "${BASH_SOURCE[0]}" | sed 's/^# //' >&2; exit 2; }

# --- pure parts (covered by --self-test) -------------------------------------------------

# mesh_names <region code>: stdin is the network registry (JSON); stdout "<own cluster name> <peer
# cluster name> <peer mesh address> <peer mesh port>". Exit 1 when the registry does not hold
# exactly two regions, one of them the given one, each with a cluster name and a mesh endpoint.
mesh_names() {
  python3 -c '
import json, sys
code = sys.argv[1]
regs = json.load(sys.stdin).get("regions") or []
if len(regs) != 2:
    sys.exit("mesh: the network registry has %d region(s), the test needs 2" % len(regs))
me, peer = [r for r in regs if r.get("code") == code], [r for r in regs if r.get("code") != code]
if len(me) != 1 or len(peer) != 1:
    sys.exit("mesh: the network registry has no region %s" % code)
for r in me + peer:
    ep = r.get("meshEndpoint") or {}
    port = ep.get("port")
    if not r.get("clusterName") or not ep.get("ips") or not isinstance(port, int) or port <= 0:
        sys.exit("mesh: region %s has no cluster name or no mesh endpoint" % r.get("code"))
p = peer[0]
print(me[0]["clusterName"], p["clusterName"], p["meshEndpoint"]["ips"][0], p["meshEndpoint"]["port"])' "$1"
}

# layer_patch <peer cluster name> <peer address> <peer port> <auth mode>: stdout the JSON merge
# patch of the HelmRelease flux-system/bp-cilium that lays the mesh layer over the values of slot
# 01: the API server stays on, clustermesh.config names the one peer by address (the chart writes
# the host alias <peer>.mesh.cilium.io), KVStoreMesh is on, and the certificates of the API server
# come from the ClusterIssuer sovereign-mesh-ca with the given tls.authMode. The slot's Service of
# the API server is not touched. Exit 1 when the mode is not migration or cluster, the address is
# not an IP address or the port is not between 1 and 65535.
layer_patch() {
  python3 -c '
import ipaddress, json, sys
name, ip, port, mode = sys.argv[1:5]
if mode not in ("migration", "cluster"):
    sys.exit("layer: the auth mode %r is neither migration nor cluster" % mode)
try:
    ipaddress.ip_address(ip)
except ValueError:
    sys.exit("layer: %r is not an IP address" % ip)
if not port.isdigit() or not 0 < int(port) < 65536 or not name:
    sys.exit("layer: no peer name, or the port %r is not between 1 and 65535" % port)
ref = {"group": "cert-manager.io", "kind": "ClusterIssuer", "name": sys.argv[5]}
mesh = {"useAPIServer": True,
        "config": {"enabled": True, "domain": "mesh.cilium.io",
                   "clusters": [{"name": name, "port": int(port), "ips": [ip]}]},
        "apiserver": {"kvstoremesh": {"enabled": True},
                      "tls": {"authMode": mode,
                              "auto": {"enabled": True, "method": "certmanager", "certManagerIssuerRef": ref}}}}
print(json.dumps({"spec": {"values": {"cilium": {"clustermesh": mesh}}}}, sort_keys=True))' "$1" "$2" "$3" "$4" "$ISSUER"
}

# issuer_manifest <certificate file> <key file>: stdout a List of the Secret <CA_NS>/<ISSUER> of
# type kubernetes.io/tls (tls.crt the certificate file: the authority, or an intermediate followed
# by its root; tls.key the key file) and the ClusterIssuer <ISSUER> of kind CA that reads it.
# Exit 1 when a file is missing or empty.
issuer_manifest() {
  python3 -c '
import base64, json, sys
crt, key, ns, name = sys.argv[1:5]
data = {}
for k, f in (("tls.crt", crt), ("tls.key", key)):
    try:
        b = open(f, "rb").read()
    except OSError as e:
        sys.exit("issuer: %s" % e)
    if not b.strip():
        sys.exit("issuer: %s is empty" % f)
    data[k] = base64.b64encode(b).decode()
labels = {"app.kubernetes.io/managed-by": "mesh-hermetic-test"}
items = [{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls",
          "metadata": {"name": name, "namespace": ns, "labels": labels}, "data": data},
         {"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer",
          "metadata": {"name": name, "labels": labels}, "spec": {"ca": {"secretName": name}}}]
print(json.dumps({"apiVersion": "v1", "kind": "List", "items": items}, sort_keys=True))' "$1" "$2" "$CA_NS" "$ISSUER"
}

# workload_manifest <1|2>: stdout a List of the test objects of the first or second cluster in
# namespace NS. Deployment echo in both clusters; Deployment solo-<n> only in cluster n. Services
# echo (global, affinity local), solo-1 and solo-2 (global) in both, each of type ClusterIP on
# port 8080. Every pod runs IMAGE (pinned by digest) as netexec: /hostname answers with the pod
# name, /dial dials an address from inside the pod. Exit 1 when the argument is not 1 or 2.
workload_manifest() {
  python3 -c '
import json, sys
n, ns, image = sys.argv[1:4]
if n not in ("1", "2"):
    sys.exit("workload: the cluster number %r is neither 1 nor 2" % n)
def deploy(name):
    sel = {"app": name}
    c = {"name": "netexec", "image": image, "args": ["netexec", "--http-port=8080"],
         "ports": [{"name": "http", "containerPort": 8080, "protocol": "TCP"}],
         "readinessProbe": {"httpGet": {"path": "/readyz", "port": 8080}, "periodSeconds": 5},
         "livenessProbe": {"httpGet": {"path": "/healthz", "port": 8080}, "periodSeconds": 10},
         "resources": {"requests": {"cpu": "10m", "memory": "16Mi"}, "limits": {"memory": "64Mi"}},
         "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                             "runAsNonRoot": True, "runAsUser": 65534, "runAsGroup": 65534,
                             "capabilities": {"drop": ["ALL"]}}}
    pod = {"containers": [c], "automountServiceAccountToken": False,
           "securityContext": {"runAsNonRoot": True, "seccompProfile": {"type": "RuntimeDefault"}}}
    return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": name, "namespace": ns, "labels": sel},
            "spec": {"replicas": 1, "selector": {"matchLabels": sel},
                     "template": {"metadata": {"labels": sel}, "spec": pod}}}
def service(name, local):
    ann = {"service.cilium.io/global": "true", "service.cilium.io/shared": "true"}
    if local:
        ann["service.cilium.io/affinity"] = "local"
    return {"apiVersion": "v1", "kind": "Service", "metadata": {"name": name, "namespace": ns, "annotations": ann},
            "spec": {"type": "ClusterIP", "selector": {"app": name},
                     "ports": [{"name": "http", "port": 8080, "targetPort": 8080, "protocol": "TCP"}]}}
items = [{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns}},
         deploy("echo"), deploy("solo-" + n), service("echo", True), service("solo-1", False), service("solo-2", False)]
print(json.dumps({"apiVersion": "v1", "kind": "List", "items": items}, sort_keys=True))' "$1" "$NS" "$IMAGE"
}

# dial_judge <pod name>[,<pod name>...]: stdin has one line per dial, the answer of netexec /dial
# with tries=1. Prints how the dials were answered; exit 0 when there is at least one dial and
# every dial was answered by one of the named pods.
dial_judge() {
  python3 -c '
import json, sys
want = set(x for x in sys.argv[1].split(",") if x)
lines = [l for l in sys.stdin.read().splitlines() if l.strip()]
ok, other, failed, seen = 0, 0, 0, set()
for l in lines:
    try:
        d = json.loads(l)
    except ValueError:
        failed += 1
        continue
    r = d.get("responses") or [] if isinstance(d, dict) else []
    if len(r) == 1 and not d.get("errors"):
        if r[0].strip() in want:
            ok += 1
        else:
            other += 1
            seen.add(r[0].strip())
    else:
        failed += 1
print("%d of %d dials answered by %s, %d by another pod%s, %d failed" % (ok, len(lines), ",".join(sorted(want)) or "nobody",
      other, " (%s)" % ",".join(sorted(seen)) if seen else "", failed))
sys.exit(0 if lines and want and ok == len(lines) else 1)' "$1"
}

# pod_lines <test namespace>: stdin is `kubectl get pods -A -o json`; stdout one line per pod of the
# mesh (in kube-system, label k8s-app cilium or clustermesh-apiserver) and per pod of the test
# namespace: "<namespace>/<name> <uid> <restarts of all containers>", sorted. Exit 1 when there is
# no such pod.
pod_lines() {
  python3 -c '
import json, sys
ns = sys.argv[1]
out = []
for p in json.load(sys.stdin).get("items", []):
    m = p.get("metadata", {})
    lab = m.get("labels") or {}
    mesh = m.get("namespace") == "kube-system" and lab.get("k8s-app") in ("cilium", "clustermesh-apiserver")
    if mesh or m.get("namespace") == ns:
        n = sum(int(c.get("restartCount", 0)) for c in p.get("status", {}).get("containerStatuses") or [])
        out.append("%s/%s %s %d" % (m.get("namespace"), m.get("name"), m.get("uid"), n))
print("\n".join(sorted(out)))
sys.exit(0 if out else 1)' "$1"
}

# restart_judge <before> <after>: two files of pod_lines. Prints every pod that restarted, is new
# or is gone; exit 0 when both files hold the same non-empty set of lines.
restart_judge() {
  python3 -c '
import sys
a, b = [set(l for l in open(f).read().splitlines() if l.strip()) for f in sys.argv[1:3]]
for l in sorted(a - b):
    print("before only: " + l)
for l in sorted(b - a):
    print("after only:  " + l)
print("%d pods compared, %d changed" % (len(a), len(a ^ b)))
sys.exit(0 if a and a == b else 1)' "$1" "$2"
}

# mesh_judge <peer cluster name>...: stdin has one line per Cilium agent of a cluster, "<pod> <what
# MESH_STATUS printed>". Prints one line per agent that does not report every named peer ready;
# exit 0 when there is at least one agent and every agent reports every named peer ready.
mesh_judge() {
  python3 -c '
import sys
peers, lines, bad = sys.argv[1:], [l.split(None, 1) for l in sys.stdin.read().splitlines() if l.strip()], 0
for f in lines:
    pod, seen = f[0], dict(x.split("=", 1) for x in (f[1] if len(f) > 1 else "").split(";") if "=" in x)
    missing = [p for p in peers if seen.get(p) != "true"]
    if missing:
        bad += 1
        print("%s: not ready %s (reports %s)" % (pod, ",".join(missing), ";".join("%s=%s" % kv for kv in sorted(seen.items())) or "no peer"))
sys.exit(0 if lines and peers and not bad else 1)' "$@"
}

# hr_judge: stdin is the HelmRelease as JSON. Prints its Ready condition; exit 0 when Ready is True
# for the current generation (the condition's observedGeneration equals metadata.generation).
hr_judge() {
  python3 -c '
import json, sys
h = json.load(sys.stdin)
gen = h.get("metadata", {}).get("generation")
c = [x for x in h.get("status", {}).get("conditions") or [] if x.get("type") == "Ready"]
c = c[0] if c else {}
print("generation %s: Ready=%s for generation %s, %s: %s" % (gen, c.get("status"), c.get("observedGeneration"),
      c.get("reason"), (c.get("message") or "")[:300]))
sys.exit(0 if c.get("status") == "True" and gen is not None and c.get("observedGeneration") == gen else 1)'
}

# cert_judge <certificates file> <secrets file> <root file>: the first two files are `kubectl get
# certificate,secret -o json` of the names in CERTS, the third the root of the case (PEM). Prints
# one line per name; exit 0 when each Certificate is Ready and names the issuer ISSUER, and the
# ca.crt of each Secret is exactly one certificate, the root (compared as DER).
cert_judge() {
  python3 -c '
import base64, json, re, sys
certs, secrets, root, names, issuer = sys.argv[1:6]
def ders(text):
    return [base64.b64decode(re.sub(r"\s", "", b)) for b in re.findall(r"-----BEGIN CERTIFICATE-----(.*?)-----END CERTIFICATE-----", text, re.S)]
def items(f):
    return {i["metadata"]["name"]: i for i in json.load(open(f)).get("items", [])}
want, cs, ss, bad = ders(open(root).read()), items(certs), items(secrets), 0
if len(want) != 1:
    sys.exit("certificates: the root file holds %d certificates, not 1" % len(want))
for n in names.split():
    c, s = cs.get(n, {}), ss.get(n, {})
    ready = [x.get("status") for x in c.get("status", {}).get("conditions") or [] if x.get("type") == "Ready"]
    ref = (c.get("spec", {}).get("issuerRef") or {}).get("name")
    try:
        ca = ders(base64.b64decode(s.get("data", {}).get("ca.crt", "")).decode())
    except (ValueError, UnicodeDecodeError):
        ca = []
    good = ready == ["True"] and ref == issuer and ca == want
    bad += not good
    print("%s: Ready=%s issuer=%s ca.crt=%s" % (n, ",".join(ready) or "absent", ref,
          "the root" if ca == want else "%d other certificate(s)" % len(ca)))
sys.exit(1 if bad else 0)' "$1" "$2" "$3" "$CERTS" "$ISSUER"
}

# verdict <case file>: PASS when the case ran to its END line with no FAIL line, NOT RUN when the
# file says so or is missing, FAIL otherwise.
verdict() {
  python3 -c '
import sys
try:
    lines = open(sys.argv[1]).read().splitlines()
except OSError:
    lines = ["NOT RUN"]
if "NOT RUN" in lines:
    print("NOT RUN")
elif "END" in lines and not [l for l in lines if l.startswith("FAIL ")]:
    print("PASS")
else:
    print("FAIL")' "$1"
}

# summary <out dir>: stdout the summary table of the cases A, B and C; exit 0 when case A passed.
summary() {
  python3 -c '
import os, sys
d = sys.argv[1]
shapes = {"A": "one self-signed authority, the same key pair in both clusters",
          "B": "one root, an intermediate per cluster"}
rows, ok = [], False
for c in "ABC":
    try:
        lines = open(os.path.join(d, "case-%s.txt" % c)).read().splitlines()
    except OSError:
        lines = ["NOT RUN"]
    head = dict(x.split("=", 1) for x in (lines[0].split()[2:] if lines and lines[0].startswith("case ") else []) if "=" in x)
    fails = [l[5:] for l in lines if l.startswith("FAIL ")]
    info = [l[5:] for l in lines if l.startswith("INFO ")]
    if "NOT RUN" in lines:
        res, why = "NOT RUN", "case A failed" if c != "A" else "not started"
    elif "END" in lines and not fails:
        res, why = "PASS", "; ".join(info)
    else:
        res, why = "FAIL", fails[0] if fails else "the case stopped before its end"
    if c == "A":
        ok = res == "PASS"
    rows.append("| %s | %s | %s | %s | %s |" % (c, shapes.get(head.get("shape"), "-"), head.get("mode", "-"), res, why.replace("|", "/")))
print("### Hermetic mesh test on the local pack")
print()
print("| Case | Authority | tls.authMode | Result | Detail |")
print("|---|---|---|---|---|")
print("\n".join(rows))
print()
print("Case A must pass. Case B decides whether each region may hold its own intermediate under one root.")
sys.exit(0 if ok else 1)' "$1"
}

# --- commands -------------------------------------------------------------------------------

# kr <region code> <kubectl argument>...: kubectl against the region's API server.
kr() { local code=$1; shift; kubectl --kubeconfig "$DIR/$code.kubeconfig" --request-timeout=30s "$@"; }

# rec, pass and fail write the case file CASE_FILE. fail ends the case (it runs in a subshell);
# miss records a failed assertion and lets the case go on.
rec() { echo "$*" >> "$CASE_FILE"; }
pass() { local what=$1; shift; rec "PASS $what $*"; log "case $CASE: PASS $what: $*"; }
miss() { local what=$1; shift; rec "FAIL $what: $*"; log "case $CASE: FAIL $what: $*"; }
fail() { miss "$@"; exit 1; }

# wait_for <seconds> <command>...: runs the command every 10 seconds until it exits 0; exit 1 when
# the time is up.
wait_for() {
  local limit=$1 t
  shift
  t=$(date +%s)
  until "$@"; do
    [ $(( $(date +%s) - t )) -lt "$limit" ] || return 1
    sleep 10
  done
}

# make_ca <dir> <shape A|B>: the authority of a case. Writes issuer-<code>.crt and issuer-<code>.key
# for both regions and root.crt. A: one self-signed authority, the same pair for both regions. B:
# one root and, per region, an intermediate signed by it; issuer-<code>.crt is the intermediate
# followed by the root, issuer-<code>.key the intermediate's key.
make_ca() {
  local d=$1 shape=$2 code
  openssl req -x509 -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 -sha256 -days 3650 \
    -subj "/CN=mesh-hermetic root" -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" -keyout "$d/root.key" -out "$d/root.crt" 2> /dev/null
  printf 'basicConstraints=critical,CA:TRUE,pathlen:0\nkeyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid:always\n' > "$d/int.ext"
  for code in "$REGION_A" "$REGION_B"; do
    if [ "$shape" = A ]; then
      cp "$d/root.crt" "$d/issuer-$code.crt"
      cp "$d/root.key" "$d/issuer-$code.key"
      continue
    fi
    openssl req -new -nodes -newkey ec -pkeyopt ec_paramgen_curve:P-256 -sha256 \
      -subj "/CN=mesh-hermetic intermediate $code" -keyout "$d/int-$code.key" -out "$d/int-$code.csr" 2> /dev/null
    openssl x509 -req -sha256 -days 3650 -in "$d/int-$code.csr" -CA "$d/root.crt" -CAkey "$d/root.key" \
      -CAcreateserial -extfile "$d/int.ext" -out "$d/int-$code.crt" 2> /dev/null
    cat "$d/int-$code.crt" "$d/root.crt" > "$d/issuer-$code.crt"
    cp "$d/int-$code.key" "$d/issuer-$code.key"
  done
}

# prepare <region code>: suspends the Flux Kustomization that applies bp-cilium and deletes the
# Secret kube-system/cilium-clustermesh when Helm does not own it (up wrote it).
prepare() {
  local code=$1 owner ons managed
  owner=$(kr "$code" -n flux-system get helmrelease bp-cilium -o 'jsonpath={.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name}')
  ons=$(kr "$code" -n flux-system get helmrelease bp-cilium -o 'jsonpath={.metadata.labels.kustomize\.toolkit\.fluxcd\.io/namespace}')
  if [ -n "$owner" ]; then
    kr "$code" -n "${ons:-flux-system}" patch kustomizations.kustomize.toolkit.fluxcd.io "$owner" --type merge -p '{"spec":{"suspend":true}}' > /dev/null
    log "$code: Flux Kustomization ${ons:-flux-system}/$owner suspended"
  fi
  if managed=$(kr "$code" -n kube-system get secret cilium-clustermesh -o 'jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}' 2> /dev/null) \
    && [ "$managed" != Helm ]; then
    kr "$code" -n kube-system delete secret cilium-clustermesh > /dev/null
    log "$code: Secret kube-system/cilium-clustermesh of ${managed:-no manager} deleted"
  fi
}

# mesh_status <region code>: one line per Cilium agent, "<pod> <what MESH_STATUS printed>". It never
# fails.
mesh_status() {
  local code=$1 pod
  for pod in $(kr "$code" -n kube-system get pods -l k8s-app=cilium -o name 2> /dev/null); do
    echo "${pod#pod/} $(kr "$code" -n kube-system exec "${pod#pod/}" -c cilium-agent -- cilium-dbg status -o "$MESH_STATUS" < /dev/null 2> /dev/null || true)"
  done
}

# mesh_ready: every agent of both regions reports its peer ready. Writes what it saw to mesh.txt.
mesh_ready() {
  local code ok=0
  : > "$OUT/mesh.txt"
  for code in "$REGION_A" "$REGION_B"; do
    mesh_status "$code" | mesh_judge "${PEER[$code]}" | sed "s/^/$code: /" >> "$OUT/mesh.txt" || ok=1
  done
  return "$ok"
}

hr_ready() { kr "$1" -n flux-system get helmrelease bp-cilium -o json | hr_judge > "$OUT/hr-$1.txt"; }

certs_ready() {
  # shellcheck disable=SC2086
  kr "$1" -n kube-system get certificate $CERTS -o json > "$OUT/certs-$1.json" 2> /dev/null || return 1
  # The Secrets hold private keys: they stay in the folder of the keys, which run removes.
  # shellcheck disable=SC2086
  kr "$1" -n kube-system get secret $CERTS -o json > "$KEYS/secrets-$1.json" 2> /dev/null || return 1
  cert_judge "$OUT/certs-$1.json" "$KEYS/secrets-$1.json" "$2" > "$OUT/cert-$1.txt"
}

# cert_field <region code> <secret> <openssl option>: the subject or issuer of the first
# certificate of the Secret's tls.crt, as openssl prints it.
cert_field() {
  kr "$1" -n kube-system get secret "$2" -o 'jsonpath={.data.tls\.crt}' | base64 -d | openssl x509 -noout "$3" -nameopt RFC2253
}

# dial <region code> <pod> <address>: one dial of <address>:8080 from inside the pod, through the
# region's API server; one line, the answer of netexec /dial.
dial() {
  local out
  out=$(kr "$1" get --raw "/api/v1/namespaces/$NS/pods/$2:8080/proxy/dial?request=hostname&protocol=http&host=$3&port=8080&tries=1" 2> /dev/null) \
    || out='{"errors":["the call through the API server failed"]}'
  printf '%s\n' "$out" | tr -d '\n'
  echo
}

# across <from code> <service> <to code>: the Service <service> dialled from the echo pod of the
# first region is answered by the pods of the Deployment <service> of the second region: first
# once within DIAL_LIMIT, then 5 times in a row.
across() {
  local from=$1 svc=$2 to=$3 pod ip want
  # The first dial may come before the remote backends are known; it is repeated until it is answered.
  pod=$(kr "$from" -n "$NS" get pods -l app=echo -o 'jsonpath={.items[0].metadata.name}')
  ip=$(kr "$from" -n "$NS" get service "$svc" -o 'jsonpath={.spec.clusterIP}')
  want=$(kr "$to" -n "$NS" get pods -l "app=$svc" -o 'jsonpath={.items[*].metadata.name}' | tr ' ' ',')
  # shellcheck disable=SC2016
  wait_for "$DIAL_LIMIT" eval 'dial "$from" "$pod" "$ip" | dial_judge "$want" > /dev/null' || true
  for _ in 1 2 3 4 5; do dial "$from" "$pod" "$ip"; done | dial_judge "$want"
}

# workloads: the test objects in both regions, and every Deployment available.
workloads() {
  local code n=1
  for code in "$REGION_A" "$REGION_B"; do
    workload_manifest "$n" | kr "$code" apply -f - > /dev/null || return 1
    kr "$code" -n "$NS" rollout status deployment/echo "deployment/solo-$n" --timeout="${ROLL_LIMIT}s" > /dev/null || return 1
    n=2
  done
}

# machines <region code>: the container names of the region's machines (one per node).
machines() {
  local node m
  for node in $(kr "$1" get nodes -o 'jsonpath={.items[*].metadata.name}'); do
    m=$(docker ps --format '{{.Names}}' | grep -x -- ".*-$node" || true)
    if [ -z "$m" ] || [ "$(printf '%s\n' "$m" | wc -l)" != 1 ]; then
      echo "node $node is not one machine: '$m'" >&2
      return 1
    fi
    echo "$m"
  done
}

resume_paused() {
  local m
  [ -s "$OUT/paused.txt" ] || return 0
  while read -r m; do docker unpause "$m" > /dev/null 2>&1 || true; done < "$OUT/paused.txt"
  : > "$OUT/paused.txt"
}

# run_case <case> <shape A|B> <auth mode>: one case, written to CASE_FILE. Runs in a subshell.
run_case() {
  local c=$1 shape=$2 mode=$3 d code own peer pip pport t snap ms at m expected fld ip want pod out
  d="$KEYS/$c"
  mkdir -p "$d"
  make_ca "$d" "$shape" || fail issuer "openssl could not make the authority"

  for code in "$REGION_A" "$REGION_B"; do
    issuer_manifest "$d/issuer-$code.crt" "$d/issuer-$code.key" | kr "$code" apply -f - > /dev/null \
      || fail issuer "$code: the Secret and the ClusterIssuer $ISSUER were not written"
  done
  for code in "$REGION_A" "$REGION_B"; do
    kr "$code" wait --for=condition=Ready "clusterissuer/$ISSUER" --timeout="${ISSUER_LIMIT}s" > /dev/null \
      || fail issuer "$code: ClusterIssuer $ISSUER is not Ready within ${ISSUER_LIMIT}s"
  done
  pass issuer "$ISSUER Ready in $REGION_A and $REGION_B"

  for code in "$REGION_A" "$REGION_B"; do
    read -r own peer pip pport <<< "$(mesh_names "$code" <<< "$REGISTRY")"
    layer_patch "$peer" "$pip" "$pport" "$mode" \
      | kr "$code" -n flux-system patch helmrelease bp-cilium --type merge --patch-file /dev/stdin > /dev/null \
      || fail layer "$code: the mesh layer was not written into HelmRelease bp-cilium"
  done
  for code in "$REGION_A" "$REGION_B"; do
    wait_for "$HR_LIMIT" hr_ready "$code" || fail layer "$code: $(cat "$OUT/hr-$code.txt" 2> /dev/null)"
  done
  pass layer "bp-cilium Ready with the layer in both: $(cat "$OUT/hr-$REGION_A.txt")"

  for code in "$REGION_A" "$REGION_B"; do
    # shellcheck disable=SC2086
    wait_for "$CERT_LIMIT" kr "$code" -n kube-system get certificate $CERTS -o name > /dev/null 2>&1 \
      || fail certificates "$code: the Certificates $CERTS do not exist within ${CERT_LIMIT}s"
    # A new authority does not renew a Certificate by itself: its Secret goes, cert-manager issues again.
    # shellcheck disable=SC2086
    kr "$code" -n kube-system delete secret $CERTS --ignore-not-found > /dev/null
  done
  for code in "$REGION_A" "$REGION_B"; do
    wait_for "$CERT_LIMIT" certs_ready "$code" "$d/root.crt" \
      || fail certificates "$code: $(tr '\n' ';' < "$OUT/cert-$code.txt" 2> /dev/null)"
    expected=$(openssl x509 -noout -subject -nameopt RFC2253 < "$d/issuer-$code.crt")
    fld=$(cert_field "$code" clustermesh-apiserver-server-cert -issuer)
    [ "${fld#issuer=}" = "${expected#subject=}" ] \
      || fail certificates "$code: the server certificate is issued by '${fld#issuer=}', not by '${expected#subject=}'"
  done
  pass certificates "Ready, issuer $ISSUER, ca.crt the root in both; the server certificate signed by $([ "$shape" = A ] && echo the root || echo "the region's intermediate")"

  for code in "$REGION_A" "$REGION_B"; do
    own=$(mesh_names "$code" <<< "$REGISTRY" | cut -d' ' -f1)
    if [ "$mode" = cluster ]; then expected="CN=remote-$own"; else expected="CN=remote"; fi
    fld=$(cert_field "$code" clustermesh-apiserver-remote-cert -subject)
    [ "${fld#subject=}" = "$expected" ] || fail mode "$code: the remote certificate is '${fld#subject=}', not '$expected'"
  done
  pass mode "tls.authMode $mode: the remote certificates name the expected user"

  for code in "$REGION_A" "$REGION_B"; do
    kr "$code" -n kube-system rollout restart deployment/clustermesh-apiserver daemonset/cilium > /dev/null \
      || fail mesh "$code: clustermesh-apiserver and cilium did not restart"
  done
  for code in "$REGION_A" "$REGION_B"; do
    for m in deployment/clustermesh-apiserver daemonset/cilium; do
      kr "$code" -n kube-system rollout status "$m" --timeout="${ROLL_LIMIT}s" > /dev/null \
        || fail mesh "$code: $m did not roll out within ${ROLL_LIMIT}s"
    done
  done
  t=$(date +%s)
  wait_for "$MESH_LIMIT" mesh_ready || fail mesh "not ready within ${MESH_LIMIT}s: $(tr '\n' ';' < "$OUT/mesh.txt")"
  ms=$(( $(date +%s) - t ))
  pass mesh "every agent reports the peer ready, ${ms}s after the restart"
  rec "INFO mesh ready in ${ms}s"

  workloads || fail global "the test Deployments did not become available"
  if ! { across "$REGION_A" solo-2 "$REGION_B" > "$OUT/across.txt" && across "$REGION_B" solo-1 "$REGION_A" >> "$OUT/across.txt"; }; then
    fail global "$(tr '\n' ';' < "$OUT/across.txt")"
  fi
  pass global "$(tr '\n' ';' < "$OUT/across.txt")"

  snap="$OUT/pods-$c"
  for code in "$REGION_A" "$REGION_B"; do
    kr "$code" get pods -A -o json | pod_lines "$NS" | sed "s|^|$code |" >> "$snap-before.txt" \
      || fail restarts "$code: no pod of the mesh or of the test was found"
  done
  m=$(machines "$REGION_B") || fail pause "the machines of $REGION_B were not found"
  pod=$(kr "$REGION_A" -n "$NS" get pods -l app=echo -o 'jsonpath={.items[0].metadata.name}')
  ip=$(kr "$REGION_A" -n "$NS" get service echo -o 'jsonpath={.spec.clusterIP}')
  want=$(kr "$REGION_A" -n "$NS" get pods -l app=echo -o 'jsonpath={.items[*].metadata.name}' | tr ' ' ',')
  printf '%s\n' "$m" > "$OUT/paused.txt"
  # shellcheck disable=SC2086
  docker pause $m > /dev/null || fail pause "docker pause $m failed"
  t=$(date +%s)
  log "case $c: $REGION_B paused (${m//$'\n'/ })"
  if kr "$REGION_B" --request-timeout=5s get --raw /readyz > /dev/null 2>&1; then
    miss pause "the API server of $REGION_B still answers while its machines are paused"
  fi
  for at in $PAUSE_AT; do
    while [ $(( $(date +%s) - t )) -lt "$at" ]; do sleep 5; done
    if out=$(for _ in $(seq "$DIALS"); do dial "$REGION_A" "$pod" "$ip"; done | dial_judge "$want"); then
      pass pause "${at}s into the pause of $REGION_B: $out"
    else
      miss pause "${at}s into the pause of $REGION_B: $out"
    fi
  done
  resume_paused
  log "case $c: $REGION_B resumed after $(( $(date +%s) - t ))s"

  t=$(date +%s)
  if wait_for "$MESH_LIMIT" mesh_ready; then
    ms=$(( $(date +%s) - t ))
    rec "INFO reconnect in ${ms}s"
    if across "$REGION_A" solo-2 "$REGION_B" > "$OUT/across.txt" && across "$REGION_B" solo-1 "$REGION_A" >> "$OUT/across.txt"; then
      pass resume "the peer ready again ${ms}s after the resume; $(tr '\n' ';' < "$OUT/across.txt")"
    else
      miss resume "the mesh is ready again after ${ms}s, but: $(tr '\n' ';' < "$OUT/across.txt")"
    fi
  else
    miss resume "not ready within ${MESH_LIMIT}s of the resume: $(tr '\n' ';' < "$OUT/mesh.txt")"
  fi

  for code in "$REGION_A" "$REGION_B"; do
    kr "$code" get pods -A -o json | pod_lines "$NS" | sed "s|^|$code |" >> "$snap-after.txt" || true
  done
  if out=$(restart_judge "$snap-before.txt" "$snap-after.txt"); then
    pass restarts "$(tail -1 <<< "$out")"
  else
    miss restarts "$(tr '\n' ';' <<< "$out")"
  fi
  rec END
}

# case_run <case> <shape> <auth mode>: run_case in a subshell; the paused machines resume after it
# whatever happened.
case_run() {
  local f="$OUT/case-$1.txt" rc
  echo "case $1 shape=$2 mode=$3" > "$f"
  log "case $1: authority of shape $2, tls.authMode $3"
  set +e
  (
    set -e
    CASE=$1
    CASE_FILE=$f
    run_case "$@"
  )
  rc=$?
  set -e
  resume_paused
  if [ "$rc" != 0 ] && ! grep -q '^FAIL ' "$f"; then echo "FAIL run: the case stopped with exit code $rc" >> "$f"; fi
  log "case $1: $(verdict "$f")"
}

cmd_run() {
  local c rc=0 shape
  [ -f "$DIR/env" ] || die "no env file $DIR/env: run scripts/local-pack.sh up --regions 2 first"
  # shellcheck disable=SC1091
  . "$DIR/env"
  if [ -z "${REGION_A:-}" ] || [ -z "${REGION_B:-}" ]; then die "the env file of the pack names no second region"; fi
  for c in python3 openssl kubectl tofu docker; do command -v "$c" > /dev/null || die "$c is not installed"; done
  REGISTRY=$(TF_DATA_DIR="$DIR/tofu-data" tofu -chdir="$ROOT/infra/providers/local" output -no-color \
    -state="$DIR/terraform.tfstate" -raw network_registry) || die "the module output network_registry could not be read"
  declare -g -A PEER=()
  for c in "$REGION_A" "$REGION_B"; do
    PEER[$c]=$(mesh_names "$c" <<< "$REGISTRY" | cut -d' ' -f2) || die "the network registry has no mesh entry for $c"
  done
  mkdir -p "$OUT"
  rm -f "$OUT"/case-*.txt "$OUT"/pods-*.txt "$OUT/summary.md"
  (umask 077 && mkdir -p "$KEYS")
  : > "$OUT/paused.txt"
  trap 'resume_paused; rm -rf "$KEYS"' EXIT
  for c in "$REGION_A" "$REGION_B"; do prepare "$c"; done
  case_run A A migration
  if [ "$(verdict "$OUT/case-A.txt")" = PASS ]; then
    case_run B B migration
    if [ "$(verdict "$OUT/case-B.txt")" = PASS ]; then shape=B; else shape=A; fi
    case_run C "$shape" cluster
  else
    printf 'case B shape=B mode=migration\nNOT RUN\n' > "$OUT/case-B.txt"
    printf 'case C shape=- mode=cluster\nNOT RUN\n' > "$OUT/case-C.txt"
  fi
  summary "$OUT" > "$OUT/summary.md" || rc=$?
  cat "$OUT/summary.md"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat "$OUT/summary.md" >> "$GITHUB_STEP_SUMMARY"; fi
  return "$rc"
}

# --- self-test ------------------------------------------------------------------------------

# The cases run their commands through eval, so the single quotes are meant and the variables are
# read there.
# shellcheck disable=SC2016,SC2034
self_test() {
  local n=0 bad=0 out t
  SELF_TEST_DIR=$(mktemp -d)
  trap 'rm -rf "$SELF_TEST_DIR"' EXIT
  t=$SELF_TEST_DIR
  # check <name> <expected exit code> <expected text or empty> <command>...
  check() {
    local name=$1 want=$2 text=$3 rc=0
    shift 3
    out=$("$@" 2>&1) || rc=$?
    n=$((n + 1))
    if [ "$rc" != "$want" ] || { [ -n "$text" ] && ! grep -q -F -- "$text" <<< "$out"; }; then
      bad=$((bad + 1))
      echo "FAIL $name: exit code $rc, expected $want; the output follows"
      printf '%s\n' "$out"
    else
      echo "ok   $name"
    fi
  }
  local reg='{"regions":[{"code":"region-a","clusterName":"sov-region-a","meshEndpoint":{"ips":["172.30.1.10"],"port":12379}},{"code":"region-b","clusterName":"sov-region-b","meshEndpoint":{"ips":["172.30.2.10"],"port":12379}}]}'
  check "names: the peer of region-a" 0 "sov-region-a sov-region-b 172.30.2.10 12379" eval 'mesh_names region-a <<< "$reg"'
  check "names: the peer of region-b" 0 "sov-region-b sov-region-a 172.30.1.10 12379" eval 'mesh_names region-b <<< "$reg"'
  check "names: an unknown region fails" 1 "no region region-c" eval 'mesh_names region-c <<< "$reg"'
  check "names: one region fails" 1 "has 1 region(s)" eval 'mesh_names region-a <<< "{\"regions\":[{\"code\":\"region-a\"}]}"'
  local noep='{"regions":[{"code":"region-a","clusterName":"sov-region-a","meshEndpoint":{"ips":["172.30.1.10"],"port":12379}},{"code":"region-b","clusterName":"sov-region-b"}]}'
  check "names: a peer without a mesh endpoint fails" 1 "region region-b has no cluster name or no mesh endpoint" eval 'mesh_names region-a <<< "$noep"'

  check "layer: migration names the issuer and the one peer" 0 '"certManagerIssuerRef": {"group": "cert-manager.io", "kind": "ClusterIssuer", "name": "sovereign-mesh-ca"}' \
    layer_patch sov-region-b 172.30.2.10 12379 migration
  check "layer: the peer by address, no tls block of its own" 0 '"clusters": [{"ips": ["172.30.2.10"], "name": "sov-region-b", "port": 12379}]' \
    layer_patch sov-region-b 172.30.2.10 12379 migration
  check "layer: mode cluster, KVStoreMesh and the API server on" 0 '"authMode": "cluster"' layer_patch p 10.0.0.1 1 cluster
  check "layer: KVStoreMesh on" 0 '"kvstoremesh": {"enabled": true}' layer_patch p 10.0.0.1 1 cluster
  check "layer: the slot's Service is not touched" 0 "" eval '! layer_patch p 10.0.0.1 1 cluster | grep -q -E "service|[Nn]ode[Pp]ort"'
  check "layer: mode legacy fails" 1 "neither migration nor cluster" layer_patch p 10.0.0.1 1 legacy
  check "layer: a name instead of an address fails" 1 "not an IP address" layer_patch p peer.example 1 migration
  check "layer: port 0 fails" 1 "not between 1 and 65535" layer_patch p 10.0.0.1 0 migration

  printf 'CERT\n' > "$t/c.pem"
  printf 'KEY\n' > "$t/k.pem"
  : > "$t/empty.pem"
  check "issuer: a tls Secret in cert-manager and a CA ClusterIssuer that reads it" 0 '"spec": {"ca": {"secretName": "sovereign-mesh-ca"}}' \
    issuer_manifest "$t/c.pem" "$t/k.pem"
  check "issuer: the Secret carries the files" 0 '"tls.crt": "Q0VSVAo=", "tls.key": "S0VZCg=="' issuer_manifest "$t/c.pem" "$t/k.pem"
  check "issuer: a missing file fails" 1 "issuer:" issuer_manifest "$t/none.pem" "$t/k.pem"
  check "issuer: an empty key fails" 1 "is empty" issuer_manifest "$t/c.pem" "$t/empty.pem"

  workload_manifest 1 > "$t/w1.json"
  workload_manifest 2 > "$t/w2.json"
  check "workload: cluster 1 runs echo and solo-1, cluster 2 echo and solo-2; three ClusterIP Services each" 0 "" python3 -c '
import json, sys
for f, own, other in ((sys.argv[1], "solo-1", "solo-2"), (sys.argv[2], "solo-2", "solo-1")):
    items = json.load(open(f))["items"]
    d = sorted(i["metadata"]["name"] for i in items if i["kind"] == "Deployment")
    s = {i["metadata"]["name"]: i for i in items if i["kind"] == "Service"}
    assert d == sorted(["echo", own]), d
    assert sorted(s) == ["echo", "solo-1", "solo-2"], sorted(s)
    assert all(x["spec"]["type"] == "ClusterIP" and x["metadata"]["annotations"]["service.cilium.io/global"] == "true" for x in s.values())
    assert s["echo"]["metadata"]["annotations"].get("service.cilium.io/affinity") == "local"
    assert "service.cilium.io/affinity" not in s[other]["metadata"]["annotations"]
' "$t/w1.json" "$t/w2.json"
  check "workload: no node port, every image pinned by digest" 0 "" eval '! grep -q -i nodeport "$t/w1.json" && python3 -c "import json,sys; i=[c[\"image\"] for x in json.load(open(sys.argv[1]))[\"items\"] if x[\"kind\"]==\"Deployment\" for c in x[\"spec\"][\"template\"][\"spec\"][\"containers\"]]; assert i and all(\"@sha256:\" in x for x in i), i" "$t/w1.json"'
  check "workload: cluster 3 fails" 1 "neither 1 nor 2" workload_manifest 3

  local okd='{"responses":["echo-1"]}' okd2='{"responses":["echo-2"]}' errd='{"errors":["dial tcp: i/o timeout"]}'
  check "dial: every dial answered by a wanted pod" 0 "2 of 2 dials answered by echo-1,echo-2" eval 'printf "%s\n" "$okd" "$okd2" | dial_judge echo-1,echo-2'
  check "dial: one dial answered by another pod fails" 1 "1 by another pod (echo-2)" eval 'printf "%s\n" "$okd" "$okd2" | dial_judge echo-1'
  check "dial: one failed dial fails" 1 "1 failed" eval 'printf "%s\n" "$okd" "$errd" | dial_judge echo-1'
  check "dial: an answer that is not JSON fails" 1 "1 failed" eval 'printf "%s\n" "not json" | dial_judge echo-1'
  check "dial: no dial fails" 1 "0 of 0" eval 'printf "" | dial_judge echo-1'
  check "dial: no wanted pod fails" 1 "nobody" eval 'printf "%s\n" "$okd" | dial_judge ""'

  local pods='{"items":[
    {"metadata":{"namespace":"kube-system","name":"cilium-x","uid":"u1","labels":{"k8s-app":"cilium"}},"status":{"containerStatuses":[{"restartCount":1},{"restartCount":2}]}},
    {"metadata":{"namespace":"kube-system","name":"cilium-operator-y","uid":"u2","labels":{"io.cilium/app":"operator"}},"status":{"containerStatuses":[{"restartCount":0}]}},
    {"metadata":{"namespace":"kube-system","name":"clustermesh-apiserver-z","uid":"u3","labels":{"k8s-app":"clustermesh-apiserver"}},"status":{}},
    {"metadata":{"namespace":"kube-system","name":"coredns-q","uid":"u4","labels":{"k8s-app":"kube-dns"}},"status":{}},
    {"metadata":{"namespace":"mesh-hermetic","name":"echo-w","uid":"u5"},"status":{"containerStatuses":[{"restartCount":0}]}}]}'
  check "pods: the mesh pods and the test pods with their restarts" 0 "kube-system/cilium-x u1 3" eval 'pod_lines mesh-hermetic <<< "$pods"'
  check "pods: three of five pods are read, not coredns and not the operator" 0 "" eval '[ "$(pod_lines mesh-hermetic <<< "$pods" | wc -l)" = 3 ] && ! pod_lines mesh-hermetic <<< "$pods" | grep -q -E "coredns|operator"'
  check "pods: no pod fails" 1 "" eval 'pod_lines mesh-hermetic <<< "{\"items\":[]}"'

  printf 'a kube-system/cilium-x u1 0\na mesh-hermetic/echo-w u5 0\n' > "$t/before.txt"
  cp "$t/before.txt" "$t/same.txt"
  printf 'a kube-system/cilium-x u1 1\na mesh-hermetic/echo-w u5 0\n' > "$t/restarted.txt"
  printf 'a kube-system/cilium-x u9 0\na mesh-hermetic/echo-w u5 0\n' > "$t/replaced.txt"
  : > "$t/none.txt"
  check "restarts: no pod restarted" 0 "2 pods compared, 0 changed" restart_judge "$t/before.txt" "$t/same.txt"
  check "restarts: a restart fails" 1 "after only:  a kube-system/cilium-x u1 1" restart_judge "$t/before.txt" "$t/restarted.txt"
  check "restarts: a replaced pod fails" 1 "after only:  a kube-system/cilium-x u9 0" restart_judge "$t/before.txt" "$t/replaced.txt"
  check "restarts: no pod read fails" 1 "" restart_judge "$t/none.txt" "$t/none.txt"

  check "mesh: every agent reports the peer ready" 0 "" eval 'printf "cilium-1 sov-b=true;\ncilium-2 sov-b=true;\n" | mesh_judge sov-b'
  check "mesh: one agent not ready fails" 1 "cilium-2: not ready sov-b (reports sov-b=false)" eval 'printf "cilium-1 sov-b=true;\ncilium-2 sov-b=false;\n" | mesh_judge sov-b'
  check "mesh: an agent that reports no peer fails" 1 "reports no peer" eval 'printf "cilium-1 \n" | mesh_judge sov-b'
  check "mesh: no agent fails" 1 "" eval 'printf "" | mesh_judge sov-b'

  local hr='{"metadata":{"generation":7},"status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":7,"reason":"UpgradeSucceeded"}]}}'
  check "release: Ready at the current generation" 0 "Ready=True for generation 7" eval 'hr_judge <<< "$hr"'
  check "release: Ready at an older generation fails" 1 "" eval 'hr_judge <<< "$(sed "s/\"observedGeneration\":7/\"observedGeneration\":6/" <<< "$hr")"'
  check "release: not Ready fails" 1 "Ready=False" eval 'hr_judge <<< "$(sed "s/\"True\"/\"False\"/" <<< "$hr")"'

  local rootb64 otherb64
  rootb64=$(printf 'root-der' | base64 -w0)
  otherb64=$(printf 'other-der' | base64 -w0)
  printf -- '-----BEGIN CERTIFICATE-----\n%s\n-----END CERTIFICATE-----\n' "$rootb64" > "$t/root.crt"
  printf -- '-----BEGIN CERTIFICATE-----\n%s\n-----END CERTIFICATE-----\n' "$otherb64" > "$t/other.crt"
  cert_files() {
    python3 -c '
import base64, json, sys
names, issuer, ready, ca, drop = sys.argv[1].split(), sys.argv[2], sys.argv[3], open(sys.argv[4]).read(), sys.argv[5]
c = [{"metadata": {"name": n}, "spec": {"issuerRef": {"name": issuer}}, "status": {"conditions": [{"type": "Ready", "status": ready}]}} for n in names]
s = [{"metadata": {"name": n}, "data": {"ca.crt": base64.b64encode(ca.encode()).decode()}} for n in names if n != drop]
json.dump({"items": c}, open(sys.argv[6], "w")); json.dump({"items": s}, open(sys.argv[7], "w"))' "$CERTS" "$1" "$2" "$3" "$4" "$t/certs.json" "$t/secrets.json"
  }
  check "certificates: Ready, the issuer, ca.crt the root" 0 "clustermesh-apiserver-server-cert: Ready=True issuer=sovereign-mesh-ca ca.crt=the root" \
    eval 'cert_files sovereign-mesh-ca True "$t/root.crt" none && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'
  check "certificates: ca.crt another certificate fails" 1 "ca.crt=1 other certificate(s)" \
    eval 'cert_files sovereign-mesh-ca True "$t/other.crt" none && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'
  check "certificates: ca.crt the root and one more fails" 1 "ca.crt=2 other certificate(s)" \
    eval 'cat "$t/root.crt" "$t/other.crt" > "$t/two.crt" && cert_files sovereign-mesh-ca True "$t/two.crt" none && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'
  check "certificates: not Ready fails" 1 "Ready=False" \
    eval 'cert_files sovereign-mesh-ca False "$t/root.crt" none && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'
  check "certificates: another issuer fails" 1 "issuer=cilium-ca" \
    eval 'cert_files cilium-ca True "$t/root.crt" none && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'
  check "certificates: a missing Secret fails" 1 "clustermesh-apiserver-local-cert: Ready=True issuer=sovereign-mesh-ca ca.crt=0 other" \
    eval 'cert_files sovereign-mesh-ca True "$t/root.crt" clustermesh-apiserver-local-cert && cert_judge "$t/certs.json" "$t/secrets.json" "$t/root.crt"'

  mkdir -p "$t/s1" "$t/s2" "$t/s3"
  printf 'case A shape=A mode=migration\nPASS issuer x\nINFO mesh ready in 40s\nEND\n' > "$t/s1/case-A.txt"
  printf 'case B shape=B mode=migration\nPASS issuer x\nFAIL mesh: not ready\n' > "$t/s1/case-B.txt"
  printf 'case C shape=A mode=cluster\nPASS issuer x\nEND\n' > "$t/s1/case-C.txt"
  printf 'case A shape=A mode=migration\nFAIL certificates: region-a: no\nEND\n' > "$t/s2/case-A.txt"
  printf 'case B shape=B mode=migration\nNOT RUN\n' > "$t/s2/case-B.txt"
  printf 'case A shape=A mode=migration\nPASS issuer x\n' > "$t/s3/case-A.txt"
  check "summary: case A passed, B failed, C passed" 0 "| B | one root, an intermediate per cluster | migration | FAIL | mesh: not ready |" summary "$t/s1"
  check "summary: the detail of a passing case" 0 "| A | one self-signed authority, the same key pair in both clusters | migration | PASS | mesh ready in 40s |" summary "$t/s1"
  check "summary: case C ran with the shape of A in mode cluster" 0 "| C | one self-signed authority, the same key pair in both clusters | cluster | PASS |" summary "$t/s1"
  check "summary: case A failed fails, B not run" 1 "| B | one root, an intermediate per cluster | migration | NOT RUN | case A failed |" summary "$t/s2"
  check "summary: a case that stopped before END fails" 1 "the case stopped before its end" summary "$t/s3"
  check "verdict: PASS, FAIL, NOT RUN" 0 "PASS FAIL NOT RUN" eval 'echo $(verdict "$t/s1/case-A.txt") $(verdict "$t/s1/case-B.txt") $(verdict "$t/s2/case-B.txt")'
  check "verdict: a FAIL line before END fails the case" 0 "FAIL" verdict "$t/s2/case-A.txt"
  check "verdict: a missing file is not run" 0 "NOT RUN" verdict "$t/none-such.txt"

  check "usage: no command" 2 "mesh-test.sh run" bash "${BASH_SOURCE[0]}"
  check "usage: an unknown command" 2 "mesh-test.sh run" bash "${BASH_SOURCE[0]}" up

  echo "self-test: $n cases, $bad failed"
  [ "$bad" = 0 ]
}

case "${1:-}" in
  run) cmd_run ;;
  --self-test) self_test ;;
  *) usage ;;
esac
