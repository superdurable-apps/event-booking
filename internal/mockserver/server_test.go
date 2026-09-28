package mockserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/superdurable-apps/event-booking/internal/api/generated"
)

func TestServerCoversRegistrationSettlementTicketAndCheckIn(t *testing.T) {
	handler, err := newWithWait(NewStore(nil), func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	assertStatus(t, request(t, handler, http.MethodGet, "/api/event", nil, nil), http.StatusOK)
	createdResponse := request(t, handler, http.MethodPost, "/api/registrations", map[string]any{
		"firstName": "Ada", "lastName": "Lovelace", "email": "ada@example.com", "acceptedPolicies": true,
	}, nil)
	if createdResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", createdResponse.StatusCode, readBody(t, createdResponse))
	}
	var created generated.RegistrationCreated
	decode(t, createdResponse, &created)
	registrationURL := "/api/registrations/" + created.RegistrationId.String()
	assertStatus(t, request(t, handler, http.MethodGet, registrationURL, nil, map[string]string{"X-Registration-Token": created.AccessToken}), http.StatusOK)

	settle := request(t, handler, http.MethodPost, "/__mock__/control", ControlRequest{Action: "settle_payment", RegistrationID: created.RegistrationId.String()}, nil)
	assertStatus(t, settle, http.StatusOK)
	ticketToken := NewStore(nil).tokens.TicketToken(created.RegistrationId.String())
	ticketURL := "/api/tickets/" + created.RegistrationId.String() + "?token=" + ticketToken
	assertStatus(t, request(t, handler, http.MethodGet, ticketURL, nil, nil), http.StatusOK)
	assertStatus(t, request(t, handler, http.MethodGet, "/api/tickets/"+created.RegistrationId.String()+"/qr.png?token="+ticketToken, nil, nil), http.StatusOK)

	publicTicketURL := "http://localhost:5173/ticket/" + created.RegistrationId.String() + "?token=" + ticketToken
	checkIn := request(t, handler, http.MethodPost, "/api/check-in", map[string]string{"ticketCode": publicTicketURL}, map[string]string{"X-Staff-Token": MockStaffToken})
	assertStatus(t, checkIn, http.StatusOK)
}

func request(t *testing.T, handler http.Handler, method, target string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Result()
}

func decode(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	contents, _ := io.ReadAll(response.Body)
	return string(contents)
}

func assertStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != want {
		contents, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d, body = %s", response.StatusCode, want, contents)
	}
}
