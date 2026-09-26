#!/usr/bin/env bash
# Walks through the docker-compose stack step by step: JWT auth, rate
# limiting (429, shared across two gatekeeper instances via Redis), an
# upstream going unhealthy, the circuit breaker opening, and a hot
# reload picked up from an edited config file on disk.
#
# Run `docker compose up -d --build` first (or `make up` in the
# background) and give it a few seconds to become healthy.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GW1=http://localhost:8080
GW2=http://localhost:8081
ADMIN1=http://localhost:9190
ADMIN2=http://localhost:9191
JWKSMOCK=http://localhost:8000
ADMIN_TOKEN=demo-admin-token

step() {
  echo
  echo "=================================================================="
  echo "== $1"
  echo "=================================================================="
}

pause() {
  read -r -p "-- press enter to continue --" _ || true
}

step "1. Waiting for gatekeeper-1 to report ready"
for _ in $(seq 1 30); do
  if curl -sf "$ADMIN1/readyz" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -s "$ADMIN1/readyz" -o /dev/null -w "readyz: %{http_code}\n"

step "2. JWT auth: /api/secure/ without a token is rejected"
curl -s -o /dev/null -w "no token -> %{http_code}\n" "$GW1/api/secure/rooms"

echo
echo "Fetching a token from the jwksmock demo identity provider..."
TOKEN=$(curl -s "$JWKSMOCK/token?sub=demo-user")
echo "token: ${TOKEN:0:40}..."
curl -s -o /dev/null -w "with token -> %{http_code}\n" -H "Authorization: Bearer $TOKEN" "$GW1/api/secure/rooms"
pause

step "3. Rate limiting: rps=2 burst=5 on /api/demo/, shared via Redis"
echo "Bursting 8 requests at gatekeeper-1 (only the same client IP counts):"
for i in $(seq 1 8); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "$GW1/api/demo/rooms")
  echo "  request $i -> $code"
done
echo
echo "The limit is shared: gatekeeper-2 sees the same client as already over budget"
curl -s -o /dev/null -w "gatekeeper-2 -> %{http_code} (expect 429 - same Redis-backed limit)\n" "$GW2/api/demo/rooms"
pause

step "4. Waiting out the rate limit window before continuing"
sleep 3

step "5. upstream-3 is deliberately flaky (ERROR_RATE=0.3, DELAY=100ms) - watch it get evicted"
echo "Current target health per gatekeeper-1:"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$ADMIN1/admin/upstreams" | (command -v jq >/dev/null && jq . || cat)
echo
echo "Sending traffic for a while so active/passive checks have a chance to catch it..."
for i in $(seq 1 20); do
  curl -s -o /dev/null "$GW1/api/demo/rooms" || true
  sleep 0.3
done
echo
echo "Target health now:"
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$ADMIN1/admin/upstreams" | (command -v jq >/dev/null && jq . || cat)
pause

step "6. Circuit breaker state (see breaker_state above; open once failures cross the threshold)"
echo "(gateway_circuit_state is also on Prometheus/Grafana: http://localhost:3000)"
pause

step "7. Hot reload: bumping the rate limit burst in the mounted config file"
CONFIG="$ROOT/docker/gatekeeper.yaml"
cp "$CONFIG" "$CONFIG.bak"
sed -i 's/burst: 5/burst: 50/' "$CONFIG"
echo "Edited $CONFIG (burst: 5 -> 50). Waiting for fsnotify + debounce to pick it up..."
sleep 2
echo "Bursting again - should no longer 429 as quickly:"
for i in $(seq 1 8); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "$GW1/api/demo/rooms")
  echo "  request $i -> $code"
done
echo
echo "Restoring the original config and triggering a reload via the admin API..."
mv "$CONFIG.bak" "$CONFIG"
curl -s -X POST -H "Authorization: Bearer $ADMIN_TOKEN" "$ADMIN1/admin/reload"
echo

step "Done. Grafana: http://localhost:3000  Jaeger: http://localhost:16686  Prometheus: http://localhost:9090"
