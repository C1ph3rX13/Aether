package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, wantStatus int, target *T) {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &payload)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s: got %d, want %d; body: %s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if target != nil {
		if err := json.NewDecoder(recorder.Body).Decode(target); err != nil {
			t.Fatalf("decode %s %s: %v; body: %s", method, path, err, recorder.Body.String())
		}
	}
}
