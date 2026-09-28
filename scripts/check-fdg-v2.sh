#!/usr/bin/env bash
set -euo pipefail

dexcli_binary="${DEXCLI:-dexcli}"
output_inventory="$(mktemp "${TMPDIR:-/tmp}/event-inventory-fdg-v2.XXXXXX")"
output_registration="$(mktemp "${TMPDIR:-/tmp}/event-registration-fdg-v2.XXXXXX")"
cleanup() {
  rm -f -- "${output_inventory}" "${output_inventory}.json" "${output_registration}" "${output_registration}.json"
}
trap cleanup EXIT

"${dexcli_binary}" visualize internal/process/inventory.go \
  --schema-version 2.0 \
  --json \
  --out "${output_inventory}"

"${dexcli_binary}" visualize internal/process/registration.go \
  --schema-version 2.0 \
  --json \
  --out "${output_registration}"

python3 - "${output_inventory}.json" "${output_registration}.json" <<'PY'
import json
import sys

for path in sys.argv[1:]:
    document = json.load(open(path))
    if document.get("valid") is not True:
        diagnostics = json.dumps(document.get("diagnostics", []), indent=2)
        raise SystemExit(f"FDG 2.0 graph is invalid ({path}):\n{diagnostics}")
    if document.get("diagnostics"):
        raise SystemExit(f"FDG 2.0 graph has diagnostics ({path}): {document['diagnostics']}")
    print(f"validated FDG 2.0 graph: {path}")
PY
