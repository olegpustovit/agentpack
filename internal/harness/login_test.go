package harness

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshTokenTellsARefusalFromAnOutage(t *testing.T) {
	for status, refused := range map[int]bool{http.StatusBadRequest: true, http.StatusUnauthorized: true, http.StatusServiceUnavailable: false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		err := RefreshToken(t.Context(), server.URL, map[string]string{}, &struct{}{})
		server.Close()
		var refusal RefreshRefused
		if err == nil || errors.As(err, &refusal) != refused {
			t.Fatalf("%d: %v", status, err)
		}
	}
}
