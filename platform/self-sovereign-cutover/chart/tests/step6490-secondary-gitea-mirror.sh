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
  'sec-gitea-pat.yaml' \
  'cutover-secondary-gitea-pat'; do
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

echo "[step6490] (b) genuine BYTE-IDENTICAL check against the main this tree left (when git is available)"
# The second tree is where this tree left main (`git merge-base HEAD origin/main`),
# never main's tip, which can hold later commits that changed this chart: in a
# release run for commit X the release writer has fetched origin/main, and run
# 44832081080 (7dff3b89f) failed here on the change of #8549, merged after it. X
# is on main, so its merge base is X and the run judges X; on a pull request's
# merge ref it is the first parent, however far main has moved; on a branch, the
# branch's base. "origin/main" in the axes below names that tree.
# tools/guards/cmd/release-own-tree runs this check with main ahead.
BASE_TREE="$(git -C "${REPO_ROOT}" merge-base HEAD origin/main 2>/dev/null || true)"
if [ -n "${BASE_TREE}" ]; then
  git -C "${REPO_ROOT}" archive "${BASE_TREE}" platform/self-sovereign-cutover/chart 2>/dev/null \
    | tar -x -C "${TMP}" 2>/dev/null
  MAIN_CHART="${TMP}/platform/self-sovereign-cutover/chart"
  if [ -d "${MAIN_CHART}" ]; then
    helm template ssc "${MAIN_CHART}" --set sovereign.fqdn="${FQDN}" --set mirrorResync.enabled=true \
      >"${TMP}/main.yaml" 2>/dev/null
    # Normalise TWENTY-FOUR axes that are EXPECTED to differ from origin/main and are NOT
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
    #   3. the #6754 step-01 primary push rewrite (batched default-branch-first).
    #      That push ALWAYS renders (single-region too), so it is NOT part of the
    #      secondaryRegions leg either; the awk collapses the whole push region
    #      (between the two step-01 push echoes) to one marker on BOTH trees, and
    #      the three new always-rendered lines it introduces (the PUSH_BATCH_BRANCHES
    #      env pair + the two script-local assignments) are stripped, so this check
    #      still catches any OTHER single-region drift while accommodating the
    #      intended batched-push change, which Case 46 verifies. #7922 reshaped
    #      the same region again (default branch + tags only, Actions switched off
    #      first) inside the same two echoes; step7922-gitea-mirror-refs.sh
    #      executes it.
    #   4. the #5596 step-06 Phase-2 factoring. The bootstrap-kit rewrite
    #      (url sed + inject_trust_ref + Phase-2.5) moved from the inline args
    #      into the ALWAYS-rendered ConfigMap key kit-rewrite.sh, which region-A
    #      sources via a new /kit-rewrite mount, and helmRepositoryPatchesSeconds
    #      moved 2400 -> 3600 for the secondary leg's wait. None of that is the
    #      secondaryRegions leg; the awk drops the kit-rewrite.sh key, collapses
    #      the Phase-2 region (target_dir= .. its last line: the Phase-2.5 SKIP
    #      `fi` on origin/main, the rewrite_kit_tree call here) to one marker on
    #      BOTH trees, and the mount/volume + deadline are canonicalised.
    #      step5596-secondary-durable-rewrite.sh verifies the factoring itself.
    #   5. #7900 changed four templates in EVERY topology: steps 03, 08 and 11
    #      (the write-failure class, the catalogue-only guard, the front-door
    #      sample and mesh rule, the live Provider wait) and removed the
    #      auto-trigger Job. None of that is the follower leg. While origin/main
    #      does not hold that change yet (its render lacks the front-door-sample
    #      anchor), the documents of those four templates are left out of BOTH
    #      trees, with every `---` separator; the token gate above still reads
    #      them in the OFF render, and tests/fresh-install-handover-chain.sh
    #      verifies the change itself. Once origin/main holds the change this
    #      axis switches itself off and the whole render is compared again.
    #   6. the release of kured's install hold (#7903) in step-08. It runs on a
    #      single-region Sovereign too (kured is held there as well), so it is
    #      NOT part of the secondaryRegions leg: the function, its two calls and
    #      the one delete of the hold that the EXIT trap already covered
    #      (fresh-pull failure path) are dropped on BOTH trees.
    #      reboot-hold-release.sh verifies the release itself.
    #   7. the #7901 server-side apply of every Secret the chain writes. The three
    #      flags are on always-rendered lines (steps 00, 05, 06, 11) and are not
    #      the secondaryRegions leg; scripts/check-secret-apply-server-side.sh
    #      verifies them. Removing the flags on BOTH trees keeps every other
    #      character of those lines compared.
    #   8. #7922 changed step 03 in EVERY topology: the image walk reads every
    #      region, the proxy is latched down only when its registry API does not
    #      answer, and a run guard ends the step. None of that is the follower
    #      leg. While origin/main does not hold it (its render lacks the
    #      prewarm-run-guard anchor), the step-03 document is left out of BOTH
    #      trees the way axis 5 does it; tests/step7922-prewarm-proxy-path.sh
    #      verifies the change itself. The axis switches itself off once
    #      origin/main holds the change.
    #   9. #7938 changed the tail of the day-two CATALOG leg's refusal line (it
    #      now names the release route), rendered in every topology and not the
    #      follower leg; daytwo-catalog-leg.sh executes the refusal. The tail
    #      after "No override exists for this refusal" is canonicalised on BOTH
    #      trees.
    #  10. #7938 changed step 03 and the chart-set library (03c) in EVERY
    #      topology: Phase A3 renders each pinned chart with the values of every
    #      HelmRelease that installs it, copy_one copies a local-registry ref
    #      from its upstream, and the run guard also checks what the pinned
    #      charts declare. None of that is the follower leg. While origin/main
    #      lacks the prewarm-sovereign-values anchor, the step-03 and 03c
    #      documents are left out of BOTH trees, as axes 5 and 8 do;
    #      tests/step7938-prewarm-sovereign-values.sh verifies the change itself.
    #      The axis switches itself off once origin/main holds the change.
    #  11. #7938 (second delivery) changed step 03's copy worker in EVERY
    #      topology: a failed read is named as a read with its identity, a
    #      destination failure only by skopeo's write-phase errors, and a copy
    #      whose source is its destination is never attempted. None of that is
    #      the follower leg. While origin/main lacks the self-copy refusal line,
    #      the step-03 document is left out of BOTH trees, as axes 5, 8 and 10
    #      do; tests/step7938-prewarm-copy-truth.sh verifies the change itself.
    #      The axis switches itself off once origin/main holds the change.
    #  12. #7938 changed step 08 (the last step, cutover-order 11) in EVERY
    #      topology: before it may report success it deletes, in the primary and
    #      in each follower, the upstream pull credential step 03 copies, and its
    #      re-poke of the GitRepository and Kustomization is one annotate in a
    #      loop. Both render in every topology, so neither is the render-gated
    #      follower leg this check guards. While origin/main
    #      lacks the pull_credential_remove function, the step-08 document is left
    #      out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/step7938-cutover-removes-pull-credentials.sh executes the
    #      removal. The axis switches itself off once origin/main holds it.
    #  13. #7938 (kit images) changed step 03 in EVERY topology: Phase A2-pins
    #      also reads what the kit's Flux Kustomizations apply, with their
    #      `images:` applied, and the images the kit's manifests and those
    #      Kustomizations run join the run guard (and the follower leg's list).
    #      None of that is the follower leg. While origin/main lacks the
    #      prewarm-kit-images anchor, the step-03 document is left out of BOTH
    #      trees, as axes 5, 8 and 10 do; tests/step7938-prewarm-kit-images.sh
    #      executes the change. The axis switches itself off once origin/main
    #      holds the change.
    #  14. step 03's walk also reads each node's sandbox (pause) image from the
    #      kubelet, in EVERY topology, and the runner ClusterRole (rbac.yaml)
    #      gains get on nodes/proxy for it. Neither is the follower leg. While
    #      origin/main lacks prewarm_node_sandbox_images, the step-03 and rbac.yaml
    #      documents are left out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/step03-node-sandbox-image.sh executes the change and asserts the
    #      grant. The axis switches itself off once origin/main holds the change.
    #  15. #7938 (a tag never moves) changed step 03's copy worker and the
    #      day-two image warm in EVERY topology: a tag the local registry holds
    #      at another digest is kept and named, and every copy passes
    #      --preserve-digests. Neither is the follower leg. While origin/main
    #      lacks the 'tag moved upstream' line, the step-03 and day-two documents
    #      are left out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/step7938-prewarm-tag-never-moves.sh executes the change. The
    #      axis switches itself off once origin/main holds the change.
    #  16. FND-G02 (Zero touch): every kubectl write of a HelmRelease or a
    #      workload carries --field-manager=catalyst-self-sovereign-cutover, and
    #      steps 08 and 09 restart by a patch of catalyst.openova.io/restartedAt
    #      instead of kubectl rollout restart, in EVERY topology; rbac.yaml's
    #      comment says so. None of that is the follower leg. The flag is
    #      removed and each restart (either form) becomes one token on BOTH
    #      trees, so every other character of those lines is still compared;
    #      tools/guards/cmd/no-hand-marks holds the change itself.
    #  17. step 08's #3695 host GitOps loop gate waits a bounded time for a
    #      Kustomization that is still progressing and names the HelmReleases
    #      that hold it, in EVERY topology (two env lines and three functions).
    #      None of that is the follower leg. While origin/main lacks
    #      host_ks_wait_ready, the step-08 document is left out of BOTH trees,
    #      as axes 5, 8 and 10 do; tests/host-ks-gate-waits.sh executes the
    #      change. The axis switches itself off once origin/main holds it.
    #  18. SOV-522 moved step 11's package-line rewrite into the key
    #      provider-pivot.sh of its ConfigMap, in EVERY topology; not the
    #      follower leg. While origin/main lacks that key, the step-11 document
    #      is left out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/step7938-stage-reapplies-provider-pivot.sh executes the change.
    #      The axis switches itself off once origin/main holds the change.
    #  19. a delivery's prewarm reads a ref that a pivot moved to harbor.<fqdn>
    #      with the local credential: step 03's copy worker, its presence probe
    #      and the 03a resolver change in EVERY topology; not the follower leg.
    #      While origin/main lacks prewarm_read_through, the step-03 and 03a
    #      documents are left out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/release-prewarm-pivoted-refs.sh executes the change. The axis
    #      switches itself off once origin/main holds the change.
    #  20. SOV-132: step 04's registry-pivot changes only the certs.d files it
    #      wrote, in EVERY topology; not the follower leg. While origin/main
    #      lacks pivot_owns, the step-04 document is left out of BOTH trees, as
    #      axes 5, 8 and 10 do; tests/registry-pivot-keeps-k3s-files.sh executes
    #      the change. The axis switches itself off once origin/main holds it.
    #      SOV-137 widens it: step 04 keeps the k3s embedded mirror first and runs
    #      images.kubectl, in EVERY topology; while origin/main lacks
    #      k3s_mirror_block the same document is left out (the pivot Go tests).
    #  21. every write into the local registry goes through one copy (offlinemirror_copy in
    #      _helpers.tpl: a list as its entries, then its own bytes), in EVERY topology: step
    #      03, the 03a resolver, step 08, the day-two warm and the follower prewarm script
    #      change; not the follower gitea leg. While origin/main lacks offlinemirror_copy,
    #      those documents are left out of BOTH trees, as axes 5, 8 and 10 do;
    #      tests/list-copy-keeps-digests.sh executes the change. The axis switches itself off
    #      once origin/main holds the change.
    #  22. the readiness probe of the registry's public name counts a curl without an HTTP
    #      answer as one not-ready probe (curl_no_answer in _helpers.tpl), in EVERY topology:
    #      step 03 and the follower prewarm script change; not the follower gitea leg. While
    #      origin/main lacks curl_no_answer, those documents are left out of BOTH trees, as
    #      axes 5, 8 and 10 do; tests/prewarm-probe-counts-failed-lookup.sh executes the
    #      change. The axis switches itself off once origin/main holds the change.
    #  23. the OpenTofu provider packages (SOV-343): step 03 Phase A5 copies them, step 11
    #      pivots the CLI configuration, the day-two reconciler gains its tofu leg, in EVERY
    #      topology; not the follower gitea leg. While origin/main lacks tofu_list_read, those
    #      documents are left out of BOTH trees; tests/tofu-providers-prewarm.sh,
    #      tests/tofu-cli-config-pivot.sh and tests/daytwo-tofu-provider-leg.sh execute the
    #      change. The axis switches itself off once origin/main holds the change.
    #  24. the cloud Workspaces and the OpenTofu CLI configuration lint (SOV-344): step 08
    #      counts the Workspaces through its hold and lints the configuration before it, and
    #      rbac.yaml grants the read of the Workspaces, in EVERY topology; not the follower gitea
    #      leg. While origin/main lacks ws_regressed, the step-08 and rbac documents are left out
    #      of BOTH trees; tests/egress-hold-workspaces.sh and tests/tofu-cli-config-host-lint.sh
    #      execute the change. The axis switches itself off once origin/main holds the change.
    drop344=0
    grep -q 'ws_regressed() {' "${TMP}/main.yaml" || drop344=1
    droptofu=0
    grep -q 'tofu_list_read() {' "${TMP}/main.yaml" || droptofu=1
    dropnoa=0
    grep -q 'curl_no_answer() {' "${TMP}/main.yaml" || dropnoa=1
    dropcpy=0
    grep -q 'offlinemirror_copy() {' "${TMP}/main.yaml" || dropcpy=1
    drop04o=0
    { grep -q 'pivot_owns() {' "${TMP}/main.yaml" && grep -q 'k3s_mirror_block() {' "${TMP}/main.yaml"; } || drop04o=1
    droppiv=0
    grep -q 'prewarm_read_through() {' "${TMP}/main.yaml" || droppiv=1
    drop08k=0
    grep -q 'host_ks_wait_ready() {' "${TMP}/main.yaml" || drop08k=1
    drop522=0
    grep -q '^  provider-pivot.sh: |$' "${TMP}/main.yaml" || drop522=1
    drop7938t=0
    grep -qF 'tag moved upstream' "${TMP}/main.yaml" || drop7938t=1
    drop7900=0
    grep -q 'front-door-sample BEGIN' "${TMP}/main.yaml" || drop7900=1
    drop7922=0
    grep -q 'prewarm-run-guard BEGIN' "${TMP}/main.yaml" || drop7922=1
    drop7938=0
    grep -q 'prewarm-sovereign-values BEGIN' "${TMP}/main.yaml" || drop7938=1
    drop7938b=0
    grep -qF 'is its own destination (#7938)' "${TMP}/main.yaml" || drop7938b=1
    drop7938c=0
    grep -q 'pull_credential_remove() {' "${TMP}/main.yaml" || drop7938c=1
    drop7938k=0
    grep -q 'prewarm-kit-images BEGIN' "${TMP}/main.yaml" || drop7938k=1
    drop03s=0
    grep -q 'prewarm_node_sandbox_images() {' "${TMP}/main.yaml" || drop03s=1
    norm() {
      awk -v drop7900="${drop7900}" -v drop7922="${drop7922}" -v drop7938="${drop7938}" -v drop7938b="${drop7938b}" -v drop7938c="${drop7938c}" -v drop7938k="${drop7938k}" -v drop03s="${drop03s}" -v drop7938t="${drop7938t}" -v drop08k="${drop08k}" -v drop522="${drop522}" -v droppiv="${droppiv}" -v drop04o="${drop04o}" -v dropcpy="${dropcpy}" -v dropnoa="${dropnoa}" -v droptofu="${droptofu}" -v drop344="${drop344}" '
        (drop7900==1 || drop7922==1 || drop7938==1 || drop7938b==1 || drop7938c==1 || drop7938k==1 || drop03s==1 || drop7938t==1 || drop08k==1 || drop522==1 || droppiv==1 || drop04o==1 || dropcpy==1 || dropnoa==1 || droptofu==1 || drop344==1) && /^---$/ {next}
        /^# Source: / {_d7 = (drop7900==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|08-egress-block-test-job|10-auto-trigger-job|11-crossplane-provider-pivot-job)\.yaml$/) || (drop7922==1 && $0 ~ /\/templates\/03-harbor-prewarm-job\.yaml$/) || (drop7938==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|03c-catalog-chart-set)\.yaml$/) || (drop7938b==1 && $0 ~ /\/templates\/03-harbor-prewarm-job\.yaml$/) || (drop7938c==1 && $0 ~ /\/templates\/08-egress-block-test-job\.yaml$/) || (drop7938k==1 && $0 ~ /\/templates\/03-harbor-prewarm-job\.yaml$/) || (drop03s==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|rbac)\.yaml$/) || (drop7938t==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|12-daytwo-harbor-pin-reconciler)\.yaml$/) || (drop08k==1 && $0 ~ /\/templates\/08-egress-block-test-job\.yaml$/) || (drop522==1 && $0 ~ /\/templates\/11-crossplane-provider-pivot-job\.yaml$/) || (droppiv==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|03a-offline-mirror-resolver)\.yaml$/) || (drop04o==1 && $0 ~ /\/templates\/04-registry-pivot-daemonset\.yaml$/) || (dropcpy==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|03a-offline-mirror-resolver|08-egress-block-test-job|12-daytwo-harbor-pin-reconciler|secondary-harbor-prewarm-script-configmap)\.yaml$/) || (dropnoa==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|secondary-harbor-prewarm-script-configmap)\.yaml$/) || (droptofu==1 && $0 ~ /\/templates\/(03-harbor-prewarm-job|11-crossplane-provider-pivot-job|12-daytwo-harbor-pin-reconciler)\.yaml$/) || (drop344==1 && $0 ~ /\/templates\/(08-egress-block-test-job|rbac)\.yaml$/)}
        _d7==1 {next}
        /if ! \$\{fresh_pull_fn\}; then$/ {print; _fp=1; next}
        _fp==1 {_fp=0; if ($0 ~ /kubectl delete ciliumclusterwidenetworkpolicy cutover-egress-block --ignore-not-found=true \|\| true$/) next}
        /^ *reboot_hold_release\(\) \{$/ {_rh=1; next}
        _rh==1 {if ($0 ~ /^ *\}$/) _rh=0; next}
        /^ *reboot_hold_release$/ {next}
        /^  kit-rewrite\.sh: \|$/ {_kk=1; next}
        _kk==1 && /# ---- end rewrite_kit_tree ----$/ {_kk=0; next}
        _kk==1 {next}
        /target_dir="clusters\/_template\/bootstrap-kit"$/ && _kr==0 {print "KIT_REWRITE_CANON"; _kr=1; next}
        _kr==1 && /rewrite_kit_tree "\$\{target_dir\}"$/ {_kr=2; next}
        _kr==1 && /Phase-2\.5 SKIP: \$\{catalyst_yaml\} not present in local mirror/ {_kr=3; next}
        _kr==3 {_kr=2; next}
        _kr==1 {next}
        /^          - name: kit-rewrite-script$/ {_km=3}
        /^      - name: kit-rewrite-script$/ {_km=6}
        _km>0 {_km--; next}
        /\[gitea-mirror\] pushing upstream/ {print "GITEA_PUSH_CANON"; _skip=1; next}
        _skip==1 && /\[gitea-mirror\] pushed all upstream/ {_skip=0; next}
        _skip==1 {next}
        {print}
      ' "$1" | sed -E \
        -e 's#bp-self-sovereign-cutover-[0-9]+\.[0-9]+\.[0-9]+#bp-self-sovereign-cutover-VERSION#g' \
        -e 's#^( *activeDeadlineSeconds: )(2400|3600)$#\1STEP06_DEADLINE#' \
        -e 's# --server-side --force-conflicts --field-manager=self-sovereign-cutover##g' \
        -e 's# --field-manager=catalyst-self-sovereign-cutover##g' \
        -e 's#kubectl rollout restart "\$\{kind\}/\$\{name\}" -n "\$\{ns\}"#RESTART_WORKLOAD#' \
        -e 's#kubectl patch "\$\{kind\}/\$\{name\}" -n "\$\{ns\}" -p "\{.*catalyst\.openova\.io/restartedBy.*\}"#RESTART_WORKLOAD#' \
        -e 's#kubectl -n "\$\{DEST_SECRET_NAMESPACE\}" rollout restart deploy/provisioning#RESTART_PROVISIONING#' \
        -e 's#kubectl -n "\$\{DEST_SECRET_NAMESPACE\}" patch deploy/provisioning -p "\{.*catalyst\.openova\.io/restartedBy.*\}"#RESTART_PROVISIONING#' \
        -e 's#(to prove fresh-pullability) — (kubectl rollout restart|the restart) patches (catalyst\.openova\.io/restartedAt onto )?the workload#\1 — RESTART_COMMENT the workload#' \
        -e 's#(No override exists for this refusal).*"$#\1 CATALOG_REFUSAL_TAIL"#' \
        -e '/- name: PUSH_BATCH_BRANCHES/{N;d}' \
        -e '/^ *default_branch="\$\{UPSTREAM_BRANCH\}"$/d' \
        -e '/^ *batch_size="\$\{PUSH_BATCH_BRANCHES\}"$/d' \
        -e 's#^ *local_url=.*GITEA_PASSWORD.*$#                  MIRROR_URL_DEF#' \
        -e 's#^ *push_url="\$\{GITEA_INTERNAL_URL\}/\$\{GITEA_ORG\}/\$\{GITEA_REPO\}\.git"$#                  MIRROR_URL_DEF#' \
        -e 's#git -c http\.extraHeader="Authorization: Basic \$\{basic_auth\}" push --mirror#git push --mirror#' \
        -e 's#push --mirror --force "\$\{(local_url|push_url)\}"#push --mirror --force "MIRROR_URL"#'
    }
    # One more axis, kept out of the twenty-three above: step 08's #3526 pivot check
    # re-reconciles the org-tenants source and names each offender's owning Kustomization,
    # in EVERY topology; not the follower leg. While origin/main lacks
    # the owner= field, the step-08 document is left out of BOTH trees;
    # tests/step08-pivot-names-owner.sh executes the change. The axis switches
    # itself off once origin/main holds it.
    if ! grep -qF '{.spec.url} owner={.metadata.labels.kustomize' "${TMP}/main.yaml"; then
      for t in main off; do
        awk '/^# Source: / {d = ($0 ~ /\/templates\/08-egress-block-test-job\.yaml$/)} d != 1 {print}' "${TMP}/${t}.yaml" >"${TMP}/${t}.no08"
        mv "${TMP}/${t}.no08" "${TMP}/${t}.yaml"
      done
    fi
    norm "${TMP}/main.yaml" >"${TMP}/main.norm"
    norm "${TMP}/off.yaml"  >"${TMP}/off.norm"
    if diff -q "${TMP}/main.norm" "${TMP}/off.norm" >/dev/null; then
      pass "the ENTIRE single-region render is byte-identical to the main this tree left (${BASE_TREE}), modulo the twenty-four axes normalised above, each verified by its own test"
    else
      fail "single-region render DIFFERS from the main this tree left (${BASE_TREE}) beyond the twenty-four normalised axes — see diff:"
      diff "${TMP}/main.norm" "${TMP}/off.norm" | head -40
    fi
  else
    echo "  skip — could not archive the chart of ${BASE_TREE}; the token-absence gate above is the no-op proof"
  fi
else
  echo "  skip — no merge base with origin/main in this checkout (not fetched, or the history is cut); the token-absence gate above is the no-op proof"
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
# #6754 — step-05's SECONDARY leg wires the SHARED catalyst-gitea-token PAT (the
# same /tmp/git-auth-secret.yaml region-A built) into the secondary's Flux
# secretRef, NOT the per-region gitea-admin-secret (whose password drifts across
# the one shared cross-region gitea DB). This assertion FAILS on the old template
# (which read gitea-admin-secret) and PASSES on the PAT-unified one.
# #7901 — applied server-side: a client-side apply keeps the PAT in the
# Secret's last-applied annotation.
if grep -qF 'kubectl --kubeconfig "${skc}" apply --server-side --force-conflicts --field-manager=self-sovereign-cutover -f /tmp/git-auth-secret.yaml' "${TMP}/step05.sh"; then
  pass "step-05 wires the SHARED catalyst-gitea-token PAT into the secondary Flux secretRef (#6754)"
else
  fail "step-05 does not apply the PAT basic-auth Secret (/tmp/git-auth-secret.yaml) to the secondary (#6754)"
fi
if grep -qF 'git-auth-secondary' "${TMP}/step05.sh" || grep -qF 'GITEA_ADMIN_SECRET_NAME' "${TMP}/step05.sh"; then
  fail "step-05 still reads the secondary's own gitea-admin-secret — its password drifts across the shared cross-region DB and 401s (#6754 regression)"
else
  pass "step-05 no longer reads the per-region gitea-admin-secret for the secondary pivot credential (#6754)"
fi
bash -n "${TMP}/step05.sh" && pass "step-05 script parses (bash -n)" || fail "step-05 script has a syntax error"

echo "[step6490] (a) the shared secondary-mirror script pushes to the IN-CLUSTER Gitea + ls-remote gates"
python3 - "${TMP}/on.yaml" "${TMP}/secmirror.sh" "${TMP}/secjob.yaml" <<'PY'
import sys, yaml
docs=[d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get('kind')=='ConfigMap' and d['metadata']['name']=='cutover-secondary-mirror-script':
        open(sys.argv[2],'w').write(d['data']['secondary-mirror.sh'])
        open(sys.argv[3],'w').write(d['data']['secondary-mirror-job.yaml'].replace('__REGION__','me-east-215-b-1').replace('__SOURCE_REF__','main'))
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
# #6754 BUG 1 — the secondary authenticates ALL gitea calls with the SHARED
# catalyst-gitea-token PAT: /api/v1 via Authorization: token, git-over-HTTP via a
# user:PAT token_auth header. The PAT arrives via secretKeyRef (GITEA_PAT) from
# the Secret region-A copies in; there is NO admin-BasicAuth and NO in-region mint
# (the two regions share ONE gitea DB, so the admin password drifts and the mint —
# which itself needs admin BasicAuth — cannot run). hw305 region-B (dep
# b2b00ce4c833badf) proved both admin-password paths 401 on a 2-region Sovereign.
if grep -qF 'Authorization: token ${GITEA_PAT}' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh /api/v1 calls authenticate with the shared catalyst-gitea-token PAT (Authorization: token) (#6754)"
else
  fail "secondary-mirror.sh does not authenticate the /api/v1 calls with the shared PAT (#6754)"
fi
if grep -qF 'token_auth=$(printf' "${TMP}/secmirror.sh" \
   && grep -qF '${GITEA_USERNAME}:${GITEA_PAT}' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh builds the token_auth header from user:PAT (#6754)"
else
  fail "secondary-mirror.sh does not build a user:PAT git-HTTP header (#6754)"
fi
# the Job reads GITEA_PAT via secretKeyRef from the distributed PAT Secret, and
# GITEA_USERNAME is the plain gitRepositorySecretRef.username (no admin-secret).
if grep -qF 'cutover-secondary-gitea-pat' "${TMP}/secjob.yaml" \
   && grep -qF 'key: token' "${TMP}/secjob.yaml"; then
  pass "secondary mirror Job secretKeyRef's the distributed catalyst-gitea-token PAT (#6754)"
else
  fail "secondary mirror Job does not secretKeyRef the distributed PAT (#6754)"
fi
if grep -qF 'value: "gitea_admin"' "${TMP}/secjob.yaml" \
   && ! grep -qF 'GITEA_PASSWORD' "${TMP}/secjob.yaml"; then
  pass "secondary mirror Job sets a plain GITEA_USERNAME and reads NO gitea-admin password (#6754)"
else
  fail "secondary mirror Job still reads a gitea-admin password or lacks the plain username (#6754)"
fi
# #6754 anti-regression (non-vacuous — TRUE on the #6645 template, FALSE now):
# the secondary MUST NOT mint a region-local PAT, and MUST NOT reference basic_auth.
if grep -qF 'users/${GITEA_USERNAME}/tokens' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still mints a region-local PAT — the mint needs admin BasicAuth, which 401s against the shared cross-region DB (#6754 regression)"
else
  pass "secondary-mirror.sh does not mint a region-local PAT (#6754)"
fi
if grep -qF 'basic_auth' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still references the admin-password BasicAuth (basic_auth) — it drifts across the shared cross-region DB (#6754 regression)"
else
  pass "secondary-mirror.sh git-auths with the shared PAT, never the admin password (#6754)"
fi
# #6754 BUG 2 — default-branch-first force-push: default branch FIRST on its own
# ref-command, then tags. A single receive-pack of all ~2,436 branch refs dies
# after the ref advertisement (pkt-line 3 EOF). #7922 — and NO other upstream
# branch: no consumer reads one, and each cost a hook process + an Actions scan.
# step7922-gitea-mirror-refs.sh EXECUTES the script against fixture repos.
if grep -qF 'push --force "${push_url}" "refs/heads/${SOURCE_REF}:refs/heads/${default_branch}"' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh force-pushes the default branch first on its own ref-command (#6754)"
else
  fail "secondary-mirror.sh does not push the default branch first — the whole-repo push dies after the ref advertisement (#6754)"
fi
if grep -qF -e 'git for-each-ref --format=' -e '${batch}' "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still enumerates and pushes the remaining upstream branches — no consumer reads them (#7922)"
else
  pass "secondary-mirror.sh pushes no upstream branch beyond the default branch (#7922)"
fi
if grep -qF "'refs/heads/*:refs/heads/*'" "${TMP}/secmirror.sh"; then
  fail "secondary-mirror.sh still pushes every head in one 'refs/heads/*:refs/heads/*' receive-pack — the whole-repo push that dies (#6754)"
else
  pass "secondary-mirror.sh no longer pushes every head in one receive-pack (#6754)"
fi
if grep -qF 'http.extraHeader="Authorization: Basic ${token_auth}" ls-remote --heads' "${TMP}/secmirror.sh"; then
  pass "secondary-mirror.sh ls-remote self-verifies via the shared-PAT header (the #6490 ordering gate, #6754 auth)"
else
  fail "secondary-mirror.sh lacks the PAT-authed git ls-remote success gate (#6754)"
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
# #6754 BUG 2 — the PRIMARY step-01 push is default-branch-first too (same
# ~2,436-branch / pkt-line-3-EOF exposure as the secondary). Assert it here on the
# extracted step-01 script (Case 46 in cutover-contract.sh guards the same shape).
# #7922 — and it pushes no other upstream branch (step7922-gitea-mirror-refs.sh
# executes it).
if grep -qF 'push --force "${local_url}" "refs/heads/${src_ref}:refs/heads/${default_branch}"' "${TMP}/step01.sh"; then
  pass "step-01 primary push sends the default branch first on its own ref-command (#6754)"
else
  fail "step-01 primary push does not push the default branch first (#6754)"
fi
if grep -qF -e 'git for-each-ref --format=' -e '${batch}' "${TMP}/step01.sh"; then
  fail "step-01 primary push still enumerates and pushes the remaining upstream branches — no consumer reads them (#7922)"
else
  pass "step-01 primary push pushes no upstream branch beyond the default branch (#7922)"
fi
if grep -qF "'refs/heads/*:refs/heads/*'" "${TMP}/step01.sh"; then
  fail "step-01 primary push still sends every head in one 'refs/heads/*:refs/heads/*' receive-pack — the whole-repo push that dies (#6754)"
else
  pass "step-01 primary push no longer sends every head in one receive-pack (#6754)"
fi
# Extract just the #6490 leg (leg-start echo .. the no-op echo) and close the if.
awk '/#6490 secondary-region mirror leg/,/secondary mirror is a no-op/' "${TMP}/step01.sh" >"${TMP}/leg.body"
{ echo 'set -eu'
  echo 'SECONDARY_GITEA_NAMESPACE=gitea'
  echo 'MIRROR_JOB_WAIT_SECONDS=5'
  echo 'MIRROR_JOB_POLL_SECONDS=1'
  # #6754 — the leg now distributes the shared PAT into each secondary before
  # stamping the Job; the harness must set the same env the render env-block does.
  echo 'SECONDARY_GITEA_PAT_SECRET=cutover-secondary-gitea-pat'
  echo 'PAT_SOURCE_NAMESPACE=catalyst-system'
  echo 'PAT_SOURCE_SECRET_NAME=catalyst-gitea-token'
  echo 'GITEA_PAT=testpat'
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
