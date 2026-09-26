#!/usr/bin/env bash
# Compares direct-to-upstream vs through-gatekeeper latency/throughput
# with vegeta, at a rate the reader supplies (default: 5000/s, 10s),
# and repeats through-gatekeeper with GOMAXPROCS=1 for the single-core
# figure the TZ asks for. Prints vegeta's own report for each run -
# read those numbers into the README table by hand; this script
# doesn't parse/aggregate them.
#
# Requires: vegeta (go install github.com/tsenart/vegeta@latest),
# and gatekeeper/testupstream already built (make build).
set -euo pipefail

RATE="${1:-5000}"
DURATION="${2:-10s}"
WORKERS="${3:-100}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_ADDR=":19001"
GATEWAY_ADDR="http://localhost:18080"
LOADTEST_CONFIG="$ROOT/configs/loadtest.yaml"

pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

echo "==> building binaries"
go build -o "$ROOT/bin/gatekeeper" "$ROOT/cmd/gatekeeper"
go build -o "$ROOT/bin/testupstream" "$ROOT/cmd/testupstream"

run_attack() {
  local label="$1" url="$2"
  echo
  echo "=== $label (rate=${RATE}/s duration=${DURATION} workers=${WORKERS}) ==="
  echo "GET $url" | vegeta attack -duration="$DURATION" -rate="${RATE}/1s" -workers="$WORKERS" -max-workers="$WORKERS" | vegeta report
}

LOG_DIR="$(mktemp -d)"
echo "==> starting testupstream on $BACKEND_ADDR (log: $LOG_DIR/testupstream.log)"
ADDR="$BACKEND_ADDR" NAME=loadtest-backend "$ROOT/bin/testupstream" >"$LOG_DIR/testupstream.log" 2>&1 &
pids+=($!)
sleep 1

echo "==> starting gatekeeper (all cores) on $GATEWAY_ADDR (log: $LOG_DIR/gatekeeper.log)"
LOADTEST_ADMIN_TOKEN=loadtest "$ROOT/bin/gatekeeper" --config="$LOADTEST_CONFIG" >"$LOG_DIR/gatekeeper.log" 2>&1 &
pids+=($!)
sleep 1

run_attack "direct to upstream, all cores"        "http://localhost${BACKEND_ADDR}/x"
run_attack "through gatekeeper, all cores"          "$GATEWAY_ADDR/api/demo/x"

echo "==> restarting gatekeeper with GOMAXPROCS=1"
kill "${pids[1]}"
wait "${pids[1]}" 2>/dev/null || true
GOMAXPROCS=1 LOADTEST_ADMIN_TOKEN=loadtest "$ROOT/bin/gatekeeper" --config="$LOADTEST_CONFIG" >"$LOG_DIR/gatekeeper-1core.log" 2>&1 &
pids[1]=$!
sleep 1

run_attack "through gatekeeper, GOMAXPROCS=1"       "$GATEWAY_ADDR/api/demo/x"

echo
echo "==> done (gatekeeper/testupstream logs kept at $LOG_DIR)."
echo "==> copy the p50/p99/throughput numbers above into README's load test table."
