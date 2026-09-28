# Event Booking Project Instructions

This is a complete Dex `go-react-v1` application. Read
`.superverse/template.json` and `openapi/openapi.yaml`, then load the installed
`dex-app-builder` skill through the current coding-agent host before changing
product behavior. Superverse Coding Sandbox preinstalls a pinned Dex Skills
release; external developers install the released Dex plugin in their coding
agent. Never assume a fixed skill path. This repository must not vendor, clone,
or initialize a project-local copy.

This product uses a custom UI for attendee registration, payment status, private
QR tickets, and globally usable phone-camera check-in. Dex Web owns event and
registration operations. Keep the public UI on the generated OpenAPI client and
do not duplicate the business state machine in React.

`openapi/openapi.yaml` is the only HTTP contract source. Never edit files below
`internal/api/generated` or `web/src/api/generated` by hand. Change the spec,
run `make generate`, and update server, UI, integration, and E2E coverage in the
same change. Both generated directories are ignored local build outputs; never
add them to Git or include them in a pull request.

The Dex Flows have stable Step, Attribute, Channel, and RPC identities. Keep
external effects in `Execute`; `WaitFor` methods only declare Channels or
Timers. Register every durable primitive in each Flow persistence schema.
Preserve open-Flow compatibility unless the user explicitly requests a
migration.

Both `internal/process/inventory.go` and `internal/process/registration.go` are
Dex Web v2 / FDG 2.0 definitions. Keep their indexed Attributes,
`GetDexSummary`, `GetDexDisplay`, Action RPCs, directives, input structs, and
Dex control flow. Every Step has exactly one group and explanation. Run
`make check-fdg-v2`; never fall back to rendering schema v1.

Use only the released Stripe and Gmail Dex connectors for provider I/O. Keep
Stripe webhook verification on `/connectors/stripe/webhook`, never store bank
details, and never place attendee PII in QR tokens or Stripe metadata. Dex is
the only business-state store; do not add a database, ORM, or cache.

After each edit batch, run the narrowest relevant Make target. Before calling
`commit_and_push`, run `make check` successfully and include it in verification.
If `make check` fails or cannot run, report `blocked=true`. Do not weaken, skip,
or delete a failing check.

Stable commands are `make bootstrap`, `make generate`, `make check-fdg-v2`,
`make test-unit`, `make test-integration`, `make test-e2e`, `make build`,
`make dev`, and `make check`.

`make bootstrap`, `npm ci`, and `go mod download` may restore dependencies
already declared by the committed manifests and lockfiles. Before adding or
upgrading a project dependency, verify that the standard library and existing
dependencies cannot satisfy explicit requested behavior. Pin the selected
version, update the manifest and lockfile together, explain why it is needed,
and run `make check`. Do not add convenience-only dependencies, perform
unrelated upgrades or audit auto-fixes such as `npm audit fix`, install global
or operating-system packages, or run remote installation scripts.

`.github/workflows/update-dex-dependencies.yml` and
`scripts/update-dex-dependencies.py` own scheduled Dex Go SDK, Dex Server, Dex
and Dex CLI release updates. Keep their stable-release checks, fixed automation
branch, template-version bump, contract updates, and explicit CI dispatch
aligned. The updater creates or refreshes a pull request; it never merges one.

Every pull request advances `templateVersion` in
`.superverse/template.json`. After Template CI passes on `main`, CI publishes
that exact commit as `v<templateVersion>`. Never move or reuse a template tag.
Dex Skills discovers the release and opens its own baseline pull request. Once
the matching Dex Skills release exists, Superverse advances both immutable
release pins in one pull request.

Use Vitest mocks of the generated client for isolated loading, failure, and
terminal UI states. When a browser-only edge case cannot be reached
economically, use test-local Playwright request interception. Do not add an
application mock server, a second business state machine, or user-visible Mock
Controls. Mock evidence never replaces real Dex integration and E2E tests.

When structure, commands, or required tooling changes, update this file,
`.superverse/template.json`, `README.md`, and contract tests together. Do not
maintain a separate static repository map.
