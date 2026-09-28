# Event Booking Application Instructions

This is a complete Dex `go-react-v1` event-registration application. Read
`.superverse/template.json` and `openapi/openapi.yaml`, then load the installed
`dex-app-builder` skill through the current coding-agent host before changing
product behavior. Superverse Coding Sandbox preinstalls a pinned Dex Skills
release; external developers install the released Dex plugin in their coding
agent. Never assume a fixed skill path. This repository must not vendor, clone,
or initialize a project-local copy.

The attendee UI, electronic ticket, and staff QR check-in are required product
surfaces. Preserve the mock-first review loop as well as the real Dex paths.
The committed event details are intentionally TBD and registration is closed
by default. Do not invent a date, venue, price, policy, or organizer identity.

`openapi/openapi.yaml` is the only HTTP contract source. Never edit files below
`internal/api/generated` or `web/src/api/generated` by hand. Change the spec,
run `make generate`, and update the server, UI, mock, integration, and E2E
coverage in the same change.

The application has two durable Flows: one long-lived capacity Flow and one
registration Flow per attendee. Capacity reservation and release must remain
atomic. Keep the hard maximum configurable and covered by the concurrent
350-request/300-acceptance integration test. Do not issue a ticket for an ACH
payment that is merely pending; require asynchronous success or a Checkout
Session whose payment status is paid. Failed and expired payments release
capacity. Provider event IDs and check-ins stay idempotent.

Keep bank data and provider credentials out of Flow input, state, logs, Stripe
metadata, URLs, and API responses. Status and ticket URLs use separate signed
capabilities. Check-in requires the staff token and must compare secrets in
constant time. `/__mock__/` remains unavailable in production.

Every Flow is a Dex Web v2 / FDG 2.0 definition. Keep indexed Attributes,
`GetDexSummary`, `GetDexDisplay`, Action RPCs, typed connector steps and
triggers, directives, input structs, and direct Dex control-flow decisions in
the Flow source. Every custom Step has exactly one group and explanation.
Validate every Flow file. Run `make check-fdg-v2`; never fall back to rendering
schema v1 or suppress unexpected diagnostics.

The Stripe connector is an independent released module. A temporary local
connector `replace` may exist only in ignored `go.work` while developing the
connector. Never commit a local path, branch, or pseudo-version. Before
publishing application changes, pin an exact official connector release and
require a diagnostic-free FDG run.

After each edit batch, run the narrowest relevant Make target. Before
publishing, run `make check` successfully. If it fails or cannot run, report
the exact release blocker; do not weaken, skip, or delete a failing check.

Stable commands are `make bootstrap`, `make generate`, `make check-generated`,
`make check-fdg-v2`, `make test-unit`, `make test-integration`,
`make test-e2e`, `make test-mock-e2e`, `make build`, `make dev`, `make mock`,
and `make check`.

`make bootstrap`, `npm ci`, and `go mod download` may restore dependencies
already declared by the committed manifests and lockfiles. Before adding or
upgrading a dependency, verify that the standard library and existing
dependencies cannot meet the requirement. Pin the version, update manifest and
lockfile together, and run `make check`. Do not run unrelated upgrades or
audit auto-fixes.

`.github/workflows/update-dex-dependencies.yml` and
`scripts/update-dex-dependencies.py` own scheduled Dex Go SDK, Dex Server, Dex
and Dex CLI stable-release updates. Keep their automation branch, contract
updates, version bump, and explicit CI dispatch aligned. The updater creates or
refreshes a pull request and never merges it.

Every pull request advances `templateVersion` in
`.superverse/template.json`. Never move or reuse a published version tag.

`make mock` starts the Go in-memory mock API and Vite HMR without Dex. Keep mock
behavior behind `cmd/mock-server` and `/__mock__/`. Mock verification does not
replace the real Dex integration and E2E suites.

When structure, commands, or required tooling changes, update this file,
`.superverse/template.json`, `README.md`, and contract tests together.
