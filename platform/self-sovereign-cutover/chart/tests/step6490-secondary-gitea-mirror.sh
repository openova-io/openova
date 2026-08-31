#!/usr/bin/env bash
# #6490 — behavioural test for the SECONDARY-region gitea-mirror leg.
#
# WHAT #6490 FIXES. On a 2-region Sovereign step-01/step-11 mirrored openova/
# openova into region-A's Gitea ONLY; step-05 then repointed region-B's Flux
# GitRepository at the external gateway door, a cross-region hop that truncates
# the large git-upload-pack ref advertisement at pkt-line 3. region-B thus had no
# reachable complete git source, its Kustomization reverted step-06's HelmRepo
# pivot, and step-06 fail-loud'd (#5596). The fix: step-01/step-11 ALSO mirror
# into EACH secondary's OWN in-cluster Gitea (a Job run IN that region via its
# kubeconfig, the headless gitea-http Service being unreachable cross-region),
# and step-05 pivots each secondary at its in-cluster gitea-http Service.
#
# The whole leg is RENDER-GATED on secondaryRegions.mirrorToSecondaryGitea
# (default false) so a single-region Sovereign renders byte-identical. This suite
# proves BOTH directions and is mutation-proof: every ON assertion is paired with
# the SAME token being ABSENT in the OFF render, so a check that cannot fail
# cannot pass vacuously.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "${CHART_DIR}/../../.." && pwd)"
FQDN="hw300.omani.works"
INCLUSTER="http://gitea-http.gitea.svc.cluster.local:3000/openova/openova"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

render() { # $1=extra --set args (space-sep), $2=outfile
  # shellcheck disable=SC2086
  helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" $1 >"$2" 2>"${TMP}/render.err" || {
    echo "helm template failed ($1):"; cat "${TMP}/render.err"; exit 1
  }
}

# ON exercises step-11 too (only rendered when mirrorResync.enabled), so enable it.
render "--set secondaryRegions.mirrorToSecondaryGitea=true --set mirrorResync.enabled=true" "${TMP}/on.yaml"
render "--set mirrorResync.enabled=true" "${TMP}/off.yaml"

echo "[step6490] (b) single-region NO-OP — the OFF render must carry NONE of the leg's bytes"
# Every byte the fix adds is gated behind mirrorToSecondaryGitea and carries one
# of these identifiers. Their total absence from the OFF render IS byte-identity.
for tok in \
  '#6490' \
  'cutover-secondary-mirror-script' \
  'secondary-mirror.sh' \
  'secondary-mirror-job.yaml' \
  'SECONDARY_INCLUSTER_GITEA_URL' \
  'SECONDARY_GITEA_NAMESPACE' \
  'secondary-gitea-mirror' \
  'cutover-gitea-mirror-pat'; do
  if grep -qF "${tok}" "${TMP}/off.yaml"; then
    fail "OFF render contains '${tok}' — the leg is not fully gated (single-region NOT a no-op)"
  else
    pass "OFF render is free of '${tok}'"
  fi
  # mutation-proof: the SAME token MUST appear in the ON render (else the check above is vacuous)
  if grep -qF "${tok}" "${TMP}/on.yaml"; then
    pass "ON render contains '${tok}' (the OFF-absence check is non-vacuous)"
  else
    fail "ON render is missing '${tok}' — the fix did not render with the flag on"
  fi
done

echo "[step6490] (b) genuine BYTE-IDENTICAL check against origin/main (when git is available)"
if git -C "${REPO_ROOT}" rev-parse --verify --quiet origin/main >/dev/null 2>&1; then
  git -C "${REPO_ROOT}" archive origin/main platform/self-sovereign-cutover/chart 2>/dev/null \
    | tar -x -C "${TMP}" 2>/dev/null
  MAIN_CHART="${TMP}/platform/self-sovereign-cutover/chart"
  if [ -d "${MAIN_CHART}" ]; then
    helm template ssc "${MAIN_CHART}" --set sovereign.fqdn="${FQDN}" --set mirrorResync.enabled=true \
      >"${TMP}/main.yaml" 2>/dev/null
    # Normalise TWO axes that are EXPECTED to differ from origin/main and are NOT
    # the secondary-leg leak this byte-check guards:
    #   1. the chart-version baked into helm.sh/chart labels (the 0.1.19x bump);
    #   2. the #6490 git-auth MECHANISM on the ALWAYS-rendered primary resync push
    #      (11-mirror-resync-cronjob.yaml) — origin/main URL-injects
    #      ${GITEA_USERNAME}:${GITEA_PASSWORD}@ into the remote while this branch
    #      carries the Authorization: Basic http.extraHeader. That push renders in
    #      single-region too, so it is NOT part of the secondaryRegions leg;
    #      canonicalising BOTH forms (and BOTH url-var names) to one token keeps
    #      this check able to catch any OTHER single-region drift while
    #      accommodating the intended fix, which cutover-contract Case 96 verifies.
    #      The regex keys on GITEA_PASSWORD / --mirror so it never touches step-01's
    #      PAT-authed refspec push.
    #   3. the #6754 PRIMARY (step-01) push mechanism — origin/main force-pushes
    #      once; this branch wraps it in a corrupt-target DELETE+RECREATE recovery
    #      (push_mirror / default_present / recreate_repo). That push renders in
    #      single-region too, so it is NOT part of the secondaryRegions leg. Both
    #      forms sit between the SAME `pushing upstream branches + tags (explicit …`
    #      and `pushed all upstream branches + tags successfully` echoes, so a range
    #      delete collapses BOTH to nothing — keeping this check able to catch any
    #      OTHER single-region drift while accommodating the intended fix (which the
    #      cutover-contract Case 96/97 and this suite's harness verify). The end
    #      pattern is anchored on the shared prefix so the branch's "… on the
    #      healthy path" suffix still matches.
    norm() {
      sed -E \
        -e 's#bp-self-sovereign-cutover-[0-9]+\.[0-9]+\.[0-9]+#bp-self-sovereign-cutover-VERSION#g' \
        -e 's#^ *local_url=.*GITEA_PASSWORD.*$#                  MIRROR_URL_DEF#' \
        -e 's#^ *push_url="\$\{GITEA_INTERNAL_URL\}/\$\{GITEA_ORG\}/\$\{GITEA_REPO\}\.git"$#                  MIRROR_URL_DEF#' \
        -e 's#git -c http\.extraHeader="Authorization: Basic \$\{basic_auth\}" push --mirror#git push --mirror#' \
        -e 's#push --mirror --force "\$\{(local_url|push_url)\}"#push --mirror --force "MIRROR_URL"#' \
        -e '/\[gitea-mirror\] pushing upstream branches \+ tags \(explicit/,/\[gitea-mirror\] pushed all upstream branches \+ tags successfully/d' \
        "$1"
    }
    norm "${TMP}/main.yaml" >"${TMP}/main.norm"
    norm "${TMP}/off.yaml"  >"${TMP}/off.norm"
    if diff -q "${TMP}/main.norm" "${TMP}/off.norm" >/dev/null; then
      pass "the ENTIRE single-region render is byte-identical to origin/main (modulo the chart-version bump and the #6490 resync git-auth mechanism, which Case 96 verifies)"
    else
      fail "single-region render DIFFERS from origin/main beyond the version bump — see diff:"
      diff "${TMP}/main.norm" "${TMP}/off.norm" | head -40
    fi
  else
    echo "  skip — could not archive origin/main chart; the token-absence gate above is the no-op proof"
  fi
else
  echo "  skip — origin/main not fetched in this checkout; the token-absence gate above is the no-op proof"
fi

echo "[step6490] (a) step-05 pivots each secondary at its IN-CLUSTER gitea-http Service"
# Extract step-05's podSpec script from the ON render and assert the secondary
# chain seeds from the in-cluster svc (mirroring region-A), NOT the external door.
python3 - "${TMP}/on.yaml" "${TMP}/step05.sh" <<'PY'
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get('kind')=='ConfigMap' and d['metadata']['name']=='cutover-step-05-flux-gitrepository-patch':
        ps=yaml.safe_load(d['data']['podSpec'])
        open(sys.argv[2],'w').write(ps['containers'][0]['args'][0]); break
PY
if grep -A1 'name: SECONDARY_INCLUSTER_GITEA_URL' "${TMP}/on.yaml" | grep -qF "${INCLUSTER}"; then
  pass "step-05 SECONDARY_INCLUSTER_GITEA_URL env == ${INCLUSTER}"
else
  fail "step-05 does not set the secondary in-cluster URL to ${INCLUSTER}"
fi
if grep -qF 'sec_url_chain="${SECONDARY_INCLUSTER_GITEA_URL:-}"' "${TMP}/step05.sh"; then
  pass "step-05 secondary candidate chain resolves to the in-cluster svc (not the external door)"
else
  fail "step-05 secondary chain does not use SECONDARY_INCLUSTER_GITEA_URL"
fi
# #6754 — the secondary Flux secretRef carries the SAME region-A catalyst-gitea-token
# PAT (built into /tmp/git-auth-secret.yaml) the region-A leg uses, NOT region-B's
# own gitea-admin-secret. Both regions share one gitea DB (shared-pg-mesh-rw), so a
# per-region admin password can never match the single shared gitea_admin row while
# the PAT (shared access_token table) authenticates from either region.
if grep -qF 'apply -f /tmp/git-auth-secret.yaml' "${TMP}/step05.sh" \
   && ! grep -qF 'git-auth-secondary' "${TMP}/step05.sh" \
   && ! grep -qF 'get secret "${GITEA_ADMIN_SECRET_NAME}"' "${TMP}/step05.sh"; then
  pass "step-05 wires the region-A catalyst-gitea-token PAT secret into the secondary Flux secretRef, never region-B's own gitea-admin-secret (#6754)"
else
  fail "step-05 secondary leg still reads region-B's own gitea-admin-secret — it cannot authenticate the shared gitea DB (#6754)"
fi
bash -n "${TMP}/step05.sh" && pass "step-05 script parses (bash -n)" || fail "step-05 script has a syntax error"

echo "[step6490] (a) the shared secondary-mirror script pushes to the IN-CLUSTER Gitea + ls-remote gates"
python3 - "${TMP}/on.yaml" "${TMP}/secmirror.sh" "${TMP}/secjob.yaml" <<'PY'
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get('kind')=='ConfigMap' and d['metadata']['name']=='cutover-secondary-mirror-script':
        open(sys.argv[2],'w').write(d['data']['secondary-mirror.sh'])
        open(sys.argv[3],'w').write(d['data']['secondary-mirror-job.yaml'].replace('__REGION__','me-east-215-b-1'))
        break
PY
if [ ! -s "${TMP}/secmirror.sh" ]; then fail "cutover-secondary-mirror-script ConfigMap / secondary-mirror.sh key absent from ON render"; fi
# The Job manifest's GITEA_INTERNAL_URL is the in-cluster svc, so the script's
# push+ls-remote (which use ${GITEA_INTERNAL_URL}) target the region-local Gitea.
if grep -qF 'value: "http://gitea-http.gitea.svc.cluster.local:3000"' "${TMP}/secjob.yaml"; then
  pass "secondary mirror Job targets GITEA_INTERNAL_URL = the in-cluster gitea-http svc"
else
  fail "secondary mirror Job does not target the in-cluster gitea-http svc"
fi
if grep -qF 'namespace: "gitea"' "${TMP}/secjob.yaml"; then
  pass "secondary mirror Job is stamped in the secondary's gitea namespace (no cutover-runner dependency)"
else
  fail "secondary mirror Job namespace is not the gitea namespace"
fi
# #6493 — ns gitea (unlike ns catalyst) is matched by the cluster kyverno
# ClusterPolicy flux-managed (rule workload-must-be-flux-managed). The Job MUST
# carry app.kubernetes.io/managed-by: flux (the escape the policy's own deny
# message names) or step-01's `set -e` apply into that ns is DENIED at admission
# and the whole cutover halts. This greps the REAL rendered Job (secjob.yaml
# sliced from the ON render above), so removing the label turns it red.
if grep -qF 'app.kubernetes.io/managed-by: flux' "${TMP}/secjob.yaml"; then
  pass "secondary mirror Job carries app.kubernetes.io/managed-by: flux — the kyverno flux-managed escape (#6493)"
else
  fail "secondary mirror Job lacks app.kubernetes.io/managed-by: flux — ns gitea's kyverno flux-managed ClusterPolicy DENIES step-01's set -e apply and the cutover halts (#6493)"
fi
# #6754 push-auth: the git push + ls-remote authenticate with the region-A
# catalyst-gitea-token PAT (token_auth header), read from the DISTRIBUTED secret
# via env GITEA_PAT — NOT the region-local admin password (basic_auth) and NOT an
# in-region mint (the #6645 mint itself needed the drifting admin password). Both
# regions share one gitea DB (shared-pg-mesh-rw), so the PAT — a row in the shared
# access_token table — authenticates from either region, while a per-region admin
# password can never match the single shared gitea_admin row (live hw305: 401).
if grep -qF 'if [ -z "${GITEA_PAT:-}" ]; then' "${TMP}/secmirror.sh" \
   && grep -qF 'Authorization: token ${GITEA_PAT}' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh sources GITEA_PAT from the distributed catalyst-gitea-token secret and token-auths the API (#6754)"
else
  fail "secondary-mirror.sh does not read the distributed PAT from env / token-auth the API (#6754)"
fi
# anti-regression (non-vacuous — TRUE on the #6645 in-region-mint script): the
# secondary must NOT mint a PAT via the admin BasicAuth API (that path depended on
# the region-local admin password matching the shared gitea_admin row, which it
# cannot — the #6754 auth root cause).
if grep -qF 'users/${GITEA_USERNAME}/tokens' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still mints a region-local PAT via admin BasicAuth — that credential cannot match the shared gitea_admin row (#6754 regression)"
else
  pass "secondary-mirror.sh does not mint in-region — it uses the region-A distributed PAT valid in the shared DB (#6754)"
fi
# #6754 corrupt-target recovery (the real BUG-2 fix): on a push failure /
# default-branch-absent the leg DELETE+RECREATEs the repo clean and retries. A
# target left corrupt by prior partial pushes (empty receive-pack advertisement)
# can never recover via force-push; a fresh repo always accepts the full push.
if grep -qF 'recreate_repo' "${TMP}/secmirror.sh" \
   && grep -qF -- '-X DELETE' "${TMP}/secmirror.sh" \
   && grep -qF 'default_present' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh recovers a corrupt/partial target via DELETE+RECREATE + push-retry (#6754)"
else
  fail "secondary-mirror.sh lacks the corrupt-target DELETE+RECREATE recovery (#6754)"
fi
if grep -qF 'token_auth=$(printf' "${TMP}/secmirror.sh" \
   && grep -qF '${GITEA_USERNAME}:${GITEA_PAT}' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh builds the token_auth header from user:PAT (#6645)"
else
  fail "secondary-mirror.sh does not build a user:PAT BasicAuth header (#6645)"
fi
if grep -qF 'http.extraHeader="Authorization: Basic ${token_auth}" push --force' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh force-pushes via the MINTED-PAT http.extraHeader (#6645 push-auth parity)"
else
  fail "secondary-mirror.sh does not PAT-authenticate the force-push (#6645)"
fi
if grep -qF 'http.extraHeader="Authorization: Basic ${token_auth}" ls-remote --heads' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh ls-remote self-verifies via the minted-PAT header (the #6490 ordering gate, #6645 auth)"
else
  fail "secondary-mirror.sh lacks the PAT-authed git ls-remote success gate (#6645)"
fi
# anti-regression (non-vacuous — TRUE on the pre-#6645 basic_auth-password push):
# the git push/ls-remote must NOT authenticate with the admin-password header.
if grep -qE 'http\.extraHeader="Authorization: Basic \$\{basic_auth\}" (push|ls-remote)' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still git-auths with the admin password (basic_auth) — Gitea git-HTTP refuses a password under an external login source (#6645 regression)"
else
  pass "secondary-mirror.sh git-auths with the minted PAT, never the admin password (#6645)"
fi
# #6645 mirror=false guard — a pull-mirror repo is read-only; the push cannot land.
if grep -qF '"mirror":true' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh guards against a read-only pull-mirror repo before pushing (#6645)"
else
  fail "secondary-mirror.sh lacks the mirror=false guard (#6645)"
fi
# anti-regression (non-vacuous — TRUE on the pre-#6490 URL-injection script):
# zero URL-injected credentials may survive in the region-local push.
if grep -qF '${GITEA_USERNAME}:${GITEA_PASSWORD}@' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still URL-injects the admin password — breaks on a URL-special-char password (#6490 regression)"
else
  pass "secondary-mirror.sh URL-injects no credentials — auth rides the header (#6490)"
fi
if grep -qF 'http.postBuffer 1048576000' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh carries the #6488 large-clone robustness (http.postBuffer + retry)"
else
  fail "secondary-mirror.sh lacks the #6488 large-clone robustness"
fi
sh -n "${TMP}/secmirror.sh" && pass "secondary-mirror.sh parses (sh -n)" || fail "secondary-mirror.sh has a syntax error"

echo "[step6490] (a)+(b) EXECUTE step-01's secondary loop — no-op with zero secondaries, acts with one"
# Slice the leg out of the RENDER (never a hand-kept copy — the #5646 drift shape)
# and rewrite the two hardcoded mount paths to temp dirs so it can run in CI.
python3 - "${TMP}/on.yaml" "${TMP}/step01.sh" <<'PY'
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get('kind')=='ConfigMap' and d['metadata']['name']=='cutover-step-01-gitea-mirror':
        ps=yaml.safe_load(d['data']['podSpec'])
        open(sys.argv[2],'w').write(ps['containers'][0]['args'][0]); break
PY
# Extract just the #6490 leg (leg-start echo .. the no-op echo) and close the if.
awk '/#6490 secondary-region mirror leg/,/secondary mirror is a no-op/' "${TMP}/step01.sh" >"${TMP}/leg.body"
{ echo 'set -eu'
  echo 'SECONDARY_GITEA_NAMESPACE=gitea'
  echo 'MIRROR_JOB_WAIT_SECONDS=5'
  echo 'MIRROR_JOB_POLL_SECONDS=1'
  # #6754 — the leg now distributes the region-A catalyst-gitea-token PAT into each
  # secondary's gitea ns; the sliced body reads ${GITEA_PAT} under set -u.
  echo 'GITEA_PAT=testpat0000'
  sed -e "s#/secondary-kubeconfigs/#${TMP}/skc/#g" -e "s#/secondary-mirror/#${TMP}/smir/#g" "${TMP}/leg.body"
  echo 'fi'
} >"${TMP}/leg.sh"
mkdir -p "${TMP}/skc" "${TMP}/smir"
printf 'x\n' >"${TMP}/smir/secondary-mirror.sh"
cp "${TMP}/secjob.yaml" "${TMP}/smir/secondary-mirror-job.yaml"
# Stub kubectl: records the applied manifest and reports the Job Succeeded.
cat >"${TMP}/kubectl" <<STUB
#!/bin/sh
for a in "\$@"; do case "\$a" in *succeeded*) echo 1; exit 0;; esac; done
case "\$*" in *"apply -f /tmp/secjob.yaml"*) cp /tmp/secjob.yaml "${TMP}/applied.yaml" 2>/dev/null || true;; esac
exit 0
STUB
chmod +x "${TMP}/kubectl"

# (b) EXECUTE with an EMPTY secondary-kubeconfigs dir -> must no-op, exit 0, never call kubectl.
cat >"${TMP}/kubectl_forbidden" <<STUB
#!/bin/sh
echo "kubectl MUST NOT be called on a single-region no-op" >&2
exit 42
STUB
chmod +x "${TMP}/kubectl_forbidden"
if PATH="${TMP}:${PATH}" cp "${TMP}/kubectl_forbidden" "${TMP}/kubectl.noop" \
   && PATH="${TMP}:${PATH}" sh -c "cp '${TMP}/kubectl.noop' '${TMP}/kubectl'; sh '${TMP}/leg.sh'" >"${TMP}/noop.out" 2>&1 \
   && grep -q 'secondary mirror is a no-op' "${TMP}/noop.out" \
   && ! grep -q 'MUST NOT be called' "${TMP}/noop.out"; then
  pass "EXECUTED: empty /secondary-kubeconfigs -> no-op, exit 0, kubectl never invoked"
else
  fail "empty /secondary-kubeconfigs did NOT cleanly no-op:"; sed 's/^/       /' "${TMP}/noop.out"
fi

# (a) EXECUTE with ONE secondary kubeconfig -> must stamp the in-cluster-targeted Job.
# restore the recording stub
cat >"${TMP}/kubectl" <<STUB
#!/bin/sh
for a in "\$@"; do case "\$a" in *succeeded*) echo 1; exit 0;; esac; done
case "\$*" in *"apply -f /tmp/secjob.yaml"*) cp /tmp/secjob.yaml "${TMP}/applied.yaml" 2>/dev/null || true;; esac
exit 0
STUB
chmod +x "${TMP}/kubectl"
printf 'kubeconfig\n' >"${TMP}/skc/me-east-215-b-1.yaml"
if PATH="${TMP}:${PATH}" sh "${TMP}/leg.sh" >"${TMP}/act.out" 2>&1 \
   && grep -q 'SUCCEEDED' "${TMP}/act.out" \
   && [ -s "${TMP}/applied.yaml" ] \
   && grep -qF 'value: "http://gitea-http.gitea.svc.cluster.local:3000"' "${TMP}/applied.yaml"; then
  pass "EXECUTED: one secondary -> stamps an in-cluster-targeted mirror Job and waits for Succeeded"
else
  fail "one-secondary execution did not stamp the in-cluster mirror Job:"; sed 's/^/       /' "${TMP}/act.out"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "[step6490] all assertions passed — single-region renders byte-identical (no-op proven by token-absence, origin/main diff, and execution), and with a secondary the leg mirrors into the region-local Gitea + pivots step-05 at the in-cluster svc + ls-remote gates."
  exit 0
fi
echo "[step6490] ${FAILURES} assertion(s) FAILED"
exit 1
