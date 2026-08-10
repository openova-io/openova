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

# ─── Cross-region COVERAGE decision (#6016) ──────────────────────────────
#
# WHY THIS EXISTS: every check above is scoped to ONE region. The loop below
# visits each context independently and asks "is what this region CONSUMES
# backed by what this region MINTS". A region that mints nothing and consumes
# nothing answers that question perfectly — so the sweep printed
# "(no *-database-secret consumers found — nothing to check here)" followed by
# "OK: every consumed role credential is backed by a minted one" and exited 0.
#
# Measured on hw293 (dep a0077ba47e3720e5) region me-east-215-b, which at that
# moment held ZERO role Secrets, ZERO consumer hub Secrets, three CNPG initdb
# Pods in CreateContainerConfigError and 8 non-Ready HelmReleases. The guard
# called it clean. A per-region check that never compares regions cannot see a
# per-region split — the absence is only meaningful RELATIVE to the peer.
#
# So: assert coverage across regions, do not assume it. Every name any region
# holds must be held by every region. bp-postgres renders the role + hub
# Secrets primary-side only (#5224 isReplicaSide) precisely because the
# catalyst-api cross-mesh sync (#3629/#5230/#4158) is supposed to deliver
# region-A's authoritative copies onward; "region B has none" is that delivery
# not having happened, which is the fault this phase names.
#
# coverage_verdict <dir> <region>...
#   reads  <dir>/<region>.names  (sorted, one Secret name per line)
#   writes <dir>/missing         ("<region> <name>" per gap)
#   echoes one of:
#     NO-PEER   fewer than 2 regions — a split is not expressible here
#     COVERED   every name held by ANY region is held by EVERY region
#     SPLIT     some name is held by one region and missing from another
coverage_verdict() {
  local dir="$1"; shift
  local regions=("$@")
  : > "${dir}/missing"
  if [ "${#regions[@]}" -lt 2 ]; then echo "NO-PEER"; return; fi
  cat "${dir}"/*.names 2>/dev/null | sort -u > "${dir}/union"
  local r n
  for r in "${regions[@]}"; do
    while read -r n; do
      [ -n "${n}" ] && echo "${r} ${n}" >> "${dir}/missing"
    done < <(comm -23 "${dir}/union" "${dir}/${r}.names")
  done
  if [ -s "${dir}/missing" ]; then echo "SPLIT"; else echo "COVERED"; fi
}

# ─── Phase 0b — coverage self-test (the negative FIRST) ──────────────────
#
# THE TRAP THIS PINS: a one-region fixture satisfies a "per-region coverage"
# check identically to a correct two-region one, because with nothing to
# compare against there is no gap to find. If NO-PEER and COVERED were the same
# verdict, every assertion below would be provable with a single region and the
# check would be decorative. They are deliberately DISTINCT tokens, and the
# two-region-with-a-one-region-secret case is pinned RED before anything else.
COV_T="$(mktemp -d)"
trap 'rm -rf "${COV_T}"' EXIT

printf 'shared-pg-harbor\nshared-pg-keycloak\n' | sort > "${COV_T}/a.names"
printf 'shared-pg-harbor\nshared-pg-keycloak\n' | sort > "${COV_T}/b.names"
[ "$(coverage_verdict "${COV_T}" a b)" = "COVERED" ] \
  || { echo "SELF-TEST FAIL: two regions with identical coverage not COVERED" >&2; exit 2; }

# THE REQUIRED NEGATIVE — two regions, the secret in only one. MUST go red.
printf 'shared-pg-harbor\n' | sort > "${COV_T}/b.names"
[ "$(coverage_verdict "${COV_T}" a b)" = "SPLIT" ] \
  || { echo "SELF-TEST FAIL: a two-region deployment with a ONE-region secret was not SPLIT — this is the #6016 shape and the whole point of this phase" >&2; exit 2; }
grep -q '^b shared-pg-keycloak$' "${COV_T}/missing" \
  || { echo "SELF-TEST FAIL: SPLIT did not NAME the missing region/secret pair" >&2; exit 2; }

# The hw293 shape specifically: the peer region holds NOTHING at all. The old
# per-region loop reported this as clean.
: > "${COV_T}/b.names"
[ "$(coverage_verdict "${COV_T}" a b)" = "SPLIT" ] \
  || { echo "SELF-TEST FAIL: a region holding ZERO secrets alongside a populated peer was not SPLIT — that is exactly the state the per-region loop called OK on hw293" >&2; exit 2; }

# And the trap itself: ONE region can never manufacture a COVERED pass.
[ "$(coverage_verdict "${COV_T}" a)" = "NO-PEER" ] \
  || { echo "SELF-TEST FAIL: a single-region fixture produced a coverage verdict — one region must be inert, never 'clean'" >&2; exit 2; }
echo "OK — coverage self-test passed (COVERED / SPLIT / NO-PEER distinguished; the one-region fixture cannot pass as covered)."

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

# ─── Phase 2 — cross-region COVERAGE sweep (#6016) ───────────────────────
#
# Phase 1 asked each region about ITSELF. This asks the regions about EACH
# OTHER, which is the only way a per-region split is visible. Runs even when a
# region has no consumers at all — that is the case Phase 1 skips past, and the
# case that wedged hw293.
echo ""
echo "== cross-region coverage =="

COV_LIVE="$(mktemp -d)"
trap 'rm -rf "${COV_T}" "${COV_LIVE}"' EXIT

cov_regions=()
cov_readable=1
for ctx in "${CONTEXTS[@]}"; do
  safe="$(printf '%s' "${ctx}" | tr -c 'A-Za-z0-9_.-' '_')"
  # The tracked set = what bp-postgres renders primary-side and the cross-mesh
  # sync is meant to carry onward: the per-owner role Secrets (basic-auth,
  # MINUS `-superuser`, which CNPG mints locally per cluster and is legitimately
  # region-local) plus the consumer hub Secrets. The CNPG-generated TLS material
  # (`-replication` / `-ca` / `-server`) is likewise region-local and excluded.
  if ! timeout 30 kubectl --context "${ctx}" -n "${MINT_NS}" get secret \
        -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.type}{"\n"}{end}' \
        2>/dev/null > "${COV_LIVE}/${safe}.raw"; then
    echo "  WARN: could not read ${MINT_NS} secrets in ${ctx} — coverage NOT evaluated (reporting nothing rather than a false split)" >&2
    cov_readable=0
    break
  fi
  awk '$2=="kubernetes.io/basic-auth" && $1 !~ /-superuser$/ { print $1; next }
       $1 ~ /-database-secret/ || $1 ~ /-database-env/       { print $1 }' \
    "${COV_LIVE}/${safe}.raw" | sort -u > "${COV_LIVE}/${safe}.names"
  printf '  %-28s holds %s tracked Secret(s)\n' "${ctx}" "$(wc -l < "${COV_LIVE}/${safe}.names")"
  cov_regions+=("${safe}")
done

if [ "${cov_readable}" -eq 1 ]; then
  cov_verdict="$(coverage_verdict "${COV_LIVE}" "${cov_regions[@]}")"
  case "${cov_verdict}" in
    NO-PEER)
      echo "  skip  single region — a per-region split is not expressible with one context." ;;
    COVERED)
      echo "  OK    every tracked Secret is present in EVERY region." ;;
    SPLIT)
      EXIT=1
      echo "  FAIL  per-region SPLIT — a Secret exists in one region and not another:" >&2
      while IFS=' ' read -r r n; do
        [ -z "${n}" ] && continue
        printf '        MISSING in %-24s %s\n' "${r}" "${n}" >&2
      done < "${COV_LIVE}/missing"
      echo "" >&2
      echo "        A consumer scheduled in the region that lacks the Secret cannot" >&2
      echo "        start: CNPG projects the role Secret into its initdb Job with" >&2
      echo "        optional:false (CreateContainerConfigError), and every workload" >&2
      echo "        gated behind that instance stalls with it. On hw293 this" >&2
      echo "        surfaced four hops downstream as keycloak-0 Init:0/1 and 8" >&2
      echo "        non-Ready HelmReleases, which is why it took three walkers to" >&2
      echo "        trace. Fix the PRODUCER (the primary-side render + the" >&2
      echo "        cross-mesh hub-secret sync) — do NOT hand-copy the Secret." >&2 ;;
  esac
fi

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
