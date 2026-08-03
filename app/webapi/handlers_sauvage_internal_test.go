package webapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSauvageInternalBearerIsNarrowAndDoesNotBypassOtherRoutes(t *testing.T) {
	server := &Server{Config: Config{SauvageInternalToken: "dedicated-token"}}
	fallbackCalled := false
	fallback := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackCalled = true
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	handler := server.sauvageInternalOrAuth(fallback)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/sauvage/contests/", http.NoBody)
	req.Header.Set("Authorization", "Bearer dedicated-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, fallbackCalled)

	fallbackCalled = false
	req = httptest.NewRequest(http.MethodGet, "/api/sauvage/events", http.NoBody)
	req.Header.Set("Authorization", "Bearer dedicated-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.True(t, fallbackCalled)
}
