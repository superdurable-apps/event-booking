package process

import (
	"strings"
	"testing"
	"time"

	stripe "github.com/superdurable/dex-connectors-library/connectors/stripe"
)

func TestPrivateTokensArePurposeBoundAndTamperEvident(t *testing.T) {
	signer, err := NewTokenSigner(strings.Repeat("s", 32))
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	flowID := "registration-29cf4fa6-35ae-492b-a4f8-cac566095606"
	token := signer.Sign("ticket", flowID)
	got, err := signer.Verify(token, "ticket")
	if err != nil || got != flowID {
		t.Fatalf("verify token = %q, %v", got, err)
	}
	if _, err := signer.Verify(token, "registration"); err != ErrInvalidToken {
		t.Fatalf("wrong-purpose error = %v", err)
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := signer.Verify(tampered, "ticket"); err != ErrInvalidToken {
		t.Fatalf("tampered-token error = %v", err)
	}
}

func TestCheckoutInputCarriesOnlyProviderSafeCorrelationMetadata(t *testing.T) {
	createdAt := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	input := RegistrationInput{
		RegistrationID: "registration-123", RegistrationToken: "registration-token", TicketToken: "ticket-token",
		FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.com", CreatedAt: createdAt,
		PublicBaseURL: "https://events.example.com/", Event: EventView{EventConfig: EventConfig{EventID: "event", Name: "Conference", Currency: "USD", PriceMinor: 7500}},
	}
	checkout := mapCheckoutInput(input)
	if checkout.Currency != "usd" || checkout.UnitAmount != 7500 || checkout.CustomerEmail != input.Email {
		t.Fatalf("unexpected checkout input: %#v", checkout)
	}
	if checkout.Metadata["registration_id"] != input.RegistrationID || checkout.Metadata["event_id"] != "event" {
		t.Fatalf("unexpected metadata: %#v", checkout.Metadata)
	}
	if strings.Contains(strings.Join(mapValues(checkout.Metadata), " "), input.Email) {
		t.Fatal("email leaked into Stripe metadata")
	}
}

func TestPaidSessionMustMatchRegistrationAmountAndCurrency(t *testing.T) {
	state := RegistrationState{RegistrationInput: RegistrationInput{
		RegistrationID: "registration-123", Event: EventView{EventConfig: EventConfig{Currency: "USD", PriceMinor: 7500}},
	}, CheckoutSessionID: "cs_test_123"}
	valid := stripe.CheckoutSession{ID: "cs_test_123", ClientReferenceID: "registration-123", Currency: "usd", AmountTotal: 7500, PaymentStatus: "paid"}
	if err := validatePaidSession(state, valid); err != nil {
		t.Fatalf("valid paid session rejected: %v", err)
	}
	wrongAmount := valid
	wrongAmount.AmountTotal = 7400
	if err := validatePaidSession(state, wrongAmount); err == nil {
		t.Fatal("mismatched amount accepted")
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
