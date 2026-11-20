#!/usr/bin/env bash
# Render and run test of templates/proxy-cache-integrity.yaml (SOV-162). CI runs
# every chart/tests/*.sh as `bash <script> <chart dir>`.
# The cases 1 to 5 run one pass of the rendered check.sh with `sh` against
# stand-ins for curl (harbor-core: the Harbor API, the token service and /v2)
# and kubectl (the Event and the counters ConfigMap), written below; case 6
# reads the default render. Each case that passes prints "ok <n> <case>"; a failure
# prints "FAIL: <reason>" on stderr and ends the test with exit 1.
# Needs bash, helm and jq (the script itself parses JSON with jq; GitHub's
# ubuntu runner carries it); a missing tool fails the test. The render is read
# with awk and grep, so no YAML tool is needed.
#   1  a missing child manifest: the index is deleted, one Event, healed 1
#   2  a missing config blob: the index and the still-listed child are deleted
#   3  a whole index is left alone: no deletion, no Event
#   4  a hosted project is never listed, read or changed
#   5  a failed read deletes nothing (an error answer, no answer, a failed listing,
#      an index listed without children); unread earlier counters stay as they are
#   6  on by default: the default render holds the check; enabled=false renders nothing
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
for tool in helm jq; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is needed"
done

cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build >/dev/null
fi

TPL=templates/proxy-cache-integrity.yaml
helm template harbor . --namespace harbor --show-only "$TPL" > "$TMP/default.yaml"
# doc <kind> <file>: the YAML document of that kind (its top-level kind line).
doc() {
  awk -v k="kind: $1" '/^---$/ { if (hit) exit; buf = ""; next } { buf = buf $0 "\n" } $0 == k { hit = 1 } END { if (hit) printf "%s", buf }' "$2"
}
for k in ServiceAccount Role RoleBinding ConfigMap CronJob Deployment; do
  doc "$k" "$TMP/default.yaml" > "$TMP/doc-$k.yaml"
done
CRON="$TMP/doc-CronJob.yaml"; DEP="$TMP/doc-Deployment.yaml"
# The literal block check.sh of the ConfigMap, its four spaces of indent removed.
awk '/^  check.sh: \|$/ { f = 1; next } f && /^    / { print substr($0, 5); next } f && /^$/ { print ""; next } f { exit }' \
  "$TMP/doc-ConfigMap.yaml" > "$TMP/check.sh"
[ "$(head -1 "$TMP/check.sh")" = '#!/bin/sh' ] || fail "the ConfigMap holds no check.sh"
[ "$(tail -1 "$TMP/check.sh")" = '[ "${failed}" = 0 ]' ] || fail "check.sh was not read to its end"
# envof <name>: the literal value of that env entry of the CronJob.
envof() {
  awk -v n="- name: $1" '{ l = $0; sub(/^ +/, "", l) } l == n { getline; sub(/^ +value: /, ""); gsub(/"/, ""); print; exit }' "$CRON"
}
CORE="$(envof HARBOR_CORE_URL)"
OBJ="$(envof EVENT_OBJECT)"
CM="$(envof METRICS_CONFIGMAP)"
[ "$CORE" = "http://harbor-core.harbor.svc.cluster.local" ] || fail "HARBOR_CORE_URL is $CORE"
[ "$OBJ" = "harbor-proxy-cache-integrity" ] || fail "EVENT_OBJECT is $OBJ"
[ "$CM" = "harbor-proxy-cache-integrity-metrics" ] || fail "METRICS_CONFIGMAP is $CM"
API="$CORE/api/v2.0"

BIN="$TMP/bin"
mkdir -p "$BIN"
# Stand-in for curl. A request is answered from the table $ST/routes, one line
# "METHOD URL CODE BODY" (BODY a file of $ST, or -), and logged to $ST/calls.
# A request the table does not hold is logged to $ST/unrouted and answered 500.
# The API wants -u admin:adminpw, /v2 wants the bearer token tkn; CODE 000 is
# no answer (curl prints 000 and exits 28).
cat > "$BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -u
out=""; fmt=""; auth=""; method=GET; url=""; bearer=no
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) fmt="$2"; shift 2 ;;
    -u) auth="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    -H) [ "$2" = "Authorization: Bearer tkn" ] && bearer=yes; shift 2 ;;
    --max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$method $url" >> "$ST/calls"
route="$(awk -v m="$method" -v u="$url" '$1 == m && $2 == u { print $3 " " $4; exit }' "$ST/routes")"
if [ -z "$route" ]; then
  echo "$method $url" >> "$ST/unrouted"
  route="500 -"
fi
code="${route%% *}"; body="${route#* }"
case "$url" in
  */v2/*) [ "$bearer" = yes ] || { code=401; body=-; } ;;
  *) [ "$auth" = "admin:adminpw" ] || { code=401; body=-; } ;;
esac
if [ "$code" = 000 ]; then printf '000'; exit 28; fi
if [ "$body" = - ]; then src=/dev/null; else src="$ST/$body"; fi
if [ -n "$out" ]; then cat "$src" > "$out"; else cat "$src"; fi
[ -z "$fmt" ] || printf '%s' "$code"
EOF
# Stand-in for kubectl, logged to $ST/kubectl: `create -f F` keeps F in
# $ST/events; `apply ... -f F` keeps F as $ST/cm.json; `get configmap N -o json`
# prints $ST/cm.json, or fails as NotFound when there is none, or fails with
# another error when $ST/get-fails exists.
cat > "$BIN/kubectl" <<'EOF'
#!/usr/bin/env bash
set -u
echo "$*" >> "$ST/kubectl"
f=""; prev=""; verb=""
for a in "$@"; do
  [ "$prev" = "-f" ] && f="$a"
  case "$a" in create|apply|get) [ -n "$verb" ] || verb="$a" ;; esac
  prev="$a"
done
case "$verb" in
  create)
    [ -n "$f" ] || exit 1
    n=$(( $(cat "$ST/events.count" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$ST/events.count"
    cp "$f" "$ST/events/$n.json" ;;
  apply) [ -n "$f" ] || exit 1; cp "$f" "$ST/cm.json" ;;
  get)
    [ ! -f "$ST/get-fails" ] || { echo 'Unable to connect to the server: net/http: TLS handshake timeout' >&2; exit 1; }
    [ -f "$ST/cm.json" ] || { echo 'Error from server (NotFound): configmaps not found' >&2; exit 1; }
    cat "$ST/cm.json" ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$BIN/curl" "$BIN/kubectl"

H64() { printf 'sha256:%064d' "$1"; }
IDX="$(H64 1)"; AMD="$(H64 2)"; ARM="$(H64 3)"; CFGA="$(H64 4)"; CFGB="$(H64 5)"
OLD="$(H64 6)"; GONE="$(H64 7)"; ONE="$(H64 8)"; HIDX="$(H64 9)"; HCH="$(H64 10)"
REPO=proxy-dockerhub/library/busybox
ARTS="$API/projects/proxy-dockerhub/repositories/library%252Fbusybox/artifacts"
V2="$CORE/v2/$REPO"

# setup <case>: a fresh stub state with the common registry:
#   openova-io (hosted, no registry_id), yotta (registry_id 0), zeta (registry_id
#   null) and proxy-dockerhub (registry_id 1) holding library/busybox with the
#   tagged index IDX (children AMD linux/amd64 and ARM linux/arm64/v8), an
#   untagged index OLD and a tagged single manifest ONE. Every read of IDX's
#   children answers 200; a case changes what it needs with route.
setup() {
  ST="$TMP/st-$1"; RUN="$TMP/run-$1"
  rm -rf "$ST" "$RUN"
  mkdir -p "$ST/events" "$RUN/admin"
  : > "$ST/routes"; : > "$ST/calls"; : > "$ST/unrouted"
  printf 'adminpw' > "$RUN/admin/HARBOR_ADMIN_PASSWORD"
  cat > "$ST/projects.json" <<JSON
[{"name":"openova-io","project_id":2},{"name":"proxy-dockerhub","project_id":3,"registry_id":1},
 {"name":"yotta","project_id":4,"registry_id":0},{"name":"zeta","project_id":5,"registry_id":null}]
JSON
  echo '[{"name":"proxy-dockerhub/library/busybox"}]' > "$ST/repos.json"
  cat > "$ST/arts.json" <<JSON
[{"digest":"$IDX","manifest_media_type":"application/vnd.oci.image.index.v1+json","tags":[{"name":"1.36"}],
  "references":[{"child_digest":"$AMD","platform":{"os":"linux","architecture":"amd64"}},
                {"child_digest":"$ARM","platform":{"os":"linux","architecture":"arm64","variant":"v8"}}]},
 {"digest":"$OLD","manifest_media_type":"application/vnd.docker.distribution.manifest.list.v2+json","tags":null,
  "references":[{"child_digest":"$GONE"}]},
 {"digest":"$ONE","manifest_media_type":"application/vnd.docker.distribution.manifest.v2+json","tags":[{"name":"single"}]}]
JSON
  echo '{"token":"tkn","expires_in":1800}' > "$ST/token.json"
  printf '{"schemaVersion":2,"config":{"digest":"%s"},"layers":[]}\n' "$CFGA" > "$ST/amd.json"
  printf '{"schemaVersion":2,"config":{"digest":"%s"},"layers":[]}\n' "$CFGB" > "$ST/arm.json"
  echo '{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}' > "$ST/manifest-unknown.json"
  echo '{"errors":[{"code":"BLOB_UNKNOWN","message":"blob unknown"}]}' > "$ST/blob-unknown.json"
  echo '{"architecture":"amd64"}' > "$ST/config.json"
  route GET "$API/projects?page=1&page_size=100" 200 projects.json
  route GET "$API/projects/proxy-dockerhub/repositories?page=1&page_size=100" 200 repos.json
  route GET "$ARTS?with_tag=true&page=1&page_size=100" 200 arts.json
  route GET "$CORE/service/token?service=harbor-registry&scope=repository:$REPO:pull" 200 token.json
  route GET "$V2/manifests/$AMD" 200 amd.json
  route GET "$V2/manifests/$ARM" 200 arm.json
  route GET "$V2/blobs/$CFGA" 200 config.json
  route GET "$V2/blobs/$CFGB" 200 config.json
}
# route METHOD URL CODE BODY: a later line for the same request wins.
route() {
  if [ -s "$ST/routes" ]; then
    awk -v m="$1" -v u="$2" '!($1 == m && $2 == u)' "$ST/routes" > "$ST/routes.new"
    mv "$ST/routes.new" "$ST/routes"
  fi
  echo "$1 $2 $3 $4" >> "$ST/routes"
}
# pass: one run of check.sh; prints its exit status, its log is $RUN/log.
pass() {
  local rc=0
  env -i PATH="$BIN:$PATH" ST="$ST" HARBOR_CORE_URL="$CORE" ADMIN_DIR="$RUN/admin" \
    WORK_DIR="$RUN/work" POD_NAMESPACE=harbor POD_NAME=harbor-proxy-cache-integrity-1-x \
    EVENT_OBJECT="$OBJ" METRICS_CONFIGMAP="$CM" \
    sh "$TMP/check.sh" > "$RUN/log" 2>&1 || rc=$?
  echo "$rc"
}
# metric <name>: its value in the counters ConfigMap the pass wrote.
metric() {
  jq -r '.data["metrics.txt"]' "$ST/cm.json" > "$ST/metrics.txt"
  sed -n "s/^harbor_proxy_cache_integrity_$1 //p" "$ST/metrics.txt"
}
called() { grep -c -x -F "$1" "$ST/calls" || true; }
deletes() { grep -c '^DELETE ' "$ST/calls" || true; }
events() { cat "$ST/events.count" 2>/dev/null || echo 0; }
nounrouted() { [ ! -s "$ST/unrouted" ] || fail "$1: requests no route holds: $(cat "$ST/unrouted")"; }
show() { echo "--- check.sh log ($1)"; cat "$RUN/log"; }

echo "[proxy-cache-integrity] Case 1: a missing child manifest"
setup 1
route GET "$V2/manifests/$AMD" 404 manifest-unknown.json
route DELETE "$ARTS/$IDX" 200 -
route GET "$ARTS/$AMD" 404 -
rc="$(pass)"
nounrouted "case 1"
[ "$rc" = 0 ] || { show 1; fail "case 1: exit status $rc"; }
[ "$(called "DELETE $ARTS/$IDX")" = 1 ] && [ "$(deletes)" = 1 ] || { show 1; fail "case 1: the index was not deleted exactly once"; }
[ "$(events)" = 1 ] || fail "case 1: $(events) Events, want 1"
ev="$ST/events/1.json"
[ "$(jq -r '[.kind, .reason, .type, .metadata.namespace, .involvedObject.kind, .involvedObject.name] | join(" ")' "$ev")" = \
  "Event ProxyCacheArtifactHealed Warning harbor CronJob $OBJ" ] || fail "case 1: the Event is $(cat "$ev")"
jq -e --arg r "$REPO@$IDX" --arg c "manifest $AMD (linux/amd64)" '(.message | contains($r)) and (.message | contains($c))' "$ev" >/dev/null \
  || fail "case 1: the Event does not name the index and the missing child: $(jq -r .message "$ev")"
grep -q -x -e "create -f .*/event.json --field-manager=catalyst-harbor-proxy-cache-integrity" "$ST/kubectl" \
  || fail "case 1: the Event was not created with the field manager"
grep -q -x -e "apply --server-side --field-manager=catalyst-harbor-proxy-cache-integrity -f .*/metrics.json" "$ST/kubectl" \
  || fail "case 1: the counters were not applied with the field manager"
[ "$(jq -r '[.kind, .metadata.name, .metadata.namespace] | join(" ")' "$ST/cm.json")" = "ConfigMap $CM harbor" ] \
  || fail "case 1: the counters ConfigMap is $(cat "$ST/cm.json")"
[ "$(metric healed_total) $(metric missing_total) $(metric read_failures_total) $(metric last_pass_ok)" = "1 1 0 1" ] \
  || fail "case 1: metrics $(cat "$ST/metrics.txt")"
[ "$(called "GET $V2/manifests/$GONE")" = 0 ] || fail "case 1: the children of the untagged index were read"
[ "$(called "GET $V2/manifests/$ONE")" = 0 ] || fail "case 1: a manifest that is not an index was read"

echo "ok 1 a missing child manifest"
echo "[proxy-cache-integrity] Case 2: a missing config blob"
setup 2
route GET "$V2/blobs/$CFGA" 404 blob-unknown.json
route DELETE "$ARTS/$IDX" 200 -
route GET "$ARTS/$AMD" 200 amd.json
route DELETE "$ARTS/$AMD" 200 -
# Counters of earlier passes: this pass adds to them.
jq -n --arg m "$(printf 'harbor_proxy_cache_integrity_healed_total 4\nharbor_proxy_cache_integrity_missing_total 4\n')" \
  '{kind: "ConfigMap", data: {"metrics.txt": $m}}' > "$ST/cm.json"
rc="$(pass)"
nounrouted "case 2"
[ "$rc" = 0 ] || { show 2; fail "case 2: exit status $rc"; }
[ "$(called "DELETE $ARTS/$IDX")" = 1 ] || { show 2; fail "case 2: the index was not deleted"; }
[ "$(called "DELETE $ARTS/$AMD")" = 1 ] && [ "$(deletes)" = 2 ] || { show 2; fail "case 2: the still-listed child was not deleted, or more was"; }
[ "$(events)" = 1 ] || fail "case 2: $(events) Events, want 1"
jq -e --arg c "config $CFGA of $AMD" '.message | contains($c)' "$ST/events/1.json" >/dev/null \
  || fail "case 2: the Event does not name the missing config: $(jq -r .message "$ST/events/1.json")"
[ "$(metric healed_total) $(metric missing_total)" = "5 5" ] || fail "case 2: the counters did not add up: $(cat "$ST/metrics.txt")"

echo "ok 2 a missing config blob"
echo "[proxy-cache-integrity] Case 3: a whole index is left alone"
setup 3
rc="$(pass)"
nounrouted "case 3"
[ "$rc" = 0 ] || { show 3; fail "case 3: exit status $rc"; }
[ "$(deletes)" = 0 ] && [ "$(events)" = 0 ] || fail "case 3: a whole index was deleted or reported"
for u in "$V2/manifests/$AMD" "$V2/manifests/$ARM" "$V2/blobs/$CFGA" "$V2/blobs/$CFGB"; do
  [ "$(called "GET $u")" = 1 ] || fail "case 3: $u was not read once"
done
[ "$(metric healed_total) $(metric indexes_checked) $(metric children_checked) $(metric last_pass_ok)" = "0 1 2 1" ] \
  || fail "case 3: metrics $(cat "$ST/metrics.txt")"

echo "ok 3 a whole index is left alone"
echo "[proxy-cache-integrity] Case 4: a hosted project is never touched"
setup 4
for p in openova-io yotta zeta; do
  # Routes that answer as a broken cache would: a check that read hosted
  # projects would find the miss and delete.
  route GET "$API/projects/$p/repositories?page=1&page_size=100" 200 "repos-$p.json"
  echo "[{\"name\":\"$p/app\"}]" > "$ST/repos-$p.json"
  route GET "$API/projects/$p/repositories/app/artifacts?with_tag=true&page=1&page_size=100" 200 hosted-arts.json
  route GET "$CORE/service/token?service=harbor-registry&scope=repository:$p/app:pull" 200 token.json
  route GET "$CORE/v2/$p/app/manifests/$HCH" 404 manifest-unknown.json
  route DELETE "$API/projects/$p/repositories/app/artifacts/$HIDX" 200 -
done
printf '[{"digest":"%s","manifest_media_type":"application/vnd.oci.image.index.v1+json","tags":[{"name":"v1"}],"references":[{"child_digest":"%s"}]}]\n' "$HIDX" "$HCH" > "$ST/hosted-arts.json"
rc="$(pass)"
nounrouted "case 4"
[ "$rc" = 0 ] || { show 4; fail "case 4: exit status $rc"; }
for p in openova-io yotta zeta; do
  ! grep -q -F "/$p/" "$ST/calls" || fail "case 4: the hosted project $p was read: $(grep -F "/$p/" "$ST/calls")"
done
[ "$(deletes)" = 0 ] && [ "$(events)" = 0 ] || fail "case 4: something was deleted or reported"
[ "$(metric indexes_checked)" = 1 ] || fail "case 4: the pull-through project was not read"

echo "ok 4 a hosted project is never touched"
echo "[proxy-cache-integrity] Case 5: a failed read deletes nothing"
for v in error noanswer missplus listing token norefs; do
  setup "5-$v"
  route DELETE "$ARTS/$IDX" 200 -
  case "$v" in
    norefs) jq --arg d "$IDX" 'map(if .digest == $d then del(.references) else . end)' "$ST/arts.json" > "$ST/arts.new"
      mv "$ST/arts.new" "$ST/arts.json" ;;
    error) route GET "$V2/manifests/$AMD" 500 - ;;
    noanswer) route GET "$V2/blobs/$CFGA" 000 - ;;
    missplus) route GET "$V2/manifests/$ARM" 404 manifest-unknown.json; route GET "$V2/manifests/$AMD" 503 - ;;
    listing) route GET "$ARTS?with_tag=true&page=1&page_size=100" 502 - ;;
    token) route GET "$CORE/service/token?service=harbor-registry&scope=repository:$REPO:pull" 401 - ;;
  esac
  rc="$(pass)"
  nounrouted "case 5 $v"
  [ "$rc" = 1 ] || { show "5 $v"; fail "case 5 ($v): exit status $rc, want 1"; }
  [ "$(deletes)" = 0 ] && [ "$(events)" = 0 ] || { show "5 $v"; fail "case 5 ($v): a failed read deleted"; }
  [ "$(metric healed_total) $(metric read_failures_total) $(metric last_pass_ok)" = "0 1 0" ] \
    || fail "case 5 ($v): metrics $(cat "$ST/metrics.txt")"
done
setup 5-projects
route GET "$API/projects?page=1&page_size=100" 500 -
rc="$(pass)"
[ "$rc" = 1 ] && [ "$(deletes)" = 0 ] && [ "$(wc -l < "$ST/calls")" = 1 ] || fail "case 5 (projects): a failed project listing went on"
[ "$(metric read_failures_total)" = 1 ] || fail "case 5 (projects): metrics $(cat "$ST/metrics.txt")"
# Earlier counters that cannot be read (not NotFound) are never overwritten.
setup 5-counters
jq -n --arg m "$(printf 'harbor_proxy_cache_integrity_healed_total 4\nharbor_proxy_cache_integrity_read_failures_total 2\n')" \
  '{kind: "ConfigMap", data: {"metrics.txt": $m}}' > "$ST/cm.json"
: > "$ST/get-fails"
rc="$(pass)"
nounrouted "case 5 counters"
[ "$rc" = 1 ] && [ "$(deletes)" = 0 ] || { show "5 counters"; fail "case 5 (counters): exit status $rc, want 1, and no deletion"; }
! grep -q '^apply ' "$ST/kubectl" || fail "case 5 (counters): the counters were written although the earlier ones could not be read"
[ "$(metric healed_total) $(metric read_failures_total)" = "4 2" ] || fail "case 5 (counters): metrics $(cat "$ST/metrics.txt")"

echo "ok 5 a failed read deletes nothing"
echo "[proxy-cache-integrity] Case 6: on by default"
count() { grep -c -E "$1" "$2" || true; }
for k in ServiceAccount Role RoleBinding ConfigMap CronJob; do
  grep -q -x -F "  name: $OBJ" "$TMP/doc-$k.yaml" || fail "case 6: no $k $OBJ in the default render"
done
grep -q -x -F "  name: $CM" "$DEP" || fail "case 6: no Deployment $CM in the default render"
[ "$(count '^kind: ' "$TMP/default.yaml")" = 6 ] || fail "case 6: the template renders other objects than the six (the counters ConfigMap belongs to the pass)"
sed -n '/^rules:$/,$p' "$TMP/doc-Role.yaml" > "$TMP/rules.yaml"
printf '%s\n' 'rules:' '  - apiGroups: [""]' '    resources: ["events"]' '    verbs: ["create"]' '  - apiGroups: [""]' \
  '    resources: ["configmaps"]' '    verbs: ["create"]' '  - apiGroups: [""]' '    resources: ["configmaps"]' \
  "    resourceNames: [\"$CM\"]" '    verbs: ["get", "patch"]' > "$TMP/rules-want.yaml"
cmp -s "$TMP/rules.yaml" "$TMP/rules-want.yaml" || fail "case 6: the Role allows more than Events and the counters ConfigMap: $(cat "$TMP/rules.yaml")"
for line in '  schedule: "17 * * * *"' '  concurrencyPolicy: Forbid' '      backoffLimit: 0' "          serviceAccountName: $OBJ" \
    '                secretName: harbor-admin' '              command: ["/bin/sh", "/etc/proxy-cache-integrity/check.sh"]'; do
  grep -q -x -F -e "$line" "$CRON" || fail "case 6: the CronJob lacks the line '$line'"
done
# httpd serves the mounted counters (-h = mountPath) on the announced port.
for line in '  replicas: 1' '    type: Recreate' '        prometheus.io/scrape: "true"' '        prometheus.io/port: "9090"' \
    '        prometheus.io/path: /metrics.txt' "            name: $CM" '            optional: true' \
    '          command: ["httpd", "-f", "-p", "9090", "-h", "/srv/metrics"]' '              containerPort: 9090' \
    '              mountPath: /srv/metrics'; do
  grep -q -x -F -e "$line" "$DEP" || fail "case 6: the metrics Deployment lacks the line '$line'"
done
for f in "$CRON" "$DEP"; do
  [ "$(count '^ +image: ' "$f")" = 1 ] && [ "$(count '^ +image: "harbor\.openova\.io/proxy-' "$f")" = 1 ] \
    || fail "case 6: an image of $(basename "$f") does not come through a proxy project"
  [ "$(count '^ +requests:$' "$f") $(count '^ +cpu: ' "$f")" = "1 1" ] || fail "case 6: cpu and memory requests of $(basename "$f")"
  [ "$(count '^ +- name: [A-Z_]*(PASSWORD|TOKEN|KEY|SECRET)' "$f")" = 0 ] || fail "case 6: an env name is refused by the policy secret-not-in-env"
done
[ "$(count '^ +livenessProbe:$' "$DEP") $(count '^ +readinessProbe:$' "$DEP")" = "1 1" ] || fail "case 6: the metrics Deployment lacks a probe"
helm template harbor . --namespace harbor --set proxyCacheIntegrity.enabled=false > "$TMP/off.yaml"
! grep -q -x -F "# Source: bp-harbor/$TPL" "$TMP/off.yaml" || fail "case 6: enabled=false still renders the check"
helm template harbor . --namespace harbor > "$TMP/all.yaml"
grep -q -x -F "# Source: bp-harbor/$TPL" "$TMP/all.yaml" || fail "case 6: the full default render lacks the check"

echo "ok 6 on by default"
echo "proxy-cache-integrity: all 6 cases passed"
