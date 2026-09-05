package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakePinger struct{ err error }

func (fake fakePinger) Ping(context.Context) error { return fake.err }

func TestReadiness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "ready", want: http.StatusOK},
		{name: "database unavailable", err: errors.New("offline"), want: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			Register(router, fakePinger{err: test.err}, "test")
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d", test.want, response.Code)
			}
		})
	}
}

func TestDocmostHealthAliases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	Register(router, fakePinger{}, "test")
	for _, path := range []string{"/api/health", "/api/health/live", "/api/health/ready"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, response.Code)
		}
	}
}
