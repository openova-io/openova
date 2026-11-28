# shellcheck shell=bash
# chart-file-deps.sh: the folders a chart's package holds, and so the charts a change releases.
# Source it, then call chart_file_deps, chart_packaged_dirs or release_set.
#
# `helm dependency build` packs the folder of every `file://` dependency into the chart that
# names it. A library chart (`type: library`: platform/kube-access-lib, platform/secrets-lib,
# platform/topology-lib) is never published by itself; every chart that names it carries the
# copy it was built with, so a change of the library is a change of each of those charts. On 27
# and 28 Nov 2026 SEC-219 and SEC-226 changed platform/secrets-lib/chart, no release followed
# for products/catalyst and products/axon, and the cut of a candidate refused main twice.
#
# A dependency is read the way scripts/check-chart-content-after-version.py (the check the cut
# of scripts/release-candidate.sh runs) reads it: each line `repository: file://<rel>` of
# Chart.yaml (its FILE_DEP), <rel> joined to the chart folder without its . and .. parts, kept
# when it lies outside the chart folder; a path that leaves the repository is an error.
#
# Callers: the steps "Detect changed Blueprints", "Release writer: bump the chart version" and
# "Commit + push bootstrap-kit pin bump + blueprint.yaml lockstep" of
# .github/workflows/blueprint-release.yaml. Held by tools/guards/cmd/release-set.

# chart_dep_path <path>: <path> without its "." and ".." parts. Exit 1 when it is absolute or
# climbs out of the repository.
chart_dep_path() {
  local p cfd_segs cfd_kept=()
  case "$1" in /*) return 1 ;; esac
  IFS=/ read -r -a cfd_segs <<< "$1"
  for p in "${cfd_segs[@]}"; do
    case "$p" in
      "" | .) ;;
      ..) [ "${#cfd_kept[@]}" -gt 0 ] || return 1; unset 'cfd_kept[${#cfd_kept[@]}-1]' ;;
      *) cfd_kept+=("$p") ;;
    esac
  done
  [ "${#cfd_kept[@]}" -gt 0 ] || { echo .; return 0; }
  (IFS=/; printf '%s\n' "${cfd_kept[*]}")
}

# chart_file_deps <rev> <chart folder>: each folder a file:// dependency of the chart's
# Chart.yaml names that lies outside the chart folder, once per line. <rev> empty: the working
# tree. A chart without a Chart.yaml at <rev> has none. Exit 1, named on stderr, when a
# dependency leaves the repository.
chart_file_deps() {
  local text rel dep seen=" "
  if [ -n "$1" ]; then
    text="$(git show "$1:$2/Chart.yaml" 2>/dev/null)" || return 0
  elif [ -f "$2/Chart.yaml" ]; then
    text="$(cat "$2/Chart.yaml")"
  else
    return 0
  fi
  while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    if ! dep="$(chart_dep_path "$2/$rel")"; then
      echo "chart-file-deps: $2/Chart.yaml: the dependency file://$rel lies outside the repository" >&2
      return 1
    fi
    case "$dep" in "$2" | "$2"/*) continue ;; esac
    case "$seen" in *" $dep "*) continue ;; esac
    seen="$seen$dep "
    printf '%s\n' "$dep"
  done < <(printf '%s\n' "$text" | sed -n -E "s|^[[:blank:]-]*repository:[[:blank:]]*[\"']?file://([^\"'[:space:]#]+).*\$|\1|p")
}

# chart_packaged_dirs <rev> <chart folder>: the chart folder, then each folder its package holds
# through file:// dependencies, directly or through a dependency's own, once per line.
chart_packaged_dirs() {
  local cfd_queue=("$2") seen=" $2 " d dep deps
  printf '%s\n' "$2"
  while [ "${#cfd_queue[@]}" -gt 0 ]; do
    d="${cfd_queue[0]}"
    cfd_queue=("${cfd_queue[@]:1}")
    deps="$(chart_file_deps "$1" "$d")" || return 1
    while IFS= read -r dep; do
      [ -n "$dep" ] || continue
      case "$seen" in *" $dep "*) continue ;; esac
      seen="$seen$dep "
      cfd_queue+=("$dep")
      printf '%s\n' "$dep"
    done <<< "$deps"
  done
}

# release_set: the Blueprint folders a push releases, sorted, once per line, from the paths it
# changed (stdin, one per line, as git diff --name-only prints them) and the Chart.yaml files of
# the working tree:
#   - <tree>/<name> of each path under platform/<name>/chart/ or products/<name>/chart/, and of
#     platform/<name>/blueprint.yaml and products/<name>/blueprint.yaml;
#   - each platform/<name> and products/<name> whose chart's package holds a changed path
#     through a file:// dependency (chart_packaged_dirs), each named on stderr with the folder.
release_set() {
  local changed cy dir dirs d f cfd_set
  changed="$(cat)"
  cfd_set="$(printf '%s\n' "$changed" | grep -E '^(platform|products)/[^/]+/(chart/|blueprint\.yaml)' | awk -F/ '{print $1"/"$2}' || true)"
  for cy in platform/*/chart/Chart.yaml products/*/chart/Chart.yaml; do
    [ -f "$cy" ] || continue
    dir="${cy%/Chart.yaml}"
    dirs="$(chart_packaged_dirs "" "$dir")" || return 1
    while IFS= read -r d; do
      if [ -z "$d" ] || [ "$d" = "$dir" ]; then
        continue
      fi
      while IFS= read -r f; do
        case "$f" in
          "$d"/*)
            echo "release-set: ${dir%/chart}: its package holds ${d}, which ${f} changed" >&2
            cfd_set="${cfd_set}"$'\n'"${dir%/chart}"
            break 2
            ;;
        esac
      done <<< "$changed"
    done <<< "$dirs"
  done
  printf '%s\n' "$cfd_set" | sed '/^$/d' | sort -u
}
