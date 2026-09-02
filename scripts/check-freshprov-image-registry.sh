#!/usr/bin/env bash
# check-freshprov-image-registry.sh — CI guard that a fresh Sovereign's
# Phase-1 image pulls never depend on a registry the node has no working
# Harbor pull-through for, forever (#5281).
#
# ─── THE FAILURE CLASS THIS GUARDS AGAINST ────────────────────────────────
#
# A fresh Sovereign converges in "Phase-1" by having Flux install the
# bootstrap-kit charts (clusters/_template/bootstrap-kit/*.yaml). BEFORE the
# sovereignty cutover rewrites node containerd `certs.d`, every image the
# kubelet pulls is resolved through the node registry MIRROR that cloud-init
# bakes into `/etc/rancher/k3s/registries.yaml` (built in
# infra/providers/{hetzner,huawei}/main.tf). That mirror transparently
# rewrites a fixed set of upstream hosts to the mothership Harbor
# pull-through proxy:
#
#     docker.io / registry-1.docker.io / index.docker.io  -> proxy-dockerhub
#     quay.io                                              -> proxy-quay
#     gcr.io / k8s.gcr.io                                  -> proxy-gcr
#     registry.k8s.io                                      -> proxy-k8s
#     ghcr.io                                              -> proxy-ghcr
#
# So a BARE `docker.io/velero/velero` ref is NOT a direct external pull — the
# node mirror serves it from `harbor.openova.io/proxy-dockerhub/...`. That is
# the intended, repo-wide pattern (many charts pin bare upstream refs and rely
# on the mirror). Flagging bare docker.io/quay.io/… would be WRONG.
#
# The genuine hw279 hostage class is an image whose host is EITHER:
#   (a) NOT in the node mirror at all — e.g. `mirror.gcr.io` (bp-trivy),
#       `cgr.dev`, `nvcr.io`, `mcr.microsoft.com`, `docker.elastic.co`; OR
#   (b) in the mirror but mapped to a Harbor proxy project that does NOT
#       EXIST on the mothership — `xpkg.upbound.io`->proxy-xpkg and
#       `public.ecr.aws`->proxy-ecr (the live mothership carries only
#       proxy-dockerhub/gcr/ghcr/k8s/quay — #5281). The rewrite 404s and
#       containerd falls back to the upstream, which is then hostage to that
#       registry's uptime.
#
# Live proof (hw279 dep 059126bb, 2026-07-20): crossplane's CORE image
# inherited the upstream subchart default `xpkg.upbound.io/crossplane/
# crossplane:v1.18.0`. The node mirror rewrites xpkg.upbound.io -> proxy-xpkg,
# which does NOT exist on the mothership, so the pull fell back to
# xpkg.upbound.io direct and upbound 503'd — wedging crossplane-system at
# Init:ErrImagePull and stalling the whole dependency chain. PR #5282 rerouted
# the core image to `harbor.openova.io/proxy-dockerhub/crossplane/crossplane`
# (docker.io publishes the identical release; proxy-dockerhub EXISTS). This
# gate makes the whole class un-reintroducible.
#
# ─── WHY A NEW GATE (vs. the two adjacent guards) ─────────────────────────
#   * scripts/check-kyverno-proxy-images.sh enforces the Kyverno
#     `harbor-proxy-pull` ADMISSION policy (a different concern: pod admission,
#     not pull-uptime — crossplane-system was already admission-exempt yet
#     still 503'd on the pull).
#   * platform/self-sovereign-cutover/chart/tests/image-registry-coverage.sh
#     checks the CUTOVER offline-mirror coverage map (a different window: it
#     ensures step-03 mirrors every host at cutover; e.g. it maps
#     mirror.gcr.io -> proxy-gcr, which is valid POST-cutover because step-03
#     skopeo-PUSHES the real image, but is INERT during Phase-1 where the
#     mothership proxy-gcr is a gcr.io pull-through that has no aquasec/* image).
#   Neither guards the Phase-1 node-mirror pull window this gate owns. (Both
#   are also, today, not wired into CI on the paths that matter.)
#
# ─── TWO PHASES (same shape as check-no-nodeports.sh / check-no-local-path.sh) ─
#   Phase 1 (authoritative, always enforced, NETWORK-FREE) — a static scan of
#     every bootstrap-kit chart's OWN sources (values.yaml + templates/**,
#     excluding vendored charts/) for an image/registry/repository ref pinned
#     to a host the node mirror has no working proxy for.
#   Phase 2 (best-effort, RENDER) — `helm dependency build` + `helm template`
#     every SUBCHART-WRAPPING bootstrap-kit chart and check every rendered
#     `image:` ref the same way. This is the ONLY phase that sees a
#     subchart-DEFAULT leak (the exact crossplane class). It is hook-aware:
#     images that appear ONLY in helm delete/rollback hook resources are NOT
#     Phase-1-install pulls, so they are excluded. A chart whose deps will not
#     build / will not render is WARN-skipped (never fails the gate on an
#     upstream-registry/tooling flake); Phase 1 still covers its own sources.
#
# Usage:
#   scripts/check-freshprov-image-registry.sh              # Phase 1 + best-effort Phase 2
#   RENDER_SWEEP=0 scripts/check-freshprov-image-registry.sh  # Phase 1 only
#   scripts/check-freshprov-image-registry.sh --self-test  # negative-control unit test
#   scripts/check-freshprov-image-registry.sh --list-images  # emit the rendered
#       Phase-1 install-image set (one ref per line, sorted, on stdout; every
#       resolved bootstrap-kit chart is rendered, flat charts included) — the
#       single renderer scripts/check-mothership-proxy-image-path.sh (prov
#       pre-flight check 7, #6778) builds its probe list from. Diagnostics go
#       to stderr; exit 2 when the render yields zero refs.
# Exit: 0 = clean, 1 = a hostage image ref reappeared, 2 = usage/setup error.

set -euo pipefail

# ─── Registry classification ──────────────────────────────────────────────
#
# SAFE hosts: the node registries.yaml mirror set INTERSECTED with the Harbor
# proxy projects that actually EXIST on the mothership (proxy-dockerhub / gcr /
# ghcr / k8s / quay — #5281). To ADD a host here it must be BOTH (a) mirrored
# in infra/providers/{hetzner,huawei}/main.tf registries.yaml AND (b) backed by
# a live mothership proxy-<x> project. `xpkg.upbound.io` + `public.ecr.aws` are
# in the mirror but their proxy-xpkg / proxy-ecr projects do NOT exist, so they
# are deliberately NOT safe.
SAFE_HOSTS_RE='^(docker\.io|registry-1\.docker\.io|index\.docker\.io|quay\.io|gcr\.io|k8s\.gcr\.io|registry\.k8s\.io|ghcr\.io|harbor\.openova\.io)$'

# An explicit Harbor-proxy route (any Sovereign FQDN) or a native openova-io
# ref is always safe regardless of the leading host token. The `/proxy-<x>`
# segment may be the END of the string (a split `registry:` field value like
# `harbor.openova.io/proxy-dockerhub`, whose `repository:` sibling carries the
# path) or mid-path (a full `image:` ref like `.../proxy-dockerhub/foo:tag`).
PROXY_SAFE_RE='(/proxy-[a-z0-9-]+(/|$))|(/openova-io/)'

# ─── Documented allowlist — genuine, deliberate non-mirror Phase-1 refs ───
#
#  * xpkg.upbound.io/{crossplane-contrib,upbound}/provider-*  — Crossplane
#    PROVIDER PACKAGES. `Provider.pkg.crossplane.io` CRs with a `spec.package:`
#    field fetched by crossplane's in-process xpkg fetcher (NOT a kubelet
#    `image:` pull — a containerd mirror is inert for it), rewritten to the
#    local Harbor by cutover step-11 (crossplaneProviderPivot). Known + tracked
#    (#4541/#5204, #5281 body, self-sovereign-cutover offlineMirror.excludedHosts).
#    They are `spec.package:` not `image:`, so the scans below never see them;
#    the entry documents the boundary.
ALLOWLIST_RE='(xpkg\.upbound\.io/(crossplane-contrib|upbound)/provider-)'

# host_of <ref> — echo the registry host of an image ref, or empty for a
# registry-less short name (which containerd resolves to docker.io = safe).
host_of() {
  local ref="$1" first
  # No path separator ⇒ a bare short name `name[:tag]` (e.g. `busybox:latest`,
  # `ubuntu-24.04`) — no registry host (the `:` is a TAG, not a port).
  case "$ref" in
    */*) first="${ref%%/*}" ;;
    *)   printf ''; return ;;
  esac
  # With a path, the first segment is a host only if it carries a dot (DNS) or
  # a colon (host:port); otherwise it is a docker-library org (`aquasec/trivy`).
  case "$first" in
    *.* | *:* ) printf '%s' "$first" ;;
    * ) printf '' ;;
  esac
}

# ref_is_hostage <ref-or-host> [is_registry_field]
#   0 = Phase-1 hostage (no working node-mirror proxy), 1 = safe.
#   When is_registry_field=1 the value IS a bare host (split `registry:` shape).
ref_is_hostage() {
  local ref="$1" is_reg="${2:-0}" host
  # Helm/envsubst placeholder → not a real ref.
  case "$ref" in
    *'{{'* | *'${'* ) return 1 ;;
  esac
  # Explicit Harbor-proxy route / native openova-io ref → safe.
  if printf '%s' "$ref" | grep -Eq "$PROXY_SAFE_RE"; then return 1; fi
  # Documented allowlist → safe.
  if printf '%s' "$ref" | grep -Eq "$ALLOWLIST_RE"; then return 1; fi
  if [ "$is_reg" = "1" ]; then
    host="$ref"
  else
    host="$(host_of "$ref")"
  fi
  # Registry-less short name → containerd resolves to docker.io → safe.
  [ -z "$host" ] && return 1
  # Host the node mirror serves via an existing proxy project → safe.
  if printf '%s' "$host" | grep -Eq "$SAFE_HOSTS_RE"; then return 1; fi
  # Anything else has no working Phase-1 proxy → HOSTAGE.
  return 0
}

# ─── Negative control (self-test) ─────────────────────────────────────────
# Proves the classifier CATCHES a host with no working node-mirror proxy and
# does NOT flag a mirror-served host / proxy route / allowlisted ref. Wired
# into CI so the gate can never silently rot into a no-op.
if [ "${1:-}" = "--self-test" ]; then
  st_fail=0
  must_flag() { if ref_is_hostage "$1" "${2:-0}"; then echo "  ok   (flagged) $1"; else echo "  FAIL (missed)  $1" >&2; st_fail=1; fi; }
  must_pass() { if ref_is_hostage "$1" "${2:-0}"; then echo "  FAIL (flagged) $1" >&2; st_fail=1; else echo "  ok   (passed)  $1"; fi; }
  echo "== self-test: hosts with NO working Phase-1 proxy MUST be flagged =="
  must_flag "xpkg.upbound.io/crossplane/crossplane:v1.18.0"   # proxy-xpkg missing (the hw279 image)
  must_flag "mirror.gcr.io/aquasec/trivy-operator:0.30.1"     # not in the node mirror (bp-trivy)
  must_flag "cgr.dev/chainguard/kubectl:latest-dev"           # not in the node mirror
  must_flag "public.ecr.aws/foo/bar:1"                        # proxy-ecr missing
  must_flag "nvcr.io/nvidia/thing:1"                          # not in the node mirror
  must_flag "mcr.microsoft.com/foo/bar:1"                     # not in the node mirror
  must_flag "mirror.gcr.io" 1                                 # split `registry:` field shape
  echo "== self-test: mirror-served hosts / proxy / native / short names must NOT be flagged =="
  must_pass "docker.io/velero/velero:v1.18.0"                 # -> proxy-dockerhub (exists)
  must_pass "quay.io/cilium/cilium:v1.16.0"                   # -> proxy-quay
  must_pass "ghcr.io/anchore/syft:v1.43.0"                    # -> proxy-ghcr
  must_pass "registry.k8s.io/kubectl:v1.31.0"                 # -> proxy-k8s
  must_pass "gcr.io/foo/bar:1"                                # -> proxy-gcr
  must_pass "harbor.openova.io/proxy-dockerhub/crossplane/crossplane:v1.18.0"
  must_pass "registry.hw279.omani.works/proxy-quay/jetstack/cert-manager-controller:v1.16.5"
  must_pass "ghcr.io/openova-io/openova/catalyst-api:1.2.3"
  must_pass "xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0"  # allowlisted (spec.package, cutover step-11)
  must_pass "docker.io" 1                                     # split `registry:` field, mirror-served
  must_pass "harbor.openova.io/proxy-dockerhub" 1             # split `registry:` field, explicit proxy prefix (bp-trivy shape)
  must_pass "busybox:latest"                                  # short name -> docker.io
  must_pass "aquasec/trivy"                                   # docker-library org, no host
  must_pass "ubuntu-24.04"                                    # Hetzner server-image slug, not a container image
  must_pass '{{ .Values.image.repository }}:{{ .Values.image.tag }}'
  echo ""
  if [ "$st_fail" -ne 0 ]; then echo "SELF-TEST FAILED — the registry classifier is broken." >&2; exit 1; fi
  echo "SELF-TEST PASSED — flags hostage hosts, passes mirror-served/proxy/native/short refs."
  exit 0
fi

ROOT="${ROOT:-.}"
cd "${ROOT}"

# --list-images: render-and-emit mode (see usage). Everything diagnostic goes
# to stderr so stdout is exactly the image set.
LIST_MODE=0
[ "${1:-}" = "--list-images" ] && LIST_MODE=1

EXIT=0
fail() { echo "FAIL: $1" >&2; EXIT=1; }

# render_chart <dir> — `helm dependency build` (only when Chart.yaml declares
# dependencies AND nothing is vendored under charts/ — neither a *.tgz nor an
# unpacked subchart dir such as bp-seaweedfs's) + `helm template` at default
# values. Rendered manifests on stdout; empty output = the chart did not render.
# Reports ok | depfail | empty through RENDER_STATUS_FILE so callers can say WHY
# a chart contributed nothing — "renders empty at default values" (a master
# gate, the common case) is not the same finding as "its subcharts would not
# build". The status travels through a FILE, not a variable: render_chart is
# called inside a command substitution, and that subshell discards any
# assignment — which left the caller's WARN unable to fire at all.
RENDER_STATUS_FILE="${RENDER_STATUS_FILE:-$(mktemp)}"
render_status() { cat "$RENDER_STATUS_FILE" 2>/dev/null; }
render_chart() {
  local dir="$1" out
  printf 'ok' > "$RENDER_STATUS_FILE"
  if grep -q '^dependencies:' "$dir/Chart.yaml" 2>/dev/null \
     && ! ls "$dir"/charts/*.tgz >/dev/null 2>&1 && ! ls "$dir"/charts/*/Chart.yaml >/dev/null 2>&1; then
    if ! ( cd "$dir" && timeout "${DEP_TIMEOUT:-120s}" helm dependency build >/dev/null 2>&1 ); then
      printf 'depfail' > "$RENDER_STATUS_FILE"; return 0
    fi
  fi
  out="$( (cd "$dir" && timeout "${RENDER_TIMEOUT:-45s}" helm template guard . 2>/dev/null) || true )"
  [ -n "$out" ] || printf 'empty' > "$RENDER_STATUS_FILE"
  printf '%s' "$out"
}

# extract_install_images — stdin: rendered manifests; stdout: every INSTALL
# image ref, sorted unique. Hook-aware: images that appear ONLY in helm
# delete/rollback hook resources never run during a fresh-prov install, so they
# are dropped (e.g. sigstore policy-controller's cgr.dev leases-cleanup
# post-delete hook). `imageName:` is the CNPG Cluster CR's image field — the
# exact key the shared-pg/per-app postgres pulls come from (#6778), so it is
# read alongside `image:`.
extract_install_images() {
  awk '
    BEGIN{ img="" }
    /^---/{
      if (doc!="" && !(doc ~ /helm\.sh\/hook"?:[[:space:]]*"?(pre-delete|post-delete|pre-rollback|post-rollback)/)) print docimgs;
      doc=""; docimgs=""
    }
    { doc=doc "\n" $0 }
    /^[[:space:]]*-?[[:space:]]*(image|imageName):[[:space:]]*/{
      line=$0; sub(/^[[:space:]]*-?[[:space:]]*(image|imageName):[[:space:]]*"?/,"",line); sub(/["[:space:]].*$/,"",line);
      if (line!="") docimgs=docimgs line "\n"
    }
    END{
      if (doc!="" && !(doc ~ /helm\.sh\/hook"?:[[:space:]]*"?(pre-delete|post-delete|pre-rollback|post-rollback)/)) print docimgs
    }
  ' | sed '/^$/d' | sort -u
}

# ─── Enumerate the fresh-prov bootstrap-kit chart set (dynamic) ───────────
BK_DIR="clusters/_template/bootstrap-kit"
if [ ! -d "$BK_DIR" ]; then echo "FATAL: $BK_DIR not found — run from repo root." >&2; exit 2; fi

mapfile -t CHART_NAMES < <(
  grep -rhoE '^[[:space:]]+chart:[[:space:]]+bp-[a-z0-9-]+' "$BK_DIR"/*.yaml \
    | sed -E 's/^[[:space:]]+chart:[[:space:]]+//' | sort -u
)
if [ "${#CHART_NAMES[@]}" -eq 0 ]; then echo "FATAL: no 'chart: bp-*' refs under $BK_DIR" >&2; exit 2; fi

declare -A CHART_DIR=()
UNRESOLVED=()
for name in "${CHART_NAMES[@]}"; do
  dir="$(grep -rl --include=Chart.yaml -E "^name: ${name}\$" platform products 2>/dev/null | head -1 | xargs -r dirname)"
  if [ -n "$dir" ]; then CHART_DIR["$name"]="$dir"; else UNRESOLVED+=("$name"); fi
done
[ "${#UNRESOLVED[@]}" -gt 0 ] && echo "WARN: could not resolve a chart dir for: ${UNRESOLVED[*]}" >&2
echo "Fresh-prov bootstrap-kit chart set: ${#CHART_DIR[@]} charts resolved." >&2

# ─── --list-images — render EVERY resolved chart and emit the image set ────
if [ "$LIST_MODE" = "1" ]; then
  if ! command -v helm >/dev/null 2>&1; then echo "FATAL: helm not on PATH — cannot render the image set." >&2; exit 2; fi
  LIST_EMPTY=(); LIST_DEPFAIL=(); LIST_RENDERED=0
  LIST_TMP="$(mktemp)"; trap 'rm -f "$LIST_TMP" "$RENDER_STATUS_FILE"' EXIT
  for name in "${!CHART_DIR[@]}"; do
    dir="${CHART_DIR[$name]}"
    rendered="$(render_chart "$dir")"
    case "$(render_status)" in
      depfail) LIST_DEPFAIL+=("$name"); continue ;;
      empty)   LIST_EMPTY+=("$name"); continue ;;
    esac
    LIST_RENDERED=$((LIST_RENDERED+1))
    printf '%s\n' "$rendered" | extract_install_images >> "$LIST_TMP"
  done
  echo "Rendered ${LIST_RENDERED}/${#CHART_DIR[@]} bootstrap-kit chart(s) for the image set." >&2
  [ "${#LIST_EMPTY[@]}" -gt 0 ] && echo "NOTE: ${#LIST_EMPTY[@]} chart(s) render EMPTY at default values (a master gate — no images to contribute): ${LIST_EMPTY[*]}" >&2
  [ "${#LIST_DEPFAIL[@]}" -gt 0 ] && echo "WARN: ${#LIST_DEPFAIL[@]} chart(s) could not build subchart deps — their images are NOT in the set: ${LIST_DEPFAIL[*]}" >&2
  sort -u "$LIST_TMP"
  n="$(sort -u "$LIST_TMP" | sed '/^$/d' | wc -l)"
  echo "Emitted ${n} unique install image ref(s)." >&2
  [ "$n" -gt 0 ] || { echo "FATAL: zero image refs rendered — a list that is empty is not a clean list." >&2; exit 2; }
  exit 0
fi

# ─── Phase 1 — static source scan (authoritative, network-free) ───────────
echo ""
echo "== Phase 1: static bootstrap-kit image-registry source scan =="
for name in "${!CHART_DIR[@]}"; do
  dir="${CHART_DIR[$name]}"
  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    file="${hit%%:*}"; rest="${hit#*:}"; lineno="${rest%%:*}"; content="${rest#*:}"
    key="$(printf '%s' "$content" | sed -E 's/^[[:space:]]*-?[[:space:]]*([a-zA-Z]+):.*/\1/')"
    ref="$(printf '%s' "$content" | sed -E 's/^[[:space:]]*-?[[:space:]]*(image|repository|registry):[[:space:]]*"?//; s/["'\''[:space:]].*$//')"
    [ -n "$ref" ] || continue
    is_reg=0; [ "$key" = "registry" ] && is_reg=1
    if ref_is_hostage "$ref" "$is_reg"; then
      fail "${file}:${lineno} pins image host '${ref}' (chart ${name}) — the fresh-prov node registry mirror has no working Harbor proxy for it, so Phase-1 pulls it DIRECT (hostage to that registry's uptime, the hw279 class #5281). Route it through an EXISTING proxy (e.g. harbor.openova.io/proxy-dockerhub/... , same pattern as PR #5282) or add a documented allowlist entry in scripts/check-freshprov-image-registry.sh."
    fi
  done < <(
    grep -rnE '^[[:space:]]*-?[[:space:]]*(image|repository|registry):[[:space:]]*"?[a-z0-9.-]+\.(io|com|aws|dev|net|org)/?' \
      "$dir" --include='*.yaml' --include='*.yml' --include='*.tpl' 2>/dev/null \
      | grep -vE '/charts/[^/]+/' | grep -vE '/tests/'
  )
done
[ "$EXIT" -eq 0 ] && echo "OK — every bootstrap-kit chart's own image pins resolve to a mirror-served proxy."

# ─── Phase 2 — rendered subchart-wrapping charts (best-effort, hook-aware) ─
echo ""
echo "== Phase 2: rendered subchart-wrapping bootstrap-kit chart scan =="
if [ "${RENDER_SWEEP:-1}" = "0" ]; then
  echo "SKIP — RENDER_SWEEP=0 (Phase 1 already covered chart sources)."
elif ! command -v helm >/dev/null 2>&1; then
  echo "WARN: helm not on PATH — skipping the render phase (Phase 1 covers sources)."
else
  SKIPPED=(); RENDERED=0
  for name in "${!CHART_DIR[@]}"; do
    dir="${CHART_DIR[$name]}"
    grep -q '^dependencies:' "$dir/Chart.yaml" 2>/dev/null || continue
    rendered="$(render_chart "$dir")"
    if [ -z "$rendered" ]; then SKIPPED+=("$name ($(render_status))"); continue; fi
    RENDERED=$((RENDERED+1))
    # Hook-aware extraction (delete/rollback hook images dropped) — shared
    # with --list-images via extract_install_images.
    while IFS= read -r ref; do
      [ -n "$ref" ] || continue
      if ref_is_hostage "$ref"; then
        fail "chart ${name} (${dir}) RENDERS install image '${ref}' on a host the fresh-prov node mirror has no working Harbor proxy for — Phase-1 pulls it DIRECT (the hw279 crossplane class, #5281). Route it through an EXISTING proxy (e.g. harbor.openova.io/proxy-dockerhub/... ) or add a documented allowlist entry."
      fi
    done < <(printf '%s\n' "$rendered" | extract_install_images)
  done
  echo "Rendered ${RENDERED} subchart-wrapping bootstrap chart(s)."
  [ "${#SKIPPED[@]}" -gt 0 ] && echo "WARN: render-skipped (Phase 1 still covers their own sources): ${SKIPPED[*]}" >&2
  [ "$EXIT" -eq 0 ] && echo "OK — every rendered install image resolves to a mirror-served proxy."
fi

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "A fresh Sovereign's Phase-1 images must resolve to a mothership" >&2
  echo "Harbor proxy the node registry mirror actually serves (docker.io/" >&2
  echo "quay.io/gcr.io/registry.k8s.io/ghcr.io -> proxy-dockerhub/quay/gcr/" >&2
  echo "k8s/ghcr), or carry an explicit harbor.openova.io/proxy-*/ route." >&2
  echo "A host with NO working node-mirror proxy (mirror.gcr.io, cgr.dev," >&2
  echo "xpkg.upbound.io [proxy-xpkg missing], public.ecr.aws [proxy-ecr" >&2
  echo "missing], nvcr.io, …) makes fresh-prov convergence hostage to that" >&2
  echo "registry's uptime — the hw279 xpkg.upbound.io 503 that wedged" >&2
  echo "crossplane-system (#5281). Route it through an EXISTING proxy (same" >&2
  echo "pattern as PR #5282: pick a registry that publishes the identical" >&2
  echo "image AND has a live mothership proxy-<x> project), or add a" >&2
  echo "DOCUMENTED allowlist entry in scripts/check-freshprov-image-registry.sh." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: every fresh-prov bootstrap-kit image resolves to a mirror-served Harbor proxy (#5281)."
exit 0
