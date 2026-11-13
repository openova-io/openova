#!/usr/bin/env bash
# CLD-055 spike, L5: does moving a HelmRelease between two Flux
# Kustomizations, with the prune annotation set, avoid an uninstall?
# Flux v2.4.0 (the version the shared template and slot 03 install).
# Each "commit" is a path switch of a Kustomization between prepared states
# of this spike commit; Flux prunes by inventory, so this is the same event.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

kubectl create namespace l5
cat <<EOF | kubectl apply -f -
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: l5src, namespace: flux-system}
spec:
  interval: 1h
  timeout: 10m
  url: https://github.com/openova-io/openova
  ref: {branch: "${GITHUB_REF_NAME}", commit: "${GITHUB_SHA}"}
  ignore: |
    /*
    !/tests/spikes/CLD-055/l5
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: ks-a, namespace: flux-system}
spec: {interval: 1h, prune: true, path: ./tests/spikes/CLD-055/l5/a1, sourceRef: {kind: GitRepository, name: l5src}}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: ks-b, namespace: flux-system}
spec: {interval: 1h, prune: true, path: ./tests/spikes/CLD-055/l5/b1, sourceRef: {kind: GitRepository, name: l5src}}
EOF
kubectl -n flux-system wait gitrepository/l5src --for=condition=Ready --timeout=15m
flux reconcile ks ks-a; flux reconcile ks ks-b
kubectl -n l5 wait hr/dummy hr/ctl --for=condition=Ready --timeout=10m

# name -> "hr_uid deploy_uid history"
snap() {
  local hr_uid dep_uid hist
  hr_uid=$(kubectl -n l5 get hr "$1" -o jsonpath='{.metadata.uid}' 2>/dev/null || echo gone)
  dep_uid=$(kubectl -n l5 get deploy -o json | python3 -c '
import json,sys
n=sys.argv[1]
print(next((d["metadata"]["uid"] for d in json.load(sys.stdin)["items"] if d["metadata"].get("annotations",{}).get("meta.helm.sh/release-name")==n),"gone"))' "$1")
  hist=$(helm history "$1" -n l5 -o json 2>/dev/null | python3 -c '
import json,sys
try: h=json.load(sys.stdin)
except Exception: h=[]
print(";".join("r%s:%s@%s" % (x["revision"], x["status"], x["updated"][:19]) for x in h) or "none")')
  echo "$hr_uid $dep_uid $hist"
}
mv_path() { kubectl -n flux-system patch kustomization "$1" --type merge -p "{\"spec\":{\"path\":\"./tests/spikes/CLD-055/l5/$2\"}}" >/dev/null; flux reconcile ks "$1"; }

read -r D_HR0 D_DEP0 D_HIST0 <<< "$(snap dummy)"
read -r C_HR0 C_DEP0 C_HIST0 <<< "$(snap ctl)"
log "before: dummy $D_HR0 $D_DEP0 $D_HIST0 | ctl $C_HR0 $C_DEP0 $C_HIST0"

log "step 1: annotate dummy (ks-a -> a2)"
mv_path ks-a a2
ANN=$(kubectl -n l5 get hr dummy -o jsonpath='{.metadata.annotations.kustomize\.toolkit\.fluxcd\.io/prune}')
log "step 2: move out of ks-a first (ks-a -> a3), the order in which a prune would bite"
mv_path ks-a a3
sleep 30
read -r D_HR1 D_DEP1 D_HIST1 <<< "$(snap dummy)"
read -r C_HR1 C_DEP1 C_HIST1 <<< "$(snap ctl)"
log "after ks-a lost them: dummy $D_HR1 $D_DEP1 $D_HIST1 | ctl $C_HR1 $C_DEP1 $C_HIST1"
log "step 3: move into ks-b (ks-b -> b2)"
mv_path ks-b b2
kubectl -n l5 wait hr/dummy hr/ctl --for=condition=Ready --timeout=10m
log "step 4: remove the annotation (ks-b -> b3)"
mv_path ks-b b3
sleep 60
flux reconcile ks ks-a; flux reconcile ks ks-b
kubectl -n l5 wait hr/dummy hr/ctl --for=condition=Ready --timeout=10m
read -r D_HR2 D_DEP2 D_HIST2 <<< "$(snap dummy)"
read -r C_HR2 C_DEP2 C_HIST2 <<< "$(snap ctl)"
log "after: dummy $D_HR2 $D_DEP2 $D_HIST2 | ctl $C_HR2 $C_DEP2 $C_HIST2"
OWNER=$(kubectl -n l5 get hr dummy -o jsonpath='{.metadata.labels.kustomize\.toolkit\.fluxcd\.io/name}')
INV_B=$(kubectl -n flux-system get kustomization ks-b -o jsonpath='{.status.inventory.entries[*].id}' | tr ' ' '\n' | grep -c '_dummy_helm.toolkit.fluxcd.io_HelmRelease' || true)
UNINSTALL_EVENTS=$(kubectl -n l5 get events -o json | python3 -c '
import json,sys
print(sum(1 for e in json.load(sys.stdin)["items"] if "uninstall" in (e.get("message","")+e.get("reason","")).lower() and e["involvedObject"]["name"]=="dummy"))')

# The first revision (number and time) identifies one release history; an
# uninstall followed by an install starts a new history.
first_rev() { echo "$1" | cut -d';' -f1 | sed 's/:[a-z-]*@/@/'; }
if [ "$ANN" = disabled ] && [ "$D_HR1" = "$D_HR0" ] && [ "$D_HR2" = "$D_HR0" ] && [ "$D_DEP2" = "$D_DEP0" ] \
   && [ "$(first_rev "$D_HIST2")" = "$(first_rev "$D_HIST0")" ] && ! echo "$D_HIST2" | grep -qi uninstall \
   && [ "$OWNER" = ks-b ] && [ "$INV_B" = 1 ]; then
  L5=pass
else
  L5=fail
fi
if [ "$C_HR2" != "$C_HR0" ] && [ "$C_DEP2" != "$C_DEP0" ] && [ "$(first_rev "$C_HIST2")" != "$(first_rev "$C_HIST0")" ]; then
  CTL=uninstalled-and-reinstalled
else
  CTL=not-reinstalled
fi
result "L5 verdict=$L5 flux=$(flux version --client 2>/dev/null | head -1 | tr ' ' '_') annotation_on_live_object=${ANN:-none} dummy_hr_uid_kept=$([ "$D_HR2" = "$D_HR0" ] && echo yes || echo no) dummy_present_after_ks_a_prune=$([ "$D_HR1" = "$D_HR0" ] && echo yes || echo no) dummy_deployment_uid_kept=$([ "$D_DEP2" = "$D_DEP0" ] && echo yes || echo no) dummy_history_before=\"$D_HIST0\" dummy_history_after=\"$D_HIST2\" dummy_owner_after=$OWNER in_ks_b_inventory=$INV_B dummy_uninstall_events=$UNINSTALL_EVENTS"
result "L5 control (moved without annotation) outcome=$CTL ctl_after_ks_a_prune=\"$C_HR1 $C_HIST1\" ctl_history_before=\"$C_HIST0\" ctl_history_after=\"$C_HIST2\""
mkdir -p "$OUT/evidence"
kubectl -n flux-system get kustomization -o yaml > "$OUT/evidence/l5-kustomizations.yaml" 2>&1
kubectl -n l5 get hr -o yaml > "$OUT/evidence/l5-helmreleases.yaml" 2>&1
kubectl -n l5 get events --sort-by=.lastTimestamp > "$OUT/evidence/l5-events.txt" 2>&1
kubectl -n flux-system logs deploy/helm-controller --tail=400 > "$OUT/evidence/l5-helm-controller.log" 2>&1
kubectl -n flux-system logs deploy/kustomize-controller --tail=400 > "$OUT/evidence/l5-kustomize-controller.log" 2>&1
grep -iE 'uninstall|pruned|deleted' "$OUT/evidence/l5-helm-controller.log" "$OUT/evidence/l5-kustomize-controller.log" | tail -20 || true
exit 0
