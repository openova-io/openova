#!/usr/bin/env bash
# secrets-lib — render gate of the three library templates (SEC-200).
#
# A fixture chart in a temporary directory uses the library through a file
# dependency, the way a platform chart does, and renders in namespace acme:
#
#   R1  openova.secretStores: ServiceAccount eso-reader (no token mount) and
#       SecretStore openbao (mount secret, role eso-ns), exactly; no store
#       for mounted/;
#   R2  a mounted value (scope sa), one region: no Secret, ExternalSecret,
#       PushSecret or Password; the birth Job's plan holds exactly
#       acme/sa/wordpress/db-password/e1, this region only; the Job logs in
#       as secrets-birth with a projected token for audience openbao, ends on
#       exit 3; the chart renders no network policy (the policy chart owns them);
#   R3  a shared mounted value in the primary of two regions: reach "all",
#       the peer's OpenBao address and this region's id reach the Job;
#   R4  the same chart in the region that is not the primary: no Job for the
#       shared value; a value with sharedAcrossRegions false is written there
#       for that region only;
#   R5  a synced value: Password, PushSecret (IfNotExists) into openbao and,
#       in the primary, ClusterSecretStore vault-<peer>; ExternalSecret with
#       the register id; in the other region only the ExternalSecret;
#   R6  mountedCopies: the Job copies Secret <name> into
#       mounted/acme/sa/<sa>/<name>/e<epoch>, from a read-only Secret volume;
#   R7  raising an epoch renames the Job (a new run);
#   R8  openova.envFromFiles renders exactly the shim text, each exec
#       argument shell-quoted;
#   F1-F15 every refused input fails the render with its message.
#
# Tools: helm, grep, awk, diff. No yq: the acceptance job runs it as is.
set -euo pipefail

LIB="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

FX="$TMP/fx"
mkdir -p "$FX/templates"
cat > "$FX/Chart.yaml" <<EOF
apiVersion: v2
name: fx
version: 0.1.0
dependencies:
  - name: secrets-lib
    version: 0.1.0
    repository: "file://$LIB"
EOF
cat > "$FX/templates/all.yaml" <<'EOF'
{{- if .Values.stores }}{{ include "openova.secretStores" . }}{{ end }}
{{- include "openova.generatedSecrets" (dict "ctx" . "secrets" .Values.generatedSecrets "peers" .Values.peers "primary" .Values.primary "self" .Values.self) }}
EOF
cat > "$FX/templates/shim.yaml" <<'EOF'
{{- if .Values.shim }}
apiVersion: v1
kind: Pod
metadata:
  name: shim
spec:
  containers:
    - name: app
      image: busybox
      {{- include "openova.envFromFiles" .Values.shim | nindent 6 }}
{{- end }}
EOF
printf 'stores: false\ngeneratedSecrets: {}\npeers: []\n' > "$FX/values.yaml"
helm dependency build "$FX" >/dev/null

# r <values file> <out>: render the fixture in namespace acme.
r() { helm template fx "$FX" --namespace acme -f "$1" > "$2" 2> "$2.err"; }
# doc <file> <kind> <name>: the one document of that kind and name.
doc() {
  awk -v k="$2" -v n="$3" '
    function flush() { if (kind == k && name == n) printf "%s", buf; buf = ""; kind = ""; name = ""; meta = 0 }
    /^---/ { flush(); next }
    /^# Source: / { next }
    /^kind: / { kind = $2 }
    /^metadata:/ { meta = 1 }
    meta && /^  name: / && name == "" { name = $2 }
    { buf = buf $0 "\n" }
    END { flush() }' "$1"
}
kinds() { grep -E '^kind: ' "$1" | awk '{print $2}' | LC_ALL=C sort | uniq -c | awk '{print $2 "=" $1}' | tr '\n' ' '; }
# plan <file>: the plan lines of the birth ConfigMap.
plan() { awk '/^  plan: \|/ { on = 1; next } on && /^  [a-z]/ { on = 0 } on { sub(/^    /, ""); if ($0 != "") print }' "$1"; }
has() { doc "$1" "$2" "$3" > "$TMP/has.doc"; grep -qxF -- "$4" "$TMP/has.doc"; }
jobname() { grep -E '^  name: secrets-birth-[0-9a-f]{10}$' "$1" | head -n 1 | awk '{print $2}'; }

# ── R1 ──────────────────────────────────────────────────────────────────
printf 'stores: true\n' > "$TMP/r1.v"
r "$TMP/r1.v" "$TMP/r1.yaml"
cat > "$TMP/r1.want" <<'EOF'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eso-reader
  namespace: "acme"
  labels:
    app.kubernetes.io/component: eso-reader
    secrets.openova.io/library: secrets-lib
automountServiceAccountToken: false
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: openbao
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  provider:
    vault:
      server: "http://openbao.openbao.svc.cluster.local:8200"
      path: secret
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: eso-ns
          serviceAccountRef:
            name: eso-reader
EOF
{ doc "$TMP/r1.yaml" ServiceAccount eso-reader; doc "$TMP/r1.yaml" SecretStore openbao; } > "$TMP/r1.got"
if diff -u "$TMP/r1.want" "$TMP/r1.got" && [ "$(kinds "$TMP/r1.yaml")" = "SecretStore=1 ServiceAccount=1 " ]; then
  pass "R1 secretStores: eso-reader and SecretStore openbao exactly, nothing for mounted/"
else fail "R1 secretStores ($(kinds "$TMP/r1.yaml"))"; fi

# ── R2 ──────────────────────────────────────────────────────────────────
cat > "$TMP/r2.v" <<'EOF'
generatedSecrets:
  db-password:
    delivery: mounted
    epoch: 1
    serviceAccount: wordpress
EOF
r "$TMP/r2.v" "$TMP/r2.yaml"
J2="$(jobname "$TMP/r2.yaml")"
doc "$TMP/r2.yaml" Job "$J2" > "$TMP/r2.job"
r2=""
[ "$(kinds "$TMP/r2.yaml")" = "ConfigMap=1 Job=1 ServiceAccount=1 " ] || r2="$r2 kinds($(kinds "$TMP/r2.yaml"))"
[ "$(plan "$TMP/r2.yaml")" = "acme/sa/wordpress/db-password/e1 32 0 - self" ] || r2="$r2 plan($(plan "$TMP/r2.yaml"))"
for l in '      serviceAccountName: secrets-birth' '      automountServiceAccountToken: false' \
         '            - {name: BAO_ROLE, value: secrets-birth}' '            - {name: PEERS, value: ""}' \
         '                  audience: openbao' '          values: [3]' '      - action: FailJob' \
         '          args: ["/etc/secrets-birth/plan/birth.sh"]'; do
  grep -qxF -- "$l" "$TMP/r2.job" || r2="$r2 job-line[$l]"
done
has "$TMP/r2.yaml" ConfigMap "$J2" '    #!/bin/sh' || r2="$r2 script"
[ -z "$r2" ] && pass "R2 mounted, one region: only the birth Job (plan acme/sa/wordpress/db-password/e1 self), no Secret, ExternalSecret or PushSecret" || fail "R2:$r2"

# ── R3 / R4 ─────────────────────────────────────────────────────────────
cat > "$TMP/r3.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  token:
    delivery: mounted
    epoch: 2
    length: 40
    symbols: 4
    scope: shared
  local-key:
    delivery: mounted
    epoch: 1
    scope: shared
    sharedAcrossRegions: false
EOF
r "$TMP/r3.v" "$TMP/r3.yaml"
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r3.v" > "$TMP/r4.v"
r "$TMP/r4.v" "$TMP/r4.yaml"
J3="$(jobname "$TMP/r3.yaml")"
r3=""
[ "$(plan "$TMP/r3.yaml")" = "$(printf 'acme/shared/local-key/e1 32 0 - self\nacme/shared/token/e2 40 4 - all')" ] || r3="$r3 plan($(plan "$TMP/r3.yaml" | tr '\n' ';'))"
has "$TMP/r3.yaml" Job "$J3" '            - {name: PEERS, value: "hw-b=http://openbao-hw-b.openbao.svc.cluster.local:8200"}' || r3="$r3 peers-env"
has "$TMP/r3.yaml" Job "$J3" '            - {name: SELF_REGION, value: "hw-a"}' || r3="$r3 self-env"
has "$TMP/r3.yaml" Job "$J3" '            - {name: PEER_ROLE, value: "secrets-birth-acme"}' || r3="$r3 peer-role"
has "$TMP/r3.yaml" Job "$J3" '            - {name: PEER_TOKEN_ROLE, value: "openbao-peer-birth-acme"}' || r3="$r3 peer-token-role"
[ -z "$r3" ] && pass "R3 primary of two regions: the shared value reaches all, the peer's address and this region's id reach the Job" || fail "R3:$r3"
r4=""
[ "$(plan "$TMP/r4.yaml")" = "acme/shared/local-key/e1 32 0 - self" ] || r4="$r4 plan($(plan "$TMP/r4.yaml" | tr '\n' ';'))"
grep -q 'acme/shared/token' "$TMP/r4.yaml" && r4="$r4 shared-value-rendered"
[ -z "$r4" ] && pass "R4 the other region: nothing for the shared value, its own value for itself only" || fail "R4:$r4"
printf 'self: hw-b\npeers: [hw-a]\nprimary: false\ngeneratedSecrets:\n  token: {delivery: mounted, epoch: 1, scope: shared}\n' > "$TMP/r4b.v"
r "$TMP/r4b.v" "$TMP/r4b.yaml"
[ "$(kinds "$TMP/r4b.yaml")" = "" ] && pass "R4 the other region with only a shared value renders no Job at all" || fail "R4 no-job ($(kinds "$TMP/r4b.yaml"))"

# ── R5 ──────────────────────────────────────────────────────────────────
cat > "$TMP/r5.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  api-key:
    delivery: synced
    registerId: reg-api-key
    epoch: 3
EOF
r "$TMP/r5.v" "$TMP/r5.yaml"
cat > "$TMP/r5.want" <<'EOF'
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: api-key-e3
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  length: 32
  digits: 6
  symbols: 0
  noUpper: false
  allowRepeat: true
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata:
  name: api-key-e3
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
spec:
  refreshInterval: 1h
  updatePolicy: IfNotExists
  deletionPolicy: Delete
  secretStoreRefs:
    - kind: SecretStore
      name: openbao
    - kind: ClusterSecretStore
      name: vault-hw-b
  selector:
    generatorRef:
      apiVersion: generators.external-secrets.io/v1alpha1
      kind: Password
      name: api-key-e3
  data:
    - match:
        secretKey: password
        remoteRef:
          remoteKey: openova/apps/acme/api-key/e3
          property: password
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: api-key
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "reg-api-key"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: api-key
    creationPolicy: Owner
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/acme/api-key/e3
        property: password
EOF
{ doc "$TMP/r5.yaml" Password api-key-e3; doc "$TMP/r5.yaml" PushSecret api-key-e3; doc "$TMP/r5.yaml" ExternalSecret api-key; } > "$TMP/r5.got"
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r5.v" > "$TMP/r5b.v"
r "$TMP/r5b.v" "$TMP/r5b.yaml"
if diff -u "$TMP/r5.want" "$TMP/r5.got" && [ "$(kinds "$TMP/r5.yaml")" = "ExternalSecret=1 Password=1 PushSecret=1 " ] \
   && [ "$(kinds "$TMP/r5b.yaml")" = "ExternalSecret=1 " ]; then
  pass "R5 synced: Password, PushSecret into openbao + vault-hw-b, ExternalSecret with its register id; the other region reads only"
else fail "R5 synced ($(kinds "$TMP/r5.yaml") / $(kinds "$TMP/r5b.yaml"))"; fi

# ── R6 / R7 ─────────────────────────────────────────────────────────────
cat > "$TMP/r6.v" <<'EOF'
generatedSecrets:
  gitea-webhook-token:
    delivery: synced
    registerId: reg-webhook
    epoch: 1
    mountedCopies:
      - serviceAccount: organization-controller
EOF
r "$TMP/r6.v" "$TMP/r6.yaml"
J6="$(jobname "$TMP/r6.yaml")"
r6=""
[ "$(plan "$TMP/r6.yaml")" = "acme/sa/organization-controller/gitea-webhook-token/e1 0 0 /etc/secrets-birth/copy-0/password self" ] || r6="$r6 plan($(plan "$TMP/r6.yaml"))"
has "$TMP/r6.yaml" Job "$J6" '            secretName: gitea-webhook-token' || r6="$r6 volume"
has "$TMP/r6.yaml" Job "$J6" '            - {name: copy-0, mountPath: /etc/secrets-birth/copy-0, readOnly: true}' || r6="$r6 mount"
[ "$(grep -c '^kind: PushSecret' "$TMP/r6.yaml")" = 1 ] || r6="$r6 pushsecrets"
[ -z "$r6" ] && pass "R6 mountedCopies: the Job copies Secret gitea-webhook-token to mounted/, no second PushSecret" || fail "R6:$r6"
sed 's/epoch: 1/epoch: 2/' "$TMP/r2.v" > "$TMP/r7.v"
r "$TMP/r7.v" "$TMP/r7.yaml"
J7="$(jobname "$TMP/r7.yaml")"
[ -n "$J2" ] && [ -n "$J7" ] && [ "$J2" != "$J7" ] && pass "R7 raising the epoch renames the Job ($J2 -> $J7)" || fail "R7 job names '$J2' '$J7'"

# ── R8 ──────────────────────────────────────────────────────────────────
cat > "$TMP/r8.v" <<'EOF'
shim:
  map:
    PGPASSWORD: db-password
    DB_USER: db-user
  exec: ["psql", "-c", "select 'ok'"]
EOF
r "$TMP/r8.v" "$TMP/r8.yaml"
cat > "$TMP/r8.want" <<'EOF'
      command: ["/bin/sh", "-c"]
      args:
        - |
          set -eu
          while IFS='=' read -r var file; do
            [ -n "$var" ] || continue
            [ -r "/var/run/secrets/openova/$file" ] || { echo "openova: secret file $file is not mounted" >&2; exit 78; }
            export "$var=$(cat "/var/run/secrets/openova/$file")"
          done <<'OPENOVA_ENV_MAP'
          DB_USER=db-user
          PGPASSWORD=db-password
          OPENOVA_ENV_MAP
          exec 'psql' '-c' 'select '\''ok'\'''
EOF
awk '/^      command: /{on=1} on' "$TMP/r8.yaml" > "$TMP/r8.got"
diff -u "$TMP/r8.want" "$TMP/r8.got" && pass "R8 envFromFiles: the exact shim text, arguments shell-quoted" || fail "R8 shim text"

# ── F1-F15 ──────────────────────────────────────────────────────────────
# refused <id> <expected message part> <values>
refused() {
  printf '%s\n' "$3" > "$TMP/$1.v"
  if r "$TMP/$1.v" "$TMP/$1.yaml"; then fail "$1 rendered: $2"
  elif grep -qF -- "$2" "$TMP/$1.yaml.err"; then pass "$1 refused: $2"
  else fail "$1 refused with another message: $(tr '\n' ' ' < "$TMP/$1.yaml.err")"; fi
}
refused F1  "delivery is required"                 'generatedSecrets: {x: {epoch: 1}}'
refused F2  "is neither mounted nor synced"        'generatedSecrets: {x: {delivery: file, epoch: 1}}'
refused F3  "registerId is required for delivery synced" 'generatedSecrets: {x: {delivery: synced, epoch: 1}}'
refused F4  "registerId is only for delivery synced"     'generatedSecrets: {x: {delivery: mounted, registerId: r, epoch: 1, serviceAccount: s}}'
refused F5  "serviceAccount is required for scope sa"    'generatedSecrets: {x: {delivery: mounted, epoch: 1}}'
refused F6  "mountedCopies is only for delivery synced"  'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, mountedCopies: [{serviceAccount: s}]}}'
refused F7  "epoch must be 1 or more"              'generatedSecrets: {x: {delivery: mounted, epoch: 0, scope: shared}}'
refused F8  "epoch is required"                    'generatedSecrets: {x: {delivery: mounted, scope: shared}}'
refused F9  "is neither sa nor shared"             'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: pod}}'
refused F10 "the name must match"                  'generatedSecrets: {Db_Password: {delivery: mounted, epoch: 1, scope: shared}}'
refused F11 "self (or openova.topology.self) is required" "$(printf 'peers: [hw-b]\nprimary: true\ngeneratedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared}}')"
refused F12 "primary must be true or false"        "$(printf 'peers: [hw-b]\ngeneratedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared}}')"
refused F13 "symbols between 0 and length"         'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, length: 4, symbols: 5}}'
refused F14 "map is empty"                         'shim: {map: {}, exec: [a]}'
refused F15 "exec is empty"                        'shim: {map: {A: a}, exec: []}'

if [ "$FAIL" -ne 0 ]; then echo "FAIL: secrets-lib render"; exit 1; fi
echo "PASS: secrets-lib render — stores, generated secrets, birth Job, shim"
