#!/usr/bin/env bash
# conformance-mailbox.sh - the setup Job of bp-stalwart-sovereign makes the mailbox that gate check BSS-G01
# reads: the principal conformance, address conformance@<fqdn>, password made in the Job and kept only in a
# Secret of the release namespace. Usage: bash conformance-mailbox.sh [<chart dir>] | --self-test
# M1 and M7 read the render; M2 to M6 run the Job's script from the render, with its env entries, against a
# stand-in for curl (no network, no cluster).
#   M1  In the release namespace the Job may read the submission and mailbox Secrets by name and create
#       a Secret, nothing else about Secrets.
#   M2  First run: Secret looked for, principal created with a generated password (24+ letters and digits)
#       and the one address, read back, then the Secret created with exactly address, user, password,
#       imap-host, imap-service and imap-port 993; exit 0; no password printed, no file left.
#   M3  Second run: the password is read from the API's compact answer, the principal set to it and to
#       the one address and read back, the Secret not written; exit 0.
#   M4  Two first runs on an empty state generate two different passwords.
#   M5  A refused principal read or write, a principal not read back and a refused Secret read or create
#       end the run with exit 1 and write no Secret; a stored Secret without a password writes no principal.
#   M6  With conformanceMailbox.user empty: no call about the mailbox, exit 0, no right to create a Secret.
#   M7  The server's file leaves sub-addressing as release v0.15.5 has it (on while the key is absent).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
helm="${HELM_BIN:-helm}"
fqdn=render-test.example.test
store=/api/v1/namespaces/stalwart-sovereign/secrets

render() { # <chart dir> <out file> [helm arguments]
  local tmp rc=0 src="$1" out="$2"
  shift 2
  tmp="$(mktemp -d)"
  cp -r "$src" "$tmp/chart"
  rm -rf "$tmp/chart/charts"
  awk '/^dependencies:/{skip=1;next} skip&&/^[^ #-]/{skip=0} !skip' "$src/Chart.yaml" > "$tmp/chart/Chart.yaml"
  "$helm" template stalwart-sovereign "$tmp/chart" --namespace stalwart-sovereign --set global.sovereignFQDN="$fqdn" "$@" > "$out" || rc=1
  rm -rf "$tmp"
  return "$rc"
}

secret_rules() { # <rendered file>: the rules about Secrets of the Role in the release namespace, sorted
  awk '/^---/{if (r != "") print r; r = ""; role = 0; inns = 0} /^kind: Role$/{role = 1}
    role && /^  namespace: "stalwart-sovereign"$/{inns = 1}
    role && inns && /^  - apiGroups:/{if (r != "") print r; r = ""}
    role && inns && /^    (resources|resourceNames|verbs):/{sub(/^    /, ""); r = r $0 " "}
    END{if (r != "") print r}' "$1" | grep '^resources: \["secrets"\]' | sort
}

prepare() { # <rendered file> <work dir>: the stand-in for curl, the Job's script and its env entries
  local w="$2"
  mkdir -p "$w/bin" "$w/sa"
  cat > "$w/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
# The mail server v0.15.5 and the API server; state under $FAKE, one line per call in $FAKE/calls.
method=GET; url=""; w=0; data=""; out=""
while [ $# -gt 0 ]; do
  case "$1" in -X) method="$2"; shift ;; -d) data="$2"; shift ;; -w) w=1; shift ;; -o) out="$2"; shift ;;
    -m|-u|-H|--cacert) shift ;; http*) url="$1" ;; esac
  shift
done
path="/${url#*://*/}"; body=""; [ -z "$data" ] || body="$(cat "${data#@}")"
answer() { if [ -n "$out" ] && [ "$out" != /dev/null ]; then printf '%s' "$1" > "$out"; fi; }
name="$(printf '%s' "$body" | sed -n 's/^{"name":"\([^"]*\)".*/\1/p')"
echo "$method $path${name:+ $name}" >> "$FAKE/calls"
mine=0; case "$path $name" in */conformance\ |*\ conformance) mine=1 ;; esac
code=200
case "$method $path" in
  "POST /api/principal")
    if [ "$mine" = 1 ] && [ -e "$FAKE/write-refused" ]; then answer '{"error":"other"}'
    else answer '{"data":3}'; if [ -n "$name" ] && { [ "$mine" = 0 ] || [ ! -e "$FAKE/lost" ]; }; then printf '%s' "$body" > "$FAKE/p-$name"; fi; fi ;;
  "GET /api/principal/"*)
    if [ "$mine" = 1 ] && [ -e "$FAKE/read-refused" ]; then code=500
    elif [ -e "$FAKE/p-${path##*/}" ]; then answer '{"data":{"id":3}}'
    else answer '{"error":"notFound"}'; fi ;;
  "PATCH /api/principal/"*)
    if [ "$mine" = 1 ] && [ -e "$FAKE/write-refused" ]; then answer '{"error":"other"}'
    else answer '{"data":null}'; printf '%s' "$body" > "$FAKE/patch-${path##*/}"; fi ;;
  "GET /api/v1/namespaces/"*"/services/"*) answer '{"spec":{"clusterIP": "10.43.0.25"}}' ;;
  "GET /api/v1/namespaces/catalyst-system/secrets/"*) code=404 ;;
  "POST /api/v1/namespaces/catalyst-system/secrets") code=201 ;;
  "GET /api/v1/namespaces/stalwart-sovereign/secrets/stalwart-sovereign-conformance")
    if [ -e "$FAKE/store-read" ]; then code="$(cat "$FAKE/store-read")"
    elif [ -e "$FAKE/store" ]; then
      p="$(sed -n 's/.*"password":"\([^"]*\)".*/\1/p' "$FAKE/store" | base64 -w0)"
      answer "{\"kind\":\"Secret\",\"metadata\":{\"managedFields\":[{\"fieldsV1\":{\"f:data\":{\"f:password\":{}}}}]},\"data\":{\"password\":\"$p\",\"user\":\"Y29uZm9ybWFuY2U=\"}}"
    else code=404; fi ;;
  "POST /api/v1/namespaces/stalwart-sovereign/secrets")
    if [ -e "$FAKE/store-write" ]; then code="$(cat "$FAKE/store-write")"
    elif [ -e "$FAKE/store" ]; then code=409
    else code=201; printf '%s' "$body" > "$FAKE/store"; fi ;;
esac
if [ "$w" = 1 ]; then printf '%s' "$code"; fi
FAKE_CURL
  printf '#!/bin/sh\nexit 0\n' > "$w/bin/sleep"
  chmod +x "$w/bin/curl" "$w/bin/sleep"
  printf 'standin-token' > "$w/sa/token"
  : > "$w/sa/ca.crt"
  awk '/^kind: Job$/{j=1} j&&/^            - \|$/{s=1;next} s&&/^---/{exit} s{sub(/^              /,""); print}' "$1" \
    | sed "s#/tmp/#$w/tmp/#g; s#/var/run/secrets/kubernetes.io/serviceaccount#$w/sa#g" > "$w/script.sh"
  awk '/^kind: Job$/{j=1} j&&/^---/{exit} j&&/^ +- name: /{n=$3; next} j&&/^ +value: /&&n!=""{v=$0; sub(/^ +value: /, "", v); gsub(/^"|"$/, "", v); print n "=" v} {n=""}' "$1" > "$w/env"
  printf '%s\n' ADMIN_PASSWORD=standin-admin-pw SUBMISSION_PASSWORD=standin-submission-pw >> "$w/env"
  grep -q '^set -eu$' "$w/script.sh"
}

run_job() { # <work dir> [file under fake/ to create]: a run on an empty state, or on the state kept with "keep"
  local w="$1" rc=0 envs
  if [ "${2:-}" != keep ]; then rm -rf "$w/fake" "$w/tmp"; mkdir -p "$w/fake" "$w/tmp"; fi
  if [ -n "${2:-}" ] && [ "$2" != keep ]; then printf '%s' "${3:-}" > "$w/fake/$2"; fi
  mapfile -t envs < "$w/env"
  rm -f "$w/fake/calls"
  ( cd "$w" && env -i PATH="$w/bin:$PATH" FAKE="$w/fake" "${envs[@]}" sh -ec "$(cat "$w/script.sh")" ) > "$w/out" 2>&1 || rc=$?
  touch "$w/fake/calls"
  echo "$rc"
}

mcalls() { grep -E 'conformance|stalwart-sovereign/secrets' "$1/fake/calls" | tr '\n' ',' || true; }
created() { sed -n 's/.*"secrets":\["\([^"]*\)"\].*/\1/p' "$1/fake/p-conformance" 2>/dev/null || true; }

behaviour() { # <rendered file> <render with conformanceMailbox.user empty>; one line per failed rule
  local w fail=0 rc n pw why first get="GET $store/stalwart-sovereign-conformance" want
  w="$(mktemp -d)"
  if ! prepare "$1" "$w"; then echo "FAIL M2: the Job's script cannot be read from the render"; rm -rf "$w"; return 1; fi

  rc="$(run_job "$w")"; pw="$(created "$w")"; why=""
  want="{\"apiVersion\":\"v1\",\"kind\":\"Secret\",\"metadata\":{\"name\":\"stalwart-sovereign-conformance\",\"labels\":{\"app.kubernetes.io/name\":\"bp-stalwart-sovereign\",\"catalyst.openova.io/component\":\"conformance-mailbox\"}},\"type\":\"Opaque\",\"stringData\":{\"address\":\"conformance@$fqdn\",\"user\":\"conformance\",\"password\":\"$pw\",\"imap-host\":\"mail.$fqdn\",\"imap-service\":\"stalwart-sovereign-bp-stalwart-sovereign-mail\",\"imap-port\":\"993\"}}"
  if [ "$rc" != 0 ]; then why="exit $rc: $(tail -1 "$w/out")"
  elif [ "$(mcalls "$w")" != "$get,GET /api/principal/conformance,POST /api/principal conformance,GET /api/principal/conformance,POST $store," ]; then why="calls $(mcalls "$w")"
  elif ! [[ "$pw" =~ ^[A-Za-z0-9]{24,}$ ]]; then why="the password is not 24 or more letters and digits"
  elif ! grep -q -F "\"emails\":[\"conformance@$fqdn\"]" "$w/fake/p-conformance"; then why="the principal has not the one address"
  elif [ "$(cat "$w/fake/store")" != "$want" ]; then why="the Secret is $(cat "$w/fake/store")"
  elif grep -q -F -e "$pw" -e standin-admin-pw "$w/out" || [ -n "$(ls -A "$w/tmp")" ]; then why="a password is printed or a file is left"
  fi
  if [ -n "$why" ]; then echo "FAIL M2: a first run does not create the principal, confirm it and then create its Secret ($why)"; fail=1; fi

  first="$(cat "$w/fake/store" 2>/dev/null || true)"
  rc="$(run_job "$w" keep)"
  if [ "$rc" != 0 ] || [ -z "$pw" ] || [ "$(cat "$w/fake/store" 2>/dev/null || true)" != "$first" ] \
     || [ "$(mcalls "$w")" != "$get,GET /api/principal/conformance,PATCH /api/principal/conformance,GET /api/principal/conformance," ] \
     || [ "$(cat "$w/fake/patch-conformance")" != "[{\"action\":\"set\",\"field\":\"secrets\",\"value\":[\"$pw\"]},{\"action\":\"set\",\"field\":\"emails\",\"value\":[\"conformance@$fqdn\"]}]" ]; then
    echo "FAIL M3: a second run does not set the principal to the stored password and address and leave the Secret (exit $rc, calls $(mcalls "$w"))"; fail=1
  fi

  rc="$(run_job "$w")"
  if [ "$rc" != 0 ] || [ -z "$(created "$w")" ] || [ "$(created "$w")" = "$pw" ]; then echo "FAIL M4: two first runs do not generate two different passwords"; fail=1; fi

  n=0
  for f in read-refused write-refused lost store-read=403 store-write=403; do
    rc="$(run_job "$w" "${f%=*}" "$(sed -n 's/.*=//p' <<<"$f")")"
    if [ "$rc" != 1 ] || [ -e "$w/fake/store" ]; then n=$((n+1)); fi
  done
  rc="$(run_job "$w" store '{"stringData":{"user":"conformance"}}')"
  if [ "$rc" != 1 ] || grep -q -E '^(POST|PATCH) /api/principal(/| )conformance' "$w/fake/calls"; then n=$((n+1)); fi
  if [ "$n" != 0 ]; then echo "FAIL M5: $n of 6 failures do not end the run with exit 1, or write a Secret or a principal"; fail=1; fi
  rm -rf "$w"

  w="$(mktemp -d)"
  if ! prepare "$2" "$w"; then echo "FAIL M6: the Job's script cannot be read from the render"; rm -rf "$w"; return 1; fi
  rc="$(run_job "$w")"
  if [ "$rc" != 0 ] || [ -n "$(mcalls "$w")" ] || grep -q -F 'verbs: ["create"]' <<<"$(secret_rules "$2")"; then
    echo "FAIL M6: with conformanceMailbox.user empty the Job makes a call about the mailbox or may create a Secret (exit $rc)"; fail=1
  fi
  rm -rf "$w"
  return "$fail"
}

check() { # <chart dir>; prints one line per failed rule
  local out off fail=0
  out="$(mktemp)"; off="$(mktemp)"
  if ! render "$1" "$out" || ! render "$1" "$off" --set conformanceMailbox.user=""; then
    echo "FAIL render: helm template failed"; rm -f "$out" "$off"; return 1
  fi
  if [ "$(secret_rules "$out" | tr '\n' ';')" != 'resources: ["secrets"] resourceNames: ["stalwart-sovereign-conformance"] verbs: ["get"] ;resources: ["secrets"] resourceNames: ["stalwart-sovereign-submission"] verbs: ["get"] ;resources: ["secrets"] verbs: ["create"] ;' ]; then
    echo "FAIL M1: the Job's rights on Secrets of the release namespace are not read by name and create: $(secret_rules "$out" | tr '\n' ';')"; fail=1
  fi
  if grep -q sub-addressing "$out"; then echo "FAIL M7: the server's file sets sub-addressing"; fail=1; fi
  behaviour "$out" "$off" || fail=1
  rm -f "$out" "$off"
  return "$fail"
}

self_test() {
  local base rc=0
  base="$(cd "$here/.." && pwd)"
  if ! check "$base" > /dev/null; then echo "self-test: the chart itself fails"; check "$base" || true; return 1; fi
  mutate() { # <rule> <file under the chart> <text that is there> <text that replaces it>
    local tmp s got
    tmp="$(mktemp -d)"; cp -r "$base" "$tmp/chart"
    s="$(cat "$tmp/chart/$2"; printf x)"; s="${s%x}"
    if [[ "$s" != *"$3"* ]]; then echo "self-test: the text to change is not in $2: $3"; rc=1; rm -rf "$tmp"; return; fi
    printf '%s' "${s/"$3"/"$4"}" > "$tmp/chart/$2"
    got="$(check "$tmp/chart" || true)"
    if grep -q "FAIL $1:" <<<"$got"; then echo "self-test: $1 fails as it should"; else echo "self-test: $1 did NOT fail"; rc=1; fi
    rm -rf "$tmp"
  }
  mutate M1 templates/setup-job-rbac.yaml $'    verbs: ["get"]\n  {{- end }}' $'    verbs: ["get", "update"]\n  {{- end }}'
  mutate M2 templates/setup-job.yaml 'value: {{ .Values.service.mail.ports.imaps | quote }}' 'value: {{ .Values.service.mail.ports.imap | quote }}'
  mutate M3 templates/setup-job.yaml 'if [ "${stored}" = "0" ]; then' 'if true; then'
  mutate M4 templates/setup-job.yaml "\$(head -c 64 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)" 'abcdefghijklmnopqrstuvwxyz0123'
  mutate M5 templates/setup-job.yaml 'not read back (HTTP ${code}); the Job retries"; exit 1; fi' 'not read back (HTTP ${code}); the Job retries"; fi'
  mutate M6 templates/setup-job.yaml 'if [ -n "${CONFORMANCE_USER:-}" ]; then' 'if true; then'
  mutate M7 templates/config-configmap.yaml '    [spam-filter]' $'    [session.rcpt]\n    sub-addressing = false\n\n    [spam-filter]'
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi
chart="$(cd "${1:-$here/..}" && pwd)"
if check "$chart"; then
  echo "PASS: the setup Job makes the mailbox of the acceptance gate and writes its Secret only after the server confirmed it"
else
  exit 1
fi
