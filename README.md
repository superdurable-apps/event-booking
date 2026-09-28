# Event booking

A Dex-powered registration site for a single event with approximately 300 attendees. It reserves capacity without overselling, creates Stripe-hosted US bank account (ACH) Checkout Sessions, emails private QR ticket links through Gmail after asynchronous payment success, and performs atomic ticket check-in.

## Architecture

- `EventInventoryFlow` is the single durable capacity ledger. Its transactional RPCs reserve, release, and mark seats paid.
- `RegistrationFlow` owns one attendee's payment, email, ticket, and check-in lifecycle.
- Stripe `checkoutSessionUpdated` events are signature-verified by the official connector before reaching the typed registration RPC.
- Gmail `sendMessage` sends the private ticket URL. The QR encodes only that HMAC-signed URL; it contains no attendee or payment data.
- Dex is the only business-state store. There is no application database, ORM, cache, or mock business server.

The participant registration, payment-status, ticket, and staff scanning pages use the generated OpenAPI client. Dex Web remains the operations interface for event and registration recovery actions.

## Local development

The repository pins Dex Server/CLI `v0.14.0`, Dex Go SDK `v0.13.1`, Stripe connector `v0.1.0`, and Gmail connector `v0.11.1`.

```sh
make bootstrap
make check
make dev
```

`make dev` expects `dexcli` on `PATH`. The app defaults to the local Dex Server at `127.0.0.1:8801` and serves on `http://127.0.0.1:8080`.

Without `DEX_CONNECTOR_CONFIG_FILE`, non-production startup uses non-routable connector endpoints so the UI and Flow graphs can be inspected safely. Provider calls will enter `needs_attention`. Configure the released connectors through Dex Web for an actual development payment/email test.

## Configuration

Event details remain TBD and are environment-configurable:

| Variable | Development default |
| --- | --- |
| `EVENT_NAME` | `Event name to be announced` |
| `EVENT_DESCRIPTION` | `Program details will be announced soon.` |
| `EVENT_STARTS_AT` | `2027-01-01T18:00:00-08:00` |
| `EVENT_TIMEZONE` | `America/Los_Angeles` |
| `EVENT_LOCATION` | `Location to be announced` |
| `EVENT_CURRENCY` | `USD` |
| `EVENT_PRICE_MINOR` | `7500` |
| `EVENT_CAPACITY` | `300` |
| `EVENT_REGISTRATION_OPEN` | `true` |
| `PUBLIC_BASE_URL` | `http://127.0.0.1:8080` |

Production additionally requires:

- `APP_ENV=production`
- `EVENT_TOKEN_SECRET` with at least 32 random bytes
- `DEX_CONNECTOR_CONFIG_FILE` containing the named `event-payments` Stripe and `event-tickets` Gmail connections, plus the `event-registration-payments` Stripe trigger binding
- an HTTPS `PUBLIC_BASE_URL`
- a trusted authentication boundary that strips caller-supplied permission headers and asserts `X-Event-Permissions`; check-in requires `checkin.scan`
- a public Stripe webhook route at `/connectors/stripe/webhook`

Automatic refunds are intentionally outside the MVP. Refunds are handled manually in Stripe Dashboard.

## Verification

```sh
make check-fdg-v2
make test-unit
make test-integration
make test-e2e
make build
```

Integration coverage uses the real Dex Server and official connector code with local provider protocol fixtures. It verifies single-seat oversell protection, ACH success delivery, Gmail ticket dispatch, QR generation, and duplicate-safe check-in.
