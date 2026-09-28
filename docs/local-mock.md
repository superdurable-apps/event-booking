# Local mock server

Use the mock server to review the registration, payment-state, ticket, and
check-in experiences before connecting the application to Dex, Stripe, or
Gmail. It implements the same OpenAPI contract with in-memory Go state.

## Start

```bash
make bootstrap
make mock
```

Open <http://127.0.0.1:8080>. Vite provides hot module replacement and proxies
`/api` and `/__mock__` to the loopback-only Go mock API.

| Variable | Default | Purpose |
| --- | --- | --- |
| `MOCK_WEB_HOST` | `0.0.0.0` | Vite bind host. |
| `MOCK_WEB_PORT` | `8080` | Browser port. |
| `MOCK_API_HOST` | `127.0.0.1` | Mock API bind host. |
| `MOCK_API_PORT` | `18081` | Mock API port. |

## Review journey

1. Submit the attendee form. The mock immediately returns a pending Stripe
   Checkout URL and stores the private status token.
2. On the registration page, choose **Settle ACH payment**, **Fail payment**,
   or **Expire checkout**. **Simulate email review** makes a paid ticket
   available while showing the Gmail recovery state.
3. After settlement, open the electronic ticket and copy its ticket URL.
4. Visit `/check-in`, enter `mock-staff-token-123`, and paste the ticket URL.
   Submit it a second time to verify duplicate check-in handling.

The server keeps state across browser refreshes. Its data is discarded when
the process restarts. The mock-only HTTP surface is
`GET /__mock__/control?registrationId=...` and `POST /__mock__/control`; the
production server always returns 404 for `/__mock__/`.

## Verification boundary

Run `make test-mock-e2e` for the complete browser journey. The mock does not
prove Dex durability, the shared 300-person capacity lock, Stripe signatures,
ACH settlement delivery, or Gmail delivery. Those boundaries are covered by
the real-Dex integration and production E2E suites; a deployment smoke test
with provider-owned test credentials remains an operator responsibility.
