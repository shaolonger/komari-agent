package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/komari-monitor/komari-agent/monitoring/netstatic"
	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/komari-monitor/komari-agent/server"
	"github.com/komari-monitor/komari-agent/update"
	"github.com/spf13/cobra"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

const (
	maximumConfigFileBytes = 1 << 20
	maximumTokenFileBytes  = 64 << 10
	agentShutdownTimeout   = 15 * time.Second
)

var agentShutdownBudget = agentShutdownTimeout

type agentServices struct {
	runTelemetry      func(context.Context) error
	updateBasicInfo   func(context.Context) error
	runBasicInfo      func(context.Context) error
	runUpdater        func(context.Context) error
	runDiagnostics    func(context.Context) error
	waitControl       func(context.Context) error
	stopNetstatic     func(context.Context) error
	reconnectInterval time.Duration
}

func defaultAgentServices() agentServices {
	return agentServices{
		runTelemetry:    server.RunTelemetryWebSocket,
		updateBasicInfo: server.UpdateBasicInfoContext,
		runBasicInfo:    server.DoUploadBasicInfoWorksContext,
		runUpdater:      update.DoUpdateWorksContext,
		runDiagnostics: func(ctx context.Context) error {
			diagnostics.RunLogger(ctx, 5*time.Minute)
			return ctx.Err()
		},
		waitControl:       server.WaitForControlWorkers,
		stopNetstatic:     netstatic.StopContext,
		reconnectInterval: time.Duration(flags.ReconnectInterval) * time.Second,
	}
}

var RootCmd = &cobra.Command{
	Use:           "komari-agent",
	Short:         "komari agent",
	Long:          `komari agent`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		stopContext, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		return runAgent(stopContext)
	},
}

func runAgent(ctx context.Context) error {
	if err := loadFromEnv(); err != nil {
		return err
	}
	if flags.ConfigFile != "" {
		bytes, err := readBoundedRegularFile(flags.ConfigFile, maximumConfigFileBytes)
		if err != nil {
			return fmt.Errorf("read config file: %w", err)
		}
		err = json.Unmarshal(bytes, flags)
		if err != nil {
			return fmt.Errorf("parse config file: %w", err)
		}
	}
	if err := loadTokenFromFile(); err != nil {
		return fmt.Errorf("load token file: %w", err)
	}
	if err := flags.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	diagnostics.SetEnabled(flags.EnableDiagnostics)

	if flags.ShowWarning {
		ShowToast()
		return nil
	}

	if flags.RemoteControlEnabled() {
		go WarnKomariRunning()
	}

	netstaticStarted := false
	lifecycleOwnsNetstatic := false
	defer func() {
		if netstaticStarted && !lifecycleOwnsNetstatic {
			stopContext, cancelStop := context.WithTimeout(context.Background(), agentShutdownTimeout)
			defer cancelStop()
			if err := netstatic.StopContext(stopContext); err != nil {
				log.Printf("Failed to stop netstatic after startup error: %v", err)
			}
		}
	}()
	if flags.MonthRotate != 0 {
		err := netstatic.StartOrContinue()
		if err != nil {
			return fmt.Errorf("start netstatic monitoring: %w", err)
		}
		netstaticStarted = true
		nics, err := monitoring.InterfaceList()
		if err != nil {
			log.Println("Failed to get interface list for netstatic:", err)
		} else if err = netstatic.SetNewConfig(netstatic.NetStaticConfig{Nics: nics}); err != nil {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), agentShutdownTimeout)
			defer cancelStop()
			return errors.Join(fmt.Errorf("configure netstatic: %w", err), netstatic.StopContext(stopCtx))
		}
	}

	log.Println("Komari Agent", update.CurrentVersion, "commit", update.BuildCommit)
	log.Println("Github Repo:", update.Repo)

	// 设置 DNS 解析行为
	if flags.CustomDNS != "" {
		dnsresolver.SetCustomDNSServer(flags.CustomDNS)
		log.Printf("Using custom DNS server: %s", flags.CustomDNS)
	} else {
		// 未设置则使用系统默认 DNS（不使用内置列表）
		log.Printf("Using system default DNS resolver")
	}

	// Auto discovery
	if flags.AutoDiscoveryKey != "" {
		err := handleAutoDiscovery()
		if err != nil {
			return fmt.Errorf("auto-discovery failed: %w", err)
		}
	}
	diskList, err := monitoring.DiskList()
	if err != nil {
		log.Println("Failed to get disk list:", err)
	}
	log.Println("Monitoring Mountpoints:", diskList)
	interfaceList, err := monitoring.InterfaceList()
	if err != nil {
		log.Println("Failed to get interface list:", err)
	}
	log.Println("Monitoring Interfaces:", interfaceList)

	// 忽略不安全的证书
	if flags.IgnoreUnsafeCert {
		log.Println("WARNING: --ignore-unsafe-cert disables remote control capabilities and automatic updates.")
	}
	// 自动更新
	if flags.AutoUpdateEnabled() {
		err := update.CheckAndUpdateContext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, update.ErrUpdateInstalled) {
				return err
			}
			log.Println("[ERROR]", err)
		}
	} else if flags.IgnoreUnsafeCert && !flags.DisableAutoUpdate {
		log.Println("Automatic updates are disabled while --ignore-unsafe-cert is enabled.")
	}
	services := defaultAgentServices()
	if !flags.AutoUpdateEnabled() {
		services.runUpdater = nil
	}
	lifecycleOwnsNetstatic = true
	return runAgentLifecycle(ctx, netstaticStarted, services)
}

func runAgentLifecycle(parent context.Context, netstaticStarted bool, services agentServices) error {
	ctx, cancel := context.WithCancel(parent)
	var background sync.WaitGroup
	backgroundErrors := make(chan error, 3)
	startBackground := func(name string, run func(context.Context) error) {
		if run == nil {
			return
		}
		background.Add(1)
		go func() {
			defer background.Done()
			if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case backgroundErrors <- fmt.Errorf("%s: %w", name, err):
				default:
				}
				cancel()
			}
		}()
	}
	startBackground("basic info worker", services.runBasicInfo)
	startBackground("update worker", services.runUpdater)
	startBackground("diagnostics worker", services.runDiagnostics)

	var runErr error
	for ctx.Err() == nil {
		if services.updateBasicInfo != nil {
			if err := services.updateBasicInfo(ctx); err != nil {
				if ctx.Err() != nil {
					break
				}
				log.Printf("Error uploading basic info: %v", err)
			} else {
				log.Println("Basic info uploaded successfully")
			}
		}
		if services.runTelemetry == nil {
			<-ctx.Done()
			break
		}
		err := services.runTelemetry(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			log.Printf("Telemetry WebSocket stopped: %v", err)
		}
		if !waitContext(ctx, services.reconnectInterval) {
			break
		}
	}
	cancel()
	select {
	case runErr = <-backgroundErrors:
	default:
	}
	shutdownErr := finishAgentShutdown(netstaticStarted, &background, services)
	if parent.Err() != nil && runErr == nil && shutdownErr == nil {
		log.Printf("Agent shutdown completed")
		return nil
	}
	return errors.Join(runErr, shutdownErr)
}

func finishAgentShutdown(netstaticStarted bool, background *sync.WaitGroup, services agentServices) error {
	log.Printf("Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), agentShutdownBudget)
	defer cancel()

	type shutdownResult struct {
		name string
		err  error
	}
	results := make(chan shutdownResult, 3)
	operations := 0
	start := func(name string, stop func(context.Context) error) {
		if stop == nil {
			return
		}
		operations++
		go func() { results <- shutdownResult{name: name, err: stop(ctx)} }()
	}
	if background != nil {
		start("background workers", func(context.Context) error {
			background.Wait()
			return nil
		})
	}
	start("control workers", services.waitControl)
	if netstaticStarted {
		start("netstatic", services.stopNetstatic)
	}

	var shutdownErrors []error
	for range operations {
		select {
		case result := <-results:
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("stop %s: %w", result.name, result.err))
			}
		case <-ctx.Done():
			shutdownErrors = append(shutdownErrors, fmt.Errorf("agent shutdown exceeded %s: %w", agentShutdownBudget, ctx.Err()))
			return errors.Join(shutdownErrors...)
		}
	}
	return errors.Join(shutdownErrors...)
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		duration = time.Second
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func Execute() int {
	for i, arg := range os.Args {
		if arg == "-autoUpdate" || arg == "--autoUpdate" {
			log.Println("WARNING: The -autoUpdate flag is deprecated in version 0.0.9 and later. Use --disable-auto-update to configure auto-update behavior.")
			// 从参数列表中移除该参数，防止cobra解析错误
			os.Args = append(os.Args[:i], os.Args[i+1:]...)
			break
		}
		if arg == "-memory-mode-available" || arg == "--memory-mode-available" {
			//flags.MemoryIncludeCache = true
			log.Println("WARNING: The --memory-mode-available flag is deprecated in version 1.0.70 and later. Use --memory-include-cache to report memory usage including cache/buffer.")
			os.Args = append(os.Args[:i], os.Args[i+1:]...)
		}
	}

	if err := RootCmd.Execute(); err != nil {
		log.Println(err)
		return commandExitCode(err)
	}
	return commandExitCode(nil)
}

func commandExitCode(err error) int {
	if errors.Is(err, update.ErrUpdateInstalled) {
		return update.RestartExitCode
	}
	if err != nil {
		return 1
	}
	return 0
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&flags.Token, "token", "t", "", "API token")
	RootCmd.PersistentFlags().StringVar(&flags.TokenFile, "token-file", "", "Path to a file containing the API token")
	//RootCmd.MarkPersistentFlagRequired("token")
	RootCmd.PersistentFlags().StringVarP(&flags.Endpoint, "endpoint", "e", "", "API endpoint")
	//RootCmd.MarkPersistentFlagRequired("endpoint")
	RootCmd.PersistentFlags().StringVar(&flags.AutoDiscoveryKey, "auto-discovery", "", "Auto discovery key for the agent")
	RootCmd.PersistentFlags().BoolVar(&flags.DisableAutoUpdate, "disable-auto-update", false, "Disable automatic updates")
	RootCmd.PersistentFlags().BoolVar(&flags.DisableWebSsh, "disable-web-ssh", true, "Disable remote control(web ssh and rce); remote control is disabled by default")
	RootCmd.PersistentFlags().BoolVar(&flags.EnableRemoteControl, "enable-remote-control", false, "Explicitly enable remote control(web ssh and rce)")
	RootCmd.PersistentFlags().BoolVar(&flags.EnableRemoteExec, "enable-remote-exec", false, "Explicitly enable remote command execution")
	RootCmd.PersistentFlags().BoolVar(&flags.EnableTerminal, "enable-terminal", false, "Explicitly enable remote terminal access")
	RootCmd.PersistentFlags().BoolVar(&flags.EnablePing, "enable-ping", false, "Explicitly enable remote ping tasks")
	RootCmd.PersistentFlags().BoolVar(&flags.AllowPrivatePingTargets, "allow-private-ping-targets", false, "Allow pinging private or otherwise sensitive addresses")
	RootCmd.PersistentFlags().StringVar(&flags.AllowedPingTypes, "allowed-ping-types", "tcp,http,icmp", "Comma-separated ping types allowed when remote ping is enabled")
	RootCmd.PersistentFlags().StringVar(&flags.AllowedPingTCPPorts, "allowed-ping-tcp-ports", "80,443,8443", "Comma-separated TCP/HTTP ports allowed for remote ping")
	RootCmd.PersistentFlags().IntVar(&flags.MaxConcurrentPings, "max-concurrent-pings", 2, "Maximum number of concurrent ping tasks per agent")
	RootCmd.PersistentFlags().IntVar(&flags.PingMinIntervalMillis, "ping-min-interval-millis", 500, "Minimum interval between accepted ping tasks in milliseconds")
	RootCmd.PersistentFlags().IntVar(&flags.MaxControlRequests, "max-control-requests", 10, "Maximum number of control requests allowed in one rate-limit window")
	RootCmd.PersistentFlags().IntVar(&flags.ControlRequestWindow, "control-request-window", 10, "Control request rate-limit window in seconds")
	//RootCmd.PersistentFlags().BoolVar(&flags.MemoryModeAvailable, "memory-mode-available", false, "[deprecated]Report memory as available instead of used.")
	RootCmd.PersistentFlags().Float64VarP(&flags.Interval, "interval", "i", 1.0, "Interval in seconds")
	RootCmd.PersistentFlags().BoolVarP(&flags.IgnoreUnsafeCert, "ignore-unsafe-cert", "u", false, "Ignore unsafe certificate errors")
	RootCmd.PersistentFlags().IntVarP(&flags.MaxRetries, "max-retries", "r", 3, "Maximum number of retries")
	RootCmd.PersistentFlags().IntVar(&flags.MaxTerminalSessions, "max-terminal-sessions", 1, "Maximum number of concurrent terminal sessions per agent")
	RootCmd.PersistentFlags().IntVar(&flags.TerminalIdleTimeout, "terminal-idle-timeout", 300, "Terminal idle timeout in seconds")
	RootCmd.PersistentFlags().IntVar(&flags.TerminalMaxDuration, "terminal-max-duration", 1800, "Maximum terminal session duration in seconds")
	RootCmd.PersistentFlags().IntVarP(&flags.ReconnectInterval, "reconnect-interval", "c", 5, "Reconnect interval in seconds")
	RootCmd.PersistentFlags().IntVar(&flags.InfoReportInterval, "info-report-interval", 5, "Interval in minutes for reporting basic info")
	RootCmd.PersistentFlags().StringVar(&flags.IncludeNics, "include-nics", "", "Comma-separated list of network interfaces to include")
	RootCmd.PersistentFlags().StringVar(&flags.ExcludeNics, "exclude-nics", "", "Comma-separated list of network interfaces to exclude")
	RootCmd.PersistentFlags().StringVar(&flags.IncludeMountpoints, "include-mountpoint", "", "Semicolon-separated list of mount points to include for disk statistics")
	RootCmd.PersistentFlags().IntVar(&flags.MonthRotate, "month-rotate", 0, "Month reset for network statistics (0 to disable)")
	RootCmd.PersistentFlags().StringVar(&flags.CFAccessClientID, "cf-access-client-id", "", "Cloudflare Access Client ID")
	RootCmd.PersistentFlags().StringVar(&flags.CFAccessClientSecret, "cf-access-client-secret", "", "Cloudflare Access Client Secret")
	RootCmd.PersistentFlags().BoolVar(&flags.AuditTaskCommands, "audit-task-commands", false, "Audit remote task commands with redaction")
	RootCmd.PersistentFlags().BoolVar(&flags.MemoryIncludeCache, "memory-include-cache", false, "Include cache/buffer in memory usage")
	RootCmd.PersistentFlags().BoolVar(&flags.MemoryReportRawUsed, "memory-exclude-bcf", false, "Use \"raminfo.Used = v.Total - v.Free - v.Buffers - v.Cached\" calculation for memory usage")
	RootCmd.PersistentFlags().StringVar(&flags.CustomDNS, "custom-dns", "", "Custom DNS server to use (e.g. 8.8.8.8, 114.114.114.114). By default, the program uses the system DNS resolver.")
	RootCmd.PersistentFlags().BoolVar(&flags.EnableGPU, "gpu", false, "Enable detailed GPU monitoring (usage, memory, multi-GPU support)")
	RootCmd.PersistentFlags().BoolVar(&flags.EnableDiagnostics, "enable-diagnostics", false, "Enable aggregate performance diagnostics without sensitive fields")
	RootCmd.PersistentFlags().BoolVar(&flags.ShowWarning, "show-warning", false, "Show security warning on Windows, run once as a subprocess")
	RootCmd.PersistentFlags().StringVar(&flags.CustomIpv4, "custom-ipv4", "", "Custom IPv4 address to use")
	RootCmd.PersistentFlags().StringVar(&flags.CustomIpv6, "custom-ipv6", "", "Custom IPv6 address to use")
	RootCmd.PersistentFlags().BoolVar(&flags.GetIpAddrFromNic, "get-ip-addr-from-nic", false, "Get IP address from network interface")
	RootCmd.PersistentFlags().StringVar(&flags.ConfigFile, "config", "", "Path to the configuration file")
}

func loadTokenFromFile() error {
	if flags.Token != "" || flags.TokenFile == "" {
		return nil
	}

	tokenBytes, err := readBoundedRegularFile(flags.TokenFile, maximumTokenFileBytes)
	if err != nil {
		return fmt.Errorf("read %s: %w", flags.TokenFile, err)
	}

	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("token file %s is empty", flags.TokenFile)
	}

	flags.Token = token
	return nil
}

func readBoundedRegularFile(path string, maximumBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	if info.Size() > maximumBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maximumBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maximumBytes)
	}
	return data, nil
}

func loadFromEnv() error {
	val := reflect.ValueOf(flags).Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		// Get the env tag
		envTag := fieldType.Tag.Get("env")
		if envTag == "" {
			continue
		}

		// Get the environment variable value
		envValue := os.Getenv(envTag)
		if envValue == "" {
			continue
		}

		// Set the field based on its type
		switch field.Kind() {
		case reflect.String:
			field.SetString(envValue)
		case reflect.Bool:
			boolValue, err := strconv.ParseBool(envValue)
			if err != nil {
				return fmt.Errorf("environment variable %s must be a boolean", envTag)
			}
			field.SetBool(boolValue)
		case reflect.Int:
			intValue, err := strconv.Atoi(envValue)
			if err != nil {
				return fmt.Errorf("environment variable %s must be an integer", envTag)
			}
			field.SetInt(int64(intValue))
		case reflect.Float64:
			floatValue, err := strconv.ParseFloat(envValue, 64)
			if err != nil {
				return fmt.Errorf("environment variable %s must be a number", envTag)
			}
			field.SetFloat(floatValue)
		}
	}
	return nil
}
