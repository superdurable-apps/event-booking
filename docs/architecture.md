# Architecture

The browser talks only to the application's OpenAPI server. The Go server owns
the Dex client and Worker, connector runtimes, signed attendee capabilities,
and static React assets.

```text
Attendee browser ── OpenAPI ── Registration service ── Dex registration Flow
                                             │                  │
                                             │                  ├─ Stripe ACH Checkout Step
                                             │                  └─ Gmail ticket Step
                                             │
Stripe webhook ── signature + durable inbox ─┘

Registration Flow ── transactional RPC ── shared event-capacity Flow

Staff browser ── staff token + QR URL ── check-in RPC ── registration Flow
```

## Durable boundaries

`EventCapacityFlow` owns a durable map of registration IDs and a reserved
count. Its transactional RPCs serialize concurrent reserve and release calls;
the registration Flow never performs an eventually-consistent read followed
by a separate write.

Each `RegistrationFlow` owns attendee, payment, ticket, provider-result, and
check-in state. Stripe webhook delivery invokes a transactional RPC and
publishes a typed payment event to the Flow. The waiting Step decides whether
to continue waiting, release capacity, or send a ticket. Both Stripe event IDs
and check-in state are deduplicated durably.

## Capability and credential boundary

The status URL carries a registration capability in the URL fragment, which
the browser forwards in a request header. The ticket URL carries a separate
HMAC capability because its full URL is encoded into the QR code and email.
Neither capability nor the staff token is stored in Dex state. Connector API
keys and webhook secrets are loaded into typed connections at startup and are
not part of Flow input or persistence.

The QR image contains the ticket URL. Check-in accepts that URL or the compact
`registration-id.ticket-token` representation, validates the staff credential
and ticket capability, then invokes the idempotent Flow Action.
