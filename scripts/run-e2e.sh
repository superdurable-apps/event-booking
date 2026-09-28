#!/usr/bin/env bash
set -euo pipefail

npm --prefix web run build
app_log="${DEX_TEST_ARTIFACT_DIR:-${TMPDIR:-/tmp}}/application.log"
connector_config="${DEX_TEST_ARTIFACT_DIR:-${TMPDIR:-/tmp}}/event-booking-connections.json"
install -m 600 internal/runtime/testdata/connections.json "${connector_config}"
DEX_CONNECTOR_CONFIG_FILE="${connector_config}" \
TICKET_SIGNING_KEY="local-e2e-ticket-signing-key-32-bytes" \
STAFF_CHECKIN_TOKEN="local-e2e-staff-token" \
PUBLIC_BASE_URL="http://127.0.0.1:${E2E_PORT:-18080}" \
PORT="${E2E_PORT:-18080}" go run ./cmd/server >"${app_log}" 2>&1 &
app_pid=$!
cleanup() { exit_code=$?; trap - EXIT INT TERM; kill "${app_pid}" 2>/dev/null || true; wait "${app_pid}" 2>/dev/null || true; exit "${exit_code}"; }
trap cleanup EXIT INT TERM
deadline=$((SECONDS + 45))
until curl --fail --silent "http://127.0.0.1:${E2E_PORT:-18080}/api/health" >/dev/null; do
  if (( SECONDS >= deadline )); then cat "${app_log}"; exit 1; fi
  sleep 0.1
done
E2E_BASE_URL="http://127.0.0.1:${E2E_PORT:-18080}" npm --prefix web run test:e2e
