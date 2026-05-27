package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/komari-monitor/komari-agent/monitoring/netstatic"
	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"
	"github.com/komari-monitor/komari-agent/server"
	"github.com/komari-monitor/komari-agent/update"
	"github.com/spf13/cobra"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

var RootCmd = &cobra.Command{
	Use:   "komari-agent",
	Short: "komari agent",
	Long:  `komari agent`,
	Run: func(cmd *cobra.Command, args []string) {
		loadFromEnv() // 从环境变量加载配置，覆盖解析
		if flags.ConfigFile != "" {
			bytes, err := os.ReadFile(flags.ConfigFile)
			if err != nil {
				log.Fatalf("Failed to read config file: %v", err)
			}
			err = json.Unmarshal(bytes, flags)
			if err != nil {
				log.Fatalf("Failed to parse config file: %v", err)
			}
		}
		if err := loadTokenFromFile(); err != nil {
			log.Fatalf("Failed to load token file: %v", err)
		}
		// 捕获中止信号，优雅退出
		stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-stopCtx.Done()
			log.Printf("shutting down gracefully...")
			netstatic.Stop()
			os.Exit(0)
		}()

		if flags.ShowWarning {
			ShowToast()
			os.Exit(0)
		}

		if flags.RemoteControlEnabled() {
			go WarnKomariRunning()
		}

		if flags.MonthRotate != 0 {
			err := netstatic.StartOrContinue()
			if err != nil {
				log.Println("Failed to start netstatic monitoring:", err)
			}
			nics, err := monitoring.InterfaceList()
			if err != nil {
				log.Println("Failed to get interface list for netstatic:", err)
			}
			err = netstatic.SetNewConfig(netstatic.NetStaticConfig{
				Nics: nics,
			})
			if err != nil {
				log.Println("Failed to set netstatic config:", err)
			}
		}

		log.Println("Komari Agent", update.CurrentVersion)
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
				log.Printf("Auto-discovery failed: %v", err)
				os.Exit(1)
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
			http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		// 自动更新
		if flags.AutoUpdateEnabled() {
			err := update.CheckAndUpdate()
			if err != nil {
				log.Println("[ERROR]", err)
			}
			go update.DoUpdateWorks()
		} else if flags.IgnoreUnsafeCert && !flags.DisableAutoUpdate {
			log.Println("Automatic updates are disabled while --ignore-unsafe-cert is enabled.")
		}
		go server.DoUploadBasicInfoWorks()
		for {
			server.UpdateBasicInfo()
			server.EstablishWebSocketConnection()
		}
	},
}

func Execute() {
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
	}
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
	RootCmd.PersistentFlags().StringVar(&flags.AllowedPingTypes, "allowed-ping-types", "tcp,http", "Comma-separated ping types allowed when remote ping is enabled")
	RootCmd.PersistentFlags().StringVar(&flags.AllowedPingTCPPorts, "allowed-ping-tcp-ports", "80,443", "Comma-separated TCP/HTTP ports allowed for remote ping")
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
	RootCmd.PersistentFlags().BoolVar(&flags.ShowWarning, "show-warning", false, "Show security warning on Windows, run once as a subprocess")
	RootCmd.PersistentFlags().StringVar(&flags.CustomIpv4, "custom-ipv4", "", "Custom IPv4 address to use")
	RootCmd.PersistentFlags().StringVar(&flags.CustomIpv6, "custom-ipv6", "", "Custom IPv6 address to use")
	RootCmd.PersistentFlags().BoolVar(&flags.GetIpAddrFromNic, "get-ip-addr-from-nic", false, "Get IP address from network interface")
	RootCmd.PersistentFlags().StringVar(&flags.ConfigFile, "config", "", "Path to the configuration file")
	RootCmd.PersistentFlags().ParseErrorsWhitelist.UnknownFlags = true
}

func loadTokenFromFile() error {
	if flags.Token != "" || flags.TokenFile == "" {
		return nil
	}

	tokenBytes, err := os.ReadFile(flags.TokenFile)
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

func loadFromEnv() {
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
			if strings.ToLower(envValue) == "true" || envValue == "1" {
				field.SetBool(true)
			}
		case reflect.Int:
			if intVal, err := strconv.Atoi(envValue); err == nil {
				field.SetInt(int64(intVal))
			}
		case reflect.Float64:
			if floatVal, err := strconv.ParseFloat(envValue, 64); err == nil {
				field.SetFloat(floatVal)
			}
		}
	}
}
