#!/bin/sh
# Harbor sign-on configurer (bp-harbor templates/sso-configure-deployment.yaml), route 1.
. /home/openova/.cache/ns-scratch/SEC-335-tools/cases/lib.sh
D=$C/harbor
rm -rf "$D"; mkdir -p "$D/script"
V=$(newvalue)
A=$(newvalue)
putfile "$D/openova" client_id harbor-test-client
putfile "$D/openova" client_secret "$V"
# The configurer also reads the Harbor administrator password from Secret harbor-admin (not this entry);
# the test gives it a value at the same path, from a plain bind mount.
putfile "$D/harbor-admin" HARBOR_ADMIN_PASSWORD "$A"
yq 'select(.kind=="ConfigMap" and .metadata.name=="harbor-sso-configure") | .data["reconcile.sh"]' "$R/harbor.yaml" \
  | sed 's#/etc/sso-creds/#/var/run/secrets/openova/#g' > "$D/script/reconcile.sh"
yq 'select(.kind=="Deployment" and .metadata.name=="harbor-sso-configure") | .spec.template.spec.containers[0].env[] | .name + "=" + .value' "$R/harbor.yaml" \
  | sed 's#^INTERVAL_SECONDS=.*#INTERVAL_SECONDS=1#; s#^HARBOR_CORE_URL=.*#HARBOR_CORE_URL=http://127.0.0.1:8099#' > "$D/env"
run() { # $1 = with | without (the mount)
  P="sec335-harbor-$1"
  podman pod create --name "$P" --network none > /dev/null
  capture_start "$P" "$D/cap-$1"
  M=""; [ "$1" = with ] && M="-v $D/openova:/var/run/secrets/openova:ro"
  podman run -d --name "$P-configure" --pod "$P" --user 1001:1001 --read-only --tmpfs /tmp \
    --env-file "$D/env" -v "$D/script:/etc/harbor-sso:ro" $M -v "$D/harbor-admin:/etc/harbor-admin:ro" \
    "$IMG_CURL" /bin/sh /etc/harbor-sso/reconcile.sh > /dev/null
  sleep 10
  podman logs "$P-configure" > "$D/$1.log" 2>&1
  podman pod rm -f "$P" > /dev/null 2>&1
}
run with
run without
if grep -l -F "PUT /api/v2.0/configurations" "$D"/cap-with/req.* 2>/dev/null | xargs -r grep -l -F "\"oidc_client_secret\": \"$V\"" | grep -q .; then
  result harbor-route1 PASS "PUT /api/v2.0/configurations carried oidc_client_secret equal to the mounted value"
else
  result harbor-route1 FAIL "no PUT carried the mounted value"
fi
if ! grep -q -F "oidc_client_secret" "$D"/cap-without/req.* 2>/dev/null && grep -q "not yet present" "$D/without.log"; then
  result harbor-control PASS "without the mount: no PUT, the script waits for the files"
else
  result harbor-control FAIL "without the mount the script went on"
fi
