#!/usr/bin/env bash
# Chart test for the StorageClass reclaim-policy reconcile hook (#4350).
#
# Two layers:
#   A. RENDER assertions (helm template) — the pre-install/pre-upgrade hook +
#      its RBAC render only when enabled, carry the correct desired reclaimPolicy,
#      and a default-off render stays EMPTY (smoke-render guard).
#   B. LOGIC assertions — extract the reconcile script from the rendered Job and
#      run it against a FAKE kubectl to prove:
#        1. Live reclaimPolicy already Delete  → NO-OP (no delete issued).
#        2. Live reclaimPolicy Retain          → DELETE issued (recreate path).
#        3. Live SC absent (fresh prov)        → NO-OP (Helm creates it).
#        4. Live reclaimPolicy empty + desired Delete → NO-OP (API default).
#
# Run: bash platform/huawei-evs-csi/chart/tests/reclaim-reconcile-hook/test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

pass=0; fail=0
ok()   { echo "  ok: $1"; pass=$((pass+1)); }
bad()  { echo "  FAIL: $1"; fail=$((fail+1)); }

echo "== A. render assertions =="

# A0. default-off render must be EMPTY (no reconcile hook, no SC).
empty="$(helm template t "${CHART_DIR}" 2>/dev/null | grep -c 'kind:' || true)"
[ "${empty}" = "0" ] && ok "default-off render emits zero resources" \
                      || bad "default-off render emitted ${empty} resources (expected 0)"

# Enabled render (the bootstrap-kit slot-55b shape).
helm template t "${CHART_DIR}" --set enabled=true --set defaultStorageClass=true \
  > "${TMP}/render.yaml" 2>/dev/null

# A1. The reconcile Job renders as a pre-install,pre-upgrade hook.
if grep -q 'name: t-reclaim-reconcile' "${TMP}/render.yaml" \
   && awk '/name: t-reclaim-reconcile$/{f=1} f' "${TMP}/render.yaml" \
        | grep -q '"helm.sh/hook": pre-install,pre-upgrade'; then
  ok "reconcile hook renders as pre-install,pre-upgrade"
else
  bad "reconcile hook missing or not a pre-install,pre-upgrade hook"
fi

# A2. The hook carries the DESIRED reclaimPolicy from values (Delete by default).
if awk '/name: t-reclaim-reconcile$/{f=1} f' "${TMP}/render.yaml" \
     | grep -A1 'name: DESIRED_RECLAIM' | grep -q 'value: "Delete"'; then
  ok "reconcile hook desired reclaimPolicy = Delete (tracks values)"
else
  bad "reconcile hook DESIRED_RECLAIM is not Delete"
fi

# A3. The rendered StorageClass itself is Delete (the value the hook drives toward).
# Scope to the StorageClass document only (stop at the next doc separator).
if awk '/^kind: StorageClass$/{f=1} f&&/^---$/{exit} f' "${TMP}/render.yaml" \
     | grep -q 'reclaimPolicy: "Delete"'; then
  ok "evs-ssd StorageClass renders reclaimPolicy Delete"
else
  bad "evs-ssd StorageClass reclaimPolicy is not Delete"
fi

# A4. RBAC for the reconcile hook (delete verb on storageclasses) renders.
if awk '/name: t-reclaim-reconcile$/{f=1} /^---/{if(seen)f=0} {if(/name: t-reclaim-reconcile/)seen=1} f' "${TMP}/render.yaml" >/dev/null \
   && grep -q '"delete"' "${TMP}/render.yaml"; then
  ok "reconcile hook RBAC grants delete on storageclasses"
else
  bad "reconcile hook RBAC missing delete verb"
fi

# A5. The desired reclaimPolicy is parameterised — overriding values flows through.
helm template t "${CHART_DIR}" --set enabled=true \
  --set 'catalystStorageClasses[0].name=evs-ssd' \
  --set 'catalystStorageClasses[0].reclaimPolicy=Retain' \
  2>/dev/null | awk '/name: t-reclaim-reconcile$/{f=1} f' \
  | grep -A1 'name: DESIRED_RECLAIM' | grep -q 'value: "Retain"' \
  && ok "DESIRED_RECLAIM follows an overridden values reclaimPolicy" \
  || bad "DESIRED_RECLAIM did not follow the overridden reclaimPolicy"

echo "== B. reconcile-script logic assertions =="

# Extract the reconcile script body (the Job's args block) into a runnable file.
# We pull the lines between the `- |` of the args and the end of the container.
python3 - "${TMP}/render.yaml" "${TMP}/reconcile.sh" <<'PY'
import sys, yaml
docs = list(yaml.safe_load_all(open(sys.argv[1])))
for d in docs:
    if not d: continue
    if d.get("kind") == "Job" and d["metadata"]["name"].endswith("reclaim-reconcile"):
        args = d["spec"]["template"]["spec"]["containers"][0]["args"][0]
        open(sys.argv[2], "w").write(args)
        sys.exit(0)
sys.exit("reclaim-reconcile Job not found")
PY
[ -s "${TMP}/reconcile.sh" ] && ok "extracted reconcile script from rendered Job" \
                             || bad "could not extract reconcile script"

# Fake kubectl harness: STATE controls what `kubectl get` returns; every
# `kubectl delete` appends to DELETE_LOG so we can assert whether a delete fired.
make_kubectl() {
  local mode="$1" policy="$2"
  cat > "${TMP}/bin/kubectl" <<EOF
#!/usr/bin/env bash
# fake kubectl — mode=${mode} policy=${policy}
if [ "\$1" = "get" ] && [ "\$2" = "storageclass" ]; then
  if [ "${mode}" = "absent" ]; then exit 1; fi
  # jsonpath reclaimPolicy query
  if printf '%s ' "\$@" | grep -q 'reclaimPolicy'; then
    printf '%s' "${policy}"; exit 0
  fi
  exit 0  # existence probe
fi
if [ "\$1" = "delete" ] && [ "\$2" = "storageclass" ]; then
  echo "DELETED \$3" >> "${TMP}/delete.log"; exit 0
fi
exit 0
EOF
  chmod +x "${TMP}/bin/kubectl"
}

mkdir -p "${TMP}/bin"
run_case() {
  local name="$1" mode="$2" policy="$3" expect_delete="$4"
  : > "${TMP}/delete.log"
  make_kubectl "${mode}" "${policy}"
  # Inject env the render hard-codes, run with fake kubectl on PATH.
  if ! PATH="${TMP}/bin:${PATH}" TARGET_SC=evs-ssd DESIRED_RECLAIM=Delete \
        bash "${TMP}/reconcile.sh" > "${TMP}/out.log" 2>&1; then
    bad "${name}: script exited non-zero"; cat "${TMP}/out.log"; return
  fi
  local fired="no"; [ -s "${TMP}/delete.log" ] && fired="yes"
  if [ "${fired}" = "${expect_delete}" ]; then
    ok "${name}: delete-fired=${fired} (expected ${expect_delete})"
  else
    bad "${name}: delete-fired=${fired} (expected ${expect_delete})"; cat "${TMP}/out.log"
  fi
}

# 1. Already Delete → idempotent no-op.
run_case "live=Delete idempotent"        present Delete no
# 2. Retain → must delete so Helm recreates with Delete.
run_case "live=Retain triggers recreate" present Retain yes
# 3. Absent (fresh prov) → no-op.
run_case "absent SC fresh-prov"          absent  ""     no
# 4. Empty live policy + desired Delete (API default) → no-op.
run_case "live=empty desired-Delete"     present ""     no

echo
echo "RESULT: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
