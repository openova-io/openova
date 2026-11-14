#!/usr/bin/env bash
# build.sh - compile the fapi-consent extension, run its unit tests and write the provider archive.
#
# Runs INSIDE the Keycloak image the archive is built for, so it compiles against exactly the
# jars that will load it. The image carries a Java runtime with the compiler and jar modules
# but no javac or jar launcher, so both tools run as modules of `java`.
#
#   docker run --rm --network none \
#     -v "$PWD/platform/keycloak/extensions/fapi-consent:/src:ro" \
#     --entrypoint /bin/bash quay.io/keycloak/keycloak:<version> /src/build.sh > fapi-consent.jar
#
# The archive goes to standard output and nothing else does; every message goes to standard
# error. The source folder is only read. Exit codes: 0 built and every unit test passed;
# 1 a compile error or a failed unit test; 2 the script cannot run here.
set -euo pipefail
export LC_ALL=C

src="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
lib=/opt/keycloak/lib/lib

if [ -t 1 ]; then
  echo "build.sh: standard output is a terminal; redirect it to the archive file" >&2
  exit 2
fi
if [ ! -d "$lib/main" ]; then
  echo "build.sh: $lib/main is missing; run this inside the Keycloak image" >&2
  exit 2
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/classes" "$work/test-classes"

classpath="$(find "$lib" -name '*.jar' | sort | tr '\n' ':')"
javac() { java -m jdk.compiler/com.sun.tools.javac.Main "$@"; }
jartool() { java -m jdk.jartool/sun.tools.jar.Main "$@"; }

echo "build.sh: Keycloak $(find "$lib/main" -name 'org.keycloak.keycloak-services-*.jar' | sed -E 's/.*services-(.*)\.jar/\1/')" >&2

mapfile -t main_sources < <(find "$src/src/main/java" -name '*.java' | sort)
mapfile -t test_sources < <(find "$src/src/test/java" -name '*.java' | sort)

echo "build.sh: compiling ${#main_sources[@]} source files" >&2
javac -encoding UTF-8 -Xlint:all,-processing,-serial -Werror \
  -cp "$classpath" -d "$work/classes" "${main_sources[@]}" 1>&2
cp -R "$src/src/main/resources/." "$work/classes/"

echo "build.sh: compiling and running ${#test_sources[@]} test source files" >&2
javac -encoding UTF-8 -cp "$classpath:$work/classes" -d "$work/test-classes" "${test_sources[@]}" 1>&2
java -cp "$classpath:$work/classes:$work/test-classes" io.openova.keycloak.fapi.UnitTests 1>&2

jartool --create --date=2026-01-01T00:00:00Z --file "$work/fapi-consent.jar" -C "$work/classes" . 1>&2
echo "build.sh: fapi-consent.jar $(wc -c < "$work/fapi-consent.jar") bytes" >&2
cat "$work/fapi-consent.jar"
