#!/usr/bin/env bash
# #7938 — a tag of the local registry never moves to another digest.
#
# Measured on hw307 (18 Nov 2026): gate runs 16 and 17 could not pair because
# ghcr.io/openova-io/chargeback:0.1.67 ran with two digests. build-chargeback.yaml
# had pushed that tag three times (d326a9d9, 51c16e5d, 509d6807); a delivery's
# step 03 read the newest upstream, found another digest in the local registry
# and copied over it (Harbor push_time 14:05:06, inside the step), and the
# follower's prewarm Job re-copied the same tag into the same shared repository
# with no check at all. The copy itself kept the source digest exactly.
#
# Every case EXECUTES the shell the chart renders: dest_already_current,
# prewarm_skopeo_copy and copy_one sliced out of the rendered step-03 script with
# the resolver of its own ConfigMap, and kept_tag and copy_one of the rendered
# follower script. skopeo and the registry are a model: an upstream (ref -> raw
# manifest bytes) and a registry (path -> digest) that a copy writes as skopeo
# does (--multi-arch all writes the source's digest; a single-platform copy of an
# index writes that platform's child).
#
#   A  two consecutive runs: the first copies, the second copies nothing and
#      leaves every digest as it was (an index, a single manifest, and a
#      single-platform ref, whose local digest is its child's);
#   B  the upstream tag moves: no copy, the local digest is kept, and the Job log
#      says WARNING with the tag and both digests (the line the delivery counts
#      into prewarmWarnings);
#   C  the source cannot be read and the tag is present: kept, never named as
#      moved to the empty-input digest;
#   D  every copy carries --preserve-digests;
#   E  the follower's copy: current is skipped, moved is kept with a WARNING,
#      absent is copied --preserve-digests --multi-arch all;
#   M  controls: with the keep rule taken out of the rendered scripts (the tree
#      before the change), B and E report the moved tag as moved — the checks
#      can fail.
#
# Usage: bash tests/step7938-prewarm-tag-never-moves.sh [CHART_DIR]
set -uo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
CHART_DIR="$(cd "${CHART_DIR}" && pwd)"
FQDN="sov.example.test"
LOCAL="registry.${FQDN}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" --set secondaryRegions.replicaRegion=region-b >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}
python3 - "${TMP}/render.yaml" "${TMP}" <<'PY'
import sys, yaml
want = {"cutover-step-03-harbor-prewarm": ("run.sh", "s03.sh"), "cutover-offline-mirror-resolver": ("resolver.sh", "resolver.sh"),
        "cutover-secondary-harbor-prewarm-script": ("secondary-harbor-prewarm.sh", "sec.sh")}
for d in yaml.safe_load_all(open(sys.argv[1])):
    if d and d.get("kind") == "ConfigMap" and d["metadata"]["name"] in want:
        key, out = want[d["metadata"]["name"]]
        open(sys.argv[2] + "/" + out, "w").write(d["data"][key])
PY
for f in s03.sh resolver.sh sec.sh; do
  [ -s "${TMP}/${f}" ] || { echo "FAIL: could not extract ${f} from the render"; exit 1; }
done
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{" {f=1} f {print} f && (/^\}$/ || $0 ~ "^"n"\\(\\) \\{.*\\}$") {exit}' "$1"; }
for f in rl_slug prewarm_multiarch_flags dest_already_current prewarm_skopeo_copy mothership_proxy_code copy_one; do
  fn "${TMP}/s03.sh" "${f}" >>"${TMP}/copy.sh"
  grep -q "^${f}() {" "${TMP}/copy.sh" || fail "could not slice ${f} out of the rendered step-03 script"
done
for f in offlinemirror_skopeo_copy offlinemirror_copy; do fn "${TMP}/resolver.sh" "${f}" >>"${TMP}/sec-copy.sh"; done
for f in raw_digest kept_tag copy_one; do
  fn "${TMP}/sec.sh" "${f}" >>"${TMP}/sec-copy.sh"
  grep -q "^${f}() {" "${TMP}/sec-copy.sh" || fail "could not slice ${f} out of the rendered follower prewarm script"
done

# The upstream: <ref>.raw holds the raw manifest the registry serves for <ref>.
UP="${TMP}/upstream"; mkdir -p "${UP}"
up_file() { printf '%s/%s.raw' "${UP}" "$(printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_')"; }
sha() { printf 'sha256:%s' "$(sha256sum <"$1" | awk '{print $1}')"; }
index_of() { printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:%s","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:%s","platform":{"os":"linux","architecture":"arm64"}}]}' "$1" "$2"; }
manifest_of() { printf '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:%s"}}\n' "$1"; }
A64=$(printf 'a%.0s' $(seq 1 64)); B64=$(printf 'b%.0s' $(seq 1 64)); C64=$(printf 'c%.0s' $(seq 1 64)); D64=$(printf 'd%.0s' $(seq 1 64))
IDX="ghcr.io/openova-io/multi:1"; ONE="ghcr.io/openova-io/chargeback:0.1.67"; SPL="docker.io/vendor/one-arch:2"
index_of "${A64}" "${B64}" >"$(up_file "${IDX}")"
manifest_of "${C64}" >"$(up_file "${ONE}")"
index_of "${C64}" "${D64}" >"$(up_file "${SPL}")"

# run <name> <ref>...: one step-03 Phase A wave of the REAL copy_one over the
# shared registry ${TMP}/registry (path<TAB>digest). Copies in <dir>/calls.
REG="${TMP}/registry"; : >"${REG}"
run() {
  local dir="${TMP}/run-$1"; shift
  mkdir -p "${dir}/cp"; : >"${dir}/calls"
  (
    set -eu
    cpdir="${dir}/cp"; STUB="${dir}"; RAWS="${UP}"
    MOTHERSHIP_HOST=""; HARBOR_HOST="${LOCAL}"; HARBOR_USERNAME=w; HARBOR_PASSWORD=p
    PREWARM_DEST_TLS_VERIFY=false; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_COPY_ATTEMPTS=2; PREWARM_PROXY_COPY_ATTEMPTS=2
    PREWARM_SINGLE_PLATFORM_SUBSTRINGS="vendor/one-arch"
    export LOCAL_REGISTRY_HOST="${LOCAL}" EXCLUDED_HOSTS="" EXCLUDED_SUBSTRINGS=""
    export HOST_PROJECT_MAP="ghcr.io:proxy-ghcr docker.io:proxy-dockerhub"
    # shellcheck source=/dev/null
    . "${TMP}/resolver.sh"
    src_creds_for() { printf ''; }
    harbor_durable_digest() { awk -F'\t' -v p="${1#"${HARBOR_HOST}"/}" '$1==p {d=$2} END {if (d) print d}' "${REG}"; }
    sleep() { :; }
    skopeo() {
      local a last="" prev="" f
      for a in "$@"; do prev="${last}"; last="${a}"; done
      case " $* " in
        *" inspect "*)
          f="${RAWS}/$(printf '%s' "${last#docker://}" | tr -c 'A-Za-z0-9._-' '_').raw"
          [ -s "${f}" ] || return 1
          cat "${f}"; return 0 ;;
      esac
      echo "$* " >>"${STUB}/calls"
      f="${RAWS}/$(printf '%s' "${prev#docker://}" | tr -c 'A-Za-z0-9._-' '_').raw"
      local d
      # an entry of a list, copied by its digest before the list (offlinemirror_copy)
      if [ ! -s "${f}" ]; then case "${prev}" in *@sha256:*) d="${prev##*@}" ;; *) return 1 ;; esac
      else d="sha256:$(sha256sum <"${f}" | awk '{print $1}')"; fi
      case " $* " in *" --multi-arch system "*) d=$(jq -r '[.manifests[]? | select(.platform.architecture == "amd64") | .digest][0] // empty' <"${f}"); [ -n "${d}" ] || d="sha256:$(sha256sum <"${f}" | awk '{print $1}')" ;; esac
      { awk -F'\t' -v p="${last#docker://"${HARBOR_HOST}"/}" '$1!=p' "${REG}"; printf '%s\t%s\n' "${last#docker://"${HARBOR_HOST}"/}" "${d}"; } >"${REG}.new"
      mv "${REG}.new" "${REG}"
    }
    # shellcheck source=/dev/null
    . "${STUB}/../copy.sh"
    for r in "$@"; do copy_one "${r}"; done
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
# copies <name>: the copies of an image to one of its local paths (the entries of a list,
# copied by digest before it, are not counted).
copies() { grep -vc '@sha256:[0-9a-f]* $' "${TMP}/run-$1/calls"; }
digest_at() { awk -F'\t' -v p="$1" '$1==p {print $2}' "${REG}"; }

echo "[step7938-tag] A: two consecutive runs — the first copies, the second copies nothing and every digest stays"
run a1 "${IDX}" "${ONE}" "${SPL}"
[ "$(cat "${TMP}/run-a1/rc")" = 0 ] || fail "run 1 failed: $(tail -3 "${TMP}/run-a1/out")"
[ "$(copies a1)" = 5 ] && pass "run 1 copies each image to each of its local paths (5)" || fail "run 1 made $(copies a1) copies, want 5: $(cut -c1-120 "${TMP}/run-a1/calls" | tr '\n' ';')"
[ "$(digest_at openova-io/multi:1)" = "$(sha "$(up_file "${IDX}")")" ] && pass "the index lands under its own digest" || fail "the index landed as $(digest_at openova-io/multi:1)"
[ "$(digest_at proxy-dockerhub/vendor/one-arch:2)" = "sha256:${C64}" ] && pass "the single-platform ref lands as its amd64 child" || fail "the single-platform ref landed as $(digest_at proxy-dockerhub/vendor/one-arch:2)"
cp "${REG}" "${TMP}/registry.after1"
run a2 "${IDX}" "${ONE}" "${SPL}"
[ "$(copies a2)" = 0 ] && pass "run 2 copies nothing" || fail "run 2 copied again: $(cut -c1-160 "${TMP}/run-a2/calls" | tr '\n' ';')"
cmp -s "${REG}" "${TMP}/registry.after1" && pass "run 2 leaves every digest unchanged" || fail "run 2 changed digests: $(diff "${TMP}/registry.after1" "${REG}" | tr '\n' ' ')"
grep -q 'WARNING' "${TMP}/run-a2/out" && fail "run 2 warns although nothing moved: $(grep -m1 WARNING "${TMP}/run-a2/out" | cut -c1-200)" || pass "run 2 warns about nothing (the single-platform ref compares as its child)"
[ "$(ls "${TMP}/run-a2/cp/"*.ok 2>/dev/null | wc -l)" = 3 ] && pass "run 2 counts all three images ok" || fail "run 2 sentinels: $(ls "${TMP}/run-a2/cp" | tr '\n' ' ')"

echo "[step7938-tag] B: the upstream re-pushes the tag — the local digest is kept and the move is a WARNING"
OLD=$(digest_at openova-io/chargeback:0.1.67)
manifest_of "${D64}" >"$(up_file "${ONE}")"
NEW=$(sha "$(up_file "${ONE}")")
run b1 "${ONE}"
[ "$(copies b1)" = 0 ] && pass "no copy over the present tag" || fail "the tag was copied over: $(head -1 "${TMP}/run-b1/calls" | cut -c1-160)"
[ "$(digest_at openova-io/chargeback:0.1.67)" = "${OLD}" ] && [ "$(digest_at proxy-ghcr/openova-io/chargeback:0.1.67)" = "${OLD}" ] \
  && pass "both local paths keep ${OLD:0:19}" || fail "the local tag moved to $(digest_at openova-io/chargeback:0.1.67)"
grep -F '[harbor-prewarm] WARNING: tag moved upstream: ' "${TMP}/run-b1/out" | grep -F "${ONE} is now ${NEW}" | grep -qF "keeps openova-io/chargeback:0.1.67 at ${OLD}" \
  && pass "the Job log names the tag, the new upstream digest and the kept local digest" \
  || fail "no WARNING names the moved tag and both digests: $(grep -m1 WARNING "${TMP}/run-b1/out" | cut -c1-240)"
[ "$(grep -c '\] WARNING' "${TMP}/run-b1/out")" = 2 ] && pass "one WARNING per local path, each counted by the delivery's prewarmWarnings grep" || fail "WARNING lines: $(grep -c '\] WARNING' "${TMP}/run-b1/out")"
ls "${TMP}/run-b1/cp/"*.ok >/dev/null 2>&1 && pass "the kept image counts present (ok)" || fail "the kept image is not ok: $(ls "${TMP}/run-b1/cp" | tr '\n' ' ')"
grep -q 'KEPT ' "${TMP}/run-b1/cp/"*.log && pass "the per-image log says KEPT" || fail "the per-image log does not say KEPT"

echo "[step7938-tag] C: the source cannot be read — the present tag is kept, never 'moved' to the empty-input digest"
mv "$(up_file "${ONE}")" "${TMP}/one.saved"
run c1 "${ONE}"
mv "${TMP}/one.saved" "$(up_file "${ONE}")"
[ "$(copies c1)" = 0 ] && [ "$(digest_at openova-io/chargeback:0.1.67)" = "${OLD}" ] && pass "no copy, the digest stays" || fail "an unreadable source led to a copy or a move"
grep -q 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855' "${TMP}/run-c1/out" "${TMP}/run-c1/cp/"*.log \
  && fail "the empty-input sha256 was taken for the source digest" || pass "a failed read is not hashed into a digest"
grep -q 'WARNING' "${TMP}/run-c1/out" && fail "an unreadable source was reported as a moved tag" || pass "no moved-tag WARNING for an unreadable source"

echo "[step7938-tag] D: every copy carries --preserve-digests"
[ -s "${TMP}/run-a1/calls" ] && ! grep -q . <<<"$(grep -v -- '--preserve-digests' "${TMP}/run-a1/calls")" \
  && pass "all $(copies a1) copies of run 1 pass --preserve-digests" || fail "a copy without --preserve-digests: $(grep -v -- '--preserve-digests' "${TMP}/run-a1/calls" | head -1 | cut -c1-160)"
grep -c 'if offlinemirror_copy "' "${TMP}/s03.sh" | grep -qx 2 && grep -q 'copy --insecure-policy --preserve-digests "\$@"' "${TMP}/resolver.sh" \
  && pass "the chart copy (Phase A2) passes it too: both go through offlinemirror_copy" || fail "the rendered chart copy does not pass --preserve-digests"

echo "[step7938-tag] E: the follower's copy into the shared registry follows the same rule"
# sec <name> <src> <dst>: the REAL follower copy_one; the artifact API answers from ${REG}.
sec() {
  local dir="${TMP}/sec-$1"; mkdir -p "${dir}"; : >"${dir}/calls"
  (
    set -eu
    cpdir="${dir}"; STUB="${dir}"; RAWS="${UP}"
    HARBOR_HOST="${LOCAL}"; HARBOR_INTERNAL_URL="http://harbor-core.harbor.svc"; HARBOR_USERNAME=w; HARBOR_PASSWORD=p
    ghcr_user=r; ghcr_pat=s; PREWARM_SKOPEO_TIMEOUT=1s; PREWARM_DEST_TLS_VERIFY=false; PREWARM_COPY_ATTEMPTS=1
    curl() {
      local a u=""; for a in "$@"; do u="${a}"; done
      u="${u#*/api/v2.0/projects/}"; local p="${u%%/repositories/*}" rest="${u#*/repositories/}"
      local repo="${rest%%/artifacts/*}" tag="${rest##*/artifacts/}"
      repo=$(printf '%s' "${repo}" | sed 's#%2F#/#g')
      awk -F'\t' -v k="${p}/${repo}:${tag}" '$1==k {printf "{\"digest\":\"%s\"}", $2}' "${REG}"
    }
    sleep() { :; }
    skopeo() {
      local a last="" prev=""; for a in "$@"; do prev="${last}"; last="${a}"; done
      case " $* " in *" inspect "*) cat "${RAWS}/$(printf '%s' "${last#docker://}" | tr -c 'A-Za-z0-9._-' '_').raw" 2>/dev/null; return $? ;; esac
      echo "$* " >>"${STUB}/calls"
    }
    # shellcheck source=/dev/null
    . "${STUB}/../sec-copy.sh"
    copy_one "docker://$2" "docker://${LOCAL}/$3" x
  ) >"${dir}/out" 2>&1
  echo $? >"${dir}/rc"
}
sec e1 "ghcr.io/openova-io/multi:1" "openova-io/multi:1"
[ ! -s "${TMP}/sec-e1/calls" ] && grep -q '(current)' "${TMP}/sec-e1/x.ok" 2>/dev/null && pass "a current tag is not copied again" || fail "the follower re-copied a current tag: $(head -1 "${TMP}/sec-e1/calls" | cut -c1-120)"
sec e2 "${ONE}" "openova-io/chargeback:0.1.67"
[ ! -s "${TMP}/sec-e2/calls" ] && grep -q "kept at ${OLD}" "${TMP}/sec-e2/x.ok" 2>/dev/null && pass "a moved tag is kept at its local digest" || fail "the follower copied over a moved tag: $(head -1 "${TMP}/sec-e2/calls" | cut -c1-120)"
grep -F '[secondary-harbor-prewarm] WARNING: tag moved upstream: ' "${TMP}/sec-e2/out" | grep -qF "is now ${NEW}" && pass "and the follower says WARNING with the new digest" || fail "the follower does not warn: $(cut -c1-200 "${TMP}/sec-e2/out")"
manifest_of "${A64}" >"$(up_file ghcr.io/openova-io/new:3)"
sec e3 "ghcr.io/openova-io/new:3" "openova-io/new:3"
grep -q -- '--preserve-digests --multi-arch all' "${TMP}/sec-e3/calls" && pass "an absent tag is copied --preserve-digests --multi-arch all (skopeo's default wrote one platform over an index)" || fail "the follower's copy flags: $(head -1 "${TMP}/sec-e3/calls" | cut -c1-160)"

echo "[step7938-tag] M: controls — with the keep rule taken out (the tree before the change) the same checks go red"
cp "${TMP}/copy.sh" "${TMP}/copy.keep"; cp "${TMP}/sec-copy.sh" "${TMP}/sec-copy.keep"
sed -i 's/^  return 2$/  return 1/' "${TMP}/copy.sh"
! grep -q '^  return 2$' "${TMP}/copy.sh" || fail "control: could not take the keep rule out of dest_already_current"
run m1 "${ONE}"
[ "$(copies m1)" -gt 0 ] && [ "$(digest_at openova-io/chargeback:0.1.67)" = "${NEW}" ] \
  && pass "without the rule the tag is copied over and moves to ${NEW:0:19} — case B's checks would fail" \
  || fail "control: without the rule the tag did not move ($(copies m1) copies) — case B cannot fail"
sed -i 's/^  if \[ "\${_ks}" = "\${_kd}" \]; then echo "current"; return 0; fi$/&; return 1/' "${TMP}/sec-copy.sh"
grep -q 'return 0; fi; return 1$' "${TMP}/sec-copy.sh" || fail "control: could not take the keep rule out of kept_tag"
{ awk -F'\t' '$1!="openova-io/chargeback:0.1.67"' "${REG}"; printf '%s\t%s\n' "openova-io/chargeback:0.1.67" "${OLD}"; } >"${REG}.c"; mv "${REG}.c" "${REG}"
sec m2 "${ONE}" "openova-io/chargeback:0.1.67"
[ -s "${TMP}/sec-m2/calls" ] && pass "without the rule the follower copies over the moved tag — case E's checks would fail" || fail "control: the follower kept the tag without the rule — case E cannot fail"
cp "${TMP}/copy.keep" "${TMP}/copy.sh"; cp "${TMP}/sec-copy.keep" "${TMP}/sec-copy.sh"

sh -n "${TMP}/s03.sh" 2>"${TMP}/syntax.err" && pass "the rendered step-03 script parses as POSIX sh" || fail "the rendered step-03 script does not parse: $(head -2 "${TMP}/syntax.err")"
sh -n "${TMP}/sec.sh" 2>"${TMP}/syntax2.err" && pass "the rendered follower script parses as POSIX sh" || fail "the rendered follower script does not parse: $(head -2 "${TMP}/syntax2.err")"

if [ "${FAILURES}" -ne 0 ]; then echo "[step7938-tag] ${FAILURES} failure(s)"; exit 1; fi
echo "[step7938-tag] all cases pass"
