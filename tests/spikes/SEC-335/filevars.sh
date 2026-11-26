#!/bin/sh
export KUBECONFIG=/dev/null
O=/home/openova/.cache/ns-scratch/SEC-335-tools/filevars.out
: > $O
echo "######## keycloak env script" >> $O
podman run --rm --network none --entrypoint /bin/sh docker.io/bitnamilegacy/keycloak:26.3.3-debian-12-r0 -c 'grep -n "KC_BOOTSTRAP_ADMIN_PASSWORD\|_FILE\b\|_FILE}" /opt/bitnami/scripts/keycloak-env.sh | head -30' >> $O 2>&1
echo "######## config-cli manifest" >> $O
podman run --rm --network none --entrypoint /bin/bash docker.io/bitnamilegacy/keycloak-config-cli:6.4.0-debian-12-r11 -c 'command -v unzip || echo no-unzip; ls /opt/bitnami/keycloak-config-cli/; ls /opt/bitnami/java/bin 2>/dev/null | tr "\n" " "' >> $O 2>&1
echo "######## grafana run.sh" >> $O
podman run --rm --network none --entrypoint /bin/sh docker.io/grafana/grafana:12.3.1 -c 'grep -n "__FILE\|exec " /run.sh' >> $O 2>&1
echo "######## pda" >> $O
podman run --rm --network none --entrypoint /bin/sh docker.io/powerdnsadmin/pda-legacy:0.4.2 -c 'command -v entrypoint.sh; cat "$(command -v entrypoint.sh)"; echo ----; grep -n "_FILE\|def load_environment\|os.environ" /app/powerdnsadmin/lib/settings.py | head -20; echo ----; grep -n "oidc_oauth_secret\|oidc_oauth_key" /app/powerdnsadmin/lib/settings.py | head; ls /app/configs' >> $O 2>&1
