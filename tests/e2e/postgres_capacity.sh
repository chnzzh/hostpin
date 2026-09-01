#!/usr/bin/env bash
set -euo pipefail

work_dir=${HOSTPIN_CAPACITY_DIR:?set HOSTPIN_CAPACITY_DIR to an isolated directory}
container=${HOSTPIN_CAPACITY_CONTAINER:-hostpin-capacity-postgres}
port=${HOSTPIN_CAPACITY_PORT:-15433}
nodes=${HOSTPIN_CAPACITY_NODES:-1000}
duration=${HOSTPIN_CAPACITY_DURATION:-65s}
server_pid=""

if [[ ! -x "$work_dir/hostpin-server" || ! -x "$work_dir/hostpin-load" ]]; then
  echo "hostpin-server and hostpin-load must be executable in $work_dir" >&2
  exit 1
fi
if docker container inspect "$container" >/dev/null 2>&1; then
  echo "refusing to replace existing container $container" >&2
  exit 1
fi
if docker image inspect postgres:16-alpine >/dev/null 2>&1; then
  printf 'true\n' >"$work_dir/postgres-image-preexisting"
else
  printf 'false\n' >"$work_dir/postgres-image-preexisting"
fi

cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --rm -d --name "$container" \
  -e POSTGRES_DB=hostpin_capacity \
  -e POSTGRES_USER=hostpin \
  -e POSTGRES_PASSWORD=hostpin-capacity \
  -p "127.0.0.1:${port}:5432" \
  postgres:16-alpine >/dev/null
postgres_ready=false
for _ in {1..80}; do
  if docker exec "$container" pg_isready -U hostpin -d hostpin_capacity >/dev/null 2>&1; then
	postgres_ready=true
    break
  fi
  sleep .25
done
if [[ "$postgres_ready" != true ]]; then
  docker logs --tail 100 "$container" >&2 || true
  echo "PostgreSQL did not become ready" >&2
  exit 1
fi

mkdir -p "$work_dir/data"
HOSTPIN_LISTEN=127.0.0.1:18091 \
HOSTPIN_PUBLIC_URL=http://127.0.0.1:18091 \
HOSTPIN_DATA_DIR="$work_dir/data" \
HOSTPIN_DB_DRIVER=postgres \
HOSTPIN_DB_DSN="postgres://hostpin:hostpin-capacity@127.0.0.1:${port}/hostpin_capacity?sslmode=disable" \
HOSTPIN_GEOIP_ENABLED=false \
"$work_dir/hostpin-server" serve --config "$work_dir/missing.yaml" >"$work_dir/server.log" 2>&1 &
server_pid=$!
server_ready=false
for _ in {1..80}; do
  if curl -fsS http://127.0.0.1:18091/readyz >/dev/null; then
	server_ready=true
    break
  fi
  sleep .25
done
if [[ "$server_ready" != true ]]; then
  cat "$work_dir/server.log" >&2 || true
  echo "Hostpin did not become ready" >&2
  exit 1
fi
curl -fsS -X POST http://127.0.0.1:18091/api/v1/setup \
  -H 'Content-Type: application/json' \
  --data '{"username":"admin","password":"capacity-test-password","enrollment_pin":"246810","site_name":"Hostpin Capacity"}' >/dev/null

HOSTPIN_LOAD_PIN=246810 "$work_dir/hostpin-load" \
  --endpoint http://127.0.0.1:18091 \
  --nodes "$nodes" \
  --duration "$duration" \
  --interval 3s \
  --enrollment-workers 4

if grep -E 'panic|fatal|"level":"ERROR"|level=ERROR' "$work_dir/server.log"; then
  echo "server log contains a blocking error" >&2
  exit 1
fi
