package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProductionHandlerDoesNotExposeMockControls(t *testing.T) {
	handler := applicationHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodGet, "/__mock__/control", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("mock control status = %d, want %d", response.Code, http.StatusNotFound)
	}
}
