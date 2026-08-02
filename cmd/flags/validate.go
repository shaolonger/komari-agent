package flags_pkg

import (
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
)

const (
	MaximumReportIntervalSeconds = 3600
	MaximumRetries               = 10
	MaximumReconnectSeconds      = 3600
	MaximumInfoReportMinutes     = 24 * 60
	MaximumConcurrentPings       = 64
	MaximumPingIntervalMillis    = 60 * 60 * 1000
	MaximumControlRequests       = 10_000
	MaximumControlWindowSeconds  = 3600
	MaximumTerminalSessions      = 32
	MaximumTerminalIdleSeconds   = 24 * 60 * 60
	MaximumTerminalDuration      = 7 * 24 * 60 * 60
	MaximumPolicyStringBytes     = 1024
	MaximumListStringBytes       = 64 * 1024
)

// Validate rejects unsafe or nonsensical externally supplied resource
// parameters before any ticker, queue, command or connection is started.
func (config Config) Validate() error {
	var validationErrors []error
	addRange := func(name string, value, minimum, maximum int) {
		if value < minimum || value > maximum {
			validationErrors = append(validationErrors, fmt.Errorf("%s must be between %d and %d (got %d)", name, minimum, maximum, value))
		}
	}

	if math.IsNaN(config.Interval) || math.IsInf(config.Interval, 0) || config.Interval < 1 || config.Interval > MaximumReportIntervalSeconds {
		validationErrors = append(validationErrors, fmt.Errorf("interval must be finite and between 1 and %d seconds", MaximumReportIntervalSeconds))
	}
	addRange("max_retries", config.MaxRetries, 0, MaximumRetries)
	addRange("reconnect_interval", config.ReconnectInterval, 1, MaximumReconnectSeconds)
	addRange("info_report_interval", config.InfoReportInterval, 1, MaximumInfoReportMinutes)
	addRange("month_rotate", config.MonthRotate, 0, 31)
	addRange("max_concurrent_pings", config.MaxConcurrentPings, 1, MaximumConcurrentPings)
	addRange("ping_min_interval_millis", config.PingMinIntervalMillis, 0, MaximumPingIntervalMillis)
	addRange("max_control_requests", config.MaxControlRequests, 1, MaximumControlRequests)
	addRange("control_request_window", config.ControlRequestWindow, 1, MaximumControlWindowSeconds)
	addRange("max_terminal_sessions", config.MaxTerminalSessions, 1, MaximumTerminalSessions)
	addRange("terminal_idle_timeout", config.TerminalIdleTimeout, 1, MaximumTerminalIdleSeconds)
	addRange("terminal_max_duration", config.TerminalMaxDuration, 1, MaximumTerminalDuration)
	if config.TerminalMaxDuration > 0 && config.TerminalIdleTimeout > config.TerminalMaxDuration {
		validationErrors = append(validationErrors, errors.New("terminal_idle_timeout must not exceed terminal_max_duration"))
	}

	for name, value := range map[string]string{
		"allowed_ping_types":     config.AllowedPingTypes,
		"allowed_ping_tcp_ports": config.AllowedPingTCPPorts,
	} {
		if len(value) > MaximumPolicyStringBytes {
			validationErrors = append(validationErrors, fmt.Errorf("%s exceeds %d bytes", name, MaximumPolicyStringBytes))
		}
	}
	for name, value := range map[string]string{
		"include_nics":        config.IncludeNics,
		"exclude_nics":        config.ExcludeNics,
		"include_mountpoints": config.IncludeMountpoints,
	} {
		if len(value) > MaximumListStringBytes {
			validationErrors = append(validationErrors, fmt.Errorf("%s exceeds %d bytes", name, MaximumListStringBytes))
		}
	}
	if strings.IndexByte(config.CustomDNS, 0) >= 0 {
		validationErrors = append(validationErrors, errors.New("custom_dns contains a NUL byte"))
	} else if config.CustomDNS != "" {
		if err := validateDNSServer(config.CustomDNS); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("custom_dns is invalid: %w", err))
		}
	}
	if len(config.Endpoint) > 4096 {
		validationErrors = append(validationErrors, errors.New("endpoint exceeds 4096 bytes"))
	}
	if len(config.TelemetrySpoolPath) > 4096 || strings.IndexByte(config.TelemetrySpoolPath, 0) >= 0 {
		validationErrors = append(validationErrors, errors.New("telemetry_spool_path is invalid or exceeds 4096 bytes"))
	}
	if err := validatePingPolicy(config.AllowedPingTypes, config.AllowedPingTCPPorts); err != nil {
		validationErrors = append(validationErrors, err)
	}

	return errors.Join(validationErrors...)
}

func validatePingPolicy(types, ports string) error {
	if types != "" {
		for _, value := range strings.Split(types, ",") {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "tcp", "http", "icmp":
			case "":
				return errors.New("allowed_ping_types contains an empty entry")
			default:
				return errors.New("allowed_ping_types contains an unsupported type")
			}
		}
	}
	if ports == "" {
		return nil
	}
	values := strings.Split(ports, ",")
	if len(values) > 128 {
		return errors.New("allowed_ping_tcp_ports contains more than 128 entries")
	}
	for _, value := range values {
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			return errors.New("allowed_ping_tcp_ports must contain ports between 1 and 65535")
		}
	}
	return nil
}

func validateDNSServer(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " /\\") {
		return errors.New("expected a host or IP address with optional port")
	}
	address := value
	if net.ParseIP(value) != nil {
		address = net.JoinHostPort(value, "53")
	} else if !strings.Contains(value, ":") {
		address = net.JoinHostPort(value, "53")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("expected host:port; IPv6 with a port must use brackets")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if len(host) > 253 {
		return errors.New("host exceeds 253 bytes")
	}
	return nil
}
