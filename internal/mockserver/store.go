package mockserver

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/superdurable-apps/event-booking/internal/api/generated"
	"github.com/superdurable-apps/event-booking/internal/process"
)

const MockStaffToken = "mock-staff-token-123"

var (
	ErrUnknownRegistration = errors.New("unknown registration")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrTicketUnavailable   = errors.New("ticket unavailable")
	ErrInvalidControl      = errors.New("invalid mock control")
)

type Clock interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type registrationRecord struct {
	View        generated.RegistrationView
	AccessToken string
	TicketToken string
	FirstName   string
	LastName    string
}

type Store struct {
	mu            sync.Mutex
	clock         Clock
	tokens        *process.TokenSigner
	event         generated.EventView
	registrations map[string]*registrationRecord
}

type ControlView struct {
	Mode         string                      `json:"mode"`
	Registration *generated.RegistrationView `json:"registration,omitempty"`
	StaffToken   string                      `json:"staffToken"`
}

func NewStore(clock Clock) *Store {
	if clock == nil {
		clock = systemClock{}
	}
	baseURL := os.Getenv("MOCK_PUBLIC_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:5173"
	}
	tokens, err := process.NewTokenSigner("mock-ticket-signing-key-32-bytes-minimum", baseURL)
	if err != nil {
		panic(err)
	}
	return &Store{
		clock: clock, tokens: tokens, registrations: make(map[string]*registrationRecord),
		event: generated.EventView{
			ID: "event-tbd", Name: "Event name TBD", Description: "A placeholder event description will go here once the program is confirmed.",
			DateTimeDisplay: "To be announced", Location: "To be announced", PriceDisplay: "$125",
			Capacity: 300, RegistrationOpen: true, RegistrationDeadlineDisplay: "To be announced", OrganizerEmail: "events@example.com",
		},
	}
}

func (store *Store) Event() generated.EventView {
	store.mu.Lock()
	defer store.mu.Unlock()
	view := store.event
	view.ReservedCount = int64(store.activeReservationCount())
	return view
}

func (store *Store) Create(firstName, lastName, email string) generated.RegistrationCreated {
	store.mu.Lock()
	defer store.mu.Unlock()
	id := uuid.New()
	rawID := id.String()
	checkoutURL, _ := url.Parse("https://checkout.stripe.test/session/" + rawID)
	view := generated.RegistrationView{
		RegistrationId: id, DisplayCode: displayCode(rawID), AttendeeName: strings.TrimSpace(firstName + " " + lastName),
		EmailMasked: maskEmail(email), State: generated.RegistrationStatePaymentPending, PaymentStatus: generated.PaymentStatusPending,
		TicketStatus: generated.TicketStatusNotAvailable, EmailDeliveryStatus: generated.EmailDeliveryStatusNotStarted,
		Message:     "Complete ACH payment in Stripe. Bank settlement can take several business days.",
		CheckoutUrl: generated.NewOptURI(*checkoutURL),
	}
	record := &registrationRecord{
		View: view, AccessToken: store.tokens.AccessToken(rawID), TicketToken: store.tokens.TicketToken(rawID), FirstName: firstName, LastName: lastName,
	}
	store.registrations[rawID] = record
	statusURL, _ := url.Parse(store.tokens.StatusURL(rawID))
	return generated.RegistrationCreated{
		RegistrationId: id, DisplayCode: view.DisplayCode, AccessToken: record.AccessToken, StatusUrl: *statusURL, State: view.State,
	}
}

func (store *Store) Registration(id, token string) (generated.RegistrationView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.registrations[id]
	if record == nil {
		return generated.RegistrationView{}, ErrUnknownRegistration
	}
	if record.AccessToken != token {
		return generated.RegistrationView{}, ErrUnauthorized
	}
	return record.View, nil
}

func (store *Store) Ticket(id, token string) (generated.TicketView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.registrations[id]
	if record == nil {
		return generated.TicketView{}, ErrUnknownRegistration
	}
	if record.TicketToken != token {
		return generated.TicketView{}, ErrUnauthorized
	}
	if record.View.PaymentStatus != generated.PaymentStatusPaid {
		return generated.TicketView{}, ErrTicketUnavailable
	}
	qrURL, _ := url.Parse(store.tokens.QRURL(id))
	view := generated.TicketView{
		RegistrationId: record.View.RegistrationId, DisplayCode: record.View.DisplayCode, AttendeeName: record.View.AttendeeName,
		EventName: store.event.Name, DateTimeDisplay: store.event.DateTimeDisplay, Location: store.event.Location,
		QrImageUrl: *qrURL, EmailDeliveryStatus: record.View.EmailDeliveryStatus, CheckedIn: record.View.CheckedInAt.IsSet(),
	}
	if checkedInAt, ok := record.View.CheckedInAt.Get(); ok {
		view.CheckedInAt = generated.NewOptDateTime(checkedInAt)
	}
	return view, nil
}

func (store *Store) TicketURL(id string) string { return store.tokens.TicketURL(id) }

func (store *Store) CheckIn(staffToken, ticketCode string) (generated.CheckInView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if staffToken != MockStaffToken {
		return generated.CheckInView{}, ErrUnauthorized
	}
	id, token := parseTicketCode(ticketCode)
	record := store.registrations[id]
	if record == nil || record.TicketToken != token {
		return generated.CheckInView{}, ErrUnknownRegistration
	}
	if record.View.PaymentStatus != generated.PaymentStatusPaid {
		return generated.CheckInView{}, ErrTicketUnavailable
	}
	result := generated.CheckInResultCheckedIn
	message := "Ticket checked in."
	checkedInAt, already := record.View.CheckedInAt.Get()
	if already {
		result = generated.CheckInResultAlreadyCheckedIn
		message = "Ticket was already checked in."
	} else {
		checkedInAt = store.clock.Now().UTC()
		record.View.CheckedInAt = generated.NewOptDateTime(checkedInAt)
		record.View.State = generated.RegistrationStateCheckedIn
		record.View.TicketStatus = generated.TicketStatusCheckedIn
		record.View.Message = "Ticket checked in."
	}
	return generated.CheckInView{
		Result: result, RegistrationId: record.View.RegistrationId, DisplayCode: record.View.DisplayCode,
		AttendeeName: record.View.AttendeeName, CheckedInAt: checkedInAt, Message: message,
	}, nil
}

func (store *Store) Reset() ControlView {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.registrations = make(map[string]*registrationRecord)
	return store.controlView(nil)
}

func (store *Store) SetState(action, id string) (ControlView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record := store.registrations[id]
	if record == nil {
		return ControlView{}, ErrUnknownRegistration
	}
	switch action {
	case "settle_payment":
		record.View.State = generated.RegistrationStateTicketReady
		record.View.PaymentStatus = generated.PaymentStatusPaid
		record.View.TicketStatus = generated.TicketStatusReady
		record.View.EmailDeliveryStatus = generated.EmailDeliveryStatusSent
		record.View.Message = "Your electronic ticket is ready."
		ticketURL, _ := url.Parse(store.tokens.TicketURL(id))
		record.View.TicketUrl = generated.NewOptURI(*ticketURL)
	case "fail_payment":
		record.View.State = generated.RegistrationStatePaymentFailed
		record.View.PaymentStatus = generated.PaymentStatusFailed
		record.View.Message = "The ACH payment failed and the reserved place was released."
	case "expire_payment":
		record.View.State = generated.RegistrationStatePaymentExpired
		record.View.PaymentStatus = generated.PaymentStatusExpired
		record.View.Message = "The payment session expired and the reserved place was released."
	case "email_review":
		record.View.State = generated.RegistrationStateTicketEmailReviewRequired
		record.View.PaymentStatus = generated.PaymentStatusPaid
		record.View.TicketStatus = generated.TicketStatusEmailReviewRequired
		record.View.EmailDeliveryStatus = generated.EmailDeliveryStatusFailed
		record.View.Message = "Your ticket is ready online, but email delivery needs organizer review."
		ticketURL, _ := url.Parse(store.tokens.TicketURL(id))
		record.View.TicketUrl = generated.NewOptURI(*ticketURL)
	default:
		return ControlView{}, ErrInvalidControl
	}
	view := record.View
	return store.controlView(&view), nil
}

func (store *Store) Control(id string) (ControlView, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if id == "" {
		return store.controlView(nil), nil
	}
	record := store.registrations[id]
	if record == nil {
		return ControlView{}, ErrUnknownRegistration
	}
	view := record.View
	return store.controlView(&view), nil
}

func (store *Store) controlView(view *generated.RegistrationView) ControlView {
	return ControlView{Mode: "mock", Registration: view, StaffToken: MockStaffToken}
}

func (store *Store) activeReservationCount() int {
	count := 0
	for _, record := range store.registrations {
		if record.View.PaymentStatus != generated.PaymentStatusFailed && record.View.PaymentStatus != generated.PaymentStatusExpired {
			count++
		}
	}
	return count
}

func displayCode(id string) string {
	compact := strings.ToUpper(strings.ReplaceAll(id, "-", ""))
	return compact[len(compact)-8:]
}

func maskEmail(email string) string {
	parts := strings.Split(strings.ToLower(email), "@")
	if len(parts) != 2 || parts[0] == "" {
		return "***"
	}
	return parts[0][:1] + strings.Repeat("*", len(parts[0])-1) + "@" + parts[1]
}

func parseTicketCode(raw string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && parsed.Scheme != "" {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) >= 2 && segments[len(segments)-2] == "ticket" {
			return segments[len(segments)-1], parsed.Query().Get("token")
		}
	}
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}
