package process

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"
	"github.com/superdurable/dex/sdk-go/dex"
)

var (
	ErrUnknownRegistration = errors.New("unknown registration")
	ErrTicketNotPaid       = errors.New("ticket is not paid")
	ErrInvalidToken        = errors.New("invalid private token")
)

type NewRegistration struct {
	RequestID  uuid.UUID
	FirstName  string
	LastName   string
	Email      string
	AcceptedAt time.Time
}

type RegistrationView struct {
	RegistrationToken string
	Status            RegistrationStatus
	FirstName         string
	LastName          string
	Email             string
	AmountMinor       int64
	Currency          string
	CheckoutURL       string
	TicketURL         string
	Message           string
}

type TicketView struct {
	Event         EventView
	AttendeeName  string
	TicketCode    string
	TicketURL     string
	QRCodeDataURL string
	CheckedIn     bool
	CheckedInAt   time.Time
}

type TokenSigner struct{ secret []byte }

func NewTokenSigner(secret string) (*TokenSigner, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("EVENT_TOKEN_SECRET must be at least 32 bytes")
	}
	return &TokenSigner{secret: []byte(secret)}, nil
}

func (signer *TokenSigner) Sign(purpose, flowID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(purpose + ":" + flowID))
	mac := hmac.New(sha256.New, signer.secret)
	_, _ = mac.Write([]byte(payload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + signature
}

func (signer *TokenSigner) Verify(token, purpose string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", ErrInvalidToken
	}
	mac := hmac.New(sha256.New, signer.secret)
	_, _ = mac.Write([]byte(parts[0]))
	expected := mac.Sum(nil)
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(provided, expected) {
		return "", ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	prefix := purpose + ":"
	if !strings.HasPrefix(string(payload), prefix) {
		return "", ErrInvalidToken
	}
	flowID := strings.TrimPrefix(string(payload), prefix)
	if !strings.HasPrefix(flowID, "registration-") || len(flowID) > 128 {
		return "", ErrInvalidToken
	}
	return flowID, nil
}

type Service struct {
	client        *dex.Client
	registrations *RegistrationFlow
	tokens        *TokenSigner
	publicBaseURL string
	now           func() time.Time
}

func NewService(client *dex.Client, registrations *RegistrationFlow, tokens *TokenSigner, publicBaseURL string) *Service {
	return &Service{client: client, registrations: registrations, tokens: tokens, publicBaseURL: strings.TrimRight(publicBaseURL, "/"), now: time.Now}
}

func (service *Service) EnsureEvent(ctx context.Context, config EventConfig) error {
	requestID := "initialize-" + config.EventID
	_, err := service.client.StartFlow(ctx, EventInventory, EventFlowID, config, dex.StartFlowOptions{
		RequestID: &requestID, IDReusePolicy: dex.IDReuseDisallow, AlreadyStarted: &dex.AlreadyStartedOptions{IgnoreError: true},
	})
	if err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := service.GetEvent(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (service *Service) GetEvent(ctx context.Context) (EventView, error) {
	var view EventView
	if err := service.client.InvokeRPC(ctx, EventFlowID, EventInventory.DescribeEvent, nil, &view); err != nil {
		return EventView{}, err
	}
	return view, nil
}

func (service *Service) StartRegistration(ctx context.Context, request NewRegistration) (RegistrationView, error) {
	if request.RequestID == uuid.Nil {
		return RegistrationView{}, fmt.Errorf("request ID is required")
	}
	event, err := service.GetEvent(ctx)
	if err != nil {
		return RegistrationView{}, err
	}
	flowID := "registration-" + request.RequestID.String()
	registrationToken := service.tokens.Sign("registration", flowID)
	ticketToken := service.tokens.Sign("ticket", flowID)
	now := service.now().UTC()
	var reservation ReserveSeatResult
	if err := service.client.InvokeRPC(ctx, EventFlowID, EventInventory.ReserveSeat, ReserveSeatInput{
		RegistrationID: flowID,
		ReservedAt:     now,
	}, &reservation); err != nil {
		return RegistrationView{}, err
	}
	event = reservation.Event
	input := RegistrationInput{
		RegistrationID: flowID, RegistrationToken: registrationToken, TicketToken: ticketToken,
		FirstName: strings.TrimSpace(request.FirstName), LastName: strings.TrimSpace(request.LastName), Email: strings.TrimSpace(request.Email),
		AcceptedTermsAt: request.AcceptedAt.UTC(), CreatedAt: now, PublicBaseURL: service.publicBaseURL, Event: event, SeatReserved: reservation.Accepted,
	}
	requestID := request.RequestID.String()
	_, err = service.client.StartFlow(ctx, service.registrations, flowID, input, dex.StartFlowOptions{
		RequestID: &requestID, IDReusePolicy: dex.IDReuseDisallow, AlreadyStarted: &dex.AlreadyStartedOptions{IgnoreError: true},
	})
	if err != nil {
		if reservation.Accepted && !reservation.Duplicate {
			var ignored EventView
			_ = service.client.InvokeRPC(context.Background(), EventFlowID, EventInventory.ReleaseSeat, RegistrationReference{RegistrationID: flowID, UpdatedAt: now}, &ignored)
		}
		return RegistrationView{}, err
	}
	return RegistrationView{
		RegistrationToken: registrationToken, Status: StatusStarting,
		FirstName: input.FirstName, LastName: input.LastName, Email: input.Email,
		AmountMinor: event.PriceMinor, Currency: event.Currency,
		Message: "Registration accepted. Preparing your secure ACH checkout.",
	}, nil
}

func (service *Service) GetRegistration(ctx context.Context, token string) (RegistrationView, error) {
	flowID, err := service.tokens.Verify(token, "registration")
	if err != nil {
		return RegistrationView{}, err
	}
	state, err := service.getRegistrationState(ctx, flowID)
	if err != nil {
		return RegistrationView{}, err
	}
	return RegistrationView{
		RegistrationToken: token, Status: state.Status, FirstName: state.FirstName, LastName: state.LastName, Email: state.Email,
		AmountMinor: state.Event.PriceMinor, Currency: state.Event.Currency, CheckoutURL: state.CheckoutURL, TicketURL: paidTicketURL(state), Message: state.Message,
	}, nil
}

func (service *Service) GetTicket(ctx context.Context, token string) (TicketView, error) {
	flowID, err := service.tokens.Verify(token, "ticket")
	if err != nil {
		return TicketView{}, err
	}
	state, err := service.getRegistrationState(ctx, flowID)
	if err != nil {
		return TicketView{}, err
	}
	if !registrationIsPaid(state) {
		return TicketView{}, ErrTicketNotPaid
	}
	png, err := qrcode.Encode(state.TicketURL, qrcode.Medium, 320)
	if err != nil {
		return TicketView{}, fmt.Errorf("encode ticket QR code: %w", err)
	}
	return TicketView{
		Event: state.Event, AttendeeName: attendeeName(state), TicketCode: state.TicketCode, TicketURL: state.TicketURL,
		QRCodeDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		CheckedIn:     !state.CheckedInAt.IsZero(), CheckedInAt: state.CheckedInAt,
	}, nil
}

func (service *Service) CheckIn(ctx context.Context, ticketToken string, scannedAt time.Time) (CheckInResult, error) {
	flowID, err := service.tokens.Verify(ticketToken, "ticket")
	if err != nil {
		return CheckInResult{}, err
	}
	var result CheckInResult
	if err := service.client.InvokeRPC(ctx, flowID, service.registrations.CheckIn, CheckInInput{ScannedAt: scannedAt.UTC()}, &result); err != nil {
		return CheckInResult{}, classifyUnknownRegistration(err)
	}
	return result, nil
}

func (service *Service) getRegistrationState(ctx context.Context, flowID string) (RegistrationState, error) {
	var state RegistrationState
	err := service.client.InvokeRPC(ctx, flowID, service.registrations.DescribeRegistration, nil, &state)
	if err != nil {
		return RegistrationState{}, classifyUnknownRegistration(err)
	}
	return state, nil
}

func classifyUnknownRegistration(err error) error {
	var missing *dex.FlowNotFoundError
	if errors.As(err, &missing) {
		return ErrUnknownRegistration
	}
	var worker *dex.WorkerInvocationError
	if errors.As(err, &worker) && worker.Detail == "ticket is not paid" {
		return ErrTicketNotPaid
	}
	return err
}

func registrationIsPaid(state RegistrationState) bool {
	return state.PaymentIntentID != "" && (state.Status == StatusPaid || state.Status == StatusTicketEmailed || state.Status == StatusNeedsAttention)
}

func paidTicketURL(state RegistrationState) string {
	if registrationIsPaid(state) {
		return state.TicketURL
	}
	return ""
}
