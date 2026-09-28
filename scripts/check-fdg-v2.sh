#!/usr/bin/env bash
set -euo pipefail

dexcli_binary="${DEXCLI:-dexcli}"
go_cache="${GOCACHE:-${TMPDIR:-/tmp}/event-booking-go-build}"
flow_sources=(internal/process/capacity.go internal/process/flow.go)
temporary_files=()

cleanup() {
  if (( ${#temporary_files[@]} > 0 )); then
    rm -f -- "${temporary_files[@]}"
  fi
}
trap cleanup EXIT

mkdir -p "${go_cache}"

for source in "${flow_sources[@]}"; do
  flow_name="$(basename "${source}" .go)"
  output_base="$(mktemp "${TMPDIR:-/tmp}/event-booking-${flow_name}-fdg-v2.XXXXXX")"
  output_json="${output_base}.json"
  temporary_files+=("${output_base}" "${output_json}")

  GOCACHE="${go_cache}" "${dexcli_binary}" visualize "${source}" \
    --schema-version 2.0 \
    --json \
    --out "${output_base}"

  python3 - "${output_json}" "${source}" <<'PY'
import json
import sys

path, source = sys.argv[1:]
document = json.load(open(path))
if document.get("valid") is not True:
    diagnostics = json.dumps(document.get("diagnostics", []), indent=2)
    raise SystemExit(f"FDG 2.0 graph for {source} is invalid:\n{diagnostics}")
if document.get("diagnostics"):
    raise SystemExit(f"FDG 2.0 graph for {source} has diagnostics: {document['diagnostics']}")
print(f"validated FDG 2.0 graph: {source}")
PY
done
