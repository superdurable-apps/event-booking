package process

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"github.com/superdurable/dex/sdk-go/dex"
)

var (
	ErrUnknownRegistration = errors.New("unknown registration")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrRegistrationClosed  = errors.New("registration is closed")
	ErrTicketUnavailable   = errors.New("ticket is not available")
	ErrInvalidTicket       = errors.New("ticket is invalid")
)

type TokenSigner struct {
	key     []byte
	baseURL string
}

func NewTokenSigner(key, baseURL string) (*TokenSigner, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("ticket signing key must contain at least 32 bytes")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("public base URL must be absolute")
	}
	return &TokenSigner{key: []byte(key), baseURL: strings.TrimRight(baseURL, "/")}, nil
}

func (signer *TokenSigner) token(purpose, registrationID string) string {
	mac := hmac.New(sha256.New, signer.key)
	_, _ = mac.Write([]byte(purpose + ":" + registrationID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (signer *TokenSigner) AccessToken(registrationID string) string {
	return signer.token("access", registrationID)
}

func (signer *TokenSigner) TicketToken(registrationID string) string {
	return signer.token("ticket", registrationID)
}

func (signer *TokenSigner) VerifyAccess(registrationID, candidate string) bool {
	return constantTimeEqual(signer.AccessToken(registrationID), candidate)
}

func (signer *TokenSigner) VerifyTicket(registrationID, candidate string) bool {
	return constantTimeEqual(signer.TicketToken(registrationID), candidate)
}

func (signer *TokenSigner) StatusURL(registrationID string) string {
	return signer.baseURL + "/registration/" + url.PathEscape(registrationID) + "#token=" + signer.AccessToken(registrationID)
}

func (signer *TokenSigner) TicketURL(registrationID string) string {
	return signer.baseURL + "/ticket/" + url.PathEscape(registrationID) + "?token=" + url.QueryEscape(signer.TicketToken(registrationID))
}

func (signer *TokenSigner) QRURL(registrationID string) string {
	return signer.baseURL + "/api/tickets/" + url.PathEscape(registrationID) + "/qr.png?token=" + url.QueryEscape(signer.TicketToken(registrationID))
}

func constantTimeEqual(expected, candidate string) bool {
	if len(expected) != len(candidate) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(candidate)) == 1
}

type RegistrationCreated struct {
	RegistrationID uuid.UUID
	DisplayCode    string
	AccessToken    string
	StatusURL      string
	State          RegistrationState
}

type RegistrationView struct {
	Record      RegistrationRecord
	DisplayCode string
	EmailMasked string
	Message     string
	TicketURL   string
}

type EventView struct {
	Config        EventConfig
	ReservedCount int64
}

type TicketView struct {
	Record      RegistrationRecord
	DisplayCode string
	QRImageURL  string
	Event       EventConfig
}

type CheckInView struct {
	Record           RegistrationRecord
	DisplayCode      string
	AlreadyCheckedIn bool
}

type Service struct {
	client     *dex.Client
	flow       *RegistrationFlow
	capacity   *CapacityClient
	config     EventConfig
	tokens     *TokenSigner
	staffToken string
}

func NewService(client *dex.Client, flow *RegistrationFlow, capacity *CapacityClient, config EventConfig, tokens *TokenSigner, staffToken string) (*Service, error) {
	if client == nil || flow == nil || capacity == nil || tokens == nil {
		return nil, fmt.Errorf("registration service dependencies are required")
	}
	if len(staffToken) < 16 {
		return nil, fmt.Errorf("staff check-in token must contain at least 16 bytes")
	}
	return &Service{client: client, flow: flow, capacity: capacity, config: config, tokens: tokens, staffToken: staffToken}, nil
}

func (service *Service) Start(ctx context.Context, firstName, lastName, email string) (RegistrationCreated, error) {
	if !service.config.RegistrationOpen {
		return RegistrationCreated{}, ErrRegistrationClosed
	}
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if firstName == "" || lastName == "" || err != nil || address.Address != email {
		return RegistrationCreated{}, fmt.Errorf("name and a plain email address are required")
	}
	id := uuid.New()
	registrationID := id.String()
	input := RegistrationInput{RegistrationID: registrationID, FirstName: firstName, LastName: lastName, Email: email}
	if _, err := service.client.StartFlow(ctx, service.flow, registrationFlowID(registrationID), input, dex.StartFlowOptions{IDReusePolicy: dex.IDReuseDisallow}); err != nil {
		return RegistrationCreated{}, err
	}
	return RegistrationCreated{
		RegistrationID: id, DisplayCode: displayCode(registrationID), AccessToken: service.tokens.AccessToken(registrationID),
		StatusURL: service.tokens.StatusURL(registrationID), State: StateReserving,
	}, nil
}

func (service *Service) Event(ctx context.Context) (EventView, error) {
	snapshot, err := service.capacity.Get(ctx)
	if err != nil {
		return EventView{}, err
	}
	config := service.config
	config.Capacity = snapshot.Capacity
	return EventView{Config: config, ReservedCount: snapshot.ReservedCount}, nil
}

func (service *Service) Get(ctx context.Context, registrationID, accessToken string) (RegistrationView, error) {
	if !service.tokens.VerifyAccess(registrationID, accessToken) {
		return RegistrationView{}, ErrUnauthorized
	}
	record, err := service.getRecord(ctx, registrationID)
	if err != nil {
		return RegistrationView{}, err
	}
	view := RegistrationView{Record: record, DisplayCode: displayCode(registrationID), EmailMasked: maskEmail(record.Email), Message: stateMessage(record.State)}
	if ticketAvailable(record) {
		view.TicketURL = service.tokens.TicketURL(registrationID)
	}
	return view, nil
}

func (service *Service) Ticket(ctx context.Context, registrationID, ticketToken string) (TicketView, error) {
	if !service.tokens.VerifyTicket(registrationID, ticketToken) {
		return TicketView{}, ErrUnauthorized
	}
	record, err := service.getRecord(ctx, registrationID)
	if err != nil {
		return TicketView{}, err
	}
	if !ticketAvailable(record) {
		return TicketView{}, ErrTicketUnavailable
	}
	return TicketView{Record: record, DisplayCode: displayCode(registrationID), QRImageURL: service.tokens.QRURL(registrationID), Event: service.config}, nil
}

func (service *Service) TicketQR(ctx context.Context, registrationID, ticketToken string) ([]byte, error) {
	if _, err := service.Ticket(ctx, registrationID, ticketToken); err != nil {
		return nil, err
	}
	return qrcode.Encode(service.tokens.TicketURL(registrationID), qrcode.Medium, 320)
}

func (service *Service) CheckIn(ctx context.Context, staffToken, ticketCode string) (CheckInView, error) {
	if !constantTimeEqual(service.staffToken, staffToken) {
		return CheckInView{}, ErrUnauthorized
	}
	registrationID, ticketToken, err := parseTicketCode(ticketCode)
	if err != nil || !service.tokens.VerifyTicket(registrationID, ticketToken) {
		return CheckInView{}, ErrInvalidTicket
	}
	record, err := service.getRecord(ctx, registrationID)
	if err != nil {
		return CheckInView{}, err
	}
	if record.CheckedInAt != nil || record.State == StateCheckedIn {
		return CheckInView{Record: record, DisplayCode: displayCode(registrationID), AlreadyCheckedIn: true}, nil
	}
	if !ticketAvailable(record) {
		return CheckInView{}, ErrTicketUnavailable
	}
	var output dex.None
	err = service.client.InvokeRPC(ctx, registrationFlowID(registrationID), service.flow.CheckIn, nil, &output)
	if err != nil {
		var inactive *dex.FlowNotActiveError
		if !errors.As(err, &inactive) {
			var missing *dex.FlowNotFoundError
			if errors.As(err, &missing) {
				return CheckInView{}, ErrUnknownRegistration
			}
			return CheckInView{}, err
		}
		updated, getErr := service.getRecord(ctx, registrationID)
		if getErr != nil {
			return CheckInView{}, getErr
		}
		if updated.State != StateCheckedIn || updated.CheckedInAt == nil {
			return CheckInView{}, err
		}
		return CheckInView{Record: updated, DisplayCode: displayCode(registrationID), AlreadyCheckedIn: true}, nil
	}
	record, err = service.getRecord(ctx, registrationID)
	if err != nil {
		return CheckInView{}, err
	}
	return CheckInView{Record: record, DisplayCode: displayCode(registrationID)}, nil
}

func (service *Service) getRecord(ctx context.Context, registrationID string) (RegistrationRecord, error) {
	if _, err := uuid.Parse(registrationID); err != nil {
		return RegistrationRecord{}, ErrUnknownRegistration
	}
	var record RegistrationRecord
	err := service.client.InvokeRPC(ctx, registrationFlowID(registrationID), service.flow.DescribeRegistration, nil, &record)
	if err == nil {
		return record, nil
	}
	var inactive *dex.FlowNotActiveError
	if !errors.As(err, &inactive) {
		var missing *dex.FlowNotFoundError
		if errors.As(err, &missing) {
			return RegistrationRecord{}, ErrUnknownRegistration
		}
		return RegistrationRecord{}, err
	}
	result, err := service.client.WaitForFlow(ctx, registrationFlowID(registrationID), dex.WaitForFlowOptions{NeedsResults: true})
	if err != nil {
		var missing *dex.FlowNotFoundError
		if errors.As(err, &missing) {
			return RegistrationRecord{}, ErrUnknownRegistration
		}
		return RegistrationRecord{}, err
	}
	if result.Status != dex.FlowCompleted {
		return RegistrationRecord{}, fmt.Errorf("registration Flow ended with status %s", result.Status)
	}
	var output RegistrationResult
	if err := result.DecodeSingleOutput(&output); err != nil {
		return RegistrationRecord{}, err
	}
	return output.Record, nil
}

func registrationFlowID(registrationID string) string { return "registration-" + registrationID }

func displayCode(registrationID string) string {
	compact := strings.ToUpper(strings.ReplaceAll(registrationID, "-", ""))
	if len(compact) > 8 {
		compact = compact[len(compact)-8:]
	}
	return compact
}

func maskEmail(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" {
		return "***"
	}
	local := parts[0][:1]
	if len(parts[0]) > 1 {
		local += strings.Repeat("*", len(parts[0])-1)
	}
	return local + "@" + parts[1]
}

func stateMessage(state RegistrationState) string {
	switch state {
	case StateReserving, StateCheckoutCreating:
		return "We are reserving your place and preparing secure payment."
	case StateCapacityFull:
		return "This event is currently full. No payment was created."
	case StatePaymentPending:
		return "Complete ACH payment in Stripe. Bank settlement can take several business days."
	case StatePaymentReviewRequired:
		return "Payment setup needs organizer review. Your place remains reserved."
	case StatePaymentFailed:
		return "The ACH payment failed and the reserved place was released."
	case StatePaymentExpired:
		return "The payment session expired and the reserved place was released."
	case StatePaid, StateTicketSending:
		return "Payment settled. We are sending your electronic ticket."
	case StateTicketReady:
		return "Your electronic ticket is ready."
	case StateTicketEmailReviewRequired:
		return "Your ticket is ready online, but email delivery needs organizer review."
	case StateCheckedIn:
		return "Ticket checked in."
	default:
		return "Registration is processing."
	}
}

func parseTicketCode(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) >= 2 && segments[len(segments)-2] == "ticket" {
			return segments[len(segments)-1], parsed.Query().Get("token"), nil
		}
	}
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", "", ErrInvalidTicket
}
