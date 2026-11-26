#!/bin/sh
# Gitea sign-on configurer (bp-gitea templates/sso-configure-deployment.yaml), route 1.
. /home/openova/.cache/ns-scratch/SEC-335-tools/cases/lib.sh
D=$C/gitea
rm -rf "$D"; mkdir -p "$D/script" "$D/bin"
V=$(newvalue)
putfile "$D/openova" key gitea-test-client
putfile "$D/openova" secret "$V"
# The rendered reconciler script, with the one change of route 1: it reads the mounted directory.
yq 'select(.kind=="ConfigMap" and .metadata.name=="gitea-sso-configure") | .data["reconcile.sh"]' "$R/gitea.yaml" \
  | sed 's#/etc/sso-creds/#/var/run/secrets/openova/#g' > "$D/script/reconcile.sh"
yq 'select(.kind=="Deployment" and .metadata.name=="gitea-sso-configure") | .spec.template.spec.containers[0].env[] | .name + "=" + .value' "$R/gitea.yaml" \
  | sed 's/^INTERVAL_SECONDS=.*/INTERVAL_SECONDS=1/' > "$D/env"
# Stand-in for kubectl: no cluster exists here; it records each call and answers like a Ready gitea pod with no sources.
cat > "$D/bin/kubectl" <<'EOF'
#!/bin/sh
echo "kubectl $*" >> /calls/kubectl.log
case "$*" in
  *"get pod"*) printf 'gitea-test-0\tTrue\n' ;;
  *"auth list"*) printf 'ID\t|Name\t|Type\t|Enabled\n' ;;
esac
exit 0
EOF
chmod 0755 "$D/bin/kubectl"
run() { # $1 = with | without (the mount)
  mkdir -p "$D/calls-$1"; chmod 0777 "$D/calls-$1"
  M=""; [ "$1" = with ] && M="-v $D/openova:/var/run/secrets/openova:ro"
  podman run -d --name "sec335-gitea-$1" --network none --user 1001:1001 --read-only --tmpfs /tmp \
    --env-file "$D/env" -e PATH=/fake/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -v "$D/script:/etc/gitea-sso:ro" $M -v "$D/bin:/fake/bin:ro" -v "$D/calls-$1:/calls" \
    "$IMG_K8S" /bin/bash /etc/gitea-sso/reconcile.sh > /dev/null
  sleep 8
  podman logs "sec335-gitea-$1" > "$D/$1.log" 2>&1
  podman rm -f "sec335-gitea-$1" > /dev/null 2>&1
}
run with
run without
if grep -F -- "add-oauth" "$D/calls-with/kubectl.log" | grep -q -F -- "--secret $V"; then
  result gitea-route1 PASS "add-oauth received --secret equal to the mounted value"
else
  result gitea-route1 FAIL "no add-oauth call carried the mounted value"
fi
if [ ! -s "$D/calls-without/kubectl.log" ] && grep -q "not yet present" "$D/without.log"; then
  result gitea-control PASS "without the mount: no kubectl call, the script waits for the files"
else
  result gitea-control FAIL "without the mount the script went on"
fi
