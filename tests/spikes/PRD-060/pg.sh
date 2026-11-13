#!/usr/bin/env bash
# Lab test PRD-060: start or stop the PostgreSQL container the experiment uses.
# Runs only inside the CI job. The container listens on 127.0.0.1:5432 of the
# runner (host network); nothing is published anywhere else.
#
#   pg.sh start <name> <image> <datadir> <profile>
#   pg.sh stop  <name>
#
# Profiles:
#   facts   no tuning, used once to read the image's extension facts
#   runner  sized for one million 1,024-dimension chunks on a 4-CPU, 16 GB
#           runner (memory for the parallel HNSW build, large cache)
#   chart   the platform chart's own defaults: 500m CPU, 512Mi memory and the
#           GUCs of platform/postgres/chart/values.yaml (instance.parameters)
set -euo pipefail

action="$1"; name="$2"
if [[ "$action" == stop ]]; then
  docker stop -t 300 "$name" >/dev/null 2>&1 || true
  docker rm -f "$name" >/dev/null 2>&1 || true
  exit 0
fi
image="$3"; data="$4"; profile="$5"

bindir="$(docker run --rm --entrypoint sh "$image" -c 'ls -d /usr/lib/postgresql/*/bin | sort -V | tail -1')"
uid="$(docker run --rm --entrypoint id "$image" -u postgres)"
gid="$(docker run --rm --entrypoint id "$image" -g postgres)"
echo "image=$image bindir=$bindir uid=$uid gid=$gid profile=$profile"

sudo mkdir -p "$data"
sudo chown "$uid:$gid" "$data"
if ! sudo test -s "$data/db/PG_VERSION"; then
  # C locale and UTF8, as CloudNativePG's default initdb does.
  docker run --rm --user "$uid:$gid" -v "$data:/pgdata" --entrypoint "$bindir/initdb" "$image" \
    -D /pgdata/db -U postgres --auth=trust --encoding=UTF8 --no-locale >/dev/null
fi

# Non-sizing GUCs of the chart, used by every profile.
common=(-c listen_addresses=127.0.0.1 -c port=5432 -c unix_socket_directories=/tmp
        -c max_connections=100 -c log_statement=ddl -c log_min_duration_statement=1000)
case "$profile" in
  facts)
    opts=(); params=() ;;
  runner)
    opts=(--shm-size=8g)
    params=(-c shared_buffers=2GB -c effective_cache_size=12GB -c work_mem=4MB
            -c maintenance_work_mem=7GB -c max_parallel_maintenance_workers=3
            -c max_parallel_workers=4 -c max_worker_processes=8
            -c wal_level=minimal -c max_wal_senders=0 -c fsync=off -c synchronous_commit=off
            -c full_page_writes=off -c max_wal_size=8GB -c checkpoint_timeout=30min
            -c max_locks_per_transaction=256) ;;
  chart)
    opts=(--memory=512m --memory-swap=512m --cpus=0.5 --shm-size=256m)
    params=(-c shared_buffers=64MB -c effective_cache_size=192MB -c work_mem=4MB
            -c maintenance_work_mem=64MB) ;;
  *) echo "unknown profile $profile" >&2; exit 2 ;;
esac

docker run -d --name "$name" --network host --user "$uid:$gid" -v "$data:/pgdata" "${opts[@]}" \
  --entrypoint "$bindir/postgres" "$image" -D /pgdata/db "${common[@]}" "${params[@]}" >/dev/null

for _ in $(seq 1 180); do
  if docker exec "$name" "$bindir/pg_isready" -h 127.0.0.1 -p 5432 -q; then
    echo "postgres ready ($profile)"
    docker inspect --format 'memory={{.HostConfig.Memory}} nanocpus={{.HostConfig.NanoCpus}} shm={{.HostConfig.ShmSize}}' "$name"
    exit 0
  fi
  sleep 1
done
docker logs "$name" 2>&1 | tail -50
exit 1
