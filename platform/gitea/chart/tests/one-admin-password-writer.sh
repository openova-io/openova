#!/usr/bin/env bash
# One writer of the Git service admin password across a two-region Sovereign.
#
# The regions' Giteas share ONE database, so there is ONE gitea_admin row. Each
# region used to generate its own random password (templates/admin-secret.yaml)
# and each region's init (passwordMode keepUpdated) wrote its own into that row:
# the follower's init overwrote what the writer region's release (and cutover
# step 06) set, and every reader of the writer's Secret (cutover step 09, the
# token mint of bp-catalyst-platform, the organization controller) failed to
# sign in. THE RULE: the bp-gitea release of the writer region owns the
# password; a second region never generates or sets it.
#
# The test builds each region's values the way Flux does and renders the chart
# with them:
#   region A (writer)   slot 10 spec.values, substituted with region A's ids;
#   region B (follower) the ConfigMap the follower root ships (slot 10
#                       valuesFrom, merged first) + slot 10 spec.values,
#                       substituted with region B's ids (merged on top).
# A writer is either half of setting the password: the admin Secret this chart
# generates (a Secret gitea-admin-secret with a password key) or the init step
# that creates the admin user / changes its password (configure_admin_user in
# the upstream gitea-init script). Expected: region A has both, region B none.
#
# Also checked, from the files Flux applies: every multi-region-follower-* root
# lists the ConfigMap (identical) and no other root does; slot 10 reads it by
# name in its own namespace; slot 10 spec.values sets no gitea.gitea.admin key.
#
# Needs helm and yq v4. Usage: bash tests/one-admin-password-writer.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
REPO="$(cd "$CHART_DIR/../../.." && pwd)"
HELM="${HELM_BIN:-helm}"
YQ="${YQ_BIN:-yq}"
SLOT="$REPO/clusters/_template/bootstrap-kit/10-gitea.yaml"
ROOTS="$REPO/clusters/_template/root"
FIXTURE="$REPO/tests/fixtures/variables/today.env"
CM_NAME="bp-gitea-follower-values"
ADMIN_SECRET="gitea-admin-secret"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

failures=0
fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
pass() { echo "PASS: $1"; }

if ! ls "$CHART_DIR"/charts/*.tgz >/dev/null 2>&1; then
  "$HELM" dependency build "$CHART_DIR" >/dev/null
fi

# The values of slot 10 for one region: Flux parses the document, substitutes
# the variables in its text, then parses it again (comments take no part).
# $1 = this region's id, $2 = the writer region's id.
slot_values() {
  (
    while IFS= read -r line; do
      case "$line" in ''|'#'*) continue ;; esac
      export "${line%%=*}=${line#*=}"
    done < "$FIXTURE"
    export SOVEREIGN_REGION_ID="$1" SOVEREIGN_WRITER_REGION_ID="$2"
    export SOVEREIGN_PRIMARY_REGION="$2" SOVEREIGN_REPLICA_REGION="region-b"
    export SOVEREIGN_GITEA_PG_HOST="shared-pg-mesh-rw.shared-data.svc.cluster.local"
    "$YQ" 'select(.kind == "HelmRelease" and .metadata.name == "bp-gitea") | ... comments = "" | .spec.values' \
      "$SLOT" > "$TMP/raw-$1.yaml"
    RAW="$TMP/raw-$1.yaml" "$YQ" -n 'load_str(strenv(RAW)) | envsubst | from_yaml'
  )
}

# ── 1. The follower values reach slot 10 in a follower region only ─────────
: > "$TMP/follower-values.yaml"
follower_roots=0
for kz in "$ROOTS"/*/kustomization.yaml; do
  root="$(basename "$(dirname "$kz")")"
  found=""
  while IFS= read -r res; do
    f="$(dirname "$kz")/$res"
    [ -f "$f" ] || continue
    if "$YQ" "select(.kind == \"ConfigMap\" and .metadata.name == \"$CM_NAME\") | .metadata.name" "$f" | grep -qx "$CM_NAME"; then
      found="$f"
    fi
  done < <("$YQ" '.resources[]' "$kz")
  case "$root" in
    multi-region-follower-*)
      follower_roots=$((follower_roots + 1))
      if [ -z "$found" ]; then
        fail "root $root (a follower region) does not apply ConfigMap $CM_NAME"
        continue
      fi
      ns="$("$YQ" 'select(.kind == "ConfigMap") | .metadata.namespace' "$found")"
      "$YQ" 'select(.kind == "ConfigMap") | .data["values.yaml"]' "$found" > "$TMP/values-$root.yaml"
      if [ -s "$TMP/follower-values.yaml" ]; then
        cmp -s "$TMP/follower-values.yaml" "$TMP/values-$root.yaml" \
          || fail "root $root carries follower values that differ from the other follower root"
      else
        cp "$TMP/values-$root.yaml" "$TMP/follower-values.yaml"
        follower_ns="$ns"
      fi
      pass "root $root applies ConfigMap $ns/$CM_NAME"
      ;;
    *)
      if [ -n "$found" ]; then fail "root $root (not a follower region) applies ConfigMap $CM_NAME"; fi
      ;;
  esac
done
[ "$follower_roots" -ge 1 ] || fail "no multi-region-follower-* root found under $ROOTS"

hr_ns="$("$YQ" 'select(.kind == "HelmRelease" and .metadata.name == "bp-gitea") | .metadata.namespace' "$SLOT")"
wired="$("$YQ" "select(.kind == \"HelmRelease\" and .metadata.name == \"bp-gitea\") | .spec.valuesFrom[]? | select(.kind == \"ConfigMap\" and .name == \"$CM_NAME\") | .valuesKey" "$SLOT")"
if [ "$wired" = "values.yaml" ] && [ "${follower_ns:-}" = "$hr_ns" ]; then
  pass "slot 10 (HelmRelease $hr_ns/bp-gitea) reads ConfigMap $CM_NAME key values.yaml"
else
  fail "slot 10 does not read ConfigMap $hr_ns/$CM_NAME key values.yaml (valuesFrom key '${wired}', ConfigMap namespace '${follower_ns:-}')"
fi
if [ "$("$YQ" 'select(.kind == "HelmRelease" and .metadata.name == "bp-gitea") | .spec.values.gitea.gitea | has("admin")' "$SLOT")" = "true" ]; then
  fail "slot 10 spec.values sets gitea.gitea.admin: it takes precedence over the follower values"
fi

# ── 2. Render both regions with their values ───────────────────────────────
slot_values region-a region-a > "$TMP/a-values.yaml"
slot_values region-b region-a > "$TMP/b-values.yaml"
if [ "$("$YQ" '.platformRepos.regionId + "/" + .platformRepos.writerRegionId' "$TMP/b-values.yaml")" != "region-b/region-a" ]; then
  fail "the substituted slot 10 values do not carry region B's ids"
fi
"$HELM" template gitea "$CHART_DIR" --namespace gitea \
  -f "$TMP/a-values.yaml" > "$TMP/a.yaml"
"$HELM" template gitea "$CHART_DIR" --namespace gitea \
  -f "$TMP/follower-values.yaml" -f "$TMP/b-values.yaml" > "$TMP/b.yaml"

# generators: Secret gitea-admin-secret with a password key (value never printed)
generators() {
  "$YQ" "select(.kind == \"Secret\" and .metadata.name == \"$ADMIN_SECRET\" and (.data.password // \"\") != \"\") | .metadata.name" "$1" | grep -cx "$ADMIN_SECRET" || true
}
# setters: the init step that creates the admin user or changes its password
setters() {
  "$YQ" 'select(.kind == "Secret" and .metadata.name == "gitea-init") | .stringData["configure_gitea.sh"]' "$1" \
    | grep -cE '^[[:space:]]*configure_admin_user[[:space:]]*$' || true
}

ga="$(generators "$TMP/a.yaml")"; sa="$(setters "$TMP/a.yaml")"
gb="$(generators "$TMP/b.yaml")"; sb="$(setters "$TMP/b.yaml")"
echo "region A (writer):   admin Secret generated ${ga}, init sets the admin password ${sa}"
echo "region B (follower): admin Secret generated ${gb}, init sets the admin password ${sb}"
if [ "$ga" = 1 ] && [ "$sa" = 1 ]; then pass "region A owns the admin password (generates it, its init applies it)"
else fail "region A must generate the admin Secret and apply it (generated ${ga}, set ${sa})"; fi
if [ "$gb" = 0 ] && [ "$sb" = 0 ]; then pass "region B neither generates nor sets the admin password"
else fail "region B is a second writer of the shared admin password (generated ${gb}, set ${sb})"; fi
writers=0
[ "$((ga + sa))" -gt 0 ] && writers=$((writers + 1))
[ "$((gb + sb))" -gt 0 ] && writers=$((writers + 1))
if [ "$writers" = 1 ]; then pass "exactly one region writes the shared admin password"
else fail "${writers} regions write the shared admin password (expected 1)"; fi

# Region B's Gitea must not depend on an admin Secret nobody renders there,
# and must not fall back to a baked password.
if "$YQ" 'select(.kind == "Deployment") | .. | select(has("secretKeyRef")) | .secretKeyRef.name' "$TMP/b.yaml" | grep -qx "$ADMIN_SECRET"; then
  fail "region B's Gitea still reads Secret $ADMIN_SECRET"
else
  pass "region B's Gitea reads no admin Secret"
fi
bpw="$("$YQ" 'select(.kind == "Deployment") | .spec.template.spec.initContainers[] | select(.name == "configure-gitea") | .env[] | select(.name == "GITEA_ADMIN_PASSWORD") | [has("valueFrom"), (.value | length)] | @csv' "$TMP/b.yaml" | grep -vE '^(---)?$' || true)"
if [ "$bpw" = "false,0" ]; then pass "region B's init has an empty admin password"
else fail "region B's init carries an admin password (valueFrom,length = ${bpw:-<no env>}; value not printed)"; fi

if [ "$failures" -ne 0 ]; then
  echo "one-admin-password-writer: ${failures} check(s) failed" >&2
  exit 1
fi
echo "one-admin-password-writer: the writer region's bp-gitea is the one writer of the admin password"
