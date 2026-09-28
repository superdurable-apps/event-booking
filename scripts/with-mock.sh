#!/usr/bin/env bash
set -euo pipefail

mock_directory="$(mktemp -d "${TMPDIR:-/tmp}/event-booking-mock.XXXXXX")"
api_host="${MOCK_API_HOST:-127.0.0.1}"
api_port="${MOCK_API_PORT:-18081}"
web_host="${MOCK_WEB_HOST:-0.0.0.0}"
web_port="${MOCK_WEB_PORT:-8080}"
api_log="${mock_directory}/api.log"
web_log="${mock_directory}/web.log"

cleanup() {
  exit_code=$?
  trap - EXIT INT TERM
  if [[ -n "${web_pid:-}" ]]; then kill "${web_pid}" 2>/dev/null || true; wait "${web_pid}" 2>/dev/null || true; fi
  if [[ -n "${api_pid:-}" ]]; then kill "${api_pid}" 2>/dev/null || true; wait "${api_pid}" 2>/dev/null || true; fi
  if [[ "${exit_code}" -eq 0 || "${exit_code}" -eq 130 || "${exit_code}" -eq 143 ]]; then
    rm -rf -- "${mock_directory}"
  else
    cat "${api_log}" >&2 || true
    cat "${web_log}" >&2 || true
    echo "Mock artifacts: ${mock_directory}" >&2
  fi
  exit "${exit_code}"
}
trap cleanup EXIT INT TERM

MOCK_API_ADDRESS="${api_host}:${api_port}" \
MOCK_PUBLIC_BASE_URL="http://127.0.0.1:${web_port}" \
go run ./cmd/mock-server >"${api_log}" 2>&1 &
api_pid=$!

deadline=$((SECONDS + 45))
until curl --fail --silent "http://${api_host}:${api_port}/api/health" >/dev/null; do
  if ! kill -0 "${api_pid}" 2>/dev/null || (( SECONDS >= deadline )); then
    cat "${api_log}" >&2
    exit 1
  fi
  sleep 0.1
done

VITE_MOCK_MODE=true \
VITE_MOCK_API_TARGET="http://${api_host}:${api_port}" \
npm --prefix web run mock -- --host "${web_host}" --port "${web_port}" --strictPort >"${web_log}" 2>&1 &
web_pid=$!

deadline=$((SECONDS + 45))
until curl --fail --silent "http://127.0.0.1:${web_port}" >/dev/null; do
  if ! kill -0 "${web_pid}" 2>/dev/null || (( SECONDS >= deadline )); then
    cat "${web_log}" >&2
    exit 1
  fi
  sleep 0.1
done

echo "Mock UI: http://127.0.0.1:${web_port}"
if (( $# > 0 )); then
  "$@"
else
  wait "${web_pid}"
fi
