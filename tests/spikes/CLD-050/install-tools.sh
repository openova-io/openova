#!/usr/bin/env bash
# Spike CLD-050 — install the pinned command-line tools into $HOME/bin.
# Runs only inside the GitHub Actions job (never on a workstation).
set -euo pipefail

KIND_VERSION=v0.31.0
KUBECTL_VERSION=v1.33.7
HELM_VERSION=v3.22.1
TOFU_VERSION=1.10.8   # the OpenTofu version baked into provider-opentofu v1.1.11
CRANK_VERSION=v1.20.14 # Crossplane CLI used for the Composition conversion (E7)

BIN="$HOME/bin"
mkdir -p "$BIN"
cd "$(mktemp -d)"

curl -fsSLo "$BIN/kind" "https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-linux-amd64"
curl -fsSLo "$BIN/kubectl" "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
curl -fsSLo helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"
tar -xzf helm.tgz && mv linux-amd64/helm "$BIN/helm"
curl -fsSLo tofu.zip "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.zip"
unzip -q -o tofu.zip tofu && mv tofu "$BIN/tofu"
curl -fsSLo "$BIN/crank" "https://releases.crossplane.io/stable/${CRANK_VERSION}/bin/linux_amd64/crank" || echo "crank download failed (E7 falls back to a scripted conversion)"
if ! command -v yq >/dev/null; then
  curl -fsSLo "$BIN/yq" https://github.com/mikefarah/yq/releases/download/v4.44.3/yq_linux_amd64
fi
chmod +x "$BIN"/*
echo "$BIN" >> "$GITHUB_PATH"

export PATH="$BIN:$PATH"
command -v aws >/dev/null || pip3 install --quiet --break-system-packages awscli
echo "tool versions:"
kind version
kubectl version --client
helm version --short
tofu version
[ -s "$BIN/crank" ] && "$BIN/crank" version --client 2>/dev/null || true
docker version --format 'docker {{.Server.Version}}'
aws --version
yq --version
python3 -c 'import yaml' 2>/dev/null || pip3 install --quiet --break-system-packages pyyaml
python3 -c 'import yaml; print("pyyaml", yaml.__version__)'
