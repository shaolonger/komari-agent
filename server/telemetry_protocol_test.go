package server

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv2"
	"github.com/komari-monitor/komari-agent/protocol/telemetryv3"
)

func TestNegotiatedTelemetryProtocolSecureFallback(t *testing.T) {
	tests := map[string]telemetryProtocol{
		"":                            telemetryProtocolV1,
		telemetryv2.LegacySubprotocol: telemetryProtocolV1,
		telemetryv2.Subprotocol:       telemetryProtocolV2,
		telemetryv3.Subprotocol:       telemetryProtocolV3,
	}
	for selected, want := range tests {
		got, err := negotiatedTelemetryProtocol(selected)
		if err != nil || got != want {
			t.Fatalf("selected %q: protocol=%d err=%v, want %d", selected, got, err, want)
		}
	}
	if _, err := negotiatedTelemetryProtocol("komari.telemetry.v999"); err == nil {
		t.Fatal("unknown selected subprotocol was accepted")
	}
}

func TestTelemetryDialerAdvertisesV3ThenV2ThenV1(t *testing.T) {
	dialer := newTelemetryWSDialer()
	want := []string{telemetryv3.Subprotocol, telemetryv2.Subprotocol, telemetryv2.LegacySubprotocol}
	if !reflect.DeepEqual(dialer.Subprotocols, want) {
		t.Fatalf("Subprotocols = %#v, want %#v", dialer.Subprotocols, want)
	}
	legacyDialer := newTelemetryWSDialerWithoutV3()
	legacyWant := []string{telemetryv2.Subprotocol, telemetryv2.LegacySubprotocol}
	if !reflect.DeepEqual(legacyDialer.Subprotocols, legacyWant) {
		t.Fatalf("fallback subprotocols = %#v, want %#v", legacyDialer.Subprotocols, legacyWant)
	}
	if terminalDialer := newWSDialer(); len(terminalDialer.Subprotocols) != 0 {
		t.Fatalf("generic/terminal dialer advertised telemetry protocols: %#v", terminalDialer.Subprotocols)
	}
}

func TestBuildTelemetryFrameUsesV3AndSecureFallbacks(t *testing.T) {
	v1 := []byte("json-v1")
	v2 := []byte("binary-v2")
	v3 := []byte("binary-v3")
	typeID, payload, err := buildTelemetryFrameWithV3(
		telemetryProtocolV3,
		func() []byte { return v1 },
		func() ([]byte, error) { return v2, nil },
		func() ([]byte, error) { return v3, nil },
	)
	if err != nil || typeID != websocket.BinaryMessage || string(payload) != string(v3) {
		t.Fatalf("v3 frame: type=%d payload=%q err=%v", typeID, payload, err)
	}
	wantErr := errors.New("v3 fixture error")
	typeID, payload, err = buildTelemetryFrameWithV3(
		telemetryProtocolV3,
		func() []byte { return v1 },
		func() ([]byte, error) { return v2, nil },
		func() ([]byte, error) { return nil, wantErr },
	)
	if !errors.Is(err, wantErr) || typeID != websocket.BinaryMessage || string(payload) != string(v2) {
		t.Fatalf("v2 fallback: type=%d payload=%q err=%v", typeID, payload, err)
	}
	typeID, payload, err = buildTelemetryFrameWithV3(
		telemetryProtocolV3,
		func() []byte { return v1 },
		func() ([]byte, error) { return nil, errors.New("v2 fixture error") },
		func() ([]byte, error) { return nil, wantErr },
	)
	if !errors.Is(err, wantErr) || typeID != websocket.TextMessage || string(payload) != string(v1) {
		t.Fatalf("v1 fallback: type=%d payload=%q err=%v", typeID, payload, err)
	}
}

func TestBuildTelemetryFrameUsesBinaryV2AndTextFallback(t *testing.T) {
	v1 := []byte(`{"cpu":{"usage":1}}`)
	v2 := []byte("binary-v2")
	typeID, payload, err := buildTelemetryFrameWith(
		telemetryProtocolV2,
		func() []byte { return v1 },
		func() ([]byte, error) { return v2, nil },
	)
	if err != nil || typeID != websocket.BinaryMessage || string(payload) != string(v2) {
		t.Fatalf("v2 frame: type=%d payload=%q err=%v", typeID, payload, err)
	}

	wantErr := errors.New("fixture encoding failure")
	typeID, payload, err = buildTelemetryFrameWith(
		telemetryProtocolV2,
		func() []byte { return v1 },
		func() ([]byte, error) { return nil, wantErr },
	)
	if !errors.Is(err, wantErr) || typeID != websocket.TextMessage || string(payload) != string(v1) {
		t.Fatalf("fallback frame: type=%d payload=%q err=%v", typeID, payload, err)
	}

	typeID, payload, err = buildTelemetryFrameWith(
		telemetryProtocolV1,
		func() []byte { return v1 },
		func() ([]byte, error) { t.Fatal("v2 generator called for v1"); return nil, nil },
	)
	if err != nil || typeID != websocket.TextMessage || string(payload) != string(v1) {
		t.Fatalf("v1 frame: type=%d payload=%q err=%v", typeID, payload, err)
	}
}
