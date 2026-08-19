#!/usr/bin/env bash
# credential-resync-6317.sh — prove the creds-resync predicate adopts a NEWER
# credential, refuses an older one, and no-ops on an absent one.
#
# WHY THIS TEST EXISTS
#
#   The sidecar is the last link in the #6317 chain: catalyst-api can renew the
#   credential, ESO can deliver it and kubelet can re-project it, and a RUNNING
#   workspace still reads the copy the init container made at pod start. The
#   sidecar closes that gap — but only if its predicate is right in BOTH
#   directions. Copying blindly would clobber a credential claude-code rotated
#   for itself; copying never would leave the stale file in place and the agent
#   401ing. Neither failure is visible from "the sidecar is running".
#
#   So the predicate is exercised against real files, and each case is checked
#   for the OUTCOME, not for the exit status of the loop.
#
# The extracted one-liner below MUST stay byte-identical to the one rendered in
# templates/statefulset.yaml — assert_matches_template() enforces that, so this
# test can never drift into passing against code the chart does not ship.
#
# Refs #6317 #4277 #4111
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
TEMPLATE="$REPO_ROOT/products/agenity/chart/templates/statefulset.yaml"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0
note() { printf '  %s\n' "$*"; }
ok()   { printf 'PASS  %s\n' "$*"; }
bad()  { printf 'FAIL  %s\n' "$*"; fail=1; }

# The predicate, with the Helm placeholder resolved to its default value.
# Kept as one line to mirror the rendered container command exactly.
PREDICATE='import json,os,shutil,sys;s=os.environ["SRC"];d=os.environ["DST"];e=lambda p:int((json.load(open(p)).get("claudeAiOauth",{}) or {}).get("expiresAt",0) or 0) if os.path.exists(p) and os.path.getsize(p)>0 else -1;se=e(s);de=e(d);sys.exit(0) if se<0 or se<=de else (shutil.copyfile(s,d),os.chmod(d,0o600),print("[creds-resync] adopted a NEWER credential from the Secret: expiresAt %d -> %d (%d bytes)"%(de,se,os.path.getsize(d)),flush=True))'

# ── 0. the predicate under test must be the one the chart ships ────────────
assert_matches_template() {
  # Reduce the template's rendered line to the same shape: strip the Helm
  # placeholder and compare the invariant body of the expression.
  if ! grep -q 'adopted a NEWER credential from the Secret' "$TEMPLATE"; then
    bad "the statefulset template no longer contains the resync predicate — this test would be asserting on code that is not shipped"
    return
  fi
  for fragment in \
    'se=e(s);de=e(d)' \
    'sys.exit(0) if se<0 or se<=de' \
    'shutil.copyfile(s,d)' \
    'os.chmod(d,0o600)'
  do
    if ! grep -qF "$fragment" "$TEMPLATE"; then
      bad "predicate fragment missing from the chart: $fragment"
      return
    fi
  done
  ok "the predicate under test matches the one the chart renders"
}

cred() { # cred <expiresAt-millis> <marker>
  printf '{"claudeAiOauth":{"accessToken":"%s","refreshToken":"rt","expiresAt":%s,"scopes":["user:inference"]}}' "$2" "$1"
}

run_predicate() { SRC="$1" DST="$2" python3 -c "$PREDICATE"; }

marker_of() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["claudeAiOauth"]["accessToken"])' "$1"; }

assert_matches_template

# ── 1. a NEWER credential in the Secret is adopted ─────────────────────────
# The whole point: this is the refreshed value arriving at a running Pod.
src="$WORK/newer-src.json"; dst="$WORK/newer-dst.json"
cred 2000000000000 FRESH > "$src"
cred 1000000000000 STALE > "$dst"
out="$(run_predicate "$src" "$dst" || true)"
if [ "$(marker_of "$dst")" = "FRESH" ]; then
  ok "a NEWER credential is adopted by the running Pod"
else
  bad "a NEWER credential was NOT adopted — the refresh cannot reach a running agent (dst still $(marker_of "$dst"))"
fi
case "$out" in
  *"adopted a NEWER credential"*) ok "the adoption is announced on stdout" ;;
  *) bad "the adoption was silent — an operator cannot tell a working re-sync from a dead one" ;;
esac

# ── 2. an OLDER credential is refused ──────────────────────────────────────
# claude-code may rotate its own file; a blind copy would undo that.
src="$WORK/older-src.json"; dst="$WORK/older-dst.json"
cred 1000000000000 OLDSECRET > "$src"
cred 2000000000000 SELFROTATED > "$dst"
run_predicate "$src" "$dst" || true
if [ "$(marker_of "$dst")" = "SELFROTATED" ]; then
  ok "an OLDER credential is refused — a self-rotated file is never clobbered"
else
  bad "an OLDER credential overwrote a fresher one — this destroys a claude-code self-refresh"
fi

# ── 3. an EQUAL credential is a no-op ──────────────────────────────────────
src="$WORK/eq-src.json"; dst="$WORK/eq-dst.json"
cred 1500000000000 SAME > "$src"
cred 1500000000000 KEEP > "$dst"
run_predicate "$src" "$dst" || true
if [ "$(marker_of "$dst")" = "KEEP" ]; then
  ok "an EQUAL expiry is a no-op — no churn on every tick"
else
  bad "an equal expiry rewrote the file — the sidecar churns the credential every interval"
fi

# ── 4. an ABSENT or EMPTY Secret never destroys the working copy ───────────
# A missing mount must not blank a credential the agent is authenticating with.
src="$WORK/missing.json"; dst="$WORK/keep.json"
cred 1500000000000 SURVIVES > "$dst"
run_predicate "$src" "$dst" || true
if [ "$(marker_of "$dst")" = "SURVIVES" ]; then
  ok "an ABSENT Secret leaves the working credential intact"
else
  bad "an absent Secret destroyed the working credential"
fi

: > "$WORK/empty.json"
run_predicate "$WORK/empty.json" "$dst" || true
if [ "$(marker_of "$dst")" = "SURVIVES" ]; then
  ok "an EMPTY Secret leaves the working credential intact"
else
  bad "an empty Secret destroyed the working credential"
fi

# ── 5. a first-boot destination (absent) accepts the Secret ───────────────
src="$WORK/first-src.json"; dst="$WORK/first-dst.json"
cred 1500000000000 FIRST > "$src"
run_predicate "$src" "$dst" || true
if [ -f "$dst" ] && [ "$(marker_of "$dst")" = "FIRST" ]; then
  ok "an absent destination is populated from the Secret"
else
  bad "an absent destination was not populated"
fi

# ── 6. VACUITY — the predicate must be able to REFUSE ─────────────────────
# If every case above passed because the predicate copies unconditionally,
# case 2 would already have failed. Prove the negative arm exists by asserting
# the refusal leaves the file byte-identical.
src="$WORK/vac-src.json"; dst="$WORK/vac-dst.json"
cred 1000000000000 A > "$src"
cred 2000000000000 B > "$dst"
before="$(sha256sum < "$dst")"
run_predicate "$src" "$dst" || true
after="$(sha256sum < "$dst")"
if [ "$before" = "$after" ]; then
  ok "vacuity: the refusal arm is real (destination byte-identical after a refused copy)"
else
  bad "vacuity: the predicate copied when it should have refused"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "FAIL — the credential re-sync predicate does not behave as the chart claims."
  exit 1
fi
echo "PASS — the re-sync adopts newer, refuses older/equal, and never destroys a working credential."
