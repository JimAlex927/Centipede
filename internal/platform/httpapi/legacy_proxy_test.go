package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyProxyForwardsRequestAndMigrationHeaders(t *testing.T) {
	legacy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/pages/info" || request.URL.RawQuery != "page=1" {
			t.Errorf("unexpected forwarded request: %s %s", request.Method, request.URL.String())
		}
		if request.Host != "frontend.test" {
			t.Errorf("unexpected preserved host: %q", request.Host)
		}
		if request.Header.Get("X-Forwarded-Host") != "frontend.test" {
			t.Errorf("unexpected forwarded host: %q", request.Header.Get("X-Forwarded-Host"))
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"pageId":"p1"}` {
			t.Errorf("unexpected body: %s", body)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer legacy.Close()

	proxy, err := newLegacyProxy(legacy.URL)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/pages/info?page=1", strings.NewReader(`{"pageId":"p1"}`))
	request.Host = "frontend.test"
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}
