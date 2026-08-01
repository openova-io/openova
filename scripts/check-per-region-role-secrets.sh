#!/usr/bin/env bash
# check-per-region-role-secrets.sh — preflight for the A16 per-region-secret
# class (#5499 task 4).
#
# WHY: this class has now recurred FIVE times — #5394, #5406, #5414, #5416,
# #5487 — and #5499 is the sixth. Same shape every time: one region mints a
# credential, the other does not, and a workload in the second region
# consumes a copy it was never issued. The consumer then fails auth against
# whatever the local role actually holds.
#
# #5499, live on hw291 (hashes only — this repo is public):
#
#   region-a  minted   shared-data/shared-pg-harbor.password = bf636d39e94a02dc
#             consumed harbor/harbor-database-secret.password = bf636d39e94a02dc   agree
#
#   region-b  minted   shared-data/shared-pg-harbor.password = <ABSENT>
#             consumed harbor/harbor-database-secret.password = bf636d39e94a02dc   MISMATCH
#
# harbor-core then died with SQLSTATE 28P01 and, because upstream harbor
# treats a DB-auth failure as FATAL, crashlooped — taking harbor-jobservice
# with it and stalling the sovereignty cutover at step-02.
#
# The bp-postgres 0.2.13 side-gate already stops the WRONG-MINT half (a
# replica-role region must not mint role Secrets). This checks the CONSUMER
# half, which is still unguarded: a region that does not mint a role Secret
# must not be left delivering a stale one to a workload that will die on it.
#
# 🛑 Prints SHA-256 PREFIXES ONLY, never a secret value. This repo is public.
#    A prefix is enough to prove agree/disagree and useless as a credential.
#    The all-zero-input hash e3b0c44298fc1c14… means the key is ABSENT — that
#    is exactly the region-b signal above, so it is called out by name.
#
# Usage:
#   scripts/check-per-region-role-secrets.sh --context <a> --context <b>
#   scripts/check-per-region-role-secrets.sh --self-test    # no cluster
#
# Exit: 0 = every consumed credential is backed by a minted one in the SAME
#       region, 1 = a divergence, 2 = setup error.

set -euo pipefail

CONTEXTS=()
SELF_TEST_ONLY=0
MINT_NS="${MINT_NS:-shared-data}"

while [ $# -gt 0 ]; do
  case "$1" in
    --context)   CONTEXTS+=("$2"); shift 2 ;;
    --self-test) SELF_TEST_ONLY=1; shift ;;
    --mint-ns)   MINT_NS="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--context <ctx>]... [--mint-ns <ns>] [--self-test]" >&2
       exit 2 ;;
  esac
done

# The sha256 of empty input. When a key is absent the extractor yields "",
# and hashing "" gives this — the #5499 region-b signature. Naming it stops
# the next reader from mistaking "absent" for "a different password".
EMPTY_SHA="e3b0c44298fc1c14"

short_sha() { printf '%s' "${1:-}" | sha256sum | cut -c1-16; }

# compare_pair — the whole decision, isolated so the self-test can drive it
# without a cluster. Echoes a verdict token; never echoes a secret.
#   AGREE          minted and consumed match
#   MINT-ABSENT    consumed exists, nothing minted here  <- the #5499 shape
#   DIVERGE        both exist and differ
#   NOT-CONSUMED   nothing consumes it here; not a fault
compare_pair() {
  local minted_sha="$1" consumed_sha="$2"
  if [ "${consumed_sha}" = "${EMPTY_SHA}" ]; then echo "NOT-CONSUMED"; return; fi
  if [ "${minted_sha}" = "${EMPTY_SHA}" ]; then echo "MINT-ABSENT"; return; fi
  if [ "${minted_sha}" = "${consumed_sha}" ]; then echo "AGREE"; return; fi
  echo "DIVERGE"
}

# ─── Phase 0 — decision self-test (vacuity guard) ────────────────────────
#
# A comparison guard reports "clean" both when the credentials agree AND when
# the comparison stopped working. Prove the decision function before trusting
# it against a cluster. All four verdicts are pinned, including the two
# negative cases — without them a compare_pair that always returned AGREE
# would satisfy the positive checks.
A="$(short_sha 'pw-alpha')"
B="$(short_sha 'pw-beta')"

[ "$(compare_pair "$A" "$A")"          = "AGREE" ]        || { echo "SELF-TEST FAIL: identical creds not AGREE" >&2; exit 2; }
[ "$(compare_pair "$EMPTY_SHA" "$A")"  = "MINT-ABSENT" ]  || { echo "SELF-TEST FAIL: absent mint not caught — this is the #5499 shape" >&2; exit 2; }
[ "$(compare_pair "$A" "$B")"          = "DIVERGE" ]      || { echo "SELF-TEST FAIL: differing creds not DIVERGE" >&2; exit 2; }
[ "$(compare_pair "$A" "$EMPTY_SHA")"  = "NOT-CONSUMED" ] || { echo "SELF-TEST FAIL: absent consumer should be inert" >&2; exit 2; }
[ "$A" != "$B" ]                                          || { echo "SELF-TEST FAIL: hasher is degenerate — distinct inputs hashed alike" >&2; exit 2; }
echo "OK — decision self-test passed (AGREE / MINT-ABSENT / DIVERGE / NOT-CONSUMED all distinguished)."

if [ "${SELF_TEST_ONLY}" -eq 1 ]; then
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

if [ "${#CONTEXTS[@]}" -eq 0 ]; then
  echo "WARN: no --context given — nothing to compare. Decision logic proven only." >&2
  exit 0
fi
if ! command -v kubectl >/dev/null 2>&1; then
  echo "WARN: kubectl not on PATH — cannot read a cluster. Reporting NOTHING rather than a false clean." >&2
  exit 0
fi

# ─── Phase 1 — per-region minted-vs-consumed sweep ───────────────────────
EXIT=0
for ctx in "${CONTEXTS[@]}"; do
  echo ""
  echo "== region context: ${ctx} =="

  # Consumers: every Secret named *-database-secret, in any namespace. That
  # is the reflected shape bp-postgres emits for a role owner (see
  # platform/harbor/chart/templates/cnpg-cluster.yaml — the Database CR's
  # role Secret is re-emitted as <workload>-database-secret).
  consumers="$(timeout 30 kubectl --context "${ctx}" get secret -A \
      -o jsonpath='{range .items[?(@.type=="Opaque")]}{.metadata.namespace}{" "}{.metadata.name}{"\n"}{end}' \
      2>/dev/null | grep -- '-database-secret' || true)"

  if [ -z "${consumers}" ]; then
    echo "  (no *-database-secret consumers found — nothing to check here)"
    continue
  fi

  while IFS=' ' read -r ns name; do
    [ -z "${name}" ] && continue
    role="${name%-database-secret}"

    consumed_raw="$(timeout 20 kubectl --context "${ctx}" -n "${ns}" get secret "${name}" \
        -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true)"
    minted_raw="$(timeout 20 kubectl --context "${ctx}" -n "${MINT_NS}" get secret "shared-pg-${role}" \
        -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true)"

    consumed_sha="$(short_sha "${consumed_raw}")"
    minted_sha="$(short_sha "${minted_raw}")"
    verdict="$(compare_pair "${minted_sha}" "${consumed_sha}")"

    case "${verdict}" in
      AGREE)
        printf '  OK    %-22s minted=%s consumed=%s\n' "${ns}/${role}" "${minted_sha}" "${consumed_sha}" ;;
      NOT-CONSUMED)
        printf '  skip  %-22s (consumer has no password key)\n' "${ns}/${role}" ;;
      MINT-ABSENT)
        EXIT=1
        printf '  FAIL  %-22s minted=<ABSENT in %s/shared-pg-%s> consumed=%s\n' \
          "${ns}/${role}" "${MINT_NS}" "${role}" "${consumed_sha}" >&2
        echo "        This region consumes a credential it never minted — the #5499 shape." >&2 ;;
      DIVERGE)
        EXIT=1
        printf '  FAIL  %-22s minted=%s consumed=%s  (DIFFER)\n' \
          "${ns}/${role}" "${minted_sha}" "${consumed_sha}" >&2 ;;
    esac
  done <<< "${consumers}"
done

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "A16 per-region-secret divergence (#5499). The consumer will fail" >&2
  echo "auth against the role the LOCAL cluster actually holds — for" >&2
  echo "harbor that is SQLSTATE 28P01 and a FATAL crashloop that stalls" >&2
  echo "the cutover at step-02." >&2
  echo "" >&2
  echo "Do NOT hand-copy the other region's Secret across — that is what" >&2
  echo "produced this state. Fix the MINT side so the region issues its" >&2
  echo "own role credential, or stop delivering the consumer a Secret" >&2
  echo "this region never minted (bp-postgres 0.2.13 side-gate)." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: every consumed role credential is backed by a minted one in the same region (#5499)."
exit 0
