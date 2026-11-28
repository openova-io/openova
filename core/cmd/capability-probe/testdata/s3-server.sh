#!/bin/sh
# The S3 server the probe's tests run against (.github/workflows/build-capability-probe.yaml).
#
#   sh testdata/s3-server.sh start <dir> [port]   start SeaweedFS 4.22 (the kit's version) with S3 over
#                                                 TLS from a private CA, then print the line
#                                                 PROBE_TEST_S3_DIR=<dir>
#   sh testdata/s3-server.sh stop                 remove the container
#
# <dir> receives: binding/ (the nine keys of object-storage v1 for an identity that may act on the
# buckets probe-bucket and probe-versioned only, ca.crt included), admin/ (an identity with Admin,
# with which the test creates the buckets) and certs/. Every key is made fresh by this run.
# CONTAINER_ENGINE (default docker) names the container engine; locally podman works the same.
set -eu
engine=${CONTAINER_ENGINE:-docker}
name=capability-probe-s3
image=docker.io/chrislusf/seaweedfs:4.22

case "${1:-}" in
stop)
  "$engine" rm -f "$name" >/dev/null
  exit 0
  ;;
start) ;;
*)
  echo "usage: $0 start <dir> [port] | stop" >&2
  exit 2
  ;;
esac
dir=${2:?usage: $0 start <dir> [port]}
port=${3:-8333}
mkdir -p "$dir/certs" "$dir/binding" "$dir/admin"
chmod 755 "$dir/certs"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout "$dir/certs/tls.key" -out "$dir/certs/tls.crt" 2>/dev/null
admin_key=$(openssl rand -hex 10 | tr a-f A-F)
admin_secret=$(openssl rand -hex 20)
probe_key=$(openssl rand -hex 10 | tr a-f A-F)
probe_secret=$(openssl rand -hex 20)
cat >"$dir/certs/s3.json" <<EOF
{"identities": [
  {"name": "admin", "credentials": [{"accessKey": "$admin_key", "secretKey": "$admin_secret"}], "actions": ["Admin"]},
  {"name": "probe", "credentials": [{"accessKey": "$probe_key", "secretKey": "$probe_secret"}],
   "actions": ["Read:probe-bucket", "Write:probe-bucket", "List:probe-bucket", "Tagging:probe-bucket",
               "Read:probe-versioned", "Write:probe-versioned", "List:probe-versioned", "Tagging:probe-versioned"]}
]}
EOF
# The server runs as user 1000 of the image and reads these files.
chmod 644 "$dir/certs/tls.key" "$dir/certs/tls.crt" "$dir/certs/s3.json"

printf s3 >"$dir/binding/type"
printf ci-seaweedfs >"$dir/binding/provider"
printf 'https://localhost:%s' "$port" >"$dir/binding/endpoint"
printf us-east-1 >"$dir/binding/region"
printf probe-bucket >"$dir/binding/bucket"
printf true >"$dir/binding/path-style"
printf '%s' "$probe_key" >"$dir/binding/access-key-id"
printf '%s' "$probe_secret" >"$dir/binding/secret-access-key"
cp "$dir/certs/tls.crt" "$dir/binding/ca.crt"
printf '%s' "$admin_key" >"$dir/admin/access-key-id"
printf '%s' "$admin_secret" >"$dir/admin/secret-access-key"
# The image runs as user 65534 and reads the binding (the workflow's run of the built image).
chmod 755 "$dir/binding"
chmod 644 "$dir"/binding/*

"$engine" rm -f "$name" >/dev/null 2>&1 || true
# Each bucket is a collection that takes 7 volumes at its first write. The image's default
# (-volume.max=0) sizes the slots from the free disk, which on a runner fits one bucket or none;
# 40 slots of 32 MB fit every bucket of the tests on any disk.
"$engine" run -d --name "$name" -p "127.0.0.1:$port:8333" -v "$dir/certs:/certs:ro" "$image" \
  server -volume.max=40 -master.volumeSizeLimitMB=32 -s3 -s3.port=8333 -s3.config=/certs/s3.json \
  -s3.cert.file=/certs/tls.crt -s3.key.file=/certs/tls.key >/dev/null

# Any HTTP answer over TLS with this CA means the gateway is up; 000 means not yet.
i=0
while :; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$dir/certs/tls.crt" "https://localhost:$port/" || true)
  [ "$code" != 000 ] && break
  i=$((i + 1))
  if [ "$i" -ge 90 ]; then
    "$engine" logs "$name" >&2 || true
    echo "s3-server: no TLS answer on https://localhost:$port within 90 seconds" >&2
    exit 1
  fi
  sleep 1
done
echo "PROBE_TEST_S3_DIR=$dir"
