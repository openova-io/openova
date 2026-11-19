#!/usr/bin/env bash
# gate-parent-job.sh - the gate's cluster work as a short-lived Job on the parent (FND-078).
#
# The gate workflows (.github/workflows/foundation-gate.yaml, foundation-gate-standing.yaml) run on
# machines hosted by GitHub. In parent execution, everything that dials a Sovereign's API port runs
# in a Job on the parent, created through the scoped access of FND-079: namespace acceptance-gate,
# ServiceAccount gate (jobs and pods: create, get, list, watch, delete; pods/log: get; secrets:
# create, delete, never read), Pod Security restricted, a quota of 4 pods, egress only to DNS and
# to TCP 443 and 6443 of the standing environment. The node's container runtime pulls the images,
# outside the Pod's network, so every image is named by its digest.
#
#   gate-parent-job.sh execution <value>     prints hosted or parent; empty means hosted; else exit 2
#   gate-parent-job.sh image --candidate <dir> --commit <sha> --output <file>
#        Builds or reuses, and pushes with docker buildx: the suite image of the commit under test,
#        built with tests/acceptance/sovereign/Containerfile as build-conformance.yaml builds it;
#        the Job image, that suite image by digest plus the commit's files at /opt/candidate (the
#        repository root a run in cloud reads); the probe image, the Playwright image by digest
#        plus the playwright package. Writes job=<ref@digest>, probe=<ref@digest> and, when it
#        built it, suite=<ref@digest> into <file>. Needs GH_TOKEN and GITHUB_ACTOR.
#   gate-parent-job.sh run --mode addresses|live|regions --out <dir> --job-image <ref@digest>
#                         [--probe-image <ref@digest>]
#        Writes the region kubeconfigs and the tool scripts into a Secret of the run (never into
#        the Job object), the pull credential into a second Secret, creates the Job with the run's
#        inputs, streams its log, fetches the files the Job printed and deletes the Job and both
#        Secrets whatever happened. Into <dir>: name, outputs (key=value), summary.md, errors,
#        files/<path>, gate.log. Exit 0 when the Job did its work and every file arrived whole.
#   gate-parent-job.sh cleanup --out <dir>   deletes the Job and the Secrets named in <dir>/name
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
#   The run's inputs, copied into the Job's environment: ENV_LABEL, GATE_RUN, CANDIDATE_COMMIT,
#   GITHUB_ACTIONS, GITHUB_WORKFLOW_REF, GITHUB_RUN_ID, GITHUB_RUN_ATTEMPT and every ACCEPT_*
#   except the paths ACCEPT_KUBECONFIG_A, _B, ACCEPT_MANIFEST, ACCEPT_REPO_ROOT, ACCEPT_RECORD_DIR.
# Every call to the parent retries a failure that looks like a lost connection, at most
# GATE_PARENT_TRIES times (default 6, waiting GATE_PARENT_BACKOFF x attempt seconds, default 5).
# The Pod's containers are read on every pass: every container must start within GATE_START_MINUTES
# (default 30); a container that can never start fails the run at once, an image the node cannot
# pull after GATE_PULL_MINUTES of back-off (default 5); once the container gate has ended, its log
# is read whether the probe has ended or not, and the wait ends GATE_FOLLOW_GRACE_SECONDS (default
# 300) after the Job's own deadline at the latest.
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

# ---------------------------------------------------------------- manifests
# manifests MODE NAME JOB_IMAGE PROBE_IMAGE OUTDIR: writes secret.json, pull.json (when a pull
# credential is set) and job.json. Only secret.json and pull.json hold a credential; they are
# written with mode 600 and removed by the caller once created.
manifests() {
  ( umask 077
    python3 - "$@" "$SELF" "$TOOLS" <<'PY'
import base64, json, os, re, sys
mode, name, job_image, probe_image, out, self_path, tools = sys.argv[1:8]
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
               "resources": {"requests": {"cpu": "500m", "memory": "512Mi"}, "limits": {"cpu": "1", "memory": "1Gi"}}}]
volumes = [{"name": "run", "secret": {"secretName": name, "defaultMode": 0o440}},
           {"name": "shared", "emptyDir": {"medium": "Memory", "sizeLimit": "16Mi"}},
           {"name": "scratch", "emptyDir": {"sizeLimit": "4Gi"}}]
if mode == "live":
    containers.append({"name": "probe", "image": probe_image, "imagePullPolicy": "IfNotPresent",
                       "command": ["/bin/bash", "/run/gate-secret/gate-parent-job.sh", "probe"],
                       "env": env, "securityContext": restricted,
                       "volumeMounts": mounts + [{"name": "shm", "mountPath": "/dev/shm"}],
                       "resources": {"requests": {"cpu": "500m", "memory": "1Gi"}, "limits": {"cpu": "1", "memory": "2Gi"}}})
    volumes.append({"name": "shm", "emptyDir": {"medium": "Memory", "sizeLimit": "256Mi"}})
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
  #   waiting <why>           a container is still being created
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
print(kind + " " + ",".join(why(n) for n in waiting))'
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
  [ "$rc" = 0 ] && echo "gate-parent-job: deleted the Job $name and its Secrets" >&2
  return $rc
}

cmd_cleanup() {
  [ "${1:-}" = --out ] && [ -n "${2:-}" ] || usage
  [ -r "${GATE_PARENT_KUBECONFIG:-}" ] || die "GATE_PARENT_KUBECONFIG is not a readable file"
  cleanup_names "$2"
}

cmd_run() {
  local mode="" jimg="" pimg=""
  RUN_OUT=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode) mode="$2"; shift 2 ;;
      --out) RUN_OUT="$2"; shift 2 ;;
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
  pk create -f "$RUN_OUT/secret.json" > /dev/null || die "the Secret of the run could not be created"
  rm -f "$RUN_OUT/secret.json"
  if [ -s "$RUN_OUT/pull.json" ]; then
    pk create -f "$RUN_OUT/pull.json" > /dev/null || die "the pull Secret of the run could not be created"
    rm -f "$RUN_OUT/pull.json"
  fi
  pk create -f "$RUN_OUT/job.json" > /dev/null || die "the Job could not be created"
  local created
  created="$(date +%s)"
  echo "gate-parent-job: created the Job $name in $NS on the parent" >&2
  # 1. Every container starts: the node pulls the images. A container that can never start ends the run at
  #    once; an image the node cannot pull ends it after GATE_PULL_MINUTES of back-off; the container gate
  #    ending first goes straight to its log.
  local start_deadline=$(( created + ${GATE_START_MINUTES:-30} * 60 )) st last="" pull_since=""
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
    [ "$st" != "$last" ] && echo "gate-parent-job: the Pod of the Job: $st" >&2
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

inner_addresses() {
  local r list rc=0
  for r in a b; do
    kubectl --kubeconfig "$SECRET_DIR/k$r" get nodes -o json > "$SCRATCH/$r.json" || { echo "GATE-ERROR the nodes of region $r could not be read"; rc=1; }
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
  local deadline=$(( $(date +%s) + ${GATE_PROBE_MINUTES:-20} * 60 ))
  while [ ! -e "$SHARED/probe.done" ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { stop "the browser probe did not end within ${GATE_PROBE_MINUTES:-20} minutes"; return; }
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

inner_live() {
  local r t born="" chain="" last="nothing was read" s complete failed current deadline rc f
  trap '[ -e "$SHARED/probe.go" ] || touch "$SHARED/probe.skip"' EXIT
  # When the environment was created: the earliest creation of kube-system in the two regions.
  for r in a b; do
    t="$(kubectl --kubeconfig "$SECRET_DIR/k$r" get namespace kube-system -o jsonpath='{.metadata.creationTimestamp}')"
    if [[ ! "$t" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then born=""; break; fi
    if [ -z "$born" ] || [[ "$t" < "$born" ]]; then born="$t"; fi
  done
  out "born=$born"
  summ "Environment created at: ${born:-not read}"
  # Wait until the hand-over chain has ended (completed, or failed at a step), at most 180 minutes.
  deadline=$(( $(date +%s) + 180 * 60 ))
  while :; do
    if s="$(kubectl --kubeconfig "$KA" -n catalyst get configmap self-sovereign-cutover-status \
          -o jsonpath='{.data.cutoverComplete}|{.data.failedStep}|{.data.currentStep}' 2>&1)"; then
      IFS='|' read -r complete failed current <<<"$s"
      if [ "$complete" = true ]; then chain="completed"; break; fi
      if [ -n "$failed" ]; then chain="failed at step $failed"; break; fi
      last="not ended, current step: ${current:-none}"
    else
      last="the status ConfigMap could not be read: $s"
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      summ "Hand-over chain: did not end within 180 minutes; last observed: $last"
      out "chain=timeout"
      echo "GATE-ERROR The hand-over chain did not end within 180 minutes; last observed: $last. The suite was not run."
      return
    fi
    sleep 30
  done
  out "chain=$chain"
  summ "Hand-over chain: $chain"
  if [ "$chain" != completed ]; then
    summ "Hand-over error: $(kubectl --kubeconfig "$KA" -n catalyst get configmap self-sovereign-cutover-status -o jsonpath='{.data.lastError}' 2>&1 || true)"
  fi
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
  local deadline=$(( $(date +%s) + ${GATE_PROBE_WAIT_MINUTES:-330} * 60 )) d="$SCRATCH/probe" rc
  mkdir -p "$SCRATCH/home" "$SCRATCH/tmp"
  while [ ! -e "$SHARED/probe.go" ] && [ ! -e "$SHARED/probe.skip" ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { echo "no sign of the container gate; the probe did not run"; exit 0; }
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
  # set) in the state STUB_PROBE, a JSON object; STUB_HANG: following the log never ends the Job.
  mkdir -p "$T/bin"
  cat > "$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
S="$STUB_DIR"
echo "$*" >> "$S/calls"
c=$(cat "$S/count" 2>/dev/null || echo 0); c=$((c + 1)); echo "$c" > "$S/count"
if [ "$c" -le "${STUB_FAIL_TIMES:-0}" ]; then echo 'Unable to connect to the server: http2: client connection lost' >&2; exit 1; fi
case "${STUB_FINAL:-ok}" in
  forbidden) echo 'Error from server (Forbidden): jobs.batch is forbidden' >&2; exit 1 ;;
  exists) echo 'Error from server (AlreadyExists): jobs.batch "x" already exists' >&2; exit 1 ;;
esac
a=" $* "
case "$a" in
  *" create -f "*) f="${a#* create -f }"; f="${f%% *}"; cp "$f" "$S/created-$(basename "$f")"; echo created ;;
  *" get job "*) printf '{"status": {%s}}\n' "$(cat "$S/job.state" 2>/dev/null)" ;;
  *" get pods "*) p=""; [ -z "${STUB_PROBE:-}" ] || p=', {"name": "probe", "state": '"$STUB_PROBE"'}'
    echo '{"items": [{"status": {"phase": "Running", "containerStatuses": [{"name": "gate", "state": {"'"${STUB_GATE:-running}"'": {}}}'"$p"']}}]}' ;;
  *" logs -f "*) cat "$S/log.txt"; [ -n "${STUB_HANG:-}" ] || echo '"succeeded": 1' > "$S/job.state" ;;
  *" logs "*) cat "$S/log.txt" ;;
  *" delete "*) echo deleted ;;
  *) echo "stub: $*" >&2; exit 3 ;;
esac
STUB
  chmod +x "$T/bin/kubectl"
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

  # the manifests
  mkdir -p "$T/tools/scripts" "$T/m"
  for f in gate-outside-dial.py standing-gate.py sso-zero-click-probe.mjs; do echo "tool $f" > "$T/tools/scripts/$f"; done
  echo "region-a-credential-abc" > "$T/ka"; echo "region-b-credential-def" > "$T/kb"; echo "owner-session-ghi" > "$T/session"
  local JI PI
  JI="ghcr.io/openova-io/openova/conformance@sha256:$(printf 'a%.0s' {1..64})"
  PI="ghcr.io/openova-io/openova/conformance@sha256:$(printf 'b%.0s' {1..64})"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GATE_SESSION_FILE="$T/session" GATE_TOOLS="$T/tools" \
      GATE_PULL_USER=bot GATE_PULL_TOKEN=pull-token-jkl ENV_LABEL=hw307 GATE_RUN=42 ACCEPT_API_PORT_OUTSIDE=1.2.3.4=timeout \
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
    if "ACCEPT_API_PORT_OUTSIDE" not in names or "ENV_LABEL" not in names: bad.append(c["name"] + " inputs")
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

  # images by digest only
  ( GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" cmd_run --mode live --out "$T/r0" --job-image ghcr.io/x/y:latest --probe-image "$PI" ) 2>/dev/null
  [ $? = 2 ]; ok $? "run: a Job image named by a tag is refused"
  ( GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" cmd_run --mode live --out "$T/r0" --job-image "$JI" ) 2>/dev/null
  [ $? = 2 ]; ok $? "run: live without a probe image is refused"
  ( cmd_run --mode sweep --out "$T/r0" --job-image "$JI" ) 2>/dev/null; [ $? = 2 ]; ok $? "run: an unknown mode is refused"

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
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r1" --job-image "$JI" --probe-image "$PI" ) > "$T/r1.out" 2>&1
  o=$?
  [ "$o" = 0 ] && cmp -s "$T/payload" "$T/r1/files/record/hw307-1.json" && [ -e "$T/created-job.json" ] && [ -e "$T/created-secret.json" ] \
    && grep -q "delete job gate-42-1-live-" "$T/calls" && grep -q "delete secret gate-42-1-live-" "$T/calls" && [ ! -e "$T/r1/secret.json" ]
  ok $? "run: the Secret and the Job are created, the record arrives, the Job and the Secret are deleted"
  grep -q '::warning::from the cluster' "$T/r1.out" && ! grep -q '::stop-commands::' "$T/r1.out"; ok $? "run: outside GitHub the log is streamed as it is"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_ACTIONS=true GITHUB_RUN_ID=43
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r1b" --job-image "$JI" --probe-image "$PI" ) > "$T/r1b.out" 2>&1
  grep -q '^::stop-commands::' "$T/r1b.out" && grep -q '^::error::a message$' "$T/r1b.out"
  ok $? "run: on GitHub the streamed log cannot issue workflow commands; the Job's errors become annotations"
  reset_stub; cp "$T/nodone.txt" "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=44
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r2" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q "delete job gate-44-1-addresses-" "$T/calls" && grep -q "delete secret gate-44-1-addresses-" "$T/calls"
  ok $? "run: a Job that did not finish its work fails the run, and the Job and the Secret are still deleted"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=45 STUB_FAIL_TIMES=1 STUB_FINAL=forbidden
    TOOLS="$T/tools" cmd_run --mode regions --out "$T/r3" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q "delete job gate-45-1-regions-" "$T/calls"
  ok $? "run: a refused create fails the run and still deletes by name"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=46 STUB_GATE=terminated STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r4" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 0 ] && [ "$(grep -c ' logs -f ' "$T/calls")" = 1 ] && grep -q "delete job gate-46-1-addresses-" "$T/calls"
  ok $? "run: when the container gate has ended the log is read at once, though the Job has not ended"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=47 GITHUB_RUN_ATTEMPT=2
    TOOLS="$T/tools" cmd_run --mode addresses --out "$T/r5" --job-image "$JI" ) > /dev/null 2>&1
  o=$?
  [ "$o" = 0 ] && grep -Eqx 'gate-47-2-addresses-[0-9a-f]{6}' "$T/r5/name" && grep -q '"name": "gate-47-2-addresses-' "$T/created-job.json" \
    && grep -q "delete job gate-47-2-addresses-" "$T/calls" && grep -q "delete secret gate-47-2-addresses-" "$T/calls"
  ok $? "run: on a re-run (attempt 2) the Job and its Secret carry the attempt in their names and are deleted by them"
  local pull='{"waiting": {"reason": "ImagePullBackOff", "message": "Back-off pulling image \"x\": not found"}}'
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=48 STUB_PROBE="$pull" GATE_PULL_MINUTES=0 GATE_START_MINUTES=0
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r6" --job-image "$JI" --probe-image "$PI" ) > "$T/r6.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'could not pull an image of the Job for 0 minutes: probe:ImagePullBackOff (Back-off pulling image "x": not found)' "$T/r6.out" \
    && ! grep -q ' logs ' "$T/calls" && grep -q "delete job gate-48-1-live-" "$T/calls" && grep -q "delete secret gate-48-1-live-" "$T/calls"
  ok $? "run: a probe container in image pull back-off fails the run with its reason, without following the log, and deletes the Job"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=49 STUB_PROBE="$pull" GATE_START_MINUTES=0
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r7" --job-image "$JI" --probe-image "$PI" ) > "$T/r7.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'did not start within 0 minutes; last: backoff probe:ImagePullBackOff' "$T/r7.out" && ! grep -q 'could not pull' "$T/r7.out"
  ok $? "run: a back-off shorter than GATE_PULL_MINUTES is waited on, within the bound GATE_START_MINUTES"
  reset_stub
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=50 STUB_PROBE='{"waiting": {"reason": "CreateContainerConfigError", "message": "no such key"}}'
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r8" --job-image "$JI" --probe-image "$PI" ) > "$T/r8.out" 2>&1
  o=$?
  [ "$o" = 1 ] && grep -q 'cannot start: probe:CreateContainerConfigError (no such key)' "$T/r8.out" && [ "$(calls_of ' get pods ')" = 1 ]
  ok $? "run: a container that can never start fails the run at the first reading"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=51 STUB_GATE=terminated STUB_PROBE='{"running": {}}' STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r9" --job-image "$JI" --probe-image "$PI" ) > "$T/r9.out" 2>&1
  o=$?
  [ "$o" = 0 ] && [ "$(calls_of ' logs -f ')" = 1 ] && grep -q 'has not ended: its container gate has ended (still: probe:running); its log is read now' "$T/r9.out" \
    && grep -q "delete job gate-51-1-live-" "$T/calls"
  ok $? "run: a probe that hangs after the container gate has ended does not hold the run; it says so"
  reset_stub; { echo "suite output"; echo GATE-DONE; } > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=52 STUB_GATE=terminated STUB_PROBE='{"waiting": {"reason": "ContainerCreating"}}' STUB_HANG=1 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode live --out "$T/r10" --job-image "$JI" --probe-image "$PI" ) > "$T/r10.out" 2>&1
  o=$?
  [ "$o" = 0 ] && grep -q '(still: probe:ContainerCreating)' "$T/r10.out"
  ok $? "run: a probe that never started does not hold the run once the container gate has ended"
  reset_stub; echo "suite output" > "$T/log.txt"
  ( export GATE_KUBECONFIG_A="$T/ka" GATE_KUBECONFIG_B="$T/kb" GITHUB_RUN_ID=53 STUB_HANG=1 GATE_FOLLOW_GRACE_SECONDS=-100000 GATE_FOLLOW_MAX=5
    TOOLS="$T/tools" cmd_run --mode regions --out "$T/r11" --job-image "$JI" ) > "$T/r11.out" 2>&1
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
  image) shift; cmd_image "$@" ;;
  run) shift; cmd_run "$@" ;;
  cleanup) shift; cmd_cleanup "$@" ;;
  recipe) shift; cmd_recipe "$@" ;;
  inner) shift; cmd_inner "$@" ;;
  probe) shift; cmd_probe ;;
  --self-test) self_test ;;
  *) usage ;;
esac
