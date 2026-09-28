package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
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
	view, err := handler.registrations.Event(ctx)
	if err != nil {
		return eventError("event_unavailable", "event availability is temporarily unavailable"), nil
	}
	return &generated.EventView{
		ID: view.Config.ID, Name: view.Config.Name, Description: view.Config.Description,
		DateTimeDisplay: view.Config.DateTimeDisplay, Location: view.Config.Location, PriceDisplay: view.Config.PriceDisplay,
		Capacity: view.Config.Capacity, ReservedCount: view.ReservedCount, RegistrationOpen: view.Config.RegistrationOpen,
		RegistrationDeadlineDisplay: view.Config.RegistrationDeadlineDisplay, OrganizerEmail: view.Config.OrganizerEmail,
	}, nil
}

func (handler *Handler) CreateRegistration(ctx context.Context, request *generated.CreateRegistrationRequest) (generated.CreateRegistrationRes, error) {
	created, err := handler.registrations.Start(ctx, request.FirstName, request.LastName, request.Email)
	switch {
	case err == nil:
		statusURL, _ := url.Parse(created.StatusURL)
		return &generated.RegistrationCreated{
			RegistrationId: created.RegistrationID, DisplayCode: created.DisplayCode, AccessToken: created.AccessToken,
			StatusUrl: *statusURL, State: generated.RegistrationState(created.State),
		}, nil
	case errors.Is(err, process.ErrRegistrationClosed):
		response := generated.CreateRegistrationConflict(errorResponse("registration_closed", "registration is not open"))
		return &response, nil
	case strings.Contains(err.Error(), "required"):
		response := generated.CreateRegistrationBadRequest(errorResponse("invalid_registration", "name and a valid email are required"))
		return &response, nil
	default:
		response := generated.CreateRegistrationServiceUnavailable(errorResponse("registration_start_failed", "unable to start registration"))
		return &response, nil
	}
}

func (handler *Handler) GetRegistration(ctx context.Context, params generated.GetRegistrationParams) (generated.GetRegistrationRes, error) {
	view, err := handler.registrations.Get(ctx, params.RegistrationId.String(), params.XRegistrationToken)
	switch {
	case err == nil:
		return generatedRegistration(view), nil
	case errors.Is(err, process.ErrUnauthorized):
		response := generated.GetRegistrationUnauthorized(errorResponse("unauthorized", "registration token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.GetRegistrationNotFound(errorResponse("registration_not_found", "registration was not found"))
		return &response, nil
	default:
		response := generated.GetRegistrationServiceUnavailable(errorResponse("registration_unavailable", "registration status is temporarily unavailable"))
		return &response, nil
	}
}

func (handler *Handler) GetTicket(ctx context.Context, params generated.GetTicketParams) (generated.GetTicketRes, error) {
	view, err := handler.registrations.Ticket(ctx, params.RegistrationId.String(), params.Token)
	switch {
	case err == nil:
		qrURL, _ := url.Parse(view.QRImageURL)
		result := &generated.TicketView{
			RegistrationId: params.RegistrationId, DisplayCode: view.DisplayCode,
			AttendeeName: strings.TrimSpace(view.Record.FirstName + " " + view.Record.LastName),
			EventName:    view.Event.Name, DateTimeDisplay: view.Event.DateTimeDisplay,
			Location: view.Event.Location, QrImageUrl: *qrURL,
			EmailDeliveryStatus: generated.EmailDeliveryStatus(view.Record.EmailDeliveryStatus),
			CheckedIn:           view.Record.CheckedInAt != nil,
		}
		if view.Record.CheckedInAt != nil {
			result.CheckedInAt = generated.NewOptDateTime(*view.Record.CheckedInAt)
		}
		return result, nil
	case errors.Is(err, process.ErrUnauthorized):
		response := generated.GetTicketUnauthorized(errorResponse("unauthorized", "ticket token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.GetTicketNotFound(errorResponse("ticket_not_found", "ticket was not found"))
		return &response, nil
	case errors.Is(err, process.ErrTicketUnavailable):
		response := generated.GetTicketConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
		return &response, nil
	default:
		response := generated.GetTicketServiceUnavailable(errorResponse("ticket_unavailable", "ticket is temporarily unavailable"))
		return &response, nil
	}
}

func (handler *Handler) GetTicketQR(ctx context.Context, params generated.GetTicketQRParams) (generated.GetTicketQRRes, error) {
	png, err := handler.registrations.TicketQR(ctx, params.RegistrationId.String(), params.Token)
	switch {
	case err == nil:
		return &generated.GetTicketQROK{Data: bytes.NewReader(png)}, nil
	case errors.Is(err, process.ErrUnauthorized):
		response := generated.GetTicketQRUnauthorized(errorResponse("unauthorized", "ticket token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.GetTicketQRNotFound(errorResponse("ticket_not_found", "ticket was not found"))
		return &response, nil
	case errors.Is(err, process.ErrTicketUnavailable):
		response := generated.GetTicketQRConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
		return &response, nil
	default:
		response := generated.GetTicketQRServiceUnavailable(errorResponse("ticket_unavailable", "ticket is temporarily unavailable"))
		return &response, nil
	}
}

func (handler *Handler) CheckInTicket(ctx context.Context, request *generated.CheckInRequest, params generated.CheckInTicketParams) (generated.CheckInTicketRes, error) {
	view, err := handler.registrations.CheckIn(ctx, params.XStaffToken, request.TicketCode)
	switch {
	case err == nil:
		result := generated.CheckInResultCheckedIn
		message := "Ticket checked in."
		if view.AlreadyCheckedIn {
			result = generated.CheckInResultAlreadyCheckedIn
			message = "Ticket was already checked in."
		}
		return &generated.CheckInView{
			Result: result, RegistrationId: mustUUID(view.Record.RegistrationID), DisplayCode: view.DisplayCode,
			AttendeeName: strings.TrimSpace(view.Record.FirstName + " " + view.Record.LastName), CheckedInAt: *view.Record.CheckedInAt, Message: message,
		}, nil
	case errors.Is(err, process.ErrUnauthorized):
		response := generated.CheckInTicketUnauthorized(errorResponse("unauthorized", "staff token is invalid"))
		return &response, nil
	case errors.Is(err, process.ErrInvalidTicket):
		response := generated.CheckInTicketNotFound(errorResponse("ticket_not_found", "ticket is invalid or was not found"))
		return &response, nil
	case errors.Is(err, process.ErrUnknownRegistration):
		response := generated.CheckInTicketNotFound(errorResponse("ticket_not_found", "ticket was not found"))
		return &response, nil
	case errors.Is(err, process.ErrTicketUnavailable):
		response := generated.CheckInTicketConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
		return &response, nil
	default:
		response := generated.CheckInTicketServiceUnavailable(errorResponse("check_in_unavailable", "check-in is temporarily unavailable"))
		return &response, nil
	}
}

func generatedRegistration(view process.RegistrationView) *generated.RegistrationView {
	registrationID := mustUUID(view.Record.RegistrationID)
	response := &generated.RegistrationView{
		RegistrationId: registrationID, DisplayCode: view.DisplayCode,
		AttendeeName: strings.TrimSpace(view.Record.FirstName + " " + view.Record.LastName), EmailMasked: view.EmailMasked,
		State: generated.RegistrationState(view.Record.State), PaymentStatus: generated.PaymentStatus(view.Record.PaymentStatus),
		TicketStatus: generated.TicketStatus(view.Record.TicketStatus), EmailDeliveryStatus: generated.EmailDeliveryStatus(view.Record.EmailDeliveryStatus),
		Message: view.Message,
	}
	if view.Record.CheckoutURL != "" {
		if checkoutURL, err := url.Parse(view.Record.CheckoutURL); err == nil {
			response.CheckoutUrl = generated.NewOptURI(*checkoutURL)
		}
	}
	if view.TicketURL != "" {
		if ticketURL, err := url.Parse(view.TicketURL); err == nil {
			response.TicketUrl = generated.NewOptURI(*ticketURL)
		}
	}
	if view.Record.CheckedInAt != nil {
		response.CheckedInAt = generated.NewOptDateTime(*view.Record.CheckedInAt)
	}
	return response
}

func mustUUID(raw string) uuid.UUID {
	parsed, _ := uuid.Parse(raw)
	return parsed
}

func eventError(code, message string) *generated.ErrorResponse {
	return &generated.ErrorResponse{Error: code, Message: message}
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
