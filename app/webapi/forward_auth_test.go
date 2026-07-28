package webapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForwardAuthMiddlewareRequiresTrustedProxyAndAllowedEmail(t *testing.T) {
	mw, err := newForwardAuthMiddleware(
		"X-Auth-Request-Email", []string{"me@e-dani.com"}, []string{"10.42.0.0/16"},
	)
	require.NoError(t, err)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Run("allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.RemoteAddr = "10.42.1.20:12345"
		req.Header.Set("X-Auth-Request-Email", "ME@e-dani.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("wrong email", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.RemoteAddr = "10.42.1.20:12345"
		req.Header.Set("X-Auth-Request-Email", "other@e-dani.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("untrusted source", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.RemoteAddr = "203.0.113.10:12345"
		req.Header.Set("X-Auth-Request-Email", "me@e-dani.com")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}
