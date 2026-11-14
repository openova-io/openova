#!/usr/bin/env bash
# Lab test AIP-030, version probe (spike branch only, never merged).
# Question: which k3s minor serves what Agent Substrate v1.0 needs from the
# API server: certificates.k8s.io/v1beta1 with PodCertificateRequest and
# ClusterTrustBundle, and the podCertificate / clusterTrustBundle projected
# volume sources. Measured twice: as installed (platform flags), then with the
# three feature gates and the v1beta1 runtime-config that upstream sets.
set -uo pipefail
OUT="${OUT:?OUT}"; mkdir -p "$OUT"
RES="$OUT/results.jsonl"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
GATES=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true
V="${K3S_VERSION:?K3S_VERSION}"

rec() { local v="${3:-}"; [ -n "$v" ] || v='{}'
  jq -cn --arg t "$1" --arg r "$2" --argjson v "$v" --arg d "${4:-}" '{test:$t,result:$r,values:$v,detail:$d}' >>"$RES"
  echo "RESULT $1 $2 $v ${4:-}" | cut -c1-2000; }
ready() { for _ in $(seq 1 60); do kubectl get --raw /readyz >/dev/null 2>&1 && return 0; sleep 3; done; return 1; }

cat >"$OUT/podcert-pod.yaml" <<'EOF'
apiVersion: v1
kind: Pod
metadata: {name: podcert-probe, namespace: default}
spec:
  containers: [{name: c, image: busybox:1.36, command: [sleep, "1"]}]
  volumes:
  - name: id
    projected:
      sources:
      - podCertificate: {signerName: example.com/probe, keyType: ECDSAP256, credentialBundlePath: credential-bundle.pem}
      - clusterTrustBundle: {signerName: example.com/probe, labelSelector: {matchLabels: {a: b}}, path: trust-bundle.pem}
EOF

probe() { # $1 = mode label
  local groups vmap pod kept err ok
  groups=$(kubectl get --raw /apis/certificates.k8s.io 2>/dev/null | jq -c '[.versions[].version]' 2>/dev/null || echo '[]')
  vmap='{}'
  for ver in $(jq -r '.[]' <<<"$groups"); do
    vmap=$(jq -c --arg v "$ver" --argjson r "$(kubectl get --raw "/apis/certificates.k8s.io/$ver" | jq -c '[.resources[].name | select(contains("/")|not)]')" '. + {($v): $r}' <<<"$vmap")
  done
  pod=$(kubectl create --dry-run=server --validate=strict -o json -f "$OUT/podcert-pod.yaml" 2>&1)
  kept=$(jq -r '[.spec.volumes[0].projected.sources[] | keys[0]] | join(",")' <<<"$pod" 2>/dev/null || echo "")
  err=$(grep -m1 -iE 'error|unknown' <<<"$pod" | cut -c1-300)
  ok=no
  if jq -e '(.v1beta1 // []) | (index("podcertificaterequests") != null) and (index("clustertrustbundles") != null)' <<<"$vmap" >/dev/null 2>&1 \
     && [ "$kept" = "podCertificate,clusterTrustBundle" ]; then ok=yes; fi
  rec "PROBE $V $1" info "$(jq -cn --arg k3s "$V" --arg mode "$1" --argjson versions "$groups" --argjson resources "$vmap" \
      --arg kept "$kept" --arg sufficient "$ok" '{k3s:$k3s,mode:$mode,certVersions:$versions,certResources:$resources,podVolumeSourcesKept:$kept,substrateApiSatisfied:$sufficient}')" "$err"
}

echo "=== k3s $V with the platform's server flags"
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$V" sh -s - server \
  --cluster-init --flannel-backend=none --disable-network-policy --disable-kube-proxy \
  --disable=traefik --disable=servicelb --write-kubeconfig-mode=0644 >"$OUT/k3s-install.log" 2>&1
if ! ready; then rec "PROBE $V install" fail '{}' "k3s API not ready"; exit 0; fi
kubectl version -o json | jq -c '.serverVersion | {gitVersion}' | tee "$OUT/server-version.json"
probe default

echo "=== restart with $GATES and runtime-config certificates.k8s.io/v1beta1=true"
sudo tee /etc/rancher/k3s/config.yaml >/dev/null <<EOF
kube-apiserver-arg:
  - feature-gates=$GATES
  - runtime-config=certificates.k8s.io/v1beta1=true
kube-controller-manager-arg:
  - feature-gates=$GATES
kubelet-arg:
  - feature-gates=$GATES
EOF
sudo systemctl restart k3s; sleep 5
if ready; then
  probe gated
else
  why=$(sudo journalctl -u k3s --no-pager -n 400 2>/dev/null | grep -iE 'feature gate|unrecognized|runtime-config|invalid|fatal' | tail -n 3 | cut -c1-300 | tr '\n' ' ')
  rec "PROBE $V gated" info "$(jq -cn --arg k3s "$V" '{k3s:$k3s,mode:"gated",apiServerStarted:false,substrateApiSatisfied:"no"}')" "$why"
fi
