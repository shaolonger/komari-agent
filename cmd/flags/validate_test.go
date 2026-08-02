package flags_pkg

import (
	"math"
	"strings"
	"testing"
)

func validTestConfig() Config {
	return Config{
		Interval:              1,
		MaxRetries:            3,
		ReconnectInterval:     5,
		InfoReportInterval:    5,
		MaxConcurrentPings:    2,
		PingMinIntervalMillis: 500,
		MaxControlRequests:    10,
		ControlRequestWindow:  10,
		MaxTerminalSessions:   1,
		TerminalIdleTimeout:   300,
		TerminalMaxDuration:   1800,
	}
}

func TestConfigValidateAcceptsDefaultsAndBoundaries(t *testing.T) {
	config := validTestConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	config.Interval = MaximumReportIntervalSeconds
	config.MaxRetries = MaximumRetries
	config.ReconnectInterval = MaximumReconnectSeconds
	config.InfoReportInterval = MaximumInfoReportMinutes
	config.MonthRotate = 31
	config.MaxConcurrentPings = MaximumConcurrentPings
	config.PingMinIntervalMillis = MaximumPingIntervalMillis
	config.MaxControlRequests = MaximumControlRequests
	config.ControlRequestWindow = MaximumControlWindowSeconds
	config.MaxTerminalSessions = MaximumTerminalSessions
	config.TerminalIdleTimeout = MaximumTerminalIdleSeconds
	config.TerminalMaxDuration = MaximumTerminalDuration
	if err := config.Validate(); err != nil {
		t.Fatalf("boundaries rejected: %v", err)
	}
}

func TestConfigValidateRejectsInvalidResourceParameters(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "nan interval", mutate: func(c *Config) { c.Interval = math.NaN() }, field: "interval"},
		{name: "infinite interval", mutate: func(c *Config) { c.Interval = math.Inf(1) }, field: "interval"},
		{name: "zero interval", mutate: func(c *Config) { c.Interval = 0 }, field: "interval"},
		{name: "retries", mutate: func(c *Config) { c.MaxRetries = MaximumRetries + 1 }, field: "max_retries"},
		{name: "reconnect", mutate: func(c *Config) { c.ReconnectInterval = 0 }, field: "reconnect_interval"},
		{name: "info interval", mutate: func(c *Config) { c.InfoReportInterval = 0 }, field: "info_report_interval"},
		{name: "month", mutate: func(c *Config) { c.MonthRotate = 32 }, field: "month_rotate"},
		{name: "ping concurrency", mutate: func(c *Config) { c.MaxConcurrentPings = 0 }, field: "max_concurrent_pings"},
		{name: "ping interval", mutate: func(c *Config) { c.PingMinIntervalMillis = -1 }, field: "ping_min_interval_millis"},
		{name: "control count", mutate: func(c *Config) { c.MaxControlRequests = 0 }, field: "max_control_requests"},
		{name: "control window", mutate: func(c *Config) { c.ControlRequestWindow = 0 }, field: "control_request_window"},
		{name: "terminal sessions", mutate: func(c *Config) { c.MaxTerminalSessions = 0 }, field: "max_terminal_sessions"},
		{name: "terminal ordering", mutate: func(c *Config) { c.TerminalIdleTimeout = c.TerminalMaxDuration + 1 }, field: "terminal_idle_timeout"},
		{name: "policy bomb", mutate: func(c *Config) { c.AllowedPingTypes = strings.Repeat("x", MaximumPolicyStringBytes+1) }, field: "allowed_ping_types"},
		{name: "ping type", mutate: func(c *Config) { c.AllowedPingTypes = "tcp,shell" }, field: "allowed_ping_types"},
		{name: "ping port", mutate: func(c *Config) { c.AllowedPingTCPPorts = "80,0" }, field: "allowed_ping_tcp_ports"},
		{name: "dns port", mutate: func(c *Config) { c.CustomDNS = "1.1.1.1:99999" }, field: "custom_dns"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			config := validTestConfig()
			testCase.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), testCase.field) {
				t.Fatalf("Validate() error = %v, want field %q", err, testCase.field)
			}
		})
	}
}
