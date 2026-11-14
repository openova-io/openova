#!/usr/bin/env bash
# The JetStream stream CATALYST_ORG is declared by this chart and bound by the code.
#
# Renders the chart with its default values and checks:
#   1. exactly one Stream object renders, catalyst-org, with exactly the spec of
#      contract C3: name CATALYST_ORG, subjects ["catalyst.>"], storage file,
#      retention limits, maxAge "336h", replicas 1, discard old;
#   2. no KeyValue bucket is named idempotency;
#   3. the stream name equals the key `stream` of docs/contracts/C3-event-subjects.yaml;
#   4. the Go code binds to the same stream: the constant StreamCatalystOrg in
#      core/services/shared/events/bridge_subscriber.go and in
#      core/controllers/pkg/natsbus/subscriber.go is the stream name, and each
#      of the two files core/services/shared/events/bridge_subscriber.go and
#      nats.go holds a default subject list, every one equal to the stream's.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
ROOT="$(cd "$CHART_DIR/../../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi
helm template nats-jetstream . --namespace nats-system > "$TMP/default.yaml"

python3 - "$TMP/default.yaml" "$ROOT" <<'PY'
import re, sys
import yaml

render, root = sys.argv[1], sys.argv[2]
want = {"name": "CATALYST_ORG", "subjects": ["catalyst.>"], "storage": "file",
        "retention": "limits", "maxAge": "336h", "replicas": 1, "discard": "old"}
fails = []
docs = [d for d in yaml.safe_load_all(open(render, encoding="utf-8")) if isinstance(d, dict)]
streams = [d for d in docs if d.get("kind") == "Stream"]
if len(streams) != 1:
    fails.append("the render holds %d Stream objects, not 1: %s" % (len(streams), [s.get("metadata", {}).get("name") for s in streams]))
else:
    got = streams[0]
    if got.get("apiVersion") != "jetstream.nats.io/v1beta2" or got.get("metadata", {}).get("name") != "catalyst-org":
        fails.append("the Stream is %s %s, not jetstream.nats.io/v1beta2 catalyst-org" % (got.get("apiVersion"), got.get("metadata", {}).get("name")))
    if got.get("spec") != want:
        fails.append("the Stream spec is %r, not %r" % (got.get("spec"), want))
buckets = sorted(d.get("spec", {}).get("bucket") for d in docs if d.get("kind") == "KeyValue")
if "idempotency" in buckets:
    fails.append("the KeyValue bucket idempotency still renders")

def read(path):
    with open("%s/%s" % (root, path), encoding="utf-8") as fh:
        return fh.read()

registry = yaml.safe_load(read("docs/contracts/C3-event-subjects.yaml"))
if registry.get("stream") != want["name"]:
    fails.append("docs/contracts/C3-event-subjects.yaml names the stream %r, not %r" % (registry.get("stream"), want["name"]))
for path in ("core/services/shared/events/bridge_subscriber.go", "core/controllers/pkg/natsbus/subscriber.go"):
    names = re.findall(r'^const StreamCatalystOrg = "([^"]*)"$', read(path), re.M)
    if names != [want["name"]]:
        fails.append("%s: StreamCatalystOrg is %r, not [%r]" % (path, names, want["name"]))
for path in ("core/services/shared/events/bridge_subscriber.go", "core/services/shared/events/nats.go"):
    lists = re.findall(r'\[\]string\{("catalyst[^}]*)\}', read(path))
    if not lists or any([x.strip().strip('"') for x in l.split(",")] != want["subjects"] for l in lists):
        fails.append("%s: the default subject lists are %r; each must be %r" % (path, lists, want["subjects"]))
for f in fails:
    print("FAIL: " + f)
if fails:
    sys.exit(1)
print("catalyst-org-stream: one Stream CATALYST_ORG, equal to contract C3 and to the code; no idempotency bucket (KeyValue buckets: %s)" % ", ".join(buckets))
PY
