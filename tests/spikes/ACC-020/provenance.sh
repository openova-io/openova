#!/usr/bin/env bash
# Lab test ACC-020, item l: where the browser library is published.
# Maven Central (the Apache release) and the npm registry. Throwaway.
set -uo pipefail
EV="${GITHUB_WORKSPACE:-$PWD}/evidence"
mkdir -p "$EV" /tmp/prov && cd /tmp/prov || exit 1
M=https://repo1.maven.org/maven2/org/apache/guacamole/guacamole-common-js
V=1.6.0

echo "=== Maven Central: org.apache.guacamole:guacamole-common-js"
curl -sSfL "$M/maven-metadata.xml" | tee maven-metadata.xml | grep -E '<(latest|release|lastUpdated)>'
echo "versions: $(grep -o '<version>[^<]*' maven-metadata.xml | sed 's/<version>//' | tr '\n' ' ')"
echo "--- files published next to the $V artifact (HTTP status):"
for f in "guacamole-common-js-$V.zip" "guacamole-common-js-$V.zip.sha1" "guacamole-common-js-$V.zip.md5" \
         "guacamole-common-js-$V.zip.sha256" "guacamole-common-js-$V.zip.sha512" "guacamole-common-js-$V.zip.asc" \
         "guacamole-common-js-$V.pom" "guacamole-common-js-$V.jar"; do
  echo "  $f -> $(curl -s -o /dev/null -w '%{http_code}' -I "$M/$V/$f")"
done
curl -sSfL -o lib.zip "$M/$V/guacamole-common-js-$V.zip"
curl -sSfL -o lib.zip.sha1 "$M/$V/guacamole-common-js-$V.zip.sha1"
curl -sSfL -o lib.zip.md5 "$M/$V/guacamole-common-js-$V.zip.md5"
curl -sSfL -o lib.zip.asc "$M/$V/guacamole-common-js-$V.zip.asc"
echo "published sha1: $(awk '{print $1}' lib.zip.sha1)  computed: $(sha1sum lib.zip | awk '{print $1}')"
echo "published md5:  $(awk '{print $1}' lib.zip.md5)  computed: $(md5sum lib.zip | awk '{print $1}')"
echo "computed sha256 of the zip: $(sha256sum lib.zip | awk '{print $1}')"
echo "--- zip contents:"
unzip -l lib.zip | sed 's/^/  /'
mkdir -p x && unzip -o -q lib.zip -d x
for js in $(find x -name '*.js' | sort); do echo "  ${js#x/}: $(wc -c < "$js") bytes, sha256 $(sha256sum "$js" | awk '{print $1}')"; done
echo "--- OpenPGP signature against the Apache Guacamole KEYS file:"
curl -sSfL -o KEYS https://downloads.apache.org/guacamole/KEYS && gpg --batch --quiet --import KEYS 2>/dev/null
gpg --batch --verify lib.zip.asc lib.zip 2>&1 | sed 's/^/  /'

echo
echo "=== npm registry: package guacamole-common-js"
curl -sSfL https://registry.npmjs.org/guacamole-common-js -o npm.json
jq -c '{name, "dist-tags", maintainers, author, repository, homepage, license, created: .time.created, modified: .time.modified}' npm.json
echo "versions: $(jq -r '.versions | keys_unsorted | join(" ")' npm.json)"
LATEST=$(jq -r '."dist-tags".latest' npm.json)
jq -c --arg v "$LATEST" '.versions[$v] | {version, _npmUser, maintainers, repository, homepage, description, dist: {tarball: .dist.tarball, integrity: .dist.integrity}, published: null}' npm.json
echo "latest $LATEST published at $(jq -r --arg v "$LATEST" '.time[$v]' npm.json)"
curl -sSfL -o npm.tgz "$(jq -r --arg v "$LATEST" '.versions[$v].dist.tarball' npm.json)"
mkdir -p npmx && tar -xzf npm.tgz -C npmx
echo "--- npm tarball files:"
find npmx -type f | sort | while read -r f; do echo "  ${f#npmx/}: $(wc -c < "$f") bytes, sha256 $(sha256sum "$f" | awk '{print $1}')"; done
echo "--- other packages on npm that carry the name:"
curl -sSfL 'https://registry.npmjs.org/-/v1/search?text=guacamole-common-js&size=10' \
  | jq -c '.objects[].package | {name, version, publisher: .publisher.username, maintainers: [.maintainers[]?.username], links: .links.repository}'
echo "--- npm packages under an apache scope:"
for n in @apache/guacamole-common-js @apache-guacamole/guacamole-common-js; do
  echo "  $n -> HTTP $(curl -s -o /dev/null -w '%{http_code}' "https://registry.npmjs.org/${n/\//%2f}")"
done
