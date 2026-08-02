package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/komari-monitor/komari-agent/dnsresolver"
)

type clientHTTPStatusError struct {
	StatusCode int
	Status     string
}

func (statusError *clientHTTPStatusError) Error() string {
	if statusError.Status != "" {
		return statusError.Status
	}
	return fmt.Sprintf("HTTP status %d", statusError.StatusCode)
}

func isAuthenticationRejection(err error) bool {
	var statusError *clientHTTPStatusError
	return errors.As(err, &statusError) &&
		(statusError.StatusCode == http.StatusUnauthorized || statusError.StatusCode == http.StatusForbidden)
}

func buildClientAPIEndpoint(path string, query url.Values) string {
	endpoint := strings.TrimSuffix(flags.Endpoint, "/") + path
	if len(query) == 0 {
		return endpoint
	}
	encodedQuery := query.Encode()
	if encodedQuery == "" {
		return endpoint
	}
	return endpoint + "?" + encodedQuery
}

func buildClientWebSocketEndpoint(path string, query url.Values) string {
	endpoint := buildClientAPIEndpoint(path, query)
	return "ws" + strings.TrimPrefix(endpoint, "http")
}

func applyClientAuthHeaders(headers http.Header) {
	if token := strings.TrimSpace(flags.Token); token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	if flags.CFAccessClientID != "" && flags.CFAccessClientSecret != "" {
		headers.Set("CF-Access-Client-Id", flags.CFAccessClientID)
		headers.Set("CF-Access-Client-Secret", flags.CFAccessClientSecret)
	}
}

func newClientRequest(method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	applyClientAuthHeaders(req.Header)
	return req, nil
}

func newJSONClientRequest(method, endpoint string, payload []byte) (*http.Request, error) {
	req, err := newClientRequest(method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	return req, nil
}

func resetRequestBody(req *http.Request) error {
	if req.GetBody == nil {
		return nil
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

func newTelemetryHTTPClient() *http.Client {
	return dnsresolver.GetTelemetryHTTPClient()
}

func newControlPlaneHTTPClient() *http.Client {
	return dnsresolver.GetControlHTTPClient()
}

func requestWithTimeout(req *http.Request, timeout time.Duration) (*http.Request, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	return req.WithContext(ctx), cancel
}
