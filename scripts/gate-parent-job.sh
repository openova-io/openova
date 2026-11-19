#!/usr/bin/env bash
# gate-parent-job.sh - the gate's cluster work as a short-lived Job on the parent (FND-078).
#
# The gate workflows (.github/workflows/foundation-gate.yaml, foundation-gate-standing.yaml) run on
# machines hosted by GitHub. In parent execution, everything that dials a Sovereign's API port runs
# in a Job on the parent, created through the scoped access of FND-079: namespace acceptance-gate,
# ServiceAccount gate (jobs and pods: create, get, list, watch, delete; pods/log: get; secrets:
# create, delete, never read; networkpolicies: create, get, delete), Pod Security restricted, a quota
# of 4 pods. The namespace's standing NetworkPolicy denies everything but DNS; each run creates its own
# NetworkPolicy, named as its Job, that lets the Job's Pods reach TCP 6443 and 443 of the addresses of
# the install under test only (/32 each, given with --targets), and deletes it with the Job. The
# manifest of the access is scripts/gate-parent-access.yaml. The node's container runtime pulls the
# images, outside the Pod's network, so every image is named by its digest.
#
#   gate-parent-job.sh execution <value>     prints hosted or parent; empty means hosted; else exit 2
#   gate-parent-job.sh preflight <value>     prints the input preflight without the prefix PREFLIGHT=
#                                            that the flight checklist prints before it
#   gate-parent-job.sh targets --env <label>
#        On the hosted machine, before any Job: prints the addresses of the install, joined by commas:
#        the API servers named by GATE_KUBECONFIG_A and _B (read from the files; a name is resolved),
#        and the IPv4 addresses the DNS gives for console.<label>.omani.works (required) and the other
#        hosts of GATEWAY_HOSTS that the suite and the probe dial. Nothing is dialed.
#   gate-parent-job.sh policy <name> <addresses>   prints the NetworkPolicy a run creates (no cluster)
#   gate-parent-job.sh image --candidate <dir> --commit <sha> --output <file>
#        Builds or reuses, and pushes with docker buildx: the suite image of the commit under test,
#        built with tests/acceptance/sovereign/Containerfile as build-conformance.yaml builds it;
#        the Job image, that suite image by digest plus the commit's files at /opt/candidate (the
#        repository root a run in cloud reads); the probe image, the Playwright image by digest
#        plus the playwright package. Writes job=<ref@digest>, probe=<ref@digest> and, when it
#        built it, suite=<ref@digest> into <file>. Needs GH_TOKEN and GITHUB_ACTOR.
#   gate-parent-job.sh run --mode addresses|live|regions --out <dir> --targets <addresses>
#                         --job-image <ref@digest> [--probe-image <ref@digest>]
#        Creates the run's NetworkPolicy for <addresses> (as targets prints them; none, or one that is
#        not the IPv4 address of a host, ends the run with exit code 2 before anything is created),
#        writes the region kubeconfigs and the tool scripts into a Secret of the run (never into
#        the Job object), the pull credential into a second Secret, creates the Job with the run's
#        inputs, streams its log, fetches the files the Job printed and deletes the Job, both
#        Secrets and the NetworkPolicy whatever happened. Into <dir>: name, outputs (key=value),
#        summary.md, errors, files/<path>, gate.log. Exit 0 when the Job did its work and every file
#        arrived whole.
#   gate-parent-job.sh cleanup --out <dir>   deletes the Job, the Secrets and the NetworkPolicy named in <dir>
#   gate-parent-job.sh recipe job <base>|probe   prints a generated Containerfile
#   gate-parent-job.sh inner addresses|live|regions   in the Job, container gate
#   gate-parent-job.sh probe                 in the Job, container probe (live only)
#   gate-parent-job.sh --self-test           no cluster, no network, no docker
#
# Environment of run and cleanup (files are never printed):
#   GATE_PARENT_KUBECONFIG   the kubeconfig of the access (secret ACCEPTANCE_PARENT_KUBECONFIG)
#   GATE_KUBECONFIG_A, _B    the kubeconfigs of the regions that start as Primary and as Standby
#   GATE_SESSION_FILE        optional: an owner session (secret ACCEPTANCE_CATALYST_SESSION)
#   GATE_PULL_USER, GATE_PULL_TOKEN   the credential the node pulls the images with
#   GATE_TOOLS               the checkout of the gate tools (default: the folder above this one)
#   GATE_RESOURCES           optional: requests and limits of the Job's containers that differ from
#                            RESOURCES_DEFAULT below, as <gate|probe>.<requests|limits>.<cpu|memory>=<quantity>
#                            separated by commas (a CPU in m or cores, a memory in Ki, Mi or Gi); the
#                            workflows pass the variable GATE_PARENT_RESOURCES of the environment gate.
#                            A value that is not valid ends the run with exit code 2 before anything is created.
#   The run's inputs, copied into the Job's environment: ENV_LABEL, GATE_RUN, CANDIDATE_COMMIT,
#   GITHUB_ACTIONS, GITHUB_WORKFLOW_REF, GITHUB_RUN_ID, GITHUB_RUN_ATTEMPT and every ACCEPT_*
#   except the paths ACCEPT_KUBECONFIG_A, _B, ACCEPT_MANIFEST, ACCEPT_REPO_ROOT, ACCEPT_RECORD_DIR.
# Every call to the parent retries a failure that looks like a lost connection, at most
# GATE_PARENT_TRIES times (default 6, waiting GATE_PARENT_BACKOFF x attempt seconds, default 5).
# The Pod's containers are read on every pass: every container must start within GATE_START_MINUTES
# (default 30); a container that can never start fails the run at once, an image the node cannot
# pull after GATE_PULL_MINUTES of back-off (default 5); a Pod the scheduler has not placed is
# reported with its phase and the scheduler's reason; once the container gate has ended, its log
# is read whether the probe has ended or not, and the wait ends GATE_FOLLOW_GRACE_SECONDS (default
# 300) after the Job's own deadline at the latest.
# In the Job no wait is silent: every wait is bounded and prints what it waits for at least once a
# minute. Each reading of a region is bounded by GATE_READ_SECONDS (default 20); the first reading of
# each region is logged; a region that cannot be read for GATE_UNREAD_MINUTES (default 5) in a row ends
# the run at once with a line that names the region, its API address and the error.
#
# Exit codes: 0 done, 1 failed, 2 wrong usage.
set -uo pipefail

VERSION=1
NS=acceptance-gate
IMAGE_REPO="${GATE_IMAGE_REPO:-ghcr.io/openova-io/openova/conformance}"
PLAYWRIGHT_BASE="mcr.microsoft.com/playwright:v1.46.0-jammy@sha256:860c541d62e212fa2d857afca98730dad12b641f941b9b5ed892e379e9e121bb"
PLAYWRIGHT_VERSION=1.46.0
SECRET_DIR=/run/gate-secret
SHARED=/shared
SCRATCH=/scratch
DIGEST_RE='^[a-z0-9][a-z0-9.:_/-]*@sha256:[0-9a-f]{64}$'
TRANSIENT_RE='connection (lost|refused|reset)|reset by peer|i/o timeout|TLS handshake timeout|unexpected EOF|EOF$|http2:|Unable to connect to the server|ServiceUnavailable|InternalError|Too Many Requests|context deadline exceeded|Client.Timeout|timed out|the server is currently unable|net/http'
# The resources of the Job's containers, the one place they are set. A request is what the scheduler
# reserves on the parent's node, so it is sized from what the container needs to START, not from its
# peak: the parent's node is 89% requested (run 44443420236 waited 30 minutes for 1 CPU and 1.5 GiB),
# and the live Pod must fit in about 1 GiB and half a CPU. Measured with podman on the images of the
# recipes: the container gate starts in 8 MiB (bash, kubectl, python3, the suite) and reads an 11 MiB
# list in 101 MiB; the container probe waits in 3 MiB and peaks at 575 MiB while Chromium loads four
# pages in two contexts, as FND-G07 does for about a minute. A limit is the ceiling of that peak. A
# container above its request is the first one evicted when the node runs short, so the parent's own
# services stay. A larger parent raises any value with GATE_RESOURCES.
RESOURCES_DEFAULT='gate.requests.cpu=250m,gate.requests.memory=256Mi,gate.limits.cpu=1,gate.limits.memory=1Gi,probe.requests.cpu=100m,probe.requests.memory=128Mi,probe.limits.cpu=1,probe.limits.memory=2Gi'
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
TOOLS="${GATE_TOOLS:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

die() { echo "gate-parent-job: $*" >&2; exit 1; }
usage() { sed -n '2,/^# Exit codes/p' "$SELF" >&2; exit 2; }

# ---------------------------------------------------------------- the access, with a bounded retry
# pk ARGS...: kubectl on the parent, namespace acceptance-gate. A failure whose message looks like a
# lost connection is tried again; any other failure ends at once. A create that answers AlreadyExists
# after a lost connection was done by the attempt before, so it counts as done.
pk() {
  local tries="${GATE_PARENT_TRIES:-6}" backoff="${GATE_PARENT_BACKOFF:-5}" i rc o e
  o="$(mktemp)"; e="$(mktemp)"
  for ((i = 1; i <= tries; i++)); do
    kubectl --kubeconfig "$GATE_PARENT_KUBECONFIG" --request-timeout=60s -n "$NS" "$@" > "$o" 2> "$e"
    rc=$?
    if [ "$rc" = 0 ]; then cat "$o"; rm -f "$o" "$e"; return 0; fi
    if [ "$i" -gt 1 ] && [ "${1:-}" = create ] && grep -q 'AlreadyExists' "$e"; then
      rm -f "$o" "$e"; return 0
    fi
    if ! grep -Eq "$TRANSIENT_RE" "$e"; then
      echo "gate-parent-job: the parent refused kubectl ${1:-}: $(head -c 400 "$e")" >&2
      rm -f "$o" "$e"; return 1
    fi
    echo "gate-parent-job: the parent did not answer kubectl ${1:-} (attempt $i of $tries): $(head -c 200 "$e")" >&2
    [ "$i" -lt "$tries" ] && sleep $((backoff * i))
  done
  echo "gate-parent-job: the parent did not answer kubectl ${1:-} after $tries attempts" >&2
  rm -f "$o" "$e"; return 1
}

# ---------------------------------------------------------------- execution
cmd_execution() {
  case "${1:-}" in
    '' | hosted) echo hosted ;;
    parent) echo parent ;;
    *) echo "gate-parent-job: the input execution must be hosted or parent, not '${1:-}'" >&2; exit 2 ;;
  esac
}

# ---------------------------------------------------------------- preflight
cmd_preflight() { # the one place the prefix is taken off: the flight checklist prints PREFLIGHT=<value>
  local v="${1:-}"
  printf '%s\n' "${v#PREFLIGHT=}"
}

# ---------------------------------------------------------------- the addresses a run may reach
# The hosts under <label>.omani.works that the suite and the probe dial on TCP 443 (console, the sign-in
# and the applications behind the gateway); console must resolve, the others when they exist.
GATEWAY_HOSTS="console auth bao chargeback gitea grafana guacamole hubble marketplace newapi pdns-admin registry"

is_ipv4() { # $1 is the IPv4 address of a host: four numbers up to 255, the first 1 to 223 and not 127
  [[ "$1" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || return 1
  local a=$((10#${BASH_REMATCH[1]})) b=$((10#${BASH_REMATCH[2]})) c=$((10#${BASH_REMATCH[3]})) d=$((10#${BASH_REMATCH[4]}))
  [ "$a" -ge 1 ] && [ "$a" -le 223 ] && [ "$a" != 127 ] && [ "$b" -le 255 ] && [ "$c" -le 255 ] && [ "$d" -le 255 ]
}

norm_targets() { # $1 addresses separated by commas or blanks: prints them sorted, each once, joined by commas
  local x out="" list
  read -ra list <<< "$(printf '%s' "${1:-}" | tr ',\n\t' '   ')"
  for x in "${list[@]}"; do
    is_ipv4 "$x" || { echo "gate-parent-job: '$x' is not the IPv4 address of a host" >&2; return 1; }
    out="$out$x"$'\n'
  done
  [ -n "$out" ] || { echo "gate-parent-job: no address of the install under test was given" >&2; return 1; }
  printf '%s' "$out" | sort -u -t. -k1,1n -k2,2n -k3,3n -k4,4n | paste -sd, -
}

kube_server() { # $1 a kubeconfig: the host of the API server of its current context, read from the file only
  local u
  u="$(kubectl config view --kubeconfig "$1" --minify --raw -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null)" || return 1
  u="${u#*://}"; u="${u%%/*}"
  case "$u" in \[*) u="${u%%]*}"; u="${u#[}" ;; *) u="${u%:*}" ;; esac
  [ -n "$u" ] && printf '%s\n' "$u"
}

resolve4() { # $1 a host name or an IPv4 address: its IPv4 addresses, one a line; nothing when it does not resolve
  if is_ipv4 "$1"; then echo "$1"; return 0; fi
  getent ahostsv4 "$1" 2>/dev/null | awk '{print $1}' | sort -u
}

cmd_targets() {
  [ "${1:-}" = --env ] && [ -n "${2:-}" ] && [ $# = 2 ] || usage
  local label="$2" r v h a all=""
  [[ "$label" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "gate-parent-job: --env must be an environment label such as hw307, not '$label'" >&2; exit 2; }
  for r in a b; do
    v="GATE_KUBECONFIG_${r^}"
    [ -r "${!v:-}" ] || die "$v is not a readable file"
    h="$(kube_server "${!v}")" || die "the kubeconfig of region $r names no API server"
    a="$(resolve4 "$h")"
    [ -n "$a" ] || die "the API server $h of region $r has no IPv4 address"
    echo "gate-parent-job: the API server of region $r: $h -> $(printf '%s ' $a)" >&2
    all="$all $a"
  done
  for h in $GATEWAY_HOSTS; do
    a="$(resolve4 "$h.$label.omani.works")"
    echo "gate-parent-job: $h.$label.omani.works -> ${a:+$(printf '%s ' $a)}${a:-no IPv4 address}" >&2
    if [ -z "$a" ]; then [ "$h" != console ] || die "console.$label.omani.works has no IPv4 address"; continue; fi
    all="$all $a"
  done
  norm_targets "$all" || exit 1
}

policy_json() { # $1 the run's name, $2 its addresses (as norm_targets prints them): the run's NetworkPolicy
  local a to="" run="${GATE_RUN:-}"
  run="${run//[^0-9A-Za-z._-]/}"
  for a in ${2//,/ }; do to="$to${to:+, }{\"ipBlock\": {\"cidr\": \"$a/32\"}}"; done
  printf '{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",\n'
  printf ' "metadata": {"name": "%s", "namespace": "%s", "labels": {"app.kubernetes.io/managed-by": "gate-parent-job", "openova.io/gate-run": "%s"}},\n' "$1" "$NS" "${run:-none}"
  printf ' "spec": {"podSelector": {"matchLabels": {"batch.kubernetes.io/job-name": "%s"}}, "policyTypes": ["Egress"],\n' "$1"
  printf '  "egress": [{"to": [%s], "ports": [{"protocol": "TCP", "port": 6443}, {"protocol": "TCP", "port": 443}]}]}}\n' "$to"
}

cmd_policy() {
  [ $# = 2 ] && [[ "$1" =~ ^gate-[a-z0-9-]{1,58}$ ]] || usage
  local t
  t="$(norm_targets "$2")" || exit 2
  policy_json "$1" "$t"
}

# ---------------------------------------------------------------- recipes
recipe_job() { # $1 the suite image by digest
  cat <<EOF
# The Job image of the gate (generated by scripts/gate-parent-job.sh, recipe $VERSION): the suite
# image of the commit under test, by digest, and that commit's files at /opt/candidate, the
# repository root that a run in cloud reads (the scripts and tables its checks run and read).
# /opt/candidate/.git names that commit in HEAD and holds no object: the suite reads only HEAD.
FROM $1
USER 0:0
RUN apk add --no-cache git py3-yaml jq curl openssl
COPY --chown=65534:65534 candidate /opt/candidate
USER 65534:65534
WORKDIR /opt/candidate
RUN test "\$(git rev-parse HEAD)" = "\$SUITE_GIT_SHA" && test -x /usr/local/bin/sovereign-acceptance && kubectl version --client >/dev/null && python3 -c 'import yaml'
EOF
}

recipe_probe() {
  cat <<EOF
# The probe image of the gate (generated by scripts/gate-parent-job.sh, recipe $VERSION): the
# Playwright image by digest and the playwright package of the same release, for the browser probe
# of gate check FND-G07 (scripts/sso-zero-click-probe.mjs, delivered in the Secret of the run).
FROM $PLAYWRIGHT_BASE
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
RUN mkdir -p /opt/probe && cd /opt/probe && printf '{"private": true}\n' > package.json \\
 && npm install --no-audit --no-fund playwright@$PLAYWRIGHT_VERSION \\
 && node -e 'const fs = require("fs"); const p = require("playwright").chromium.executablePath(); if (!fs.existsSync(p)) { console.error("no Chromium at " + p); process.exit(1); } console.log(p)' \\
 && chmod -R a+rX /opt/probe
USER 65534:65534
EOF
}

recipe_hash() { # $1 job or probe: the first 12 hex of the recipe's SHA-256
  if [ "$1" = job ]; then recipe_job BASE; else recipe_probe; fi | sha256sum | cut -c1-12
}

cmd_recipe() {
  case "${1:-}" in
    job) [ -n "${2:-}" ] || usage; recipe_job "$2" ;;
    probe) recipe_probe ;;
    *) usage ;;
  esac
}

# ---------------------------------------------------------------- images
existing_digest() { # $1 tag: prints sha256:<hex> when the tag exists as one manifest
  docker buildx imagetools inspect "$1" --format '{{json .Manifest}}' 2>/dev/null | python3 -c '
import json, sys
try:
    m = json.load(sys.stdin)
except ValueError:
    sys.exit(1)
d, t = m.get("digest", ""), m.get("mediaType", "")
ok = t.endswith("manifest.v2+json") or t.endswith("image.manifest.v1+json")
sys.exit(0 if ok and print(d) is None and d.startswith("sha256:") else 1)'
}

build_push() { # $1 tag, $2 Containerfile, $3 context, more: extra flags; prints the digest
  local tag="$1" file="$2" ctx="$3" meta
  shift 3
  meta="$(mktemp)"
  docker buildx build --builder gate-parent-job --platform linux/amd64 --provenance=false --sbom=false \
    --label org.opencontainers.image.source=https://github.com/openova-io/openova \
    -f "$file" -t "$tag" --push --metadata-file "$meta" "$@" "$ctx" >&2 || { rm -f "$meta"; return 1; }
  python3 -c 'import json, sys; d = json.load(open(sys.argv[1])).get("containerimage.digest", ""); print(d); sys.exit(0 if d.startswith("sha256:") else 1)' "$meta"
  local rc=$?; rm -f "$meta"; return $rc
}

cmd_image() {
  local cand="" commit="" output=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --candidate) cand="$2"; shift 2 ;;
      --commit) commit="$2"; shift 2 ;;
      --output) output="$2"; shift 2 ;;
      *) usage ;;
    esac
  done
  [ -n "$cand" ] && [ -n "$output" ] && [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || usage
  [ "$(git -C "$cand" rev-parse HEAD)" = "$commit" ] || die "the checkout $cand is not at the commit $commit"
  [ -n "${GH_TOKEN:-}" ] && [ -n "${GITHUB_ACTOR:-}" ] || die "GH_TOKEN and GITHUB_ACTOR are needed to push the images"
  printf '%s' "$GH_TOKEN" | docker login "${IMAGE_REPO%%/*}" -u "$GITHUB_ACTOR" --password-stdin >&2 || die "docker login failed"
  docker buildx inspect gate-parent-job >/dev/null 2>&1 || docker buildx create --name gate-parent-job --driver docker-container >&2 \
    || die "the builder could not be created"
  local work probe job suite="" jtag ptag d
  work="$(mktemp -d)"
  ptag="$IMAGE_REPO:gate-probe-$(recipe_hash probe)"
  if d="$(existing_digest "$ptag")"; then
    echo "gate-parent-job: the probe image exists: $ptag" >&2
  else
    recipe_probe > "$work/probe.Containerfile"
    mkdir -p "$work/probe"
    d="$(build_push "$ptag" "$work/probe.Containerfile" "$work/probe")" || die "the probe image could not be built and pushed"
  fi
  probe="$IMAGE_REPO@$d"
  jtag="$IMAGE_REPO:gate-job-$commit-$(recipe_hash job)"
  if d="$(existing_digest "$jtag")"; then
    echo "gate-parent-job: the Job image of $commit exists: $jtag" >&2
  else
    # The suite image, as .github/workflows/build-conformance.yaml builds it.
    local ctx="$work/suite"
    mkdir -p "$ctx/scripts"
    (cd "$cand/tests/acceptance/sovereign" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
      -o "$ctx/sovereign-acceptance" ./cmd/sovereign-acceptance) || die "the suite could not be built"
    cp "$cand/scripts/check-live-nodeports.sh" "$ctx/scripts/check-live-nodeports.sh"
    (cd "$cand" && python3 scripts/acceptance-gate.py export) > "$ctx/gates.json" || die "the manifest could not be exported"
    d="$(build_push "$IMAGE_REPO:gate-suite-$commit" "$cand/tests/acceptance/sovereign/Containerfile" "$ctx" \
      --build-arg "SUITE_GIT_SHA=$commit" --label "org.opencontainers.image.revision=$commit" \
      --label org.opencontainers.image.title=conformance)" || die "the suite image could not be built and pushed"
    suite="$IMAGE_REPO@$d"
    # The Job image: the suite image by digest and the commit's files without media files.
    mkdir -p "$work/job/candidate/.git/objects" "$work/job/candidate/.git/refs"
    git -C "$cand" archive --format=tar "$commit" -- . \
      ':(exclude,glob)**/*.png' ':(exclude,glob)**/*.jpg' ':(exclude,glob)**/*.jpeg' ':(exclude,glob)**/*.gif' \
      ':(exclude,glob)**/*.webp' ':(exclude,glob)**/*.mp4' ':(exclude,glob)**/*.webm' ':(exclude,glob)**/*.mov' \
      ':(exclude,glob)**/*.pdf' ':(exclude,glob)**/*.pptx' ':(exclude,glob)**/*.docx' ':(exclude,glob)**/*.xlsx' \
      ':(exclude,glob)**/*.zip' | tar -x -C "$work/job/candidate" || die "the files of $commit could not be written"
    printf '%s\n' "$commit" > "$work/job/candidate/.git/HEAD"
    recipe_job "$suite" > "$work/job/Containerfile"
    d="$(build_push "$jtag" "$work/job/Containerfile" "$work/job" --label "org.opencontainers.image.revision=$commit")" \
      || die "the Job image could not be built and pushed"
  fi
  job="$IMAGE_REPO@$d"
  rm -rf "$work"
  { echo "job=$job"; echo "probe=$probe"; [ -z "$suite" ] || echo "suite=$suite"; } > "$output"
  cat "$output"
}

# ---------------------------------------------------------------- resources
# resources_json: RESOURCES_DEFAULT with GATE_RESOURCES over it, as JSON {container: {requests|limits:
# {cpu, memory}}} and, under "amount", the same in millicores and bytes. Exit 2 when a value is not
# valid: an unknown key, a key named twice, a quantity not of its resource's form or not above 0, a
# request above its limit.
resources_json() {
  python3 - "$RESOURCES_DEFAULT" "${GATE_RESOURCES:-}" <<'PY'
import json, re, sys
def bad(msg):
    print("gate-parent-job: GATE_RESOURCES: " + msg, file=sys.stderr); sys.exit(2)
def parse(text):
    out = {}
    for item in (x for x in re.split(r"[\s,]+", text) if x):
        key, eq, value = item.partition("=")
        p = key.split(".")
        if not eq or len(p) != 3 or p[0] not in ("gate", "probe") or p[1] not in ("requests", "limits") or p[2] not in ("cpu", "memory"):
            bad("'%s' is not <gate|probe>.<requests|limits>.<cpu|memory>=<quantity>" % item)
        if key in out:
            bad("%s is named twice" % key)
        out[key] = value
    return out
def amount(key, value):  # millicores or bytes, or None when the quantity is not of the resource's form
    if key.endswith(".cpu"):
        m = re.fullmatch(r"([0-9]{1,6})m|([0-9]{1,3})(?:\.([0-9]{1,3}))?", value)
        if not m:
            return None
        return int(m.group(1)) if m.group(1) else int(m.group(2)) * 1000 + int((m.group(3) or "").ljust(3, "0"))
    m = re.fullmatch(r"([0-9]{1,7})(Ki|Mi|Gi)", value)
    return int(m.group(1)) << {"Ki": 10, "Mi": 20, "Gi": 30}[m.group(2)] if m else None
values = parse(sys.argv[1])
values.update(parse(sys.argv[2]))
res, amt = {}, {}
for key, value in sorted(values.items()):
    a = amount(key, value)
    if not a:
        bad("%s=%s is not a %s above 0" % (key, value, "CPU in m or cores" if key.endswith(".cpu") else "memory in Ki, Mi or Gi"))
    c, kind, r = key.split(".")
    res.setdefault(c, {}).setdefault(kind, {})[r] = value
    amt.setdefault(c, {}).setdefault(kind, {})[r] = a
for c in sorted(amt):
    for r in ("cpu", "memory"):
        if amt[c]["requests"][r] > amt[c]["limits"][r]:
            bad("%s.requests.%s=%s is above its limit %s" % (c, r, res[c]["requests"][r], res[c]["limits"][r]))
res["amount"] = amt
print(json.dumps(res, sort_keys=True))
PY
}

# ---------------------------------------------------------------- manifests
# manifests MODE NAME JOB_IMAGE PROBE_IMAGE OUTDIR: writes secret.json, pull.json (when a pull
# credential is set), job.json and resources (one line: what the Pod requests and its limits). Only
# secret.json and pull.json hold a credential; they are written with mode 600 and removed by the
# caller once created. Exit 2 when GATE_RESOURCES is not valid.
manifests() {
  local res
  res="$(resources_json)" || return 2
  ( umask 077
    python3 - "$@" "$SELF" "$TOOLS" "$res" <<'PY'
import base64, json, os, re, sys
mode, name, job_image, probe_image, out, self_path, tools, res = sys.argv[1:9]
res = json.loads(res)
labels = {"app.kubernetes.io/managed-by": "gate-parent-job",
          "openova.io/gate-run": re.sub(r"[^0-9A-Za-z._-]", "", os.environ.get("GATE_RUN", "")) or "none",
          "openova.io/gate-mode": mode}
def b64(data):
    return base64.b64encode(data).decode()
def read(path):
    with open(path, "rb") as f:
        return f.read()
data = {"ka": b64(read(os.environ["GATE_KUBECONFIG_A"])), "kb": b64(read(os.environ["GATE_KUBECONFIG_B"])),
        "gate-parent-job.sh": b64(read(self_path))}
for tool in ("gate-outside-dial.py", "standing-gate.py", "sso-zero-click-probe.mjs"):
    data[tool] = b64(read(os.path.join(tools, "scripts", tool)))
sess = os.environ.get("GATE_SESSION_FILE", "")
data["session"] = b64(read(sess).strip()) if sess and os.path.isfile(sess) else ""
secret = {"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
          "metadata": {"name": name, "namespace": "acceptance-gate", "labels": labels}, "data": data}
json.dump(secret, open(os.path.join(out, "secret.json"), "w"))
pull_name = ""
user, token = os.environ.get("GATE_PULL_USER", ""), os.environ.get("GATE_PULL_TOKEN", "")
if user and token:
    pull_name = name + "-pull"
    host = job_image.split("/")[0]
    cfg = {"auths": {host: {"username": user, "password": token, "auth": b64((user + ":" + token).encode())}}}
    pull = {"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/dockerconfigjson",
            "metadata": {"name": pull_name, "namespace": "acceptance-gate", "labels": labels},
            "data": {".dockerconfigjson": b64(json.dumps(cfg).encode())}}
    json.dump(pull, open(os.path.join(out, "pull.json"), "w"))
paths = {"ACCEPT_KUBECONFIG_A", "ACCEPT_KUBECONFIG_B", "ACCEPT_MANIFEST", "ACCEPT_REPO_ROOT", "ACCEPT_RECORD_DIR"}
fixed = ["ENV_LABEL", "GATE_RUN", "CANDIDATE_COMMIT", "GITHUB_ACTIONS", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID",
         "GITHUB_RUN_ATTEMPT"]
names = fixed + sorted(k for k in os.environ if k.startswith("ACCEPT_") and k not in paths)
env = [{"name": k, "value": os.environ[k]} for k in names if k in os.environ]
env += [{"name": "HOME", "value": "/scratch/home"}, {"name": "TMPDIR", "value": "/scratch/tmp"}]
restricted = {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}, "runAsNonRoot": True}
mounts = [{"name": "run", "mountPath": "/run/gate-secret", "readOnly": True},
          {"name": "shared", "mountPath": "/shared"}, {"name": "scratch", "mountPath": "/scratch"}]
containers = [{"name": "gate", "image": job_image, "imagePullPolicy": "IfNotPresent",
               "command": ["/bin/bash", "/run/gate-secret/gate-parent-job.sh", "inner", mode],
               "env": env, "securityContext": restricted, "volumeMounts": mounts,
               "resources": {k: dict(res["gate"][k]) for k in ("requests", "limits")}}]
volumes = [{"name": "run", "secret": {"secretName": name, "defaultMode": 0o440}},
           {"name": "shared", "emptyDir": {"medium": "Memory", "sizeLimit": "16Mi"}},
           {"name": "scratch", "emptyDir": {"sizeLimit": "4Gi"}}]
if mode == "live":
    containers.append({"name": "probe", "image": probe_image, "imagePullPolicy": "IfNotPresent",
                       "command": ["/bin/bash", "/run/gate-secret/gate-parent-job.sh", "probe"],
                       "env": env, "securityContext": restricted,
                       "volumeMounts": mounts + [{"name": "shm", "mountPath": "/dev/shm"}],
                       "resources": {k: dict(res["probe"][k]) for k in ("requests", "limits")}})
    volumes.append({"name": "shm", "emptyDir": {"medium": "Memory", "sizeLimit": "256Mi"}})
def total(kind):
    names = [c["name"] for c in containers]
    cpu = sum(res["amount"][n][kind]["cpu"] for n in names)
    mem = sum(res["amount"][n][kind]["memory"] for n in names)
    each = ", ".join("%s %s CPU %s" % (n, res[n][kind]["cpu"], res[n][kind]["memory"]) for n in names)
    return "%dm CPU and %dMi (%s)" % (cpu, mem >> 20, each)
open(os.path.join(out, "resources"), "w").write("its Pod requests %s; limits %s\n" % (total("requests"), total("limits")))
deadline = {"addresses": 900, "regions": 9000, "live": 19200}[mode]
pod = {"restartPolicy": "Never", "automountServiceAccountToken": False, "enableServiceLinks": False,
       "securityContext": {"runAsNonRoot": True, "runAsUser": 65534, "runAsGroup": 65534, "fsGroup": 65534,
                           "seccompProfile": {"type": "RuntimeDefault"}},
       "containers": containers, "volumes": volumes}
if pull_name:
    pod["imagePullSecrets"] = [{"name": pull_name}]
job = {"apiVersion": "batch/v1", "kind": "Job",
       "metadata": {"name": name, "namespace": "acceptance-gate", "labels": labels},
       "spec": {"backoffLimit": 0, "activeDeadlineSeconds": deadline, "ttlSecondsAfterFinished": 3600,
                "template": {"metadata": {"labels": labels}, "spec": pod}}}
json.dump(job, open(os.path.join(out, "job.json"), "w"), indent=1)
open(os.path.join(out, "secrets"), "w").write(name + ("\n" + pull_name if pull_name else "") + "\n")
open(os.path.join(out, "deadline"), "w").write("%d\n" % deadline)
PY
  )
}

# ---------------------------------------------------------------- reading the Job
job_state() { # prints running, succeeded or failed
  pk get job "$1" -o json | python3 -c '
import json, sys
s = json.load(sys.stdin).get("status", {})
failed = s.get("failed", 0) or any(c.get("type") == "Failed" and c.get("status") == "True" for c in s.get("conditions", []))
print("succeeded" if s.get("succeeded", 0) else "failed" if failed else "running")'
}

pod_view() { # the containers of the Job's Pod, read from their statuses, as one line:
  #   none                    no Pod yet
  #   ended [<name>:<state>]  the container gate has terminated (the others that have not, with their state)
  #   started                 every container runs or has ended
  #   stuck <why>             a container can never start (its configuration or image name is wrong)
  #   backoff <why>           the node cannot pull an image and is backing off
  #   waiting <why>           a container is still being created; when no container has a status and
  #                           the Pod is not scheduled, <why> ends with its phase and the reason of
  #                           its condition PodScheduled
  pk get pods -l "job-name=$1" -o json | python3 -c '
import json, sys
items = json.load(sys.stdin).get("items", [])
if not items:
    print("none"); sys.exit(0)
pod = items[0]
status = pod.get("status", {})
cs = {c.get("name"): c for c in status.get("containerStatuses", []) or []}
names = [c.get("name") for c in pod.get("spec", {}).get("containers", []) or []] or list(cs)
def state(n):
    return (cs.get(n) or {}).get("state", {}) or {}
def reason(n):
    s = state(n)
    return "running" if "running" in s else (s.get("waiting") or {}).get("reason") or "not created"
def why(n):
    m = " ".join(str((state(n).get("waiting") or {}).get("message", "")).split())[:300]
    return "%s:%s%s" % (n, reason(n), " (%s)" % m if m else "")
if "terminated" in state("gate") or status.get("phase") in ("Succeeded", "Failed"):
    print(" ".join(["ended"] + ["%s:%s" % (n, reason(n)) for n in names if "terminated" not in state(n)])); sys.exit(0)
waiting = [n for n in names if not ("running" in state(n) or "terminated" in state(n))]
if not waiting:
    print("started"); sys.exit(0)
rs = {reason(n) for n in waiting}
kind = ("stuck" if rs & {"CreateContainerConfigError", "CreateContainerError", "InvalidImageName", "ErrImageNeverPull"}
        else "backoff" if rs & {"ErrImagePull", "ImagePullBackOff"} else "waiting")
sched = next((c for c in status.get("conditions", []) or [] if c.get("type") == "PodScheduled"), {})
note = ""
if not cs and sched.get("status") != "True":
    m =" ".join(str(sched.get("message", "")).split())[:300]
    note = " (%s; not scheduled%s%s)" % (status.get("phase") or "no phase", ": " + str(sched["reason"]) if sched.get("reason") else "",
                                         ", " + m if m else "")
print(kind + " " + ",".join(why(n) for n in waiting) + note)'
}

view_filter() { # hides the file blocks from the streamed log
  awk '/^GATE-FILE-BEGIN /{h=1; print "[the Job prints the file " $2 "]"; fflush(); next} /^GATE-FILE-END /{h=0; next} !h {print; fflush()}'
}

parse_log() { # $1 the log of container gate, $2 the folder: files/, outputs, summary.md, errors
  python3 - "$1" "$2" <<'PY'
import base64, hashlib, os, re, sys
log, out = sys.argv[1], sys.argv[2]
os.makedirs(os.path.join(out, "files"), exist_ok=True)
outputs, summary, errors, bad, done = [], [], [], [], False
block = None
for raw in open(log, encoding="utf-8", errors="replace"):
    line = raw.rstrip("\n")
    if block is not None:
        if line == "GATE-FILE-END " + block[0]:
            name, want, parts = block
            block = None
            try:
                data = base64.b64decode("".join(parts), validate=True)
            except ValueError:
                bad.append("%s: not base64" % name); continue
            if hashlib.sha256(data).hexdigest() != want:
                bad.append("%s: the content differs from its SHA-256" % name); continue
            path = os.path.normpath(os.path.join(out, "files", name))
            if not path.startswith(os.path.join(out, "files") + os.sep):
                bad.append("%s: not a path inside the folder" % name); continue
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "wb").write(data)
        else:
            block[2].append(line.strip())
        continue
    m = re.match(r"^GATE-FILE-BEGIN (\S+) ([0-9a-f]{64})$", line)
    if m:
        block = (m.group(1), m.group(2), []); continue
    if line.startswith("GATE-OUTPUT ") and "=" in line:
        outputs.append(line[len("GATE-OUTPUT "):])
    elif line.startswith("GATE-SUMMARY "):
        summary.append(line[len("GATE-SUMMARY "):])
    elif line.startswith("GATE-ERROR "):
        errors.append(line[len("GATE-ERROR "):])
    elif line == "GATE-DONE":
        done = True
if block is not None:
    bad.append("%s: the block has no end" % block[0])
open(os.path.join(out, "outputs"), "w").write("".join(o + "\n" for o in outputs))
open(os.path.join(out, "summary.md"), "w").write("".join(s + "\n" for s in summary))
open(os.path.join(out, "errors"), "w").write("".join(e + "\n" for e in errors))
for b in bad:
    print("gate-parent-job: a file of the Job did not arrive whole: " + b, file=sys.stderr)
if not done:
    print("gate-parent-job: the Job did not finish its work (no GATE-DONE line in its log)", file=sys.stderr)
sys.exit(0 if done and not bad else 1)
PY
}

cleanup_names() { # $1 the folder of the run
  local f="$1/name" name s rc=0
  [ -s "$f" ] || return 0
  name="$(cat "$f")"
  pk delete job "$name" --ignore-not-found --wait=false --cascade=background > /dev/null || rc=1
  if [ -s "$1/secrets" ]; then
    while read -r s; do
      [ -n "$s" ] || continue
      pk delete secret "$s" --ignore-not-found --wait=false > /dev/null || rc=1
    done < "$1/secrets"
  fi
  if [ -e "$1/policy.json" ]; then
    pk delete networkpolicy "$name" --ignore-not-found --wait=false > /dev/null || rc=1
  fi
  [ "$rc" = 0 ] && echo "gate-parent-job: deleted the Job $name, its Secrets and its NetworkPolicy" >&2
  return $rc
}

cmd_cleanup() {
  [ "${1:-}" = --out ] && [ -n "${2:-}" ] || usage
  [ -r "${GATE_PARENT_KUBECONFIG:-}" ] || die "GATE_PARENT_KUBECONFIG is not a readable file"
  cleanup_names "$2"
}

cmd_run() {
  local mode="" jimg="" pimg="" targets=""
  RUN_OUT=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode) mode="$2"; shift 2 ;;
      --out) RUN_OUT="$2"; shift 2 ;;
      --targets) targets="$2"; shift 2 ;;
      --job-image) jimg="$2"; shift 2 ;;
      --probe-image) pimg="$2"; shift 2 ;;
      *) usage ;;
    esac
  done
  case "$mode" in addresses | live | regions) ;; *) echo "gate-parent-job: --mode must be addresses, live or regions" >&2; exit 2 ;; esac
  [ -n "$RUN_OUT" ] || usage
  [[ "$jimg" =~ $DIGEST_RE ]] || { echo "gate-parent-job: --job-image must name a digest: <repository>@sha256:<64 hex>, not '$jimg'" >&2; exit 2; }
  if [ "$mode" = live ] && ! [[ "$pimg" =~ $DIGEST_RE ]]; then
    echo "gate-parent-job: --probe-image must name a digest: <repository>@sha256:<64 hex>, not '$pimg'" >&2; exit 2
  fi
  targets="$(norm_targets "$targets")" || {
    echo "gate-parent-job: --targets must list the IPv4 addresses of the install under test (gate-parent-job.sh targets); the run does not start" >&2; exit 2; }
  resources_json > /dev/null || exit 2
  local v
  for v in GATE_PARENT_KUBECONFIG GATE_KUBECONFIG_A GATE_KUBECONFIG_B; do
    [ -r "${!v:-}" ] || die "$v is not a readable file"
  done
  mkdir -p "$RUN_OUT"
  local name
  name="gate-${GITHUB_RUN_ID:-0}-${GITHUB_RUN_ATTEMPT:-1}-$mode-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
  printf '%s\n' "$name" > "$RUN_OUT/name"
  trap 'rm -f "$RUN_OUT/secret.json" "$RUN_OUT/pull.json"; cleanup_names "$RUN_OUT"' EXIT
  manifests "$mode" "$name" "$jimg" "${pimg:-none}" "$RUN_OUT" || die "the manifests could not be written"
  policy_json "$name" "$targets" > "$RUN_OUT/policy.json"
  pk create -f "$RUN_OUT/policy.json" > /dev/null \
    || die "the NetworkPolicy of the run could not be created: the Role of the gate needs create, get and delete on networkpolicies (scripts/gate-parent-access.yaml)"
  echo "gate-parent-job: created the NetworkPolicy $name: the Pods of the Job may reach TCP 6443 and 443 of $targets and nothing else but DNS" >&2
  pk create -f "$RUN_OUT/secret.json" > /dev/null || die "the Secret of the run could not be created"
  rm -f "$RUN_OUT/secret.json"
  if [ -s "$RUN_OUT/pull.json" ]; then
    pk create -f "$RUN_OUT/pull.json" > /dev/null || die "the pull Secret of the run could not be created"
    rm -f "$RUN_OUT/pull.json"
  fi
  pk create -f "$RUN_OUT/job.json" > /dev/null || die "the Job could not be created"
  local created
  created="$(date +%s)"
  echo "gate-parent-job: created the Job $name in $NS on the parent; $(cat "$RUN_OUT/resources")" >&2
  # 1. Every container starts: the node pulls the images. A container that can never start ends the run at
  #    once; an image the node cannot pull ends it after GATE_PULL_MINUTES of back-off; the container gate
  #    ending first goes straight to its log.
  local start_deadline=$(( created + ${GATE_START_MINUTES:-30} * 60 )) st last="" pull_since="" said=0
  while :; do
    st="$(pod_view "$name")" || die "the Pod of the Job could not be read"
    case "$st" in
      started | ended*) break ;;
      stuck*) die "the Pod of the Job cannot start: ${st#stuck }" ;;
      backoff*)
        [ -n "$pull_since" ] || pull_since="$(date +%s)"
        [ "$(( $(date +%s) - pull_since ))" -ge "$(( ${GATE_PULL_MINUTES:-5} * 60 ))" ] \
          && die "the node could not pull an image of the Job for ${GATE_PULL_MINUTES:-5} minutes: ${st#backoff }" ;;
      *) pull_since="" ;;
    esac
    if [ "$st" != "$last" ] || [ $(( $(date +%s) - said )) -ge 50 ]; then
      echo "gate-parent-job: the Pod of the Job: $st" >&2; said="$(date +%s)"
    fi
    last="$st"
    [ "$(date +%s)" -ge "$start_deadline" ] && die "the Pod of the Job did not start within ${GATE_START_MINUTES:-30} minutes; last: $st"
    sleep "${GATE_POLL_SECONDS:-10}"
  done
  # 2. Stream the log until the Job has ended. A dropped stream is followed again from shortly before. After
  #    each pass the containers are read: once the container gate has ended its log holds everything, so a
  #    probe that hangs or never started does not hold the run. The wait ends at the latest
  #    GATE_FOLLOW_GRACE_SECONDS (default 300) after the Job's own deadline.
  local tok="" state since="" t_end n=0 view follow_end
  follow_end=$(( created + $(cat "$RUN_OUT/deadline") + ${GATE_FOLLOW_GRACE_SECONDS:-300} ))
  if [ "${GITHUB_ACTIONS:-}" = true ]; then tok="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"; echo "::stop-commands::$tok"; fi
  while :; do
    state="$(job_state "$name")" || state=unknown
    [ "$state" = succeeded ] || [ "$state" = failed ] && break
    n=$((n + 1))
    [ "$n" -gt "${GATE_FOLLOW_MAX:-500}" ] && { state="not ended after ${GATE_FOLLOW_MAX:-500} follows of its log"; break; }
    kubectl --kubeconfig "$GATE_PARENT_KUBECONFIG" -n "$NS" logs -f "job/$name" -c gate ${since:+--since=${since}s} 2>/dev/null | view_filter
    view="$(pod_view "$name")" || view="unread"
    case "$view" in
      ended*)
        state="$(job_state "$name")" || state=unknown
        if [ "$state" != succeeded ] && [ "$state" != failed ]; then
          view="${view#ended}"
          state="not ended: its container gate has ended${view:+ (still: ${view# })}"
        fi
        break ;;
      stuck* | backoff*) state="not ended: a container cannot run: ${view#* }"; break ;;
    esac
    [ "$(date +%s)" -ge "$follow_end" ] && { state="not ended $(( (follow_end - created) / 60 )) minutes after it was created"; break; }
    t_end="$(date +%s)"
    sleep "${GATE_POLL_SECONDS:-10}"
    since=$(( $(date +%s) - t_end + 5 ))
  done
  [ -n "$tok" ] && echo "::$tok::"
  case "$state" in
    succeeded | failed) echo "gate-parent-job: the Job ended: $state" >&2 ;;
    *) echo "gate-parent-job: the Job has $state; its log is read now and the Job is deleted" >&2 ;;
  esac
  # 3. The whole log, and the files the Job printed in it.
  pk logs "job/$name" -c gate > "$RUN_OUT/gate.log" || die "the log of the Job could not be read"
  [ "$mode" = live ] && { pk logs "job/$name" -c probe > "$RUN_OUT/probe-container.log" || true; }
  parse_log "$RUN_OUT/gate.log" "$RUN_OUT" || return 1
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && cat "$RUN_OUT/summary.md" >> "$GITHUB_STEP_SUMMARY"
  local e
  while IFS= read -r e; do [ -n "$e" ] && echo "::error::$e"; done < "$RUN_OUT/errors"
  return 0
}

# ---------------------------------------------------------------- in the Job: container gate
out() { echo "GATE-OUTPUT $*"; }
summ() { echo "GATE-SUMMARY $*"; }
emit_file() { # $1 the name in the folder of the run, $2 the file
  [ -f "$2" ] || return 0
  echo "GATE-FILE-BEGIN $1 $(sha256sum < "$2" | cut -c1-64)"
  base64 -w 76 < "$2"
  echo "GATE-FILE-END $1"
}

inner_setup() {
  mkdir -p "$SCRATCH/home" "$SCRATCH/tmp"
  KA="$SECRET_DIR/ka"; KB="$SECRET_DIR/kb"
}

# ---------------------------------------------------------------- in the Job: reading a region
# rk REGION ARGS...: kubectl against region a or b, bounded by GATE_READ_SECONDS (default 20); the output is
# left in RK_OUT, the error in RK_ERR. Returns 0 when it read, 2 when the region's API server answered with
# an error (Error from server: the region can be read), 1 when the region was not reached, and 3 when it has
# not been reached for GATE_UNREAD_MINUTES (default 5) in a row: then it prints a GATE-ERROR line that names
# the region, its API address and the error. The first reading of each region is logged.
declare -A RK_SEEN=() RK_DOWN=()
RK_OUT="" RK_ERR=""
region_addr() { kube_server "$SECRET_DIR/k$1" || echo "address not read"; }
rk() {
  local r="$1" rc now s="${GATE_READ_SECONDS:-20}"
  shift
  RK_OUT="$(timeout "$s" kubectl --kubeconfig "$SECRET_DIR/k$r" --request-timeout="${s}s" "$@" 2> "$SCRATCH/tmp/rk.err")"
  rc=$?
  RK_ERR="$(tr '\n' ' ' < "$SCRATCH/tmp/rk.err" | head -c 400 | sed 's/ *$//')"
  case "$rc" in 124 | 143) RK_ERR="no answer within $s seconds${RK_ERR:+: $RK_ERR}" ;; esac
  now="$(date +%s)"
  if [ "$rc" = 0 ] || [[ "$RK_ERR" == *"Error from server"* ]]; then
    [ -n "${RK_SEEN[$r]:-}" ] || echo "gate: region $r ($(region_addr "$r")) read for the first time at $(date -u +%H:%M:%SZ)"
    RK_SEEN[$r]=1; RK_DOWN[$r]=""
    [ "$rc" = 0 ] && return 0
    return 2
  fi
  [ -n "${RK_DOWN[$r]:-}" ] || RK_DOWN[$r]="$now"
  if [ $(( now - RK_DOWN[$r] )) -ge $(( ${GATE_UNREAD_MINUTES:-5} * 60 )) ]; then
    echo "GATE-ERROR region $r ($(region_addr "$r")) could not be read for ${GATE_UNREAD_MINUTES:-5} minutes: ${RK_ERR:-kubectl exit code $rc}"
    return 3
  fi
  return 1
}

rk_until() { # rk_until REGION WHAT ARGS...: rk until the region answers (rk's code) or gives up (3); says what it waits for at every try
  local r="$1" what="$2" rc
  shift 2
  while :; do
    rk "$r" "$@"; rc=$?
    [ "$rc" = 1 ] || return "$rc"
    echo "gate: waiting to read $what of region $r ($(region_addr "$r")): $RK_ERR"
    sleep "${GATE_WAIT_SECONDS:-15}"
  done
}

inner_addresses() {
  local r list rc=0
  for r in a b; do
    rk_until "$r" "the nodes" get nodes -o json
    case "$?" in
      0) printf '%s\n' "$RK_OUT" > "$SCRATCH/$r.json" ;;
      3) rc=1; break ;;
      *) echo "GATE-ERROR the nodes of region $r ($(region_addr "$r")) could not be read: $RK_ERR"; rc=1 ;;
    esac
  done
  if [ "$rc" = 0 ]; then
    list="$(python3 "$SECRET_DIR/gate-outside-dial.py" addresses "$SCRATCH/a.json" "$SCRATCH/b.json")" || rc=1
    out "list=$list"
  fi
  out "rc=$rc"
}

inner_regions() {
  local r rc=0
  for r in b a; do
    python3 "$SECRET_DIR/standing-gate.py" source --kubeconfig "$SECRET_DIR/k$r" --region "$r" --candidate "$CANDIDATE_COMMIT" || { rc=1; break; }
  done
  if [ "$rc" = 0 ]; then
    for r in b a; do
      python3 "$SECRET_DIR/standing-gate.py" converge --kubeconfig "$SECRET_DIR/k$r" --region "$r" --minutes 60 --interval 30 || { rc=1; break; }
    done
  fi
  out "rc=$rc"
}

kc() { # kcadm.sh inside region A's identity provider; the password never leaves the Pod
  kubectl --kubeconfig "$KA" -n keycloak exec keycloak-0 -c keycloak -- sh -c '
    c=/tmp/kcadm-gate.config; k=/opt/bitnami/keycloak/bin/kcadm.sh
    "$k" config credentials --config "$c" --server http://localhost:8080 --realm master --user admin \
      --password "$(cat /opt/bitnami/keycloak/secrets/admin-password)" >/dev/null || exit 1
    exec "$k" "$@" --config "$c"' sh "$@"
}

mint() { # $1 the e-mail, or empty for the owner session as scripts/acceptance-sovereign.sh mints it
  local issuer signer
  issuer="$(kubectl --kubeconfig "$KA" -n catalyst-system get deploy catalyst-api \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_PIN_ISSUER")].value}')"
  issuer="${issuer:-https://console.$ENV_LABEL.omani.works}"
  signer="$(kubectl --kubeconfig "$KA" -n catalyst-system get deploy catalyst-api \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_HANDOVER_SIGNER_PATH")].value}')"
  signer="${signer:-/var/lib/catalyst/handover-jwt-private.pem}"
  if [ -n "${1:-}" ]; then
    kubectl --kubeconfig "$KA" -n catalyst-system exec deploy/catalyst-api -- cat "$signer" \
      | sovereign-acceptance mint-session --key - --issuer "$issuer" --email "$1" --ttl 1h
  else
    kubectl --kubeconfig "$KA" -n catalyst-system exec deploy/catalyst-api -- cat "$signer" \
      | sovereign-acceptance mint-session --key - --issuer "$issuer"
  fi
}

sso_check() { # gate check FND-G07, as the hosted step does it; the session stays in the Pod's memory
  local d="$SCRATCH/sso" fqdn="$ENV_LABEL.omani.works" user ids id uid g gid rc
  user="gate-$GATE_RUN@$fqdn"
  mkdir -p "$d"
  stop() { echo "GATE-ERROR Sign-in check FND-G07: $1"; summ "Sign-in check FND-G07: not run, $1"; }
  kc get users -r sovereign -q search=gate- --limit 1000 --fields id,username > "$d/users.json" \
    || { stop "the users of the realm sovereign could not be listed"; return; }
  ids="$(python3 -c 'import json, re, sys; p = re.compile(r"^gate-[0-9]+@" + re.escape(sys.argv[2]) + "$", re.I); print(" ".join(u["id"] for u in json.load(open(sys.argv[1])) if p.match(u.get("username", ""))))' "$d/users.json" "$fqdn")" \
    || { stop "the list of users could not be read"; return; }
  for id in $ids; do
    kc delete "users/$id" -r sovereign || { stop "the user $id that an earlier run left could not be deleted"; return; }
  done
  uid="$(kc create users -r sovereign -s "username=$user" -s "email=$user" -s enabled=true -s emailVerified=true -i)" \
    || { stop "the user $user could not be created"; return; }
  [[ "$uid" =~ ^[0-9a-f-]{36}$ ]] || { stop "creating the user $user printed no id"; return; }
  kc get groups -r sovereign -q search=sovereign- --limit 1000 --fields id,name > "$d/groups.json" \
    || { stop "the groups of the realm sovereign could not be listed"; return; }
  for g in sovereign-admins sovereign-viewers; do
    gid="$(python3 -c 'import json, sys; print(next(x["id"] for x in json.load(open(sys.argv[1])) if x.get("name") == sys.argv[2]))' "$d/groups.json" "$g")" \
      || { stop "the group /$g is not in the realm sovereign"; return; }
    kc update "users/$uid/groups/$gid" -r sovereign -s realm=sovereign -s "userId=$uid" -s "groupId=$gid" -n \
      || { stop "the user $user could not join the group /$g"; return; }
  done
  kc get "users/$uid/groups" -r sovereign --fields name > "$d/member.json" \
    || { stop "the groups of the user $user could not be read"; return; }
  python3 -c 'import json, sys; n = {x.get("name") for x in json.load(open(sys.argv[1]))}; sys.exit(0 if {"sovereign-admins", "sovereign-viewers"} <= n else 1)' "$d/member.json" \
    || { stop "the user $user is not in both groups /sovereign-admins and /sovereign-viewers"; return; }
  ( umask 077; mint "$user" > "$SHARED/session" ) || { stop "the session of $user could not be minted"; return; }
  [ -s "$SHARED/session" ] || { stop "the session of $user is empty"; return; }
  touch "$SHARED/probe.go"
  local deadline=$(( $(date +%s) + ${GATE_PROBE_MINUTES:-20} * 60 )) said=0
  while [ ! -e "$SHARED/probe.done" ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { stop "the browser probe did not end within ${GATE_PROBE_MINUTES:-20} minutes"; return; }
    if [ $(( $(date +%s) - said )) -ge 50 ]; then
      echo "gate: waiting for the browser probe of FND-G07 (container probe), at most $(( (deadline - $(date +%s) + 59) / 60 )) minutes more"
      said="$(date +%s)"
    fi
    sleep 5
  done
  rc="$(cat "$SHARED/probe.rc" 2>/dev/null || echo unknown)"
  summ "### Sign-in check FND-G07 as $user (probe exit code $rc)"
  summ ""
  summ '```'
  head -n 300 "$SHARED/probe.log" 2>/dev/null | sed 's/^/GATE-SUMMARY /'
  summ '```'
  [ -f "$SHARED/result.json" ] && cp "$SHARED/result.json" "$d/result.json"
}

sso_delete() { # the user of this run; a user it cannot delete is deleted by the next run
  local d="$SCRATCH/sso" user="gate-$GATE_RUN@$ENV_LABEL.omani.works" ids id
  rm -f "$SHARED/session"
  mkdir -p "$d"
  if ! kc get users -r sovereign -q search=gate- --limit 1000 --fields id,username > "$d/left.json"; then
    summ "Sign-in check FND-G07: the users could not be listed; the next run deletes $user."
    return
  fi
  ids="$(python3 -c 'import json, sys; print(" ".join(u["id"] for u in json.load(open(sys.argv[1])) if u.get("username", "").lower() == sys.argv[2].lower()))' "$d/left.json" "$user")"
  for id in $ids; do
    kc delete "users/$id" -r sovereign || summ "Sign-in check FND-G07: $user could not be deleted; the next run deletes it."
  done
  summ "Sign-in check FND-G07: the user $user is deleted."
}

not_read() { # $1 the region rk gave up on (its GATE-ERROR line is printed): the run ends without the suite
  out "chain=unreachable"
  summ "Hand-over chain: not read; region $1 ($(region_addr "$1")) could not be read for ${GATE_UNREAD_MINUTES:-5} minutes: $RK_ERR. The suite was not run."
}

live_wait() { # the creation time, then the wait for the hand-over chain; 1 when the suite must not run
  local r t rc born="" chain="" last="nothing was read" complete failed current deadline mins="${GATE_CHAIN_MINUTES:-180}"
  # When the environment was created: the earliest creation of kube-system in the two regions.
  for r in a b; do
    rk_until "$r" "the creation time of kube-system" get namespace kube-system -o jsonpath='{.metadata.creationTimestamp}'
    rc=$?
    [ "$rc" = 3 ] && { not_read "$r"; return 1; }
    t="$RK_OUT"; [ "$rc" = 0 ] || t=""
    if [[ ! "$t" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then born=""; break; fi
    if [ -z "$born" ] || [[ "$t" < "$born" ]]; then born="$t"; fi
  done
  out "born=$born"
  summ "Environment created at: ${born:-not read}"
  # Wait until the hand-over chain has ended (completed, or failed at a step), at most GATE_CHAIN_MINUTES (180).
  # Every pass reads both regions and prints one line: a pass takes at most two readings and the pause.
  deadline=$(( $(date +%s) + mins * 60 ))
  while :; do
    rk a -n catalyst get configmap self-sovereign-cutover-status -o jsonpath='{.data.cutoverComplete}|{.data.failedStep}|{.data.currentStep}'
    case "$?" in
      0) IFS='|' read -r complete failed current <<< "$RK_OUT"
         if [ "$complete" = true ]; then chain="completed"; break; fi
         if [ -n "$failed" ]; then chain="failed at step $failed"; break; fi
         last="not ended, current step: ${current:-none}" ;;
      3) not_read a; return 1 ;;
      *) last="the status ConfigMap could not be read: $RK_ERR" ;;
    esac
    rk b get namespace kube-system -o name
    [ "$?" = 3 ] && { not_read b; return 1; }
    echo "gate: waiting for the hand-over chain, read in region a ($(region_addr a)): $last; region b ($(region_addr b)) $([ -z "${RK_DOWN[b]:-}" ] && echo answers || echo "not read for $(( $(date +%s) - RK_DOWN[b] )) seconds: $RK_ERR"); $(( (deadline - $(date +%s) + 59) / 60 )) minutes left"
    if [ "$(date +%s)" -ge "$deadline" ]; then
      summ "Hand-over chain: did not end within $mins minutes; last observed: $last"
      out "chain=timeout"
      echo "GATE-ERROR The hand-over chain did not end within $mins minutes; last observed: $last. The suite was not run."
      return 1
    fi
    sleep "${GATE_WAIT_SECONDS:-15}"
  done
  out "chain=$chain"
  summ "Hand-over chain: $chain"
  if [ "$chain" != completed ]; then
    rk a -n catalyst get configmap self-sovereign-cutover-status -o jsonpath='{.data.lastError}'
    summ "Hand-over error: ${RK_OUT:-$RK_ERR}"
  fi
  return 0
}

inner_live() {
  local rc f
  trap '[ -e "$SHARED/probe.go" ] || touch "$SHARED/probe.skip"' EXIT
  live_wait || return 0
  # The suite, as scripts/acceptance-sovereign.sh runs it, from the files of the commit under test.
  export ACCEPT_KUBECONFIG_A="$KA" ACCEPT_KUBECONFIG_B="$KB" ACCEPT_MANIFEST=/opt/conformance/gates.json \
    ACCEPT_RUN_IN=cloud ACCEPT_REPO_ROOT=/opt/candidate ACCEPT_CONSOLE="${ACCEPT_CONSOLE:-https://console.$ENV_LABEL.omani.works}"
  mkdir -p "$SCRATCH/record"
  CATALYST_SESSION="$(cat "$SECRET_DIR/session" 2>/dev/null || true)"
  if [ -z "$CATALYST_SESSION" ] && [ "${ACCEPT_MINT:-0}" = 1 ]; then CATALYST_SESSION="$(mint "")"; fi
  export CATALYST_SESSION
  if [ -z "$CATALYST_SESSION" ]; then
    echo "the session is empty: no owner session was given and none could be minted" | tee "$SCRATCH/acceptance.log"
    rc=3
  else
    (cd /opt/candidate && sovereign-acceptance run --env "$ENV_LABEL" --record --record-dir "$SCRATCH/record") | tee "$SCRATCH/acceptance.log"
    rc=${PIPESTATUS[0]}
  fi
  unset CATALYST_SESSION
  out "suite_rc=$rc"
  # Gate check FND-G07, unless the run was NOT MEASURED.
  if [ "$rc" != 4 ]; then
    sso_check
    sso_delete
  fi
  emit_file acceptance.log "$SCRATCH/acceptance.log"
  for f in "$SCRATCH"/record/*.json; do
    [ -f "$f" ] && emit_file "record/$(basename "$f")" "$f"
  done
  emit_file sso/result.json "$SCRATCH/sso/result.json"
}

cmd_inner() {
  case "${1:-}" in addresses | live | regions) ;; *) usage ;; esac
  inner_setup
  "inner_$1"
  echo "GATE-DONE"
}

# ---------------------------------------------------------------- in the Job: container probe
cmd_probe() {
  local deadline=$(( $(date +%s) + ${GATE_PROBE_WAIT_MINUTES:-330} * 60 )) d="$SCRATCH/probe" rc said=0
  mkdir -p "$SCRATCH/home" "$SCRATCH/tmp"
  while [ ! -e "$SHARED/probe.go" ] && [ ! -e "$SHARED/probe.skip" ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { echo "no sign of the container gate; the probe did not run"; exit 0; }
    if [ $(( $(date +%s) - said )) -ge 50 ]; then
      echo "probe: waiting for the container gate to start the browser probe, at most $(( (deadline - $(date +%s) + 59) / 60 )) minutes more"
      said="$(date +%s)"
    fi
    sleep 5
  done
  [ -e "$SHARED/probe.go" ] || { echo "the container gate skipped the probe"; exit 0; }
  mkdir -p "$d"
  cp "$SECRET_DIR/sso-zero-click-probe.mjs" "$d/probe.mjs"
  ln -sfn /opt/probe/node_modules "$d/node_modules"
  (cd "$d" && node "$d/probe.mjs" --fqdn "$ENV_LABEL.omani.works" --session-file "$SHARED/session" \
    --result-file "$SHARED/result.json") > "$SHARED/probe.log" 2>&1
  rc=$?
  echo "$rc" > "$SHARED/probe.rc"
  touch "$SHARED/probe.done"
  cat "$SHARED/probe.log"
  echo "probe exit code $rc"
}

# ---------------------------------------------------------------- self-test
self_test() {
  local T fails=0 n=0 o
  T="$(mktemp -d)"
  trap 'rm -rf "$T"' RETURN
  # The cases state the run's environment themselves: a GitHub runner sets GITHUB_ACTIONS, the
  # step summary and the attempt of a re-run (2 and more), and a job may set GATE_*, ACCEPT_* or
  # a session. None of them may change what a case sees, so every variable the script or the
  # stand-in reads is cleared here, and the attempt is 1 unless a case sets it.
  local v
  for v in $(compgen -e); do
    case "$v" in
      GITHUB_* | GATE_* | ACCEPT_* | STUB_* | ENV_LABEL | CANDIDATE_COMMIT | CATALYST_SESSION) unset "$v" ;;
    esac
  done
  export GITHUB_RUN_ATTEMPT=1
  ok() { n=$((n + 1)); if [ "$1" = 0 ]; then echo "ok   $2"; else echo "FAIL $2"; fails=$((fails + 1)); fi; }
  # A stand-in kubectl, the only one the cases reach (first on PATH; the parent kubeconfig is a
  # file of the test). STUB_FAIL_TIMES lost connections first, then STUB_FINAL (ok, forbidden,
  # exists). It writes every call into calls, the Job state from job.state, the log from log.txt;
  # the Pod's container gate is in the state STUB_GATE, the container probe (when STUB_PROBE is
  # set) in the state STUB_PROBE, a JSON object; STUB_HANG: following the log never ends the Job;
  # STUB_UNSCHEDULED: the Pod is Pending, not scheduled, with that message and no container status.
  # The regions (kubeconfigs $T/sec/ka and kb): `config view` prints the server of the file; STUB_DOWN
  # (a or b) cannot be reached; STUB_CHAIN is the status ConfigMap (down: not reached, missing: NotFound).
  # A stand-in getent answers the names of the file dns ("<name> <address>...").
  mkdir -p "$T/bin"
  cat > "$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
S="$STUB_DIR"
k=""; p=""; for x in "$@"; do [ "$p" = --kubeconfig ] && k="$x"; p="$x"; done
if [ "${1:-}" = config ]; then sed -n 's/^ *server: *//p' "$k" | head -n 1; exit 0; fi
echo "$*" >> "$S/calls"
c=$(cat "$S/count" 2>/dev/null || echo 0); c=$((c + 1)); echo "$c" > "$S/count"
if [ "$c" -le "${STUB_FAIL_TIMES:-0}" ]; then echo 'Unable to connect to the server: http2: client connection lost' >&2; exit 1; fi
case "${STUB_FINAL:-ok}" in
  forbidden) echo 'Error from server (Forbidden): jobs.batch is forbidden' >&2; exit 1 ;;
  exists) echo 'Error from server (AlreadyExists): jobs.batch "x" already exists' >&2; exit 1 ;;
esac
if [ -n "${STUB_DOWN:-}" ] && [ "$k" = "$S/sec/k$STUB_DOWN" ]; then
  echo "Unable to connect to the server: dial tcp $(sed -n 's#^ *server: *https://##p' "$k"): i/o timeout" >&2; exit 1
fi
a=" $* "
case "$a" in
  *" get namespace kube-system -o jsonpath="*) echo 2026-11-19T20:00:00Z ;;
  *" get namespace kube-system -o name "*) echo namespace/kube-system ;;
  *" get configmap self-sovereign-cutover-status "*)
    case "${STUB_CHAIN:-true||}" in
      down) echo 'Unable to connect to the server: dial tcp 203.0.113.1:6443: i/o timeout' >&2; exit 1 ;;
      missing) echo 'Error from server (NotFound): configmaps "self-sovereign-cutover-status" not found' >&2; exit 1 ;;
      *) printf '%s' "${STUB_CHAIN:-true||}" ;;
    esac ;;
  *" get nodes "*) echo '{"items": []}' ;;
  *" create -f "*) f="${a#* create -f }"; f="${f%% *}"; cp "$f" "$S/created-$(basename "$f")"; echo created ;;
  *" get job "*) printf '{"status": {%s}}\n' "$(cat "$S/job.state" 2>/dev/null)" ;;
  *" get pods "*) p=""; [ -z "${STUB_PROBE:-}" ] || p=', {"name": "probe", "state": '"$STUB_PROBE"'}'
    if [ -n "${STUB_UNSCHEDULED:-}" ]; then
      echo '{"items": [{"spec": {"containers": [{"name": "gate"}, {"name": "probe"}]}, "status": {"phase": "Pending", "conditions": [{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "'"$STUB_UNSCHEDULED"'"}]}}]}'
    else
      echo '{"items": [{"status": {"phase": "Running", "containerStatuses": [{"name": "gate", "state": {"'"${STUB_GATE:-running}"'": {}}}'"$p"']}}]}'
    fi ;;
  *" logs -f "*) cat "$S/log.txt"; [ -n "${STUB_HANG:-}" ] || echo '"succeeded": 1' > "$S/job.state" ;;
  *" logs "*) cat "$S/log.txt" ;;
  *" delete "*) echo deleted ;;
  *) echo "stub: $*" >&2; exit 3 ;;
esac
STUB
  cat > "$T/bin/getent" <<'STUB'
#!/usr/bin/env bash
[ "$1" = ahostsv4 ] || exit 2
v="$(awk -v n="$2" '$1 == n {for (i = 2; i <= NF; i++) print $i}' "$STUB_DIR/dns" 2>/dev/null)"
[ -n "$v" ] || exit 2
for x in $v; do printf '%s STREAM %s\n%s DGRAM\n%s RAW\n' "$x" "$2" "$x" "$x"; done
STUB
  chmod +x "$T/bin/kubectl" "$T/bin/getent"
  reset_stub() { rm -f "$T/calls" "$T/count" "$T/created-"* "$T/job.state"; : > "$T/calls"; }
  export STUB_DIR="$T" GATE_PARENT_BACKOFF=0 GATE_POLL_SECONDS=0 GATE_PARENT_TRIES=6
  export PATH="$T/bin:$PATH" GATE_PARENT_KUBECONFIG="$T/parent.yaml"
  echo "parent-credential-xyz" > "$T/parent.yaml"
  calls() { wc -l < "$T/calls" | tr -d ' '; }
  calls_of() { grep -c -F -e "$1" "$T/calls"; } # how many calls hold $1
  [ "$(command -v kubectl)" = "$T/bin/kubectl" ]; ok $? "the cases reach only the stand-in kubectl"

  # execution
  [ "$(cmd_execution '')" = hosted ]; ok $? "execution: empty means hosted"
  [ "$(cmd_execution hosted)" = hosted ]; ok $? "execution: hosted"
  [ "$(cmd_execution parent)" = parent ]; ok $? "execution: parent"
  ( cmd_execution runner-label ) 2>/dev/null; [ $? = 2 ]; ok $? "execution: another value is refused with exit code 2"

  # the bounded retry
  reset_stub; STUB_FAIL_TIMES=2 STUB_FINAL=ok pk get job x >/dev/null 2>&1; [ $? = 0 ] && [ "$(calls)" = 3 ]
  ok $? "retry: two lost connections, then the answer: 3 calls, done"
  reset_stub; STUB_FAIL_TIMES=99 pk get job x >/dev/null 2>&1; [ $? = 1 ] && [ "$(calls)" = 6 ]
  ok $? "retry: the bound: 6 calls, then failed"
  reset_stub; STUB_FAIL_TIMES=0 STUB_FINAL=forbidden pk get job x >/dev/null 2>&1; [ $? = 1 ] && [ "$(calls)" = 1 ]
  ok $? "retry: a refusal is not tried again"
  reset_stub; STUB_FAIL_TIMES=1 STUB_FINAL=exists pk create -f "$T/parent.yaml" >/dev/null 2>&1; [ $? = 0 ] && [ "$(calls)" = 2 ]
  ok $? "retry: AlreadyExists after a lost connection is the create of the attempt before"
  reset_stub; STUB_FAIL_TIMES=0 STUB_FINAL=exists pk create -f "$T/parent.yaml" >/dev/null 2>&1; [ $? = 1 ]
  ok $? "retry: AlreadyExists at the first attempt is a conflict"

  # the input preflight, with or without the prefix the flight checklist prints
  [ "$(cmd_preflight 'PREFLIGHT=2026-11-19T20:41:00Z;servers=3/20/17,volumes=12/50/38')" = '2026-11-19T20:41:00Z;servers=3/20/17,volumes=12/50/38' ]
  ok $? "preflight: the prefix PREFLIGHT= is taken off"
  [ "$(cmd_preflight '2026-11-19T20:41:00Z;servers=3/20/17,volumes=12/50/38')" = '2026-11-19T20:41:00Z;servers=3/20/17,volumes=12/50/38' ] && [ -z "$(cmd_preflight '')" ]
  ok $? "preflight: a value without the prefix and an empty one stay as they are"

  # the addresses of the install: the API servers of the kubeconfigs and the gateway hosts in the DNS, nothing dialed
  mkdir -p "$T/sec" "$T/scr" "$T/sh"
  printf 'apiVersion: v1\nclusters:\n- cluster:\n    server: https://203.0.113.1:6443\n  name: a\n' > "$T/sec/ka"
  printf 'apiVersion: v1\nclusters:\n- cluster:\n    server: https://203.0.113.24:6443\n  name: b\n' > "$T/sec/kb"
  sed 's#https://203.0.113.24:6443#https://api-b.example.org:6443#' "$T/sec/kb" > "$T/kb-name"
  printf '%s\n' 'console.hw399.omani.works 203.0.113.35' 'auth.hw399.omani.works 203.0.113.35' \
    'marketplace.hw399.omani.works 203.0.113.36' 'api-b.example.org 203.0.113.25' 'console.hw400.omani.works' > "$T/dns"
  [ "$(GATE_KUBECONFIG_A="$T/sec/ka" GATE_KUBECONFIG_B="$T/sec/kb" cmd_targets --env hw399 2>/dev/null)" = 203.0.113.1,203.0.113.24,203.0.113.35,203.0.113.36 ]
  ok $? "targets: the API servers of both kubeconfigs and the gateway addresses in the DNS, sorted, each once"
  [ "$(GATE_KUBECONFIG_A="$T/sec/ka" GATE_KUBECONFIG_B="$T/kb-name" cmd_targets --env hw399 2>/dev/null)" = 203.0.113.1,203.0.113.25,203.0.113.35,203.0.113.36 ]
  ok $? "targets: an API server named by a host name is resolved"
  ( GATE_KUBECONFIG_A="$T/sec/ka" GATE_KUBECONFIG_B="$T/sec/kb" cmd_targets --env hw400 ) > "$T/t3.out" 2>&1
  [ $? = 1 ] && grep -q 'console.hw400.omani.works has no IPv4 address' "$T/t3.out"
  ok $? "targets: a console the DNS does not resolve stops before any Job"

  # the run's NetworkPolicy: the addresses in, one exact /32 each out, TCP 6443 and 443 only, the Pods of the run's Job only
  cmd_policy gate-9-1-live-abcdef ' 203.0.113.35, 203.0.113.1,203.0.113.1' > "$T/pol.json"
  jq -e '.kind == "NetworkPolicy" and .metadata.namespace == "acceptance-gate" and .metadata.name == "gate-9-1-live-abcdef"
    and .spec.podSelector == {"matchLabels": {"batch.kubernetes.io/job-name": "gate-9-1-live-abcdef"}} and .spec.policyTypes == ["Egress"]
    and .spec.egress == [{"to": [{"ipBlock": {"cidr": "203.0.113.1/32"}}, {"ipBlock": {"cidr": "203.0.113.35/32"}}],
                          "ports": [{"protocol": "TCP", "port": 6443}, {"protocol": "TCP", "port": 443}]}]' "$T/pol.json" > /dev/null
  ok $? "policy: each address once as an exact /32, TCP 6443 and 443 only, for the Pods of the run's Job only"
  local bad_t rc_t=0
  for bad_t in '' ' , ' '203.0.113.0/24' '0.0.0.0' '203.0.113.256' '127.0.0.1' '224.0.0.1' 'console.hw399.omani.works' '203.0.113.1,*'; do
    ( cmd_policy gate-9-1-live-abcdef "$bad_t" ) > /dev/null 2>&1; [ $? = 2 ] || { echo "not refused: '$bad_t'"; rc_t=1; }
  done
  ok "$rc_t" "policy: no address, a range, an address that is no host's or a name: exit code 2"

  # the waits of the Job: bounded, one line per pass, and a region that cannot be read ends the run with its address and error
  live() { ( export STUB_DOWN="$1" STUB_CHAIN="$2" GATE_UNREAD_MINUTES=0 GATE_CHAIN_MINUTES=0
             SECRET_DIR="$T/sec" SCRATCH="$T/scr" SHARED="$T/sh" GATE_WAIT_SECONDS=0 GATE_READ_SECONDS=5
             inner_setup; live_wait; echo "live_wait=$?" ) 2>&1; }
  live b 'true||' > "$T/w1.out"
  grep -q '^gate: region a (203.0.113.1) read for the first time at ' "$T/w1.out" \
    && grep -qx 'GATE-ERROR region b (203.0.113.24) could not be read for 0 minutes: Unable to connect to the server: dial tcp 203.0.113.24:6443: i/o timeout' "$T/w1.out" \
    && grep -qx 'GATE-OUTPUT chain=unreachable' "$T/w1.out" && grep -qx 'live_wait=1' "$T/w1.out" && ! grep -q 'born=' "$T/w1.out"
  ok $? "wait: a region not read ends the run at once with its name, its address and the error; the first reading is logged"
  live '' down > "$T/w2.out"
  grep -qx 'GATE-OUTPUT born=2026-11-19T20:00:00Z' "$T/w2.out" && grep -qx 'GATE-OUTPUT chain=unreachable' "$T/w2.out" \
    && grep -qx 'GATE-ERROR region a (203.0.113.1) could not be read for 0 minutes: Unable to connect to the server: dial tcp 203.0.113.1:6443: i/o timeout' "$T/w2.out" \
    && grep -qx 'live_wait=1' "$T/w2.out" && ! grep -q 'chain=timeout' "$T/w2.out"
  ok $? "wait: region a not read during the wait for the hand-over chain ends the wait at once, not after 180 minutes"
  live '' missing > "$T/w3.out"
  grep -q '^gate: waiting for the hand-over chain, read in region a (203.0.113.1): the status ConfigMap could not be read: Error from server (NotFound): .*; region b (203.0.113.24) answers; 0 minutes left$' "$T/w3.out" \
    && grep -qx 'GATE-OUTPUT chain=timeout' "$T/w3.out" && ! grep -q 'unreachable' "$T/w3.out"
  ok $? "wait: a region whose server answers with an error is read, not unreachable; the wait ends at its bound"
  live '' '||5' > "$T/w4.out"
  [ "$(grep -c '^gate: waiting for the hand-over chain, read in region a (203.0.113.1): not ended, current step: 5; region b (203.0.113.24) answers; ' "$T/w4.out")" = 1 ] \
    && grep -q '^gate: region b (203.0.113.24) read for the first time at ' "$T/w4.out" && grep -qx 'GATE-OUTPUT chain=timeout' "$T/w4.out"
  ok $? "wait: every pass prints what it waits for, with both regions"
  live '' 'true||' > "$T/w5.out"
  grep -qx 'GATE-OUTPUT chain=completed' "$T/w5.out" && grep -qx 'live_wait=0' "$T/w5.out"
  ok $? "wait: a completed hand-over chain lets the suite run"
  reset_stub
  ( export STUB_FAIL_TIMES=2 GATE_UNREAD_MINUTES=5; SECRET_DIR="$T/sec" SCRATCH="$T/scr" SHARED="$T/sh" GATE_WAIT_SECONDS=0 GATE_READ_SECONDS=5
    inner_setup; inner_addresses ) > "$T/w6.out" 2>&1
  [ "$(grep -c '^gate: waiting to read the nodes of region a (203.0.113.1): Unable to connect to the server: http2: client connection lost$' "$T/w6.out")" = 2 ] \
    && grep -q '^gate: region a (203.0.113.1) read for the first time' "$T/w6.out" && grep -q '^gate: region b (203.0.113.24) read for the first time' "$T/w6.out"
  ok $? "wait: a region not reached yet is read again within its bound, saying so at every try"
  reset_stub
  ( export STUB_DOWN=a GATE_UNREAD_MINUTES=0; SECRET_DIR="$T/sec" SCRATCH="$T/scr" SHARED="$T/sh" GATE_WAIT_SECONDS=0 GATE_READ_SECONDS=5
    inner_setup; inner_addresses ) > "$T/w7.out" 2>&1
  grep -qx 'GATE-ERROR region a (203.0.113.1) could not be read for 0 minutes: Unable to connect to the server: dial tcp 203.0.113.1:6443: i/o timeout' "$T/w7.out" \
    && grep -qx 'GATE-OUTPUT rc=1' "$T/w7.out" && ! grep -q 'sec/kb' "$T/calls"
  ok $? "wait: the addresses end at once when a region cannot be read, naming it, without reading the other"

  # the manifests
  mkdir -p "$T/tools/scripts" "$T/m"
  for f in gate-outside-dial.py standing-gate.py sso-zero-click-probe.mjs; do echo "tool $f" > "$T/tools/scripts/$f"; done
  echo "region-a-credential-abc" > "$T/ka"; echo "region-b-credential-def" > "$T/kb"; echo "owner-session-ghi" > "$T/session"
  local JI PI TG=203.0.113.1,203.0.113.24
  JI="ghcr.io/openova-io/openova/conformance@sha256:$(printf 'a%.0s' {1..64})"
  PI="ghcr.io/openova-io/openova/conformance@sha256:$(printf 'b%.0s' {1..64})"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GATE_SESSION_FILE="$T/session" GATE_TOOLS="$T/tools" \
      GATE_PULL_USER=bot GATE_PULL_TOKEN=pull-token-jkl ENV_LABEL=hw307 GATE_RUN=42 ACCEPT_API_PORT_OUTSIDE=1.2.3.4=timeout \
      ACCEPT_PEER_PORTS_OUTSIDE=1.2.3.4:2379=timeout,1.2.3.4:12379=timeout \
      ACCEPT_MANIFEST=/runner/gates.json CATALYST_SESSION=env-session-mno
    TOOLS="$T/tools" manifests live gate-42-1-live-abc "$JI" "$PI" "$T/m" )
  ok $? "manifests: written"
  python3 - "$T/m/job.json" <<'PY'
import json, sys
j = json.load(open(sys.argv[1])); p = j["spec"]["template"]["spec"]; bad = []
if j["metadata"]["namespace"] != "acceptance-gate": bad.append("namespace")
if p.get("automountServiceAccountToken") is not False: bad.append("token mounted")
sc = p["securityContext"]
if not (sc.get("runAsNonRoot") and sc.get("seccompProfile", {}).get("type") == "RuntimeDefault"): bad.append("pod security")
for c in p["containers"]:
    s = c["securityContext"]
    if s.get("allowPrivilegeEscalation") is not False or s.get("capabilities") != {"drop": ["ALL"]}: bad.append(c["name"] + " security")
    for k in ("requests", "limits"):
        if set(c["resources"].get(k, {})) != {"cpu", "memory"}: bad.append(c["name"] + " " + k)
    if "@sha256:" not in c["image"]: bad.append(c["name"] + " image by tag")
    names = {e["name"] for e in c["env"]}
    if "CATALYST_SESSION" in names or "ACCEPT_MANIFEST" in names: bad.append(c["name"] + " env")
    if not {"ACCEPT_API_PORT_OUTSIDE", "ACCEPT_PEER_PORTS_OUTSIDE", "ENV_LABEL"} <= names: bad.append(c["name"] + " inputs")
if [c["name"] for c in p["containers"]] != ["gate", "probe"]: bad.append("containers")
if j["spec"]["backoffLimit"] != 0 or not j["spec"].get("activeDeadlineSeconds"): bad.append("job spec")
if p.get("imagePullSecrets") != [{"name": "gate-42-1-live-abc-pull"}]: bad.append("pull secret")
if any(k in p for k in ("hostNetwork", "hostPID", "hostIPC")): bad.append("host namespaces")
print(bad) if bad else None
sys.exit(1 if bad else 0)
PY
  ok $? "manifests: the Job is at Pod Security restricted, with requests and limits, images by digest, no token, the inputs"
  ! grep -q -e credential -e session -e pull-token "$T/m/job.json"; ok $? "manifests: no kubeconfig, session or pull credential in the Job object"
  python3 -c 'import base64, json, sys; d = json.load(open(sys.argv[1]))["data"]; sys.exit(0 if base64.b64decode(d["ka"]).startswith(b"region-a-credential") and base64.b64decode(d["session"]) == b"owner-session-ghi" and "gate-parent-job.sh" in d and "sso-zero-click-probe.mjs" in d else 1)' "$T/m/secret.json"
  ok $? "manifests: the Secret of the run holds the kubeconfigs, the session and the scripts"
  python3 -c 'import json, sys; s = json.load(open(sys.argv[1])); sys.exit(0 if s["type"] == "kubernetes.io/dockerconfigjson" else 1)' "$T/m/pull.json"
  ok $? "manifests: the pull Secret is of type dockerconfigjson"
  mkdir -p "$T/m2"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb"; TOOLS="$T/tools" manifests addresses gate-1-1-addresses-x "$JI" none "$T/m2" )
  python3 -c 'import json, sys; p = json.load(open(sys.argv[1]))["spec"]["template"]["spec"]; sys.exit(0 if [c["name"] for c in p["containers"]] == ["gate"] and "imagePullSecrets" not in p else 1)' "$T/m2/job.json"
  ok $? "manifests: addresses runs the container gate only"

  # the resources: requests the parent can schedule, limits as the ceiling, GATE_RESOURCES over the defaults
  python3 - "$T/m/job.json" <<'PY'
import json, re, sys
def q(v):  # millicores or bytes
    m = re.fullmatch(r"([0-9]+)m", v)
    if m: return int(m.group(1))
    m = re.fullmatch(r"([0-9]+)(Ki|Mi|Gi)", v)
    return int(m.group(1)) << {"Ki": 10, "Mi": 20, "Gi": 30}[m.group(2)] if m else int(float(v) * 1000)
cs = json.load(open(sys.argv[1]))["spec"]["template"]["spec"]["containers"]
req = lambda r: sum(q(c["resources"]["requests"][r]) for c in cs)
bad = []
if req("cpu") > 500: bad.append("the Pod requests %dm CPU" % req("cpu"))
if req("memory") > 1 << 30: bad.append("the Pod requests %dMi" % (req("memory") >> 20))
for c in cs:
    for r in ("cpu", "memory"):
        if q(c["resources"]["requests"][r]) > q(c["resources"]["limits"][r]): bad.append(c["name"] + " request above limit")
lim = {c["name"]: c["resources"]["limits"] for c in cs}
if lim != {"gate": {"cpu": "1", "memory": "1Gi"}, "probe": {"cpu": "1", "memory": "2Gi"}}: bad.append("limits %s" % lim)
print(bad) if bad else None
sys.exit(1 if bad else 0)
PY
  ok $? "resources: the live Pod requests at most 500m CPU and 1Gi in all, the limits stay 1 CPU 1Gi and 1 CPU 2Gi"
  grep -qx 'its Pod requests 350m CPU and 384Mi (gate 250m CPU 256Mi, probe 100m CPU 128Mi); limits 2000m CPU and 3072Mi (gate 1 CPU 1Gi, probe 1 CPU 2Gi)' "$T/m/resources"
  ok $? "resources: the line the run prints names what the Pod requests and its limits"
  mkdir -p "$T/m3"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GATE_RESOURCES='probe.requests.memory=1Gi, gate.limits.cpu=2500m'
    TOOLS="$T/tools" manifests live gate-2-1-live-x "$JI" "$PI" "$T/m3" )
  python3 -c '
import json, sys
c = {x["name"]: x["resources"] for x in json.load(open(sys.argv[1]))["spec"]["template"]["spec"]["containers"]}
sys.exit(0 if c == {"gate": {"requests": {"cpu": "250m", "memory": "256Mi"}, "limits": {"cpu": "2500m", "memory": "1Gi"}},
                    "probe": {"requests": {"cpu": "100m", "memory": "1Gi"}, "limits": {"cpu": "1", "memory": "2Gi"}}} else 1)' "$T/m3/job.json"
  ok $? "resources: GATE_RESOURCES raises the values it names, the others keep their defaults"
  local bad_res rc_all=0
  for bad_res in 'gate.requests.memory=lots' 'sidecar.requests.cpu=1' 'gate.request.cpu=1' 'gate.requests.cpu' 'gate.requests.cpu=0m' \
      'gate.requests.memory=512M' 'gate.requests.cpu=512Mi' 'probe.requests.memory=4Gi' 'gate.limits.cpu=100m' 'gate.requests.cpu=1,gate.requests.cpu=2'; do
    ( GATE_RESOURCES="$bad_res" resources_json ) > /dev/null 2>&1; [ $? = 2 ] || { echo "not refused: $bad_res"; rc_all=1; }
  done
  ok "$rc_all" "resources: an unknown key, a key named twice, a quantity of the wrong form or 0, a request above its limit: exit code 2"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=55 GATE_RESOURCES='probe.requests.memory=3Gi'
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r13" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r13.out" 2>&1
  o=$?
  [ "$o" = 2 ] && [ "$(calls)" = 0 ] && grep -q 'probe.requests.memory=3Gi is above its limit 2Gi' "$T/r13.out"
  ok $? "run: GATE_RESOURCES that is not valid ends the run with exit code 2 before anything is created on the parent"

  # images by digest only
  ( GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" cmd_run --mode live --out "$T/r0" --targets "$TG" --job-image ghcr.io/x/y:latest --probe-image "$PI" ) 2>/dev/null
  [ $? = 2 ]; ok $? "run: a Job image named by a tag is refused"
  ( GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" cmd_run --mode live --out "$T/r0" --targets "$TG" --job-image "$JI" ) 2>/dev/null
  [ $? = 2 ]; ok $? "run: live without a probe image is refused"
  ( cmd_run --mode sweep --out "$T/r0" --targets "$TG" --job-image "$JI" ) 2>/dev/null; [ $? = 2 ]; ok $? "run: an unknown mode is refused"

  # the files of the Job: emitted, streamed, parsed
  printf 'line one\n\x00\x01 binary\n' > "$T/payload"
  { echo "suite output"; echo "::warning::from the cluster"; emit_file record/hw307-1.json "$T/payload"; out "suite_rc=0"
    summ "Hand-over chain: completed"; echo "GATE-ERROR a message"; echo GATE-DONE; } > "$T/log.txt"
  parse_log "$T/log.txt" "$T/p1" 2>/dev/null && cmp -s "$T/payload" "$T/p1/files/record/hw307-1.json" \
    && grep -qx 'suite_rc=0' "$T/p1/outputs" && grep -qx 'Hand-over chain: completed' "$T/p1/summary.md" && grep -qx 'a message' "$T/p1/errors"
  ok $? "files: a file the Job prints arrives whole, with the outputs, the summary and the errors"
  view_filter < "$T/log.txt" | grep -q -e 'bGluZSBvbmUK' -e 'GATE-FILE'; [ $? = 1 ]; ok $? "files: the streamed log hides the file blocks"
  sed 's/^bGluZSBvbmUK/bGluZSB0d28K/' "$T/log.txt" > "$T/bad.txt"
  parse_log "$T/bad.txt" "$T/p2" 2>/dev/null; [ $? = 1 ]; ok $? "files: a changed file fails on its SHA-256"
  grep -v '^GATE-DONE$' "$T/log.txt" > "$T/nodone.txt"
  parse_log "$T/nodone.txt" "$T/p3" 2>/dev/null; [ $? = 1 ]; ok $? "files: a log without GATE-DONE fails"
  grep -v '^GATE-FILE-END' "$T/log.txt" > "$T/noend.txt"
  parse_log "$T/noend.txt" "$T/p4" 2>/dev/null; [ $? = 1 ]; ok $? "files: a block without its end fails"
  { echo "GATE-FILE-BEGIN ../escape $(sha256sum < "$T/payload" | cut -c1-64)"; base64 -w 76 < "$T/payload"; echo "GATE-FILE-END ../escape"; echo GATE-DONE; } > "$T/esc.txt"
  parse_log "$T/esc.txt" "$T/p5" 2>/dev/null; [ $? = 1 ] && [ ! -e "$T/escape" ]; ok $? "files: a path outside the folder is refused"

  # a whole run against the stand-in parent: created, streamed, read, deleted
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GATE_TOOLS="$T/tools" GITHUB_RUN_ID=42 ENV_LABEL=hw307
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r1" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r1.out" 2>&1
  o=$?
  [ "$o" = 0 ] && cmp -s "$T/payload" "$T/r1/files/record/hw307-1.json" && [ -e "$T/created-job.json" ] && [ -e "$T/created-secret.json" ] \
    && grep -q "delete job gate-42-1-live-" "$T/calls" && grep -q "delete secret gate-42-1-live-" "$T/calls" && [ ! -e "$T/r1/secret.json" ]
  ok $? "run: the Secret and the Job are created, the record arrives, the Job and the Secret are deleted"
  [ "$(grep -o 'create -f [^ ]*' "$T/calls" | sed 's#.*/##' | paste -sd' ' -)" = "policy.json secret.json job.json" ] \
    && jq -e '.metadata.name == $n and .spec.podSelector.matchLabels["batch.kubernetes.io/job-name"] == $n
      and [.spec.egress[].to[].ipBlock.cidr] == ["203.0.113.1/32", "203.0.113.24/32"]' --arg n "$(cat "$T/r1/name")" "$T/created-policy.json" > /dev/null \
    && grep -q "delete networkpolicy gate-42-1-live-" "$T/calls"
  ok $? "run: the run's NetworkPolicy for its addresses is created before the Secret and the Job, and deleted with them"
  local bad_r rc_r=0
  for bad_r in '' '203.0.113.0/24' 'console.hw307.omani.works'; do
    reset_stub
    ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=61
      TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r15" --targets "$bad_r" --job-image "$JI" ) > "$T/r15.out" 2>&1
    [ $? = 2 ] && [ "$(calls)" = 0 ] && grep -q 'the run does not start' "$T/r15.out" || { echo "not refused: '$bad_r'"; rc_r=1; }
  done
  ok "$rc_r" "run: an empty address list, a range or a name refuses to start, before anything is created on the parent"
  grep -q '::warning::from the cluster' "$T/r1.out" && ! grep -q '::stop-commands::' "$T/r1.out"; ok $? "run: outside GitHub the log is streamed as it is"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_ACTIONS=true GITHUB_RUN_ID=43
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r1b" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r1b.out" 2>&1
  grep -q '^::stop-commands::' "$T/r1b.out" && grep -q '^::error::a message$' "$T/r1b.out"
  ok $? "run: on GitHub the streamed log cannot issue workflow commands; the Job's errors become annotations"
  reset_stub; cp "$T/nodone.txt" "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=44
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r2" --targets "$TG" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q "delete job gate-44-1-addresses-" "$T/calls" && grep -q "delete secret gate-44-1-addresses-" "$T/calls"
  ok $? "run: a Job that did not finish its work fails the run, and the Job and the Secret are still deleted"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=45 STUB_FAIL_TIMES=1 STUB_FINAL=forbidden
    TOOLS="$T/tools" cmd_run --mode regions --out "$T/r3" --targets "$TG" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q "delete job gate-45-1-regions-" "$T/calls"
  ok $? "run: a refused create fails the run and still deletes by name"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=46 STUB_GATE=terminated STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r4" --targets "$TG" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 0 ] && [ "$(grep -c ' logs -f ' "$T/calls")" = 1 ] && grep -q "delete job gate-46-1-addresses-" "$T/calls"
  ok $? "run: when the container gate has ended the log is read at once, though the Job has not ended"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=47 GITHUB_RUN_ATTEMPT=2
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r5" --targets "$TG" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 0 ] && grep -Eqx 'gate-47-2-addresses-[0-9a-f]{6}' "$T/r5/name" && grep -q '"name": "gate-47-2-addresses-' "$T/created-job.json" \
    && grep -q "delete job gate-47-2-addresses-" "$T/calls" && grep -q "delete secret gate-47-2-addresses-" "$T/calls"
  ok $? "run: on a re-run (attempt 2) the Job and its Secret carry the attempt in their names and are deleted by them"
  local pull='{"waiting": {"reason": "ImagePullBackOff", "message": "Back-off pulling image \"x\": not found"}}'
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=48 STUB_PROBE="$pull" GATE_PULL_MINUTES=0 GATE_START_MINUTES=0
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r6" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r6.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'could not pull an image of the Job for 0 minutes: probe:ImagePullBackOff (Back-off pulling image "x": not found)' "$T/r6.out" \
    && ! grep -q ' logs ' "$T/calls" && grep -q "delete job gate-48-1-live-" "$T/calls" && grep -q "delete secret gate-48-1-live-" "$T/calls"
  ok $? "run: a probe container in image pull back-off fails the run with its reason, without following the log, and deletes the Job"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=49 STUB_PROBE="$pull" GATE_START_MINUTES=0
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r7" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r7.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'did not start within 0 minutes; last: backoff probe:ImagePullBackOff' "$T/r7.out" && ! grep -q 'could not pull' "$T/r7.out"
  ok $? "run: a back-off shorter than GATE_PULL_MINUTES is waited on, within the bound GATE_START_MINUTES"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=54 GATE_START_MINUTES=0
    export STUB_UNSCHEDULED='0/1 nodes are available: 1 Insufficient memory. preemption: 0/1 nodes are available: 1 No preemption victims found for incoming pod.'
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r12" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r12.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'the Pod of the Job: waiting gate:not created,probe:not created (Pending; not scheduled: Unschedulable, 0/1 nodes are available: 1 Insufficient memory\.' "$T/r12.out" \
    && grep -q 'did not start within 0 minutes; last: waiting gate:not created,probe:not created (Pending; not scheduled: Unschedulable, 0/1 nodes' "$T/r12.out" \
    && ! grep -q ' logs ' "$T/calls" && grep -q "delete job gate-54-1-live-" "$T/calls" && grep -q "delete secret gate-54-1-live-" "$T/calls"
  ok $? "run: a Pod the scheduler has not placed is reported with its phase and the scheduler's reason, and the Job is deleted"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=50 STUB_PROBE='{"waiting": {"reason": "CreateContainerConfigError", "message": "no such key"}}'
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r8" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r8.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'cannot start: probe:CreateContainerConfigError (no such key)' "$T/r8.out" && [ "$(calls_of ' get pods ')" = 1 ]
  ok $? "run: a container that can never start fails the run at the first reading"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=51 STUB_GATE=terminated STUB_PROBE='{"running": {}}' STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r9" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r9.out" 2>&1
  o=$?
  [ "$o" = 0 ] && [ "$(calls_of ' logs -f ')" = 1 ] && grep -q 'has not ended: its container gate has ended (still: probe:running); its log is read now' "$T/r9.out" \
    && grep -q "delete job gate-51-1-live-" "$T/calls"
  ok $? "run: a probe that hangs after the container gate has ended does not hold the run; it says so"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=52 STUB_GATE=terminated STUB_PROBE='{"waiting": {"reason": "ContainerCreating"}}' STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r10" --targets "$TG" --job-image "$JI" --probe-image "$PI" ) > "$T/r10.out" 2>&1
  o=$?
  [ "$o" = 0 ] && grep -q '(still: probe:ContainerCreating)' "$T/r10.out"
  ok $? "run: a probe that never started does not hold the run once the container gate has ended"
  reset_stub; echo "suite output" > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=53 STUB_HANG=1 GATE_FOLLOW_GRACE_SECONDS=-100000 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode regions --out "$T/r11" --targets "$TG" --job-image "$JI" ) > "$T/r11.out" 2>&1
  o=$?
  [ "$o" = 1 ] && [ "$(calls_of ' logs -f ')" = 1 ] && grep -q 'has not ended .* minutes after it was created' "$T/r11.out" && grep -q "delete job gate-53-1-regions-" "$T/calls"
  ok $? "run: the wait ends at the Job's deadline even when its log can be followed for ever"

  # the recipes
  recipe_job "$JI" | grep -qx "FROM $JI"; ok $? "recipe: the Job image starts from the suite image by digest"
  recipe_probe | grep -Eq '^FROM mcr\.microsoft\.com/playwright:v1\.46\.0-jammy@sha256:[0-9a-f]{64}$' && recipe_probe | grep -q "playwright@1.46.0"
  ok $? "recipe: the probe image starts from the Playwright image by digest, with the same release of the package"
  [ "$(recipe_hash job)" != "$(recipe_hash probe)" ] && [[ "$(recipe_hash job)" =~ ^[0-9a-f]{12}$ ]]; ok $? "recipe: each recipe has its own hash for its tag"

  echo "gate-parent-job self-test: $((n - fails)) of $n cases passed"
  [ "$fails" = 0 ]
}

case "${1:-}" in
  execution) shift; cmd_execution "$@" ;;
  preflight) shift; cmd_preflight "$@" ;;
  targets) shift; cmd_targets "$@" ;;
  policy) shift; cmd_policy "$@" ;;
  image) shift; cmd_image "$@" ;;
  run) shift; cmd_run "$@" ;;
  cleanup) shift; cmd_cleanup "$@" ;;
  recipe) shift; cmd_recipe "$@" ;;
  inner) shift; cmd_inner "$@" ;;
  probe) shift; cmd_probe ;;
  --self-test) self_test ;;
  *) usage ;;
esac
