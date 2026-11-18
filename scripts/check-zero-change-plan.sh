#!/usr/bin/env bash
# check-zero-change-plan.sh - gate check CLD-G12 (work package CLD-094): the infrastructure plan of
# a provisioned deployment shows no change.
#
#   check-zero-change-plan.sh run --deployment <id> --candidate <dir> --result <file>
#                             [--kubeconfig <file>] [--namespace <ns>] [--target <pod or deploy/name>]
#                             [--transcript <file>] [--regenerate-bin <file>]
#   check-zero-change-plan.sh judge <transcript> <result file>
#   check-zero-change-plan.sh --self-test
#
# run   Works inside the engine Pod of the parent that provisioned the deployment (kubectl exec;
#       default namespace catalyst, target deploy/catalyst-api), because only that Pod mounts the
#       deployment's working directory and record and holds the cloud credentials. It streams in
#       the module of the deployment's provider, the shared templates and the cloud profiles from
#       <dir> (the commit under test) and the command tfvars-regenerate built from <dir>; there it
#         copy        copies the working directory of <id> into a temporary directory, without
#                     .terraform, and stages the module of <dir> in it the way the engine does
#                     (the module's *.tf and *.tftpl files, those of seal/, _shared beside it);
#                     the module files the copy held before are removed first;
#         regenerate  writes the variables file again from the deployment record with
#                     tfvars-regenerate, which validates it against the cloud profiles of <dir>
#                     and prints only the names of the keys that changed;
#         init        tofu init, with the engine's provider plugin cache (at most 15 minutes);
#         plan        tofu plan -detailed-exitcode -lock=false (at most 30 minutes).
#       The state and the working directory are never written: every step works on the copy, and
#       the copy is removed at the end. Only the step markers, the changed key names, the error
#       summaries and the plan's action lines (address and action, no value) leave the Pod.
#       The transcript is judged into the result file (below). Exit 0 PASS, 1 FAIL, 2 usage error.
# judge Reads a transcript and writes the result file that
#       scripts/acceptance-gate.py merge-check --check CLD-G12 adds to a gate record:
#       {"id": "CLD-G12", "status": "PASS" or "FAIL", "steps": [...]}. PASS needs all four steps
#       to end with exit code 0 and the plan to print "No changes.". A plan with changes (exit
#       code 2) is a FAIL that names the counts and lists every planned action; nothing is
#       subtracted or ignored. A step that did not start or did not finish is a FAIL, and so is a
#       transcript without any step: a missing measurement is never a pass.
# --self-test  Runs the remote part against a fake engine (a fake kubectl, tofu and
#       tfvars-regenerate) and judges every kind of transcript; each rule is shown to fail.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# The part that runs inside the engine Pod: POSIX sh, busybox tools. $1 is the deployment id;
# standard input is a tar of module/, _shared/, providers/ (the cloud profiles) and
# bin/tfvars-regenerate.
read -r -d '' REMOTE <<'SH' || true
set -u
id="$1"
work="${CATALYST_TOFU_WORKDIR:-/var/lib/catalyst/tofu}"
recs="${CATALYST_DEPLOYMENTS_DIR:-/var/lib/catalyst/deployments}"
cache="${CATALYST_TF_PLUGIN_CACHE_DIR:-/var/lib/catalyst/tofu-plugin-cache}"
step=none
say() { printf 'CLD-G12 %s\n' "$*"; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
begin() { step="$1"; say "begin $1 $(now)"; }
finish() { say "end $step $1 $(now)"; }
lines() { while IFS= read -r l; do say "line $step $l"; done; }
errors() { grep 'Error: ' "$1" | sed 's/^[^E]*Error: /Error: /' | head -20 | lines; }
limit() { if command -v timeout > /dev/null 2>&1; then timeout "$@"; else shift; "$@"; fi; }
begin copy
t="$(mktemp -d "${TMPDIR:-/tmp}/cld-g12.XXXXXX")" || { echo "no temporary directory" | lines; finish 1; exit 0; }
# The copy holds the state and the credentials: it goes on every way out. A dropped exec stream
# reaches this shell as HUP, or as PIPE at its next write.
trap 'rm -rf "$t"' EXIT
trap 'rm -rf "$t"; exit 1' HUP INT PIPE TERM
src="$work/$id"
dst="$t/w/$id"
rc=0
if [ ! -f "$src/tofu.auto.tfvars.json" ]; then
  echo "the working directory $src holds no tofu.auto.tfvars.json" | lines; rc=1
elif [ ! -f "$recs/$id.json" ]; then
  echo "the deployment record $recs/$id.json does not exist" | lines; rc=1
elif ! mkdir -p "$t/in" "$dst" "$t/w/_shared" || ! tar -xf - -C "$t/in"; then
  echo "the files of the commit under test could not be unpacked" | lines; rc=1
else
  for f in "$src"/* "$src"/.[!.]*; do
    [ -e "$f" ] || continue
    [ "${f##*/}" = .terraform ] && continue
    cp -a "$f" "$dst/" || { echo "${f##*/} could not be copied" | lines; rc=1; }
  done
  rm -f "$dst"/*.tf "$dst"/*.tftpl "$dst"/seal/*.tf "$dst"/seal/*.tftpl
  for f in "$t/in/module"/*.tf "$t/in/module"/*.tftpl; do
    [ -e "$f" ] && { cp "$f" "$dst/" || rc=1; }
  done
  if [ -d "$t/in/module/seal" ]; then
    mkdir -p "$dst/seal" || rc=1
    for f in "$t/in/module/seal"/*.tf "$t/in/module/seal"/*.tftpl; do
      [ -e "$f" ] && { cp "$f" "$dst/seal/" || rc=1; }
    done
  fi
  for f in "$t/in/_shared"/*.tf "$t/in/_shared"/*.tftpl; do
    [ -e "$f" ] && { cp "$f" "$t/w/_shared/" || rc=1; }
  done
  [ -f "$dst/main.tf" ] || { echo "the module of the commit under test has no main.tf" | lines; rc=1; }
fi
finish "$rc"
[ "$rc" = 0 ] || exit 0
begin regenerate
if [ -f "$t/in/providers/_contract/cloudprofile.schema.json" ]; then
  "$t/in/bin/tfvars-regenerate" -record "$recs/$id.json" -dir "$dst" -providers "$t/in/providers" > "$t/out" 2>&1
else
  "$t/in/bin/tfvars-regenerate" -record "$recs/$id.json" -dir "$dst" > "$t/out" 2>&1
fi
rc=$?
lines < "$t/out"
finish "$rc"
[ "$rc" = 0 ] || exit 0
begin init
cd "$dst" || { finish 1; exit 0; }
export TF_INPUT=false TF_IN_AUTOMATION=true
if [ -d "$cache" ]; then export TF_PLUGIN_CACHE_DIR="$cache"; fi
limit 900 tofu init -input=false -no-color > "$t/out" 2>&1
rc=$?
errors "$t/out"
finish "$rc"
[ "$rc" = 0 ] || exit 0
begin plan
limit 1800 tofu plan -detailed-exitcode -lock=false -input=false -no-color > "$t/out" 2>&1
rc=$?
grep -E '^  # .* (will|must) be ' "$t/out" | head -400 | lines
grep -E '^Plan: |^No changes\.' "$t/out" | head -1 | lines
errors "$t/out"
finish "$rc"
SH

# judge <transcript> <result file>
judge() {
  python3 - "$1" "$2" <<'PY'
import datetime, json, re, sys

transcript, out = sys.argv[1], sys.argv[2]
ORDER = ["copy", "regenerate", "init", "plan"]
NAMES = {
    "reach": "reach the engine Pod of the parent",
    "copy": "copy the working directory and stage the module of the commit under test",
    "regenerate": "regenerate the variables file from the deployment record",
    "init": "tofu init in the copy",
    "plan": "tofu plan -detailed-exitcode shows no change",
}
now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
try:
    text = open(transcript, encoding="utf-8", errors="replace").read().splitlines()
except OSError as e:
    text = ["CLD-G12 abort the transcript could not be read: %s" % e]
began, ended, said, other, aborts = {}, {}, {}, [], []
for raw in text:
    m = re.match(r"^CLD-G12 (begin|end|line|abort) ?(.*)$", raw)
    if not m:
        if raw.strip():
            other.append(raw.strip())
        continue
    kind, rest = m.group(1), m.group(2)
    if kind == "abort":
        aborts.append(rest)
    elif kind == "begin":
        p = rest.split(" ")
        if len(p) == 2 and p[0] in ORDER:
            began[p[0]] = p[1]
    elif kind == "end":
        p = rest.split(" ")
        if len(p) == 3 and p[0] in ORDER and re.match(r"^[0-9]+$", p[1]):
            ended[p[0]] = (int(p[1]), p[2])
    else:
        p = rest.split(" ", 1)
        if p[0] in ORDER:
            said.setdefault(p[0], []).append(p[1] if len(p) > 1 else "")

def ms(a, b):
    try:
        f = "%Y-%m-%dT%H:%M:%SZ"
        return max(0, int((datetime.datetime.strptime(b, f) - datetime.datetime.strptime(a, f)).total_seconds() * 1000))
    except ValueError:
        return 0

def stamp(v):
    return v if re.match(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$", v or "") else now

steps = []
def add(sid, status, start, finish, error=None, evidence=None):
    st = {"journey": "CLD-G12", "id": sid, "name": NAMES[sid], "status": status, "started_at": stamp(start),
          "finished_at": stamp(finish), "duration_ms": ms(stamp(start), stamp(finish))}
    if error:
        st["error"] = error[:4000]
    if evidence:
        st["evidence"] = evidence
    steps.append(st)

if not began:
    why = "; ".join(aborts + other[-5:]) or "the transcript holds no step"
    add("reach", "FAIL", now, now, "no step of the check ran: " + why)
for sid in ORDER:
    if sid not in began:
        if steps and steps[-1]["status"] == "PASS":
            add(sid, "FAIL", now, now, "the step did not start")
        break
    start = began[sid]
    lines = said.get(sid, [])
    if sid not in ended:
        add(sid, "FAIL", start, now, "the step did not finish" + (": " + "; ".join(lines[-5:]) if lines else ""))
        break
    rc, finish = ended[sid]
    if sid == "plan":
        actions = [l.strip() for l in lines if l.startswith("  # ")]
        summary = [l for l in lines if l.startswith("Plan: ")]
        errs = [l for l in lines if l.startswith("Error: ")]
        if rc == 0 and "No changes." in [l.split(" Your")[0] for l in lines]:
            add(sid, "PASS", start, finish, evidence={"plan": "No changes."})
        elif rc == 0:
            add(sid, "FAIL", start, finish, "tofu plan exited 0 without the line No changes.")
        elif rc == 2:
            add(sid, "FAIL", start, finish, "the plan shows changes: " + (summary[0] if summary else "no Plan line was printed"),
                evidence={"actions": actions})
        else:
            add(sid, "FAIL", start, finish, "tofu plan exit code %d: %s" % (rc, "; ".join(errs) or "no error was printed"))
    elif rc == 0:
        add(sid, "PASS", start, finish, evidence={"keys": lines} if sid == "regenerate" and lines else None)
    else:
        add(sid, "FAIL", start, finish, "exit code %d: %s" % (rc, "; ".join(lines) or "nothing was printed"))
    if steps[-1]["status"] != "PASS":
        break

status = "PASS" if steps and all(s["status"] == "PASS" for s in steps) and steps[-1]["id"] == "plan" else "FAIL"
with open(out, "w", encoding="utf-8") as fh:
    json.dump({"id": "CLD-G12", "status": status, "steps": steps}, fh, indent=2)
    fh.write("\n")
for s in steps:
    print("CLD-G12 %-10s %s%s" % (s["id"], s["status"], (": " + s["error"]) if s.get("error") else ""))
    for a in (s.get("evidence") or {}).get("actions", []):
        print("    " + a)
print("CLD-G12 " + status)
PY
}

usage() { echo "usage: check-zero-change-plan.sh run --deployment <id> --candidate <dir> --result <file> [--kubeconfig <file>] [--namespace <ns>] [--target <t>] [--transcript <file>] [--regenerate-bin <file>] | judge <transcript> <result> | --self-test" >&2; exit 2; }

run_check() {
  local id="" cand="" result="" kc="${KUBECONFIG:-}" ns=catalyst target=deploy/catalyst-api transcript="" bin=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --deployment) id="${2:-}"; shift 2 ;;
      --candidate) cand="${2:-}"; shift 2 ;;
      --result) result="${2:-}"; shift 2 ;;
      --kubeconfig) kc="${2:-}"; shift 2 ;;
      --namespace) ns="${2:-}"; shift 2 ;;
      --target) target="${2:-}"; shift 2 ;;
      --transcript) transcript="${2:-}"; shift 2 ;;
      --regenerate-bin) bin="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  [ -n "$id" ] && [ -n "$cand" ] && [ -n "$result" ] || usage
  [[ "$id" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,62}$ ]] || { echo "check-zero-change-plan: the deployment id $id is not a deployment id" >&2; exit 2; }
  local work; work="$(mktemp -d)"
  # shellcheck disable=SC2064 # the path is fixed now; run is the whole of this process
  trap "rm -rf '$work'" EXIT
  [ -n "$transcript" ] || transcript="$work/transcript"
  : > "$transcript"
  local kargs=(-n "$ns")
  [ -n "$kc" ] && kargs=(--kubeconfig "$kc" -n "$ns")
  abort() { printf 'CLD-G12 abort %s\n' "$1" >> "$transcript"; }
  local arch="" provider="" goarch=""
  if ! arch="$(kubectl "${kargs[@]}" exec "$target" -- uname -m 2>>"$transcript")"; then
    abort "the engine Pod $ns/$target could not be reached with kubectl exec"
  else
    case "$arch" in x86_64|amd64) goarch=amd64 ;; aarch64|arm64) goarch=arm64 ;; *) abort "the engine Pod runs on $arch" ;; esac
  fi
  if [ -n "$goarch" ]; then
    # The record holds no credential: every one is redacted before it is stored.
    provider="$(kubectl "${kargs[@]}" exec "$target" -- sh -c 'cat "${CATALYST_DEPLOYMENTS_DIR:-/var/lib/catalyst/deployments}/$1.json"' sh "$id" 2>>"$transcript" \
      | python3 -c 'import json, sys; print(((json.load(sys.stdin).get("request") or {}).get("provider") or "").strip())' 2>>"$transcript")" \
      || { abort "the deployment record of $id could not be read"; provider=""; }
    if [ -n "$provider" ] && [[ ! "$provider" =~ ^[a-z0-9-]+$ ]]; then abort "the deployment record gives the provider $provider"; provider=""; fi
    [ -n "$provider" ] || abort "the deployment record of $id names no provider"
  fi
  if [ -n "$provider" ]; then
    local pack="$work/pack" mod="$cand/infra/providers/$provider"
    mkdir -p "$pack/module" "$pack/_shared" "$pack/bin"
    if [ ! -f "$mod/main.tf" ]; then
      abort "the commit under test has no module infra/providers/$provider"; provider=""
    else
      find "$mod" -maxdepth 1 -type f \( -name '*.tf' -o -name '*.tftpl' \) -exec cp {} "$pack/module/" \;
      if [ -d "$mod/seal" ]; then
        mkdir -p "$pack/module/seal"
        find "$mod/seal" -maxdepth 1 -type f \( -name '*.tf' -o -name '*.tftpl' \) -exec cp {} "$pack/module/seal/" \;
      fi
      if [ -d "$cand/infra/providers/_shared" ]; then
        find "$cand/infra/providers/_shared" -maxdepth 1 -type f \( -name '*.tf' -o -name '*.tftpl' \) -exec cp {} "$pack/_shared/" \;
      fi
      # The cloud profiles that Validate reads: the schema and every pack's profile.yaml.
      if [ -f "$cand/infra/providers/_contract/cloudprofile.schema.json" ]; then
        mkdir -p "$pack/providers/_contract"
        cp "$cand/infra/providers/_contract/cloudprofile.schema.json" "$pack/providers/_contract/"
        for f in "$cand"/infra/providers/*/profile.yaml; do
          [ -f "$f" ] || continue
          f="${f%/profile.yaml}"; mkdir -p "$pack/providers/${f##*/}"; cp "$f/profile.yaml" "$pack/providers/${f##*/}/"
        done
      fi
      if [ -n "$bin" ]; then
        cp "$bin" "$pack/bin/tfvars-regenerate"
      elif ! (cd "$cand/products/catalyst/bootstrap/api" && CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
               go build -trimpath -ldflags '-s -w' -o "$pack/bin/tfvars-regenerate" ./cmd/tfvars-regenerate) >>"$transcript" 2>&1; then
        abort "tfvars-regenerate of the commit under test could not be built"; provider=""
      fi
    fi
  fi
  if [ -n "$provider" ]; then
    tar -C "$pack" -cf - . | kubectl "${kargs[@]}" exec -i "$target" -- sh -c "$REMOTE" sh "$id" >>"$transcript" 2>&1 \
      || echo "kubectl exec ended with an error" >> "$transcript"
  fi
  judge "$transcript" "$result"
  python3 -c 'import json, sys; sys.exit(0 if json.load(open(sys.argv[1]))["status"] == "PASS" else 1)' "$result"
}

self_test() {
  local t fails=0 total=0
  t="$(mktemp -d)"
  # shellcheck disable=SC2064 # the path is fixed now; the self-test is the whole of this process
  trap "rm -rf '$t'" EXIT
  ok() { total=$((total + 1)); if [ "$1" = 0 ]; then echo "  PASS  $2"; else echo "  FAIL  $2"; fails=$((fails + 1)); fi; }
  # A fake engine: the working directory, the record, a plugin cache, tofu and kubectl.
  local id=0123456789abcdef eng="$t/engine" cand="$t/candidate"
  mkdir -p "$eng/tofu/$id/.terraform/providers" "$eng/tofu/$id/seal" "$eng/deployments" "$eng/cache" "$t/bin" \
           "$cand/infra/providers/fakecloud/seal" "$cand/infra/providers/_shared" "$cand/infra/providers/_contract"
  echo '{"provider": "fakecloud"}' > "$eng/tofu/$id/tofu.auto.tfvars.json"
  echo 'old' > "$eng/tofu/$id/main.tf"; echo 'old' > "$eng/tofu/$id/stale.tf"; echo 'old' > "$eng/tofu/$id/seal/main.tf"
  echo '{}' > "$eng/tofu/$id/terraform.tfstate"
  echo '{"id": "'"$id"'", "request": {"provider": "fakecloud"}}' > "$eng/deployments/$id.json"
  echo 'new' > "$cand/infra/providers/fakecloud/main.tf"; echo 'new' > "$cand/infra/providers/fakecloud/seal/main.tf"
  echo 'new' > "$cand/infra/providers/fakecloud/cloudinit-worker.tftpl"; echo 'x' > "$cand/infra/providers/fakecloud/README.md"
  echo 'new' > "$cand/infra/providers/_shared/cloudinit-control-plane.tftpl"
  echo '{}' > "$cand/infra/providers/_contract/cloudprofile.schema.json"
  echo 'metadata: {name: fakecloud}' > "$cand/infra/providers/fakecloud/profile.yaml"
  cat > "$t/regen" <<'EOF'
#!/bin/sh
[ "$1" = -record ] && [ -f "$2" ] && [ "$3" = -dir ] && [ -f "$4/tofu.auto.tfvars.json" ] || { echo "bad arguments $*"; exit 2; }
[ "$5" = -providers ] && [ -f "$6/_contract/cloudprofile.schema.json" ] && [ -f "$6/fakecloud/profile.yaml" ] \
  || { echo "the engine refuses the request of the record: no cloud profiles of the commit under test"; exit 3; }
[ "${FAKE_REGEN:-ok}" = ok ] || { echo "the engine refuses the request of the record: x is required"; exit 3; }
echo '{"provider": "fakecloud", "regenerated": true}' > "$4/tofu.auto.tfvars.json"
echo "changed regions"
EOF
  cat > "$t/bin/tofu" <<'EOF'
#!/bin/sh
case "$1" in
  init)
    [ "$TF_PLUGIN_CACHE_DIR" = "$FAKE_CACHE" ] || { echo "Error: no plugin cache"; exit 1; }
    [ "${FAKE_INIT:-ok}" = ok ] || { printf '\342\225\267\n\342\224\202 Error: Failed to query available provider packages\n'; exit 1; }
    echo "OpenTofu has been successfully initialized!"; exit 0 ;;
  plan)
    for f in main.tf seal/main.tf cloudinit-worker.tftpl ../_shared/cloudinit-control-plane.tftpl; do
      [ "$(cat "$f" 2>/dev/null)" = new ] || { echo "Error: $f is not the new module"; exit 1; }
    done
    [ ! -e stale.tf ] && [ ! -e README.md ] && [ ! -e .terraform ] && [ -f terraform.tfstate ] || { echo "Error: the copy is not staged"; exit 1; }
    grep -q regenerated tofu.auto.tfvars.json || { echo "Error: the variables file of the copy is not the regenerated one"; exit 1; }
    case "${FAKE_PLAN:-none}" in
      none) echo "No changes. Your infrastructure matches the configuration."; exit 0 ;;
      silent) exit 0 ;;
      change) printf '  # fakecloud_address.nat["b"] will be created\n  # fakecloud_instance.cp[0] must be replaced\n  # x has changed\nPlan: 1 to add, 0 to change, 1 to destroy.\n'; exit 2 ;;
      error) printf '\342\224\202 Error: Invalid provider configuration\n'; exit 1 ;;
      many) i=0; while [ "$i" -lt 450 ]; do echo "  # fakecloud_address.a[$i] will be created"; i=$((i + 1)); done
        echo "Plan: 450 to add, 0 to change, 0 to destroy."; exit 2 ;;
      HUP|PIPE) # the exec stream drops during the plan: the remote shell gets the signal
        p=$PPID; [ "$(cat "/proc/$p/comm" 2>/dev/null)" = timeout ] && p="$(cut -d' ' -f4 "/proc/$p/stat")"
        kill -s "$FAKE_PLAN" "$p"; exit 9 ;;
      hang) # the connection to the Pod ends during the plan: kill the remote shell, above timeout
        echo "  # a will be created"; p=$PPID
        [ "$(cat "/proc/$p/comm" 2>/dev/null)" = timeout ] && p="$(cut -d' ' -f4 "/proc/$p/stat")"
        kill -9 "$p"; exit 9 ;;
    esac ;;
esac
exit 1
EOF
  cat > "$t/bin/kubectl" <<'EOF'
#!/bin/bash
# The fake kubectl runs what follows -- here, in the fake engine's environment. Like a process
# started in a container, it gets the default action for HUP and PIPE: a CI runner may start its
# steps with SIGPIPE ignored, and a shell cannot trap a signal that was ignored when it started.
[ "${FAKE_KUBECTL:-ok}" = ok ] || { echo "error: unable to upgrade connection: pod not found" >&2; exit 1; }
while [ $# -gt 0 ] && [ "$1" != -- ]; do shift; done
shift
exec python3 -c 'import os, signal, sys
for s in (signal.SIGHUP, signal.SIGPIPE):
    signal.signal(s, signal.SIG_DFL)
os.execvp(sys.argv[1], sys.argv[1:])' "$@"
EOF
  chmod +x "$t/regen" "$t/bin/tofu" "$t/bin/kubectl"
  export CATALYST_TOFU_WORKDIR="$eng/tofu" CATALYST_DEPLOYMENTS_DIR="$eng/deployments" CATALYST_TF_PLUGIN_CACHE_DIR="$eng/cache" FAKE_CACHE="$eng/cache"
  export TMPDIR="$t"
  local oldpath="$PATH"
  export PATH="$t/bin:$PATH"
  case_run() { # <expected exit code> <label> <grep pattern in the result or ""> env...
    local want="$1" label="$2" pattern="$3"; shift 3
    local rc=0
    env "$@" bash "$HERE/check-zero-change-plan.sh" run --deployment "$id" --candidate "$cand" --result "$t/result.json" \
      --regenerate-bin "$t/regen" --transcript "$t/transcript" > "$t/out" 2>&1 || rc=$?
    local good=0
    [ "$rc" = "$want" ] || good=1
    if [ -n "$pattern" ] && ! grep -q -- "$pattern" "$t/result.json"; then good=1; fi
    python3 - "$t/result.json" "$HERE/acceptance-gate.py" <<'PY' || good=1
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("gate", sys.argv[2])
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
problem = gate.result_problem(json.load(open(sys.argv[1])), "CLD-G12")
if problem:
    print("the result is not of the form merge-check reads:", problem)
    sys.exit(1)
PY
    [ "$good" = 0 ] || { echo "        exit code $rc (want $want); result:"; sed 's/^/        /' "$t/result.json" | head -40; sed 's/^/        /' "$t/out" | head -20; }
    ok "$good" "$label"
  }
  case_run 0 "run: a staged copy whose plan shows no change is PASS" '"plan": "No changes."'
  [ -f "$eng/tofu/$id/stale.tf" ] && [ "$(cat "$eng/tofu/$id/main.tf")" = old ] && [ -d "$eng/tofu/$id/.terraform" ] \
    && [ "$(cat "$eng/tofu/$id/tofu.auto.tfvars.json")" = '{"provider": "fakecloud"}' ]
  ok $? "run: the working directory itself is not changed, its variables file neither"
  [ -z "$(find "$t" -maxdepth 1 -name 'cld-g12.*')" ]
  ok $? "run: the copy is removed at the end"
  local sig
  for sig in HUP PIPE; do
    case_run 1 "rule finish: a stream dropped by $sig during the plan is FAIL" 'the step did not finish' FAKE_PLAN=$sig
    [ -z "$(find "$t" -maxdepth 1 -name 'cld-g12.*')" ]
    ok $? "run: the copy is removed when the stream drops by $sig"
  done
  case_run 1 "rule plan: a plan with changes is FAIL, with the counts and every planned action" 'Plan: 1 to add, 0 to change, 1 to destroy.' FAKE_PLAN=change
  grep -q 'must be replaced' "$t/result.json" && ! grep -q 'has changed' "$t/result.json"
  ok $? "rule plan: the actions are listed, drift lines are not"
  case_run 1 "rule plan: more actions than are listed still give the counts" 'the plan shows changes: Plan: 450 to add' FAKE_PLAN=many
  case_run 1 "rule plan: a plan that fails is FAIL with its error" 'tofu plan exit code 1: Error: Invalid provider configuration' FAKE_PLAN=error
  case_run 1 "rule plan: exit code 0 without the line No changes. is FAIL" 'without the line No changes.' FAKE_PLAN=silent
  case_run 1 "rule finish: a step that did not finish is FAIL" 'the step did not finish' FAKE_PLAN=hang
  case_run 1 "rule init: a failed init is FAIL with its error" 'Error: Failed to query available provider packages' FAKE_INIT=fail
  case_run 1 "rule regenerate: a record the engine refuses is FAIL" 'exit code 3: the engine refuses' FAKE_REGEN=fail
  mv "$cand/infra/providers/_contract" "$t/contract"
  case_run 1 "run: the cloud profiles of the commit under test reach tfvars-regenerate" 'no cloud profiles of the commit under test'
  mv "$t/contract" "$cand/infra/providers/_contract"
  case_run 1 "rule reach: an engine Pod that cannot be reached is FAIL" 'no step of the check ran' FAKE_KUBECTL=fail
  mv "$eng/deployments/$id.json" "$t/record.json"
  case_run 1 "rule reach: a deployment without a record is FAIL" 'names no provider'
  mv "$t/record.json" "$eng/deployments/$id.json"
  mv "$eng/tofu/$id" "$t/kept"
  case_run 1 "rule copy: a deployment without a working directory is FAIL" 'holds no tofu.auto.tfvars.json'
  mv "$t/kept" "$eng/tofu/$id"
  mv "$cand/infra/providers/fakecloud" "$t/module"
  case_run 1 "rule reach: a commit without the provider's module is FAIL" 'has no module infra/providers/fakecloud'
  mv "$t/module" "$cand/infra/providers/fakecloud"
  export PATH="$oldpath"
  # judge alone
  printf 'CLD-G12 begin copy 2026-11-18T10:00:00Z\nCLD-G12 end copy 0 2026-11-18T10:00:01Z\n' > "$t/tr"
  judge "$t/tr" "$t/r.json" > /dev/null
  grep -q '"id": "regenerate"' "$t/r.json" && grep -q 'the step did not start' "$t/r.json" && grep -q '"status": "FAIL"' "$t/r.json"
  ok $? "rule start: a transcript that ends after a passing step is FAIL"
  : > "$t/tr"
  judge "$t/tr" "$t/r.json" > /dev/null
  grep -q 'the transcript holds no step' "$t/r.json"
  ok $? "rule reach: an empty transcript is FAIL"
  local rc=0
  bash "$HERE/check-zero-change-plan.sh" run --deployment '../x' --candidate "$cand" --result "$t/r.json" > /dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ]
  ok $? "usage: an id that is not a deployment id is refused"
  rc=0
  bash "$HERE/check-zero-change-plan.sh" run --deployment "$id" > /dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ]
  ok $? "usage: run without --candidate and --result is refused"
  echo "check-zero-change-plan self-test: $((total - fails))/$total passed"
  [ "$fails" = 0 ]
}

case "${1:-}" in
  run) shift; run_check "$@" ;;
  judge) [ $# = 3 ] || usage; judge "$2" "$3" ;;
  --self-test) self_test ;;
  *) usage ;;
esac
