#!/usr/bin/env bash
# standing-update.sh - delivers a release candidate to the standing environment through the
# product's release route, one region per request, and puts back what it moved when a region does
# not take it (FND-076).
#
#   standing-update.sh run          the update; in a Job on the parent (gate-parent-job.sh run --mode update)
#   standing-update.sh --self-test  the cases of scripts/standing-update/self-test.sh: a fake kubectl, a
#                                   fake route, a fake standing-gate.py; no cluster, no network
#
# The route is PUT and GET /api/v1/sovereign/release on the console of the environment
# (products/catalyst/bootstrap/api/internal/lifecycle/release.go): stage and prewarm in every region,
# then promote the region the request names (SOV-521). Region a of the gate is the route's primary,
# region b its one follower, named as the keys of the Secret cutover-secondary-kubeconfigs in region a
# name it. The update moves a region's Git only through the route: it merges, pushes and copies
# nothing itself. In order:
#   1. Already at the candidate: scripts/standing-gate.py source passes for b and for a, and GET reports
#      the candidate delivered. Nothing is sent; exit 0.
#   2. For b, then a: the state before (every HelmRelease and Kustomization with Ready and its
#      revision, every Pod neither Running nor Succeeded) and the OpenBao raft snapshot. The snapshot
#      is saved in the region's OpenBao pod, on its data volume (STANDING_SNAPSHOT_DIR/pre-update.snap),
#      and read back as a gzip tar holding meta.json and state.bin; its size and sha256 are logged and
#      the file leaves neither the region nor the run. One that cannot be read back stops the run
#      before any request.
#   3. A dry run naming b. Any phase but dry-run stops the run with the route's message and the
#      reports of the regions (a stage conflict names its files); nothing moved. A route that does not
#      know the field region was installed before SOV-521: the run says so and stops.
#   4. b: the request naming b, with the credential {registry, username, password} of ghcr.io from
#      STANDING_PULL_FILE (the route signs in with it and keeps it in the pull Secret it makes for that
#      delivery only; a dry run and a rollback carry none), the route's final
#      phase, Flux of b applies the candidate (standing-gate.py source), every HelmRelease is Ready
#      (standing-gate.py converge), and b compares clean with its state before: no HelmRelease or
#      Kustomization that was Ready is now not Ready, and no more Pods are neither Running nor Succeeded.
#   5. a, the same way, only after b converged and compared clean.
#   6. A failure after the route promoted a region in this run: one rollback request for each region the
#      run promoted, primary first; then a wait until Flux of that region applies a commit whose tree is
#      the tree of the main the promote recorded, and every HelmRelease is Ready; exit 1. A request whose
#      Job does not end within STANDING_ROUTE_MINUTES counts as a promote, since that Job can still
#      promote: its rollback request waits while the Job runs, and the route's not-promoted means it
#      moved nothing.
# Every wait reads once a minute (STANDING_POLL_SECONDS). The session that calls the route is minted in
# the run from region a's signer, with the issuer and the signer's path that catalyst-api of region a
# states; a failed or empty read stops the run before any request. The session, the pull
# credential and the token of the snapshot stay in files of mode 600 in the run's scratch folder and are
# printed nowhere.
#
# Environment of run:
#   STANDING_KUBECONFIG_A, _B   the kubeconfigs of region a (the route's primary) and b (its follower)
#   STANDING_PULL_FILE          a dockerconfigjson with an auth for ghcr.io: each delivery's credential
#   CANDIDATE_ID, CANDIDATE_COMMIT, ENV_LABEL   the candidate rc/<id>, its commit, the environment's label
#   GATE_RUN                    the run, in the e-mail address of the run's session (default 0)
#   STANDING_SUMMARY            optional: a file the summary is appended to, as Markdown
#   Optional: STANDING_CONSOLE (default: the issuer catalyst-api of region a states), STANDING_NAMESPACE
#   (catalyst, the namespace of the route's Jobs), STANDING_FOLLOWER (default: read in region a),
#   STANDING_GATE (an executable that stands for standing-gate.py), STANDING_POLL_SECONDS (60),
#   STANDING_ROUTE_MINUTES (90), STANDING_SOURCE_MINUTES (20), STANDING_CONVERGE_MINUTES (30),
#   STANDING_SNAPSHOT_DIR (/openbao/data/standing-update), STANDING_OPENBAO_NAMESPACE (openbao),
#   STANDING_OPENBAO_POD (openbao-0), STANDING_OPENBAO_CONTAINER (openbao), STANDING_OPENBAO_AUTH
#   (kubernetes), STANDING_OPENBAO_ROLE (openbao-snapshot: the login role and its ServiceAccount).
#
# Exit codes: 0 updated, or already at the candidate; 1 failed (what the run moved was put back as far as
# the route let it); 2 wrong usage, nothing sent.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SELF="$HERE/$(basename "${BASH_SOURCE[0]}")"
ROUTE_PATH=/api/v1/sovereign/release
ID_RE='^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{7}$'
FINAL=' dry-run delivered rolled-back conflict refused moved failed partial '
REPO_DIR=/data/git/gitea-repositories/openova/openova.git
GITEA_SELECTOR=app.kubernetes.io/name=gitea,app.kubernetes.io/instance=gitea

say() { echo "standing-update: $*"; }
summ() { if [ -n "${STANDING_SUMMARY:-}" ]; then echo "$*" >> "$STANDING_SUMMARY"; fi; return 0; }
usage() { sed -n '2,/^# Exit codes/p' "$SELF" >&2; exit 2; }

kc_of() { if [ "$1" = a ]; then echo "$KA"; else echo "$KB"; fi; }
k() { local r="$1"; shift; kubectl --kubeconfig "$(kc_of "$r")" --request-timeout=120s "$@"; }
kx() { local r="$1"; shift; kubectl --kubeconfig "$(kc_of "$r")" "$@"; } # exec: no request timeout
gate_py() { if [ -n "${STANDING_GATE:-}" ]; then "$STANDING_GATE" "$@"; else python3 "$HERE/standing-gate.py" "$@"; fi; }
route_name() { if [ "$1" = a ]; then echo primary; else echo "$FOLLOWER"; fi; }
err_line() { tail -n 1 "$W/err" 2>/dev/null | cut -c1-300; }

# ---------------------------------------------------------------- the session and the route
# mint_session: the sovereign-admin session of the run, from region a's handover signer, into a header
# file of mode 600. The issuer and the signer's path are the ones catalyst-api of region a states; a
# failed or empty read ends it with the cause in W/err. CONSOLE is the route's host: STANDING_CONSOLE,
# else the issuer of the session.
mint_session() {
  local issuer signer
  issuer="$(k a -n catalyst-system get deploy catalyst-api \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_PIN_ISSUER")].value}' 2> "$W/err")" || return 1
  signer="$(k a -n catalyst-system get deploy catalyst-api \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="CATALYST_HANDOVER_SIGNER_PATH")].value}' 2> "$W/err")" || return 1
  [ -n "$issuer" ] || { echo "catalyst-system/catalyst-api of region a states no CATALYST_PIN_ISSUER" > "$W/err"; return 1; }
  [ -n "$signer" ] || { echo "catalyst-system/catalyst-api of region a states no CATALYST_HANDOVER_SIGNER_PATH" > "$W/err"; return 1; }
  CONSOLE="${STANDING_CONSOLE:-$issuer}"
  CONSOLE="${CONSOLE%/}"
  ( umask 077
    tok="$(kx a -n catalyst-system exec deploy/catalyst-api -- cat "$signer" 2> "$W/err" \
      | sovereign-acceptance mint-session --key - --issuer "$issuer" --email "standing-update-$RUN_ID@$ENV_LABEL.omani.works" --ttl 8h 2>> "$W/err")" || exit 1
    [ -n "$tok" ] || exit 1
    printf 'Cookie: catalyst_session=%s\n' "$tok" > "$W/cookie" )
}

# route GET | route PUT <body file>: one call; ROUTE_CODE is the HTTP code (or "none: <why>"), the
# answer is in W/answer.
route() {
  local a=(-sS --max-time 60 -o "$W/answer" -w '%{http_code}' -H "@$W/cookie" -H 'Accept: application/json')
  [ "$1" = PUT ] && a+=(-X PUT -H 'Content-Type: application/json' --data-binary "@$2")
  : > "$W/answer"
  ROUTE_CODE="$(curl "${a[@]}" "$CONSOLE$ROUTE_PATH" 2> "$W/err")" || ROUTE_CODE="none: $(err_line)"
}

ans() { jq -r "$@" "$W/answer" 2>/dev/null; }

# route_message: the route's phase, step, message and the report of each region, from W/answer.
route_message() {
  ans '"phase " + (.phase // "none") + (if .failedStep then ", step " + .failedStep else "" end) + ": " + (.status.message // "no message")'
  ans '.status // {} | to_entries[] | select(.key | endswith(".report")) | select(.value != "") | "  " + .key + ": " + (.value | tostring)' | cut -c1-800
}

# send <body file>: PUT; waits while another release Job of the route runs. SENT_JOB on 202, else
# REFUSAL names why, and nothing was started.
send() {
  local deadline err detail
  SENT_JOB=""; REFUSAL=""
  deadline=$(( $(date +%s) + ROUTE_MINUTES * 60 ))
  while :; do
    route PUT "$1"
    if [ "$ROUTE_CODE" = 202 ]; then
      SENT_JOB="$(ans '.job // ""')"
      [ -n "$SENT_JOB" ] && return 0
      REFUSAL="the route answered 202 and named no Job"; return 1
    fi
    err="$(ans '.error // ""')"; detail="$(ans '.detail // ""' | cut -c1-600)"
    if [ "$ROUTE_CODE" = 409 ] && [ "$err" = release-in-progress ] && [ "$(date +%s)" -lt "$deadline" ]; then
      say "the route runs another release Job ($detail); asking again in a minute"
      sleep "$POLL"; continue
    fi
    case "$detail" in
      *'unknown field "region"'* | *'unknown field "rollback"'*)
        REFUSAL="the route of this environment was installed before SOV-521: it refuses a request that names a region (HTTP $ROUTE_CODE $err: $detail). One whole delivery (a request with no region) of a candidate that carries SOV-521 installs the route that takes one; then run the update again. The route started nothing" ;;
      *) REFUSAL="HTTP $ROUTE_CODE${err:+ $err}${detail:+: $detail}; the route started nothing" ;;
    esac
    return 1
  done
}

# wait_route <job>: reads GET once a minute until the route's Job <job> reached a final phase; PHASE
# is that phase, or timeout. The last answer stays in W/answer.
wait_route() {
  local job="$1" deadline last="" p j step
  deadline=$(( $(date +%s) + ROUTE_MINUTES * 60 ))
  PHASE=""
  while :; do
    route GET
    if [ "$ROUTE_CODE" = 200 ]; then
      j="$(ans '.job // ""')"; p="$(ans '.phase // ""')"; step="$(ans '.step // ""')"
      if [ "$j" = "$job" ]; then
        case "$FINAL" in *" $p "*) PHASE="$p"; return 0 ;; esac
        [ "$p/$step" != "$last" ] && say "the route's Job $job: phase $p${step:+, step $step}"
        last="$p/$step"
      else
        say "the route reports the Job ${j:-none}, not $job; reading again in a minute"
      fi
    else
      say "the route could not be read (HTTP $ROUTE_CODE); reading again in a minute"
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then PHASE=timeout; return 0; fi
    sleep "$POLL"
  done
}

body() { # body <file> <jq filter> [<region>]: the request's JSON, mode 600; $k[0] is the credential
  ( umask 077; jq -n --arg c "$CANDIDATE_ID" --arg r "${3:-}" --slurpfile k "$W/credential" "$2" > "$1" )
}

# ---------------------------------------------------------------- the regions
# follower_name: FOLLOWER, the route's name of region b.
follower_name() {
  local keys n
  if [ -n "${STANDING_FOLLOWER:-}" ]; then FOLLOWER="$STANDING_FOLLOWER"; return 0; fi
  keys="$(k a -n "$NS" get secret cutover-secondary-kubeconfigs -o go-template='{{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}' 2> "$W/err")" \
    || { say "the follower regions could not be read in region a (Secret $NS/cutover-secondary-kubeconfigs): $(err_line)"; return 1; }
  FOLLOWER="$(printf '%s\n' "$keys" | sed -n 's/\.yaml$//p')"
  n="$(printf '%s' "$FOLLOWER" | grep -c .)"
  [ "$n" = 1 ] || { say "region a names $n follower regions in $NS/cutover-secondary-kubeconfigs, not 1: the update knows two regions"; return 1; }
}

# state_of <region> <file>: every HelmRelease and Kustomization with Ready and its revision, every Pod
# neither Running nor Succeeded.
state_of() {
  local r="$1" f="$2" ready
  ready='(([.status.conditions[]? | select(.type == "Ready") | .status] | first) // "Unknown")'
  { k "$r" get helmreleases.helm.toolkit.fluxcd.io -A -o json 2> "$W/err" | jq -r '.items[] | "HelmRelease " + .metadata.namespace + "/" + .metadata.name + " " + '"$ready"' + " " + ((.status.lastAppliedRevision // .status.history[0].chartVersion // "-") | tostring)' || return 1
    k "$r" get kustomizations.kustomize.toolkit.fluxcd.io -A -o json 2> "$W/err" | jq -r '.items[] | "Kustomization " + .metadata.namespace + "/" + .metadata.name + " " + '"$ready"' + " " + ((.status.lastAppliedRevision // "-") | tostring)' || return 1
    k "$r" get pods -A -o json 2> "$W/err" | jq -r '.items[] | select(.status.phase != "Running" and .status.phase != "Succeeded") | "Pod " + .metadata.namespace + "/" + .metadata.name + " " + (.status.phase // "Unknown")' || return 1
  } > "$f"
}

count_of() { # count_of <file> <kind> [Ready]: lines of that kind (and Ready True)
  if [ -n "${3:-}" ]; then awk -v k="$2" '$1 == k && $3 == "True"' "$1" | wc -l | tr -d ' '; else awk -v k="$2" '$1 == k' "$1" | wc -l | tr -d ' '; fi
}

describe() { # describe <file>: one line
  echo "$(count_of "$1" HelmRelease) HelmReleases ($(count_of "$1" HelmRelease Ready) Ready), $(count_of "$1" Kustomization) Kustomizations ($(count_of "$1" Kustomization Ready) Ready), $(count_of "$1" Pod) Pods neither Running nor Succeeded"
}

# compare <region>: 0 when the state after is not worse than the state before; names each difference.
compare() {
  local r="$1" bf="$W/$1.before" af="$W/$1.after" worse=0 kind key st now pb pa
  state_of "$r" "$af" || { say "the state after of region $r could not be read: $(err_line)"; return 1; }
  while read -r kind key st _; do
    { [ "$kind" = HelmRelease ] || [ "$kind" = Kustomization ]; } && [ "$st" = True ] || continue
    now="$(awk -v k="$kind" -v n="$key" '$1 == k && $2 == n {print $3; exit}' "$af")"
    if [ -z "$now" ]; then say "region $r: the $kind $key was Ready before and is gone now"; continue; fi
    [ "$now" = True ] || { say "region $r: the $kind $key was Ready before and is $now now"; worse=1; }
  done < "$bf"
  pb="$(count_of "$bf" Pod)"; pa="$(count_of "$af" Pod)"
  if [ "$pa" -gt "$pb" ]; then
    say "region $r: $pa Pods are neither Running nor Succeeded, $pb before:"
    awk '$1 == "Pod" {print "  " $2 " " $3}' "$af" | head -n 30
    worse=1
  fi
  say "region $r after: $(describe "$af")"
  return $worse
}

# snapshot <region>: the OpenBao raft snapshot, saved in the region's OpenBao pod and read back here.
snapshot() {
  local r="$1" f="$W/$1.snap" list size sha
  ( umask 077; k "$r" -n "$OB_NS" create token "$OB_ROLE" --duration=10m > "$W/$r.token" 2> "$W/err" ) \
    || { say "the OpenBao snapshot of region $r: no token of the ServiceAccount $OB_NS/$OB_ROLE: $(err_line)"; rm -f "$W/$r.token"; return 1; }
  # shellcheck disable=SC2016 # expanded by the shell in the OpenBao pod
  kx "$r" -n "$OB_NS" exec -i "$OB_POD" -c "$OB_CONTAINER" -- sh -c '
    set -e
    BAO_TOKEN="$(bao write -field=token "auth/$1/login" role="$2" jwt=-)"
    export BAO_TOKEN
    mkdir -p "$3"
    bao operator raft snapshot save "$3/pre-update.snap.new" >/dev/null
    cat "$3/pre-update.snap.new"' sh "$OB_AUTH" "$OB_ROLE" "$OB_DIR" < "$W/$r.token" > "$f" 2> "$W/err"
  local rc=$?
  rm -f "$W/$r.token"
  [ "$rc" = 0 ] || { say "the OpenBao snapshot of region $r could not be taken in $OB_NS/$OB_POD: $(err_line)"; return 1; }
  list="$(tar -tzf "$f" 2> /dev/null)" || list=""
  if ! grep -Eqx '(\./)?meta\.json' <<< "$list" || ! grep -Eqx '(\./)?state\.bin' <<< "$list"; then
    say "the OpenBao snapshot of region $r does not read back as a gzip tar holding meta.json and state.bin; no request was sent"
    return 1
  fi
  kx "$r" -n "$OB_NS" exec "$OB_POD" -c "$OB_CONTAINER" -- mv "$OB_DIR/pre-update.snap.new" "$OB_DIR/pre-update.snap" 2> "$W/err" \
    || { say "the OpenBao snapshot of region $r could not be kept as $OB_DIR/pre-update.snap: $(err_line)"; return 1; }
  size="$(wc -c < "$f" | tr -d ' ')"; sha="$(sha256sum < "$f" | cut -c1-64)"
  rm -f "$f"
  say "the OpenBao snapshot of region $r: $size bytes, sha256 $sha, kept in $OB_NS/$OB_POD:$OB_DIR/pre-update.snap"
  summ "- OpenBao snapshot of region $r: $size bytes, sha256 \`$sha\`, kept in the region at \`$OB_NS/$OB_POD:$OB_DIR/pre-update.snap\`"
}

# wait_source <region>: until Flux of the region applied the candidate commit or a descendant.
wait_source() {
  local r="$1" deadline last=""
  deadline=$(( $(date +%s) + SOURCE_MINUTES * 60 ))
  while :; do
    if gate_py source --kubeconfig "$(kc_of "$r")" --region "$r" --candidate "$CANDIDATE_COMMIT" > "$W/gate.out" 2>&1; then
      say "$(tail -n 1 "$W/gate.out")"; return 0
    fi
    [ "$(tail -n 1 "$W/gate.out")" != "$last" ] && say "$(tail -n 1 "$W/gate.out"); reading again in a minute"
    last="$(tail -n 1 "$W/gate.out")"
    [ "$(date +%s)" -ge "$deadline" ] && { say "region $r: Flux did not apply the candidate within $SOURCE_MINUTES minutes"; return 1; }
    sleep "$POLL"
  done
}

converge() { # converge <region>
  gate_py converge --kubeconfig "$(kc_of "$1")" --region "$1" --minutes "$CONVERGE_MINUTES" --interval "$POLL_GATE" 2>&1 | sed 's/^/  /'
  [ "${PIPESTATUS[0]}" = 0 ]
}

# wait_tree <region> <from> <to>: until Flux of the region applied a commit other than <to> whose tree is
# the tree of <from>, read in the region's own Gitea.
wait_tree() {
  local r="$1" from="$2" to="$3" deadline applied sha pod trees
  deadline=$(( $(date +%s) + SOURCE_MINUTES * 60 ))
  while :; do
    applied="$(k "$r" -n flux-system get kustomization bootstrap-kit -o jsonpath='{.status.lastAppliedRevision}' 2> "$W/err")" || applied=""
    sha="$(printf '%s' "$applied" | grep -oE '[0-9a-f]{40}$')"
    if [ -n "$sha" ] && [ "$sha" != "$to" ]; then
      pod="$(k "$r" -n gitea get pods -l "$GITEA_SELECTOR" -o jsonpath='{.items[0].metadata.name}' 2> "$W/err")" || pod=""
      trees="$(kx "$r" -n gitea exec "$pod" -c gitea -- git -C "$REPO_DIR" rev-parse "$sha^{tree}" "$from^{tree}" 2> "$W/err")" || trees=""
      if [ -n "$trees" ] && [ "$(printf '%s\n' "$trees" | sort -u | grep -c .)" = 1 ]; then
        say "region $r: Flux applied $sha, whose tree is the tree of $from"; return 0
      fi
    fi
    [ "$(date +%s)" -ge "$deadline" ] && { say "region $r: Flux did not apply the tree of $from within $SOURCE_MINUTES minutes (applied: ${applied:-not read})"; return 1; }
    sleep "$POLL"
  done
}

# ---------------------------------------------------------------- delivery and rollback
# deliver <region>: the request for the region, then its convergence and comparison. The route makes
# the delivery's pull Secret from the request's credential and deletes it when the delivery ends.
deliver() {
  local r="$1" name prev
  name="$(route_name "$r")"
  route GET
  prev="$( [ "$ROUTE_CODE" = 200 ] && ans --arg n "$name" '.promoted[$n].promotedTo // ""')"
  body "$W/request.json" '{candidate: $c, region: $r, credential: $k[0]}' "$name"
  say "the request for region $r (the route's $name): deliver rc/$CANDIDATE_ID"
  summ "- request for region $r (the route's $name)"
  if ! send "$W/request.json"; then say "the route refused the request for region $r: $REFUSAL"; return 1; fi
  wait_route "$SENT_JOB"
  if [ "$PHASE" = timeout ]; then # the Job still runs and can promote the region after this
    MOVED="$MOVED $r"
    say "the route's Job $SENT_JOB did not end within $ROUTE_MINUTES minutes: it can still promote region $r, so region $r counts as moved"
    summ "- the route's Job for region $r did not end within $ROUTE_MINUTES minutes"
    return 1
  fi
  if [ "$(ans --arg n "$name" '.promoted[$n].phase // ""')" = promoted ] && [ "$(ans --arg n "$name" '.promoted[$n].promotedTo // ""')" != "$prev" ]; then
    MOVED="$MOVED $r"
    printf -v "FROM_$r" '%s' "$(ans --arg n "$name" '.promoted[$n].promotedFrom // ""')"
    printf -v "TO_$r" '%s' "$(ans --arg n "$name" '.promoted[$n].promotedTo // ""')"
  fi
  if [ "$PHASE" != delivered ]; then
    say "the route ended the request for region $r: $(route_message | head -n 1)"
    route_message | tail -n +2
    summ "- the route ended the request for region $r with phase $PHASE"
    return 1
  fi
  say "the route delivered rc/$CANDIDATE_ID to region $r"
  wait_source "$r" || return 1
  converge "$r" || { say "region $r did not converge within $CONVERGE_MINUTES minutes"; return 1; }
  compare "$r" || { say "region $r compares worse than its state before"; summ "- region $r compares worse than its state before"; return 1; }
  say "region $r converged and compared clean"
  summ "- region $r converged and compared clean: $(describe "$W/$r.after")"
}

# rollback_moved: one rollback request for each region this run promoted, primary first. A region whose
# Job did not end (no FROM yet) is put back from what the promote recorded; its request waits while that
# Job runs, and the route's not-promoted means that Job moved nothing.
rollback_moved() {
  local r name from to rc=0
  for r in a b; do
    case " $MOVED " in *" $r "*) ;; *) continue ;; esac
    name="$(route_name "$r")"; from="FROM_$r"; to="TO_$r"; from="${!from:-}"; to="${!to:-}"
    say "the rollback request for region $r (the route's $name)${from:+: back to the tree of $from}"
    summ "- rollback request for region $r (the route's $name)"
    body "$W/rollback.json" '{candidate: $c, region: $r, rollback: true}' "$name"
    if ! send "$W/rollback.json"; then
      if [ -z "$from" ] && [ "$(ans '.error // ""')" = not-promoted ]; then
        say "the route recorded no promote of region $r by rc/$CANDIDATE_ID: its Job moved nothing, nothing is put back"
        summ "- region $r: the route recorded no promote, nothing is put back"
        continue
      fi
      say "the route refused the rollback of region $r: $REFUSAL"; rc=1; continue
    fi
    wait_route "$SENT_JOB"
    if [ "$PHASE" != rolled-back ]; then say "the rollback of region $r ended: $(route_message | head -n 1)"; rc=1; continue; fi
    [ -n "$from" ] || from="$(ans --arg n "$name" '.promoted[$n].promotedFrom // ""')"
    [ -n "$to" ] || to="$(ans --arg n "$name" '.promoted[$n].promotedTo // ""')"
    [ -n "$from" ] || { say "the route rolled region $r back and names no main its promote recorded"; rc=1; continue; }
    wait_tree "$r" "$from" "$to" || { rc=1; continue; }
    converge "$r" || { say "region $r did not converge on the tree of $from within $CONVERGE_MINUTES minutes"; rc=1; continue; }
    say "region $r runs the tree of $from again and has converged"
    summ "- region $r runs the tree of \`$from\` again and has converged"
  done
  return $rc
}

fail() { # fail <message>: rolls back what the run moved, then exit 1
  say "FAILED: $*"
  summ "- FAILED: $*"
  if [ -n "${MOVED// /}" ]; then
    rollback_moved || say "a rollback did not end clean; the lines above name the region"
  else
    say "the run promoted no region; nothing is rolled back"
  fi
  say "result: failed"
  exit 1
}

cleanup() { rm -rf "$W"; }

# ---------------------------------------------------------------- run
cmd_run() {
  local v r
  for v in STANDING_KUBECONFIG_A STANDING_KUBECONFIG_B STANDING_PULL_FILE; do
    [ -r "${!v:-}" ] || { echo "standing-update: $v is not a readable file" >&2; exit 2; }
  done
  [[ "${CANDIDATE_ID:-}" =~ $ID_RE ]] || { echo "standing-update: CANDIDATE_ID must be <yyyymmddThhmmssZ>-<7 hex>, not '${CANDIDATE_ID:-}'" >&2; exit 2; }
  [[ "${CANDIDATE_COMMIT:-}" =~ ^[0-9a-f]{40}$ ]] || { echo "standing-update: CANDIDATE_COMMIT must be 40 hex" >&2; exit 2; }
  [ "${CANDIDATE_COMMIT:0:7}" = "${CANDIDATE_ID##*-}" ] || { echo "standing-update: CANDIDATE_COMMIT does not start with the 7 hex of CANDIDATE_ID" >&2; exit 2; }
  [[ "${ENV_LABEL:-}" =~ ^[a-z0-9-]+$ ]] || { echo "standing-update: ENV_LABEL is not set" >&2; exit 2; }
  jq -e '.auths["ghcr.io"].auth | @base64d | test("^[^:]+:.")' "$STANDING_PULL_FILE" > /dev/null 2>&1 \
    || { echo "standing-update: STANDING_PULL_FILE has no auth <user>:<password> for ghcr.io" >&2; exit 2; }
  KA="$STANDING_KUBECONFIG_A"; KB="$STANDING_KUBECONFIG_B"
  RUN_ID="$(printf '%s' "${GATE_RUN:-0}" | tr -cd '0-9a-z')"; RUN_ID="${RUN_ID:-0}"
  NS="${STANDING_NAMESPACE:-catalyst}"
  POLL="${STANDING_POLL_SECONDS:-60}"; POLL_GATE="$POLL"; [ "$POLL_GATE" -ge 1 ] 2>/dev/null || POLL_GATE=1
  ROUTE_MINUTES="${STANDING_ROUTE_MINUTES:-90}"; SOURCE_MINUTES="${STANDING_SOURCE_MINUTES:-20}"; CONVERGE_MINUTES="${STANDING_CONVERGE_MINUTES:-30}"
  OB_NS="${STANDING_OPENBAO_NAMESPACE:-openbao}"; OB_POD="${STANDING_OPENBAO_POD:-openbao-0}"
  OB_CONTAINER="${STANDING_OPENBAO_CONTAINER:-openbao}"; OB_AUTH="${STANDING_OPENBAO_AUTH:-kubernetes}"
  OB_ROLE="${STANDING_OPENBAO_ROLE:-openbao-snapshot}"; OB_DIR="${STANDING_SNAPSHOT_DIR:-/openbao/data/standing-update}"
  MOVED=""
  W="$(mktemp -d)"
  trap cleanup EXIT
  ( umask 077; jq '.auths["ghcr.io"].auth | @base64d | index(":") as $i
      | {registry: "ghcr.io", username: .[:$i], password: .[$i + 1:]}' "$STANDING_PULL_FILE" > "$W/credential" ) \
    || fail "the credential of ghcr.io could not be read from STANDING_PULL_FILE; no request was sent"
  summ "### Update of the standing environment $ENV_LABEL to rc/$CANDIDATE_ID"
  summ ""
  mint_session || fail "the session of the run could not be minted from region a's signer: $(err_line)"
  follower_name || fail "the route's name of region b is not known"
  say "region a is the route's primary, region b its follower $FOLLOWER; the route: $CONSOLE$ROUTE_PATH"

  # 1. already at the candidate
  local at=yes
  for r in b a; do
    if gate_py source --kubeconfig "$(kc_of "$r")" --region "$r" --candidate "$CANDIDATE_COMMIT" > "$W/gate.out" 2>&1; then
      say "$(tail -n 1 "$W/gate.out")"
    else
      say "$(tail -n 1 "$W/gate.out")"; at=no
    fi
  done
  route GET
  [ "$ROUTE_CODE" = 200 ] || fail "the route could not be read (HTTP $ROUTE_CODE); no request was sent"
  if [ "$at" = yes ] && [ "$(ans '.status.candidate // ""')" = "$CANDIDATE_ID" ] && [ "$(ans '.phase // ""')" = delivered ]; then
    say "both regions run rc/$CANDIDATE_ID and the route reports it delivered: no request is sent"
    summ "- already at rc/$CANDIDATE_ID: both regions run it and the route reports it delivered; no request was sent"
    say "result: already-at"
    exit 0
  fi

  # 2. the state before and the snapshot of each region
  for r in b a; do
    state_of "$r" "$W/$r.before" || fail "the state before of region $r could not be read: $(err_line); no request was sent"
    say "the state before of region $r: $(describe "$W/$r.before")"
    summ "- state before, region $r: $(describe "$W/$r.before")"
    snapshot "$r" || fail "no OpenBao snapshot of region $r was read back; no request was sent"
  done

  # 3. the dry run
  body "$W/dry.json" '{candidate: $c, region: $r, dryRun: true}' "$FOLLOWER"
  say "the dry run of rc/$CANDIDATE_ID, naming region b (the route's $FOLLOWER)"
  send "$W/dry.json" || fail "the route refused the dry run: $REFUSAL"
  wait_route "$SENT_JOB"
  if [ "$PHASE" != dry-run ]; then
    say "the route's dry run: $(route_message | head -n 1)"
    route_message | tail -n +2
    [ "$PHASE" != conflict ] || say "the route's stage met a conflict: the candidate changes a file that a region's own Git changed after its cutover (each report above names it); the update does not resolve it"
    fail "the route's dry run ended with phase $PHASE, not dry-run: $(ans '.status.message // "no message"' | cut -c1-600); nothing moved"
  fi
  say "the route's dry run: $(ans '.status.message // "no message"' | cut -c1-600)"
  summ "- dry run: $(ans '.status.message // "no message"' | cut -c1-300)"

  # 4. and 5. the follower, then the primary
  deliver b || fail "region b did not take rc/$CANDIDATE_ID"
  deliver a || fail "region a did not take rc/$CANDIDATE_ID"
  say "rc/$CANDIDATE_ID is delivered to both regions, which converged and compared clean"
  summ "- rc/$CANDIDATE_ID is delivered to both regions"
  say "result: updated"
  exit 0
}

case "${1:-}" in
  run) cmd_run ;;
  --self-test) bash "$HERE/standing-update/self-test.sh" "$SELF" ;;
  *) usage ;;
esac
