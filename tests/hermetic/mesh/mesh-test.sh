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
#   restarts      the pods of the mesh (the Cilium agents and clustermesh-apiserver) and of the test
#                 are the same from before the pause to after resume, and every container of them has
#                 exactly the restarts the platform makes: 0 before the pause; after the resume 0,
#                 except when the second cluster was frozen longer than the lock lease (LOCK_LEASE):
#                 then its clustermesh-apiserver container kvstoremesh has exactly 1, its last
#                 termination exit code 255 and the last line of its previous log, past the JSON
#                 lines of its etcd client, is LEASE_LOST (last_log_line). The
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
# cluster, every Flux Kustomization that applies bp-cilium, directly or through the one that applies
# it, the outermost first, so that the kit does not take the layer back, and deletes the Secret kube-system/cilium-clustermesh that up wrote, which the chart writes
# from then on. The authority keys are made with openssl in a folder of mode 700 under
# MESH_TEST_DIR and removed when run ends. When case A fails, B and C are not run.
# Exit codes: 0 case A passed (B and C are reported, whatever their result); 1 case A failed or the
# run could not start; 2 wrong usage. run needs bash, jq, openssl, kubectl, tofu and docker;
# --self-test needs bash and jq only. Nothing here creates a Service of type NodePort or
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
# The lease of the leader lock that KVStoreMesh holds on the etcd beside it: Cilium 1.19.3,
# pkg/defaults/defaults.go:318 (LockLeaseTTL = 25 * time.Second; no flag, no chart value). A
# process frozen longer than that finds the lease gone when it wakes, and KVStoreMesh stops on
# purpose (clustermesh-apiserver/kvstoremesh/root.go:100-102) with exit code 255 and this line.
LOCK_LEASE=25
LEASE_LOST='level=fatal msg="Leader election lost"'
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
  jq -s -r --arg code "$1" '
    def die($m): "\($m)\n" | halt_error(1);
    def ok: (.clusterName | type) == "string" and .clusterName != ""
      and ((.meshEndpoint.ips // []) | length) > 0
      and (.meshEndpoint.port | type) == "number" and .meshEndpoint.port > 0
      and .meshEndpoint.port == (.meshEndpoint.port | floor);
    (.[0].regions // []) as $regs
    | if ($regs | length) != 2 then die("mesh: the network registry has \($regs | length) region(s), the test needs 2") else . end
    | [$regs[] | select(.code == $code)] as $me
    | [$regs[] | select(.code != $code)] as $peer
    | if ($me | length) != 1 or ($peer | length) != 1 then die("mesh: the network registry has no region \($code)") else . end
    | [($me + $peer)[] | select(ok | not)] as $bad
    | if ($bad | length) > 0 then die("mesh: region \($bad[0].code) has no cluster name or no mesh endpoint") else . end
    | "\($me[0].clusterName) \($peer[0].clusterName) \($peer[0].meshEndpoint.ips[0]) \($peer[0].meshEndpoint.port)"'
}

# is_ip <text>: exit 0 when the text is an IPv4 address in dotted-decimal form or an IPv6 address.
is_ip() {
  local o
  if [[ $1 =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]]; then
    for o in "${BASH_REMATCH[@]:1}"; do [ "$((10#$o))" -le 255 ] || return 1; done
    return 0
  fi
  [[ $1 == *:* && $1 =~ ^[0-9A-Fa-f:.]+$ ]]
}

# layer_patch <peer cluster name> <peer address> <peer port> <auth mode>: stdout the JSON merge
# patch of the HelmRelease flux-system/bp-cilium that lays the mesh layer over the values of slot
# 01: the API server stays on, clustermesh.config names the one peer by address (the chart writes
# the host alias <peer>.mesh.cilium.io), KVStoreMesh is on, and the certificates of the API server
# come from the ClusterIssuer sovereign-mesh-ca with the given tls.authMode. The slot's Service of
# the API server is not touched. Exit 1 when the mode is not migration or cluster, the address is
# not an IP address or the port is not between 1 and 65535.
layer_patch() {
  local name=$1 ip=$2 port=$3 mode=$4
  case $mode in
    migration | cluster) ;;
    *) echo "layer: the auth mode '$mode' is neither migration nor cluster" >&2; return 1 ;;
  esac
  is_ip "$ip" || { echo "layer: '$ip' is not an IP address" >&2; return 1; }
  if [ -z "$name" ] || ! [[ $port =~ ^0*([0-9]{1,5})$ ]] || [ "${BASH_REMATCH[1]}" -lt 1 ] || [ "${BASH_REMATCH[1]}" -gt 65535 ]; then
    echo "layer: no peer name, or the port '$port' is not between 1 and 65535" >&2
    return 1
  fi
  jq -n -c -S --arg name "$name" --arg ip "$ip" --argjson port "${BASH_REMATCH[1]}" --arg mode "$mode" --arg issuer "$ISSUER" '
    {spec: {values: {cilium: {clustermesh: {
      useAPIServer: true,
      config: {enabled: true, domain: "mesh.cilium.io", clusters: [{name: $name, port: $port, ips: [$ip]}]},
      apiserver: {kvstoremesh: {enabled: true},
                  tls: {authMode: $mode,
                        auto: {enabled: true, method: "certmanager",
                               certManagerIssuerRef: {group: "cert-manager.io", kind: "ClusterIssuer", name: $issuer}}}}}}}}}'
}

# issuer_manifest <certificate file> <key file>: stdout a List of the Secret <CA_NS>/<ISSUER> of
# type kubernetes.io/tls (tls.crt the certificate file: the authority, or an intermediate followed
# by its root; tls.key the key file) and the ClusterIssuer <ISSUER> of kind CA that reads it.
# Exit 1 when a file is missing or empty.
issuer_manifest() {
  local f
  for f in "$1" "$2"; do
    if ! { [ -f "$f" ] && [ -r "$f" ]; }; then echo "issuer: $f cannot be read" >&2; return 1; fi
    grep -q '[^[:space:]]' "$f" || { echo "issuer: $f is empty" >&2; return 1; }
  done
  jq -n -c -S --arg crt "$(base64 -w0 < "$1")" --arg key "$(base64 -w0 < "$2")" --arg ns "$CA_NS" --arg name "$ISSUER" '
    {"app.kubernetes.io/managed-by": "mesh-hermetic-test"} as $labels
    | {apiVersion: "v1", kind: "List", items: [
        {apiVersion: "v1", kind: "Secret", type: "kubernetes.io/tls",
         metadata: {name: $name, namespace: $ns, labels: $labels}, data: {"tls.crt": $crt, "tls.key": $key}},
        {apiVersion: "cert-manager.io/v1", kind: "ClusterIssuer",
         metadata: {name: $name, labels: $labels}, spec: {ca: {secretName: $name}}}]}'
}

# workload_manifest <1|2>: stdout a List of the test objects of the first or second cluster in
# namespace NS. Deployment echo in both clusters; Deployment solo-<n> only in cluster n. Services
# echo (global, affinity local), solo-1 and solo-2 (global) in both, each of type ClusterIP on
# port 8080. Every pod runs IMAGE (pinned by digest) as netexec: /hostname answers with the pod
# name, /dial dials an address from inside the pod. Exit 1 when the argument is not 1 or 2.
workload_manifest() {
  case $1 in
    1 | 2) ;;
    *) echo "workload: the cluster number '$1' is neither 1 nor 2" >&2; return 1 ;;
  esac
  jq -n -c -S --arg n "$1" --arg ns "$NS" --arg image "$IMAGE" '
    def deploy($name): {app: $name} as $sel
      | {apiVersion: "apps/v1", kind: "Deployment", metadata: {name: $name, namespace: $ns, labels: $sel},
         spec: {replicas: 1, selector: {matchLabels: $sel}, template: {metadata: {labels: $sel}, spec: {
           containers: [{name: "netexec", image: $image, args: ["netexec", "--http-port=8080"],
             ports: [{name: "http", containerPort: 8080, protocol: "TCP"}],
             readinessProbe: {httpGet: {path: "/readyz", port: 8080}, periodSeconds: 5},
             livenessProbe: {httpGet: {path: "/healthz", port: 8080}, periodSeconds: 10},
             resources: {requests: {cpu: "10m", memory: "16Mi"}, limits: {memory: "64Mi"}},
             securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true,
                               runAsNonRoot: true, runAsUser: 65534, runAsGroup: 65534,
                               capabilities: {drop: ["ALL"]}}}],
           automountServiceAccountToken: false,
           securityContext: {runAsNonRoot: true, seccompProfile: {type: "RuntimeDefault"}}}}}};
    def service($name; $local): {apiVersion: "v1", kind: "Service",
      metadata: {name: $name, namespace: $ns,
                 annotations: ({"service.cilium.io/global": "true", "service.cilium.io/shared": "true"}
                               + (if $local then {"service.cilium.io/affinity": "local"} else {} end))},
      spec: {type: "ClusterIP", selector: {app: $name},
             ports: [{name: "http", port: 8080, targetPort: 8080, protocol: "TCP"}]}};
    {apiVersion: "v1", kind: "List", items: [{apiVersion: "v1", kind: "Namespace", metadata: {name: $ns}},
      deploy("echo"), deploy("solo-" + $n), service("echo"; true), service("solo-1"; false), service("solo-2"; false)]}'
}

# dial_judge <pod name>[,<pod name>...]: stdin has one line per dial, the answer of netexec /dial
# with tries=1. Prints how the dials were answered; exit 0 when there is at least one dial and
# every dial was answered by one of the named pods.
dial_judge() {
  jq -R -s -r --arg want "$1" '
    def truthy: . != null and . != false and . != 0 and . != "" and . != [] and . != {};
    def trim: sub("^\\s+"; "") | sub("\\s+$"; "");
    ($want | split(",") | map(select(. != "")) | unique) as $w
    | [split("\n")[] | select(test("\\S"))] as $lines
    | [$lines[] | (try fromjson catch null) as $d
       | (if ($d | type) == "object" and ($d.responses | truthy) then $d.responses else [] end) as $r
       | if ($r | type) == "array" and ($r | length) == 1 and ($d.errors | truthy | not)
         then ($r[0] | tostring | trim) as $x | if any($w[]; . == $x) then {ok: $x} else {other: $x} end
         else {failed: true} end] as $v
    | [$v[] | select(.ok != null)] as $ok
    | [$v[] | select(.other != null) | .other] as $other
    | "\($ok | length) of \($lines | length) dials answered by \(if ($w | length) > 0 then ($w | join(",")) else "nobody" end), \($other | length) by another pod\(if ($other | length) > 0 then " (\($other | unique | join(",")))" else "" end), \([$v[] | select(.failed)] | length) failed",
      (if ($lines | length) > 0 and ($w | length) > 0 and ($ok | length) == ($lines | length) then empty else "" | halt_error(1) end)'
}

# JQ_PODS: the jq function pods($ns) over `kubectl get pods -A -o json` (slurped): the pods of the
# mesh (in kube-system, label k8s-app cilium or clustermesh-apiserver) and of the test namespace.
# shellcheck disable=SC2016 # a jq program: its $ns is a jq variable
JQ_PODS='def pods($ns): .[0].items[]? | select(.metadata.namespace == $ns
  or (.metadata.namespace == "kube-system" and (.metadata.labels["k8s-app"] as $a | $a == "cilium" or $a == "clustermesh-apiserver")));'

# pod_lines <test namespace>: stdin is `kubectl get pods -A -o json`; stdout one line per pod of the
# mesh (in kube-system, label k8s-app cilium or clustermesh-apiserver) and per pod of the test
# namespace: "<namespace>/<name> <uid> <container>=<restarts>,...", the containers sorted by name
# ("-" when there is none), sorted. Exit 1 when there is no such pod.
pod_lines() {
  jq -s -r --arg ns "$1" "$JQ_PODS"' if length == 0 then "" | halt_error(1) else . end |
    [pods($ns) | "\(.metadata.namespace)/\(.metadata.name) \(.metadata.uid) \(
       [.status.containerStatuses[]? | "\(.name)=\(.restartCount // 0)"] | sort | if length > 0 then join(",") else "-" end)"]
    | sort | join("\n"), (if length > 0 then empty else "" | halt_error(1) end)'
}

# restart_lines <test namespace>: stdin is `kubectl get pods -A -o json`; stdout one line per
# container that restarted, of the pods that pod_lines reads: "<namespace>/<pod> <container>
# <restarts> <exit code of its last termination, - when none is recorded>", sorted.
restart_lines() {
  jq -s -r --arg ns "$1" "$JQ_PODS"' if length == 0 then "" | halt_error(1) else . end |
    [pods($ns) | .metadata as $m | .status.containerStatuses[]? | select((.restartCount // 0) != 0)
     | "\($m.namespace)/\($m.name) \(.name) \(.restartCount) \(.lastState.terminated.exitCode // "-")"]
    | sort | join("\n")'
}

# last_log_line: stdin is the previous log of a container; stdout its last line that is not blank
# and not a JSON record, cut at 500 characters. kvstoremesh writes through two loggers, Cilium's
# (logfmt, "time=... level=...") and the zap logger of its etcd client (one JSON object per line),
# and a JSON line of the etcd client can follow the fatal line of Cilium's logger.
last_log_line() {
  { grep -v -E '^[[:space:]]*($|\{)' || true; } | tail -n 1 | cut -c1-500
}

# restart_judge <before> <after> <restarts> <frozen region> <seconds frozen>: the first two files
# are pod_lines, each line led by the region code, before the pause and after the resume; the third
# has one line per restarted container, "<region> <namespace>/<pod> <container> restarts=<n>
# exit=<code> last=<last_log_line of its previous log>". The rule is what the platform does, as
# exact values: the same pods before and after; every container at 0 restarts before the pause; after
# the resume every container at 0, except when the region was frozen longer than LOCK_LEASE seconds:
# then the container kvstoremesh of its one clustermesh-apiserver pod is at exactly 1, with exit code
# 255 and LEASE_LOST as that line. Prints every difference; the last line is the
# verdict; exit 0 when there is none.
restart_judge() {
  local f
  for f in "$1" "$2" "$3"; do if ! { [ -f "$f" ] && [ -r "$f" ]; }; then echo "restarts: $f cannot be read" >&2; return 1; fi; done
  [[ $5 =~ ^[0-9]+$ ]] || { echo "restarts: the seconds frozen '$5' are not a number" >&2; return 1; }
  jq -n -r --rawfile before "$1" --rawfile after "$2" --rawfile restarts "$3" --arg frozen "$4" \
    --argjson secs "$((10#$5))" --argjson lease "$LOCK_LEASE" --arg lost "$LEASE_LOST" '
    def lines: split("\n") | map(select(test("\\S")));
    def trim: sub("^\\s+"; "") | sub("\\s+$"; "");
    def quote: "'" + . + "'";
    def sorted_entries: to_entries | sort_by(.key);
    # "<container>=<restarts>,..." or "-" as an object; an error when a part is not that.
    def counts: if . == "-" then {} else
      reduce (split(",")[] | (index("=") // error("no =")) as $i
              | {key: .[:$i], value: (.[$i + 1:] | if test("^[+-]?[0-9]+$") then ltrimstr("+") | tonumber else error("no number") end)}) as $e
        ({}; .[$e.key] = $e.value) end;
    # {pods: {"<region> <pod>": {uid, cs: {<container>: <restarts>}}}, bad: [unreadable lines]}
    def pods: reduce lines[] as $l ({pods: {}, bad: []};
      ($l | [splits("\\s+")] | map(select(. != ""))) as $x
      | (if ($x | length) < 4 then null else (try {key: "\($x[0]) \($x[1])", uid: $x[2], cs: ($x[3] | counts)} catch null) end) as $p
      | if $p == null then .bad += ["unreadable line: " + $l] else .pods[$p.key] = {uid: $p.uid, cs: $p.cs} end);
    ($before | pods) as $A | ($after | pods) as $B | $A.pods as $a | $B.pods as $b
    | (reduce ($restarts | lines[] | capture("^(?<r>\\S+) (?<p>\\S+) (?<c>\\S+) restarts=\\S+ exit=(?<code>\\S+) last=(?<last>.*)$")) as $m
        ({}; .["\($m.r) \($m.p) \($m.c)"] = {code: $m.code, last: ($m.last | trim)})) as $ev
    | (if $secs > $lease then
         [$b | to_entries[] | (.key | split(" ")) as $k
          | select($k[0] == $frozen and ($k[1] | startswith("kube-system/clustermesh-apiserver-")) and (.value.cs | has("kvstoremesh")))
          | .key] | sort
       else null end) as $cm
    | (if $cm != null and ($cm | length) == 1 then {($cm[0] + " kvstoremesh"): 1} else {} end) as $want
    | ($A.bad + $B.bad
       + (if ($a | length) == 0 or ($b | length) == 0 then ["no pod read"] else [] end)
       + [($a + $b) | keys[] as $k
          | if $b[$k] == null then "gone: \($k)"
            elif $a[$k] == null then "new: \($k)"
            elif $a[$k].uid != $b[$k].uid then "replaced: \($k) (uid \($a[$k].uid), then \($b[$k].uid))"
            else empty end]
       + [$a | sorted_entries[] | .key as $k | .value.cs | sorted_entries[] | select(.value != 0)
          | "before the pause: \($k) \(.key)=\(.value), expected 0"]
       + (if $cm != null and ($cm | length) != 1
          then ["\($frozen) was frozen \($secs)s, longer than the lock lease of \($lease)s, and has \($cm | length) clustermesh-apiserver pods with a container kvstoremesh, not 1"]
          else [] end)
       + [$b | sorted_entries[] | .key as $k | .value.cs | sorted_entries[] | ($want["\($k) \(.key)"] // 0) as $w
          | select(.value != $w) | "\($k) \(.key)=\(.value) restarts, expected \($w)"]
       + [$want | keys[] as $k | ($k | split(" ")) as $p | select($b["\($p[0]) \($p[1])"].cs[$p[2]] == 1)
          | if $ev[$k] == null then "\($k): no last termination and no previous log read"
            else (if $ev[$k].code != "255" then "\($k): exit code \($ev[$k].code), expected 255" else empty end),
                 (if $ev[$k].last == $lost or ($ev[$k].last | test("^time=\\S+ ") and sub("^time=\\S+ "; "") == $lost) then empty
                  else "\($k): the previous log ends \($ev[$k].last | quote), expected \($lost | quote)" end) end]) as $bad
    | $bad[],
      (if ($bad | length) > 0 then "\($a | length) pods compared, \($bad | length) difference(s)"
       elif ($want | length) > 0 then "\($a | length) pods compared; \($want | keys[0]) restarted once, exit 255, its previous log ends \($lost | quote): \($frozen) was frozen \($secs)s, longer than the lock lease of \($lease)s; every other container 0 restarts"
       else "\($a | length) pods compared, every container 0 restarts (frozen \($secs)s, not longer than the lock lease of \($lease)s)" end),
      (if ($bad | length) > 0 then "" | halt_error(1) else empty end)'
}

# mesh_judge <peer cluster name>...: stdin has one line per Cilium agent of a cluster, "<pod> <what
# MESH_STATUS printed>". Prints one line per agent that does not report every named peer ready;
# exit 0 when there is at least one agent and every agent reports every named peer ready.
mesh_judge() {
  jq -R -s -r '
    $ARGS.positional as $peers
    | [split("\n")[] | select(test("\\S")) | capture("^\\s*(?<pod>\\S+)\\s*(?<rest>.*)$")] as $lines
    | [$lines[] | .pod as $pod
       | (reduce (.rest | split(";")[] | index("=") as $i | select($i != null) | {key: .[:$i], value: .[$i + 1:]}) as $e
           ({}; .[$e.key] = $e.value)) as $seen
       | [$peers[] | select($seen[.] != "true")] as $missing
       | select($missing | length > 0)
       | "\($pod): not ready \($missing | join(",")) (reports \([$seen | to_entries | sort_by(.key)[] | "\(.key)=\(.value)"]
           | if length > 0 then join(";") else "no peer" end))"] as $bad
    | $bad[], (if ($lines | length) > 0 and ($peers | length) > 0 and ($bad | length) == 0 then empty else "" | halt_error(1) end)' \
    --args "$@"
}

# hr_judge: stdin is the HelmRelease as JSON. Prints its Ready condition; exit 0 when Ready is True
# for the current generation (the condition's observedGeneration equals metadata.generation).
hr_judge() {
  jq -s -r '
    def s: if . == null then "None" else tostring end;
    if length == 0 then "" | halt_error(1) else . end | .[0] as $h | $h.metadata.generation as $gen
    | (first($h.status.conditions[]? | select(.type == "Ready")) // {}) as $c
    | "generation \($gen | s): Ready=\($c.status | s) for generation \($c.observedGeneration | s), \($c.reason | s): \(($c.message // "") | .[:300])",
      (if $c.status == "True" and $gen != null and $c.observedGeneration == $gen then empty else "" | halt_error(1) end)'
}

# flux_owners: stdin is `kubectl get helmreleases,kustomizations -A -o json` of a cluster (a List).
# Prints "<namespace> <name>" of every Flux Kustomization that applies HelmRelease
# flux-system/bp-cilium, directly or through the Kustomization that applies it, the outermost first.
# Flux labels each object it applies with kustomize.toolkit.fluxcd.io/name and /namespace. Since
# SOV-154 the root Kustomization sovereign-root applies bootstrap-kit and takes back a suspension of
# bootstrap-kit alone, and with it the layer. Exit 1 when bp-cilium is missing, a named
# Kustomization is missing, or the chain loops.
flux_owners() {
  jq -r '
    def owner($ks): (.metadata.labels // {}) as $l | $l["kustomize.toolkit.fluxcd.io/name"] as $n
      | if ($n // "") == "" then empty
        else ($l["kustomize.toolkit.fluxcd.io/namespace"] // "flux-system") as $s
          | first($ks[] | select(.metadata.name == $n and .metadata.namespace == $s))
            // ("owners: the Flux Kustomization \($s)/\($n) is not in the cluster\n" | halt_error(1))
        end;
    [.items[]? | select(.kind == "Kustomization")] as $ks
    | (first(.items[]? | select(.kind == "HelmRelease" and .metadata.namespace == "flux-system" and .metadata.name == "bp-cilium"))
       // ("owners: no HelmRelease flux-system/bp-cilium\n" | halt_error(1)))
    | [limit(9; recurse(owner($ks)))] as $chain
    | if ($chain | length) > 8 then "owners: the Flux Kustomizations above bp-cilium form a loop\n" | halt_error(1) else . end
    | $chain[1:] | reverse[] | "\(.metadata.namespace) \(.metadata.name)"'
}

# cert_judge <certificates file> <secrets file> <root file>: the first two files are `kubectl get
# certificate,secret -o json` of the names in CERTS, the third the root of the case (PEM). Prints
# one line per name; exit 0 when each Certificate is Ready and names the issuer ISSUER, and the
# ca.crt of each Secret is exactly one certificate, the root (compared as DER, by the base64 text
# of each PEM block without its line breaks).
cert_judge() {
  jq -n -r --slurpfile certs "$1" --slurpfile secrets "$2" --rawfile root "$3" --arg names "$CERTS" --arg issuer "$ISSUER" '
    def ders: [match("-----BEGIN CERTIFICATE-----([^-]*)-----END CERTIFICATE-----"; "g") | .captures[0].string | gsub("\\s"; "")];
    def items: reduce (.[0].items[]?) as $i ({}; .[$i.metadata.name] = $i);
    ($root | ders) as $want
    | if ($want | length) != 1 then "certificates: the root file holds \($want | length) certificates, not 1\n" | halt_error(1) else . end
    | ($certs | items) as $cs | ($secrets | items) as $ss
    | [$names | split(" ")[] | select(. != "") | . as $n | ($cs[$n] // {}) as $c | ($ss[$n] // {}) as $s
       | [$c.status.conditions[]? | select(.type == "Ready") | .status] as $ready
       | $c.spec.issuerRef.name as $ref
       | ((try ($s.data["ca.crt"] // "" | @base64d) catch "") | ders) as $ca
       | {line: "\($n): Ready=\(if ($ready | length) > 0 then ($ready | join(",")) else "absent" end) issuer=\($ref // "None") ca.crt=\(
           if $ca == $want then "the root" else "\($ca | length) other certificate(s)" end)",
          good: ($ready == ["True"] and $ref == $issuer and $ca == $want)}]
    | (.[].line), (if all(.good) then empty else "" | halt_error(1) end)'
}

# verdict <case file>: PASS when the case ran to its END line with no FAIL line, NOT RUN when the
# file says so or is missing, FAIL otherwise.
verdict() {
  if ! { [ -f "$1" ] && [ -r "$1" ]; } || grep -q -x -F 'NOT RUN' "$1"; then
    echo "NOT RUN"
  elif grep -q -x -F END "$1" && ! grep -q '^FAIL ' "$1"; then
    echo PASS
  else
    echo FAIL
  fi
}

# summary <out dir>: stdout the summary table of the cases A, B and C; exit 0 when case A passed.
summary() {
  local d=$1 c f x shape mode res why authority ok=1 rows=() head=()
  for c in A B C; do
    f="$d/case-$c.txt"
    shape='' mode=- head=()
    if [ -f "$f" ] && [ -r "$f" ]; then read -r -a head < "$f" || true; fi
    if [ "${head[0]:-}" = case ]; then
      for x in "${head[@]:2}"; do
        case $x in
          shape=*) shape=${x#shape=} ;;
          mode=*) mode=${x#mode=} ;;
        esac
      done
    fi
    if ! { [ -f "$f" ] && [ -r "$f" ]; } || grep -q -x -F 'NOT RUN' "$f"; then
      res="NOT RUN"
      if [ "$c" = A ]; then why="not started"; else why="case A failed"; fi
    elif grep -q -x -F END "$f" && ! grep -q '^FAIL ' "$f"; then
      res=PASS
      why=$(awk '/^INFO /{printf "%s%s", (n++ ? "; " : ""), substr($0, 6)}' "$f")
    else
      res=FAIL
      if grep -q '^FAIL ' "$f"; then why=$(grep -m 1 '^FAIL ' "$f" | cut -c6-); else why="the case stopped before its end"; fi
    fi
    if [ "$c" = A ] && [ "$res" = PASS ]; then ok=0; fi
    case $shape in
      A) authority="one self-signed authority, the same key pair in both clusters" ;;
      B) authority="one root, an intermediate per cluster" ;;
      *) authority=- ;;
    esac
    rows+=("| $c | $authority | $mode | $res | ${why//|//} |")
  done
  echo "### Hermetic mesh test on the local pack"
  echo
  echo "| Case | Authority | tls.authMode | Result | Detail |"
  echo "|---|---|---|---|---|"
  printf '%s\n' "${rows[@]}"
  echo
  echo "Case A must pass. Case B decides whether each region may hold its own intermediate under one root."
  return "$ok"
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

# prepare <region code>: suspends every Flux Kustomization that applies bp-cilium (flux_owners), the
# outermost first, and deletes the Secret kube-system/cilium-clustermesh when Helm does not own it
# (up wrote it).
prepare() {
  local code=$1 owners owner ons managed
  owners=$(kr "$code" get helmreleases.helm.toolkit.fluxcd.io,kustomizations.kustomize.toolkit.fluxcd.io -A -o json | flux_owners) \
    || die "$code: the Flux Kustomizations that apply bp-cilium could not be read"
  while read -r ons owner; do
    [ -n "$owner" ] || continue
    kr "$code" -n "$ons" patch kustomizations.kustomize.toolkit.fluxcd.io "$owner" --type merge -p '{"spec":{"suspend":true}}' > /dev/null
    log "$code: Flux Kustomization $ons/$owner suspended"
  done <<< "$owners"
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
    kr "$code" -n kube-system delete secret $CERTS --ignore-not-found --wait=false > /dev/null
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
  frozen=$(( $(date +%s) - t ))
  log "case $c: $REGION_B resumed after ${frozen}s"

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

  # The exit code and the last_log_line of the previous log of every container that restarted (none
  # when restart_lines prints its one empty line). kubectl reads stdin, so each call gets /dev/null
  # and leaves the list to the loop.
  : > "$snap-restarts.txt"
  for code in "$REGION_A" "$REGION_B"; do
    pods=$(kr "$code" get pods -A -o json || true)
    pod_lines "$NS" <<< "$pods" | sed "s|^|$code |" >> "$snap-after.txt" || true
    restart_lines "$NS" <<< "$pods" | while read -r p ctr n x; do
      [ -n "$p" ] || continue
      last=$(kr "$code" -n "${p%%/*}" logs "${p#*/}" -c "$ctr" --previous --tail=100 < /dev/null 2> /dev/null | last_log_line || true)
      printf '%s %s %s restarts=%s exit=%s last=%s\n' "$code" "$p" "$ctr" "$n" "$x" "$last"
    done >> "$snap-restarts.txt" || true
  done
  if out=$(restart_judge "$snap-before.txt" "$snap-after.txt" "$snap-restarts.txt" "$REGION_B" "$frozen"); then
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
  for c in jq openssl kubectl tofu docker; do command -v "$c" > /dev/null || die "$c is not installed"; done
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

  check "layer: migration names the issuer and the one peer" 0 '"certManagerIssuerRef":{"group":"cert-manager.io","kind":"ClusterIssuer","name":"sovereign-mesh-ca"}' \
    layer_patch sov-region-b 172.30.2.10 12379 migration
  check "layer: the peer by address, no tls block of its own" 0 '"clusters":[{"ips":["172.30.2.10"],"name":"sov-region-b","port":12379}]' \
    layer_patch sov-region-b 172.30.2.10 12379 migration
  check "layer: mode cluster, KVStoreMesh and the API server on" 0 '"authMode":"cluster"' layer_patch p 10.0.0.1 1 cluster
  check "layer: KVStoreMesh on" 0 '"kvstoremesh":{"enabled":true}' layer_patch p 10.0.0.1 1 cluster
  check "layer: the slot's Service is not touched" 0 "" eval '! layer_patch p 10.0.0.1 1 cluster | grep -q -E "service|[Nn]ode[Pp]ort"'
  check "layer: mode legacy fails" 1 "neither migration nor cluster" layer_patch p 10.0.0.1 1 legacy
  check "layer: a name instead of an address fails" 1 "not an IP address" layer_patch p peer.example 1 migration
  check "layer: port 0 fails" 1 "not between 1 and 65535" layer_patch p 10.0.0.1 0 migration

  printf 'CERT\n' > "$t/c.pem"
  printf 'KEY\n' > "$t/k.pem"
  : > "$t/empty.pem"
  check "issuer: a tls Secret in cert-manager and a CA ClusterIssuer that reads it" 0 '"spec":{"ca":{"secretName":"sovereign-mesh-ca"}}' \
    issuer_manifest "$t/c.pem" "$t/k.pem"
  check "issuer: the Secret carries the files" 0 '"tls.crt":"Q0VSVAo=","tls.key":"S0VZCg=="' issuer_manifest "$t/c.pem" "$t/k.pem"
  check "issuer: a missing file fails" 1 "issuer:" issuer_manifest "$t/none.pem" "$t/k.pem"
  check "issuer: an empty key fails" 1 "is empty" issuer_manifest "$t/c.pem" "$t/empty.pem"

  workload_manifest 1 > "$t/w1.json"
  workload_manifest 2 > "$t/w2.json"
  check "workload: cluster 1 runs echo and solo-1, cluster 2 echo and solo-2; three ClusterIP Services each" 0 "" \
    jq -e -n --slurpfile w1 "$t/w1.json" --slurpfile w2 "$t/w2.json" '
      def fits($own; $other): .items as $i | [$i[] | select(.kind == "Service")] as $s
        | ([$i[] | select(.kind == "Deployment") | .metadata.name] | sort) == (["echo", $own] | sort)
          and ([$s[] | .metadata.name] | sort) == ["echo", "solo-1", "solo-2"]
          and all($s[]; .spec.type == "ClusterIP" and .metadata.annotations["service.cilium.io/global"] == "true")
          and [$s[] | select(.metadata.name == "echo") | .metadata.annotations["service.cilium.io/affinity"]] == ["local"]
          and [$s[] | select(.metadata.name == $other) | .metadata.annotations | has("service.cilium.io/affinity")] == [false];
      ($w1[0] | fits("solo-1"; "solo-2")) and ($w2[0] | fits("solo-2"; "solo-1"))'
  check "workload: no node port, every image pinned by digest" 0 "" eval '! grep -q -i nodeport "$t/w1.json" && jq -e "[.items[] | select(.kind == \"Deployment\") | .spec.template.spec.containers[].image] | length > 0 and all(contains(\"@sha256:\"))" "$t/w1.json"'
  check "workload: cluster 3 fails" 1 "neither 1 nor 2" workload_manifest 3

  local okd='{"responses":["echo-1"]}' okd2='{"responses":["echo-2"]}' errd='{"errors":["dial tcp: i/o timeout"]}'
  check "dial: every dial answered by a wanted pod" 0 "2 of 2 dials answered by echo-1,echo-2" eval 'printf "%s\n" "$okd" "$okd2" | dial_judge echo-1,echo-2'
  check "dial: one dial answered by another pod fails" 1 "1 by another pod (echo-2)" eval 'printf "%s\n" "$okd" "$okd2" | dial_judge echo-1'
  check "dial: one failed dial fails" 1 "1 failed" eval 'printf "%s\n" "$okd" "$errd" | dial_judge echo-1'
  check "dial: an answer that is not JSON fails" 1 "1 failed" eval 'printf "%s\n" "not json" | dial_judge echo-1'
  check "dial: no dial fails" 1 "0 of 0" eval 'printf "" | dial_judge echo-1'
  check "dial: no wanted pod fails" 1 "nobody" eval 'printf "%s\n" "$okd" | dial_judge ""'

  local pods='{"items":[
    {"metadata":{"namespace":"kube-system","name":"cilium-x","uid":"u1","labels":{"k8s-app":"cilium"}},"status":{"containerStatuses":[{"name":"sidecar","restartCount":2},{"name":"cilium-agent","restartCount":1,"lastState":{"terminated":{"exitCode":137}}}]}},
    {"metadata":{"namespace":"kube-system","name":"cilium-operator-y","uid":"u2","labels":{"io.cilium/app":"operator"}},"status":{"containerStatuses":[{"name":"cilium-operator","restartCount":3}]}},
    {"metadata":{"namespace":"kube-system","name":"clustermesh-apiserver-z","uid":"u3","labels":{"k8s-app":"clustermesh-apiserver"}},"status":{}},
    {"metadata":{"namespace":"kube-system","name":"coredns-q","uid":"u4","labels":{"k8s-app":"kube-dns"}},"status":{"containerStatuses":[{"name":"coredns","restartCount":1}]}},
    {"metadata":{"namespace":"mesh-hermetic","name":"echo-w","uid":"u5"},"status":{"containerStatuses":[{"name":"netexec","restartCount":0}]}}]}'
  check "pods: the mesh pods and the test pods with the restarts of each container" 0 "kube-system/cilium-x u1 cilium-agent=1,sidecar=2" eval 'pod_lines mesh-hermetic <<< "$pods"'
  check "pods: three of five pods are read, not coredns and not the operator" 0 "" eval '[ "$(pod_lines mesh-hermetic <<< "$pods" | wc -l)" = 3 ] && ! pod_lines mesh-hermetic <<< "$pods" | grep -q -E "coredns|operator"'
  check "pods: no pod fails" 1 "" eval 'pod_lines mesh-hermetic <<< "{\"items\":[]}"'
  check "restart read: each restarted container of those pods with the exit code of its last termination" 0 "" \
    eval 'test "$(restart_lines mesh-hermetic <<< "$pods")" = "$(printf "kube-system/cilium-x cilium-agent 1 137\nkube-system/cilium-x sidecar 2 -")"'

  # The pods of both regions before the pause, and after the resume as the platform leaves them and
  # as it must not; the restarts files hold what the run reads of each restarted container.
  printf '%s\n' 'a kube-system/cilium-x u1 cilium-agent=0' 'a kube-system/clustermesh-apiserver-q u7 apiserver=0,etcd=0,kvstoremesh=0' \
    'a mesh-hermetic/echo-w u5 netexec=0' 'b kube-system/cilium-y u6 cilium-agent=0' \
    'b kube-system/clustermesh-apiserver-z u3 apiserver=0,etcd=0,kvstoremesh=0' > "$t/before.txt"
  sed 's/u3 apiserver=0,etcd=0,kvstoremesh=0/u3 apiserver=0,etcd=0,kvstoremesh=1/' "$t/before.txt" > "$t/kvs1.txt"
  sed 's/u3 apiserver=0,etcd=0,kvstoremesh=0/u3 apiserver=0,etcd=0,kvstoremesh=2/' "$t/before.txt" > "$t/kvs2.txt"
  sed 's/u3 apiserver=0,etcd=0,kvstoremesh=0/u3 apiserver=0,etcd=1,kvstoremesh=1/' "$t/before.txt" > "$t/etcd.txt"
  sed 's/u7 apiserver=0,etcd=0,kvstoremesh=0/u7 apiserver=0,etcd=0,kvstoremesh=1/' "$t/before.txt" > "$t/akvs.txt"
  sed 's/u6 cilium-agent=0/u9 cilium-agent=0/' "$t/kvs1.txt" > "$t/replaced.txt"
  : > "$t/none.txt"
  local fatal='time=2026-11-19T02:45:29.093750954Z level=fatal msg="Leader election lost"'
  printf 'b kube-system/clustermesh-apiserver-z kvstoremesh restarts=1 exit=255 last=%s\n' "$fatal" > "$t/ev.txt"
  printf 'b kube-system/clustermesh-apiserver-z kvstoremesh restarts=1 exit=1 last=%s\n' "$fatal" > "$t/ev-exit.txt"
  printf 'b kube-system/clustermesh-apiserver-z kvstoremesh restarts=1 exit=255 last=%s\n' \
    'time=2026-11-19T02:45:29.09352355Z level=info msg="Stopped hive" duration=1.327118627s' > "$t/ev-line.txt"
  printf 'a kube-system/clustermesh-apiserver-q kvstoremesh restarts=1 exit=255 last=%s\n' "$fatal" > "$t/ev-a.txt"
  # The end of a previous log of kvstoremesh as run 44480907033 read it: a JSON line of the etcd
  # client follows the fatal line of Cilium's logger.
  local hive='time=2026-11-19T02:45:29.09352355Z level=info msg="Stopped hive" duration=1.327118627s'
  local etcdw='{"level":"warn","ts":"2026-11-19T02:45:29.090294Z","logger":"etcd-client","msg":"retrying of unary invoker failed","error":"rpc error: code = Canceled desc = grpc: the client connection is closing"}'
  check "last log line: the fatal line of Cilium's logger, past the JSON lines of the etcd client and a blank line" 0 "" \
    eval 'test "$(printf "%s\n" "$etcdw" "$hive" "$fatal" "$etcdw" "  $etcdw" "" | last_log_line)" = "$fatal"'
  check "last log line: a line of Cilium's logger after the fatal line is the last line" 0 "" \
    eval 'test "$(printf "%s\n" "$fatal" "$hive" "$etcdw" | last_log_line)" = "$hive"'
  printf 'b kube-system/clustermesh-apiserver-z kvstoremesh restarts=1 exit=255 last=%s\n' \
    "$(printf '%s\n' "$hive" "$fatal" "$etcdw" | last_log_line)" > "$t/ev-json.txt"
  check "restarts: frozen 302 s, kvstoremesh of the frozen region once, exit 255, its lease lost: passes" 0 \
    "b kube-system/clustermesh-apiserver-z kvstoremesh restarted once, exit 255" restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/ev.txt" b 302
  check "restarts: frozen 302 s, the fatal line followed by a JSON line of the etcd client: passes" 0 \
    "b kube-system/clustermesh-apiserver-z kvstoremesh restarted once, exit 255" restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/ev-json.txt" b 302
  check "restarts: frozen 302 s and kvstoremesh at 0 fails" 1 "b kube-system/clustermesh-apiserver-z kvstoremesh=0 restarts, expected 1" \
    restart_judge "$t/before.txt" "$t/before.txt" "$t/none.txt" b 302
  check "restarts: frozen 302 s and kvstoremesh at 2 fails" 1 "kvstoremesh=2 restarts, expected 1" restart_judge "$t/before.txt" "$t/kvs2.txt" "$t/ev.txt" b 302
  check "restarts: frozen 302 s and another container of the pod restarted fails" 1 "b kube-system/clustermesh-apiserver-z etcd=1 restarts, expected 0" \
    restart_judge "$t/before.txt" "$t/etcd.txt" "$t/ev.txt" b 302
  check "restarts: frozen 302 s and kvstoremesh of the region not frozen restarted fails" 1 "a kube-system/clustermesh-apiserver-q kvstoremesh=1 restarts, expected 0" \
    restart_judge "$t/before.txt" "$t/akvs.txt" "$t/ev-a.txt" b 302
  check "restarts: frozen 302 s and another last log line fails" 1 "the previous log ends" restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/ev-line.txt" b 302
  check "restarts: frozen 302 s and another exit code fails" 1 "exit code 1, expected 255" restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/ev-exit.txt" b 302
  check "restarts: frozen 302 s and no termination read fails" 1 "no last termination and no previous log read" \
    restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/none.txt" b 302
  check "restarts: frozen 25 s, not longer than the lease, and no restart passes" 0 "5 pods compared, every container 0 restarts" \
    restart_judge "$t/before.txt" "$t/before.txt" "$t/none.txt" b 25
  check "restarts: frozen 25 s and kvstoremesh restarted fails" 1 "kvstoremesh=1 restarts, expected 0" restart_judge "$t/before.txt" "$t/kvs1.txt" "$t/ev.txt" b 25
  check "restarts: a replaced pod fails" 1 "replaced: b kube-system/cilium-y (uid u6, then u9)" restart_judge "$t/before.txt" "$t/replaced.txt" "$t/ev.txt" b 302
  check "restarts: a restart before the pause fails" 1 "before the pause: b kube-system/clustermesh-apiserver-z kvstoremesh=1, expected 0" \
    restart_judge "$t/kvs1.txt" "$t/kvs1.txt" "$t/ev.txt" b 302
  check "restarts: no pod read fails" 1 "no pod read" restart_judge "$t/none.txt" "$t/none.txt" "$t/none.txt" b 302

  check "mesh: every agent reports the peer ready" 0 "" eval 'printf "cilium-1 sov-b=true;\ncilium-2 sov-b=true;\n" | mesh_judge sov-b'
  check "mesh: one agent not ready fails" 1 "cilium-2: not ready sov-b (reports sov-b=false)" eval 'printf "cilium-1 sov-b=true;\ncilium-2 sov-b=false;\n" | mesh_judge sov-b'
  check "mesh: an agent that reports no peer fails" 1 "reports no peer" eval 'printf "cilium-1 \n" | mesh_judge sov-b'
  check "mesh: no agent fails" 1 "" eval 'printf "" | mesh_judge sov-b'

  local hr='{"metadata":{"generation":7},"status":{"conditions":[{"type":"Ready","status":"True","observedGeneration":7,"reason":"UpgradeSucceeded"}]}}'
  check "release: Ready at the current generation" 0 "Ready=True for generation 7" eval 'hr_judge <<< "$hr"'
  check "release: Ready at an older generation fails" 1 "" eval 'hr_judge <<< "$(sed "s/\"observedGeneration\":7/\"observedGeneration\":6/" <<< "$hr")"'
  check "release: not Ready fails" 1 "Ready=False" eval 'hr_judge <<< "$(sed "s/\"True\"/\"False\"/" <<< "$hr")"'

  # flux_list <owner of bootstrap-kit> <owner of sovereign-root>: the List that `kubectl get
  # helmreleases,kustomizations -A -o json` prints, with the objects and labels of the local pack of
  # run 44701413010 (first boot since SOV-154: sovereign-root applies bootstrap-kit); "" no owner.
  flux_list() {
    jq -n -c --arg kit "$1" --arg root "$2" '
      def obj($api; $kind; $name; $owner): {apiVersion: $api, kind: $kind, metadata: ({name: $name, namespace: "flux-system"}
        + if $owner == "" then {} else {labels: {"kustomize.toolkit.fluxcd.io/name": $owner, "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}} end)};
      {apiVersion: "v1", kind: "List", metadata: {resourceVersion: ""}, items: [
        obj("helm.toolkit.fluxcd.io/v2"; "HelmRelease"; "bp-cert-manager"; "bootstrap-kit"),
        obj("helm.toolkit.fluxcd.io/v2"; "HelmRelease"; "bp-cilium"; "bootstrap-kit"),
        obj("kustomize.toolkit.fluxcd.io/v1"; "Kustomization"; "bootstrap-kit"; $kit),
        obj("kustomize.toolkit.fluxcd.io/v1"; "Kustomization"; "bootstrap-kit-crs"; "sovereign-root"),
        obj("kustomize.toolkit.fluxcd.io/v1"; "Kustomization"; "sovereign-root"; $root)]}'
  }
  check "owners: the root Kustomization, then the kit it applies" 0 "" eval '[ "$(flux_list sovereign-root "" | flux_owners | tr "\n" ,)" = "flux-system sovereign-root,flux-system bootstrap-kit," ]'
  check "owners: the kit alone when nothing applies it" 0 "" eval '[ "$(flux_list "" "" | flux_owners | tr "\n" ,)" = "flux-system bootstrap-kit," ]'
  check "owners: a Kustomization named but absent fails" 1 "flux-system/sovereign-gone is not in the cluster" eval 'flux_list sovereign-gone "" | flux_owners'
  check "owners: a loop fails" 1 "form a loop" eval 'flux_list sovereign-root bootstrap-kit | flux_owners'
  check "owners: no bp-cilium fails" 1 "no HelmRelease flux-system/bp-cilium" eval 'flux_list "" "" | jq -c "del(.items[1])" | flux_owners'

  local rootb64 otherb64
  rootb64=$(printf 'root-der' | base64 -w0)
  otherb64=$(printf 'other-der' | base64 -w0)
  printf -- '-----BEGIN CERTIFICATE-----\n%s\n-----END CERTIFICATE-----\n' "$rootb64" > "$t/root.crt"
  printf -- '-----BEGIN CERTIFICATE-----\n%s\n-----END CERTIFICATE-----\n' "$otherb64" > "$t/other.crt"
  # cert_files <issuer> <Ready status> <ca.crt file> <name without a Secret>: writes certs.json and
  # secrets.json, as cert_judge reads them, for the names in CERTS.
  cert_files() {
    jq -n -c --arg names "$CERTS" --arg issuer "$1" --arg ready "$2" '{items: [$names | split(" ")[] | select(. != "")
      | {metadata: {name: .}, spec: {issuerRef: {name: $issuer}}, status: {conditions: [{type: "Ready", status: $ready}]}}]}' > "$t/certs.json"
    jq -n -c --arg names "$CERTS" --arg ca "$(base64 -w0 < "$3")" --arg drop "$4" '{items: [$names | split(" ")[] | select(. != "" and . != $drop)
      | {metadata: {name: .}, data: {"ca.crt": $ca}}]}' > "$t/secrets.json"
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
