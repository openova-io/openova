#!/usr/bin/env bash
# self-test.sh <path of standing-update.sh> - the cases of `standing-update.sh --self-test` (FND-076).
#
# Four fakes in a folder of the test come first on PATH: kubectl (the regions a and b, told apart by the
# name of the kubeconfig), curl (the release route: PUT first refuses a body as release.go does, an
# unknown field, pullSecret, a delivery without the case's credential, a dry run or rollback with one,
# then answers from the case's list; GET answers the
# state the last PUT left, first with phase staging, then with its final phase), sovereign-acceptance
# (mint-session; it takes only the issuer catalyst-api states) and a stand-in for standing-gate.py (source
# passes once the region carries the candidate, converge as the case says). The fake kubectl answers the
# two jsonpaths of catalyst-api exactly and a cat of the signer's stated path only, so a wrong read cannot
# pass. A route Job of phase running or stalled does not end while the run waits; one of phase running
# promotes its region before the next request. No cluster, no network. A session, a token and a pull
# credential with markers are planted; no case may print them. Each case fails when the rule it names
# is broken.
set -uo pipefail
SCRIPT="${1:?usage: self-test.sh <path of standing-update.sh>}"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
fails=0; n=0
ok() { n=$((n + 1)); if [ "$1" = 0 ]; then echo "ok   $2"; else echo "FAIL $2"; fails=$((fails + 1)); fi; }

ID=20261119T125842Z-774da0d
COMMIT=774da0d4250000000000000000000000000000ab
FOLLOWER=standby-1
SESSION_MARK=SESSION-MARK-7f3a
PULL_MARK=PULL-MARK-91c2
TOKEN_MARK=TOKEN-MARK-55e0
# what catalyst-api of region a states, neither of them the value a guess would take; the fake kubectl
# answers only these two jsonpaths and a cat of only this path
ISSUER=https://console.t1.omantel.biz
SIGNER=/var/lib/catalyst/signer-of-the-case.pem
# shellcheck disable=SC2089 # the quotes are part of the jsonpath
JP_ISSUER='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_PIN_ISSUER")].value}'
# shellcheck disable=SC2089 # the quotes are part of the jsonpath
JP_SIGNER='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_HANDOVER_SIGNER_PATH")].value}'

mkdir -p "$T/bin"
cat > "$T/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
# fake kubectl: region from the kubeconfig's name (.../ka or .../kb)
S="$FAKE_DIR"; kc=""; args=()
while [ $# -gt 0 ]; do
  case "$1" in --kubeconfig) kc="$2"; shift 2 ;; --request-timeout=*) shift ;; *) args+=("$1"); shift ;; esac
done
r="$(basename "$kc")"; r="${r#k}"
set -- "${args[@]}"
a=" $* "
echo "kubectl $r $*" >> "$S/events"
pick() { if [ -e "$S/moved.$r" ] && [ ! -e "$S/rolledback.$r" ] && [ -e "$S/$r.$1.after.json" ]; then cat "$S/$r.$1.after.json"; else cat "$S/$r.$1.json"; fi; }
case "$a" in
  *" get helmreleases.helm.toolkit.fluxcd.io -A "*) pick hr ;;
  *" get kustomizations.kustomize.toolkit.fluxcd.io -A "*) pick ks ;;
  *" get pods -A "*) pick pods ;;
  *" create token "*) echo "$TOKEN_MARK-$r" ;;
  *" exec -i openbao-0 "*)
    tok="$(cat)"; [ "$tok" = "$TOKEN_MARK-$r" ] || { echo "no token on stdin" >&2; exit 1; }
    [ -e "$S/snap.$r" ] || { echo "error: permission denied" >&2; exit 1; }
    cat "$S/snap.$r" ;;
  *" exec openbao-0 "*" mv "*) echo "snapshot kept $r" >> "$S/events" ;;
  *" -n catalyst-system get deploy catalyst-api -o jsonpath=$JP_ISSUER "* | *" -n catalyst-system get deploy catalyst-api -o jsonpath=$JP_SIGNER "*)
    [ ! -e "$S/deploy-unreadable" ] || { echo 'Error from server (Forbidden): deployments.apps "catalyst-api" is forbidden' >&2; exit 1; }
    case "$a" in *"$JP_ISSUER"*) [ -e "$S/no-issuer" ] || echo "$ISSUER" ;; *) [ -e "$S/no-signer" ] || echo "$SIGNER" ;; esac ;;
  " -n catalyst-system exec deploy/catalyst-api -- cat $SIGNER ") echo "KEY-OF-THE-SIGNER" ;;
  *" exec deploy/catalyst-api -- cat "*) echo "cat: can't open '${a##* cat }': No such file or directory" >&2; exit 1 ;;
  *" get secret cutover-secondary-kubeconfigs "*) if [ -e "$S/followers" ]; then cat "$S/followers"; else printf '%s\n' "$FOLLOWER.yaml"; fi ;;
  *" get kustomization bootstrap-kit "*) echo "main@sha1:$(cat "$S/applied.$r" 2>/dev/null || echo 0000000000000000000000000000000000000000)" ;;
  *" get pods -l app.kubernetes.io/name=gitea"*) echo gitea-0 ;;
  *" exec gitea-0 "*" rev-parse "*)
    for x in "$@"; do case "$x" in *'^{tree}') c="${x%^\{tree\}}"; awk -v c="$c" '$1 == c {print $2}' "$S/trees" ;; esac; done ;;
  *) echo "fake kubectl: no answer for: $*" >&2; exit 3 ;;
esac
FAKE
cat > "$T/bin/curl" <<'FAKE'
#!/usr/bin/env bash
# fake release route
S="$FAKE_DIR"; out=""; method=GET; data=""; url=""; hdrs=()
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;; -w | --max-time) shift 2 ;; -X) method="$2"; shift 2 ;;
    -H) hdrs+=("$2"); shift 2 ;; --data-binary) data="${2#@}"; shift 2 ;; http*) url="$1"; shift ;; *) shift ;;
  esac
done
authed=0
for h in "${hdrs[@]}"; do case "$h" in @*) grep -qx "Cookie: catalyst_session=$SESSION_MARK" "${h#@}" && authed=1 ;; esac; done
[ "$authed" = 1 ] || { echo '{"error":"unauthenticated"}' > "$out"; printf 401; exit 0; }
[ "$url" = "$ISSUER/api/v1/sovereign/release" ] || { echo '{}' > "$out"; printf 404; exit 0; }
if [ "$method" = GET ]; then
  g=$(( $(cat "$S/gets" 2>/dev/null || echo 0) + 1 )); echo "$g" > "$S/gets"
  if [ "$g" = 1 ] && [ -e "$S/fresh" ] && [ ! -e "$S/direct" ]; then jq '.phase = "staging" | .status.phase = "staging"' "$S/route.json" > "$out"; else cp "$S/route.json" "$out"; fi
  printf 200; exit 0
fi
promote() { # promote <region of the gate> <the route's name>
  jq --arg n "$2" --arg f "$(cat "$S/from.$1")" --arg t "$(cat "$S/to.$1")" '.promoted[$n] = {promotedFrom: $f, promotedTo: $t, phase: "promoted"}' "$S/route.json" > "$S/route.new" && mv "$S/route.new" "$S/route.json"
  touch "$S/moved.$1"; cat "$S/to.$1" > "$S/applied.$1"
}
# a Job that was still running (phase running) promotes its region before the next request is read
if [ -e "$S/pending" ]; then read -r pg pn < "$S/pending"; rm -f "$S/pending"; promote "$pg" "$pn"; fi
p=$(( $(cat "$S/puts" 2>/dev/null || echo 0) + 1 )); echo "$p" > "$S/puts"; rm -f "$S/gets"; touch "$S/fresh"
# the log names the credential ok (the case's), wrong or absent, never its password
cred="$(jq -r --arg pm "$PULL_MARK" 'if has("credential") | not then "" elif .credential == {registry: "ghcr.io", username: "bot", password: $pm} then "ok" else "wrong" end' "$data")"
echo "PUT $(jq -c -S --arg c "$cred" 'if has("credential") then .credential = $c else . end' "$data")" >> "$S/events"
[ -z "$cred" ] || [ "$(stat -c %a "$data")" = 600 ] || echo "CREDENTIAL IN A BODY OF MODE $(stat -c %a "$data")" >> "$S/events"
refuse() { jq -n --arg e "$2" --arg d "$3" '{error: $e, detail: $d}' > "$out"; printf '%s' "$1"; exit 0; }
region="$(jq -r '.region // ""' "$data")"; rollback="$(jq -r '.rollback // false' "$data")"; dry="$(jq -r '.dryRun // false' "$data")"
# the refusals of release.go, before anything is staged
extra="$(jq -r 'keys - ["candidate", "dryRun", "credential", "region", "rollback", "pullSecret"] | first // ""' "$data")"
[ -z "$extra" ] || refuse 400 bad-body "json: unknown field \"$extra\""
[ "$(jq 'has("pullSecret")' "$data")" = false ] || refuse 400 pull-secret-removed 'pullSecret is removed: send credential {"registry": "ghcr.io", "username": "<user>", "password": "<password or token>"}; nothing was staged'
[ "$rollback" != true ] || [ -z "$cred" ] || refuse 400 rollback-credential 'leave credential out of a rollback'
[ "$dry" != true ] || [ -z "$cred" ] || refuse 400 dry-run-credential 'leave credential out of a dry run'
if [ "$rollback" != true ] && [ "$dry" != true ]; then
  [ -n "$cred" ] || refuse 422 registry-credential-missing 'a delivery copies from ghcr.io: send credential {"registry": "ghcr.io", "username": "<user>", "password": "<password or token>"}; nothing was staged'
  [ "$cred" = ok ] || refuse 422 registry-credential-refused 'ghcr.io refused the credential (HTTP 401): send a credential that may pull from ghcr.io; nothing was staged'
fi
line="$(sed -n "${p}p" "$S/answers")"
code="${line%% *}"; rest="${line#* }"
[ "$code" = 202 ] || refuse "$code" "${rest%% *}" "${rest#* }"
final="$rest"; job="sovereign-release-$p"
case "$region" in primary) gr=a ;; "$FOLLOWER") gr=b ;; *) gr="" ;; esac
msg="phase $final"
[ "$final" = conflict ] && msg="stage conflict in a region: nothing moved in any region; see <region>.stage.report"
jq --arg j "$job" --arg f "$final" --arg c "$(jq -r .candidate "$data")" --arg m "$msg" --arg reg "$region" \
  '.job = $j | .phase = $f | .status = ((.status // {}) | with_entries(select(.key | test("[.](promotedFrom|promotedTo|phase)$")))) + {candidate: $c, phase: $f, message: $m, job: $j}
   | if $f == "conflict" then .status["primary.stage.report"] = "CONFLICT clusters/_template/infrastructure/providers/base/provider-opentofu.yaml" else . end' \
  "$S/route.json" > "$S/route.new" && mv "$S/route.new" "$S/route.json"
if [ "$final" = delivered ] && [ -n "$gr" ] && [ "$dry" != true ] && [ "$rollback" != true ]; then
  promote "$gr" "$region"
fi
# phase running: the Job does not end while the run waits and promotes later; phase stalled: it never does
if [ "$final" = running ] && [ -n "$gr" ] && [ "$dry" != true ] && [ "$rollback" != true ]; then echo "$gr $region" > "$S/pending"; fi
if [ "$final" = rolled-back ] && [ -n "$gr" ]; then
  jq --arg n "$region" '.promoted[$n].phase = "rolled-back"' "$S/route.json" > "$S/route.new" && mv "$S/route.new" "$S/route.json"
  touch "$S/rolledback.$gr"; cat "$S/rb.$gr" > "$S/applied.$gr"
fi
jq -n --arg j "$job" '{phase: "requested", job: $j}' > "$out"
printf 202
FAKE
cat > "$T/bin/sovereign-acceptance" <<'FAKE'
#!/usr/bin/env bash
[ "${1:-}" = mint-session ] || exit 2
iss=""; while [ $# -gt 0 ]; do case "$1" in --issuer) iss="$2"; shift 2 ;; *) shift ;; esac; done
[ "$iss" = "$ISSUER" ] || { echo "not the issuer catalyst-api states: $iss" >&2; exit 1; }
[ "$(cat)" = KEY-OF-THE-SIGNER ] || { echo "no key" >&2; exit 1; }
echo "$SESSION_MARK"
FAKE
cat > "$T/bin/fake-standing-gate" <<'FAKE'
#!/usr/bin/env bash
S="$FAKE_DIR"; cmd="$1"; shift; region=""
while [ $# -gt 0 ]; do case "$1" in --region) region="$2"; shift 2 ;; *) shift ;; esac; done
echo "gate $cmd $region" >> "$S/events"
case "$cmd" in
  source)
    if [ -e "$S/at.$region" ] || { [ -e "$S/moved.$region" ] && [ ! -e "$S/rolledback.$region" ]; }; then
      echo "PASS region $region: Flux applied the candidate"; exit 0
    fi
    echo "FAIL R5 region $region: the candidate is not an ancestor"; exit 1 ;;
  converge)
    if [ -e "$S/stuck.$region" ] && [ ! -e "$S/rolledback.$region" ]; then echo "FAIL region $region: not converged within 30 minutes"; exit 1; fi
    echo "PASS region $region: all 3 HelmReleases that are not suspended are Ready"; exit 0 ;;
esac
exit 2
FAKE
chmod +x "$T/bin/"*
# shellcheck disable=SC2090 # the jsonpaths are compared as text
export PATH="$T/bin:$PATH" SESSION_MARK PULL_MARK TOKEN_MARK FOLLOWER ISSUER SIGNER JP_ISSUER JP_SIGNER
[ "$(command -v kubectl)" = "$T/bin/kubectl" ] && [ "$(command -v curl)" = "$T/bin/curl" ]
ok $? "the cases reach only the fake kubectl and the fake route"

# a good snapshot (gzip tar with meta.json and state.bin) and two bad ones
mkdir -p "$T/snap-good" "$T/snap-half"
echo '{"Version":1}' > "$T/snap-good/meta.json"; echo 'raft state' > "$T/snap-good/state.bin"
tar -czf "$T/good.snap" -C "$T/snap-good" meta.json state.bin
cp "$T/snap-good/meta.json" "$T/snap-half/"; tar -czf "$T/half.snap" -C "$T/snap-half" meta.json
echo "not a tar" > "$T/text.snap"

hr() { # hr <Ready status>: one HelmRelease
  printf '{"items": [{"metadata": {"namespace": "flux-system", "name": "bp-catalyst-platform"}, "status": {"conditions": [{"type": "Ready", "status": "%s"}], "lastAppliedRevision": "1.4.2166"}}]}\n' "$1"
}
ks='{"items": [{"metadata": {"namespace": "flux-system", "name": "bootstrap-kit"}, "status": {"conditions": [{"type": "Ready", "status": "True"}], "lastAppliedRevision": "main@sha1:1111111111111111111111111111111111111111"}}]}'
pods='{"items": [{"metadata": {"namespace": "catalyst", "name": "old-job-x"}, "status": {"phase": "Failed"}}, {"metadata": {"namespace": "kube-system", "name": "c"}, "status": {"phase": "Running"}}]}'

# new_case <name>: a folder with both regions Ready, good snapshots, no candidate applied, the route idle
new_case() {
  S="$T/case-$1"; mkdir -p "$S"
  for r in a b; do hr True > "$S/$r.hr.json"; echo "$ks" > "$S/$r.ks.json"; echo "$pods" > "$S/$r.pods.json"; cp "$T/good.snap" "$S/snap.$r"; done
  echo 1111111111111111111111111111111111111111 > "$S/from.a"; echo 2222222222222222222222222222222222222222 > "$S/to.a"; echo 3333333333333333333333333333333333333333 > "$S/rb.a"
  echo 4444444444444444444444444444444444444444 > "$S/from.b"; echo 5555555555555555555555555555555555555555 > "$S/to.b"; echo 6666666666666666666666666666666666666666 > "$S/rb.b"
  printf '%s\n' "1111111111111111111111111111111111111111 tree-1" "3333333333333333333333333333333333333333 tree-1" \
    "2222222222222222222222222222222222222222 tree-2" "4444444444444444444444444444444444444444 tree-4" \
    "6666666666666666666666666666666666666666 tree-4" "5555555555555555555555555555555555555555 tree-5" > "$S/trees"
  echo '{"phase": "none", "status": {}}' > "$S/route.json"
  : > "$S/answers"; : > "$S/events"
  printf '{"auths": {"ghcr.io": {"username": "bot", "password": "%s", "auth": "%s"}}}\n' "$PULL_MARK" "$(printf 'bot:%s' "$PULL_MARK" | base64)" > "$S/pull.json"
  echo "kubeconfig a" > "$S/ka"; echo "kubeconfig b" > "$S/kb"
}

run() { # run [<NAME=value> ...]: the update in the case's folder, with these settings on top; RC, the output in S/out
  ( export FAKE_DIR="$S" STANDING_KUBECONFIG_A="$S/ka" STANDING_KUBECONFIG_B="$S/kb" STANDING_PULL_FILE="$S/pull.json" \
      CANDIDATE_ID="$ID" CANDIDATE_COMMIT="$COMMIT" ENV_LABEL=t1 GATE_RUN=42 STANDING_GATE="$T/bin/fake-standing-gate" \
      STANDING_POLL_SECONDS=0 STANDING_ROUTE_MINUTES=1 STANDING_SOURCE_MINUTES=0 STANDING_CONVERGE_MINUTES=1 \
      STANDING_SUMMARY="$S/summary.md"
    # shellcheck disable=SC2163 # each argument is NAME=value
    [ $# = 0 ] || export "$@"
    unset STANDING_CONSOLE STANDING_FOLLOWER STANDING_NAMESPACE
    bash "$SCRIPT" run ) > "$S/out" 2>&1
  RC=$?
  cat "$S/out" >> "$T/all-output"; cat "$S/summary.md" >> "$T/all-output" 2>/dev/null
}
puts() { grep -c '^PUT ' "$S/events"; }
put_n() { grep '^PUT ' "$S/events" | sed -n "${1}p"; }
line_of() { grep -n -F -e "$1" "$2" | head -n 1 | cut -d: -f1; } # the first line that holds $1, or empty
before() { local x y; x="$(line_of "$1" "$3")"; y="$(line_of "$2" "$3")"; [ -n "$x" ] && [ -n "$y" ] && [ "$x" -lt "$y" ]; }
put_has() { [ "$(put_n "$1" | grep -c -F -e "$2")" -ge 1 ]; } # the n-th request holds $2 (grep -q in a pipe can fail on SIGPIPE)

# 0. the session and the follower: read in region a, and a failed or empty read stops the run before any request
for x in deploy-unreadable no-issuer no-signer; do
  new_case "session-$x"; touch "$S/$x"; echo "202 dry-run" > "$S/answers"
  case "$x" in deploy-unreadable) want='deployments.apps "catalyst-api" is forbidden' ;; no-issuer) want='states no CATALYST_PIN_ISSUER' ;; *) want='states no CATALYST_HANDOVER_SIGNER_PATH' ;; esac
  run
  [ "$RC" = 1 ] && [ "$(puts)" = 0 ] && [ "$(grep -c -F ' exec deploy/catalyst-api ' "$S/events")" = 0 ] \
    && [ "$(grep -c -F "the session of the run could not be minted from region a's signer: " "$S/out")" = 1 ] && [ "$(grep -c -F -e "$want" "$S/out")" -ge 1 ]
  ok $? "session ($x): the run reads no signer, sends no request and names that read of catalyst-api in region a as the cause"
done
for f in 0 2; do
  new_case "followers-$f"; echo "202 dry-run" > "$S/answers"
  if [ "$f" = 0 ]; then : > "$S/followers"; else printf '%s\n' "$FOLLOWER.yaml" standby-2.yaml > "$S/followers"; fi
  run
  [ "$RC" = 1 ] && [ "$(puts)" = 0 ] && [ "$(grep -c -F "region a names $f follower regions" "$S/out")" = 1 ] && [ "$(grep -c -F 'create token' "$S/events")" = 0 ]
  ok $? "follower: region a naming $f follower regions, not one, stops the run before any snapshot and any request"
done

# 1. already at the candidate
new_case at; touch "$S/at.a" "$S/at.b"
echo "{\"phase\": \"delivered\", \"job\": \"sovereign-release-9\", \"status\": {\"candidate\": \"$ID\", \"phase\": \"delivered\"}}" > "$S/route.json"
run
[ "$RC" = 0 ] && [ "$(puts)" = 0 ] && grep -q 'result: already-at' "$S/out" && ! grep -q 'create token' "$S/events"
ok $? "already at: both regions carry the candidate and the route reports it delivered: no request, no snapshot, exit 0"
new_case at-route; touch "$S/at.a" "$S/at.b"; echo "202 dry-run" > "$S/answers"; echo "202 delivered" >> "$S/answers"; echo "202 delivered" >> "$S/answers"
run
[ "$RC" = 0 ] && [ "$(puts)" = 3 ] && grep -q 'result: updated' "$S/out"
ok $? "already at needs the route too: regions that carry the candidate but a route that does not report it delivered get the requests"

# 2. the dry run
new_case dry-conflict; echo "202 conflict" > "$S/answers"
run
[ "$RC" = 1 ] && [ "$(puts)" = 1 ] && put_has 1 '"dryRun":true' && put_has 1 "\"region\":\"$FOLLOWER\"" \
  && grep -q 'dry run ended with phase conflict, not dry-run: stage conflict in a region' "$S/out" \
  && grep -q 'primary.stage.report: CONFLICT clusters/_template/infrastructure/providers/base/provider-opentofu.yaml' "$S/out" \
  && grep -q "the route's stage met a conflict" "$S/out" \
  && ! put_has 1 credential
ok $? "dry run: a phase other than dry-run (a stage conflict) stops the run with the route's message and report, and no delivery request"
new_case dry-old-route; echo '400 bad-body json: unknown field "region"' > "$S/answers"
run
[ "$RC" = 1 ] && [ "$(puts)" = 1 ] && grep -q 'installed before SOV-521' "$S/out" && grep -q 'result: failed' "$S/out"
ok $? "dry run: a route installed before SOV-521 (unknown field region) is named and the run stops with no delivery request"

# 3. the snapshot
for bad in half text missing; do
  new_case "snap-$bad"; echo "202 dry-run" > "$S/answers"
  if [ "$bad" = missing ]; then rm -f "$S/snap.a"; else cp "$T/$bad.snap" "$S/snap.a"; fi
  run
  [ "$RC" = 1 ] && [ "$(puts)" = 0 ] && grep -q 'the OpenBao snapshot of region a ' "$S/out" && grep -q 'no request was sent' "$S/out" \
    && grep -q 'snapshot kept b' "$S/events" && ! grep -q 'snapshot kept a' "$S/events"
  ok $? "snapshot: one that is not a gzip tar holding meta.json and state.bin ($bad) is not kept as pre-update.snap and stops the run before any request"
done

# 4. the whole path
new_case path; printf '%s\n' "202 dry-run" "202 delivered" "202 delivered" > "$S/answers"
run
[ "$RC" = 0 ] && [ "$(puts)" = 3 ] && grep -q 'result: updated' "$S/out"
ok $? "path: dry run, request for b, request for a, exit 0"
put_has 2 "\"region\":\"$FOLLOWER\"" && put_has 2 '"credential":"ok"' && put_has 3 '"region":"primary"' && put_has 3 '"credential":"ok"' \
  && ! put_has 2 dryRun && ! put_has 1 credential && ! grep -q 'CREDENTIAL IN A BODY OF MODE' "$S/events"
ok $? "path: the request for the follower names region b, the request for the primary names primary, each with the credential of ghcr.io in a body of mode 600; the dry run carries none"
before 'gate converge b' "\"region\":\"primary\"" "$S/events" && before 'region b converged and compared clean' 'the request for region a' "$S/out"
ok $? "path: the request for the primary is sent only after b converged and compared clean"
before 'the state before of region b' 'the OpenBao snapshot of region b:' "$S/out" && before 'the OpenBao snapshot of region b:' 'the state before of region a' "$S/out" \
  && before 'the OpenBao snapshot of region a:' 'the dry run of' "$S/out" && before 'the dry run of' 'the request for region b' "$S/out" \
  && before 'the request for region b' 'region b converged and compared clean' "$S/out" && before 'the request for region a' 'region a converged and compared clean' "$S/out" \
  && grep -Eq "the OpenBao snapshot of region b: [0-9]+ bytes, sha256 $(sha256sum < "$T/good.snap" | cut -c1-64)" "$S/out"
ok $? "path: the log names the state before and the snapshot (size and sha256) of each region, the dry run, b, then a, in this order"
! grep -q -E ' (create|delete) secret ' "$S/events" && grep -q 'snapshot kept a' "$S/events" && grep -q 'snapshot kept b' "$S/events"
ok $? "path: the run makes no Secret in a region (the route makes the delivery's pull Secret), and the snapshots are kept in the regions"

# 5. the follower does not converge
new_case b-stuck; printf '%s\n' "202 dry-run" "202 delivered" "202 rolled-back" > "$S/answers"; touch "$S/stuck.b"
run
[ "$RC" = 1 ] && [ "$(puts)" = 3 ] && put_has 3 "\"region\":\"$FOLLOWER\"" && put_has 3 '"rollback":true' \
  && [ "$(grep '^PUT ' "$S/events" | grep -c -F '"region":"primary"')" = 0 ] && grep -q 'region b runs the tree of 4444444444444444444444444444444444444444 again' "$S/out"
ok $? "b does not converge: one rollback request for b, no request for the primary, exit 1"

# 6. the primary compares worse
new_case a-worse; printf '%s\n' "202 dry-run" "202 delivered" "202 delivered" "202 rolled-back" "202 rolled-back" > "$S/answers"
hr False > "$S/a.hr.after.json"
run
[ "$RC" = 1 ] && [ "$(puts)" = 5 ] && put_has 4 '"region":"primary"' && put_has 4 '"rollback":true' \
  && put_has 5 "\"region\":\"$FOLLOWER\"" && put_has 5 '"rollback":true' \
  && grep -q 'HelmRelease flux-system/bp-catalyst-platform was Ready before and is False now' "$S/out"
ok $? "a compares worse than before: rollback requests for a, then for b, exit 1"
new_case a-pods; printf '%s\n' "202 dry-run" "202 delivered" "202 delivered" "202 rolled-back" "202 rolled-back" > "$S/answers"
echo '{"items": [{"metadata": {"namespace": "catalyst", "name": "old-job-x"}, "status": {"phase": "Failed"}}, {"metadata": {"namespace": "catalyst", "name": "new-y"}, "status": {"phase": "Pending"}}]}' > "$S/a.pods.after.json"
run
[ "$RC" = 1 ] && [ "$(puts)" = 5 ] && grep -q '2 Pods are neither Running nor Succeeded, 1 before' "$S/out"
ok $? "a compares worse: more Pods neither Running nor Succeeded than before is worse"

# 7. a route answer of phase refused, moved or failed
for ph in refused moved failed; do
  new_case "b-$ph"; printf '%s\n' "202 dry-run" "202 $ph" > "$S/answers"
  run
  [ "$RC" = 1 ] && [ "$(puts)" = 2 ] && grep -q "phase $ph" "$S/out" && grep -q 'the run promoted no region; nothing is rolled back' "$S/out"
  ok $? "the route answers phase $ph: the run stops, nothing more is sent, nothing is rolled back"
done
new_case b-refused-http; printf '%s\n' "202 dry-run" "202 delivered" > "$S/answers"
printf '{"auths": {"ghcr.io": {"auth": "%s"}}}\n' "$(printf 'bot:%s-revoked' "$PULL_MARK" | base64)" > "$S/pull.json"
run
[ "$RC" = 1 ] && [ "$(puts)" = 2 ] && put_has 2 '"credential":"wrong"' && grep -q 'HTTP 422 registry-credential-refused' "$S/out" \
  && grep -q 'the run promoted no region; nothing is rolled back' "$S/out"
ok $? "the route refuses the credential of the request for b: the run stops, nothing more is sent"
new_case a-failed; printf '%s\n' "202 dry-run" "202 delivered" "202 failed" "202 rolled-back" > "$S/answers"
run
[ "$RC" = 1 ] && [ "$(puts)" = 4 ] && put_has 4 "\"region\":\"$FOLLOWER\"" && put_has 4 '"rollback":true'
ok $? "the request for a ends failed after b was promoted: b is rolled back"
new_case b-up-to-date; printf '%s\n' "202 dry-run" "202 delivered" "202 failed" > "$S/answers"
# the route recorded b's promote to this candidate before the run: its up-to-date path leaves promotedTo as it was
jq --arg n "$FOLLOWER" --arg f "$(cat "$S/from.b")" --arg t "$(cat "$S/to.b")" '.promoted[$n] = {promotedFrom: $f, promotedTo: $t, phase: "promoted"}' \
  "$S/route.json" > "$S/route.new" && mv "$S/route.new" "$S/route.json"
run
[ "$RC" = 1 ] && [ "$(puts)" = 3 ] && [ "$(grep '^PUT ' "$S/events" | grep -c -F '"rollback":true')" = 0 ] \
  && grep -q 'the run promoted no region; nothing is rolled back' "$S/out"
ok $? "b delivered without a new promote (promotedTo as before the request): b does not count as moved and is not rolled back when a fails"
new_case busy; printf '%s\n' "409 release-in-progress the release Job sovereign-release-0 is running" "202 dry-run" "202 delivered" "202 delivered" > "$S/answers"
run
[ "$RC" = 0 ] && [ "$(puts)" = 4 ] && put_has 1 '"dryRun":true' && put_has 2 '"dryRun":true' \
  && [ "$(grep -c -F 'the route runs another release Job (the release Job sovereign-release-0 is running); asking again' "$S/out")" = 1 ]
ok $? "while another release Job runs (409 release-in-progress) the same request is sent again, then the run goes on"

# 7b. a request whose Job does not end within the limit
new_case b-timeout-promotes; touch "$S/direct"; printf '%s\n' "202 dry-run" "202 running" "202 rolled-back" > "$S/answers"
run STANDING_ROUTE_MINUTES=0
[ "$RC" = 1 ] && [ "$(puts)" = 3 ] && put_has 3 "\"region\":\"$FOLLOWER\"" && put_has 3 '"rollback":true' \
  && [ "$(grep -c -F 'did not end within 0 minutes: it can still promote region b' "$S/out")" = 1 ] \
  && [ "$(grep -c -F 'region b runs the tree of 4444444444444444444444444444444444444444 again' "$S/out")" = 1 ] \
  && [ "$(grep -c -F 'the run promoted no region' "$S/out")" = 0 ]
ok $? "timeout: the Job for b does not end and promotes b later: b counts as moved and is rolled back to the tree its promote recorded"
new_case b-timeout-stalled; touch "$S/direct"
printf '%s\n' "202 dry-run" "202 stalled" "409 not-promoted no promote of region $FOLLOWER by rc/$ID is recorded; nothing moved" > "$S/answers"
run STANDING_ROUTE_MINUTES=0
[ "$RC" = 1 ] && [ "$(puts)" = 3 ] && put_has 3 '"rollback":true' \
  && [ "$(grep -c -F 'the route recorded no promote of region b' "$S/out")" = 1 ] && [ "$(grep -c -F 'a rollback did not end clean' "$S/out")" = 0 ]
ok $? "timeout: the Job for b does not end and promotes nothing: the rollback request is refused not-promoted and the run says nothing is put back"

# 8. usage
new_case usage
( export FAKE_DIR="$S" STANDING_KUBECONFIG_A="$S/ka" STANDING_KUBECONFIG_B="$S/kb" STANDING_PULL_FILE="$S/pull.json" \
    CANDIDATE_ID=rc-latest CANDIDATE_COMMIT="$COMMIT" ENV_LABEL=t1; bash "$SCRIPT" run ) > "$S/out" 2>&1
r1=$?
echo '{"auths": {}}' > "$S/nopull.json"
( export FAKE_DIR="$S" STANDING_KUBECONFIG_A="$S/ka" STANDING_KUBECONFIG_B="$S/kb" STANDING_PULL_FILE="$S/nopull.json" \
    CANDIDATE_ID="$ID" CANDIDATE_COMMIT="$COMMIT" ENV_LABEL=t1; bash "$SCRIPT" run ) > "$S/out2" 2>&1
r2=$?
[ "$r1" = 2 ] && [ "$r2" = 2 ] && [ ! -s "$S/events" ]
ok $? "usage: a candidate id of the wrong form, a pull credential without ghcr.io: exit 2, nothing called"

# 9. the credentials are printed nowhere
cat "$T"/case-*/events >> "$T/all-output" 2>/dev/null
! grep -q -e "$SESSION_MARK" -e "$PULL_MARK" -e "$(printf 'bot:%s' "$PULL_MARK" | base64)" -e "$TOKEN_MARK" -e KEY-OF-THE-SIGNER "$T/all-output"
ok $? "no case printed the session, the pull credential, the token of the snapshot or the signer"

echo "standing-update self-test: $((n - fails)) of $n cases passed"
[ "$fails" = 0 ]
