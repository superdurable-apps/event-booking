package mockserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/superdurable-apps/event-booking/internal/api/generated"
)

const operationDelay = 150 * time.Millisecond

type waitFunc func(context.Context, time.Duration) error

type Handler struct {
	store *Store
	wait  waitFunc
}

type ControlRequest struct {
	Action         string `json:"action"`
	RegistrationID string `json:"registrationId,omitempty"`
}

func New(store *Store) (http.Handler, error) { return newWithWait(store, waitForDuration) }

func newWithWait(store *Store, wait waitFunc) (http.Handler, error) {
	if store == nil {
		return nil, errors.New("mock store is required")
	}
	handler := &Handler{store: store, wait: wait}
	apiServer, err := generated.NewServer(handler,
		generated.WithErrorHandler(writeGeneratedError),
		generated.WithNotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "unknown API route")
		}),
		generated.WithMethodNotAllowed(func(w http.ResponseWriter, _ *http.Request, allowed string) {
			w.Header().Set("Allow", allowed)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create mock OpenAPI server: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", apiServer)
	mux.HandleFunc("/__mock__/control", handler.handleControl)
	return mux, nil
}

func (handler *Handler) GetHealth(context.Context) (*generated.HealthResponse, error) {
	return &generated.HealthResponse{Status: generated.HealthResponseStatusOk}, nil
}

func (handler *Handler) GetEvent(context.Context) (generated.GetEventRes, error) {
	view := handler.store.Event()
	return &view, nil
}

func (handler *Handler) CreateRegistration(ctx context.Context, request *generated.CreateRegistrationRequest) (generated.CreateRegistrationRes, error) {
	if err := handler.wait(ctx, operationDelay); err != nil {
		return nil, err
	}
	created := handler.store.Create(request.FirstName, request.LastName, request.Email)
	return &created, nil
}

func (handler *Handler) GetRegistration(_ context.Context, params generated.GetRegistrationParams) (generated.GetRegistrationRes, error) {
	view, err := handler.store.Registration(params.RegistrationId.String(), params.XRegistrationToken)
	switch {
	case err == nil:
		return &view, nil
	case errors.Is(err, ErrUnauthorized):
		response := generated.GetRegistrationUnauthorized(errorResponse("unauthorized", "registration token is invalid"))
		return &response, nil
	case errors.Is(err, ErrUnknownRegistration):
		response := generated.GetRegistrationNotFound(errorResponse("registration_not_found", "registration was not found"))
		return &response, nil
	default:
		return nil, err
	}
}

func (handler *Handler) GetTicket(_ context.Context, params generated.GetTicketParams) (generated.GetTicketRes, error) {
	view, err := handler.store.Ticket(params.RegistrationId.String(), params.Token)
	switch {
	case err == nil:
		return &view, nil
	case errors.Is(err, ErrUnauthorized):
		response := generated.GetTicketUnauthorized(errorResponse("unauthorized", "ticket token is invalid"))
		return &response, nil
	case errors.Is(err, ErrUnknownRegistration):
		response := generated.GetTicketNotFound(errorResponse("ticket_not_found", "ticket was not found"))
		return &response, nil
	case errors.Is(err, ErrTicketUnavailable):
		response := generated.GetTicketConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
		return &response, nil
	default:
		return nil, err
	}
}

func (handler *Handler) GetTicketQR(_ context.Context, params generated.GetTicketQRParams) (generated.GetTicketQRRes, error) {
	if _, err := handler.store.Ticket(params.RegistrationId.String(), params.Token); err != nil {
		switch {
		case errors.Is(err, ErrUnauthorized):
			response := generated.GetTicketQRUnauthorized(errorResponse("unauthorized", "ticket token is invalid"))
			return &response, nil
		case errors.Is(err, ErrUnknownRegistration):
			response := generated.GetTicketQRNotFound(errorResponse("ticket_not_found", "ticket was not found"))
			return &response, nil
		case errors.Is(err, ErrTicketUnavailable):
			response := generated.GetTicketQRConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
			return &response, nil
		default:
			return nil, err
		}
	}
	png, err := qrcode.Encode(handler.store.TicketURL(params.RegistrationId.String()), qrcode.Medium, 320)
	if err != nil {
		return nil, err
	}
	return &generated.GetTicketQROK{Data: bytes.NewReader(png)}, nil
}

func (handler *Handler) CheckInTicket(_ context.Context, request *generated.CheckInRequest, params generated.CheckInTicketParams) (generated.CheckInTicketRes, error) {
	view, err := handler.store.CheckIn(params.XStaffToken, request.TicketCode)
	switch {
	case err == nil:
		return &view, nil
	case errors.Is(err, ErrUnauthorized):
		response := generated.CheckInTicketUnauthorized(errorResponse("unauthorized", "staff token is invalid"))
		return &response, nil
	case errors.Is(err, ErrUnknownRegistration):
		response := generated.CheckInTicketNotFound(errorResponse("ticket_not_found", "ticket is invalid or was not found"))
		return &response, nil
	case errors.Is(err, ErrTicketUnavailable):
		response := generated.CheckInTicketConflict(errorResponse("ticket_not_ready", "ticket is not available until ACH payment settles"))
		return &response, nil
	default:
		return nil, err
	}
}

func (handler *Handler) handleControl(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		view, err := handler.store.Control(request.URL.Query().Get("registrationId"))
		writeControlResponse(w, view, err)
		return
	}
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	defer request.Body.Close()
	var control ControlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&control); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_control", "mock control request is invalid")
		return
	}
	var (
		view ControlView
		err  error
	)
	if control.Action == "reset" {
		view = handler.store.Reset()
	} else {
		view, err = handler.store.SetState(control.Action, control.RegistrationID)
	}
	writeControlResponse(w, view, err)
}

func writeControlResponse(w http.ResponseWriter, view ControlView, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, view)
	case errors.Is(err, ErrUnknownRegistration):
		writeError(w, http.StatusNotFound, "registration_not_found", "registration was not found")
	default:
		writeError(w, http.StatusConflict, "invalid_control_state", err.Error())
	}
}

func waitForDuration(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func errorResponse(code, message string) generated.ErrorResponse {
	return generated.ErrorResponse{Error: code, Message: message}
}

func writeGeneratedError(_ context.Context, w http.ResponseWriter, _ *http.Request, _ error) {
	writeError(w, http.StatusBadRequest, "invalid_request", "request does not match the OpenAPI contract")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, generated.ErrorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
