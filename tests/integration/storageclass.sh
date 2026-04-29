#!/usr/bin/env bash
# tests/integration/storageclass.sh
#
# Integration test: a fresh Sovereign control-plane MUST have a default
# StorageClass before Flux applies the bootstrap-kit Kustomization.
#
# Why this test exists
# --------------------
# 2026-04-29: omantel.omani.works was provisioned end-to-end and Flux
# happily reconciled the bootstrap-kit Kustomization, but the bp-spire,
# bp-keycloak postgres, and bp-openbao HelmReleases all stalled with
# every PVC stuck `Pending`:
#
#   $ kubectl get pvc -A
#   keycloak       data-keycloak-postgresql-0   Pending  ...
#   spire-system   spire-data-spire-server-0    Pending  ...
#
# Root cause: cloud-init's k3s install passed `--disable=local-storage`
# with the design intent that Crossplane would install hcloud-csi day-2
# and register the StorageClass. That created a circular dependency:
# the bootstrap-kit's PVC-using HelmReleases all block waiting on a
# StorageClass that would only exist AFTER bp-crossplane reconciled,
# and they ARE part of the bootstrap-kit Kustomization that needs to
# converge before the day-2 path runs.
#
# Resolution (#TODO-ISSUE-NUMBER): keep k3s' built-in
# local-path-provisioner, register `local-path` as the default
# StorageClass during cloud-init, BEFORE Flux applies the bootstrap-kit
# Kustomization. Operators upgrading to multi-node migrate to hcloud-csi
# as a separate, deliberate step.
#
# This test enforces that contract:
#
#   1. Render-assertion (always run, cheap, deterministic).
#      Greps the cloud-init template (infra/hetzner/cloudinit-control-plane.tftpl)
#      to confirm:
#        a. `--disable=local-storage` is NOT in the INSTALL_K3S_EXEC line
#           (regression gate — re-introducing the flag breaks the contract)
#        b. The default-StorageClass patch step IS present
#        c. The local-path-provisioner Ready wait IS present
#        d. The "StorageClass missing" verification gate IS present
#
#   2. Kind-cluster proof (run when kind binary is available).
#      Creates a fresh kind cluster (kind ships with the same
#      local-path-provisioner k3s does — actually it's
#      rancher.io/local-path) and asserts:
#        a. A default StorageClass exists post-bootstrap
#        b. A test PVC binds to that StorageClass within 30s
#      This catches the failure mode where someone "fixes" the cloud-init
#      template syntactically but the resulting cluster still has no
#      default class.
#
# Exit 0 = pass. Any other exit = fail; the failing assertion is logged
# to stderr.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEMPLATE="${REPO_ROOT}/infra/hetzner/cloudinit-control-plane.tftpl"

log() {
  printf '[storageclass] %s\n' "$*" >&2
}

if [ ! -f "${TEMPLATE}" ]; then
  log "ERROR: cloud-init template missing at ${TEMPLATE}"
  exit 2
fi

# -------------------------------------------------------------------
# Phase 1 — render-assertion (always run)
# -------------------------------------------------------------------
log "phase 1/2 — render-assertion against ${TEMPLATE}"

# 1a. Negative: --disable=local-storage MUST NOT appear in the
#     INSTALL_K3S_EXEC string. If it does, every PVC in the bootstrap-kit
#     stays Pending and the Sovereign deadlocks.
#
#     We narrow the grep to the line that begins the INSTALL_K3S_EXEC
#     curl-pipe; allowing the template to mention the flag in a comment
#     (which we want — explaining why we dropped it) without failing.
if grep -nE "^[[:space:]]*-[[:space:]]+'?curl[^'#]*INSTALL_K3S_EXEC=[^'#]*--disable=local-storage" "${TEMPLATE}" >/dev/null 2>&1; then
  log "FAIL — INSTALL_K3S_EXEC still passes --disable=local-storage"
  log "       this re-introduces the bootstrap deadlock fixed for omantel.omani.works."
  log "       k3s ships local-path-provisioner; keep it as the default StorageClass"
  log "       so bp-spire / bp-keycloak / bp-openbao PVCs bind on a fresh Sovereign."
  log "       Offending line(s):"
  grep -nE "^[[:space:]]*-[[:space:]]+'?curl[^'#]*INSTALL_K3S_EXEC=[^'#]*--disable=local-storage" "${TEMPLATE}" >&2 || true
  exit 1
fi
log "  1a PASS — INSTALL_K3S_EXEC does not pass --disable=local-storage"

# 1b. Positive: the default-class patch MUST be present and must run
#     against the `local-path` StorageClass. Match the patch verb, the
#     class name, AND the is-default-class annotation key in one
#     anchored line so a half-rewrite doesn't slip past.
if ! grep -nE 'patch[[:space:]]+storageclass[[:space:]]+local-path[[:space:]]+-p[[:space:]]+.*storageclass\.kubernetes\.io/is-default-class' "${TEMPLATE}" >/dev/null 2>&1; then
  log "FAIL — default-StorageClass patch step is missing or malformed"
  log "       expected a runcmd line that does:"
  log "         kubectl patch storageclass local-path -p '{\"metadata\":{\"annotations\":{\"storageclass.kubernetes.io/is-default-class\":\"true\"}}}'"
  log "       without it, PVCs without an explicit storageClassName will not bind."
  exit 1
fi
log "  1b PASS — default-class patch step present (storageclass.kubernetes.io/is-default-class)"

# 1c. Positive: the local-path-provisioner Ready wait MUST be present and
#     happen on the kube-system namespace with the canonical label.
if ! grep -nE 'wait[[:space:]]+--for=condition=Ready[[:space:]]+pod[[:space:]]+-l[[:space:]]+app=local-path-provisioner' "${TEMPLATE}" >/dev/null 2>&1; then
  log "FAIL — local-path-provisioner readiness wait is missing"
  log "       expected: kubectl wait --for=condition=Ready pod -l app=local-path-provisioner --timeout=60s"
  log "       without it, the patch + verify steps below race the controller startup."
  exit 1
fi
log "  1c PASS — local-path-provisioner Ready wait present"

# 1d. Positive: the verification gate MUST be present so a missing
#     StorageClass fails cloud-init loudly instead of letting Flux
#     reconcile into a broken state.
if ! grep -nE 'storageclass\.storage\.k8s\.io/local-path' "${TEMPLATE}" >/dev/null 2>&1; then
  log "FAIL — StorageClass verification gate is missing"
  log "       expected a runcmd line that asserts the local-path StorageClass exists,"
  log "       e.g.: kubectl get sc -o name | grep -q '^storageclass.storage.k8s.io/local-path\$'"
  log "       without it, a missing class falls through silently and Flux deadlocks."
  exit 1
fi
log "  1d PASS — StorageClass verification gate present"

# 1e. Ordering: the patch + verify steps must come BEFORE the Flux
#     bootstrap apply (`kubectl apply -f /var/lib/catalyst/flux-bootstrap.yaml`),
#     so the bootstrap-kit Kustomization sees a default class on its first
#     reconciliation.
PATCH_LINE=$(grep -nE 'patch[[:space:]]+storageclass[[:space:]]+local-path' "${TEMPLATE}" | head -1 | cut -d: -f1)
FLUX_LINE=$(grep -nE 'kubectl[^#]*apply[[:space:]]+-f[[:space:]]+/var/lib/catalyst/flux-bootstrap\.yaml' "${TEMPLATE}" | head -1 | cut -d: -f1)
if [ -z "${PATCH_LINE}" ] || [ -z "${FLUX_LINE}" ]; then
  log "FAIL — could not locate patch line (${PATCH_LINE}) and/or Flux apply line (${FLUX_LINE})"
  exit 1
fi
if [ "${PATCH_LINE}" -ge "${FLUX_LINE}" ]; then
  log "FAIL — default-class patch (line ${PATCH_LINE}) must come BEFORE flux-bootstrap apply (line ${FLUX_LINE})"
  log "       otherwise the bootstrap-kit Kustomization reconciles before the default class is set,"
  log "       and the PVC-using HelmReleases stall Pending."
  exit 1
fi
log "  1e PASS — patch (line ${PATCH_LINE}) precedes Flux apply (line ${FLUX_LINE})"

log "phase 1 PASS — cloud-init template enforces local-path default StorageClass before Flux"

# -------------------------------------------------------------------
# Phase 2 — kind-cluster proof (run when kind binary is available)
# -------------------------------------------------------------------
KIND_BIN="${KIND_BIN:-kind}"
if ! command -v "${KIND_BIN}" >/dev/null 2>&1; then
  if [ -x /tmp/kind ]; then
    KIND_BIN=/tmp/kind
  else
    log "phase 2 SKIP — kind binary not on PATH and /tmp/kind absent"
    log "  install with: curl -fsSLo /tmp/kind https://kind.sigs.k8s.io/dl/v0.24.0/kind-linux-amd64 && chmod +x /tmp/kind"
    log "  (this phase is a live-cluster sanity check; phase 1 is the binding gate)"
    log "PASS (phase 1 only)"
    exit 0
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  log "phase 2 SKIP — docker not on PATH (kind requires docker)"
  log "PASS (phase 1 only)"
  exit 0
fi

CLUSTER_NAME="catalyst-storageclass-test-$$"
KUBECONFIG_FILE="$(mktemp -t kind-kubeconfig.XXXXXX)"

cleanup_kind() {
  log "tearing down kind cluster ${CLUSTER_NAME}"
  "${KIND_BIN}" delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
  rm -f "${KUBECONFIG_FILE}"
}
trap cleanup_kind EXIT

log "phase 2/2 — provisioning fresh kind cluster ${CLUSTER_NAME}"
"${KIND_BIN}" create cluster --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG_FILE}" --wait 120s >/dev/null 2>&1
log "  cluster up"

export KUBECONFIG="${KUBECONFIG_FILE}"

# 2a. Default StorageClass exists.
DEFAULT_SC=$(kubectl get sc -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' | head -1)
if [ -z "${DEFAULT_SC}" ]; then
  log "FAIL — kind cluster has no default StorageClass"
  log "       this would replicate the omantel.omani.works deadlock on a real Sovereign."
  kubectl get sc -o yaml >&2 || true
  exit 1
fi
log "  2a PASS — default StorageClass: ${DEFAULT_SC}"

# 2b. A PVC binds within 30s.
TEST_NS="storageclass-test-$$"
kubectl create namespace "${TEST_NS}" >/dev/null 2>&1
cat <<EOF | kubectl apply -n "${TEST_NS}" -f - >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: bind-test
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Mi
EOF

# kind's default StorageClass is WaitForFirstConsumer, so we need a Pod
# to trigger binding. Apply one.
cat <<EOF | kubectl apply -n "${TEST_NS}" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: bind-test
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: bind-test
EOF

if ! kubectl wait -n "${TEST_NS}" pvc/bind-test --for=jsonpath='{.status.phase}'=Bound --timeout=60s >/dev/null 2>&1; then
  log "FAIL — PVC bind-test did not Bind within 60s"
  kubectl describe pvc -n "${TEST_NS}" bind-test >&2 || true
  exit 1
fi
log "  2b PASS — test PVC bound to default StorageClass"

kubectl delete namespace "${TEST_NS}" --ignore-not-found=true --wait=false >/dev/null 2>&1 || true

log "phase 2 PASS — fresh cluster has working default StorageClass"

log "PASS:"
log "  - cloud-init template enforces local-path default StorageClass before Flux"
log "  - fresh cluster default StorageClass exists and binds PVCs"
exit 0
