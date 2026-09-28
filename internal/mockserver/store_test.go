package mockserver

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/superdurable-apps/event-booking/internal/api/generated"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func TestStorePaymentTicketAndCheckInLifecycle(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	store := NewStore(clock)
	created := store.Create("Ada", "Lovelace", "ada@example.com")
	registration, err := store.Registration(created.RegistrationId.String(), created.AccessToken)
	if err != nil || registration.PaymentStatus != generated.PaymentStatusPending {
		t.Fatalf("pending registration = %+v, %v", registration, err)
	}
	if _, err := store.Ticket(created.RegistrationId.String(), store.tokens.TicketToken(created.RegistrationId.String())); !errors.Is(err, ErrTicketUnavailable) {
		t.Fatalf("ticket before settlement error = %v", err)
	}
	if _, err := store.SetState("settle_payment", created.RegistrationId.String()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	ticket, err := store.Ticket(created.RegistrationId.String(), store.tokens.TicketToken(created.RegistrationId.String()))
	if err != nil || ticket.EmailDeliveryStatus != generated.EmailDeliveryStatusSent {
		t.Fatalf("settled ticket = %+v, %v", ticket, err)
	}
	ticketURL := store.TicketURL(created.RegistrationId.String())
	first, err := store.CheckIn(MockStaffToken, ticketURL)
	if err != nil || first.Result != generated.CheckInResultCheckedIn {
		t.Fatalf("first check-in = %+v, %v", first, err)
	}
	second, err := store.CheckIn(MockStaffToken, ticketURL)
	if err != nil || second.Result != generated.CheckInResultAlreadyCheckedIn || !second.CheckedInAt.Equal(first.CheckedInAt) {
		t.Fatalf("second check-in = %+v, %v", second, err)
	}
}

func TestStoreKeepsTicketUsableAndEmailFailureVisibleAfterCheckIn(t *testing.T) {
	store := NewStore(&fakeClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)})
	created := store.Create("Katherine", "Johnson", "katherine@example.com")
	registrationID := created.RegistrationId.String()
	if _, err := store.SetState("email_review", registrationID); err != nil {
		t.Fatalf("email review: %v", err)
	}
	ticketToken := store.tokens.TicketToken(registrationID)
	ticket, err := store.Ticket(registrationID, ticketToken)
	if err != nil || ticket.EmailDeliveryStatus != generated.EmailDeliveryStatusFailed {
		t.Fatalf("email failure ticket = %+v, %v", ticket, err)
	}
	if _, err := store.CheckIn(MockStaffToken, store.TicketURL(registrationID)); err != nil {
		t.Fatalf("check in email failure ticket: %v", err)
	}
	ticket, err = store.Ticket(registrationID, ticketToken)
	if err != nil || !ticket.CheckedIn || ticket.EmailDeliveryStatus != generated.EmailDeliveryStatusFailed {
		t.Fatalf("checked-in email failure ticket = %+v, %v", ticket, err)
	}
}

func TestStoreReleasesFailedMockReservation(t *testing.T) {
	store := NewStore(nil)
	created := store.Create("Grace", "Hopper", "grace@example.com")
	if store.Event().ReservedCount != 1 {
		t.Fatal("reservation was not counted")
	}
	if _, err := store.SetState("fail_payment", created.RegistrationId.String()); err != nil {
		t.Fatalf("fail payment: %v", err)
	}
	if store.Event().ReservedCount != 0 {
		t.Fatal("failed payment did not release reservation")
	}
}
