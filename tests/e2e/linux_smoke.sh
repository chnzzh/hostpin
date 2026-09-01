#!/bin/sh
set -eu

test_root=${1:?usage: linux_smoke.sh TEST_ROOT}
server_binary="$test_root/hostpin-server"
agent_binary="$test_root/hostpin-agent"
installed_agent="$test_root/hostpin-agent-installed"
installed_probe_agent="$test_root/hostpin-probe-agent-installed"
data_dir="$test_root/data"
agent_config="$test_root/agent.json"
probe_config="$test_root/probe-agent.json"
server_log="$test_root/server.log"
agent_log="$test_root/agent.log"
probe_agent_log="$test_root/probe-agent.log"
release_log="$test_root/release-server.log"
release_dir="$test_root/releases"
cookie_jar="$test_root/admin.cookies"
base_url=${HOSTPIN_E2E_URL:-http://127.0.0.1:18082}
listen_address=${HOSTPIN_E2E_LISTEN:-127.0.0.1:18082}
release_port=${HOSTPIN_RELEASE_TEST_PORT:-18083}
server_pid=
agent_pid=
probe_agent_pid=
release_pid=

cleanup() {
  if [ -n "$probe_agent_pid" ]; then kill "$probe_agent_pid" 2>/dev/null || true; wait "$probe_agent_pid" 2>/dev/null || true; fi
  if [ -n "$agent_pid" ]; then kill "$agent_pid" 2>/dev/null || true; wait "$agent_pid" 2>/dev/null || true; fi
  if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
  if [ -n "$release_pid" ]; then kill "$release_pid" 2>/dev/null || true; wait "$release_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT INT TERM

test -x "$server_binary"
test -x "$agent_binary"
mkdir -p "$data_dir" "$release_dir"
cp "$agent_binary" "$release_dir/hostpin-agent-linux-amd64"
sha256sum "$release_dir/hostpin-agent-linux-amd64" | awk '{print $1}' >"$release_dir/hostpin-agent-linux-amd64.sha256"
python3 -m http.server "$release_port" --bind 127.0.0.1 --directory "$release_dir" >"$release_log" 2>&1 &
release_pid=$!
release_ready=false
for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$release_port/hostpin-agent-linux-amd64.sha256" >/dev/null 2>&1; then release_ready=true; break; fi
  sleep 0.1
done
if [ "$release_ready" != true ]; then
  echo "Local Agent release fixture did not become ready" >&2
  tail -100 "$release_log" >&2
  exit 1
fi

HOSTPIN_LISTEN="$listen_address" \
HOSTPIN_PUBLIC_URL="$base_url" \
HOSTPIN_DATA_DIR="$data_dir" \
HOSTPIN_DB_DRIVER=sqlite \
HOSTPIN_DB_DSN="$data_dir/hostpin.db" \
HOSTPIN_GEOIP_ENABLED=false \
"$server_binary" serve --config "$test_root/missing.yaml" >"$server_log" 2>&1 &
server_pid=$!

ready=false
for _ in $(seq 1 60); do
  if curl -fsS "$base_url/readyz" >/dev/null 2>&1; then ready=true; break; fi
  sleep 0.25
done
if [ "$ready" != true ]; then
  echo "Hostpin server did not become ready" >&2
  tail -100 "$server_log" >&2
  exit 1
fi

setup_code=$(curl -sS -o "$test_root/setup.json" -w '%{http_code}' \
  -c "$cookie_jar" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"remote-smoke-password","enrollment_pin":"246810","site_name":"Hostpin Linux QA","site_description":"Remote Linux smoke test"}' \
  "$base_url/api/v1/setup")
test "$setup_code" = 201
csrf_token=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["csrf_token"])' "$test_root/setup.json")
curl -fsS "$base_url/install.sh" -o "$test_root/install.sh"
sh -n "$test_root/install.sh"
curl -fsS "$base_url/uninstall.sh" -o "$test_root/uninstall.sh"
sh -n "$test_root/uninstall.sh"
HOME="$test_root/uninstall-home" sh "$test_root/uninstall.sh" --dry-run >"$test_root/uninstall-dry-run.log"
grep -q 'nothing was changed' "$test_root/uninstall-dry-run.log"

HOSTPIN_RELEASE_BASE="http://127.0.0.1:$release_port" \
HOSTPIN_NONINTERACTIVE=1 \
HOSTPIN_PIN=246810 \
HOSTPIN_NODE_NAME=linux-smoke-edge \
HOSTPIN_NODE_GROUP=remote-qa \
HOSTPIN_NODE_REGION=Seoul \
HOSTPIN_NODE_TAGS=linux,remote,e2e \
HOSTPIN_AGENT_BINARY="$installed_agent" \
sh "$test_root/install.sh" --allow-http --no-service --config "$agent_config" >"$test_root/install.log" 2>&1

"$installed_agent" run --config "$agent_config" --log-level warn >"$agent_log" 2>&1 &
agent_pid=$!

online=false
nodes_json=
for _ in $(seq 1 60); do
  nodes_json=$(curl -fsS "$base_url/api/v1/public/nodes")
  if printf '%s' "$nodes_json" | grep -q '"name":"linux-smoke-edge"' && printf '%s' "$nodes_json" | grep -q '"online":true'; then
    online=true
    break
  fi
  sleep 0.5
done
if [ "$online" != true ]; then
  echo "Agent did not become visible and online" >&2
  printf '%s\n' "$nodes_json" >&2
  tail -100 "$agent_log" >&2
  exit 1
fi

node_id=$(printf '%s' "$nodes_json" | python3 -c 'import json,sys; print(next(item["node"]["id"] for item in json.load(sys.stdin)["data"] if item["node"]["name"] == "linux-smoke-edge"))')
history_json=$(curl -fsS "$base_url/api/v1/public/history?node_id=$node_id&hours=1")
printf '%s' "$history_json" | grep -q '"received_at"'

HOSTPIN_RELEASE_BASE="http://127.0.0.1:$release_port" \
HOSTPIN_NONINTERACTIVE=1 \
HOSTPIN_PIN=246810 \
HOSTPIN_NODE_NAME=linux-smoke-router \
HOSTPIN_NODE_GROUP=remote-qa \
HOSTPIN_NODE_REGION='Private LAN' \
HOSTPIN_NODE_TAGS=linux,router,nat,e2e \
HOSTPIN_PROBE_PUBLIC=true \
HOSTPIN_AGENT_BINARY="$installed_probe_agent" \
sh "$test_root/install.sh" --probe-node --allow-http --no-service --config "$probe_config" >"$test_root/probe-install.log" 2>&1

python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); assert data["role"] == "probe", data' "$probe_config"
target_payload=$(printf '{"name":"Linux smoke route","type":"tcp","target":"127.0.0.1:18082","target_node_id":"%s","interval_seconds":5,"timeout_seconds":2,"samples":3,"public":true,"enabled":true}' "$node_id")
target_code=$(curl -sS -o "$test_root/latency-target.json" -w '%{http_code}' \
  -b "$cookie_jar" \
  -H "X-CSRF-Token: $csrf_token" \
  -H 'Content-Type: application/json' \
  -d "$target_payload" \
  "$base_url/api/v1/admin/latency/targets")
test "$target_code" = 201

"$installed_probe_agent" run --config "$probe_config" --log-level warn >"$probe_agent_log" 2>&1 &
probe_agent_pid=$!

latency_online=false
latency_json=
for _ in $(seq 1 60); do
  latency_json=$(curl -fsS "$base_url/api/v1/public/latency")
  if printf '%s' "$latency_json" | grep -q '"name":"linux-smoke-router"' && \
     printf '%s' "$latency_json" | grep -q '"online":true' && \
     printf '%s' "$latency_json" | grep -q '"latency_ms"'; then
    latency_online=true
    break
  fi
  sleep 0.5
done
if [ "$latency_online" != true ]; then
  echo "Probe Node did not publish a latency result" >&2
  printf '%s\n' "$latency_json" >&2
  tail -100 "$probe_agent_log" >&2
  exit 1
fi

sleep 12
clock_ticks=$(getconf CLK_TCK)
ticks_before=$(awk '{print $14 + $15}' "/proc/$agent_pid/stat")
probe_ticks_before=$(awk '{print $14 + $15}' "/proc/$probe_agent_pid/stat")
started_at=$(date +%s)
peak_rss_kib=0
rss_kib=0
probe_peak_rss_kib=0
probe_rss_kib=0
for _ in $(seq 1 15); do
  sleep 1
  rss_kib=$(ps -o rss= -p "$agent_pid" | tr -d ' ')
  probe_rss_kib=$(ps -o rss= -p "$probe_agent_pid" | tr -d ' ')
  if [ "$rss_kib" -gt "$peak_rss_kib" ]; then peak_rss_kib=$rss_kib; fi
  if [ "$probe_rss_kib" -gt "$probe_peak_rss_kib" ]; then probe_peak_rss_kib=$probe_rss_kib; fi
done
ticks_after=$(awk '{print $14 + $15}' "/proc/$agent_pid/stat")
probe_ticks_after=$(awk '{print $14 + $15}' "/proc/$probe_agent_pid/stat")
ended_at=$(date +%s)
cpu_percent=$(awk -v delta="$((ticks_after - ticks_before))" -v hz="$clock_ticks" -v elapsed="$((ended_at - started_at))" 'BEGIN { printf "%.3f", delta / hz / elapsed * 100 }')
probe_cpu_percent=$(awk -v delta="$((probe_ticks_after - probe_ticks_before))" -v hz="$clock_ticks" -v elapsed="$((ended_at - started_at))" 'BEGIN { printf "%.3f", delta / hz / elapsed * 100 }')
if [ -z "$rss_kib" ] || [ "$rss_kib" -gt 20480 ]; then
  echo "Agent RSS ${rss_kib:-missing} KiB exceeds the 20 MiB stable target" >&2
  exit 1
fi
if [ "$peak_rss_kib" -gt 25600 ]; then
  echo "Agent peak RSS ${peak_rss_kib} KiB exceeds the 25 MiB collection target" >&2
  exit 1
fi
if [ -z "$probe_rss_kib" ] || [ "$probe_rss_kib" -gt 20480 ]; then
  echo "Probe Agent RSS ${probe_rss_kib:-missing} KiB exceeds the 20 MiB stable target" >&2
  exit 1
fi
if [ "$probe_peak_rss_kib" -gt 25600 ]; then
  echo "Probe Agent peak RSS ${probe_peak_rss_kib} KiB exceeds the 25 MiB target" >&2
  exit 1
fi
if ! awk -v value="$cpu_percent" 'BEGIN { exit !(value <= 1.0) }'; then
  echo "Agent stable CPU ${cpu_percent}% exceeds 1% of one core" >&2
  exit 1
fi
if ! awk -v value="$probe_cpu_percent" 'BEGIN { exit !(value <= 1.0) }'; then
  echo "Probe Agent stable CPU ${probe_cpu_percent}% exceeds 1% of one core" >&2
  exit 1
fi

latencies=
for _ in $(seq 1 25); do
  value=$(curl -sS -o /dev/null -w '%{time_total}' "$base_url/api/v1/public/nodes")
  latencies="$latencies$value
"
done
p95=$(printf '%s' "$latencies" | sed '/^$/d' | sort -n | awk 'NR==24{print; exit}')
if ! awk -v value="$p95" 'BEGIN { exit !(value < 0.300) }'; then
  echo "Latest status API p95 ${p95}s exceeds 300ms" >&2
  exit 1
fi
if grep -E 'panic|fatal|"level":"ERROR"|level=ERROR' "$server_log" "$agent_log" "$probe_agent_log" "$release_log"; then
  echo "Hostpin runtime log contains a blocking error" >&2
  exit 1
fi

printf '{"status":"ok","node_id":"%s","agent_rss_kib":%s,"agent_peak_rss_kib":%s,"agent_cpu_percent":"%s","probe_rss_kib":%s,"probe_peak_rss_kib":%s,"probe_cpu_percent":"%s","latest_api_p95_seconds":%s}\n' \
  "$node_id" "$rss_kib" "$peak_rss_kib" "$cpu_percent" "$probe_rss_kib" "$probe_peak_rss_kib" "$probe_cpu_percent" "$p95"
