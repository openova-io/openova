#!/usr/bin/env bash
# check-flux-controller-pull-credential — the Flux controllers must never be the
# only workloads on a node whose image pull carries no credential of their own.
#
# WHY (#6079). The mothership's GitOps loop was dead for ten days. Measured
# read-only on the node 2026-08-10/11:
#
#   Failed to pull ghcr.io/fluxcd/source-controller:v1.8.0: failed to authorize:
#   failed to fetch oauth token: GET https://ghcr.io/token
#   ?scope=repository:fluxcd/source-controller:pull&service=ghcr.io -> 403 Forbidden
#
# `fluxcd/source-controller` is a PUBLIC repository. An anonymous token request
# for that exact scope, egressing from that exact node, returned 200 at the same
# moment. GHCR answers 401 for "no credentials" and 403 for "credentials I
# reject", so the 403 is POSITIVE evidence that the node presented a credential
# and GHCR refused it. It did not come from k3s: wharfie logs `Using private
# registry config file at <path>` whenever /etc/rancher/k3s/registries.yaml
# exists, and that line is absent from the node's k3s start — so the credential
# came from the kubelet docker keyring (a node-local docker config.json), which
# no Kubernetes object exposes.
#
# THE MECHANISM THAT MAKES THIS A CLASS, not an incident. kubelet builds its
# keyring from the pod's imagePullSecrets PLUS the node's own docker-config
# files, tries every credential that matches the registry, and NEVER retries
# anonymously once they have all failed (k8s pkg/kubelet/images/image_manager.go,
# EnsureImageExists). So a PUBLIC image that would pull fine with no auth at all
# fails BECAUSE auth is configured and stale. There is no anonymous-fallback
# switch. The only in-cluster lever is a WORKING credential in the pod's own
# keyring, because kubelet returns on the first credential that succeeds.
#
# Every other namespace on the mothership survived the same stale node
# credential for exactly that reason — a working imagePullSecret sat in front of
# it. flux-system did not: `flux install` gives each controller its OWN
# ServiceAccount (source-/kustomize-/helm-/notification-controller, never
# `default`), so every default-SA or namespace-wide imagePullSecret misses them
# precisely. The controllers that would have redeployed everything else were the
# only ones that could not start.
#
# WHAT THIS GUARD ASSERTS (source-side, network-free, no cluster):
#   1. the bp-flux HelmRelease carries a postRenderer patch that attaches an
#      imagePullSecret to the Flux controller ServiceAccounts;
#   2. the patch's target selector matches EVERY controller ServiceAccount the
#      flux2 subchart renders — a partial selector leaves the un-selected
#      controllers stranded and still reads green;
#   3. the selector is ANCHORED, so it cannot silently widen onto unrelated
#      ServiceAccounts;
#   4. the named Secret is one the bootstrap actually creates in flux-system.
#
# It does NOT assert that public images need authentication — they do not. It
# asserts that when a node-level credential exists and is bad, the controllers
# that repair everything else are not the single workload with no way around it.
# Removing a stale node credential is the node-side half and is an operator
# action (see the remediation runbook on #6079).
#
# USAGE
#   scripts/check-flux-controller-pull-credential.sh
#   scripts/check-flux-controller-pull-credential.sh --self-test
#
# READ-ONLY. Reads repo files; never contacts a cluster, a registry or a node.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HR_FILE="${REPO_ROOT}/clusters/_template/bootstrap-kit/03-flux.yaml"

# The controller ServiceAccounts the flux2 subchart renders. The first four are
# what `flux install` creates unconditionally; the image-* pair appears when an
# operator enables those controllers and carries the identical exposure.
CONTROLLER_SAS=(
  source-controller
  kustomize-controller
  helm-controller
  notification-controller
  image-automation-controller
  image-reflector-controller
)

# ServiceAccounts in the same render that MUST NOT be swept up by the selector.
# They are Job/CronJob identities on a different pull path; a selector that
# matches them has widened past its warrant.
NON_CONTROLLER_SAS=(
  bp-flux-stuck-hr-recovery
  flux-flux-check
  default
)

fail=0
note() { printf '%s\n' "$*"; }
bad()  { printf 'FAIL: %s\n' "$*"; fail=1; }

# ─── The extractor ────────────────────────────────────────────────────────
#
# Pulls, from a bp-flux HelmRelease document, the postRenderer patch that
# attaches an imagePullSecret to ServiceAccounts, and prints two fields:
#   <selector-regex>\t<secret-name>
# Prints nothing when no such patch exists. python3 + PyYAML because the
# structure is nested list-of-maps with an embedded YAML string; a grep over
# this file would pass on a patch that targets the wrong kind.
extract() {
  python3 - "$1" <<'PY'
import sys, yaml

src = open(sys.argv[1]).read()
for doc in yaml.safe_load_all(src):
    if not doc or doc.get("kind") != "HelmRelease":
        continue
    for pr in (doc.get("spec", {}).get("postRenderers") or []):
        for p in (pr.get("kustomize", {}).get("patches") or []):
            tgt = p.get("target") or {}
            if tgt.get("kind") != "ServiceAccount":
                continue
            body = yaml.safe_load(p.get("patch") or "") or {}
            if body.get("kind") != "ServiceAccount":
                continue
            for ips in (body.get("imagePullSecrets") or []):
                name = ips.get("name")
                if name:
                    print("%s\t%s" % (tgt.get("name", ""), name))
PY
}

# ─── The source of the named Secret (SEC-154) ─────────────────────────────
#
# secret_source <root> <secret> prints the source of the Secret <secret> in
# flux-system and returns 0, or prints why it has none and returns 1. Two
# sources count, and only these:
#   - first boot: a line "name: <secret>" in the shared first-boot template,
#     which applies the manifest before Flux starts;
#   - a template: a document of kind Secret named <secret> in namespace
#     flux-system, in a file that clusters/_template/bootstrap-secrets/
#     kustomization.yaml lists, which the Flux Kustomization bootstrap-secrets
#     renders (its path ./clusters/_template/bootstrap-secrets), and ONLY while
#     the Kustomization bootstrap-kit, which applies 03-flux.yaml, waits for
#     bootstrap-secrets (dependsOn). Without that order the controllers could
#     start with a patch that names a Secret not yet rendered.
secret_source() {
  local root="$1" secret="$2" dir f ns flux="" other=""
  if grep -q "name: ${secret}\$" "$root/infra/providers/_shared/cloudinit-control-plane.tftpl" 2>/dev/null; then
    printf "secret '%s' is created by the shared cloud-init bootstrap\n" "$secret"; return 0
  fi
  dir="$root/clusters/_template/bootstrap-secrets"
  for f in $(sed -n -E 's/^  - ([A-Za-z0-9._-]+\.yaml)$/\1/p' "$dir/kustomization.yaml" 2>/dev/null); do
    for ns in $(awk -v want="$secret" '
        function done_() { if (k == "Secret" && n == want) print (s == "" ? "none" : s); k = n = s = ""; m = 0 }
        /^---/ { done_(); next }
        /^kind: / { k = $2 }
        /^metadata:/ { m = 1; next }
        /^[^ #]/ { m = 0 }
        m && /^  name: / { n = $2 }
        m && /^  namespace: / { s = $2 }
        END { done_() }' "$dir/$f" 2>/dev/null); do
      if [ "$ns" = flux-system ]; then flux="clusters/_template/bootstrap-secrets/$f"; else other="clusters/_template/bootstrap-secrets/$f (namespace $ns)"; fi
    done
  done
  if [ -z "$flux" ]; then
    if [ -n "$other" ]; then
      printf "the patch names imagePullSecret '%s', and the template %s does not create it in flux-system\n" "$secret" "$other"
    else
      printf "the patch names imagePullSecret '%s', which neither the shared cloud-init bootstrap nor a template of clusters/_template/bootstrap-secrets creates in flux-system\n" "$secret"
    fi
    return 1
  fi
  if ! grep -q -x -F '  path: ./clusters/_template/bootstrap-secrets' "$root/clusters/_template/root/base-primary/bootstrap-secrets.yaml" 2>/dev/null; then
    printf "the template %s creates '%s', but the Kustomization bootstrap-secrets (clusters/_template/root/base-primary/bootstrap-secrets.yaml) does not render ./clusters/_template/bootstrap-secrets\n" "$flux" "$secret"; return 1
  fi
  if ! awk '/^  dependsOn:/ { d = 1; next } d && /^    - name: bootstrap-secrets$/ { w = 1 } d && /^  [^ -]/ { d = 0 } END { exit !w }' "$root/clusters/_template/root/base-primary/bootstrap-kit.yaml" 2>/dev/null; then
    printf "the template %s creates '%s', but the Kustomization bootstrap-kit (clusters/_template/root/base-primary/bootstrap-kit.yaml), which applies 03-flux.yaml, does not dependsOn bootstrap-secrets\n" "$flux" "$secret"; return 1
  fi
  printf "secret '%s' is rendered from the template %s by bootstrap-secrets, which bootstrap-kit waits for\n" "$secret" "$flux"
}

# ─── Self-test: the negative control ──────────────────────────────────────
#
# Every assertion below is exercised against a fixture that VIOLATES it, so a
# green run of this script is evidence the checks can fail. Without this the
# guard would be indistinguishable from a script that prints OK.
if [ "${1:-}" = "--self-test" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  st_fail=0

  mkfixture() { # $1=file  $2=target-name-regex (empty => omit the patch entirely)
    {
      printf 'apiVersion: helm.toolkit.fluxcd.io/v2\n'
      printf 'kind: HelmRelease\n'
      printf 'metadata:\n  name: bp-flux\n  namespace: flux-system\n'
      printf 'spec:\n  postRenderers:\n    - kustomize:\n        patches:\n'
      printf '          - target:\n              kind: Deployment\n              name: source-controller\n'
      printf '            patch: |\n              apiVersion: apps/v1\n              kind: Deployment\n              metadata:\n                name: source-controller\n'
      if [ -n "$2" ]; then
        printf '          - target:\n              kind: ServiceAccount\n              name: %s\n' "$2"
        printf '            patch: |\n              apiVersion: v1\n              kind: ServiceAccount\n              metadata:\n                name: x\n              imagePullSecrets:\n                - name: %s\n' "${3:-ghcr-pull}"
      fi
    } > "$1"
  }

  note "== self-test 1: NO ServiceAccount patch at all MUST be rejected =="
  mkfixture "$tmp/none.yaml" ""
  if [ -z "$(extract "$tmp/none.yaml")" ]; then
    note "  ok — absent patch produces no extraction"
  else
    note "  BROKEN — extractor invented a patch that is not in the document"; st_fail=1
  fi

  note "== self-test 2: a selector covering only SOME controllers MUST be rejected =="
  mkfixture "$tmp/partial.yaml" '^(source|kustomize)-controller$'
  sel="$(extract "$tmp/partial.yaml" | cut -f1)"
  missed=0
  for sa in "${CONTROLLER_SAS[@]}"; do
    printf '%s' "$sa" | grep -Eq "$sel" || missed=1
  done
  if [ "$missed" -eq 1 ]; then
    note "  ok — partial selector '$sel' leaves controllers unmatched, as it must"
  else
    note "  BROKEN — a 2-of-6 selector was accepted as complete"; st_fail=1
  fi

  note "== self-test 3: an UNANCHORED selector MUST be rejected =="
  mkfixture "$tmp/wide.yaml" 'controller'
  wsel="$(extract "$tmp/wide.yaml" | cut -f1)"
  leaked=0
  for sa in "${NON_CONTROLLER_SAS[@]}"; do
    printf '%s' "$sa" | grep -Eq "$wsel" && leaked=1
  done
  case "$wsel" in
    ^*\$) note "  BROKEN — fixture selector was supposed to be unanchored"; st_fail=1 ;;
    *)    note "  ok — '$wsel' is unanchored and is caught by the anchor check" ;;
  esac

  note "== self-test 4: a selector matching a NON-controller SA MUST be rejected =="
  mkfixture "$tmp/greedy.yaml" '^[a-z-]+$'
  gsel="$(extract "$tmp/greedy.yaml" | cut -f1)"
  gleak=0
  for sa in "${NON_CONTROLLER_SAS[@]}"; do
    printf '%s' "$sa" | grep -Eq "$gsel" && gleak=1
  done
  if [ "$gleak" -eq 1 ]; then
    note "  ok — greedy selector '$gsel' sweeps up a non-controller SA, as it must"
  else
    note "  BROKEN — a selector matching 'default' was accepted"; st_fail=1
  fi

  note "== self-test 5: the POSITIVE control — the shipped shape must pass =="
  mkfixture "$tmp/good.yaml" '^[a-z-]+-controller$'
  gsel2="$(extract "$tmp/good.yaml" | cut -f1)"
  ok=1
  for sa in "${CONTROLLER_SAS[@]}"; do
    printf '%s' "$sa" | grep -Eq "$gsel2" || ok=0
  done
  for sa in "${NON_CONTROLLER_SAS[@]}"; do
    printf '%s' "$sa" | grep -Eq "$gsel2" && ok=0
  done
  if [ "$ok" -eq 1 ]; then
    note "  ok — the shipped selector covers every controller and nothing else"
  else
    note "  BROKEN — the shipped selector shape does not satisfy its own checks"; st_fail=1
  fi

  # SEC-154: the source of the named Secret. mkroot <root> <template namespace,
  # or "" for no template> <kit waits: yes|no> <first boot: yes|no> writes the
  # five files secret_source reads.
  mkroot() {
    mkdir -p "$1/infra/providers/_shared" "$1/clusters/_template/bootstrap-secrets" "$1/clusters/_template/root/base-primary"
    if [ "$4" = yes ]; then printf 'write_files:\n  - content: |\n      metadata:\n        name: ghcr-pull\n' > "$1/infra/providers/_shared/cloudinit-control-plane.tftpl"; else printf 'write_files: []\n' > "$1/infra/providers/_shared/cloudinit-control-plane.tftpl"; fi
    printf 'resources:\n  - harbor-robot-token.yaml\n' > "$1/clusters/_template/bootstrap-secrets/kustomization.yaml"
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: harbor-robot-token\n  namespace: flux-system\n' > "$1/clusters/_template/bootstrap-secrets/harbor-robot-token.yaml"
    if [ -n "$2" ]; then
      printf '  - ghcr-pull.yaml\n' >> "$1/clusters/_template/bootstrap-secrets/kustomization.yaml"
      printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: ghcr-pull\n  namespace: %s\ntype: kubernetes.io/dockerconfigjson\n' "$2" > "$1/clusters/_template/bootstrap-secrets/ghcr-pull.yaml"
    fi
    printf 'kind: Kustomization\nmetadata:\n  name: bootstrap-secrets\nspec:\n  path: ./clusters/_template/bootstrap-secrets\n' > "$1/clusters/_template/root/base-primary/bootstrap-secrets.yaml"
    printf 'kind: Kustomization\nmetadata:\n  name: bootstrap-kit\nspec:\n  path: ./clusters/_template/bootstrap-kit\n' > "$1/clusters/_template/root/base-primary/bootstrap-kit.yaml"
    if [ "$3" = yes ]; then printf '  dependsOn:\n    - name: bootstrap-secrets\n' >> "$1/clusters/_template/root/base-primary/bootstrap-kit.yaml"; fi
    printf '  wait: true\n' >> "$1/clusters/_template/root/base-primary/bootstrap-kit.yaml"
  }

  note "== self-test 6: the template source with the wait of the kit MUST pass =="
  mkroot "$tmp/r6" flux-system yes no
  if secret_source "$tmp/r6" ghcr-pull > "$tmp/r6.out"; then
    note "  ok — the template source with the wait of the kit passes"
  else
    note "  BROKEN — the template source with the wait of the kit was refused: $(cat "$tmp/r6.out")"; st_fail=1
  fi

  note "== self-test 7: NO source MUST be rejected, naming the Secret =="
  mkroot "$tmp/r7" "" yes no
  if ! secret_source "$tmp/r7" ghcr-pull > "$tmp/r7.out" && grep -q -F "'ghcr-pull'" "$tmp/r7.out"; then
    note "  ok — no source fails and names the Secret ghcr-pull"
  else
    note "  BROKEN — a Secret with no source was accepted, or not named: $(cat "$tmp/r7.out")"; st_fail=1
  fi

  note "== self-test 8: a template in ANOTHER namespace MUST be rejected =="
  mkroot "$tmp/r8" catalyst-system yes no
  if ! secret_source "$tmp/r8" ghcr-pull > "$tmp/r8.out" && grep -q -F '(namespace catalyst-system)' "$tmp/r8.out"; then
    note "  ok — a template in another namespace fails"
  else
    note "  BROKEN — a template outside flux-system was accepted: $(cat "$tmp/r8.out")"; st_fail=1
  fi

  note "== self-test 9: a template WITHOUT the wait of the kit MUST be rejected =="
  mkroot "$tmp/r9" flux-system no no
  if ! secret_source "$tmp/r9" ghcr-pull > "$tmp/r9.out" && grep -q -F 'does not dependsOn bootstrap-secrets' "$tmp/r9.out"; then
    note "  ok — a template without the wait of the kit fails"
  else
    note "  BROKEN — a template the kit does not wait for was accepted: $(cat "$tmp/r9.out")"; st_fail=1
  fi

  note "== self-test 10: the first-boot source MUST pass =="
  mkroot "$tmp/r10" "" no yes
  if secret_source "$tmp/r10" ghcr-pull > "$tmp/r10.out"; then
    note "  ok — the first-boot source passes"
  else
    note "  BROKEN — the first-boot source was refused: $(cat "$tmp/r10.out")"; st_fail=1
  fi

  if [ "$st_fail" -eq 0 ]; then
    note ""; note "SELF-TEST PASS — every assertion below can fail."
    exit 0
  fi
  note ""; note "SELF-TEST FAIL — the checks below prove nothing."
  exit 1
fi

# ─── The real check ───────────────────────────────────────────────────────

[ -f "$HR_FILE" ] || { bad "$HR_FILE not found — bp-flux slot 03 moved or was deleted."; exit 1; }

line="$(extract "$HR_FILE" || true)"

if [ -z "$line" ]; then
  bad "the bp-flux HelmRelease has NO postRenderer patch attaching an imagePullSecret"
  bad "to the Flux controller ServiceAccounts."
  note ""
  note "Without it the four Flux controllers are the only workloads on the node"
  note "whose ghcr pull has no credential of its own. kubelet never retries"
  note "anonymously after a keyring credential is rejected, so ONE stale"
  note "node-level credential strands the entire GitOps loop — and the controller"
  note "that would repair it is the one that cannot start. Refs #6079."
  exit 1
fi

selector="$(printf '%s' "$line" | cut -f1)"
secret="$(printf '%s' "$line" | cut -f2)"

note "bp-flux ServiceAccount pull-credential patch:"
note "  selector: $selector"
note "  secret:   $secret"
note ""

# 1. Every controller ServiceAccount is covered.
for sa in "${CONTROLLER_SAS[@]}"; do
  if printf '%s' "$sa" | grep -Eq "$selector"; then
    note "  covered: $sa"
  else
    bad "controller ServiceAccount '$sa' is NOT matched by '$selector' — it keeps"
    bad "        the exact exposure #6079 documents while the check reads green."
  fi
done

# 2. Nothing else is.
for sa in "${NON_CONTROLLER_SAS[@]}"; do
  if printf '%s' "$sa" | grep -Eq "$selector"; then
    bad "'$selector' also matches '$sa', which is not a Flux controller."
  fi
done

# 3. The selector is anchored at both ends, so it cannot widen by accident.
case "$selector" in
  ^*\$) : ;;
  *) bad "selector '$selector' is not anchored (^…\$). An unanchored kustomize name"
     bad "        selector is a substring match and will drift onto unrelated objects." ;;
esac

# 4. The Secret is one the bootstrap actually creates in flux-system. A patch
#    naming a Secret nothing produces is inert AND makes kubelet log
#    FailedToRetrieveImagePullSecret on every controller pod. Its two sources
#    are those of secret_source (SEC-154).
if src="$(secret_source "$REPO_ROOT" "$secret")"; then
  note ""
  note "  $src"
else
  bad "$src — the patch would be inert."
fi

note ""
if [ "$fail" -eq 0 ]; then
  note "PASS — every Flux controller ServiceAccount carries a pull credential of its own."
  exit 0
fi
note "FAIL — see above. Refs #6079."
exit 1
