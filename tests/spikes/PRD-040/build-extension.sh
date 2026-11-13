#!/usr/bin/env bash
# Compile the stub consent extension against the jars of the Keycloak image
# under test (same version at compile time and at run time), then hand the jar
# to the SIG's Keycloak image build.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
: "${KC_VERSION:?}" "${KCSIG:?}" "${RUNNER_TEMP:?}"
W="$RUNNER_TEMP/ext"
rm -rf "$W" && mkdir -p "$W/classes"
IMAGE="quay.io/keycloak/keycloak:${KC_VERSION}"
docker pull -q "$IMAGE"
cid="$(docker create "$IMAGE")"
docker cp "$cid:/opt/keycloak/lib/lib/main" "$W/lib"
docker rm "$cid" >/dev/null
echo "Keycloak jars used for compilation:"
ls "$W/lib" | grep -E '^org\.keycloak\.keycloak-(core|services|server-spi|server-spi-private)-[0-9]'
docker run --rm -v "$W:/w" -v "$HERE/extension:/src:ro" eclipse-temurin:21-jdk bash -c '
  set -e
  javac --release 17 -Xlint:-options -cp "/w/lib/*" -d /w/classes $(find /src/src -name "*.java")
  cp -r /src/resources/. /w/classes/
  jar cf /w/prd040-consent-stub.jar -C /w/classes .
  jar tf /w/prd040-consent-stub.jar'
cp "$W/prd040-consent-stub.jar" "$KCSIG/conformance-tests-env/test-target/keycloak/keycloak/prd040-consent-stub.jar"
sha256sum "$W/prd040-consent-stub.jar"
