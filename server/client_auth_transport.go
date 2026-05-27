package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

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