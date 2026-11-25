#!/bin/bash
# run.sh <k3s release>:<case> ...: the runner of lab test SOV-722. Each case of
# scripts/check-registry-drain.sh (or of the file named in HARNESS) runs on the given k3s release,
# one after another, on one Docker host. The release is set as the default of k3s_version in
# infra/providers/local/variables.tf of this checkout (restored at the end), and the release's
# published k3s-images.txt is added when it is missing. One log per run under logs/ and one line
# per run in runs.tsv: end time, release, case, exit, seconds, k3s image digest, verdict, harness.
# Containers only: no cluster and no kubeconfig. Put yq v4.44.3 first in PATH. bin/docker runs
# docker through sudo, for a host whose Docker socket needs root.
export KUBECONFIG=/dev/null
set -u
here="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$here/../../.." && pwd)"
OUT="${OUT:-$here}"
export PATH="$here/bin:$PATH" WORK="${WORK:-$OUT/work}" REGISTRY_DRAIN_ANY_HOST=1 NO_COLOR=1
mkdir -p "$OUT/logs" "$WORK"
cd "$ROOT" || exit 2
L=infra/providers/_shared/k3s-images
for pair in "$@"; do
  v="${pair%%:*}" c="${pair#*:}"
  log="$OUT/logs/$v-$c-$(date -u +%Y%m%dT%H%M%SZ).log"
  if [ ! -s "$L/$v.txt" ]; then
    curl -fsSL --max-time 60 -o "$L/$v.txt" "https://github.com/k3s-io/k3s/releases/download/${v/+/%2B}/k3s-images.txt" \
      || { printf '%s\t%s\tno-image-list\n' "$v" "$c" >> "$OUT/runs.tsv"; continue; }
  fi
  sed -i -E '/^variable "k3s_version"/,/^}/ s/^( *default *= *)"[^"]*"/\1"'"$v"'"/' infra/providers/local/variables.tf
  {
    echo "== $(date -u +%Y-%m-%dT%H:%M:%SZ) harness $(git rev-parse --short HEAD) case $c k3s $v"
    echo "== image list sha256 $(sha256sum "$L/$v.txt" | cut -c1-64)"
    echo "== $(yq --version); docker $(docker version -f '{{.Server.Version}}'); kernel $(uname -r)"
    echo "== harness file ${HARNESS:-scripts/check-registry-drain.sh}"
  } > "$log"
  t=$(date +%s)
  bash "${HARNESS:-scripts/check-registry-drain.sh}" "$c" >> "$log" 2>&1
  rc=$?
  s=$(($(date +%s) - t))
  d="$(docker image inspect -f '{{index .RepoDigests 0}}' "docker.io/rancher/k3s:${v/+/-}" 2>/dev/null)"
  echo "== exit $rc after $s s; k3s image $d" >> "$log"
  verdict="$(grep -E '^(PASS|FAIL) ' "$log" | tail -n 1 | cut -c1-400)"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$v" "$c" "$rc" "$s" "$d" "$verdict" \
    "$(basename "${HARNESS:-scripts/check-registry-drain.sh}")" >> "$OUT/runs.tsv"
done
git checkout -- infra/providers/local/variables.tf
