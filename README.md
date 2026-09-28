# Event Booking

A durable registration and check-in application for an event with up to 300
attendees. Attendees register in the React site, pay by Stripe-hosted ACH
Checkout, receive a QR-code ticket by Gmail after the payment settles, and
present that ticket for staff check-in.

Event name, schedule, location, price, registration deadline, organizer, and
public URL are runtime configuration. The committed defaults intentionally say
TBD and keep registration closed until the final event details are supplied.

## Registration lifecycle

1. The registration Flow validates the attendee and atomically reserves one
   place in a shared event-capacity Flow.
2. Stripe creates a hosted Checkout Session for US bank account payment. Bank
   account details stay on Stripe and never cross this application's API.
3. The status page shows the Checkout link and remains pending while ACH is
   settling. A completed Checkout is not treated as paid unless Stripe reports
   `payment_status=paid`; asynchronous success is accepted through the signed
   webhook.
4. A successful settlement makes the ticket available and sends its private
   URL and QR code by Gmail. A failed payment or expired Checkout releases the
   reserved place.
5. Staff scan the QR code, or paste its ticket URL, at `/check-in`. Check-in is
   idempotent, so rescanning reports that the attendee was already admitted.

Capacity and every registration are durable Dex Flows. The capacity test starts
350 concurrent reservations and verifies that exactly 300 are accepted.

## Local product review

The mock server exercises the whole browser journey without Dex, Stripe, or
Gmail:

```bash
make bootstrap
make mock
```

Open <http://127.0.0.1:8080>, register, then use the on-page mock controls to
settle or fail the payment. The mock staff token is
`mock-staff-token-123`. See [docs/local-mock.md](docs/local-mock.md) for the
mock boundary and controls.

## Stripe and Gmail sandbox end-to-end runbook

Use this runbook to prove the real Dex, Stripe Checkout, Stripe webhook, Gmail
delivery, QR ticket, and staff check-in path from a browser. It is the detailed
operator procedure for the request: “请帮我详细的一步步的做这个测试”.

This is a **provider-backed sandbox test, not a real-money transfer**. Stripe
Checkout Session IDs and API keys must begin with `cs_test_` and `sk_test_`.
Never enter a real bank account, real online-banking credentials, a live Stripe
key, or a live webhook secret while following this procedure. A sandbox test
proves the integration and asynchronous state transitions without moving
funds.

The test uses four working surfaces:

| Surface | Purpose | Keep running? |
| --- | --- | --- |
| Terminal A | Stripe CLI webhook forwarding | Yes, through payment settlement |
| Terminal B | Dex plus the event-booking server | Yes, through check-in |
| Browser | Registration, Stripe Checkout, ticket, and check-in | Yes |
| Gmail | Confirm delivery and open the emailed ticket | Until delivery is verified |

### 1. Check prerequisites

From the repository root, verify the required tools and restore the pinned
dependencies:

```bash
go version
node --version
npm --version
dexcli version
stripe version
make bootstrap
make check-fdg-v2
```

Install the Stripe CLI from the
[official Stripe CLI instructions](https://docs.stripe.com/stripe-cli) if the
last command is unavailable. Do not replace or upgrade dependencies merely to
run this test.

This application pins the official Stripe connector release `v0.1.0`,
published from
[superdurable/dex-connectors-library#66](https://github.com/superdurable/dex-connectors-library/pull/66).
Verify that exact public module without a workspace replacement:

```bash
GOWORK=off go mod download github.com/superdurable/dex-connectors-library/connectors/stripe@v0.1.0
```

Do not use a local path, branch, pseudo-version, or committed `replace` for
release verification. Run `make check-fdg-v2` and `make check` against the
published module.

### 2. Select the Stripe sandbox and obtain its test key

1. Sign in to the Stripe Dashboard.
2. Use the account picker to select the intended account.
3. Enter a **Sandbox** or turn on **Test mode**. Do not continue in live mode.
4. Open **Developers → API keys**.
5. Reveal and copy the secret test key beginning with `sk_test_`. Do not use the
   publishable key beginning with `pk_test_`.
6. Store the value in a password manager or paste it directly into the ignored
   local connection file created in step 5. Do not put it in shell history,
   screenshots, logs, issues, commits, or pull requests.

The Stripe Dashboard account selected here must be the same account authorized
by the Stripe CLI in the next step.

### 3. Authenticate the Stripe CLI

In Terminal A, start device authorization:

```bash
stripe login
```

1. The CLI prints a pairing code and opens, or asks you to open, a Stripe device
   authorization URL such as
   `https://access.stripe.com/stripecli/oauth2/device`. Follow the exact URL
   printed by the CLI.
2. Confirm that the browser shows the same pairing code.
3. Approve access for the same Stripe sandbox account used in step 2.
4. Return to Terminal A and wait for the success message.

CLI login authorizes the CLI; it does not replace the `sk_test_` application
credential.

### 4. Start local Stripe webhook forwarding

Still in Terminal A, forward only the four events consumed by the application:

```bash
stripe listen \
  --events checkout.session.completed,checkout.session.async_payment_succeeded,checkout.session.async_payment_failed,checkout.session.expired \
  --forward-to http://127.0.0.1:8080/webhooks/stripe
```

The CLI prints a signing secret beginning with `whsec_`. Copy that value into
the local connection file in step 5. Leave this terminal running. If the
listener is restarted and prints a different secret, update the file and
restart the application before testing another payment.

During a successful test, Terminal A should eventually show both of these
event classes with a local HTTP `200` response:

```text
checkout.session.completed
checkout.session.async_payment_succeeded
```

`checkout.session.completed` alone is not sufficient for an ACH ticket. The
application waits for paid status or asynchronous payment success before
issuing the ticket.

### 5. Authorize Gmail for the ticket email

The local Gmail connector accepts a short-lived OAuth access token. For a
manual sandbox run:

1. Open the [Google OAuth 2.0 Playground](https://developers.google.com/oauthplayground/).
2. In **Step 1**, enter or select the scope
   `https://www.googleapis.com/auth/gmail.send`.
3. Click **Authorize APIs**.
4. Sign in to the mailbox that will send the test ticket and approve the
   requested Gmail send permission.
5. After Google redirects back to the playground, click
   **Exchange authorization code for tokens**.
6. Copy the **Access token**, not the authorization code, ID token, refresh
   token, or the `code=` query parameter in the browser URL.
7. Record the exact primary email address of the Google account that granted
   access. It becomes `primary_email` in the connector file.

OAuth Playground access tokens are short-lived. If Gmail later returns `401`
or `invalid_grant`, repeat this step and restart the application with the new
access token. Never commit the token.

Create `.local/connections.json` with mode `0600`. The `.local/` directory is
ignored by Git.

```bash
mkdir -p .local
touch .local/connections.json
chmod 600 .local/connections.json
```

Put the following JSON in the file, replacing every `REPLACE_ME` value. Use
the test key from step 2, the webhook secret from step 4, and the Gmail values
from this step:

```json
{
  "schemaVersion": "connectors.dex.dev/local-connections/v1alpha1",
  "connections": [
    {
      "connectorId": "stripe",
      "modulePath": "github.com/superdurable/dex-connectors-library/connectors/stripe",
      "moduleVersion": "v0.1.0",
      "provider": "stripe",
      "connectionName": "stripe-payments",
      "configuration": {},
      "credentials": {
        "secret_key": "sk_test_REPLACE_ME",
        "webhook_secret": "whsec_REPLACE_ME"
      }
    },
    {
      "connectorId": "gmail",
      "modulePath": "github.com/superdurable/dex-connectors-library/connectors/google/gmail",
      "moduleVersion": "v0.11.0",
      "provider": "google",
      "connectionName": "gmail-tickets",
      "configuration": {},
      "credentials": {
        "access_token": "REPLACE_ME",
        "primary_email": "REPLACE_ME@example.com"
      }
    }
  ],
  "triggerBindings": [
    {
      "connectorId": "stripe",
      "connectionName": "stripe-payments",
      "triggerName": "checkoutSessionUpdated",
      "bindingName": "registration-checkout-updates",
      "configuration": {
        "eventTypes": [
          "checkout.session.completed",
          "checkout.session.async_payment_succeeded",
          "checkout.session.async_payment_failed",
          "checkout.session.expired"
        ]
      }
    }
  ]
}
```

Confirm that Git will not include the credential file:

```bash
git check-ignore -v .local/connections.json
```

### 6. Configure and start the real local application

In Terminal B, from the repository root, export the local-only credentials and
event settings. Replace all three `REPLACE_WITH_...` values before running the
server. The sandbox amount must be a positive integer in the currency's minor
unit; it is test data and must not be treated as the real event price.

```bash
export DEX_CONNECTOR_CONFIG_FILE="$PWD/.local/connections.json"
export TICKET_SIGNING_KEY="$(openssl rand -hex 32)"
export STAFF_CHECKIN_TOKEN="$(openssl rand -hex 24)"
export PUBLIC_BASE_URL="http://127.0.0.1:8080"
export PORT="8080"
export EVENT_NAME="REPLACE_WITH_SANDBOX_EVENT_NAME"
export EVENT_PRICE_DISPLAY="REPLACE_WITH_SANDBOX_PRICE_LABEL"
export EVENT_PRICE_CENTS="REPLACE_WITH_POSITIVE_INTEGER"
export REGISTRATION_OPEN="true"
printf 'Save this local staff token now: %s\n' "$STAFF_CHECKIN_TOKEN"
make dev
```

`make dev` builds the React application, starts an isolated local Dex server,
starts the Dex Worker and webhook runtime, and serves the site on port 8080.
Wait until startup completes, then verify health in another shell:

```bash
curl --fail http://127.0.0.1:8080/api/health
```

Save the value of `STAFF_CHECKIN_TOKEN` securely for step 11. Do not restart
Terminal B during the journey: `make dev` uses temporary Dex state, and a new
`TICKET_SIGNING_KEY` also invalidates URLs from the previous run.

The committed date, venue, organizer, deadline, and public event details remain
TBD. Set approved test-only display values through environment variables when
needed; do not edit the committed defaults to invent real event details.

### 7. Create a registration in the browser

1. Open <http://127.0.0.1:8080>.
2. Confirm the page shows the intended sandbox event name and price label.
3. Open the registration form.
4. Enter a test attendee name and an email address whose inbox you can inspect.
5. Accept the displayed policy confirmation.
6. Click **Continue to secure ACH payment**.
7. Save the resulting registration status URL. Its `#token=` fragment is a
   private attendee capability and must not be posted publicly.

The first status read may show **Payment is processing** with payment
**Not Started** while the durable Flow is creating the Checkout Session. The
page checks again automatically every two seconds while the registration is in
a transitional state. **Open secure ACH checkout** should appear without a
page reload; **Refresh status** remains available as a manual fallback.

### 8. Complete Stripe ACH Checkout using the test bank

Before entering anything, confirm all three safeguards:

- the address is on `checkout.stripe.com`;
- the Checkout Session identifier begins with `cs_test_`;
- Stripe displays test institutions such as **Test (Non-OAuth)**.

For the fastest successful path:

1. In **Full name**, enter the attendee's test name.
2. Click the green **Test (Non-OAuth)** institution.
3. Follow Stripe's test prompts, consent to the sandbox connection, and select
   a test checking account if asked.
4. Return to Checkout and click **Pay**.
5. Do not search for a real bank and do not enter real credentials.

The other colorful institutions simulate OAuth, ownership mismatch, invalid
payment methods, and unavailable-bank scenarios. They are useful for negative
testing but should not be selected for the first successful run.

If the instant-verification test institution is unavailable, Stripe also
documents the manual sandbox success values below. The manual path can include
microdeposit or waiting screens, so it is not the preferred happy path:

| Field | Stripe sandbox success value |
| --- | --- |
| Routing number | `110000000` |
| Account number | `000123456789` |

These are public Stripe test values, not real bank details. See
[Stripe's ACH test data](https://docs.stripe.com/testing?numbers-or-method-or-token=tokens#ach-direct-debit).

### 9. Wait for asynchronous payment success and the ticket

Stripe may redirect back before ACH settlement is represented as successful in
the application. On the registration status page:

1. Keep the status page open; it checks again automatically every two seconds.
2. If payment is still **Pending**, check Terminal A. Wait for
   `checkout.session.async_payment_succeeded` and its local HTTP `200`.
3. Allow the next automatic check to complete, or click **Refresh status** to
   request an immediate update.
4. Require payment **Paid** and ticket **Ready** before treating the run as a
   success.
5. Click **Open electronic ticket** and confirm that a QR code, attendee name,
   ticket ID, date display, location display, and **Valid for entry** appear.

The application intentionally does not issue a ticket merely because Checkout
was completed. A pending ACH payment must remain ticketless.

### 10. Verify Stripe and Gmail independently

In the Stripe Dashboard sandbox:

1. Open **Payments** and locate the payment for this test registration.
2. Confirm the payment is in the successful/paid state, not merely processing.
3. Open the related Checkout Session and confirm it is a test object.
4. Open **Developers → Events** and confirm delivery of the completed event and
   the asynchronous success event.

In the attendee Gmail inbox:

1. Search for `Your ticket for` followed by the configured event name.
2. Confirm the sender is the `primary_email` account configured in step 5.
3. Open the message and confirm it contains an **Open your ticket** link and a
   QR image.
4. Open the ticket link and confirm it displays the same ticket ID as the
   status page.

Ticket readiness and email delivery are related but separately observable. If
the API reports `emailDeliveryStatus=failed`, the paid ticket and QR code remain
valid online while the ticket page warns the attendee to contact the organizer.
An `unknown` outcome likewise preserves the online ticket without claiming
that Gmail delivered it. After credentials are repaired, the organizer can use
the durable **Resend ticket** Action; a successful resend changes the status to
`sent`.

### 11. Check in the QR ticket

Use a second browser profile or window on the same machine when possible so the
attendee ticket and staff screen remain separate. A second physical device
cannot reach a `127.0.0.1` URL; it requires a separately configured, reachable
HTTPS `PUBLIC_BASE_URL`.

1. Open <http://127.0.0.1:8080/check-in>.
2. Enter the exact `STAFF_CHECKIN_TOKEN` created in Terminal B.
3. Either click **Start camera** and scan the ticket QR code, or copy the full
   private ticket URL and paste it into **Ticket code or URL**.
4. Click **Check in attendee**.
5. Require **Check-in successful** and confirm the attendee name and ticket ID.
6. Submit the same ticket a second time.
7. Require **Already checked in**. This proves idempotence and must not admit
   the attendee twice.

Refresh the attendee's ticket page after check-in and confirm it reports that
the ticket has already been admitted.

### 12. Record the test result and stop local services

Record only non-secret evidence:

- test date and operator;
- application commit;
- Stripe sandbox account name;
- registration display code, not the private status or ticket URL;
- Checkout Session and event IDs, without bank or credential data;
- whether payment became paid, Gmail delivered, QR rendered, first check-in
  succeeded, and the repeat was rejected as already checked in;
- any unexpected UI or provider messages.

Stop Terminal B and Terminal A with `Ctrl-C`. Delete copied secrets from notes
and rotate or revoke temporary credentials when appropriate. The ignored
`.local/connections.json` may be retained for another local run only if its
permissions remain `0600` and the machine is trusted.

### Current limitations and troubleshooting

- **Status stays Not Started:** allow several seconds for automatic polling,
  then click **Refresh status** as a manual fallback. If the Checkout link
  still does not appear, inspect Terminal B for a Stripe connection or Dex Flow
  error.
- **Stripe Checkout was opened previously:** start a new registration after an
  application/Dex restart. Old local status links and Checkout Sessions belong
  to the previous durable run.
- **Webhook gets a non-200 response:** verify that Terminal B is running on
  port 8080 and that the `whsec_` value in `.local/connections.json` is the one
  printed by the currently running listener. Restart the application after a
  credential change.
- **Payment remains pending:** do not manufacture a ticket or check in. Verify
  the `checkout.session.async_payment_succeeded` event in the Stripe sandbox
  and Terminal A.
- **No Gmail message:** refresh the registration first. Then verify the access
  token scope, `primary_email`, spam folder, and Gmail API response. OAuth
  Playground tokens expire and require application restart after replacement.
- **Ticket URL is unauthorized:** a changed `TICKET_SIGNING_KEY` invalidates
  capabilities. Finish the journey within one server run or register again.
- **Camera scan is unavailable:** paste the full ticket URL into the staff
  form. Camera support depends on the browser and permissions.
- **Incorrect check-in time:** the current build has a known timestamp-display
  defect in the check-in Action path and can show a zero/year-1 time. Check-in
  success and duplicate rejection are durable, but the displayed timestamp is
  not yet valid evidence until that defect is fixed.
- **Real funds are required:** stop this sandbox procedure. A `cs_test_`
  Checkout Session never transfers real money. Live ACH requires separate
  production approval, live keys, a public HTTPS webhook, approved event
  details and price, and a controlled production validation plan.

## Production configuration

Build the web bundle before starting the Go server:

```bash
make bootstrap
make build
./bin/event-booking
```

Set these application variables in the deployment environment:

| Variable | Default | Purpose |
| --- | --- | --- |
| `EVENT_ID` | `event-tbd` | Stable lowercase event identifier. Do not change it after opening registration. |
| `EVENT_NAME` | `Event details coming soon` | Public event name and Stripe product name. |
| `EVENT_DESCRIPTION` | TBD text | Landing-page description. |
| `EVENT_DATE_DISPLAY` | `To be announced` | Display-ready date and time. |
| `EVENT_LOCATION` | `To be announced` | Display-ready venue or online location. |
| `EVENT_PRICE_DISPLAY` | `To be announced` | Human-readable ticket price. |
| `EVENT_PRICE_CENTS` | `0` | Stripe amount in the currency's minor unit; must be positive when registration opens. |
| `EVENT_CURRENCY` | `usd` | Lowercase three-letter currency. |
| `EVENT_CAPACITY` | `300` | Maximum active reservations. |
| `REGISTRATION_OPEN` | `false` | Set to `true` only after all public and payment settings are final. |
| `REGISTRATION_DEADLINE_DISPLAY` | `To be announced` | Display-ready registration deadline. |
| `ORGANIZER_EMAIL` | `events@example.com` | Public support address. |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Public origin used in status, ticket, and QR URLs; production must use HTTPS. |
| `TICKET_SIGNING_KEY` | none | Secret with at least 32 bytes, used for attendee access and ticket capabilities. |
| `STAFF_CHECKIN_TOKEN` | none | Secret with at least 16 bytes, required by the staff check-in API. |
| `DEX_CONNECTOR_CONFIG_FILE` | none | Path to the Stripe and Gmail local connection file. |
| `DEX_FLOW_SERVICE_ADDRESS` | `127.0.0.1:8801` | Dex Flow Service address. |
| `DEX_WORKER_BIND_ADDRESS` | `127.0.0.1:8811` | Worker listener address. |
| `DEX_WORKER_TARGET` | `127.0.0.1:8811` | Worker address advertised to Dex. |
| `DEX_BLOB_CACHE_DIR` | OS temp directory | Local blob-cache directory. |
| `PORT` | `8080` | HTTP port for the site, API, and Stripe webhook. |

Use secrets generated by the deployment platform; do not commit connector
credentials or either application token.

### Stripe and Gmail connections

Create the named connector connections `stripe-payments` and `gmail-tickets`.
For local configuration, point `DEX_CONNECTOR_CONFIG_FILE` to a mode-0600 file
with this shape:

```json
{
  "schemaVersion": "connectors.dex.dev/local-connections/v1alpha1",
  "connections": [
    {
      "connectorId": "stripe",
      "modulePath": "github.com/superdurable/dex-connectors-library/connectors/stripe",
      "moduleVersion": "v0.1.0",
      "provider": "stripe",
      "connectionName": "stripe-payments",
      "configuration": {},
      "credentials": {
        "secret_key": "sk_live_REPLACE_ME",
        "webhook_secret": "whsec_REPLACE_ME"
      }
    },
    {
      "connectorId": "gmail",
      "modulePath": "github.com/superdurable/dex-connectors-library/connectors/google/gmail",
      "moduleVersion": "v0.11.0",
      "provider": "google",
      "connectionName": "gmail-tickets",
      "configuration": {},
      "credentials": {
        "access_token": "REPLACE_ME",
        "primary_email": "tickets@example.com"
      }
    }
  ],
  "triggerBindings": [
    {
      "connectorId": "stripe",
      "connectionName": "stripe-payments",
      "triggerName": "checkoutSessionUpdated",
      "bindingName": "registration-checkout-updates",
      "configuration": {
        "eventTypes": [
          "checkout.session.completed",
          "checkout.session.async_payment_succeeded",
          "checkout.session.async_payment_failed",
          "checkout.session.expired"
        ]
      }
    }
  ]
}
```

Create one Stripe webhook endpoint at
`https://YOUR_PUBLIC_HOST/webhooks/stripe` and subscribe it to the four events
listed above. The webhook handler verifies Stripe signatures, durably queues
accepted events, and the registration Flow deduplicates their event IDs.

## Dex skills

Develop this application with the released
[Dex plugin](https://github.com/superdurable/dex-skills#install) installed in
the coding-agent host. Invoke `dex-app-builder` as the product workflow
entrypoint; it loads the matching `dex-sdk` guidance. Superverse Coding Sandbox
preinstalls a pinned release, while external developers install the plugin in
Codex, Claude, Cursor, or another Agent Skills client. The application never
assumes a fixed skill path. This repository does not contain, initialize, or
read a project-local skill copy.

## Verification

```bash
make check-generated
make check-fdg-v2
make test-unit
make test-integration
make test-e2e
make test-mock-e2e
make build
```

`make check` runs the complete release gate. Real Stripe settlement and Gmail
delivery still require operator-owned credentials and a public HTTPS webhook;
the deterministic suites use local fakes and never need live provider secrets.

The release tooling pins Dex Go SDK `v0.13.1`, Dex Server `v0.13.2`, and Dex
CLI `v0.13.8` and updates those stable baselines together.

The application uses the official Stripe connector release `v0.1.0`, published
from [superdurable/dex-connectors-library#66](https://github.com/superdurable/dex-connectors-library/pull/66).
