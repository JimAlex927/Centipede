package httpapi

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// newLegacyProxy returns a reverse proxy used during the Docmost migration.
// Centipede-owned routes are registered normally; the Gin NoRoute hook sends
// everything else to the existing Node service, allowing endpoint-by-endpoint
// replacement without a flag day cutover.
func newLegacyProxy(rawTarget string) (http.Handler, error) {
	target, err := url.Parse(rawTarget)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(log.Writer(), "legacy proxy: ", log.LstdFlags)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalHost := request.Host
		originalScheme := "http"
		if request.TLS != nil {
			originalScheme = "https"
		}
		originalDirector(request)
		request.Header.Set("X-Forwarded-Host", originalHost)
		request.Header.Set("X-Forwarded-Proto", originalScheme)
	}
	return proxy, nil
}
