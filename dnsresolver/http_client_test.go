package dnsresolver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func useHTTPClientTestState(t *testing.T) {
	t.Helper()
	previousUnsafe := flags.IgnoreUnsafeCert
	dnsConfigMu.Lock()
	previousDNS := CustomDNSServer
	CustomDNSServer = ""
	dnsConfigMu.Unlock()
	resolvedHostCache.Clear()
	resetHTTPClients()
	t.Cleanup(func() {
		flags.IgnoreUnsafeCert = previousUnsafe
		dnsConfigMu.Lock()
		CustomDNSServer = previousDNS
		dnsConfigMu.Unlock()
		resolvedHostCache.Clear()
		resetHTTPClients()
	})
}

func TestHTTPClientsAreReusedAndPhysicallyIsolatedByPolicy(t *testing.T) {
	useHTTPClientTestState(t)

	telemetryStrict := getPolicyHTTPClient(httpTelemetryStrict)
	telemetryInsecure := getPolicyHTTPClient(httpTelemetryInsecure)
	control := GetControlHTTPClient()
	update := GetUpdateHTTPClient()
	clients := []*http.Client{telemetryStrict, telemetryInsecure, control, update}
	seenClients := make(map[*http.Client]bool)
	seenTransports := make(map[*http.Transport]bool)
	for index, client := range clients {
		if seenClients[client] {
			t.Fatalf("policy %d reused another policy's client", index)
		}
		seenClients[client] = true
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("policy %d transport = %T", index, client.Transport)
		}
		if seenTransports[transport] {
			t.Fatalf("policy %d reused another policy's transport", index)
		}
		seenTransports[transport] = true
		if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			t.Fatalf("policy %d TLS config = %+v", index, transport.TLSClientConfig)
		}
		wantUnsafe := index == 1
		if transport.TLSClientConfig.InsecureSkipVerify != wantUnsafe {
			t.Fatalf("policy %d InsecureSkipVerify = %v, want %v", index, transport.TLSClientConfig.InsecureSkipVerify, wantUnsafe)
		}
		if client.Timeout != 0 {
			t.Fatalf("policy %d client timeout = %s, want per-request deadlines", index, client.Timeout)
		}
	}
	if GetControlHTTPClient() != control || GetUpdateHTTPClient() != update {
		t.Fatal("policy clients were not reused")
	}
	flags.IgnoreUnsafeCert = false
	if GetTelemetryHTTPClient() != telemetryStrict {
		t.Fatal("strict telemetry policy selected the wrong client")
	}
	flags.IgnoreUnsafeCert = true
	if GetTelemetryHTTPClient() != telemetryInsecure {
		t.Fatal("explicit unsafe telemetry policy selected the wrong client")
	}
}

func TestOnlyTelemetryPolicyCanUseUnverifiedCertificate(t *testing.T) {
	useHTTPClientTestState(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	for name, client := range map[string]*http.Client{
		"telemetry-strict": getPolicyHTTPClient(httpTelemetryStrict),
		"control":          GetControlHTTPClient(),
		"update":           GetUpdateHTTPClient(),
	} {
		resp, err := client.Get(server.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Fatalf("%s accepted an unverified certificate", name)
		}
	}
	flags.IgnoreUnsafeCert = true
	resp, err := GetTelemetryHTTPClient().Get(server.URL)
	if err != nil {
		t.Fatalf("explicit insecure telemetry request failed: %v", err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); string(body) != "ok" {
		t.Fatalf("telemetry response = %q", body)
	}
}

func TestLongLivedHTTPClientReusesConnectionAndHonorsRequestDeadline(t *testing.T) {
	useHTTPClientTestState(t)
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	client := GetControlHTTPClient()
	for range 3 {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new connections = %d, want one reused keep-alive connection", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/slow", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("deadline request error = %v, context = %v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("request deadline took %s", elapsed)
	}
}

func TestUpdateRedirectPolicyRejectsDowngrade(t *testing.T) {
	useHTTPClientTestState(t)
	client := GetUpdateHTTPClient()
	redirect, err := http.NewRequest(http.MethodGet, "http://updates.example.test/asset", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(redirect, nil); err == nil {
		t.Fatal("update redirect accepted an HTTP downgrade")
	}
	secure, err := http.NewRequest(http.MethodGet, "https://objects.example.test/asset", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(secure, nil); err != nil {
		t.Fatalf("secure update redirect rejected: %v", err)
	}
}

func TestHTTPPolicyCacheIsConcurrentSafe(t *testing.T) {
	useHTTPClientTestState(t)
	want := GetControlHTTPClient()
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 1_000 {
				if got := GetControlHTTPClient(); got != want {
					t.Errorf("control client pointer changed")
					return
				}
			}
		}()
	}
	workers.Wait()
}
