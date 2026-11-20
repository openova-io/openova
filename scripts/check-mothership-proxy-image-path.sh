#!/usr/bin/env bash
# check-mothership-proxy-image-path.sh — prov pre-flight check 7 (#6778):
# every image a fresh Sovereign pulls before its cutover must be pullable,
# whole, along the path a node pulls it, with the credential a node is given,
# BEFORE a fire. Read-only: GET and HEAD only, never a registry or cluster write.
#
# ─── THE FAILURE CLASSES THIS GUARDS AGAINST ──────────────────────────────
#
# 2026-09-01: the parent's registry blob store was wiped by hand. The proxy
# cache kept its MANIFEST rows, so a health endpoint, a `/v2/` ping and a GET of
# a tag's index all answered 200 while the index's linux/amd64 CHILD manifest
# was gone; hw306 and hw307 were fired into it (every shared-pg initdb Pod in
# ImagePullBackOff, Phase 1 timed out, #6778 #6795).
# 2026-11-20: release candidate rc/20261120T073828Z-11731a9. This check said
# `IMAGE-PATH PASS 93` while a fresh install would have failed:
#   - its list missed at least 36 images the install pulls (raw manifests of
#     clusters/_template such as the Job of slot 12b, whose tag is a
#     Kustomization `images: newTag`; components only kit variables switch on,
#     the continuum controller; 18 charts that render EMPTY at their defaults;
#     k3s's own images);
#   - it read no layer, and read anonymously because its default robot account
#     was refused; the origin answered 200 for an index whose linux/amd64 child
#     was `manifest unknown` (25 repositories), and HEAD 404 for blobs it had
#     not cached yet;
#   - continuum-controller:920753e and sovereign-ops:fe65309 were absent from
#     the origin's hosted project.
# Since SOV-132 every node pulls EVERY image through the origin (its proxy
# projects for upstream images, its hosted project `openova-io` for
# ghcr.io/openova-io/openova/*) with --disable-default-registry-endpoint:
# there is no public fallback, so what the origin cannot serve, no node pulls.
#
# ─── CONTRACT ─────────────────────────────────────────────────────────────
#
#   1. The image list is BUILT FROM WHAT A FRESH INSTALL APPLIES, never a hand
#      list. Four sources, each counted; a source that yields no image FAILS
#      (exit 2), because a list that lost a source is not the list. An image
#      is an `image:` or `imageName:` value, or the `spec.package` of a
#      Crossplane Provider, Function or Configuration (pkg.crossplane.io): the
#      package fetch bypasses containerd, but the provider's and function's
#      Deployment runs that same ref as its container image, which the kubelet
#      pulls through the node map:
#        kit     every HelmRelease of clusters/_template/bootstrap-kit (the
#                files its kustomization.yaml lists), rendered with the values
#                the kit gives it: `check-freshprov-image-registry.sh
#                --list-images`, the one renderer (see its block for the value
#                sets and the Flux substitution);
#        raw     every manifest under clusters/_template that a Flux
#                Kustomization applies (the folders a `path:
#                ./clusters/_template/<folder>` names, HelmReleases excluded:
#                they are the kit), with the `images:` overrides of the
#                Kustomization that applies the folder (spec.images of a Flux
#                Kustomization, images of a kustomization.yaml);
#        k3s     the images k3s itself pulls: the release's k3s-images.txt,
#                pinned in infra/providers/_shared/k3s-images/<version>.txt for
#                the default of variable k3s_version of the huawei module;
#        phase0  what cloud-init pulls before Flux: the cilium chart version
#                it installs (infra/providers/_shared/cloudinit-control-plane.tftpl).
#      `--list-only` prints the list (`--first-party`: only the images
#      ghcr.io/openova-io/openova/*, which the node takes from the hosted
#      project). The same list feeds .github/workflows/warmup-bastion-harbor.yaml
#      and check 5 of scripts/check-train-coherent.sh.
#   2. Each ref is resolved THE WAY THE NODE RESOLVES IT, from the node registry
#      map /etc/rancher/k3s/registries.yaml. Each provider module builds that map
#      from the installation's variables document (`registry_mirror_yaml_<provider>`
#      in infra/providers/<provider>/main.tf, the rule of
#      upstream.Variables.RegistriesYAML), so this check builds the same map the
#      same way (node_map below): the document is `.variables` of HW_TFVARS, the
#      hosted rewrites are the module's `node_registry_hosted_rewrites`
#      (MIRROR_TF). Every entry lists the Sovereign's own registry first; before
#      the fire that registry does not exist, so the node pulls through the next
#      endpoint, the origin. A host no mirror of the map serves has no path.
#   3. The credential is the one the node is given: the login of the `configs`
#      of that map, `robot$openova-bot` (read from MIRROR_TF; HARBOR_ROBOT_USER
#      overrides it) with HARBOR_ROBOT_TOKEN from the environment, else the
#      `.harbor_robot_token` of HW_TFVARS. Without it the check refuses to run
#      (exit 2): an anonymous read is not what a node does.
#   4. Per image (tools/guards/cmd/image-path probe): the manifest of the tag,
#      the linux/amd64 child, then the config AND every layer by HEAD.
#        - a blob of a PROXY project that answers 404 is "not cached yet": the
#          origin fills a proxied image's blobs some minutes after its manifest
#          was first asked for, so the manifest is asked for once more and the
#          blobs are read again until PROBE_FILL_WAIT (default 10m) has passed;
#        - still missing then, it is asked for by GET at the origin
#          (ORIGIN_REGISTRY_SCHEME://ORIGIN_REGISTRY_HOST of the document, the
#          same proxy project, the node's login; a pull, never a push): the
#          origin stores a proxied blob only on a GET of it, and the relay in
#          front of it asks by HEAD first, so no wait fills it (20 Nov 2026:
#          eight blobs of 67-351 bytes of two cilium images, warmed by hand).
#          Smallest first, at most PROBE_FILL_MAX_BYTES (16 MiB) a run: a
#          larger layer is left to the origin's own pull. The node path is
#          read again until PROBE_FILL_SETTLE (2m); the run names every object
#          it filled with its size; still missing then is NEVER FILLED;
#        - an index served with its linux/amd64 child unknown is a DAMAGED CACHE
#          ENTRY, the repository named. Only a registry-side repair helps
#          (story SOV-162); this check deletes nothing;
#        - a first-party image (the hosted project) whose tag or blob is absent
#          is MISSING AT THE ORIGIN.
#   5. Summary line (paste it into the train manifest):
#          IMAGE-PATH PASS <n> images resolved through the origin (index+child+config+layers)
#          IMAGE-PATH FAIL <k>/<n>: <first 5 failures>
#
# ─── USAGE ────────────────────────────────────────────────────────────────
#   HW_TFVARS=<tofu.auto.tfvars.json> [HARBOR_ROBOT_TOKEN=...] scripts/check-mothership-proxy-image-path.sh
#   scripts/check-mothership-proxy-image-path.sh --images <file>      # a targeted list (still needs HW_TFVARS)
#   scripts/check-mothership-proxy-image-path.sh --list-only [--first-party] [--root <dir>] [--sources "kit raw k3s phase0"]
#   scripts/check-mothership-proxy-image-path.sh --resolve <ref>      # the node's resolution of one ref
#   scripts/check-mothership-proxy-image-path.sh --self-test          # stand-in registry and a fixture kit
# Needs bash, jq, yq (v4), helm and Go (the list and the probe), curl (self-test).
# Env: PROBE_PARALLEL (4), PROBE_TIMEOUT (25s), PROBE_FILL_WAIT (10m),
#      PROBE_FILL_EVERY (20s), PROBE_FILL_SETTLE (2m), PROBE_FILL_MAX_BYTES
#      (16777216), MIRROR_TF, CLOUDINIT, HARBOR_ROBOT_USER,
#      KIT_VALUE_SETS (see check-freshprov-image-registry.sh).
# Exit: 0 every image resolved whole; 1 at least one failed; 2 nothing could be
#       judged (no credential, no variables document, a source without image,
#       a list that could not be built).
# Credentials are NEVER printed. Refs #6778 #6795.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PROBE_PARALLEL="${PROBE_PARALLEL:-4}"
PROBE_TIMEOUT="${PROBE_TIMEOUT:-25s}"
PROBE_FILL_WAIT="${PROBE_FILL_WAIT:-10m}"
PROBE_FILL_EVERY="${PROBE_FILL_EVERY:-20s}"
PROBE_FILL_SETTLE="${PROBE_FILL_SETTLE:-2m}"
PROBE_FILL_MAX_BYTES="${PROBE_FILL_MAX_BYTES:-16777216}"
MIRROR_TF="${MIRROR_TF:-${REPO_ROOT}/infra/providers/huawei/main.tf}"
MODULE_REL="$(dirname "${MIRROR_TF#"${REPO_ROOT}"/}")"   # the module folder, under the repository root
FIRST_PARTY_RE='^ghcr\.io/openova-io/openova/'

IMAGES_FILE=""; MODE="probe"; RESOLVE_REF=""; FIRST_PARTY=0; LIST_ROOT="$REPO_ROOT"
LIST_SOURCES="kit raw k3s phase0"
while [ $# -gt 0 ]; do
  case "$1" in
    --images) IMAGES_FILE="${2:?--images needs a file}"; shift 2 ;;
    --parallel) PROBE_PARALLEL="${2:?--parallel needs N}"; shift 2 ;;
    --list-only) MODE="list"; shift ;;
    --first-party) FIRST_PARTY=1; shift ;;
    --root) LIST_ROOT="$(cd "${2:?--root needs a folder}" && pwd)" || exit 2; shift 2 ;;
    --sources) LIST_SOURCES="${2:?--sources needs a list}"; shift 2 ;;
    --resolve) MODE="resolve"; RESOLVE_REF="${2:?--resolve needs an image ref}"; shift 2 ;;
    --self-test) MODE="self-test"; shift ;;
    -h|--help) sed -n '2,100p' "$0"; exit 0 ;;
    *) echo "usage error: unknown argument '$1' (see --help)" >&2; exit 2 ;;
  esac
done
CLOUDINIT="${CLOUDINIT:-${LIST_ROOT}/infra/providers/_shared/cloudinit-control-plane.tftpl}"
VALUE_SETS="${KIT_VALUE_SETS:-tests/fixtures/variables/today.env tests/fixtures/variables/today.env+tests/fixtures/variables/two-region-secondary.env}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ─── The registry half, in Go (tools/guards/cmd/image-path) ───────────────
TOOL="${WORK}/image-path"
if ! ( cd "${REPO_ROOT}/tools/guards" && go build -o "$TOOL" ./cmd/image-path ) >"${WORK}/build.log" 2>&1; then
  echo "IMAGE-PATH FAIL 0/0: cannot build tools/guards/cmd/image-path (needs Go):"; sed 's/^/  /' "${WORK}/build.log"
  exit 2
fi

# ─── The node registry map, built the way the provider modules build it ───
# SOV-132: `registry_mirror_yaml_<provider>` of infra/providers/<provider>/
# main.tf is built from the variables document by the rule of
# upstream.Variables.RegistriesYAML. NODE_MAP_JQ is that rule for `mirrors`:
# each proxy name N with project path P lists the local registry, then (while
# SOVEREIGN_CONSUME is origin) ORIGIN_PROXY_ENDPOINTS_JSON[N] or the default
# origin, with the module's hosted rewrite of N, else "(.*)" -> "P/$1" when P
# is not empty; then LOCAL_REGISTRY_HOST (local only), ORIGIN_REGISTRY_HOST
# (local, origin; only while origin and when it is not a proxy name) and "*"
# (local only). With $prefire "1" the local registry, which the fire creates,
# is dropped from every entry: what is left is what a node can pull through
# before the fire.
NODE_MAP_JQ='. as $d
  | ("https://" + $d.LOCAL_REGISTRY_HOST) as $local
  | ($d.SOVEREIGN_CONSUME == "origin") as $on
  | ($d.REGISTRY_PROXY_PROJECTS_JSON | fromjson) as $projects
  | ($d.ORIGIN_PROXY_ENDPOINTS_JSON | fromjson) as $origins
  | ($d.ORIGIN_REGISTRY_SCHEME + "://" + $d.ORIGIN_REGISTRY_HOST) as $origin
  | (($projects | to_entries | map(.key as $n | .value as $p | {($n): (
        {endpoint: ([$local] + (if $on then [($origins[$n] // "") | if . == "" then $origin else . end] else [] end))}
        + (if ($hosted | has($n)) then {rewrite: $hosted[$n]} elif $p != "" then {rewrite: {"(.*)": ($p + "/$1")}} else {} end))})
      | add) // {})
    + {($d.LOCAL_REGISTRY_HOST): {endpoint: [$local]}}
    + (if $on and (($projects | has($d.ORIGIN_REGISTRY_HOST)) | not) then {($d.ORIGIN_REGISTRY_HOST): {endpoint: [$local, $origin]}} else {} end)
    + {"*": {endpoint: [$local]}}
  | if $prefire == "1" then map_values(.endpoint |= map(select(. != $local))) else . end'

# hosted_rewrites <main.tf> — the module's `node_registry_hosted_rewrites` as JSON.
hosted_rewrites() {
  grep -q -E '^  node_registry_hosted_rewrites = \{' "$1" \
    || { echo "no node_registry_hosted_rewrites in $1: that module builds no node registry map" >&2; return 2; }
  awk '/^  node_registry_hosted_rewrites = \{$/ {r = 1; next}
       r && /^  \}$/ {exit}
       r && /^    "[^"]*" = \{$/ {h = $1; next}
       r && /^      "/ {s = $0; sub(/^ +/, "", s); i = index(s, "\" "); v = substr(s, i + 1); sub(/^ *= */, "", v); print "[" h "," substr(s, 1, i) "," v "]"}' "$1" \
    | jq -s -e 'reduce .[] as $x ({}; .[$x[0]][$x[1]] = $x[2])'
}

# node_map <document.json> <main.tf> [1] — the node registry map (`mirrors`)
# the module builds from that document; with 1, its pre-fire view.
node_map() {
  local hosted
  hosted="$(hosted_rewrites "$2")" || return 2
  jq -c -e --argjson hosted "$hosted" --arg prefire "${3:-}" "$NODE_MAP_JQ" "$1"
}

# proxy_projects <document.json> — the proxy projects of the origin (the
# first segment of each project path), as a JSON list.
proxy_projects() {
  jq -c -e '.REGISTRY_PROXY_PROJECTS_JSON | fromjson | [.[] | select(. != "") | split("/")[0]] | unique' "$1"
}

# origin_url <document.json> — the origin registry, scheme and host, whose
# proxy projects the node path fronts; empty when the document names none.
origin_url() {
  jq -r 'if (.ORIGIN_REGISTRY_SCHEME // "") != "" and (.ORIGIN_REGISTRY_HOST // "") != ""
         then .ORIGIN_REGISTRY_SCHEME + "://" + .ORIGIN_REGISTRY_HOST else empty end' "$1"
}

# node_user <main.tf> — the login the module writes into the `configs` of the map.
node_user() {
  sed -n -E 's/.*auth = \{ username = "([^"]+)".*/\1/p' "$1" | head -1
}

# tftest_document <multi_region.tftest.hcl> — the module test's own variables
# document (its top-level `variables = { ... }`) as JSON.
tftest_document() {
  awk '/^variables \{$/ {t = 1} t && /^  variables = \{$/ {v = 1; next} v && /^  \}$/ {exit} v' "$1" \
    | sed -n -E 's/^ +([A-Z0-9_]+) += (".*")$/["\1",\2]/p' | jq -s -e 'map({(.[0]): .[1]}) | add'
}

# ─── The sources of the image list ────────────────────────────────────────
# Each writes refs, one a line, to stdout; nothing printed is a source that
# yielded no image.

# value_args <set> — the -vars arguments of `image-path subst` for one value set.
value_args() {
  local p; local -a parts
  IFS='+' read -r -a parts <<< "$1"
  for p in "${parts[@]}"; do printf -- '-vars\n%s\n' "${LIST_ROOT}/${p}"; done
}

src_kit() {
  ROOT="$LIST_ROOT" IMAGE_PATH_BIN="$TOOL" KIT_VALUE_SETS="$VALUE_SETS" \
    bash "${SCRIPT_DIR}/check-freshprov-image-registry.sh" --list-images 2>"${WORK}/kit.err" \
    || { echo "  the kit could not be rendered:" >&2; grep -v '^WARNING: Kubernetes' "${WORK}/kit.err" | sed 's/^/    /' >&2; return 2; }
}

src_raw() {
  local kdir="${LIST_ROOT}/clusters/_template" p f set found="${WORK}/raw-found.tsv" ov="${WORK}/raw-overrides.jsonl"
  local -a folders vargs
  : > "$found"; : > "$ov"
  mapfile -t folders < <(grep -rhoE 'path: \./clusters/_template/[A-Za-z0-9._/-]+' "$kdir" "${LIST_ROOT}/infra/providers/_shared" 2>/dev/null \
    | sed -E 's#^path: \./clusters/_template/##; s#/+$##' | sort -u)
  [ "${#folders[@]}" -gt 0 ] || { echo "  raw: no Flux Kustomization under $kdir names a folder" >&2; return 0; }
  while IFS= read -r f; do
    yq -o=json -I=0 'select(.kind == "Kustomization" and ((.apiVersion // "") | test("^kustomize\.toolkit\.fluxcd\.io/")) and ((.spec.images // []) | length) > 0)
      | {"path": (.spec.path | sub("^\./clusters/_template/"; "") | sub("/+$"; "")), "images": .spec.images}' "$f" >> "$ov" 2>/dev/null
  done < <(find "$kdir" -type f -name '*.yaml')
  for p in "${folders[@]}"; do
    [ -d "$kdir/$p" ] || continue
    if [ -f "$kdir/$p/kustomization.yaml" ]; then
      P="$p" yq -o=json -I=0 'select(((.images // []) | length) > 0) | {"path": strenv(P), "images": .images}' "$kdir/$p/kustomization.yaml" >> "$ov" 2>/dev/null
    fi
    while IFS= read -r f; do
      for set in $VALUE_SETS; do
        mapfile -t vargs < <(value_args "$set")
        yq 'select(.kind != "HelmRelease") | ... comments=""' "$f" 2>/dev/null > "${WORK}/raw.yaml" \
          && "$TOOL" subst "${vargs[@]}" < "${WORK}/raw.yaml" > "${WORK}/raw.sub.yaml" 2>"${WORK}/raw.err" \
          || { echo "  raw: ${f#"${LIST_ROOT}"/} cannot be read or substituted: $(head -1 "${WORK}/raw.err")" >&2; return 2; }
        yq '(.. | select(tag == "!!map") | (.image, .imageName) | select(tag == "!!str")),
          (select(((.apiVersion // "") | test("^pkg\.crossplane\.io/")) and (.kind == "Provider" or .kind == "Function" or .kind == "Configuration"))
            | .spec.package | select(tag == "!!str"))' "${WORK}/raw.sub.yaml" 2>/dev/null \
          | while IFS= read -r img; do [ -n "$img" ] && printf '%s\t%s\n' "$p" "$img"; done >> "$found"
      done
    done < <(find "$kdir/$p" -type f \( -name '*.yaml' -o -name '*.yml' \) ! -name kustomization.yaml | sort)
  done
  # An override of the Kustomization that applies the folder: newName, then a
  # digest or newTag (kustomize's images transformer).
  sort -u "$found" | jq -R -r --slurpfile ov "$ov" '
    split("\t") as [$folder, $img]
    | ($img | capture("^(?<name>[^@]+?)(:(?<tag>[^:/@]+))?(@(?<digest>.+))?$")) as $r
    | ([$ov[] | select(.path == $folder) | .images[] | select(.name == $r.name)] | first) as $o
    | if $o == null then $img
      else ($o.newName // $r.name) + (if $o.digest then "@" + $o.digest elif $o.newTag then ":" + ($o.newTag | tostring) elif $r.tag then ":" + $r.tag else "" end)
      end'
}

src_k3s() {
  local tf="${LIST_ROOT}/${MODULE_REL}/variables.tf" v f
  v="$(awk '/^variable "k3s_version"/ {k = 1} k && /^[[:space:]]*default[[:space:]]*=/ {gsub(/[" ]/, ""); sub(/^default=/, ""); print; exit}' "$tf" 2>/dev/null)"
  f="${LIST_ROOT}/infra/providers/_shared/k3s-images/${v}.txt"
  if [ -z "$v" ] || [ ! -s "$f" ]; then
    echo "  k3s: no image list is pinned for k3s '${v:-?}' (the release's k3s-images.txt as infra/providers/_shared/k3s-images/${v:-?}.txt)" >&2
    return 0
  fi
  sed -e 's/#.*//' -e 's/[[:space:]]//g' -e '/^$/d' "$f"
}

src_phase0() {
  local cv
  [ -s "$CLOUDINIT" ] || { echo "  phase0: no cloud-init template at $CLOUDINIT" >&2; return 0; }
  awk '/for img in \\$/{f=1; next} f && /; do/{f=0} f{gsub(/[[:space:]\\]/,""); if ($0!="") print}' "$CLOUDINIT"
  cv="$(grep -oE 'helm upgrade --install cilium cilium/cilium --version [0-9][0-9.]*' "$CLOUDINIT" | head -1 | awk '{print $NF}')"
  if [ -n "$cv" ]; then printf 'quay.io/cilium/cilium:v%s\nquay.io/cilium/operator-generic:v%s\n' "$cv" "$cv"; fi
}

# build_image_list <out> — the list of a fresh install; one count line per
# source into ${WORK}/counts. 2 when a source failed or yielded no image.
build_image_list() {
  local out="$1" s n fp rc
  : > "$out"; : > "${WORK}/counts"
  for s in $LIST_SOURCES; do
    case "$s" in kit|raw|k3s|phase0) ;; *) echo "  unknown source '$s'" >> "${WORK}/counts"; return 2 ;; esac
    rc=0; "src_$s" > "${WORK}/src-$s.txt" || rc=$?
    sed -i -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e '/^$/d' "${WORK}/src-$s.txt"
    n="$(sort -u "${WORK}/src-$s.txt" | wc -l)"
    fp="$(sort -u "${WORK}/src-$s.txt" | grep -cE "$FIRST_PARTY_RE")"
    echo "  ℹ source ${s}: ${n} image(s), ${fp} first-party" >> "${WORK}/counts"
    if [ "$rc" -ne 0 ]; then echo "IMAGE-PATH FAIL 0/0: the source '${s}' could not be read" >> "${WORK}/counts"; return 2; fi
    if [ "$n" -eq 0 ]; then echo "IMAGE-PATH FAIL 0/0: the source '${s}' yielded no image (a list that lost a source is not the list)" >> "${WORK}/counts"; return 2; fi
    cat "${WORK}/src-$s.txt" >> "$out"
  done
  sort -u -o "$out" "$out"
  if [ "$FIRST_PARTY" = "1" ]; then
    grep -E "$FIRST_PARTY_RE" "$out" > "${out}.fp" || true
    mv "${out}.fp" "$out"
    [ -s "$out" ] || { echo "IMAGE-PATH FAIL 0/0: no first-party image in the list" >> "${WORK}/counts"; return 2; }
  fi
  return 0
}

# probe <list> — the walk of every image; the summary line is the tool's.
run_probe() {
  PROBE_USER="$1" PROBE_TOKEN="$2" "$TOOL" probe -parallel "$PROBE_PARALLEL" -timeout "$PROBE_TIMEOUT" \
    -fill-wait "$PROBE_FILL_WAIT" -fill-every "$PROBE_FILL_EVERY" -fill-settle "$PROBE_FILL_SETTLE" \
    -fill-max-bytes "$PROBE_FILL_MAX_BYTES" < "$3"
}

# ─── Self-test ─────────────────────────────────────────────────────────────
if [ "$MODE" = "self-test" ]; then
  st_fail=0
  expect() { # expect <label> <want-rc> <got-rc> <must-contain> <output>
    if [ "$3" = "$2" ] && printf '%s' "$5" | grep -q -- "$4"; then echo "  ok   $1"
    else echo "  FAIL $1 (rc=$3 want $2; output must contain '$4')"; printf '%s\n' "$5" | sed 's/^/       | /'; st_fail=1; fi
  }
  lacks() { # lacks <label> <must-not-contain> <output>
    if printf '%s' "$3" | grep -q -- "$2"; then echo "  FAIL $1 (output must not contain '$2')"; st_fail=1; else echo "  ok   $1"; fi
  }
  echo "== self-test: the node's resolution table is the map the provider modules build =="
  P="${REPO_ROOT}/infra/providers"
  for p in huawei hetzner; do
    tftest_document "${P}/${p}/tests/multi_region.tftest.hcl" > "${WORK}/doc-${p}.json"; drc=$?
    keys="$(node_map "${WORK}/doc-${p}.json" "${P}/${p}/main.tf" 2>&1 | jq -r 'keys | join(" ")' 2>&1)"
    expect "${p}: the map of its module test's document has the ten entries of a child" 0 "$drc" "^\* docker.io gcr.io ghcr.io harbor.openova.io quay.io registry-1.docker.io registry.k8s.io registry.sov.example.test xpkg.upbound.io$" "$keys"
  done
  rp() { PROBE_MIRRORS="$1" "$TOOL" resolve "$2"; }
  HWDOC="${WORK}/doc-huawei.json"; HWTF="${P}/huawei/main.tf"
  hw="$(node_map "$HWDOC" "$HWTF" 1)"
  r() { rp "$hw" "$1"; }
  o="http://registry.parent.example.test:5000"
  expect "ghcr bare -> the origin's proxy-ghcr"   0 0 "^OK $o/proxy-ghcr/cloudnative-pg/postgresql 16$" "$(r ghcr.io/cloudnative-pg/postgresql:16)"
  expect "private catalyst -> hosted project"     0 0 "^OK $o/openova-io/openova/catalyst-api 1e45db8$" "$(r ghcr.io/openova-io/openova/catalyst-api:1e45db8)"
  expect "harbor host -> its origin, at the root" 0 0 "^OK $o/proxy-ghcr/cloudnative-pg/postgresql 16$" "$(r harbor.openova.io/proxy-ghcr/cloudnative-pg/postgresql:16)"
  expect "quay -> proxy-quay"                     0 0 "^OK $o/proxy-quay/cilium/cilium v1.19.3$" "$(r quay.io/cilium/cilium:v1.19.3)"
  expect "docker.io -> proxy-dockerhub"           0 0 "^OK $o/proxy-dockerhub/velero/velero v1.18.0$" "$(r docker.io/velero/velero:v1.18.0)"
  expect "short name -> proxy-dockerhub library/" 0 0 "^OK $o/proxy-dockerhub/library/busybox 1.36$" "$(r busybox:1.36)"
  expect "digest ref probes BY DIGEST"            0 0 "^OK $o/proxy-k8s/pause sha256:0123$" "$(r registry.k8s.io/pause:3.9@sha256:0123)"
  expect "docker.io official image gets library/" 0 0 "^OK $o/proxy-dockerhub/library/busybox latest$" "$(r docker.io/busybox:latest)"
  expect "xpkg -> its own origin, at the root"    0 0 "^OK https://xpkg.example.test/crossplane/crossplane v1.18.0$" "$(r xpkg.upbound.io/crossplane/crossplane:v1.18.0)"
  expect "host outside the list -> only *, FAIL"  0 0 "^FAIL host=mirror.gcr.io " "$(r mirror.gcr.io/library/busybox:1.36)"
  expect "the Sovereign's own registry -> FAIL"   0 0 "^FAIL host=registry.sov.example.test " "$(r registry.sov.example.test/openova-io/app:1)"
  expect "hetzner: no hosted rewrite there"       0 0 "^OK https://registry.parent.example.test/proxy-ghcr/openova-io/openova/catalyst-api 1e45db8$" "$(rp "$(node_map "${WORK}/doc-hetzner.json" "${P}/hetzner/main.tf" 1)" ghcr.io/openova-io/openova/catalyst-api:1e45db8)"
  jq '.SOVEREIGN_CONSUME = "local"' "$HWDOC" > "${WORK}/doc-local.json"
  expect "SOVEREIGN_CONSUME local -> no origin"   0 0 "^FAIL host=ghcr.io " "$(rp "$(node_map "${WORK}/doc-local.json" "$HWTF" 1)" ghcr.io/cloudnative-pg/postgresql:16)"
  jq '.ORIGIN_REGISTRY_HOST = "registry.parent.example.test"' "$HWDOC" > "${WORK}/doc-originhost.json"
  expect "origin host not a proxy name -> entry"  0 0 "^OK https://registry.parent.example.test/openova-io/app 1$" "$(rp "$(node_map "${WORK}/doc-originhost.json" "$HWTF" 1)" registry.parent.example.test/openova-io/app:1)"
  expect "placeholder is SKIP"                    0 0 "^SKIP helm/envsubst placeholder" "$(r '{{ .Values.image }}:latest')"
  expect "Kyverno allowed-image GLOB is SKIP"     0 0 "^SKIP image GLOB pattern" "$(r '*/proxy-*/*')"
  expect "a bare name is docker.io library/:latest" 0 0 "^OK $o/proxy-dockerhub/library/busybox latest$" "$(r busybox)"
  expect "the node's login is read from the module" 0 0 '^robot\$openova-bot$' "$(node_user "$HWTF")"
  expect "the proxy projects are read from the document" 0 0 '"proxy-ghcr"' "$(proxy_projects "$HWDOC")"

  echo "== self-test: the walk against a stand-in origin (tools/guards/cmd/image-path stub) =="
  port_file="${WORK}/port"
  "$TOOL" stub -port-file "$port_file" -user 'robot$openova-bot' -password stub-node-token -fill-after 2s \
    -absent openova-io/openova/gone & srv_pid=$!
  for _ in $(seq 1 50); do [ -s "$port_file" ] && break; sleep 0.1; done
  [ -s "$port_file" ] || { echo "SELF-TEST FAILED — the stand-in registry did not start" >&2; kill "$srv_pid" 2>/dev/null; exit 1; }
  fport="$(cat "$port_file")"
  # The map the module builds from its test's document with every origin
  # pointed at the stand-in: the same SHAPE as a real one.
  jq --arg o "http://127.0.0.1:${fport}" --arg h "127.0.0.1:${fport}" \
    '.ORIGIN_PROXY_ENDPOINTS_JSON |= (fromjson | map_values($o) | tojson) | .ORIGIN_REGISTRY_SCHEME = "http" | .ORIGIN_REGISTRY_HOST = $h' \
    "$HWDOC" > "${WORK}/fixture-doc.json"
  fixture_mirrors="$(node_map "${WORK}/fixture-doc.json" "$HWTF" 1)"
  PROBE_PROXY_PROJECTS="$(proxy_projects "${WORK}/fixture-doc.json")"
  PROBE_ORIGIN="$(origin_url "${WORK}/fixture-doc.json")"
  export PROBE_MIRRORS="$fixture_mirrors" PROBE_PROXY_PROJECTS PROBE_ORIGIN
  expect "the origin the fill asks is read from the document" 0 0 "^http://127.0.0.1:${fport}$" "$PROBE_ORIGIN"
  PROBE_FILL_WAIT=10s; PROBE_FILL_EVERY=500ms; PROBE_FILL_SETTLE=2s
  node='robot$openova-bot'
  pr() { printf '%s\n' "$@" > "${WORK}/l.txt"; run_probe "$node" stub-node-token "${WORK}/l.txt"; }
  out="$(pr ghcr.io/healthy/app:16 harbor.openova.io/single/app:1.0 harbor.openova.io/redirected/app:1.0 ghcr.io/openova-io/openova/catalyst-api:1)"; rc=$?
  expect "healthy index+child+config+layers (a 301, a first-party image) -> PASS 4" 0 "$rc" "^IMAGE-PATH PASS 4 images resolved through the origin (index+child+config+layers)$" "$out"
  expect "  the first-party image came from the hosted project" 0 "$rc" "^PASS ghcr.io/openova-io/openova/catalyst-api:1 -> http://127.0.0.1:${fport}/openova-io/openova/catalyst-api:1 " "$out"
  jq '.REGISTRY_PROXY_PROJECTS_JSON |= (fromjson | del(."harbor.openova.io") | tojson)' "${WORK}/fixture-doc.json" > "${WORK}/fixture-cut.json"
  out="$(PROBE_MIRRORS="$(node_map "${WORK}/fixture-cut.json" "$HWTF" 1)" pr ghcr.io/healthy/app:16 harbor.openova.io/single/app:1.0)"; rc=$?
  expect "a host no mirror of the node map serves -> FAIL" 1 "$rc" "^IMAGE-PATH FAIL 1/2: harbor.openova.io/single/app:1.0 (NO NODE PATH)$" "$out"
  out="$(pr ghcr.io/healthy/app:16 ghcr.io/damaged/app:16)"; rc=$?
  expect "index 200 + amd64 child unknown -> DAMAGED CACHE ENTRY" 1 "$rc" "^IMAGE-PATH FAIL 1/2: ghcr.io/damaged/app:16 (DAMAGED CACHE ENTRY)$" "$out"
  expect "  naming the repository, deleting nothing" 1 "$rc" "DAMAGED CACHE ENTRY http://127.0.0.1:${fport}/proxy-ghcr/damaged/app \[.*this check deletes nothing\]" "$out"
  expect "  the /v2/ ping the old check trusted answers 200 all the same" 0 0 "^200$" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${fport}/v2/")"
  out="$(pr ghcr.io/filling/app:1)"; rc=$?
  expect "layers not cached yet, then filled -> PASS, and it says so" 0 "$rc" "^PASS ghcr.io/filling/app:1 .*(was not cached yet: filled after" "$out"
  out="$(PROBE_FILL_WAIT=2s pr ghcr.io/neverfill/app:1)"; rc=$?
  expect "layers not cached yet and never filled -> FAIL" 1 "$rc" "^IMAGE-PATH FAIL 1/1: ghcr.io/neverfill/app:1 (NEVER FILLED)$" "$out"
  out="$(PROBE_FILL_WAIT=1s pr ghcr.io/getfill/app:1)"; rc=$?
  expect "a blob the origin stores only on a GET -> filled by a GET there, PASS" 0 "$rc" "^PASS ghcr.io/getfill/app:1 .*(was not cached yet: filled by a GET at the origin, 1 object(s), 67 bytes)$" "$out"
  expect "  the run counts it" 0 "$rc" "filled 1 object(s), 67 bytes, by a GET through the origin's proxy project at http://127.0.0.1:${fport} " "$out"
  expect "  and names it with its size" 0 "$rc" "^    filled proxy-ghcr/getfill/app@sha256:1\{64\} 67 bytes$" "$out"
  out="$(PROBE_FILL_WAIT=1s pr ghcr.io/bigfill/app:1)"; rc=$?
  expect "a layer over the fill bound is not fetched -> FAIL" 1 "$rc" "^IMAGE-PATH FAIL 1/1: ghcr.io/bigfill/app:1 (NEVER FILLED)$" "$out"
  expect "  and the line names the bound" 1 "$rc" "^FAIL ghcr.io/bigfill/app:1 NEVER FILLED .* of 3221225472 bytes is over the fill bound of 16777216 bytes a run" "$out"
  out="$(pr ghcr.io/openova-io/openova/gone:920753e)"; rc=$?
  expect "a first-party image the hosted project lacks -> MISSING AT THE ORIGIN" 1 "$rc" "^IMAGE-PATH FAIL 1/1: ghcr.io/openova-io/openova/gone:920753e (MISSING AT THE ORIGIN)$" "$out"
  printf 'ghcr.io/healthy/app:16\n' > "${WORK}/one.txt"
  out="$(run_probe "$node" "" "${WORK}/one.txt")"; rc=$?
  expect "no credential -> nothing is read" 2 "$rc" "^IMAGE-PATH FAIL 0/0: no credential" "$out"
  out="$(run_probe 'robot$catalyst' stub-node-token "${WORK}/one.txt")"; rc=$?
  expect "a credential the origin refuses -> FAIL" 1 "$rc" "^IMAGE-PATH FAIL 1/1: ghcr.io/healthy/app:16 (CREDENTIAL REFUSED)$" "$out"
  : > "${WORK}/empty.txt"
  out="$(run_probe "$node" stub-node-token "${WORK}/empty.txt")"; rc=$?
  expect "empty list -> FAIL 'no images extracted'" 2 "$rc" "^IMAGE-PATH FAIL 0/0: no images extracted" "$out"
  out="$(pr '{{ .Values.image }}:latest' '*/proxy-*/*')"; rc=$?
  expect "all-skipped list -> FAIL 'no images extracted'" 2 "$rc" "^IMAGE-PATH FAIL 0/0: no images extracted" "$out"
  kill "$srv_pid" 2>/dev/null; wait "$srv_pid" 2>/dev/null

  echo "== self-test: the list holds one image of each source of a fixture install =="
  F="${WORK}/fixture"
  mkdir -p "$F/clusters/_template/bootstrap-kit" "$F/clusters/_template/fixture-raw" "$F/platform/fixture/chart/templates" \
    "$F/products" "$F/${MODULE_REL}" "$F/infra/providers/_shared/k3s-images" "$F/tests/fixtures/variables"
  printf 'resources:\n  - 01-fixture.yaml\n  - 02-fixture-base.yaml\n  - 12b-raw.yaml\n' > "$F/clusters/_template/bootstrap-kit/kustomization.yaml"
  cat > "$F/clusters/_template/bootstrap-kit/01-fixture.yaml" <<'YAML'
# ${A comment the substitution never sees}
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-fixture
  namespace: flux-system
spec:
  releaseName: fixture
  targetNamespace: fixture
  chart:
    spec:
      chart: bp-fixture
      version: 1.0.0
  values:
    enabled: ${FIXTURE_ENABLED:-false}
    image: ghcr.io/openova-io/openova/fixture-app:kit1
    package: xpkg.upbound.io/crossplane-contrib/fixture-provider:kit1
YAML
  # A release of the same chart that the kit switches on itself, whatever the
  # value set: the kit source keeps an image when the overlay is not laid on.
  cat > "$F/clusters/_template/bootstrap-kit/02-fixture-base.yaml" <<'YAML'
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-fixture-base
  namespace: flux-system
spec:
  releaseName: fixture-base
  targetNamespace: fixture
  chart:
    spec:
      chart: bp-fixture
      version: 1.0.0
  values:
    enabled: true
    image: ghcr.io/openova-io/openova/fixture-base:kit1
    package: xpkg.upbound.io/crossplane-contrib/fixture-base-provider:kit1
YAML
  cat > "$F/clusters/_template/bootstrap-kit/12b-raw.yaml" <<'YAML'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: fixture-raw
  namespace: flux-system
spec:
  path: ./clusters/_template/fixture-raw
  images:
    - name: ghcr.io/openova-io/openova/fixture-ops
      newTag: raw1
YAML
  printf 'apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: ops\nspec:\n  template:\n    spec:\n      containers:\n        - name: ops\n          image: ghcr.io/openova-io/openova/fixture-ops\n' \
    > "$F/clusters/_template/fixture-raw/job.yaml"
  # A Crossplane package is an image (its Deployment runs the same ref); a
  # `package:` of another kind is not.
  pkgdocs='apiVersion: pkg.crossplane.io/v1\nkind: %s\nmetadata:\n  name: fixture\nspec:\n  package: %s\n---\napiVersion: example.test/v1\nkind: Widget\nmetadata:\n  name: fixture\nspec:\n  package: fixture-not-an-image\n'
  # shellcheck disable=SC2059
  printf "$pkgdocs" Function xpkg.upbound.io/crossplane-contrib/fixture-fn:v1 > "$F/clusters/_template/fixture-raw/function.yaml"
  # shellcheck disable=SC2059
  printf "{{- if .Values.enabled }}\n${pkgdocs}{{- end }}\n" Provider '{{ .Values.package }}' > "$F/platform/fixture/chart/templates/provider.yaml"
  printf 'apiVersion: v2\nname: bp-fixture\nversion: 1.0.0\n' > "$F/platform/fixture/chart/Chart.yaml"
  printf 'enabled: false\nimage: ghcr.io/openova-io/openova/fixture-app:default\npackage: xpkg.upbound.io/crossplane-contrib/fixture-provider:default\n' > "$F/platform/fixture/chart/values.yaml"
  printf '{{- if .Values.enabled }}\napiVersion: v1\nkind: Pod\nmetadata:\n  name: app\nspec:\n  containers:\n    - name: app\n      image: {{ .Values.image }}\n{{- end }}\n' \
    > "$F/platform/fixture/chart/templates/pod.yaml"
  printf 'variable "k3s_version" {\n  type    = string\n  default = "v9.9.9+k3s1"\n}\n' > "$F/${MODULE_REL}/variables.tf"
  printf '# the release list\ndocker.io/rancher/mirrored-pause:3.6\n' > "$F/infra/providers/_shared/k3s-images/v9.9.9+k3s1.txt"
  printf 'runcmd:\n  - rt helm upgrade --install cilium cilium/cilium --version 9.9.9 -n kube-system\n' > "$F/infra/providers/_shared/cloudinit-control-plane.tftpl"
  # The fixture component (fixture-app, fixture-provider) is switched on ONLY by
  # the overlay of a region of a two-region install, as CONTINUUM_ENABLED is
  # (false in today.env): the default value sets must lay the overlay on.
  printf '# the single-region primary: the fixture component is off here\nFIXTURE_ENABLED=false\n' > "$F/tests/fixtures/variables/today.env"
  printf '# a region of a two-region install: only this overlay switches the fixture component on\nFIXTURE_ENABLED=true\n' > "$F/tests/fixtures/variables/two-region-secondary.env"
  ls1() { env -u KIT_VALUE_SETS bash "$0" --list-only --root "$F" "$@" 2>&1; }
  out="$(ls1)"; rc=$?
  for img in ghcr.io/openova-io/openova/fixture-app:kit1 ghcr.io/openova-io/openova/fixture-ops:raw1 docker.io/rancher/mirrored-pause:3.6 quay.io/cilium/cilium:v9.9.9 \
    xpkg.upbound.io/crossplane-contrib/fixture-provider:kit1 xpkg.upbound.io/crossplane-contrib/fixture-fn:v1 ghcr.io/openova-io/openova/fixture-base:kit1; do
    expect "the list holds $img" 0 "$rc" "^${img}$" "$out"
  done
  lacks "  but not the package of a kind that is not a Crossplane package" "fixture-not-an-image" "$out"
  expect "  and counts each source" 0 "$rc" "^  ℹ source raw: 2 image(s), 1 first-party$" "$out"
  out="$(KIT_VALUE_SETS=tests/fixtures/variables/today.env bash "$0" --list-only --root "$F" 2>&1)"; rc=$?
  expect "with today.env alone the list still builds" 0 "$rc" "^ghcr.io/openova-io/openova/fixture-base:kit1$" "$out"
  lacks "  but loses the component only the overlay switches on" "fixture-app:kit1" "$out"
  lacks "  and its Crossplane package" "fixture-provider:kit1" "$out"
  out="$(cd "$F" && env -u KIT_VALUE_SETS ROOT="$F" IMAGE_PATH_BIN="$TOOL" bash "${SCRIPT_DIR}/check-freshprov-image-registry.sh" --list-images 2>&1)"; rc=$?
  expect "the renderer's own default value sets lay the overlay on too" 0 "$rc" "^ghcr.io/openova-io/openova/fixture-app:kit1$" "$out"
  mkdir -p "$F/clusters/_template/bootstrap-kit/folder-entry"
  cp "$F/clusters/_template/bootstrap-kit/kustomization.yaml" "${WORK}/kustomization.keep"
  printf '  - folder-entry\n' >> "$F/clusters/_template/bootstrap-kit/kustomization.yaml"
  out="$(ls1)"; rc=$?
  expect "a kit entry that is not a file (a folder) fails the source" 2 "$rc" "^IMAGE-PATH FAIL 0/0: the source 'kit' could not be read" "$out"
  expect "  and is named" 2 "$rc" "folder-entry: not a file of the kit" "$out"
  cp "${WORK}/kustomization.keep" "$F/clusters/_template/bootstrap-kit/kustomization.yaml"
  lacks "the chart at its DEFAULT values renders no image (the handling before this change loses it)" "image:" \
    "$(helm template fixture "$F/platform/fixture/chart" 2>&1)"
  without() { # without <label> <sources> <lost> <kept>: the list still builds, without that source's image
    out="$(ls1 --sources "$2")"; rc=$?
    expect "without the $1 handling the list still builds" 0 "$rc" "$4" "$out"
    lacks "  but loses $3" "$3" "$out"
  }
  without kit "raw k3s phase0" "fixture-app:kit1" "^ghcr.io/openova-io/openova/fixture-ops:raw1$"
  without raw "kit k3s phase0" "fixture-ops" "^ghcr.io/openova-io/openova/fixture-app:kit1$"
  without k3s "kit raw phase0" "mirrored-pause" "^quay.io/cilium/cilium:v9.9.9$"
  without phase0 "kit raw k3s" "quay.io/cilium/cilium" "^docker.io/rancher/mirrored-pause:3.6$"
  out="$(ls1 --first-party)"; rc=$?
  expect "--first-party keeps the first-party images" 0 "$rc" "^ghcr.io/openova-io/openova/fixture-ops:raw1$" "$out"
  lacks "  and drops the others" "mirrored-pause" "$out"
  rm "$F/infra/providers/_shared/k3s-images/v9.9.9+k3s1.txt"
  out="$(ls1)"; rc=$?
  expect "a source that yields no image FAILS the list" 2 "$rc" "^IMAGE-PATH FAIL 0/0: the source 'k3s' yielded no image" "$out"
  echo ""
  if [ "$st_fail" -ne 0 ]; then echo "SELF-TEST FAILED — the image-path check is broken." >&2; exit 1; fi
  echo "SELF-TEST PASSED — builds the map the modules build, walks index, child, config and every layer with the node's login, waits for a blob that is not cached yet, fills by a GET at the origin a blob only a GET fills (within its byte bound), names a damaged cache entry and a first-party image missing at the origin, refuses to run without a credential, and lists one image of each source."
  exit 0
fi

# ─── List only ────────────────────────────────────────────────────────────
LIST="${WORK}/images.txt"
if [ "$MODE" = "list" ]; then
  build_image_list "$LIST"; rc=$?
  cat "${WORK}/counts" >&2
  [ "$rc" -eq 0 ] || exit 2
  cat "$LIST"
  exit 0
fi

# ─── The node's resolution table ──────────────────────────────────────────
# The map the module builds from the installation's own variables document,
# in its pre-fire view.
[ -n "${HW_TFVARS:-}" ] && [ -s "${HW_TFVARS}" ] \
  || { echo "IMAGE-PATH FAIL 0/0: no variables document (set HW_TFVARS to the installation's tofu.auto.tfvars.json; the node registry map is built from its .variables)"; exit 2; }
jq -e '.variables | objects' "$HW_TFVARS" > "${WORK}/variables.json" 2>/dev/null \
  && MIRRORS="$(node_map "${WORK}/variables.json" "$MIRROR_TF" 1)" \
  && PROXIES="$(proxy_projects "${WORK}/variables.json")" \
  || { echo "IMAGE-PATH FAIL 0/0: could not build the node registry map from .variables of $HW_TFVARS with the hosted rewrites of $MIRROR_TF"; exit 2; }
PROBE_ORIGIN="$(origin_url "${WORK}/variables.json")"
export PROBE_MIRRORS="$MIRRORS" PROBE_PROXY_PROJECTS="$PROXIES" PROBE_ORIGIN

if [ "$MODE" = "resolve" ]; then "$TOOL" resolve "$RESOLVE_REF"; exit $?; fi

# ─── Image list ───────────────────────────────────────────────────────────
if [ -n "$IMAGES_FILE" ]; then
  [ -s "$IMAGES_FILE" ] || { echo "IMAGE-PATH FAIL 0/0: no images extracted (--images file '$IMAGES_FILE' is empty or missing)"; exit 2; }
  sed '/^$/d' "$IMAGES_FILE" | sort -u > "$LIST"
else
  build_image_list "$LIST"; rc=$?
  cat "${WORK}/counts"
  [ "$rc" -eq 0 ] || exit 2
fi

# ─── The node's credential (never printed) ────────────────────────────────
NODE_USER="${HARBOR_ROBOT_USER:-$(node_user "$MIRROR_TF")}"
if [ -z "${HARBOR_ROBOT_TOKEN:-}" ]; then
  HARBOR_ROBOT_TOKEN="$(jq -r '.harbor_robot_token // empty' "$HW_TFVARS" 2>/dev/null)"
fi

echo "Probing $(wc -l < "$LIST") image ref(s) along the node's own mirror path (index -> linux/amd64 child -> config and every layer) as ${NODE_USER:-?} …"
run_probe "${NODE_USER:-}" "${HARBOR_ROBOT_TOKEN:-}" "$LIST"
exit $?
