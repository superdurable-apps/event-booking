package process

import (
	"strings"
	"testing"

	gmail "github.com/superdurable/dex-connectors-library/connectors/google/gmail"
	"github.com/superdurable/dex/sdk-go/dex"
)

func TestCapacityFlowDefinitionRegistersDurableState(t *testing.T) {
	registry, err := dex.NewRegistry([]dex.Flow{EventCapacityFlow})
	if err != nil {
		t.Fatalf("register capacity Flow: %v", err)
	}
	if registry == nil {
		t.Fatal("registry is nil")
	}
	if got := len(EventCapacityFlow.GetPersistenceSchema().Attributes); got != 3 {
		t.Fatalf("attribute count = %d, want 3", got)
	}
	if got := len(EventCapacityFlow.GetRPCs()); got != 5 {
		t.Fatalf("RPC count = %d, want 5", got)
	}
}

func TestTokenSignerSeparatesAccessAndTicketCapabilities(t *testing.T) {
	signer, err := NewTokenSigner(strings.Repeat("k", 32), "https://events.example.com")
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	id := "00000000-0000-4000-8000-000000000001"
	access := signer.AccessToken(id)
	ticket := signer.TicketToken(id)
	if access == ticket {
		t.Fatal("access and ticket tokens are equal")
	}
	if !signer.VerifyAccess(id, access) || signer.VerifyAccess(id, ticket) {
		t.Fatal("access token verification did not enforce purpose")
	}
	if !strings.Contains(signer.StatusURL(id), "#token=") {
		t.Fatal("status URL does not keep access token in fragment")
	}
	if !strings.Contains(signer.TicketURL(id), "?token=") {
		t.Fatal("ticket URL does not contain ticket capability")
	}
}

func TestEventConfigurationKeepsTBDDeploymentClosed(t *testing.T) {
	config := EventConfig{
		ID: "event-tbd", Name: "Event details coming soon", Capacity: 300, Currency: "usd",
		OrganizerEmail: "events@example.com", PublicBaseURL: "https://events.example.com",
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("closed TBD config: %v", err)
	}
	config.RegistrationOpen = true
	if err := config.Validate(); err == nil {
		t.Fatal("open registration accepted without a positive price")
	}
}

func TestTicketCodeAcceptsURLAndCompactForm(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000001"
	token := strings.Repeat("a", 43)
	for _, code := range []string{"https://events.example.com/ticket/" + id + "?token=" + token, id + "." + token} {
		gotID, gotToken, err := parseTicketCode(code)
		if err != nil || gotID != id || gotToken != token {
			t.Fatalf("parse %q = %q, %q, %v", code, gotID, gotToken, err)
		}
	}
}

func TestTicketEmailDeliveryStatusSupportsFailureAndSuccessfulResend(t *testing.T) {
	record := RegistrationRecord{PaymentStatus: PaymentPaid, TicketStatus: TicketSending, EmailDeliveryStatus: EmailDeliverySending}
	markTicketEmailNeedsReview(&record, gmail.SendMessageResult{Branch: gmail.SendMessageBranchProviderRejected})
	if record.EmailDeliveryStatus != EmailDeliveryFailed || record.TicketStatus != TicketEmailReviewRequired {
		t.Fatalf("failed delivery record = %+v", record)
	}

	markTicketEmailSending(&record)
	if record.EmailDeliveryStatus != EmailDeliverySending || record.TicketStatus != TicketSending {
		t.Fatalf("resending delivery record = %+v", record)
	}

	markTicketEmailSent(&record)
	if record.EmailDeliveryStatus != EmailDeliverySent || record.TicketStatus != TicketReady {
		t.Fatalf("successful resend record = %+v", record)
	}
}

func TestTicketEmailDeliveryStatusMarksUncertainOutcomeUnknown(t *testing.T) {
	record := RegistrationRecord{}
	markTicketEmailNeedsReview(&record, gmail.SendMessageResult{Branch: gmail.SendMessageBranchUncertain})
	if record.EmailDeliveryStatus != EmailDeliveryUnknown {
		t.Fatalf("email delivery status = %q, want %q", record.EmailDeliveryStatus, EmailDeliveryUnknown)
	}
}
