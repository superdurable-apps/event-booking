//go:build integration

package process_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/superdurable-apps/event-booking/internal/process"
	appRuntime "github.com/superdurable-apps/event-booking/internal/runtime"
)

func TestRegistrationACHWebhookTicketEmailAndAtomicCheckIn(t *testing.T) {
	var gmailSends atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/stripe/checkout/sessions":
			if err := request.ParseForm(); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			writeCheckoutSession(response, request.Form)
		case "/gmail/users/me/messages/send":
			gmailSends.Add(1)
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"id":"msg_ticket_1","threadId":"thread_ticket_1"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer provider.Close()

	t.Setenv("DEX_CONNECTOR_CONFIG_FILE", "")
	t.Setenv("STRIPE_ENDPOINT", provider.URL+"/stripe")
	t.Setenv("GMAIL_ENDPOINT", provider.URL+"/gmail")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_integration")
	t.Setenv("PUBLIC_BASE_URL", "https://events.example.test")
	t.Setenv("EVENT_CAPACITY", "1")
	t.Setenv("EVENT_PRICE_MINOR", "7500")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runtime := startRuntime(t, ctx)
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	}()

	first, err := runtime.Registrations.StartRegistration(ctx, process.NewRegistration{
		RequestID: uuid.New(), FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", AcceptedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("start first registration: %v", err)
	}
	first = waitForRegistration(t, ctx, runtime, first.RegistrationToken, process.StatusCheckoutReady)
	if !strings.HasPrefix(first.CheckoutURL, "https://checkout.stripe.test/") {
		t.Fatalf("checkout URL = %q", first.CheckoutURL)
	}

	second, err := runtime.Registrations.StartRegistration(ctx, process.NewRegistration{
		RequestID: uuid.New(), FirstName: "Grace", LastName: "Hopper", Email: "grace@example.com", AcceptedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("start second registration: %v", err)
	}
	waitForRegistration(t, ctx, runtime, second.RegistrationToken, process.StatusSoldOut)

	registrationID := flowIDFromToken(t, first.RegistrationToken)
	webhookBody := fmt.Sprintf(`{"id":"evt_paid_1","object":"event","type":"checkout.session.async_payment_succeeded","created":%d,"data":{"object":{"id":"cs_%s","object":"checkout.session","url":"https://checkout.stripe.test/%s","client_reference_id":"%s","payment_status":"paid","status":"complete","payment_intent":"pi_paid_1","currency":"usd","amount_total":7500,"metadata":{"registration_id":"%s","event_id":"default"}}}}`, time.Now().Unix(), strings.TrimPrefix(registrationID, "registration-"), registrationID, registrationID, registrationID)
	postSignedWebhook(t, ctx, runtime.StripeWebhook, webhookBody, "whsec_integration")
	first = waitForRegistration(t, ctx, runtime, first.RegistrationToken, process.StatusTicketEmailed)
	if gmailSends.Load() != 1 {
		t.Fatalf("Gmail sends = %d, want 1", gmailSends.Load())
	}

	ticketToken := ticketTokenFromURL(t, first.TicketURL)
	ticket, err := runtime.Registrations.GetTicket(ctx, ticketToken)
	if err != nil {
		t.Fatalf("get paid ticket: %v", err)
	}
	if !strings.HasPrefix(ticket.QRCodeDataURL, "data:image/png;base64,") || ticket.AttendeeName != "Ada Lovelace" {
		t.Fatalf("unexpected ticket: %#v", ticket)
	}
	checkedAt := time.Now().UTC().Truncate(time.Millisecond)
	checked, err := runtime.Registrations.CheckIn(ctx, ticketToken, checkedAt)
	if err != nil || checked.Status != "checked_in" {
		t.Fatalf("first check-in = %#v, %v", checked, err)
	}
	duplicate, err := runtime.Registrations.CheckIn(ctx, ticketToken, checkedAt.Add(time.Minute))
	if err != nil || duplicate.Status != "already_checked_in" || !duplicate.CheckedInAt.Equal(checked.CheckedInAt) {
		t.Fatalf("duplicate check-in = %#v, %v", duplicate, err)
	}
}

func startRuntime(t *testing.T, ctx context.Context) *appRuntime.Runtime {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runtime, err := appRuntime.New(logger)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	result := runtime.Start()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, dialErr := net.DialTimeout("tcp", os.Getenv("DEX_WORKER_TARGET"), 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		select {
		case err := <-result:
			t.Fatalf("start runtime: %v", err)
		case <-deadline.C:
			t.Fatalf("wait for Worker listener: %v", dialErr)
		case <-ticker.C:
		}
	}
	if err := runtime.EnsureEvent(ctx); err != nil {
		t.Fatalf("ensure event: %v", err)
	}
	return runtime
}

func waitForRegistration(t *testing.T, ctx context.Context, runtime *appRuntime.Runtime, token string, status process.RegistrationStatus) process.RegistrationView {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last process.RegistrationView
	var lastErr error
	for {
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		last, lastErr = runtime.Registrations.GetRegistration(attempt, token)
		cancel()
		if lastErr == nil && last.Status == status {
			return last
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for registration %s: last=%#v error=%v", status, last, lastErr)
		case <-ticker.C:
		}
	}
}

func writeCheckoutSession(response http.ResponseWriter, form url.Values) {
	registrationID := form.Get("client_reference_id")
	amount, _ := strconv.ParseInt(form.Get("line_items[0][price_data][unit_amount]"), 10, 64)
	payload := map[string]any{
		"id": "cs_" + strings.TrimPrefix(registrationID, "registration-"), "object": "checkout.session",
		"url": "https://checkout.stripe.test/" + registrationID, "client_reference_id": registrationID,
		"payment_status": "unpaid", "status": "open", "currency": form.Get("line_items[0][price_data][currency]"), "amount_total": amount,
		"metadata": map[string]string{"registration_id": registrationID, "event_id": form.Get("metadata[event_id]")},
	}
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}

func postSignedWebhook(t *testing.T, ctx context.Context, handler http.Handler, body, secret string) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		timestamp := time.Now().Unix()
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "." + body))
		signature := fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
		request := httptest.NewRequest(http.MethodPost, "/connectors/stripe/webhook", strings.NewReader(body)).WithContext(ctx)
		request.Header.Set("Stripe-Signature", signature)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("post signed webhook: status=%d body=%s", response.Code, response.Body.String())
		case <-ticker.C:
		}
	}
}

func flowIDFromToken(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("invalid registration token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	return strings.TrimPrefix(string(payload), "registration:")
}

func ticketTokenFromURL(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse ticket URL: %v", err)
	}
	return strings.TrimPrefix(parsed.Path, "/tickets/")
}
