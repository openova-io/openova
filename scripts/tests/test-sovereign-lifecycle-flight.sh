#!/usr/bin/env bash
# test-sovereign-lifecycle-flight.sh - the flight checklist of scripts/sovereign-lifecycle.sh
# (capture keeps the variables document, sweep, fire refuses without a clean sweep), through
# stubs: nothing leaves this host. A stub scripts/flight-sweep.sh takes its exit codes from a
# list (the last one repeats); a stub scripts/prov-preflight.sh records its call and refuses.
# Usage: bash scripts/tests/test-sovereign-lifecycle-flight.sh   Exit 0: every case passed.
set -uo pipefail

here="$(cd "$(dirname "$0")/../.." && pwd)"
t="$(mktemp -d)"; trap 'rm -rf "$t"' EXIT
mkdir -p "$t/scripts" "$t/bin"
cp "$here/scripts/sovereign-lifecycle.sh" "$t/scripts/"
cat > "$t/scripts/flight-sweep.sh" <<'EOF'
echo "$1" >> "$T/sweep.calls"
c="$(cat "$T/codes")"; echo "${c#* }" > "$T/codes"; exit "${c%% *}"
EOF
printf 'echo call >> "$T/preflight.calls"; exit 1\n' > "$t/scripts/prov-preflight.sh"
printf '#!/usr/bin/env bash\ncase "$*" in *"get pods"*) echo pod/catalyst-api-0 ;; *exec*) cat "$T/vars.json" ;; *) exit 1 ;; esac\n' > "$t/bin/kubectl"
printf '#!/usr/bin/env bash\necho 404\n' > "$t/bin/curl"
chmod +x "$t/bin/kubectl" "$t/bin/curl"
export T="$t" PATH="$t/bin:$PATH" FLIGHT_DIR="$t/flight" SWEEP_INTERVAL=0 HW_TFVARS=/dev/null CATALYST_MOTHERSHIP_KEY=/nonexistent
unset PREV_ID SWEEP_TIMEOUT
id=0a1b2c3d4e5f6789 kept="$t/flight/0a1b2c3d4e5f6789-vars.json" doc='{"deployment_id":"0a1b2c3d4e5f6789"}' failed=0

run() { # run <codes> <arguments...>: the output goes to $t/out, the exit code to $t/rc
  echo "$1" > "$t/codes"; shift; rm -f "$t/sweep.calls" "$t/preflight.calls"
  bash "$t/scripts/sovereign-lifecycle.sh" "$@" > "$t/out" 2>&1; echo $? > "$t/rc"
}
calls() { if [ -f "$t/$1.calls" ]; then wc -l < "$t/$1.calls" | tr -d ' '; else echo 0; fi; }
check() { # check <case> <condition...>
  local name="$1"; shift
  if "$@"; then echo "ok   $name"; else echo "FAIL $name"; sed 's/^/     | /' "$t/out"; failed=1; fi
}

run 0 sweep "$id"
check "sweep without a kept document is not measured and lists nothing" test "$(cat "$t/rc") $(calls sweep)" = "3 0"
run 0 sweep '../x'
check "sweep without a plain id is a usage error" test "$(cat "$t/rc") $(calls sweep)" = "2 0"
echo "$doc" > "$t/vars.json"; run 0 capture "$id"
check "capture keeps the variables document, readable by its owner only" \
  test "$(cat "$kept" 2>/dev/null) $(stat -c %a "$kept" 2>/dev/null)" = "$doc 600"
echo 'not json' > "$t/vars.json"; run 0 capture "$id"
check "a capture that reads no document keeps the earlier copy" test "$(cat "$kept" 2>/dev/null)" = "$doc"
run 0 sweep "$id"
check "a clean sweep lists the kept document once, exits 0 and prints the leftover count" \
  test "$(cat "$t/rc") $(calls sweep) $(cat "$t/sweep.calls") $(tail -1 "$t/out")" = "0 1 $kept SWEEP PASS $id leftovers=0"
run "1 1 0" sweep "$id"
check "sweep lists again while leftovers remain" test "$(cat "$t/rc") $(calls sweep)" = "0 3"
SWEEP_TIMEOUT=0 run 1 sweep "$id"
check "leftovers at the deadline exit 1" test "$(cat "$t/rc") $(calls sweep)" = "1 1"
for code in 3 7; do
  run "$code" sweep "$id"; check "lister exit $code is not measured" test "$(cat "$t/rc")" = 3
done
run 0 fire hw999
check "fire without PREV_ID refuses before the sweep and the pre-flight" test "$(cat "$t/rc") $(calls sweep) $(calls preflight)" = "1 0 0"
PREV_ID="$id" run "1 1 0" fire hw999
check "fire with leftovers refuses after one listing" test "$(cat "$t/rc") $(calls sweep) $(calls preflight)" = "1 1 0"
PREV_ID="$id" run 3 fire hw999
check "fire with a sweep that is not measured refuses" test "$(cat "$t/rc") $(calls preflight)" = "1 0"
PREV_ID=0000aaaa run 0 fire hw999
check "fire with a PREV_ID that has no kept document refuses" test "$(cat "$t/rc") $(calls sweep) $(calls preflight)" = "1 0 0"
PREV_ID="$id" run 0 fire hw999
check "fire after a clean sweep prints the leftover count and goes on to the pre-flight" \
  test "$(grep -c -x -F "SWEEP PASS $id leftovers=0" "$t/out") $(calls preflight)" = "1 1"

if [ "$failed" = 0 ]; then echo "every case passed"; else echo "a case failed"; fi
exit "$failed"
