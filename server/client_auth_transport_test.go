package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBuildClientAPIEndpointDoesNotUseTokenQuery(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.Endpoint = "https://example.com/root/"
	flags.Token = "secret-token"

	endpoint := buildClientAPIEndpoint("/api/clients/task/result", nil)
	if endpoint != "https://example.com/root/api/clients/task/result" {
		t.Fatalf("buildClientAPIEndpoint() = %q", endpoint)
	}
	if strings.Contains(endpoint, "token=") {
		t.Fatalf("expected endpoint without token query, got %q", endpoint)
	}
}

func TestBuildClientWebSocketEndpointKeepsOnlyExpectedQuery(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.Endpoint = "https://example.com"
	flags.Token = "secret-token"

	endpoint := buildClientWebSocketEndpoint("/api/clients/terminal", url.Values{"id": []string{"terminal/abc"}})
	if strings.Contains(endpoint, "token=") {
		t.Fatalf("expected websocket endpoint without token query, got %q", endpoint)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Scheme != "wss" {
		t.Fatalf("scheme = %q, want %q", parsed.Scheme, "wss")
	}
	if got := parsed.Query().Get("id"); got != "terminal/abc" {
		t.Fatalf("id query = %q, want %q", got, "terminal/abc")
	}
}

func TestNewClientRequestAddsBearerAuthorizationAndCFAccessHeaders(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.Token = "secret-token"
	flags.CFAccessClientID = "cf-id"
	flags.CFAccessClientSecret = "cf-secret"

	req, err := newClientRequest(http.MethodPost, "https://example.com/api/clients/uploadBasicInfo", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("newClientRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer secret-token")
	}
	if got := req.Header.Get("CF-Access-Client-Id"); got != "cf-id" {
		t.Fatalf("CF-Access-Client-Id = %q, want %q", got, "cf-id")
	}
	if got := req.Header.Get("CF-Access-Client-Secret"); got != "cf-secret" {
		t.Fatalf("CF-Access-Client-Secret = %q, want %q", got, "cf-secret")
	}
}

func TestNewWSHeadersAddsBearerAuthorization(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.Token = "secret-token"

	headers := newWSHeaders()
	if got := headers.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer secret-token")
	}
}

func TestNewJSONClientRequestSupportsRetryableBodies(t *testing.T) {
	useServerFlagsSnapshot(t)

	flags.Token = "secret-token"
	payload := []byte(`{"status":"ok"}`)

	req, err := newJSONClientRequest(http.MethodPost, "https://example.com/api/clients/task/result", payload)
	if err != nil {
		t.Fatalf("newJSONClientRequest() error = %v", err)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}
	body, err := req.GetBody()
	if err != nil {
		t.Fatalf("req.GetBody() error = %v", err)
	}
	defer body.Close()
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if string(bodyBytes) != string(payload) {
		t.Fatalf("retry body = %q, want %q", string(bodyBytes), string(payload))
	}

	req.Body = io.NopCloser(strings.NewReader("stale"))
	if err := resetRequestBody(req); err != nil {
		t.Fatalf("resetRequestBody() error = %v", err)
	}
	resetBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("io.ReadAll(req.Body) error = %v", err)
	}
	if string(resetBody) != string(payload) {
		t.Fatalf("reset body = %q, want %q", string(resetBody), string(payload))
	}
}