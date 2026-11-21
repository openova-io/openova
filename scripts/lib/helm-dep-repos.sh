# shellcheck shell=bash
# helm-dep-repos.sh: the one place that tells Helm the chart repositories a
# chart's dependencies name. Source it, then call add_dep_repos <chart dir>.
#
# `helm dependency build` takes one of two paths (Helm v3.18.4). Without a
# Chart.lock beside Chart.yaml it resolves like `helm dependency update` and
# fetches from a repository URL nobody added. With a Chart.lock it refuses every
# http(s) repository that `helm repo add` did not register:
#   Error: no repository definition for <url>. Please add the missing repos via 'helm repo add'
# No Chart.lock is tracked (.gitignore), but a dependency build leaves one, so
# the next build in the same tree takes the second path. After add_dep_repos a
# dependency build passes on either path. file:// and oci:// dependencies need
# no repository.
#
# Callers: scripts/check-freshprov-image-registry.sh (render_chart) and the
# build job of .github/workflows/blueprint-release.yaml (steps "Release writer:
# bump the chart version" and "Helm dependency build"). Held by
# tools/guards/cmd/release-own-tree.

add_dep_repos() { # <dir> — `helm repo add` every http(s) repository its Chart.yaml dependencies name
  local url
  for url in $(awk '/^dependencies:/ {d = 1} d && /repository:/ {gsub(/["'\'' ]/, ""); sub(/^-?repository:/, ""); print}' "$1/Chart.yaml" | grep -E '^https?://' | sort -u); do
    helm repo add --force-update "dep-$(printf '%s' "$url" | sha256sum | cut -c1-12)" "$url" >/dev/null 2>&1 || true
  done
}
