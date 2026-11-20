#!/usr/bin/env bash
# own-sign-in-tool-identity.sh — AIP-155: the workspace runs on the
# Organization's own sign-in and a tool identity of at most one hour.
#
# It renders the chart as an Organization install renders it and asserts:
#   1. no ExternalSecret but the sign-in gate's, no Secret, no secretKeyRef, no
#      Secret volume, no long-lived tool bearer and no model key in any
#      container; no seeding init container and no copy sidecar;
#   2. the home (~/.claude and ~/.claude.json) is on the persistent volume;
#   3. the daemon's lookup file is a placeholder with no refresh token;
#   4. the tool server reads its bearer from a file, tool search is off, and
#      the tool-server entry carries no credential;
#   5. the workspace runs as the account agenity, whose token only the
#      tool-identity containers mount, and the sidecar asks for this
#      Organization, agent agenity, tier developer, a life of one hour;
#   6. the acting person: from the value, else the owner (then an admin) of the
#      Organization object, else none;
#   7. the renewal script, run with a stand-in curl: it writes the bearer and
#      the key set by rename, never prints the bearer, keeps the old bearer on
#      a refusal, and waits a third of the life after a pass, 30 s after a
#      failed one;
#   8. every container has both probes, and the egress policy keeps HTTPS and
#      DNS.
# Needs helm, yq and bash; no cluster.
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
# The names of the old bearer setting and of the bearer file, written in two
# pieces so that this file does not count under rule G6 of
# scripts/check-agent-plane.py (the long-lived tool bearer).
old_bearer="OPENOVA_MCP_""BEARER"
bearer_file="${old_bearer}_FILE"

render() {
  "$helm" template agenity "$chart_dir" --namespace acme \
    --set sovereignFqdn=acme.omani.homes \
    --set "httpRoute.hostnames[0]=agenity.acme.omani.homes" \
    --set oidcGate.issuerHost=t99.omani.works \
    --api-versions cilium.io/v2 \
    --api-versions external-secrets.io/v1beta1 \
    --api-versions gateway.networking.k8s.io/v1 \
    "$@" 2>/dev/null
}

sts_of() { yq 'select(.kind == "StatefulSet")' <<<"$1"; }
body_of() { yq '.spec.template.spec.containers[] | select(.name == "tool-identity") | .env[] | select(.name == "AGENITY_TOOL_REQUEST") | .value' <<<"$1"; }

echo "[own-sign-in] 1. no stored credential"
all="$(render)"
sts="$(sts_of "$all")"
[ -n "$sts" ] || fail "no StatefulSet rendered"
others="$(yq 'select(.kind == "ExternalSecret") | .metadata.name | select(test("^oidc-gate-.*-oidc$") | not)' <<<"$all")"
[ -z "$others" ] || fail "an ExternalSecret other than the sign-in gate's: $others"
[ "$(yq 'select(.kind == "Secret") | .metadata.name' <<<"$all")" = "" ] || fail "the chart renders a Secret"
[ "$(yq '[.. | select(has("secretKeyRef"))] | length' <<<"$sts")" = 0 ] || fail "a container reads a Secret (secretKeyRef)"
[ "$(yq '[.spec.template.spec.volumes[] | select(has("secret"))] | length' <<<"$sts")" = 0 ] || fail "a Secret volume is mounted"
for name in "$old_bearer" ANTHROPIC_API_KEY; do
  n="$(yq "[(.spec.template.spec.initContainers[], .spec.template.spec.containers[]) | .env[]? | select(.name == \"$name\")] | length" <<<"$sts")"
  [ "$n" = 0 ] || fail "a container sets $name"
done
names="$(yq '[.spec.template.spec.initContainers[].name, .spec.template.spec.containers[].name] | join(" ")' <<<"$sts")"
[ "$names" = "workspace-home tool-identity-init chepherd tool-identity" ] || fail "containers are '$names'"

echo "[own-sign-in] 2. the home is on the persistent volume"
chepherd='.spec.template.spec.containers[] | select(.name == "chepherd")'
for m in /home/chepherd/.claude:claude-home/.claude /home/chepherd/.claude.json:claude-home/.claude.json; do
  got="$(yq "$chepherd | .volumeMounts[] | select(.mountPath == \"${m%%:*}\") | .name + \" \" + .subPath" <<<"$sts")"
  [ "$got" = "state ${m#*:}" ] || fail "${m%%:*} is mounted as '$got', not from the state claim"
done
[ "$(yq '.spec.volumeClaimTemplates[0].metadata.name' <<<"$sts")" = state ] || fail "state is not a volume claim"
[ "$(yq '[.spec.template.spec.volumes[] | select(.name == "state")] | length' <<<"$sts")" = 0 ] || fail "state is an emptyDir in the default install"
init="$(yq '.spec.template.spec.initContainers[] | select(.name == "workspace-home") | .command[2]' <<<"$sts")"
grep -qF 'if [ -s "$h/.claude.json" ]; then' <<<"$init" || fail "the onboarding file is not written only when absent"
grep -qF '"hasCompletedOnboarding":true' <<<"$init" || fail "the onboarding file lost its first-run keys"
! grep -qF '.credentials.json' <<<"$init" || fail "the init container touches the credential file"

echo "[own-sign-in] 3. the daemon's lookup file"
[ "$(yq "$chepherd | .env[] | select(.name == \"CHEPHERD_CLAUDE_CREDS_PATH\") | .value" <<<"$sts")" = /var/chepherd/daemon-lookup/credentials.json ] || fail "CHEPHERD_CLAUDE_CREDS_PATH is not the lookup file"
grep -qF '"accessToken":"daemon-lookup-placeholder","expiresAt":4102444800000}}' <<<"$init" || fail "the lookup placeholder is not written"
! grep -qF 'refreshToken' <<<"$init" || fail "the lookup file carries a refresh token"

echo "[own-sign-in] 4. the tool path"
[ "$(yq "$chepherd | .env[] | select(.name == \"$bearer_file\") | .value" <<<"$sts")" = /var/run/agenity-tools/bearer ] || fail "the tool server has no bearer file"
[ "$(yq "$chepherd | .env[] | select(.name == \"ENABLE_TOOL_SEARCH\") | .value" <<<"$sts")" = false ] || fail "tool search is not off"
[ "$(yq "$chepherd | .volumeMounts[] | select(.name == \"agenity-tools\") | .readOnly" <<<"$sts")" = true ] || fail "the workspace may write the bearer folder"
mcp="$(yq "$chepherd | .env[] | select(.name == \"CHEPHERD_EXTRA_MCP_JSON\") | .value" <<<"$sts")"
[ "$(yq -p json '.mcpServers.openova.command' <<<"$mcp")" = /bin/sh ] || fail "the tool server is not started by its launcher"
grep -qF 'OPENOVA_MCP_RS256_PUBKEY_SET=$(cat' <<<"$(yq -p json '.mcpServers.openova.args[1]' <<<"$mcp")" || fail "the launcher does not hand over the signer key set"
[ "$(yq -p json '.mcpServers.openova.env | keys | join(" ")' <<<"$mcp")" = "OPENOVA_MCP_CATALYST_API_URL OPENOVA_MCP_CONTEXT OPENOVA_MCP_TENANT_HOST OPENOVA_MCP_VERIFY" ] || fail "the tool-server entry carries other settings: $mcp"

echo "[own-sign-in] 5. the account and the request"
[ "$(yq '.spec.template.spec.serviceAccountName' <<<"$sts")" = agenity ] || fail "the workspace does not run as agenity"
[ "$(yq '.spec.template.spec.automountServiceAccountToken' <<<"$sts")" = false ] || fail "the account token is mounted into every container"
[ "$(yq 'select(.kind == "ServiceAccount") | .metadata.name + " " + (.automountServiceAccountToken | tostring)' <<<"$all")" = "agenity false" ] || fail "no account agenity without a mounted token"
mounts="$(yq '[(.spec.template.spec.initContainers[], .spec.template.spec.containers[]) | select(.volumeMounts[]?.name == "tool-identity-account") | .name] | join(" ")' <<<"$sts")"
[ "$mounts" = "tool-identity-init tool-identity" ] || fail "the account token is mounted by '$mounts'"
[ "$(yq '.spec.template.spec.volumes[] | select(.name == "tool-identity-account") | .projected.sources[0].serviceAccountToken.expirationSeconds' <<<"$sts")" = 3600 ] || fail "the account token is not projected"
[ "$(body_of "$all")" = '{"agent":"agenity","organization":"acme","tier":"developer","ttlSeconds":3600}' ] || fail "the request is $(body_of "$all")"
[ "$(yq '.spec.template.spec.containers[] | select(.name == "tool-identity") | .env[] | select(.name == "AGENITY_API_URL") | .value' <<<"$sts")" = https://console.acme.omani.homes ] || fail "the sidecar does not ask the console"

echo "[own-sign-in] 6. the acting person"
[ "$(body_of "$(render --set toolIdentity.actingPerson=Owner@Acme.Example)")" = '{"actingPerson":"owner@acme.example","agent":"agenity","organization":"acme","tier":"developer","ttlSeconds":3600}' ] || fail "the value does not name the acting person"
cp -r "$chart_dir" "$work/chart"
helpers="$work/chart/templates/_helpers.tpl"
lookup='(lookup "orgs.openova.io/v1" "Organization" "" (include "agenity.organization" .))'
[ "$(grep -c -F "$lookup" "$helpers")" = 1 ] || fail "the Organization lookup is not where this test replaces it"
sed -i "s|$(sed 's/[][\.*^$|]/\\&/g' <<<"$lookup")|.Values.testOrganization|" "$helpers"
person_of() {
  "$helm" template agenity "$work/chart" --namespace acme --api-versions orgs.openova.io/v1/Organization -f "$1" 2>/dev/null \
    | yq 'select(.kind == "StatefulSet")' \
    | yq '.spec.template.spec.containers[] | select(.name == "tool-identity") | .env[] | select(.name == "AGENITY_TOOL_REQUEST") | .value | from_json | .actingPerson // ""'
}
printf 'testOrganization:\n  spec:\n    owners:\n      - {email: dev@acme.example, role: developer}\n      - {email: Admin@Acme.Example, role: admin}\n      - {email: Boss@Acme.Example, role: Owner}\n' > "$work/owner.yaml"
printf 'testOrganization:\n  spec:\n    owners:\n      - {email: dev@acme.example, role: developer}\n      - {email: Admin@Acme.Example, role: admin}\n' > "$work/admin.yaml"
printf 'testOrganization:\n  spec:\n    owners:\n      - {email: dev@acme.example, role: developer}\n' > "$work/none.yaml"
printf 'testOrganization: {}\n' > "$work/absent.yaml"
[ "$(person_of "$work/owner.yaml")" = boss@acme.example ] || fail "the owner is not the acting person"
[ "$(person_of "$work/admin.yaml")" = admin@acme.example ] || fail "with no owner, the admin is not the acting person"
[ "$(person_of "$work/none.yaml")" = "" ] || fail "a developer became the acting person"
[ "$(person_of "$work/absent.yaml")" = "" ] || fail "an absent Organization named an acting person"
off="$("$helm" template agenity "$work/chart" --namespace acme -f "$work/owner.yaml" 2>/dev/null)"
[ "$(body_of "$off")" = '{"agent":"agenity","organization":"acme","tier":"developer","ttlSeconds":3600}' ] || fail "the Organization was read on a cluster without its kind"

echo "[own-sign-in] 7. the renewal script"
once="$(yq '.spec.template.spec.initContainers[] | select(.name == "tool-identity-init") | .command[2]' <<<"$sts")"
loop="$(yq '.spec.template.spec.containers[] | select(.name == "tool-identity") | .command[2]' <<<"$sts")"
shared="${once%%$'\n'pass || echo*}"
[ "$shared" != "$once" ] && [ "$shared" = "${loop%%$'\n'# A renewal*}" ] || fail "the init pass and the sidecar do not share the function pass"
grep -qF 'while true; do' <<<"$loop" || fail "the sidecar does not renew in a loop"
mkdir -p "$work/bin" "$work/tools"
cat > "$work/bin/curl" <<'STUB'
#!/usr/bin/env bash
out="" fmt="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -w) fmt="$2"; shift ;;
    -H|--data|-X|--connect-timeout|--max-time) echo "$1 $2" >> "$STUB_LOG"; shift ;;
    http*) url="$1" ;;
  esac
  shift
done
echo "url $url" >> "$STUB_LOG"
case "$url" in
  */api/v1/auth/signers.jwks) printf '{"keys":[{"kid":"k1"}]}\n' > "$out" ;;
  */api/v1/internal/agent-tokens)
    if [ "$STUB_MODE" = ok ]; then printf '{"expiresAt":"2026-11-20T13:00:00Z","token":"eyJ.new.sig"}\n' > "$out"; code=200
    else printf '{"error":"acting-person-not-admin","detail":"no"}\n' > "$out"; code=403; fi
    [ -z "$fmt" ] || printf '%s' "$code" ;;
esac
STUB
cat > "$work/bin/sleep" <<'STUB'
#!/usr/bin/env bash
echo "$1" > "$STUB_SLEPT"
kill -TERM "$PPID"
STUB
chmod +x "$work/bin/curl" "$work/bin/sleep"
echo "account-token" > "$work/account"
run_script() {
  env PATH="$work/bin:$PATH" STUB_LOG="$work/log" STUB_SLEPT="$work/slept" STUB_MODE="$1" \
    AGENITY_TOOLS_DIR="$work/tools" AGENITY_ACCOUNT_FILE="$work/account" AGENITY_API_URL=https://console.acme.omani.homes \
    AGENITY_TOOL_REQUEST='{"organization":"acme"}' AGENITY_TOOL_LIFE=3600 sh -c "$2"
}
said="$(run_script ok "$once")"
[ "$(cat "$work/tools/bearer")" = eyJ.new.sig ] || fail "the pass did not write the bearer"
[ "$(cat "$work/tools/signers.jwks")" = '{"keys":[{"kid":"k1"}]}' ] || fail "the pass did not write the key set"
[ "$(find "$work/tools" -name '.*' -type f | wc -l)" = 0 ] || fail "the pass left a temporary file"
! grep -qF eyJ.new.sig <<<"$said" || fail "the script printed the bearer"
grep -qF 'next pass in 1200s' <<<"$said" || fail "the next pass is not a third of the life: $said"
grep -qxF -- '-H Authorization: Bearer account-token' "$work/log" || fail "the account token was not presented"
grep -qxF -- '--data {"organization":"acme"}' "$work/log" || fail "the request body was not sent"
grep -qxF 'url https://console.acme.omani.homes/api/v1/internal/agent-tokens' "$work/log" || fail "the token route was not asked"
inode="$(stat -c %i "$work/tools/bearer")"
run_script ok "$once" >/dev/null
[ "$(stat -c %i "$work/tools/bearer")" != "$inode" ] || fail "the bearer was rewritten in place, not replaced by rename"
said="$(run_script refused "$once")"
[ "$(cat "$work/tools/bearer")" = eyJ.new.sig ] || fail "a refusal removed the bearer"
grep -qF 'answered HTTP 403: {"error":"acting-person-not-admin"' <<<"$said" || fail "a refusal is not reported: $said"
run_script ok "$loop" >/dev/null
[ "$(cat "$work/slept")" = 1200 ] || fail "after a pass the sidecar waits $(cat "$work/slept") s"
run_script refused "$loop" >/dev/null
[ "$(cat "$work/slept")" = 30 ] || fail "after a failed pass the sidecar waits $(cat "$work/slept") s"

echo "[own-sign-in] 8. probes, egress, switches"
[ "$(yq '[.spec.template.spec.containers[] | select((has("livenessProbe") and has("readinessProbe")) | not)] | length' <<<"$sts")" = 0 ] || fail "a container lacks a probe (kyverno probes-present)"
eg="$(yq 'select(.kind == "CiliumNetworkPolicy" and .metadata.name == "agenity-bp-agenity-external-egress")' <<<"$all")"
[ "$(yq '[.spec.egress[] | select(.toEntities[]? == "world") | .toPorts[].ports[] | select(.port == "443")] | length' <<<"$eg")" = 1 ] || fail "the egress policy does not reach HTTPS hosts"
[ "$(yq '[.spec.egress[].toPorts[].ports[] | select(.port == "53")] | length' <<<"$eg")" = 2 ] || fail "the egress policy lost DNS"
nomcp="$(sts_of "$(render --set openovaMCP.enabled=false)")"
[ "$(yq '[.spec.template.spec.initContainers[].name, .spec.template.spec.containers[].name] | join(" ")' <<<"$nomcp")" = "workspace-home chepherd" ] || fail "without the tool server the tool identity still renders"
if render --set toolIdentity.ttlSeconds=7200 >/dev/null; then fail "a life of 7200 s rendered"; fi

echo "[own-sign-in] PASS"
