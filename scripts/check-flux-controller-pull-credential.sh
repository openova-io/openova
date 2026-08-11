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
#    FailedToRetrieveImagePullSecret on every controller pod.
if grep -rq "name: ${secret}\$" "${REPO_ROOT}/infra/providers/_shared/cloudinit-control-plane.tftpl"; then
  note ""
  note "  secret '${secret}' is created by the shared cloud-init bootstrap"
else
  bad "the patch names imagePullSecret '${secret}', which the shared cloud-init"
  bad "        bootstrap does not create in flux-system — the patch would be inert."
fi

note ""
if [ "$fail" -eq 0 ]; then
  note "PASS — every Flux controller ServiceAccount carries a pull credential of its own."
  exit 0
fi
note "FAIL — see above. Refs #6079."
exit 1
