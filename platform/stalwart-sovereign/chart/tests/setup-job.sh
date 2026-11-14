#!/usr/bin/env bash
# setup-job.sh - the setup Job of bp-stalwart-sovereign is bounded, is not a hook, can reach
# the admin API, can run again, and takes no other party's password.
#
# Usage: bash setup-job.sh [<chart dir>]   check the chart (default: the chart this file is in)
#        bash setup-job.sh --self-test      show that every rule below can fail
#
# The chart is rendered with helm from a copy whose Chart.yaml has no dependencies block, so
# no chart repository is needed. Rules R1 to R8 read the render. Rules R9 to R12 run the Job's
# script, taken from the render, under sh against a stand-in for curl that this file writes
# into a temporary folder; no network and no cluster is used.
#   R1  No rendered object is a Helm hook: Helm always waits for a hook, and a hook Job that
#       does not finish holds the release and the bootstrap kit.
#   R2  Exactly one Job, named <fullname>-setup-<10 hex>: the suffix is a checksum of its pod
#       template, so a changed template gets a new Job and an unchanged one is left alone.
#       Changing the script changes the name.
#   R3  The Job is bounded: activeDeadlineSeconds and backoffLimit are set.
#   R4  The mirror Secret carries every key its readers use (smtp-*, user, password, SMTP_*)
#       and the reflector annotations, with the default reflection namespace chargeback.
#   R5  The mirror is written only after the principal is confirmed.
#   R6  The submission password is read from the chart's own submission Secret or generated;
#       the helper never reads the mirror Secret, which may hold another server's password.
#   R7  The NetworkPolicy, which selects the Job's pod, lets it reach the admin API: an egress
#       rule to the chart's own pods on TCP 8080.
#   R8  With sovereignSMTPCredentialsMirror.enabled=false nothing is rendered into the mirror
#       namespace and the Job is told not to write the mirror.
#   R9  First run: the principal is created, then confirmed, then the mirror is created with
#       the values of the submission Secret; exit 0; no password is printed and no request
#       file is left.
#   R10 Second run: the principal's secret is set and the mirror is replaced (PUT) by the
#       same body as in the first run; exit 0. Running again leaves the same result.
#   R11 A refused principal read or write, a principal that is not confirmed, or a refused
#       mirror write ends the run with exit 1; in the first three no mirror is written.
#   R12 When the admin API never answers, the run gives up after at most 60 tries with exit 1
#       and calls nothing else.
# Exit 0 when every rule holds, 1 when one fails.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
helm="${HELM_BIN:-helm}"

render() { # <chart dir> <out file> [helm arguments]
  local src="$1" out="$2" tmp rc=0
  shift 2
  tmp="$(mktemp -d)"
  cp -r "$src" "$tmp/chart"
  rm -rf "$tmp/chart/charts"
  awk '/^dependencies:/{skip=1;next} skip&&/^[^ #-]/{skip=0} !skip' "$src/Chart.yaml" > "$tmp/chart/Chart.yaml"
  "$helm" template stalwart-sovereign "$tmp/chart" --namespace stalwart-sovereign \
    --set global.sovereignFQDN=render-test.example.test "$@" > "$out" || rc=1
  rm -rf "$tmp"
  return "$rc"
}

job_name() { # <rendered file>
  awk '/^kind: Job$/{f=1;next} f&&/^  name: /{print $2;exit}' "$1"
}

write_fake_curl() { # <file>: a stand-in for curl that answers from files under $FAKE
  cat > "$1" <<'FAKE_CURL'
#!/usr/bin/env bash
# Stand-in for curl. State and answers are files under $FAKE:
#   health-down        the admin API does not answer (exit 7)
#   principal          the submission principal exists (holds the last body written)
#   principal-read     HTTP code for every read of the principal
#   principal-write    HTTP code for every write of the principal
#   principal-lost     a principal create answers 200 and stores nothing
#   mirror             the mirror Secret exists (holds the last body written)
#   mirror-write       HTTP code for every write of the mirror
# Every call is one line of $FAKE/calls: <method> <path>.
method=GET; url=""; w=0; f=0; data=""
while [ $# -gt 0 ]; do
  case "$1" in
    -X) method="$2"; shift ;;
    -d) data="$2"; shift ;;
    -w) w=1; shift ;;
    -m|-u|-H|-o|--cacert) shift ;;
    -fsS) f=1 ;;
    http*) url="$1" ;;
  esac
  shift
done
path="/${url#*://*/}"
echo "$method $path" >> "$FAKE/calls"
body() { if [ "${data#@}" != "$data" ]; then cat "${data#@}"; else printf '%s' "$data"; fi; }
code=500
case "$method $path" in
  "GET /api/health")
    if [ -e "$FAKE/health-down" ]; then exit 7; fi
    code=200 ;;
  "GET /api/principal/"*)
    if [ -e "$FAKE/principal-read" ]; then code="$(cat "$FAKE/principal-read")"
    elif [ -e "$FAKE/principal" ]; then code=200
    else code=404; fi ;;
  "POST /api/principal")
    if [ -e "$FAKE/principal-write" ]; then code="$(cat "$FAKE/principal-write")"
    else code=200; [ -e "$FAKE/principal-lost" ] || body > "$FAKE/principal"; fi ;;
  "PATCH /api/principal/"*)
    if [ -e "$FAKE/principal-write" ]; then code="$(cat "$FAKE/principal-write")"
    else code=200; body > "$FAKE/principal"; fi ;;
  "POST /api/settings") code=200 ;;
  "GET /api/settings/group"*) printf '{"public-key":"STANDINKEY"}'; code=200 ;;
  "GET /api/v1/namespaces/"*"/secrets/"*)
    if [ -e "$FAKE/mirror" ]; then code=200; else code=404; fi ;;
  "POST /api/v1/namespaces/"*"/secrets")
    if [ -e "$FAKE/mirror-write" ]; then code="$(cat "$FAKE/mirror-write")"
    elif [ -e "$FAKE/mirror" ]; then code=409
    else code=201; body > "$FAKE/mirror"; fi ;;
  "PUT /api/v1/namespaces/"*"/secrets/"*)
    if [ -e "$FAKE/mirror-write" ]; then code="$(cat "$FAKE/mirror-write")"
    elif [ -e "$FAKE/mirror" ]; then code=200; body > "$FAKE/mirror"
    else code=404; fi ;;
  "PATCH /api/v1/namespaces/"*"/configmaps/"*) code=200 ;;
esac
if [ "$w" = 1 ]; then printf '%s' "$code"; fi
if [ "$f" = 1 ] && [ "$code" -ge 400 ]; then exit 22; fi
exit 0
FAKE_CURL
  chmod +x "$1"
}

run_job() { # <work dir>: runs the Job's script once; prints its exit code; output in <work dir>/out
  local w="$1" rc=0
  rm -f "$w/fake/calls"
  mkdir -p "$w/tmp"
  ( cd "$w" && env -i PATH="$w/bin:$PATH" FAKE="$w/fake" \
      STALWART_URL="http://stalwart.test:8080" SOVEREIGN_FQDN="render-test.example.test" \
      MAIL_HOST="mail.render-test.example.test" SENDER_ADDRESS="noreply@render-test.example.test" \
      SUBMISSION_USER="noreply" RELEASE_NAMESPACE="stalwart-sovereign" DNS_RECORDS_CONFIGMAP="dns-records" \
      MIRROR_NAMESPACE="catalyst-system" MIRROR_SECRET_NAME="sovereign-smtp-credentials" MIRROR_ENABLED="true" \
      REFLECTION_NAMESPACES="chargeback" ADMIN_PASSWORD="standin-admin-pw" SUBMISSION_PASSWORD="standin-submission-pw" \
      sh -ec "$(cat "$w/script.sh")" ) > "$w/out" 2>&1 || rc=$?
  touch "$w/fake/calls"
  echo "$rc"
}

fresh() { # <work dir>: forget every state and answer of the stand-in
  rm -rf "$1/fake" "$1/tmp"
  mkdir -p "$1/fake" "$1/tmp"
}

mirror_writes() { # <work dir>: number of calls that write the mirror
  grep -c -E '^(POST|PUT|PATCH) /api/v1/namespaces/catalyst-system/secrets' "$1/fake/calls" || true
}

writes() { # <work dir>: the calls about the principal and the mirror, in order, each followed by a comma
  grep -E ' /api/principal| /api/v1/namespaces/catalyst-system/secrets' "$1/fake/calls" | tr '\n' ',' || true
}

behaviour() { # <rendered file>; prints one line per failed rule
  local out="$1" w fail=0 rc n first mirror="/api/v1/namespaces/catalyst-system/secrets"
  w="$(mktemp -d)"
  mkdir -p "$w/bin" "$w/sa"
  write_fake_curl "$w/bin/curl"
  printf '#!/bin/sh\nexit 0\n' > "$w/bin/sleep"
  chmod +x "$w/bin/sleep"
  printf 'standin-token' > "$w/sa/token"
  : > "$w/sa/ca.crt"
  awk '/^kind: Job$/{j=1} j&&/^            - \|$/{s=1;next} s&&/^---/{exit} s{sub(/^              /,""); print}' "$out" \
    | sed "s#/tmp/#$w/tmp/#g; s#/var/run/secrets/kubernetes.io/serviceaccount#$w/sa#g" > "$w/script.sh"
  if ! grep -q '^set -eu$' "$w/script.sh"; then
    echo "FAIL R9: the Job's script cannot be read from the render"
    echo "FAIL R10: the Job's script cannot be read from the render"
    echo "FAIL R11: the Job's script cannot be read from the render"
    echo "FAIL R12: the Job's script cannot be read from the render"
    rm -rf "$w"; return 1
  fi

  # R9: first run.
  local why=""
  fresh "$w"
  rc="$(run_job "$w")"
  first="$(cat "$w/fake/mirror" 2>/dev/null || true)"
  if [ "$rc" != "0" ]; then why="exit $rc"
  elif [ "$(writes "$w")" != "GET /api/principal/noreply,POST /api/principal,GET /api/principal/noreply,GET $mirror/sovereign-smtp-credentials,POST $mirror," ]; then why="other calls, or another order: $(writes "$w")"
  elif grep -q -e 'standin-admin-pw' -e 'standin-submission-pw' "$w/out"; then why="a password is printed"
  elif [ -n "$(ls -A "$w/tmp")" ]; then why="a request file is left"
  else
    why="$(python3 - "$w/fake/principal" "$w/fake/mirror" <<'PY' || true
import base64, json, sys
def need(ok, what):
    if not ok:
        print(what)
        sys.exit(1)
try:
    principal = json.load(open(sys.argv[1]))
    mirror = json.load(open(sys.argv[2]))
    meta = mirror["metadata"]
    got = {k: base64.b64decode(v).decode() for k, v in mirror["data"].items()}
except Exception as e:
    need(False, "a request body is not what was expected: %s" % type(e).__name__)
need(principal.get("name") == "noreply" and principal.get("secrets") == ["standin-submission-pw"]
     and principal.get("emails") == ["noreply@render-test.example.test"], "the principal has another name, secret or address")
need((mirror.get("kind"), mirror.get("type"), meta.get("name"), meta.get("namespace"))
     == ("Secret", "Opaque", "sovereign-smtp-credentials", "catalyst-system"), "the mirror is not the Secret catalyst-system/sovereign-smtp-credentials")
pre = "reflector.v1.k8s.emberstack.com/"
need(meta.get("annotations") == {pre + "reflection-allowed": "true", pre + "reflection-allowed-namespaces": "chargeback",
                                 pre + "reflection-auto-enabled": "true", pre + "reflection-auto-namespaces": "chargeback"},
     "the mirror has other annotations than the four reflector annotations")
want = {"host": "mail.render-test.example.test", "port": "587", "from": "noreply@render-test.example.test",
        "user": "noreply", "pass": "standin-submission-pw"}
keys = {"smtp-" + k: v for k, v in want.items()}
keys.update({"SMTP_" + k.upper(): v for k, v in want.items()})
keys.update({"user": want["user"], "password": want["pass"]})
need(sorted(got) == sorted(keys), "the mirror has other keys: " + " ".join(sorted(got)))
need(got == keys, "a key of the mirror has another value: " + " ".join(sorted(k for k in keys if got[k] != keys[k])))
PY
)"
  fi
  if [ -n "$why" ]; then
    echo "FAIL R9: a first run does not create the principal, confirm it and then create the mirror with the right values ($why)"; fail=1
  fi

  # R10: second run, on the state the first run left.
  rc="$(run_job "$w")"
  if [ "$rc" != "0" ] || [ -z "$first" ] || [ "$(cat "$w/fake/mirror" 2>/dev/null || true)" != "$first" ] \
     || [ "$(writes "$w")" != "GET /api/principal/noreply,PATCH /api/principal/noreply,GET /api/principal/noreply,GET $mirror/sovereign-smtp-credentials,PUT $mirror/sovereign-smtp-credentials," ] \
     || ! grep -q -F '"value":["standin-submission-pw"]' "$w/fake/principal" \
     || [ -n "$(ls -A "$w/tmp")" ]; then
    echo "FAIL R10: a second run does not set the principal's secret and replace the mirror by the same body (exit $rc)"; fail=1
  fi

  # R11: a failure ends the run with exit 1.
  n=0
  fresh "$w"; echo 500 > "$w/fake/principal-read"
  rc="$(run_job "$w")"; if [ "$rc" != "1" ] || [ "$(mirror_writes "$w")" != "0" ]; then n=$((n+1)); fi
  fresh "$w"; echo 500 > "$w/fake/principal-write"
  rc="$(run_job "$w")"; if [ "$rc" != "1" ] || [ "$(mirror_writes "$w")" != "0" ]; then n=$((n+1)); fi
  fresh "$w"; : > "$w/fake/principal-lost"
  rc="$(run_job "$w")"; if [ "$rc" != "1" ] || [ "$(mirror_writes "$w")" != "0" ]; then n=$((n+1)); fi
  fresh "$w"; echo 403 > "$w/fake/mirror-write"
  rc="$(run_job "$w")"; if [ "$rc" != "1" ] || [ -e "$w/fake/mirror" ]; then n=$((n+1)); fi
  if [ "$n" != "0" ]; then
    echo "FAIL R11: $n of 4 failures (principal read, principal write, principal not confirmed, mirror write) do not end the run with exit 1"; fail=1
  fi

  # R12: the wait for the admin API is bounded.
  fresh "$w"; : > "$w/fake/health-down"
  rc="$(run_job "$w")"
  n="$(grep -c -x 'GET /api/health' "$w/fake/calls" || true)"
  if [ "$rc" != "1" ] || [ "$n" -lt 1 ] || [ "$n" -gt 60 ] || [ "$(grep -c . "$w/fake/calls" || true)" != "$n" ]; then
    echo "FAIL R12: with no admin API the run does not give up after at most 60 tries (exit $rc, $n tries)"; fail=1
  fi

  rm -rf "$w"
  return "$fail"
}

check() { # <chart dir>; prints one line per failed rule
  local dir="$1" out fail=0 name n k
  out="$(mktemp)"
  if ! render "$dir" "$out"; then echo "FAIL render: helm template failed"; rm -f "$out"; return 1; fi

  if grep -q 'helm.sh/hook' "$out"; then echo "FAIL R1: a rendered object is a Helm hook"; fail=1; fi

  n="$(grep -c '^kind: Job$' "$out" || true)"
  name="$(job_name "$out")"
  if [ "$n" != "1" ] || ! [[ "$name" =~ ^stalwart-sovereign-bp-stalwart-sovereign-setup-[0-9a-f]{10}$ ]]; then
    echo "FAIL R2: expected one Job named <fullname>-setup-<10 hex>, got $n Job(s), name '$name'"; fail=1
  else
    local mut out2
    mut="$(mktemp -d)"; out2="$(mktemp)"
    cp -r "$dir" "$mut/chart"
    sed -i 's/log "done"/log "done again"/' "$mut/chart/templates/setup-job.yaml"
    if render "$mut/chart" "$out2" && [ "$(job_name "$out2")" = "$name" ]; then
      echo "FAIL R2: the Job name does not change when its script changes"; fail=1
    fi
    rm -rf "$mut" "$out2"
  fi

  local spec
  spec="$(awk '/^kind: Job$/{f=1} f&&/^---/{exit} f' "$out")"
  if ! grep -qE '^  activeDeadlineSeconds: [1-9][0-9]*$' <<<"$spec" || ! grep -qE '^  backoffLimit: [0-9]+$' <<<"$spec"; then
    echo "FAIL R3: the Job has no activeDeadlineSeconds or no backoffLimit"; fail=1
  fi

  for k in smtp-host:HOST smtp-port:PORT smtp-from:FROM smtp-user:USER smtp-pass:PASS user:USER password:PASS \
           SMTP_HOST:HOST SMTP_PORT:PORT SMTP_FROM:FROM SMTP_USER:USER SMTP_PASS:PASS; do
    if ! grep -qF "\"${k%%:*}\": \"\${B64_${k##*:}}\"" <<<"$spec"; then
      echo "FAIL R4: the mirror Secret has no key ${k%%:*}"; fail=1
    fi
  done
  for k in reflection-allowed-namespaces reflection-auto-namespaces; do
    if ! grep -qF "\"reflector.v1.k8s.emberstack.com/$k\": \"\${REFLECTION_NAMESPACES}\"" <<<"$spec"; then
      echo "FAIL R4: the mirror Secret has no annotation $k"; fail=1
    fi
  done
  if [ "$(awk '/- name: REFLECTION_NAMESPACES$/{getline; sub(/^ +/, ""); print; exit}' <<<"$spec")" != 'value: "chargeback"' ]; then
    echo "FAIL R4: the default reflection namespace is not chargeback"; fail=1
  fi

  local confirm write
  confirm="$(awk 'index($0, "principal ${SUBMISSION_USER} confirmed"){print NR; exit}' <<<"$spec")"
  write="$(awk 'index($0, "<<MIRROR_JSON"){print NR; exit}' <<<"$spec")"
  if [ -z "$confirm" ] || [ -z "$write" ] || [ "$confirm" -ge "$write" ]; then
    echo "FAIL R5: the mirror is not written after the principal is confirmed"; fail=1
  fi

  local helper
  helper="$(awk '/define "bp-stalwart-sovereign.submissionPassword"/{f=1} f{print} f&&/^\{\{- end -\}\}$/{exit}' "$dir/templates/_helpers.tpl")"
  if [ -z "$helper" ] || [ "$(grep -c 'lookup' <<<"$helper")" != "1" ] || grep -q 'sovereignSMTPCredentialsMirror\|sovereign-smtp-credentials' <<<"$helper"; then
    echo "FAIL R6: the submission password helper reads more than the chart's own submission Secret"; fail=1
  fi

  # R7: one line per egress rule of the NetworkPolicy; the wanted rule names the chart's own
  # pods (both selector labels) and TCP 8080.
  local policy egress
  policy="$(awk '/^---/{if(f){exit} d=""} {d=d $0 "\n"} /^kind: NetworkPolicy$/{f=1} END{if(f) printf "%s", d}' "$out")"
  egress="$(awk '/^  egress:$/{e=1;next} e&&/^  [^ ]/{e=0} e&&/^    - /{printf "\n"} e&&!/^ *#/{gsub(/^ +/,""); printf "%s|", $0}' <<<"$policy")"
  if [ "$(grep -c '^kind: NetworkPolicy$' "$out" || true)" != "1" ] \
     || ! awk '/^  podSelector:$/{f=1;next} f&&/^  [^ ]/{exit} f&&/app.kubernetes.io\/name: bp-stalwart-sovereign$/{ok=1} END{exit ok?0:1}' <<<"$policy" \
     || ! awk 'index($0, "- to:|- podSelector:|matchLabels:|")==1 && index($0, "app.kubernetes.io/name: bp-stalwart-sovereign|") && index($0, "app.kubernetes.io/instance: stalwart-sovereign|") && index($0, "ports:|- protocol: TCP|port: 8080|"){ok=1} END{exit ok?0:1}' <<<"$egress"; then
    echo "FAIL R7: the NetworkPolicy does not let the Job's pod reach the admin API (no egress rule to the chart's own pods on TCP 8080)"; fail=1
  fi

  # R8: the mirror switched off.
  local off
  off="$(mktemp)"
  if ! render "$dir" "$off" --set sovereignSMTPCredentialsMirror.enabled=false \
     || grep -q 'namespace: "\{0,1\}catalyst-system' "$off" \
     || [ "$(awk '/- name: MIRROR_ENABLED$/{getline; sub(/^ +/, ""); print; exit}' "$off")" != 'value: "false"' ]; then
    echo "FAIL R8: with the mirror switched off an object is still rendered into the mirror namespace, or the Job is not told"; fail=1
  fi
  rm -f "$off"

  behaviour "$out" || fail=1

  rm -f "$out"
  return "$fail"
}

self_test() {
  local base tmp rc=0
  base="$(cd "$here/.." && pwd)"
  if ! check "$base" > /dev/null; then echo "self-test: the chart itself fails"; check "$base" || true; return 1; fi
  mutate() { # <rule> <file under templates> <text that is there> <text that replaces it>
    tmp="$(mktemp -d)"; cp -r "$base" "$tmp/chart"
    python3 - "$tmp/chart/templates/$2" "$3" "$4" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
s = p.read_text()
if sys.argv[2] not in s:
    sys.exit("self-test: the text to change is not in %s: %s" % (p.name, sys.argv[2]))
p.write_text(s.replace(sys.argv[2], sys.argv[3], 1))
PY
    local got
    got="$(check "$tmp/chart" || true)"
    if grep -q "FAIL $1:" <<<"$got"; then echo "self-test: $1 fails as it should"; else echo "self-test: $1 did NOT fail"; rc=1; fi
    rm -rf "$tmp"
  }
  mutate R1 setup-job.yaml $'kind: Job\nmetadata:\n' $'kind: Job\nmetadata:\n  annotations:\n    "helm.sh/hook": post-install\n'
  mutate R2 setup-job.yaml '($pod | sha256sum | trunc 10)' '"fixed"'
  mutate R3 setup-job.yaml $'  activeDeadlineSeconds: {{ .Values.setupJob.activeDeadlineSeconds }}\n' ''
  mutate R4 setup-job.yaml '"SMTP_PASS": "${B64_PASS}"' '"OTHER": "${B64_PASS}"'
  mutate R5 setup-job.yaml 'log "principal ${SUBMISSION_USER} confirmed"' 'log "principal checked"'
  mutate R6 _helpers.tpl '  {{- $pwd = randAlphaNum 32 -}}' $'  {{- $m := lookup "v1" "Secret" "catalyst-system" "sovereign-smtp-credentials" -}}\n  {{- $pwd = randAlphaNum 32 -}}'
  mutate R7 networkpolicy.yaml $'    - to:\n        - podSelector:\n' $'    - to:\n        - namespaceSelector:\n'
  mutate R8 setup-job-rbac.yaml '{{- if $cfgEnabled }}' '{{- if true }}'
  mutate R9 setup-job.yaml "printf '%s' '587'" "printf '%s' '25'"
  mutate R10 setup-job.yaml '-X PUT "${K8S_API}' '-X PATCH "${K8S_API}'
  mutate R11 setup-job.yaml $'not confirmed (HTTP ${code}); the Job retries"\n            exit 1\n' $'not confirmed (HTTP ${code}); the Job retries"\n'
  mutate R12 setup-job.yaml 'for i in $(seq 1 60); do' 'for i in $(seq 1 600); do'
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi
chart="${1:-$here/..}"
chart="$(cd "$chart" && pwd)"
if check "$chart"; then
  echo "PASS: setup Job is bounded, not a hook, reaches the admin API, can run again, and takes no other party's password"
else
  exit 1
fi
