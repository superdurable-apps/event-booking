package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/superdurable-apps/event-booking/internal/api/generated"
	"github.com/superdurable-apps/event-booking/internal/process"
)

type Handler struct{ registrations *process.Service }

func NewHandler(registrations *process.Service) (*generated.Server, error) {
	handler := &Handler{registrations: registrations}
	return generated.NewServer(handler,
		generated.WithErrorHandler(writeGeneratedError),
		generated.WithNotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "unknown API route")
		}),
		generated.WithMethodNotAllowed(func(w http.ResponseWriter, _ *http.Request, allowed string) {
			w.Header().Set("Allow", allowed)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		}),
	)
}

func (handler *Handler) GetHealth(context.Context) (*generated.HealthResponse, error) {
	return &generated.HealthResponse{Status: generated.HealthResponseStatusOk}, nil
}

func (handler *Handler) GetEvent(ctx context.Context) (generated.GetEventRes, error) {
	event, err := handler.registrations.GetEvent(ctx)
	if err != nil {
		response := errorResponse("event_unavailable", "event inventory is temporarily unavailable")
		return &response, nil
	}
	return generatedEvent(event), nil
}

func (handler *Handler) CreateRegistration(ctx context.Context, request *generated.CreateRegistrationRequest) (generated.CreateRegistrationRes, error) {
	view, err := handler.registrations.StartRegistration(ctx, process.NewRegistration{
		RequestID: request.RequestId, FirstName: request.FirstName, LastName: request.LastName, Email: request.Email, AcceptedAt: time.Now().UTC(),
	})
	if err != nil {
		response := generated.CreateRegistrationServiceUnavailable(errorResponse("registration_start_failed", "unable to start registration"))
		return &response, nil
	}
	return generatedRegistration(view), nil
}

func (handler *Handler) GetRegistration(ctx context.Context, params generated.GetRegistrationParams) (generated.GetRegistrationRes, error) {
	view, err := handler.registrations.GetRegistration(ctx, params.RegistrationToken)
	switch {
	case err == nil:
		return generatedRegistration(view), nil
	case errors.Is(err, process.ErrInvalidToken):
		response := generated.GetRegistrationBadRequest(errorResponse("invalid_registration_token", "registration token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.GetRegistrationNotFound(errorResponse("unknown_registration", "registration was not found"))
		return &response, nil
	default:
		response := generated.GetRegistrationServiceUnavailable(errorResponse("registration_unavailable", "registration is temporarily unavailable"))
		return &response, nil
	}
}

func (handler *Handler) GetTicket(ctx context.Context, params generated.GetTicketParams) (generated.GetTicketRes, error) {
	view, err := handler.registrations.GetTicket(ctx, params.TicketToken)
	switch {
	case err == nil:
		return generatedTicket(view), nil
	case errors.Is(err, process.ErrInvalidToken):
		response := generated.GetTicketBadRequest(errorResponse("invalid_ticket_token", "ticket token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.GetTicketNotFound(errorResponse("unknown_ticket", "ticket was not found"))
		return &response, nil
	case errors.Is(err, process.ErrTicketNotPaid):
		response := generated.GetTicketConflict(errorResponse("ticket_not_paid", "ticket is not available until ACH payment succeeds"))
		return &response, nil
	default:
		response := generated.GetTicketServiceUnavailable(errorResponse("ticket_unavailable", "ticket is temporarily unavailable"))
		return &response, nil
	}
}

func (handler *Handler) CheckInTicket(ctx context.Context, request *generated.CheckInRequest, params generated.CheckInTicketParams) (generated.CheckInTicketRes, error) {
	if !hasPermission(params.XEventPermissions, "checkin.scan") {
		response := generated.CheckInTicketUnauthorized(errorResponse("missing_permission", "checkin.scan permission is required"))
		return &response, nil
	}
	result, err := handler.registrations.CheckIn(ctx, request.TicketToken, time.Now().UTC())
	switch {
	case err == nil:
		return &generated.CheckInView{
			Status: generated.CheckInViewStatus(result.Status), AttendeeName: result.AttendeeName,
			TicketCode: result.TicketCode, CheckedInAt: result.CheckedInAt,
		}, nil
	case errors.Is(err, process.ErrInvalidToken):
		response := generated.CheckInTicketBadRequest(errorResponse("invalid_ticket_token", "ticket token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.CheckInTicketNotFound(errorResponse("unknown_ticket", "ticket was not found"))
		return &response, nil
	case errors.Is(err, process.ErrTicketNotPaid):
		response := generated.CheckInTicketConflict(errorResponse("ticket_not_paid", "ticket payment is not confirmed"))
		return &response, nil
	default:
		response := generated.CheckInTicketServiceUnavailable(errorResponse("checkin_unavailable", "check-in is temporarily unavailable"))
		return &response, nil
	}
}

func generatedEvent(view process.EventView) *generated.EventView {
	return &generated.EventView{
		EventId: view.EventID, Name: view.Name, Description: view.Description, StartsAt: view.StartsAt,
		Timezone: view.Timezone, Location: view.Location, Currency: strings.ToUpper(view.Currency), PriceMinor: view.PriceMinor,
		Capacity: view.Capacity, Reserved: view.Reserved, Paid: view.Paid, Remaining: view.Remaining, RegistrationOpen: view.RegistrationOpen,
	}
}

func generatedRegistration(view process.RegistrationView) *generated.RegistrationView {
	response := &generated.RegistrationView{
		RegistrationToken: view.RegistrationToken, Status: generated.RegistrationStatus(view.Status), FirstName: view.FirstName,
		LastName: view.LastName, Email: view.Email, AmountMinor: view.AmountMinor, Currency: strings.ToUpper(view.Currency),
	}
	if value, err := url.Parse(view.CheckoutURL); err == nil && value.IsAbs() {
		response.CheckoutUrl = generated.NewOptURI(*value)
	}
	if value, err := url.Parse(view.TicketURL); err == nil && value.IsAbs() {
		response.TicketUrl = generated.NewOptURI(*value)
	}
	if view.Message != "" {
		response.Message = generated.NewOptString(view.Message)
	}
	return response
}

func generatedTicket(view process.TicketView) *generated.TicketView {
	ticketURL, _ := url.Parse(view.TicketURL)
	response := &generated.TicketView{
		Event: *generatedEvent(view.Event), AttendeeName: view.AttendeeName, TicketCode: view.TicketCode,
		TicketUrl: *ticketURL, QrCodeDataUrl: view.QRCodeDataURL, CheckedIn: view.CheckedIn,
	}
	if !view.CheckedInAt.IsZero() {
		response.CheckedInAt = generated.NewOptDateTime(view.CheckedInAt)
	}
	return response
}

func hasPermission(header, permission string) bool {
	for _, value := range strings.FieldsFunc(header, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if value == permission {
			return true
		}
	}
	return false
}

func errorResponse(code, message string) generated.ErrorResponse {
	return generated.ErrorResponse{Error: code, Message: message}
}

func writeGeneratedError(_ context.Context, w http.ResponseWriter, _ *http.Request, _ error) {
	writeError(w, http.StatusBadRequest, "invalid_request", "request does not match the OpenAPI contract")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(generated.ErrorResponse{Error: code, Message: message})
}
