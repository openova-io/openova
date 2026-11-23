#!/usr/bin/env bash
# bp-openbao — the snapshot backup to object storage (SEC-180).
#
# Runs the CronJob's own script, cut from the render, with the host's curl
# against a local fake: OpenBao under /v1/, elsewhere an S3 bucket that
# verifies the AWS signature version 4 of every request and refuses the rest.
#   B1  off by default (and without kubernetesAuth): nothing of it renders;
#   B2  on: six-hourly, one run at a time, own service account and audience,
#       the bucket Secret as files, no credential in the environment;
#   B3  policy openbao-snapshot reads only sys/storage/raft/snapshot; role
#       openbao-snapshot binds openbao/openbao-backup, written before any role;
#   B4  a run logs in, reads one snapshot, puts it byte-identical and signed at
#       <prefix><region>/<UTC time>.snap, deletes only this region's snapshot
#       older than 30 days, revokes its token; no secret in curl's arguments;
#   B5  a refused login, an unverified key pair and an empty bucket name each
#       end the run non-zero and delete nothing.
# On origin/main before SEC-180 there is no backup template: B2 to B5 fail.
# Usage: bash tests/backup-render.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
CHART_DIR="$(cd "$CHART_DIR" && pwd)"
TMP="$(mktemp -d)"
PIDS=""
cleanup() { for p in $PIDS; do kill "$p" 2>/dev/null || true; done; rm -rf "$TMP"; }
trap cleanup EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi
for t in curl go yq helm; do command -v "$t" >/dev/null || { echo "FAIL: $t required"; exit 1; }; done
FAIL=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; FAIL=1; }

R=(helm template openbao . --namespace openbao)
ON=(--set backup.enabled=true --set backup.bucket=bk-test --set backup.bucketSecretName=bk-creds --set peerTrust.selfRegion=hw-a)
"${R[@]}" > "$TMP/off.yaml" || { echo "FAIL: default render"; exit 1; }
"${R[@]}" --set backup.enabled=true --set kubernetesAuth.enabled=false > "$TMP/noauth.yaml" || { echo "FAIL: render without kubernetesAuth"; exit 1; }
"${R[@]}" "${ON[@]}" > "$TMP/on.yaml" || { echo "FAIL: render with backup.enabled"; exit 1; }
q() { yq "$1" "$2"; }
CJ='select(.kind == "CronJob" and .metadata.name == "openbao-backup")'
POD="$CJ | .spec.jobTemplate.spec.template.spec"
DES='select(.kind == "ConfigMap" and .metadata.name == "openbao-config-desired") | .data'

# ── B1 ────────────────────────────────────────────────────────────────────
n_off="$(q 'select(.metadata.name == "openbao-backup" or .metadata.name == "openbao-backup-script") | .kind' "$TMP/off.yaml" | grep -c .)"
n_noauth="$(q 'select(.metadata.name == "openbao-backup" or .metadata.name == "openbao-backup-script") | .kind' "$TMP/noauth.yaml" | grep -c .)"
pol_off="$(q "$DES | .manifest" "$TMP/off.yaml" | grep -c 'openbao-snapshot')"
if [ "$n_off" = 0 ] && [ "$n_noauth" = 0 ] && [ "$pol_off" = 0 ]; then
  pass "B1 off by default: no backup object, policy or role; none without kubernetesAuth"
else
  fail "B1 off by default: $n_off backup objects by default, $n_noauth without kubernetesAuth, $pol_off desired-state lines"
fi

# ── B2 ────────────────────────────────────────────────────────────────────
b2=""
[ "$(q "$CJ | .spec.schedule" "$TMP/on.yaml")" = "17 */6 * * *" ] || b2="$b2 schedule"
[ "$(q "$CJ | .spec.concurrencyPolicy" "$TMP/on.yaml")" = "Forbid" ] || b2="$b2 concurrencyPolicy"
[ "$(q "$CJ | .spec.jobTemplate.metadata.labels[\"app.kubernetes.io/managed-by\"]" "$TMP/on.yaml")" = "flux" ] || b2="$b2 job-label"
[ "$(q "$POD | .serviceAccountName" "$TMP/on.yaml")" = "openbao-backup" ] || b2="$b2 serviceAccountName"
[ "$(q "$POD | .automountServiceAccountToken" "$TMP/on.yaml")" = "false" ] || b2="$b2 pod-automount"
[ "$(q 'select(.kind == "ServiceAccount" and .metadata.name == "openbao-backup") | .automountServiceAccountToken' "$TMP/on.yaml")" = "false" ] || b2="$b2 sa-automount"
[ "$(q "$POD | .volumes[] | select(.name == \"jwt\") | .projected.sources[0].serviceAccountToken.audience" "$TMP/on.yaml")" = "openbao" ] || b2="$b2 audience"
[ "$(q "$POD | .volumes[] | select(.name == \"bucket\") | .secret.secretName" "$TMP/on.yaml")" = "bk-creds" ] || b2="$b2 bucket-secret"
[ "$(q "$POD | .volumes[] | select(.name == \"bucket\") | .secret.items[].key" "$TMP/on.yaml" | sort | tr '\n' ' ')" = "s3-access-key s3-endpoint s3-region s3-secret-key " ] || b2="$b2 bucket-keys"
[ "$(q "$POD | .containers[0].env[] | select(has(\"valueFrom\")) | .name" "$TMP/on.yaml" | grep -c .)" = 0 ] || b2="$b2 env-valueFrom"
[ "$(q "$POD | .containers[0].env[].name" "$TMP/on.yaml" | grep -c -i -E 'PASSWORD|TOKEN|KEY|SECRET')" = 0 ] || b2="$b2 env-name"
case "$(q "$POD | .containers[0].image" "$TMP/on.yaml")" in harbor.openova.io/proxy-dockerhub/*) ;; *) b2="$b2 image" ;; esac
if [ -z "$b2" ]; then
  pass "B2 on: six-hourly CronJob, one run at a time, own service account and audience, bucket Secret bk-creds as files, no credential in env"
else
  fail "B2 on: wrong or missing:$b2"
fi

# ── B3 ────────────────────────────────────────────────────────────────────
pol="$(q "$DES | .[\"sys.policies.acl.openbao-snapshot.json\"]" "$TMP/on.yaml" | yq -p json '.policy')"
role="$(q "$DES | .[\"auth.kubernetes.role.openbao-snapshot.json\"]" "$TMP/on.yaml")"
q "$DES | .manifest" "$TMP/on.yaml" > "$TMP/manifest"
first_role="$(grep -n ' auth/kubernetes/role/' "$TMP/manifest" | head -n 1 | cut -d: -f1)"
pol_line="$(grep -n -x 'write sys/policies/acl/openbao-snapshot' "$TMP/manifest" | cut -d: -f1)"
b3=""
[ "$(grep -c "^path " <<<"$pol")" = 1 ] && grep -q -x -F 'path "sys/storage/raft/snapshot" {' <<<"$pol" \
  && grep -q -x -F '  capabilities = ["read"]' <<<"$pol" || b3="$b3 policy"
[ "$(printf '%s' "$role" | yq -p json -o json -I 0 '[.bound_service_account_names, .bound_service_account_namespaces, .policies, .audience, .ttl, .max_ttl]')" \
  = '[["openbao-backup"],["openbao"],["openbao-snapshot"],"openbao","15m","15m"]' ] || b3="$b3 role"
[ -n "$pol_line" ] && [ -n "$first_role" ] && [ "$pol_line" -lt "$first_role" ] || b3="$b3 order"
if [ -z "$b3" ]; then
  pass "B3 desired state: policy openbao-snapshot reads only sys/storage/raft/snapshot; role binds openbao/openbao-backup, audience openbao, 15m"
else
  fail "B3 desired state: wrong or missing:$b3"
fi

# ── the fake: OpenBao under /v1/, an S3 bucket that verifies signature v4 ──
mkdir -p "$TMP/fake"
cat > "$TMP/fake/main.go" <<'GOEOF'
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var root, logf, portf, ak, sk, reg, jwt, token, snap = flag.String("root", "", ""), flag.String("log", "", ""), flag.String("port", "", ""),
	flag.String("ak", "", ""), flag.String("sk", "", ""), flag.String("region", "", ""), flag.String("jwt", "", ""), flag.String("token", "", ""), flag.String("snapshot", "", "")

func record(f string, a ...any) {
	o, _ := os.OpenFile(*logf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintf(o, f+"\n", a...)
	o.Close()
}

func enc(s string) (out string) { // the URI encoding of signature v4
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-_.~", c) >= 0 {
			out += string(c)
		} else {
			out += fmt.Sprintf("%%%02X", c)
		}
	}
	return out
}

func mac(k []byte, s string) []byte { h := hmac.New(sha256.New, k); h.Write([]byte(s)); return h.Sum(nil) }
func hash(b []byte) string          { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// sigv4 is "ok" when the request carries a valid AWS signature version 4 of ak/sk for region reg, service s3.
func sigv4(r *http.Request, body []byte) string {
	f, pre := map[string]string{}, "AWS4-HMAC-SHA256 "
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, pre) {
		for _, p := range strings.Split(a[len(pre):], ",") {
			if kv := strings.SplitN(strings.TrimSpace(p), "=", 2); len(kv) == 2 {
				f[kv[0]] = kv[1]
			}
		}
	}
	c, date := strings.Split(f["Credential"], "/"), r.Header.Get("X-Amz-Date")
	if len(c) != 5 || c[0] != *ak || c[2] != *reg || c[3] != "s3" || c[4] != "aws4_request" || len(date) < 8 || c[1] != date[:8] {
		return "bad-scope"
	}
	q := []string{}
	for _, p := range strings.Split(r.URL.RawQuery, "&") {
		if kv := append(strings.SplitN(p, "=", 2), ""); p != "" {
			k, _ := url.QueryUnescape(kv[0])
			v, _ := url.QueryUnescape(kv[1])
			q = append(q, enc(k)+"="+enc(v))
		}
	}
	sort.Strings(q)
	h := ""
	for _, n := range strings.Split(f["SignedHeaders"], ";") {
		v := r.Header.Get(n)
		if n == "host" {
			v = r.Host
		}
		h += n + ":" + strings.Join(strings.Fields(v), " ") + "\n"
	}
	ph := r.Header.Get("X-Amz-Content-Sha256")
	if ph != "UNSIGNED-PAYLOAD" && ph != hash(body) {
		return "bad-payload"
	}
	sts := "AWS4-HMAC-SHA256\n" + date + "\n" + strings.Join(c[1:], "/") + "\n" +
		hash([]byte(strings.Join([]string{r.Method, r.URL.EscapedPath(), strings.Join(q, "&"), h, f["SignedHeaders"], ph}, "\n")))
	key := mac(mac(mac(mac([]byte("AWS4"+*sk), c[1]), c[2]), "s3"), "aws4_request")
	if !hmac.Equal([]byte(hex.EncodeToString(mac(key, sts))), []byte(f["Signature"])) {
		return "bad"
	}
	return "ok"
}

func serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p, tok, deny := r.URL.EscapedPath(), r.Header.Get("X-Vault-Token") == *token, `{"errors":["permission denied"]}`
	var in struct{ Role, JWT string }
	switch {
	case r.Method == "POST" && p == "/v1/auth/kubernetes/login":
		_ = json.Unmarshal(body, &in)
		if record("bao login role=%s jwt=%t", in.Role, in.JWT == *jwt); in.Role != "openbao-snapshot" || in.JWT != *jwt {
			http.Error(w, deny, 400)
			return
		}
		fmt.Fprintf(w, `{"auth":{"client_token":%q,"policies":["default","openbao-snapshot"]}}`, *token)
	case r.Method == "GET" && p == "/v1/sys/storage/raft/snapshot":
		if record("bao snapshot token=%t", tok); !tok {
			http.Error(w, deny, 403)
			return
		}
		w.Write(snapshot)
	case r.Method == "POST" && p == "/v1/auth/token/revoke-self":
		record("bao revoke-self token=%t", tok)
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/v1/"):
		record("bao unexpected %s %s", r.Method, p)
		http.Error(w, "{}", 404)
	default:
		bucket(w, r, p, body)
	}
}

func bucket(w http.ResponseWriter, r *http.Request, p string, body []byte) {
	sig := sigv4(r, body)
	if record("s3 %s %s?%s sig=%s", r.Method, p, r.URL.RawQuery, sig); sig != "ok" {
		http.Error(w, "<Error><Code>SignatureDoesNotMatch</Code></Error>", 403)
		return
	}
	parts := append(strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2), "")
	dir, obj := filepath.Join(*root, parts[0]), filepath.Join(*root, parts[0], parts[1])
	switch fi, err := os.Stat(obj); {
	case parts[1] == "" && r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		prefix, keys := r.URL.Query().Get("prefix"), []string{}
		filepath.Walk(dir, func(f string, i os.FileInfo, err error) error {
			if rel, _ := filepath.Rel(dir, f); err == nil && !i.IsDir() && strings.HasPrefix(rel, prefix) {
				keys = append(keys, "<Contents><Key>"+rel+"</Key></Contents>")
			}
			return nil
		})
		sort.Strings(keys)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><IsTruncated>false</IsTruncated>%s</ListBucketResult>`,
			parts[0], prefix, len(keys), strings.Join(keys, ""))
	case parts[1] != "" && r.Method == "PUT":
		os.MkdirAll(filepath.Dir(obj), 0o755)
		os.WriteFile(obj, body, 0o644)
	case parts[1] != "" && r.Method == "HEAD" && err == nil:
		w.Header().Set("Content-Length", fmt.Sprint(fi.Size()))
	case parts[1] != "" && r.Method == "DELETE":
		os.Remove(obj)
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

var snapshot []byte

func main() {
	flag.Parse()
	snapshot, _ = os.ReadFile(*snap)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	os.WriteFile(*portf+".tmp", []byte(fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)), 0o644)
	os.Rename(*portf+".tmp", *portf)
	http.Serve(ln, http.HandlerFunc(serve))
}
GOEOF
(cd "$TMP/fake" && GOWORK=off go build -o "$TMP/fakebao" main.go) || { echo "FAIL: the fake does not build"; exit 1; }

# ── the script and its environment, cut from the render ─────────────────
q 'select(.kind == "ConfigMap" and .metadata.name == "openbao-backup-script") | .data["backup.sh"]' "$TMP/on.yaml" > "$TMP/backup.sh"
q "$POD | .containers[0].env[] | .name + \"=\" + .value" "$TMP/on.yaml" > "$TMP/env"
grep -q 'raft/snapshot' "$TMP/backup.sh" || { echo "FAIL: no backup script in the render"; exit 1; }
REAL_CURL="$(command -v curl)"
mkdir -p "$TMP/bin"
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/curl-args"\nexec "%s" "$@"\n' "$TMP" "$REAL_CURL" > "$TMP/bin/curl"
chmod +x "$TMP/bin/curl"

AK="AKIDBACKUPTEST"; SK="bk/Secret+0123456789"; JWT="eyJhbGciOiJSUzI1NiJ9.backup-sa.sig"; TOK="s.backup-run-0001"
stamp() { date -u -d "@$(( $(date +%s) - $1 * 86400 ))" +%Y%m%dT%H%M%SZ; }
OLD="$(stamp 31)"; KEEP="$(stamp 29)"

# run <case> <access key> <secret key> <pod token> [ENV=VALUE...]: a fresh fake
# and bucket with seeded objects, then one run of the script; exit code in <case>/rc.
run() {
  local c="$TMP/$1" ak="$2" sk="$3" pod="$4"; shift 4
  mkdir -p "$c/bucket/bk-test/openbao/hw-a" "$c/bucket/bk-test/openbao/hw-b" "$c/bucket/bk-test/other" "$c/creds" "$c/work"
  head -c 1500000 /dev/urandom > "$c/snapshot"
  for o in "openbao/hw-a/$OLD.snap" "openbao/hw-a/$KEEP.snap" "openbao/hw-a/notes-$OLD.txt" "openbao/hw-b/$OLD.snap" "other/$OLD.snap"; do
    echo seeded > "$c/bucket/bk-test/$o"
  done
  printf "%s" "$pod" > "$c/jwt"
  "$TMP/fakebao" -root "$c/bucket" -log "$c/log" -port "$c/port" -ak "$AK" -sk "$SK" -region test-region-1 \
    -jwt "$JWT" -token "$TOK" -snapshot "$c/snapshot" & PIDS="$PIDS $!"
  for _ in $(seq 1 100); do [ -s "$c/port" ] && break; sleep 0.1; done
  printf 'http://127.0.0.1:%s' "$(cat "$c/port")" > "$c/creds/s3-endpoint"
  printf 'test-region-1' > "$c/creds/s3-region"; printf '%s' "$ak" > "$c/creds/s3-access-key"; printf '%s' "$sk" > "$c/creds/s3-secret-key"
  touch "$c/log"; : > "$TMP/curl-args"
  ( set -a; . "$TMP/env"; BAO_ADDR="http://127.0.0.1:$(cat "$c/port")"; BUCKET_DIR="$c/creds"; JWT_FILE="$c/jwt"; WORK_DIR="$c/work"
    for kv in "$@"; do export "${kv?}"; done
    PATH="$TMP/bin:$PATH" sh "$TMP/backup.sh" ) > "$c/out" 2>&1
  echo $? > "$c/rc"; cp "$TMP/curl-args" "$c/curl-args"
}
has() { [ -e "$TMP/$1/bucket/bk-test/$2" ]; }

# ── B4 ────────────────────────────────────────────────────────────────────
run b4 "$AK" "$SK" "$JWT"
new="$(ls "$TMP/b4/bucket/bk-test/openbao/hw-a" | grep -E '^[0-9]{8}T[0-9]{6}Z\.snap$' | grep -v -x -e "$OLD.snap" -e "$KEEP.snap")"
b4=""
[ "$(cat "$TMP/b4/rc")" = 0 ] || b4="$b4 exit=$(cat "$TMP/b4/rc")"
grep -q -x 'bao login role=openbao-snapshot jwt=true' "$TMP/b4/log" || b4="$b4 login"
grep -q -x 'bao snapshot token=true' "$TMP/b4/log" || b4="$b4 snapshot-read"
grep -q -x 'bao revoke-self token=true' "$TMP/b4/log" || b4="$b4 revoke"
{ [ "$(printf '%s\n' "$new" | grep -c .)" = 1 ] && cmp -s "$TMP/b4/snapshot" "$TMP/b4/bucket/bk-test/openbao/hw-a/$new"; } || b4="$b4 put($new)"
[ "$(grep -c '^s3 ' "$TMP/b4/log")" -ge 4 ] && [ "$(grep '^s3 ' "$TMP/b4/log" | grep -c -v ' sig=ok$')" = 0 ] || b4="$b4 signatures"
grep -q -x "s3 GET /bk-test?list-type=2&prefix=openbao%2Fhw-a%2F sig=ok" "$TMP/b4/log" || b4="$b4 list"
[ "$(grep -c '^s3 DELETE ' "$TMP/b4/log")" = 1 ] && ! has b4 "openbao/hw-a/$OLD.snap" || b4="$b4 delete-old"
has b4 "openbao/hw-a/$KEEP.snap" && has b4 "openbao/hw-a/notes-$OLD.txt" && has b4 "openbao/hw-b/$OLD.snap" && has b4 "other/$OLD.snap" || b4="$b4 kept"
grep -q -F -e "$AK" -e "$SK" -e "$TOK" -e "$JWT" "$TMP/b4/curl-args" && b4="$b4 argv"
[ "$(grep -c . "$TMP/b4/curl-args")" -ge 6 ] || b4="$b4 curl-not-run"
if [ -z "$b4" ]; then
  pass "B4 a run: login, snapshot read, put byte-identical at openbao/hw-a/$new, signed, 31-day-old deleted, rest kept, token revoked"
else
  fail "B4 a run: wrong or missing:$b4"; sed 's/^/        /' "$TMP/b4/out" "$TMP/b4/log"
fi

# ── B5 ────────────────────────────────────────────────────────────────────
run b5-login "$AK" "$SK" "not-the-pod-token"
run b5-key "$AK" "wrong/Secret+key" "$JWT"
run b5-bucket "$AK" "$SK" "$JWT" "BUCKET="
b5=""
{ [ "$(cat "$TMP/b5-login/rc")" != 0 ] && grep -q 'FATAL: login as openbao-snapshot' "$TMP/b5-login/out" \
  && ! grep -q -e '^bao snapshot' -e '^s3 ' "$TMP/b5-login/log"; } || b5="$b5 login"
{ [ "$(cat "$TMP/b5-key/rc")" != 0 ] && grep -q '^s3 PUT .* sig=bad$' "$TMP/b5-key/log" && ! grep -q '^s3 DELETE ' "$TMP/b5-key/log" \
  && has b5-key "openbao/hw-a/$OLD.snap" && [ "$(ls "$TMP/b5-key/bucket/bk-test/openbao/hw-a" | grep -c .)" = 3 ]; } || b5="$b5 key"
{ [ "$(cat "$TMP/b5-bucket/rc")" != 0 ] && grep -q 'FATAL: backup.bucket is empty' "$TMP/b5-bucket/out" && [ ! -s "$TMP/b5-bucket/log" ]; } || b5="$b5 bucket"
if [ -z "$b5" ]; then
  pass "B5 failures: a refused login reads nothing and sends nothing; an unverified key pair puts and deletes nothing; an empty bucket sends no request"
else
  fail "B5 failures: wrong:$b5"; for c in b5-login b5-key b5-bucket; do sed "s/^/        $c: /" "$TMP/$c/out" "$TMP/$c/log"; done
fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao snapshot backup"; exit 1; fi
echo "PASS: bp-openbao snapshot backup — off by default; on, one signed, verified upload a run and a 30-day retention of this region's snapshots"
