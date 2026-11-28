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
#   R9  a named key and a placeholder (SEC-201): the plan line carries the
#       key as a sixth field and a placeholder as source "empty", length and
#       symbols 0; key password renders the line of a value without the new
#       fields; a shared placeholder in the primary still reaches the peers;
#   R10 the namespace input (SEC-217), both cases of one chart (stores, a
#       mounted value, a synced value with a copy, a shared value of the
#       primary): without it (or null) every object is in namespace acme and every
#       path of the birth Job's plan is below acme/; with namespace
#       org-services every object, path, remote key and peer role names
#       org-services and nothing names acme. In both the Job's namespace is
#       the first part of every path it writes: a birth Job in one namespace
#       is never given a path of another unless the input names it;
#   R11 a template (SEC-219), the role Secret of a database operator in
#       namespace postgres: target.template with type kubernetes.io/basic-auth,
#       label cnpg.io/reload "true" and keys username and password, exactly;
#   R12 copies (SEC-219): one ExternalSecret per copy, by name, in the value's
#       namespace (a copy that names that namespace too), each of the same
#       remote key and register id, with its own template or none;
#   R13 join (SEC-219): one Secret carries two values, each under its own
#       key; the joined value is born (Password, PushSecret) but renders no
#       ExternalSecret; a copy carries both; the other region reads only;
#   R14 seedFrom (SEC-226): each plan line of a seeded value has source
#       seed:<Secret>/<key>; Role secrets-birth-seed grants get on exactly the
#       Secrets of this region's plan, bound to secrets-birth; the Job gets an
#       API token (no audience) and the cluster CA; a plan without a seed
#       renders none of it;
#   R15 source external (SEC-228), primary of two regions and the other
#       region: only ExternalSecrets (no Password, PushSecret or Job), each
#       reading openova/apps/<ns>/<name>/e<epoch> with its template and the
#       joined external value, annotated with the writer, exactly; a
#       generated value of the same chart renders as alone;
#   R16 source generated renders the value without source, byte for byte;
#   F1-F66 every refused input fails the render with its message; F33 is a
#       copy in another namespace, F45 to F51 the seedFrom of SEC-226, F52
#       to F66 the source external of SEC-228.
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
# The input namespace is given only when the values hold the key: every other
# case calls the library as a chart that does not know it (the default).
cat > "$FX/templates/all.yaml" <<'EOF'
{{- $in := dict "ctx" . "secrets" .Values.generatedSecrets "peers" .Values.peers "primary" .Values.primary "self" .Values.self }}
{{- if hasKey .Values "namespace" }}{{ $_ := set $in "namespace" .Values.namespace }}{{ end }}
{{- if .Values.stores }}{{ include "openova.secretStores" (ternary (dict "ctx" . "namespace" .Values.namespace) . (hasKey .Values "namespace")) }}{{ end }}
{{- include "openova.generatedSecrets" $in }}
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

# ── R9 ──────────────────────────────────────────────────────────────────
cat > "$TMP/r9.v" <<'EOF'
generatedSecrets:
  model-credential:
    delivery: mounted
    epoch: 1
    serviceAccount: axon
    sharedAcrossRegions: false
    key: document
    placeholder: true
  api:
    delivery: mounted
    epoch: 1
    serviceAccount: app
    key: token
    length: 24
  db-password:
    delivery: mounted
    epoch: 1
    serviceAccount: wordpress
    key: password
    placeholder: false
EOF
r "$TMP/r9.v" "$TMP/r9.yaml"
cat > "$TMP/r9.want" <<'EOF'
acme/sa/app/api/e1 24 0 - self token
acme/sa/wordpress/db-password/e1 32 0 - self
acme/sa/axon/model-credential/e1 0 0 empty self document
EOF
plan "$TMP/r9.yaml" > "$TMP/r9.got"
r9=""
diff -u "$TMP/r9.want" "$TMP/r9.got" || r9="$r9 plan"
[ "$(kinds "$TMP/r9.yaml")" = "ConfigMap=1 Job=1 ServiceAccount=1 " ] || r9="$r9 kinds($(kinds "$TMP/r9.yaml"))"
printf 'self: hw-a\npeers: [hw-b]\nprimary: true\ngeneratedSecrets:\n  seed: {delivery: mounted, epoch: 1, scope: shared, key: document, placeholder: true}\n' > "$TMP/r9b.v"
r "$TMP/r9b.v" "$TMP/r9b.yaml"
J9="$(jobname "$TMP/r9b.yaml")"
[ "$(plan "$TMP/r9b.yaml")" = "acme/shared/seed/e1 0 0 empty all document" ] || r9="$r9 shared-plan($(plan "$TMP/r9b.yaml"))"
has "$TMP/r9b.yaml" Job "$J9" '            - {name: PEERS, value: "hw-b=http://openbao-hw-b.openbao.svc.cluster.local:8200"}' || r9="$r9 shared-peers-env"
[ -z "$r9" ] && pass "R9 named key and placeholder: key as sixth field, placeholder as 'empty' 0 0, key password as before, a shared placeholder reaches the peers" || fail "R9:$r9"

# ── R10 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r10a.v" <<'EOF'
stores: true
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  auth-signing:
    delivery: mounted
    epoch: 1
    serviceAccount: auth
  api-key:
    delivery: synced
    registerId: reg-api-key
    epoch: 1
    mountedCopies:
      - serviceAccount: auth
  token:
    delivery: mounted
    epoch: 1
    scope: shared
EOF
{ cat "$TMP/r10a.v"; printf 'namespace: org-services\n'; } > "$TMP/r10b.v"
r10=""
r "$TMP/r10a.v" "$TMP/r10a.yaml" || r10="$r10 a-render($(tr '\n' ' ' < "$TMP/r10a.yaml.err"))"
r "$TMP/r10b.v" "$TMP/r10b.yaml" || r10="$r10 b-render($(tr '\n' ' ' < "$TMP/r10b.yaml.err"))"
# nslines <file>: per distinct namespace line, the count of documents and the namespace.
nslines() { grep -E '^  namespace: ' "$1" | LC_ALL=C sort | uniq -c | awk '{print $1 " " $3 ";"}' | tr -d '\n'; }
# heads <file>: the first part of every path of the plan, each once.
heads() { plan "$1" | awk -F/ '{print $1}' | LC_ALL=C sort -u | tr '\n' ' '; }
# jobns <file> <job>: the namespace of the birth Job.
jobns() { doc "$1" Job "$2" | awk '/^  namespace: / && !n { print $2; n = 1 }'; }
# Each measure is taken once; on a failed (empty) render it is empty and fails below.
KA="$(kinds "$TMP/r10a.yaml" || true)"; KB="$(kinds "$TMP/r10b.yaml" || true)"
NA="$(nslines "$TMP/r10a.yaml" || true)"; NB="$(nslines "$TMP/r10b.yaml" || true)"
JA="$(jobname "$TMP/r10a.yaml" || true)"; JB="$(jobname "$TMP/r10b.yaml" || true)"
JNA="$(jobns "$TMP/r10a.yaml" "$JA" || true)"; JNB="$(jobns "$TMP/r10b.yaml" "$JB" || true)"
HA="$(heads "$TMP/r10a.yaml" || true)"; HB="$(heads "$TMP/r10b.yaml" || true)"
[ "$KA" = "ConfigMap=1 ExternalSecret=1 Job=1 Password=1 PushSecret=1 SecretStore=1 ServiceAccount=2 " ] || r10="$r10 a-kinds($KA)"
[ "$NA" = '8 "acme";' ] || r10="$r10 a-namespaces($NA)"
cat > "$TMP/r10a.want" <<'EOF'
acme/sa/auth/api-key/e1 0 0 /etc/secrets-birth/copy-0/password self
acme/sa/auth/auth-signing/e1 32 0 - all
acme/shared/token/e1 32 0 - all
EOF
plan "$TMP/r10a.yaml" > "$TMP/r10a.got"
diff -u "$TMP/r10a.want" "$TMP/r10a.got" || r10="$r10 a-plan"
[ "$JNA" = '"acme"' ] && [ "$HA" = 'acme ' ] || r10="$r10 a-job-writes-another($JNA / $HA)"
has "$TMP/r10a.yaml" Job "$JA" '            - {name: PEER_ROLE, value: "secrets-birth-acme"}' || r10="$r10 a-peer-role"
[ "$KB" = "ConfigMap=1 ExternalSecret=1 Job=1 Password=1 PushSecret=1 SecretStore=1 ServiceAccount=2 " ] || r10="$r10 b-kinds($KB)"
[ "$NB" = '8 "org-services";' ] || r10="$r10 b-namespaces($NB)"
cat > "$TMP/r10b.want" <<'EOF'
org-services/sa/auth/api-key/e1 0 0 /etc/secrets-birth/copy-0/password self
org-services/sa/auth/auth-signing/e1 32 0 - all
org-services/shared/token/e1 32 0 - all
EOF
plan "$TMP/r10b.yaml" > "$TMP/r10b.got"
diff -u "$TMP/r10b.want" "$TMP/r10b.got" || r10="$r10 b-plan"
[ "$JNB" = '"org-services"' ] && [ "$HB" = 'org-services ' ] || r10="$r10 b-job-writes-another($JNB / $HB)"
has "$TMP/r10b.yaml" Job "$JB" '            - {name: PEER_ROLE, value: "secrets-birth-org-services"}' || r10="$r10 b-peer-role"
has "$TMP/r10b.yaml" Job "$JB" '            - {name: PEER_TOKEN_ROLE, value: "openbao-peer-birth-org-services"}' || r10="$r10 b-peer-token-role"
has "$TMP/r10b.yaml" PushSecret api-key-e1 '          remoteKey: openova/apps/org-services/api-key/e1' || r10="$r10 b-remote-key"
has "$TMP/r10b.yaml" ExternalSecret api-key '        key: openova/apps/org-services/api-key/e1' || r10="$r10 b-external-key"
[ -s "$TMP/r10b.yaml" ] && ! grep -q acme "$TMP/r10b.yaml" || r10="$r10 b-names-acme"
[ -n "$JB" ] && [ "$JB" != "$JA" ] || r10="$r10 same-job-name"
# The input given as null (a values key that is not set) is the default.
{ cat "$TMP/r10a.v"; printf 'namespace: null\n'; } > "$TMP/r10c.v"
r "$TMP/r10c.v" "$TMP/r10c.yaml" || r10="$r10 c-render($(tr '\n' ' ' < "$TMP/r10c.yaml.err"))"
plan "$TMP/r10c.yaml" > "$TMP/r10c.got"
NC="$(nslines "$TMP/r10c.yaml" || true)"
diff -u "$TMP/r10a.want" "$TMP/r10c.got" && [ "$NC" = '8 "acme";' ] || r10="$r10 c-null-not-default($NC)"
[ -z "$r10" ] && pass "R10 namespace input: absent or null, all 8 objects and every plan path in acme; org-services, all 8 objects, paths, keys and peer roles in org-services, nothing names acme; the Job writes only below its own namespace" || fail "R10:$r10"

# ── R11 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r11.v" <<'EOF'
namespace: postgres
generatedSecrets:
  shared-pg-harbor:
    delivery: synced
    registerId: shared-pg-role-passwords
    epoch: 1
    template:
      type: kubernetes.io/basic-auth
      labels:
        cnpg.io/reload: "true"
      data:
        username: harbor
        password: "{{ .password }}"
EOF
cat > "$TMP/r11.want" <<'EOF'
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: shared-pg-harbor
  namespace: "postgres"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "shared-pg-role-passwords"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: shared-pg-harbor
    creationPolicy: Owner
    template:
      engineVersion: v2
      type: "kubernetes.io/basic-auth"
      metadata:
        labels:
          "cnpg.io/reload": "true"
      data:
        "password": "{{ .password }}"
        "username": "harbor"
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/postgres/shared-pg-harbor/e1
        property: password
EOF
r11=""
r "$TMP/r11.v" "$TMP/r11.yaml" || r11="$r11 render($(tr '\n' ' ' < "$TMP/r11.yaml.err"))"
doc "$TMP/r11.yaml" ExternalSecret shared-pg-harbor > "$TMP/r11.got"
diff -u "$TMP/r11.want" "$TMP/r11.got" || r11="$r11 external-secret"
K11="$(kinds "$TMP/r11.yaml" || true)"
[ "$K11" = "ExternalSecret=1 Password=1 PushSecret=1 " ] || r11="$r11 kinds($K11)"
[ -z "$r11" ] && pass "R11 template: the role Secret in postgres, type kubernetes.io/basic-auth, label cnpg.io/reload, keys username and password" || fail "R11:$r11"

# ── R12 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r12.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  app-owner:
    delivery: synced
    registerId: reg-app
    epoch: 2
    copies:
      - name: app-db
        template:
          type: Opaque
          labels:
            catalyst.openova.io/binding-secret: app
          annotations:
            helm.sh/resource-policy: keep
          data:
            password: "{{ .password }}"
            uri: "postgresql://app:{{ .password }}@pg-rw.acme.svc.cluster.local:5432/app"
      - name: app-db-x-mgmt-x-mgmt-vcluster
        namespace: acme
EOF
cat > "$TMP/r12.want" <<'EOF'
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: "app-db"
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "reg-app"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: "app-db"
    creationPolicy: Owner
    template:
      engineVersion: v2
      type: "Opaque"
      metadata:
        labels:
          "catalyst.openova.io/binding-secret": "app"
        annotations:
          "helm.sh/resource-policy": "keep"
      data:
        "password": "{{ .password }}"
        "uri": "postgresql://app:{{ .password }}@pg-rw.acme.svc.cluster.local:5432/app"
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/acme/app-owner/e2
        property: password
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: "app-db-x-mgmt-x-mgmt-vcluster"
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "reg-app"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: "app-db-x-mgmt-x-mgmt-vcluster"
    creationPolicy: Owner
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/acme/app-owner/e2
        property: password
EOF
r12=""
r "$TMP/r12.v" "$TMP/r12.yaml" || r12="$r12 render($(tr '\n' ' ' < "$TMP/r12.yaml.err"))"
{ doc "$TMP/r12.yaml" ExternalSecret '"app-db"'; doc "$TMP/r12.yaml" ExternalSecret '"app-db-x-mgmt-x-mgmt-vcluster"'; } > "$TMP/r12.got"
diff -u "$TMP/r12.want" "$TMP/r12.got" || r12="$r12 copies"
K12="$(kinds "$TMP/r12.yaml" || true)"
[ "$K12" = "ExternalSecret=3 Password=1 PushSecret=1 " ] || r12="$r12 kinds($K12)"
has "$TMP/r12.yaml" ExternalSecret app-owner '    name: app-owner' || r12="$r12 own-target"
[ "$(grep -c -x -F '  namespace: "acme"' "$TMP/r12.yaml")" = 5 ] || r12="$r12 namespaces"
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r12.v" > "$TMP/r12b.v"
r "$TMP/r12b.v" "$TMP/r12b.yaml" || r12="$r12 b-render"
K12B="$(kinds "$TMP/r12b.yaml" || true)"
[ "$K12B" = "ExternalSecret=3 " ] || r12="$r12 b-kinds($K12B)"
[ -z "$r12" ] && pass "R12 copies: ExternalSecrets app-db (own template) and app-db-x-mgmt-x-mgmt-vcluster (namespace acme named) beside app-owner, all in acme, one remote key and register id; the other region reads all three" || fail "R12:$r12"

# ── R13 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r13.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  powerdns-api-credentials:
    delivery: synced
    registerId: reg-pdns
    epoch: 1
    join: [webserver-password]
    template:
      data:
        api-key: "{{ .password }}"
        webserver-password: '{{ index . "webserver-password" }}'
    copies:
      - name: pdns-zone-bootstrap
  webserver-password:
    delivery: synced
    epoch: 2
    length: 24
EOF
cat > "$TMP/r13.want" <<'EOF'
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: powerdns-api-credentials
  namespace: "acme"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "reg-pdns"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: powerdns-api-credentials
    creationPolicy: Owner
    template:
      engineVersion: v2
      data:
        "api-key": "{{ .password }}"
        "webserver-password": "{{ index . \"webserver-password\" }}"
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/acme/powerdns-api-credentials/e1
        property: password
    - secretKey: webserver-password
      remoteRef:
        key: openova/apps/acme/webserver-password/e2
        property: password
EOF
r13=""
r "$TMP/r13.v" "$TMP/r13.yaml" || r13="$r13 render($(tr '\n' ' ' < "$TMP/r13.yaml.err"))"
doc "$TMP/r13.yaml" ExternalSecret powerdns-api-credentials > "$TMP/r13.got"
diff -u "$TMP/r13.want" "$TMP/r13.got" || r13="$r13 joined-secret"
K13="$(kinds "$TMP/r13.yaml" || true)"
[ "$K13" = "ExternalSecret=2 Password=2 PushSecret=2 " ] || r13="$r13 kinds($K13)"
has "$TMP/r13.yaml" PushSecret webserver-password-e2 '          remoteKey: openova/apps/acme/webserver-password/e2' || r13="$r13 joined-born"
has "$TMP/r13.yaml" Password webserver-password-e2 '  length: 24' || r13="$r13 joined-length"
[ -z "$(doc "$TMP/r13.yaml" ExternalSecret webserver-password)" ] || r13="$r13 joined-has-own-secret"
doc "$TMP/r13.yaml" ExternalSecret '"pdns-zone-bootstrap"' > "$TMP/r13.copy"
[ "$(grep -c -x -E '    - secretKey: (password|webserver-password)' "$TMP/r13.copy")" = 2 ] || r13="$r13 copy-carries-both"
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r13.v" > "$TMP/r13b.v"
r "$TMP/r13b.v" "$TMP/r13b.yaml" || r13="$r13 b-render"
K13B="$(kinds "$TMP/r13b.yaml" || true)"
[ "$K13B" = "ExternalSecret=2 " ] || r13="$r13 b-kinds($K13B)"
doc "$TMP/r13b.yaml" ExternalSecret powerdns-api-credentials > "$TMP/r13b.got"
diff -u "$TMP/r13.want" "$TMP/r13b.got" || r13="$r13 b-joined-secret"
[ -z "$r13" ] && pass "R13 join: Secret powerdns-api-credentials carries both values under api-key and webserver-password; webserver-password is born but has no Secret; the copy carries both; the other region reads only" || fail "R13:$r13"

# ── R14 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r14.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
generatedSecrets:
  master-key:
    delivery: mounted
    epoch: 1
    serviceAccount: chargeback
    sharedAcrossRegions: false
    key: key
    length: 43
    seedFrom: {name: chargeback-master-key, key: master-key}
  again:
    delivery: mounted
    epoch: 1
    scope: shared
    sharedAcrossRegions: false
    seedFrom: {name: chargeback-master-key, key: other}
  signing:
    delivery: mounted
    epoch: 2
    scope: shared
    seedFrom: {name: old.signing, key: tls.key}
  fresh:
    delivery: mounted
    epoch: 1
    scope: shared
    sharedAcrossRegions: false
EOF
cat > "$TMP/r14.want" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: secrets-birth-seed
  namespace: "acme"
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: ["chargeback-master-key","old.signing"]
    verbs: [get]
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: secrets-birth-seed
  namespace: "acme"
  labels:
    app.kubernetes.io/name: secrets-birth
    secrets.openova.io/library: secrets-lib
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: secrets-birth-seed
subjects:
  - kind: ServiceAccount
    name: secrets-birth
    namespace: "acme"
EOF
r14=""
r "$TMP/r14.v" "$TMP/r14.yaml" || r14="$r14 render($(tr '\n' ' ' < "$TMP/r14.yaml.err"))"
J14="$(jobname "$TMP/r14.yaml")"
[ "$(plan "$TMP/r14.yaml")" = "$(printf '%s\n' 'acme/shared/again/e1 32 0 seed:chargeback-master-key/other self' 'acme/shared/fresh/e1 32 0 - self' \
  'acme/sa/chargeback/master-key/e1 43 0 seed:chargeback-master-key/master-key self key' 'acme/shared/signing/e2 32 0 seed:old.signing/tls.key all')" ] \
  || r14="$r14 plan($(plan "$TMP/r14.yaml" | tr '\n' ';'))"
{ doc "$TMP/r14.yaml" Role secrets-birth-seed; doc "$TMP/r14.yaml" RoleBinding secrets-birth-seed; } > "$TMP/r14.got"
diff -u "$TMP/r14.want" "$TMP/r14.got" || r14="$r14 role"
[ "$(kinds "$TMP/r14.yaml")" = "ConfigMap=1 Job=1 Role=1 RoleBinding=1 ServiceAccount=1 " ] || r14="$r14 kinds($(kinds "$TMP/r14.yaml"))"
doc "$TMP/r14.yaml" Job "$J14" > "$TMP/r14.job"
for l in '            - {name: KUBE_API, value: "https://kubernetes.default.svc"}' '            - {name: KUBE_NAMESPACE, value: "acme"}' \
         '            - {name: KUBE_DIR, value: /var/run/secrets/openova-birth-kube}' \
         '            - {name: kube, mountPath: /var/run/secrets/openova-birth-kube, readOnly: true}' \
         '                  name: kube-root-ca.crt' '                    - {key: ca.crt, path: ca.crt}' '                  path: token'; do
  grep -qxF -- "$l" "$TMP/r14.job" || r14="$r14 job-line[$l]"
done
# The API token has the API's audience: the one audience line is OpenBao's.
[ "$(grep -c 'audience:' "$TMP/r14.job")" = 1 ] || r14="$r14 audiences"
has "$TMP/r14.yaml" ConfigMap "$J14" '    seed_read() {' || r14="$r14 script"
# A plan without a seed renders none of it (R2): no Role, no API token, no seed functions.
grep -qE 'seed_read|KUBE_|secrets-birth-seed|kube-root-ca' "$TMP/r2.yaml" && r14="$r14 seedless-plan-has-seed-parts"
# The other region: its plan holds no shared value, so its Role names only its own seed.
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r14.v" > "$TMP/r14b.v"
r "$TMP/r14b.v" "$TMP/r14b.yaml" || r14="$r14 b-render"
has "$TMP/r14b.yaml" Role secrets-birth-seed '    resourceNames: ["chargeback-master-key"]' || r14="$r14 b-role"
[ -z "$r14" ] && pass "R14 seedFrom (SEC-226): plan sources seed:<Secret>/<key>; Role secrets-birth-seed (get on chargeback-master-key and old.signing only) bound to secrets-birth; the Job's API token and CA; the other region's Role names only its own seed; no seed, none of it" || fail "R14:$r14"

# ── R15 ─────────────────────────────────────────────────────────────────
cat > "$TMP/r15.v" <<'EOF'
self: hw-a
peers: [hw-b]
primary: true
namespace: velero
generatedSecrets:
  velero-objectstorage-credentials:
    delivery: synced
    source: external
    registerId: velero-objectstorage-credentials
    writer: "the hand-over Job (SEC-229), as sovereign-ops"
    epoch: 1
    join: [s3-secret-key]
    template:
      data:
        cloud: 'aws_access_key_id={{ .password }} aws_secret_access_key={{ index . "s3-secret-key" }}'
    copies:
      - name: velero-cloud
  s3-secret-key:
    delivery: synced
    source: external
    epoch: 2
  api-key:
    delivery: synced
    registerId: reg-api-key
    epoch: 3
EOF
cat > "$TMP/r15.want" <<'EOF'
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: velero-objectstorage-credentials
  namespace: "velero"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "velero-objectstorage-credentials"
    secrets.openova.io/writer: "the hand-over Job (SEC-229), as sovereign-ops"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: velero-objectstorage-credentials
    creationPolicy: Owner
    template:
      engineVersion: v2
      data:
        "cloud": "aws_access_key_id={{ .password }} aws_secret_access_key={{ index . \"s3-secret-key\" }}"
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/velero/velero-objectstorage-credentials/e1
        property: password
    - secretKey: s3-secret-key
      remoteRef:
        key: openova/apps/velero/s3-secret-key/e2
        property: password
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: "velero-cloud"
  namespace: "velero"
  labels:
    secrets.openova.io/library: secrets-lib
  annotations:
    secrets.openova.io/register-id: "velero-objectstorage-credentials"
    secrets.openova.io/writer: "the hand-over Job (SEC-229), as sovereign-ops"
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: SecretStore
    name: openbao
  target:
    name: "velero-cloud"
    creationPolicy: Owner
  data:
    - secretKey: password
      remoteRef:
        key: openova/apps/velero/velero-objectstorage-credentials/e1
        property: password
    - secretKey: s3-secret-key
      remoteRef:
        key: openova/apps/velero/s3-secret-key/e2
        property: password
EOF
r15=""
r "$TMP/r15.v" "$TMP/r15.yaml" || r15="$r15 render($(tr '\n' ' ' < "$TMP/r15.yaml.err"))"
{ doc "$TMP/r15.yaml" ExternalSecret velero-objectstorage-credentials; doc "$TMP/r15.yaml" ExternalSecret '"velero-cloud"'; } > "$TMP/r15.got"
diff -u "$TMP/r15.want" "$TMP/r15.got" || r15="$r15 external-secrets"
# Exactly the two ExternalSecrets of the external values beside the generated
# value's three objects: no Password, PushSecret or birth Job for them.
K15="$(kinds "$TMP/r15.yaml" || true)"
[ "$K15" = "ExternalSecret=3 Password=1 PushSecret=1 " ] || r15="$r15 kinds($K15)"
[ "$(grep -c -E '^  name: (velero-objectstorage-credentials|s3-secret-key)-e[0-9]+$' "$TMP/r15.yaml")" = 0 ] || r15="$r15 external-value-born"
# The generated value beside them renders as it does alone (R5, in velero).
printf 'self: hw-a\npeers: [hw-b]\nprimary: true\nnamespace: velero\n' > "$TMP/r15a.v"
awk '/^  api-key:/ { on = 1 } /^generatedSecrets:/ || on' "$TMP/r15.v" >> "$TMP/r15a.v"
r "$TMP/r15a.v" "$TMP/r15a.yaml" || r15="$r15 alone-render"
for k in "Password api-key-e3" "PushSecret api-key-e3" "ExternalSecret api-key"; do
  # shellcheck disable=SC2086
  [ "$(doc "$TMP/r15.yaml" $k)" = "$(doc "$TMP/r15a.yaml" $k)" ] && [ -n "$(doc "$TMP/r15a.yaml" $k)" ] || r15="$r15 generated-differs[$k]"
done
# The other region renders the same two ExternalSecrets, and nothing more for them.
sed 's/^self: hw-a/self: hw-b/; s/^peers: \[hw-b\]/peers: [hw-a]/; s/^primary: true/primary: false/' "$TMP/r15.v" > "$TMP/r15b.v"
r "$TMP/r15b.v" "$TMP/r15b.yaml" || r15="$r15 b-render"
K15B="$(kinds "$TMP/r15b.yaml" || true)"
[ "$K15B" = "ExternalSecret=3 " ] || r15="$r15 b-kinds($K15B)"
{ doc "$TMP/r15b.yaml" ExternalSecret velero-objectstorage-credentials; doc "$TMP/r15b.yaml" ExternalSecret '"velero-cloud"'; } > "$TMP/r15b.got"
diff -u "$TMP/r15.want" "$TMP/r15b.got" || r15="$r15 b-external-secrets"
[ -z "$r15" ] && pass "R15 source external: ExternalSecrets velero-objectstorage-credentials and its copy read e1 and the joined s3-secret-key e2, annotated with the writer; no Password, PushSecret or Job in either region; the generated api-key renders as alone" || fail "R15:$r15"

# ── R16 ─────────────────────────────────────────────────────────────────
awk '{ print } /^    delivery: synced$/ { print "    source: generated" }' "$TMP/r5.v" > "$TMP/r16.v"
r16=""
r "$TMP/r16.v" "$TMP/r16.yaml" || r16="$r16 render($(tr '\n' ' ' < "$TMP/r16.yaml.err"))"
grep -qxF '    source: generated' "$TMP/r16.v" || r16="$r16 no-source-in-input"
[ -s "$TMP/r16.yaml" ] && cmp -s "$TMP/r5.yaml" "$TMP/r16.yaml" || r16="$r16 differs"
[ -z "$r16" ] && pass "R16 source generated renders R5's value byte for byte" || fail "R16:$r16"

# ── F1-F66 ──────────────────────────────────────────────────────────────
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
refused F16 "key and placeholder are only for delivery mounted" 'generatedSecrets: {x: {delivery: synced, registerId: r, epoch: 1, key: token}}'
refused F17 "key and placeholder are only for delivery mounted" 'generatedSecrets: {x: {delivery: synced, registerId: r, epoch: 1, placeholder: true}}'
refused F18 'key "a b" must match'                 'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, key: "a b"}}'
refused F19 'key "" must match'                    'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, key: ""}}'
refused F20 "placeholder must be true or false"    'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, placeholder: "yes"}}'
refused F21 "a placeholder is empty: it takes no length or symbols" 'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, placeholder: true, length: 8}}'
refused F22 'namespace "" is not a namespace name'  "$(printf 'namespace: ""\ngeneratedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared}}')"
refused F23 'namespace "Org_Services" is not a namespace name' "$(printf 'namespace: Org_Services\ngeneratedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared}}')"
refused F24 'namespace "-org" is not a namespace name' "$(printf 'stores: true\nnamespace: "-org"\n')"
# SEC-219: template, copies and join. S is a synced value that takes them.
S='delivery: synced, registerId: r, epoch: 1'
refused F25 "template is only for delivery synced"  'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, template: {type: Opaque}}}'
refused F26 "copies is only for delivery synced"    'generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, copies: [{name: c}]}}'
refused F27 "join is only for delivery synced"      "generatedSecrets: {x: {delivery: mounted, epoch: 1, scope: shared, join: [w]}, w: {$S}}"
refused F28 'template field "label" is none of type, labels, annotations and data' "generatedSecrets: {x: {$S, template: {label: {a: b}}}}"
refused F29 'template labels "cnpg.io/reload" must be a string (quote it)' "generatedSecrets: {x: {$S, template: {labels: {cnpg.io/reload: true}}}}"
refused F30 'template data key "a b" must match'     "generatedSecrets: {x: {$S, template: {data: {\"a b\": v}}}}"
refused F31 'template type "basic auth" is not a Secret type' "generatedSecrets: {x: {$S, template: {type: basic auth}}}"
refused F32 "copies[0]: name is required, a Secret name" "generatedSecrets: {x: {$S, copies: [{template: {type: Opaque}}]}}"
refused F33 "copies[0]: namespace \"external-dns\" is not the value's namespace \"acme\". A copy stays in its value's namespace; a value read in another namespace is written through by its producer (SEC-225)" "generatedSecrets: {x: {$S, copies: [{name: c, namespace: external-dns}]}}"
refused F34 'copies[0]: name "w" is the name of a value of this chart' "generatedSecrets: {x: {$S, copies: [{name: w}]}, w: {$S}}"
refused F35 'copies[0]: name "c" is already a copy of generatedSecrets.w' "generatedSecrets: {x: {$S, copies: [{name: c}]}, w: {$S, copies: [{name: c}]}}"
refused F36 'copies[0]: field "keys" is none of name, namespace and template' "generatedSecrets: {x: {$S, copies: [{name: c, keys: [password]}]}}"
refused F37 'copies[0]: name "Bad_Name" is not a Secret name' "generatedSecrets: {x: {$S, copies: [{name: Bad_Name}]}}"
refused F38 'join names "nope", which is not a synced value of this chart' "generatedSecrets: {x: {$S, join: [nope]}}"
refused F39 'join names "w", which is not a synced value of this chart' "generatedSecrets: {x: {$S, join: [w]}, w: {delivery: mounted, epoch: 1, scope: shared}}"
refused F40 "a value does not join itself"          "generatedSecrets: {x: {$S, join: [x]}}"
refused F41 'join names "w" twice'                  "generatedSecrets: {x: {$S, join: [w, w]}, w: {delivery: synced, epoch: 1}}"
refused F42 "join names password, the key of the value itself" "generatedSecrets: {x: {$S, join: [password]}, password: {delivery: synced, epoch: 1}}"
refused F43 "generatedSecrets.x joins this value, so it renders no Secret of its own and takes no registerId" "generatedSecrets: {x: {$S, join: [w]}, w: {$S}}"
refused F44 "mountedCopies reads key password of Secret x, so template data must hold password" "generatedSecrets: {x: {$S, mountedCopies: [{serviceAccount: a}], template: {data: {token: '{{ .password }}'}}}}"
# SEC-226: seedFrom. M is a mounted value that takes it.
M='delivery: mounted, epoch: 1, scope: shared'
refused F45 "seedFrom is only for delivery mounted"  "generatedSecrets: {x: {$S, seedFrom: {name: a, key: b}}}"
refused F46 "seedFrom must be a map of name and key" "generatedSecrets: {x: {$M, seedFrom: a}}"
refused F47 'seedFrom field "namespace" is none of name and key' "generatedSecrets: {x: {$M, seedFrom: {name: a, key: b, namespace: c}}}"
refused F48 "seedFrom.name must be the name of a Secret of namespace acme" "generatedSecrets: {x: {$M, seedFrom: {name: Bad_Name, key: b}}}"
refused F49 "seedFrom.key must be a Secret key"      "generatedSecrets: {x: {$M, seedFrom: {name: a}}}"
refused F50 "seedFrom.key must be a Secret key"      "generatedSecrets: {x: {$M, seedFrom: {name: a, key: \"a/b\"}}}"
refused F51 "a placeholder is empty: it takes no seedFrom" "generatedSecrets: {x: {$M, placeholder: true, seedFrom: {name: a, key: b}}}"
# SEC-228: source external. E is an external value that renders its Secret.
E='delivery: synced, source: external, registerId: r, writer: w, epoch: 1'
X='takes no'
refused F52 'source "file" is neither generated nor external' "generatedSecrets: {x: {$S, source: file}}"
refused F53 "source is only for delivery synced"    "generatedSecrets: {x: {$M, source: external}}"
refused F54 "writer is only for delivery synced"    "generatedSecrets: {x: {$M, writer: w}}"
refused F55 "writer is required for source external: who writes openova/apps/acme/x/e1 (printable text)" "generatedSecrets: {x: {delivery: synced, source: external, registerId: r, epoch: 1}}"
refused F56 "writer is required for source external" "generatedSecrets: {x: {$E, writer: \" \"}}"
refused F57 "writer is required for source external" "generatedSecrets: {x: {$E, writer: 3}}"
refused F58 "writer is required for source external" "generatedSecrets: {x: {$E, writer: \"a\\nb\"}}"
refused F59 "(source external) is never generated or pushed here, so it $X length" "generatedSecrets: {x: {$E, length: 40}}"
refused F60 "(source external) is never generated or pushed here, so it $X symbols" "generatedSecrets: {x: {$E, symbols: 0}}"
refused F61 "(source external) is never generated or pushed here, so it $X sharedAcrossRegions" "generatedSecrets: {x: {$E, sharedAcrossRegions: false}}"
refused F62 "(source external) is never generated or pushed here, so it $X mountedCopies" "generatedSecrets: {x: {$E, mountedCopies: [{serviceAccount: a}]}}"
refused F63 "writer is only for source external: a generated value is written by its PushSecret" "generatedSecrets: {x: {$S, writer: w}}"
refused F64 "generatedSecrets.x joins this value, so it renders no Secret of its own and $X writer" "generatedSecrets: {x: {$E, join: [w]}, w: {delivery: synced, source: external, writer: w, epoch: 1}}"
refused F65 'join names "w", whose source is not this value'"'"'s: the values of one Secret have one source and one writer' "generatedSecrets: {x: {$E, join: [w]}, w: {delivery: synced, epoch: 1}}"
refused F66 'join names "w", whose source is not this value'"'"'s' "generatedSecrets: {x: {$S, join: [w]}, w: {delivery: synced, source: external, epoch: 1}}"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: secrets-lib render"; exit 1; fi
echo "PASS: secrets-lib render — stores, generated secrets, birth Job, shim"
