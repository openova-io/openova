#!/usr/bin/env bash
# Test of the scheduled Git bundles of bp-gitea (decision record 0036, B1 Git
# row and B3; FND-184): templates/git-bundle-cronjob.yaml and the gitBundle
# block of values.yaml. CI runs every chart/tests/*.sh as
# `bash <script> <chart dir>`.
#
#   G1  Default render (no bucket): no CronJob.
#   G2  A bucket: one CronJob at the defaults of record 0036 (every 15
#       minutes, 10 bundles kept), the image of the Gitea Deployment, the key
#       pair and the administrator login as mounted files, the cpu and memory
#       requests and limits, no Secret rendered,
#       no secret-like variable name in the environment, and the Job it
#       creates carries the label that the policy flux-managed admits.
#   G3  With a bucket, an empty endpoint, Secret name, image or keep, or a
#       keep below 1 fails the render and names the value.
#   G4  The schedule and the number kept are values an installation may change.
#   G5  Only the writer region renders the CronJob.
#   G6  The Secret register's entry git-bundle-bucket matches the default
#       Secret, and nothing longer.
#   R1  The rendered script, run against stand-ins (real git on local bare
#       repositories, a stand-in curl for the Gitea API and the bucket):
#       one bundle per repository with a commit, named after the head of its
#       default branch, that clones to that head with every ref; none for an
#       empty repository; the 10 newest bundles of a repository stay; other
#       keys and the bundles of a deleted repository are left alone.
#   R2  A second run with no new commit writes and deletes nothing.
#   R3  A new commit gets a new bundle; the oldest bundle beyond 10 goes.
#   R4  No key and no password is ever on a command line.
#   R5  A refused upload fails the run after the other repositories are
#       done; a refused repository list fails the run at once.
#
# Usage: bash tests/git-bundle-render.sh [CHART_DIR]. Needs helm, yq (v4) and git.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REGISTER="$(cd "$CHART_DIR/../../.." && pwd)/scripts/secret-guard-baselines/secret-object-register.yaml"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
pass() { echo "  PASS  $1"; }
# want <label> <expected> <actual>
want() { [ "$3" = "$2" ] || fail "$1: expected '$2', got '$3'"; }
# count <kind> <file>: the number of documents of that kind
count() { yq -N "select(.kind == \"$1\") | .metadata.name" "$2" | grep -c . || true; }

cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then
  helm dependency build >/dev/null
fi
ON=(--set gitBundle.objectStore.bucket=sov-bucket --set gitBundle.objectStore.endpoint=https://s3.example.test)
render() { helm template gitea . --namespace gitea "$@"; }

# ── G1 ────────────────────────────────────────────────────────────────────
render > "$TMP/default.yaml"
want "G1 CronJobs" "0" "$(count CronJob "$TMP/default.yaml")"
pass "G1 default render, no bucket: no CronJob"

# ── G2 ────────────────────────────────────────────────────────────────────
render "${ON[@]}" > "$TMP/on.yaml"
want "G2 CronJobs" "1" "$(count CronJob "$TMP/on.yaml")"
yq -N 'select(.kind == "CronJob")' "$TMP/on.yaml" > "$TMP/cj.yaml"
c() { yq -N "$1" "$TMP/cj.yaml"; }
P='.spec.jobTemplate.spec.template.spec'
env_of() { c "$P.containers[0].env[] | select(.name == \"$1\") | .value"; }
want "G2 CronJob" "gitea-git-bundle/gitea" "$(c '.metadata.name + "/" + .metadata.namespace')"
want "G2 schedule" "*/15 * * * *" "$(c '.spec.schedule')"
want "G2 concurrencyPolicy" "Forbid" "$(c '.spec.concurrencyPolicy')"
want "G2 the Job's label for the policy flux-managed" "flux" "$(c '.spec.jobTemplate.metadata.labels."app.kubernetes.io/managed-by"')"
want "G2 KEEP" "10" "$(env_of KEEP)"
want "G2 BUCKET" "sov-bucket" "$(env_of BUCKET)"
want "G2 ENDPOINT" "https://s3.example.test" "$(env_of ENDPOINT)"
want "G2 GITEA_URL" "http://gitea-http.gitea.svc.cluster.local:3000" "$(env_of GITEA_URL)"
want "G2 restartPolicy" "Never" "$(c "$P.restartPolicy")"
want "G2 runAsNonRoot" "true" "$(c "$P.securityContext.runAsNonRoot")"
want "G2 readOnlyRootFilesystem" "true" "$(c "$P.containers[0].securityContext.readOnlyRootFilesystem")"
want "G2 service account token" "false" "$(c "$P.automountServiceAccountToken")"
want "G2 cpu request" "50m" "$(c "$P.containers[0].resources.requests.cpu")"
want "G2 memory request" "64Mi" "$(c "$P.containers[0].resources.requests.memory")"
want "G2 cpu limit" "500m" "$(c "$P.containers[0].resources.limits.cpu")"
want "G2 memory limit" "512Mi" "$(c "$P.containers[0].resources.limits.memory")"
deploy_image="$(yq -N 'select(.kind == "Deployment" and .metadata.name == "gitea") | .spec.template.spec.containers[0].image' "$TMP/on.yaml")"
want "G2 image is the Gitea Deployment's" "$deploy_image" "$(c "$P.containers[0].image")"
case "$deploy_image" in */proxy-*/*:*) ;; *) fail "G2 image not pulled through a proxy project with a tag: $deploy_image" ;; esac
want "G2 key pair volume" "object-storage s3-access-key,s3-region,s3-secret-key" \
  "$(c "$P.volumes[] | select(.name == \"object-storage\") | .secret.secretName + \" \" + ([.secret.items[].key] | sort | join(\",\"))")"
want "G2 administrator volume" "gitea-admin-secret" "$(c "$P.volumes[] | select(.name == \"gitea-admin\") | .secret.secretName")"
want "G2 mounts" "/etc/gitea-admin /etc/object-storage /tmp" "$(c "[$P.containers[0].volumeMounts[].mountPath] | sort | join(\" \")")"
want "G2 secret-like variable names" "" "$(c "$P.containers[0].env[].name" | grep -i -E 'PASSWORD|TOKEN|KEY|SECRET' || true)"
want "G2 secretKeyRef" "0" "$(grep -c secretKeyRef "$TMP/cj.yaml" || true)"
want "G2 Secrets rendered" "$(count Secret "$TMP/default.yaml")" "$(count Secret "$TMP/on.yaml")"
c "$P.containers[0].args[0]" > "$TMP/script.sh"
grep -q -F 'bundle create -q "$work/b.bundle" --all' "$TMP/script.sh" || fail "G2 the script does not run git bundle create --all"
pass "G2 bucket: one CronJob every 15 minutes, 10 kept, the Deployment's image, keys as mounted files, no Secret rendered"

# ── G3 ────────────────────────────────────────────────────────────────────
for k in objectStore.endpoint objectStore.secretName image keep; do
  if render "${ON[@]}" --set-string "gitBundle.$k=" > /dev/null 2> "$TMP/g3.err"; then
    fail "G3 gitBundle.$k empty: the render succeeded"
  fi
  grep -q -F "gitBundle.$k is required" "$TMP/g3.err" || fail "G3 gitBundle.$k empty: the error does not name the value: $(cat "$TMP/g3.err")"
done
if render "${ON[@]}" --set gitBundle.keep=0 > /dev/null 2> "$TMP/g3.err"; then fail "G3 gitBundle.keep=0: the render succeeded"; fi
grep -q -F "gitBundle.keep must be 1 or more" "$TMP/g3.err" || fail "G3 gitBundle.keep=0: $(cat "$TMP/g3.err")"
pass "G3 with a bucket, an empty endpoint, Secret name, image or keep, or keep below 1, fails the render and is named"

# ── G4 ────────────────────────────────────────────────────────────────────
render "${ON[@]}" --set-string gitBundle.schedule="7 * * * *" --set gitBundle.keep=3 \
  --show-only templates/git-bundle-cronjob.yaml > "$TMP/g4.yaml"
want "G4 schedule" "7 * * * *" "$(yq -N '.spec.schedule' "$TMP/g4.yaml")"
want "G4 KEEP" "3" "$(yq -N "$P.containers[0].env[] | select(.name == \"KEEP\") | .value" "$TMP/g4.yaml")"
pass "G4 an installation may change the schedule and the number kept"

# ── G5 ────────────────────────────────────────────────────────────────────
render "${ON[@]}" --set platformRepos.regionId=region-b --set platformRepos.writerRegionId=region-a > "$TMP/g5-other.yaml"
want "G5 CronJobs in a region that does not take the writes" "0" "$(count CronJob "$TMP/g5-other.yaml")"
render "${ON[@]}" --set platformRepos.regionId=region-a --set platformRepos.writerRegionId=region-a > "$TMP/g5-writer.yaml"
want "G5 CronJobs in the writer region" "1" "$(count CronJob "$TMP/g5-writer.yaml")"
pass "G5 only the region whose Gitea takes the writes renders the CronJob"

# ── G6 ────────────────────────────────────────────────────────────────────
[ -f "$REGISTER" ] || fail "G6 register not found: $REGISTER"
want "G6 entry" "gitea T SEC-342" "$(yq -N '.entries[] | select(.id == "git-bundle-bucket") | .namespace + " " + .class + " " + .removal' "$REGISTER")"
re="$(yq -N '.entries[] | select(.id == "git-bundle-bucket") | .name' "$REGISTER")"
name="$(yq -N '.gitBundle.objectStore.secretName' values.yaml)"
grep -q -E "$re" <<<"$name" || fail "G6 the entry's name $re does not match the default Secret $name"
for n in object-storage-x gitea-object-storage; do
  if grep -q -E "$re" <<<"$n"; then fail "G6 the entry's name $re matches $n"; fi
done
pass "G6 the register entry git-bundle-bucket matches the default Secret of the bundles"

# ── R: the rendered script against stand-ins ──────────────────────────────
export HOME="$TMP/home" GIT_CONFIG_NOSYSTEM=1 GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test
mkdir -p "$HOME" "$TMP/bin" "$TMP/admin" "$TMP/store" "$TMP/s3" "$TMP/srv" "$TMP/api" "$TMP/run"
printf gitea_admin > "$TMP/admin/username"; printf 'pw-R4-admin' > "$TMP/admin/password"
printf 'ak-R4-access' > "$TMP/store/s3-access-key"; printf 'sk-R4-secret' > "$TMP/store/s3-secret-key"; printf region-x > "$TMP/store/s3-region"
AUTH="$(printf 'gitea_admin:pw-R4-admin' | base64 | tr -d '\n')"
: > "$TMP/s3.index"; echo 0 > "$TMP/s3.seq"

# The stand-in curl: the Gitea repository list from $TMP/api/page<N>.json and
# a bucket in $TMP/s3, with an index "<sequence> <key>" for LastModified. It
# logs every command line and refuses a request without the right login.
cat > "$TMP/bin/curl" <<'STUB'
#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >> "$STUB/argv.log"
out=""; fmt=""; method=GET; upload=""; sig=""; conf=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -K) [ "$2" = - ] && conf="$(cat)"; shift 2 ;;
    -o) out="$2"; shift 2 ;;
    -w) fmt="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    -T) upload="$2"; method=PUT; shift 2 ;;
    --aws-sigv4) sig="$2"; shift 2 ;;
    -H|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
emit() { if [ -n "$out" ]; then cat > "$out"; else cat; fi; }
case "$url" in
  */api/v1/repos/search\?*)
    page="${url##*page=}"
    if [ "$conf" != "header = \"Authorization: Basic $AUTH\"" ] || [ -n "${STUB_API_FAIL:-}" ]; then
      echo '{"message":"unauthorized"}' | emit; [ -n "$fmt" ] && printf 401; exit 0
    fi
    if [ -f "$STUB/api/page$page.json" ]; then emit < "$STUB/api/page$page.json"; else echo '{"ok":true,"data":[]}' | emit; fi
    [ -n "$fmt" ] && printf 200; exit 0 ;;
  "$S3_BASE"/*)
    rest="${url#"$S3_BASE"/}"
    [ "$conf" = 'user = "ak-R4-access:sk-R4-secret"' ] && [ "$sig" = "aws:amz:region-x:s3" ] || { echo "curl: (22) 403" >&2; exit 22; }
    case "$method" in
      GET)
        prefix="$(printf '%s' "${rest#*prefix=}" | sed 's#%2F#/#g')"
        { printf '<?xml version="1.0"?><ListBucketResult><Name>sov-bucket</Name>'
          while read -r seq key; do
            case "$key" in "$prefix"*) printf '<Contents><Key>%s</Key><LastModified>2026-11-24T%06d.000Z</LastModified><Size>1</Size></Contents>' "$key" "$seq" ;; esac
          done < "$STUB/s3.index"
          printf '</ListBucketResult>\n'; } | emit ;;
      PUT)
        case "$rest" in "${STUB_PUT_FAIL:-//}"*) echo "curl: (22) 403" >&2; exit 22 ;; esac
        mkdir -p "$STUB/s3/$(dirname "$rest")"; cp "$upload" "$STUB/s3/$rest"
        seq=$(( $(cat "$STUB/s3.seq") + 1 )); echo "$seq" > "$STUB/s3.seq"
        awk -v k="$rest" '$2!=k' "$STUB/s3.index" > "$STUB/s3.index.new" || true
        mv "$STUB/s3.index.new" "$STUB/s3.index"; echo "$seq $rest" >> "$STUB/s3.index"
        echo "PUT $rest" >> "$STUB/writes.log"; : | emit ;;
      DELETE)
        rm -f "$STUB/s3/$rest"; awk -v k="$rest" '$2!=k' "$STUB/s3.index" > "$STUB/s3.index.new" || true
        mv "$STUB/s3.index.new" "$STUB/s3.index"; echo "DELETE $rest" >> "$STUB/writes.log"; : | emit ;;
    esac
    exit 0 ;;
esac
echo "stand-in curl: unexpected $method $url" >&2; exit 2
STUB
chmod +x "$TMP/bin/curl"
# A wrapper of git that logs each command line, so that a test sees a clone.
REAL_GIT="$(command -v git)"
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "$STUB/git.log"\nexec %s "$@"\n' "$REAL_GIT" > "$TMP/bin/git"
chmod +x "$TMP/bin/git"

# seed <key>: an object written before the first run
seed() { mkdir -p "$TMP/s3/$(dirname "$1")"; echo x > "$TMP/s3/$1"; local s=$(( $(cat "$TMP/s3.seq") + 1 )); echo "$s" > "$TMP/s3.seq"; echo "$s $1" >> "$TMP/s3.index"; }
for i in 1 2 3 4 5 6 7 8 9 10 11; do seed "git/openova/sovereign-regions/$(printf 'f%039d' "$i").bundle"; done
seed git/openova/sovereign-regions/notes.txt
for h in a b c; do seed "git/gone/repo/$(printf '%040d' 0 | tr 0 "$h").bundle"; done

# Bare repositories: openova/sovereign-regions (main, feature, tag v1),
# openova/empty-one (no commit), alice/private-app (default branch trunk).
git -c init.defaultBranch=main init -q "$TMP/work"
git -C "$TMP/work" commit -q --allow-empty -m one
git -C "$TMP/work" branch feature
git -C "$TMP/work" tag v1
git -C "$TMP/work" commit -q --allow-empty -m two
mkdir -p "$TMP/srv/openova" "$TMP/srv/alice"
git clone -q --bare "$TMP/work" "$TMP/srv/openova/sovereign-regions.git"
git -c init.defaultBranch=main init -q --bare "$TMP/srv/openova/empty-one.git"
git -c init.defaultBranch=trunk init -q "$TMP/work2"
git -C "$TMP/work2" commit -q --allow-empty -m app
git clone -q --bare "$TMP/work2" "$TMP/srv/alice/private-app.git"
# The list: two repositories, an empty one, the parent of a fork nested in a
# repository object, and user objects whose full_name must not be read.
cat > "$TMP/api/page1.json" <<'JSON'
{"ok":true,"data":[{"id":1,"owner":{"login":"openova","full_name":"Open Ova"},"name":"sovereign-regions","full_name":"openova/sovereign-regions","clone_url":"https://gitea.example.test/openova/sovereign-regions.git","default_branch":"main"},{"id":2,"owner":{"login":"openova","full_name":""},"name":"empty-one","clone_url":"https://gitea.example.test/openova/empty-one.git","empty":true}]}
JSON
cat > "$TMP/api/page2.json" <<'JSON'
{"ok":true,"data":[{"id":3,"owner":{"login":"alice","full_name":"Alice A"},"name":"private-app","private":true,"fork":true,"parent":{"id":1,"clone_url":"https://gitea.example.test/openova/sovereign-regions.git"},"clone_url":"https://gitea.example.test/alice/private-app.git","default_branch":"trunk"}]}
JSON

S3_BASE="https://s3.example.test/sov-bucket"
runscript() {
  : > "$TMP/writes.log"; : > "$TMP/git.log"
  env PATH="$TMP/bin:$PATH" STUB="$TMP" S3_BASE="$S3_BASE" AUTH="$AUTH" \
    GITEA_ADMIN_DIR="$TMP/admin" OBJECT_STORE_DIR="$TMP/store" TMPDIR="$TMP/run" HOME="$TMP/home" \
    GITEA_URL="file://$TMP/srv" BUCKET="$(env_of BUCKET)" ENDPOINT="$(env_of ENDPOINT)" KEEP="$(env_of KEEP)" \
    GIT_TERMINAL_PROMPT=0 "$@" sh "$TMP/script.sh" > "$TMP/run.log" 2>&1
}
keys() { awk '{print $2}' "$TMP/s3.index" | grep -E "^$1" | sort || true; }
head_of() { git -C "$TMP/srv/$1.git" rev-parse HEAD; }

# ── R1 ────────────────────────────────────────────────────────────────────
runscript || fail "R1 the first run failed: $(cat "$TMP/run.log")"
h1="$(head_of openova/sovereign-regions)"; ha="$(head_of alice/private-app)"
want "R1 writes" "PUT git/alice/private-app/$ha.bundle,PUT git/openova/sovereign-regions/$h1.bundle" \
  "$(grep '^PUT' "$TMP/writes.log" | sort | paste -s -d, -)"
want "R1 bundles of the empty repository" "" "$(keys git/openova/empty-one/)"
want "R1 bundles kept" "10" "$(keys 'git/openova/sovereign-regions/[0-9a-f]+\.bundle$' | grep -c . || true)"
want "R1 deleted" "DELETE git/openova/sovereign-regions/$(printf 'f%039d' 1).bundle,DELETE git/openova/sovereign-regions/$(printf 'f%039d' 2).bundle" \
  "$(grep '^DELETE' "$TMP/writes.log" | sort | paste -s -d, -)"
want "R1 other key" "git/openova/sovereign-regions/notes.txt" "$(keys git/openova/sovereign-regions/notes)"
want "R1 deleted repository" "3" "$(keys git/gone/repo/ | grep -c . || true)"
git clone -q "$TMP/s3/git/openova/sovereign-regions/$h1.bundle" "$TMP/restore"
want "R1 restored head" "$h1" "$(git -C "$TMP/restore" rev-parse HEAD)"
want "R1 restored refs" "HEAD refs/heads/feature refs/heads/main refs/tags/v1" \
  "$(git bundle list-heads "$TMP/s3/git/openova/sovereign-regions/$h1.bundle" | cut -d' ' -f2 | sort | paste -s -d' ' -)"
want "R1 default branch trunk" "$ha" "$(git bundle list-heads "$TMP/s3/git/alice/private-app/$ha.bundle" | awk '$2=="HEAD"{print $1}')"
pass "R1 one bundle per repository with a commit, named after its head, clones to it with every ref; 10 kept; other keys left alone"

# ── R2 ────────────────────────────────────────────────────────────────────
runscript || fail "R2 the second run failed: $(cat "$TMP/run.log")"
want "R2 writes" "" "$(cat "$TMP/writes.log")"
want "R2 clones" "0" "$(grep -c -E '^clone ' "$TMP/git.log" || true)"
pass "R2 a run with no new commit clones, writes and deletes nothing"

# ── R3 ────────────────────────────────────────────────────────────────────
git -C "$TMP/work" commit -q --allow-empty -m three
git -C "$TMP/work" push -q "$TMP/srv/openova/sovereign-regions.git" main
h3="$(head_of openova/sovereign-regions)"
runscript || fail "R3 the third run failed: $(cat "$TMP/run.log")"
want "R3 writes" "PUT git/openova/sovereign-regions/$h3.bundle,DELETE git/openova/sovereign-regions/$(printf 'f%039d' 3).bundle" \
  "$(paste -s -d, - < "$TMP/writes.log")"
want "R3 bundles kept" "10" "$(keys 'git/openova/sovereign-regions/[0-9a-f]+\.bundle$' | grep -c . || true)"
pass "R3 a new commit gets its bundle and the oldest beyond 10 is deleted"

# ── R4 ────────────────────────────────────────────────────────────────────
for s in pw-R4-admin ak-R4-access sk-R4-secret "$AUTH"; do
  if grep -q -F -e "$s" "$TMP/argv.log" "$TMP/git.log"; then fail "R4 a credential is on a command line: $s"; fi
done
grep -q -F -e '--aws-sigv4 aws:amz:region-x:s3' "$TMP/argv.log" || fail "R4 no signed request"
pass "R4 no key and no password is ever on a command line"

# ── R5 ────────────────────────────────────────────────────────────────────
git -C "$TMP/work2" commit -q --allow-empty -m app2
git -C "$TMP/work2" push -q "$TMP/srv/alice/private-app.git" trunk
git -C "$TMP/work" commit -q --allow-empty -m four
git -C "$TMP/work" push -q "$TMP/srv/openova/sovereign-regions.git" main
if runscript STUB_PUT_FAIL=git/alice/; then fail "R5 a refused upload: the run succeeded"; fi
grep -q -F 'alice/private-app: FAILED' "$TMP/run.log" || fail "R5 the refused repository is not named: $(cat "$TMP/run.log")"
want "R5 the other repository" "PUT git/openova/sovereign-regions/$(head_of openova/sovereign-regions).bundle" "$(grep '^PUT' "$TMP/writes.log" || true)"
if runscript STUB_API_FAIL=1; then fail "R5 a refused repository list: the run succeeded"; fi
grep -q -F 'FATAL: the repository list, page 1, answered 401' "$TMP/run.log" || fail "R5 the refused list is not named: $(cat "$TMP/run.log")"
want "R5 writes after a refused list" "" "$(cat "$TMP/writes.log")"
pass "R5 a refused upload fails the run after the others; a refused repository list fails it at once"

echo "git-bundle-render: all cases passed"
