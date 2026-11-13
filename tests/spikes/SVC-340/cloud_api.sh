#!/usr/bin/env bash
# Spike SVC-340, part 2: the cloud API path, as far as it can be measured
# without a cloud account.
#
# The platform reaches the cloud through the huaweicloud OpenTofu provider at
# the version pinned in infra/providers/huawei/.terraform.lock.hcl. This part
# asks the pinned provider itself, with fake credentials and no network path
# to any cloud:
#   1. which cache (DCS) resources it has and how their password attributes
#      are declared (provider schema);
#   2. whether changing the password of an existing cache instance or cache
#      account is planned as an in-place update or as a replacement (plan
#      against a hand-built state, -refresh=false, no API call);
#   3. which cloud API call the update makes (provider source at the pinned
#      tag).
# Every observation is one line of $OUT/cloud-results.tsv.
set -uo pipefail

OUT=${OUT:?}
REPO_ROOT=${REPO_ROOT:-$PWD}
mkdir -p "$OUT"
RES="$OUT/cloud-results.tsv"
: > "$RES"
rec() {
  local d
  d=$(printf '%s' "${4:-}" | tr '\r\n\t' '   ' | cut -c1-300)
  printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$d" | tee -a "$RES"
}

PROV=registry.opentofu.org/huaweicloud/huaweicloud
LOCK=$REPO_ROOT/infra/providers/huawei/.terraform.lock.hcl
PIN=$(awk -v p="provider \"$PROV\"" 'index($0,p)==1{f=1;next} f && $1=="version"{gsub(/"/,"",$3); print $3; exit}' "$LOCK")
rec provider pinned_version "${PIN:-none}" "infra/providers/huawei/.terraform.lock.hcl"
[ -n "$PIN" ] || exit 1
export TF_PLUGIN_CACHE_DIR=$OUT/plugin-cache
mkdir -p "$TF_PLUGIN_CACHE_DIR"

write_provider() { # DIR
  cat > "$1/provider.tf" <<EOF
terraform {
  required_providers {
    huaweicloud = {
      source  = "huaweicloud/huaweicloud"
      version = "$PIN"
    }
  }
}

# Fake credentials, a closed local auth URL and a region that does not
# exist: nothing in this spike reaches a cloud.
provider "huaweicloud" {
  region                  = "spike-region-1"
  access_key              = "SPIKEFAKEACCESSKEY"
  secret_key              = "SPIKEFAKESECRETKEY"
  project_id              = "0123456789abcdef0123456789abcdef"
  auth_url                = "http://127.0.0.1:9/v3"
  skip_check_website_type = true
  skip_check_upgrade      = true
}

variable "pw" {
  type      = string
  sensitive = true
}
EOF
}

# ---------------------------------------------------------------- 1 schema
SCH=$OUT/tf-schema
mkdir -p "$SCH"
write_provider "$SCH"
if ! (cd "$SCH" && tofu init -backend=false -input=false -no-color) > "$OUT/tofu-init.log" 2>&1; then
  rec provider init no "$(tail -5 "$OUT/tofu-init.log")"
  exit 1
fi
rec provider init yes "$(grep -i 'installed huaweicloud' "$OUT/tofu-init.log")"
(cd "$SCH" && tofu providers schema -json) > "$OUT/schema.json"
DCS=$(jq -r --arg p "$PROV" '.provider_schemas[$p].resource_schemas | keys[] | select(startswith("huaweicloud_dcs"))' "$OUT/schema.json" | tr '\n' ' ')
rec provider dcs_resources "-" "$DCS"
for r in huaweicloud_dcs_instance huaweicloud_dcs_account; do
  a=$(jq -c --arg p "$PROV" --arg r "$r" '.provider_schemas[$p].resource_schemas[$r].block.attributes // {}
        | to_entries | map(select(.key | test("password"))) | map({(.key): (.value | {required, optional, computed, sensitive})}) | add' "$OUT/schema.json")
  rec "$r" password_attributes "-" "$a"
  q=$(jq -r --arg p "$PROV" --arg r "$r" '.provider_schemas[$p].resource_schemas[$r].block.attributes // {}
        | to_entries | map(select(.value.required == true) | .key) | join(" ")' "$OUT/schema.json")
  rec "$r" required_attributes "-" "$q"
done

# ---------------------------------------------------------------- 2 plan
# Build a config from the schema's required attributes, plan a create to get
# the full planned object, turn it into an existing state, then plan a
# password change against that state without refresh.
plan_password_change() { # RESOURCE PASSWORD_ATTRIBUTE
  local r=$1 pa=$2 d="$OUT/tf-$1" sv body
  mkdir -p "$d"
  write_provider "$d"
  body=$(jq -r --arg p "$PROV" --arg r "$r" --arg pa "$pa" '
    .provider_schemas[$p].resource_schemas[$r].block.attributes
    | to_entries
    | map(select(.value.required == true or .key == $pa))
    | map(
        if .key == $pa then "  \(.key) = var.pw"
        elif .key == "engine" then "  engine = \"Redis\""
        elif .key == "engine_version" then "  engine_version = \"6.0\""
        elif .value.type == "string" then "  \(.key) = \"spike-\(.key)\""
        elif .value.type == "number" then "  \(.key) = 1"
        elif .value.type == "bool" then "  \(.key) = false"
        elif (.value.type | type) == "array" and (.value.type[0] == "list" or .value.type[0] == "set") then "  \(.key) = [\"spike-\(.key)\"]"
        elif (.value.type | type) == "array" and .value.type[0] == "map" then "  \(.key) = {}"
        else "  # unsupported type for \(.key)" end)
    | join("\n")' "$OUT/schema.json")
  printf 'resource "%s" "spike" {\n%s\n}\n' "$r" "$body" > "$d/main.tf"
  echo "--- $d/main.tf"; cat "$d/main.tf"
  (cd "$d" && tofu init -backend=false -input=false -no-color) > "$d/init.log" 2>&1
  if ! (cd "$d" && tofu plan -refresh=false -input=false -no-color -var pw=spike-old-pw -out=create.tfplan) > "$d/create.log" 2>&1; then
    rec "$r" plan_create_ok no "$(grep -E 'Error|error' "$d/create.log" | head -4)"
    return 0
  fi
  rec "$r" plan_create_ok yes ""
  (cd "$d" && tofu show -json create.tfplan) > "$d/create.json"
  sv=$(jq --arg p "$PROV" --arg r "$r" '.provider_schemas[$p].resource_schemas[$r].version // 0' "$OUT/schema.json")
  jq --arg r "$r" --arg prov "provider[\"$PROV\"]" --argjson sv "$sv" '
    {version: 4, terraform_version: "1.8.5", serial: 1, lineage: "spike-svc-340", outputs: {},
     resources: [{mode: "managed", type: $r, name: "spike", provider: $prov,
       instances: [{schema_version: $sv, sensitive_attributes: [],
         attributes: (.planned_values.root_module.resources[0].values + {id: "spike-existing-id"})}]}]}' \
    "$d/create.json" > "$d/terraform.tfstate"
  if ! (cd "$d" && tofu plan -refresh=false -input=false -no-color -var pw=spike-new-pw -out=rotate.tfplan) > "$d/rotate.log" 2>&1; then
    rec "$r" plan_rotate_ok no "$(grep -E 'Error|error' "$d/rotate.log" | head -4)"
    return 0
  fi
  (cd "$d" && tofu show -json rotate.tfplan) > "$d/rotate.json"
  rec "$r" plan_password_change_actions "$(jq -c '.resource_changes[0].change.actions' "$d/rotate.json")" \
    "replace_paths=$(jq -c '.resource_changes[0].change.replace_paths // []' "$d/rotate.json")"
  rec "$r" plan_changed_attributes "-" "$(jq -c '.resource_changes[0].change as $c
      | [$c.after | to_entries[] | select(.value != ($c.before[.key])) | .key]' "$d/rotate.json")"
  rec "$r" plan_summary_line "-" "$(grep -E 'update in-place|must be replaced|Plan:' "$d/rotate.log" | head -3)"
}
plan_password_change huaweicloud_dcs_instance password
plan_password_change huaweicloud_dcs_account account_password

# ---------------------------------------------------------------- 3 source
SRC=$OUT/provider-src
if git clone -q --depth 1 --branch "v$PIN" --filter=blob:none --sparse \
     https://github.com/huaweicloud/terraform-provider-huaweicloud.git "$SRC" 2>"$OUT/clone.err"; then
  git -C "$SRC" sparse-checkout set huaweicloud/services/dcs docs/resources
  rec provider_source tag "v$PIN" "$(git -C "$SRC" rev-parse HEAD)"
else
  rec provider_source tag none "$(cat "$OUT/clone.err")"
  exit 0
fi
D=$SRC/huaweicloud/services/dcs
block() { awk -v k="\"$2\": {" 'index($0,k){f=1} f{print; if ($0 ~ /^[[:space:]]*},[[:space:]]*$/) exit}' "$1"; }
for pair in "resource_huaweicloud_dcs_instance.go password" "resource_huaweicloud_dcs_account.go account_password"; do
  set -- $pair
  f=$D/$1
  echo "--- $1: declared cloud API calls"
  grep -n '@API' "$f"
  echo "--- $1: schema of \"$2\""
  b=$(block "$f" "$2"); printf '%s\n' "$b"
  rec "$1" "${2}_force_new" "$(grep -q 'ForceNew: *true' <<<"$b" && echo yes || echo no)" "$(tr -s '\t ' ' ' <<<"$b")"
  echo "--- $1: update path for \"$2\""
  grep -n -A4 "HasChange(\"$2\")" "$f"
  rec "$1" "${2}_api_calls" "-" "$(grep '@API' "$f" | grep -i password | sed 's#^// *@API ##' | tr '\n' ';')"
done
echo "--- password update function of the instance resource"
grep -n -A16 'func updateInstancePassword' "$D/resource_huaweicloud_dcs_instance.go"
echo "--- account resource: password handling"
grep -n -i 'password' "$D/resource_huaweicloud_dcs_account.go"
for doc in dcs_account.md dcs_instance.md; do
  echo "--- docs/resources/$doc: password and version notes"
  grep -n -i -E 'password|redis 6|redis 7|only.*support|-> ' "$SRC/docs/resources/$doc" | head -30
done
echo "== done"
