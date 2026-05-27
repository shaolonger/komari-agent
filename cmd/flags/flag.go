package flags_pkg

type Config struct {
	AutoDiscoveryKey     string  `json:"auto_discovery_key" env:"AGENT_AUTO_DISCOVERY_KEY"`           // 自动发现密钥
	DisableAutoUpdate    bool    `json:"disable_auto_update" env:"AGENT_DISABLE_AUTO_UPDATE"`         // 禁用自动更新
	DisableWebSsh        bool    `json:"disable_web_ssh" env:"AGENT_DISABLE_WEB_SSH"`                 // 兼容旧配置的远程控制禁用开关；默认禁用远程控制
	EnableRemoteControl  bool    `json:"enable_remote_control" env:"AGENT_ENABLE_REMOTE_CONTROL"`     // 显式启用远程控制（web ssh 和 rce）
	EnableRemoteExec     bool    `json:"enable_remote_exec" env:"AGENT_ENABLE_REMOTE_EXEC"`           // 显式启用远程命令执行
	EnableTerminal       bool    `json:"enable_terminal" env:"AGENT_ENABLE_TERMINAL"`                 // 显式启用远程终端
	EnablePing           bool    `json:"enable_ping" env:"AGENT_ENABLE_PING"`                         // 显式启用远程 ping 探测
	AllowPrivatePingTargets bool `json:"allow_private_ping_targets" env:"AGENT_ALLOW_PRIVATE_PING_TARGETS"` // 允许 ping 私有/敏感地址
	AllowedPingTypes     string  `json:"allowed_ping_types" env:"AGENT_ALLOWED_PING_TYPES"`           // 允许的 ping 类型，逗号分隔
	AllowedPingTCPPorts  string  `json:"allowed_ping_tcp_ports" env:"AGENT_ALLOWED_PING_TCP_PORTS"`   // 允许的 TCP/HTTP ping 端口，逗号分隔
	MaxConcurrentPings   int     `json:"max_concurrent_pings" env:"AGENT_MAX_CONCURRENT_PINGS"`       // 单机允许的最大并发 ping 任务数
	PingMinIntervalMillis int    `json:"ping_min_interval_millis" env:"AGENT_PING_MIN_INTERVAL_MILLIS"` // 两次 ping 任务之间的最小间隔，毫秒
	MaxControlRequests   int     `json:"max_control_requests" env:"AGENT_MAX_CONTROL_REQUESTS"`       // 控制请求窗口内允许的最大请求数
	ControlRequestWindow int     `json:"control_request_window" env:"AGENT_CONTROL_REQUEST_WINDOW"`   // 控制请求限流窗口，秒
	AuditTaskCommands    bool    `json:"audit_task_commands" env:"AGENT_AUDIT_TASK_COMMANDS"`         // 显式开启任务命令审计日志
	MaxTerminalSessions  int     `json:"max_terminal_sessions" env:"AGENT_MAX_TERMINAL_SESSIONS"`     // 单机允许的最大终端会话数
	TerminalIdleTimeout  int     `json:"terminal_idle_timeout" env:"AGENT_TERMINAL_IDLE_TIMEOUT"`     // 终端空闲超时，单位秒
	TerminalMaxDuration  int     `json:"terminal_max_duration" env:"AGENT_TERMINAL_MAX_DURATION"`     // 终端最大会话时长，单位秒
	MemoryModeAvailable  bool    `json:"memory_mode_available" env:"AGENT_MEMORY_MODE_AVAILABLE"`     // [deprecated] 已弃用，请使用 MemoryIncludeCache
	TokenFile            string  `json:"token_file" env:"AGENT_TOKEN_FILE"`                           // Token 文件路径
	Token                string  `json:"token" env:"AGENT_TOKEN"`                                     // Token
	Endpoint             string  `json:"endpoint" env:"AGENT_ENDPOINT"`                               // 面板地址
	Interval             float64 `json:"interval" env:"AGENT_INTERVAL"`                               // 数据采集间隔，单位秒
	IgnoreUnsafeCert     bool    `json:"ignore_unsafe_cert" env:"AGENT_IGNORE_UNSAFE_CERT"`           // 忽略不安全的证书
	MaxRetries           int     `json:"max_retries" env:"AGENT_MAX_RETRIES"`                         // 最大重试次数
	ReconnectInterval    int     `json:"reconnect_interval" env:"AGENT_RECONNECT_INTERVAL"`           // 重连间隔，单位秒
	InfoReportInterval   int     `json:"info_report_interval" env:"AGENT_INFO_REPORT_INTERVAL"`       // 基础信息上报间隔，单位分钟
	IncludeNics          string  `json:"include_nics" env:"AGENT_INCLUDE_NICS"`                       // 仅统计网卡，逗号分隔的网卡名称列表，支持通配符
	ExcludeNics          string  `json:"exclude_nics" env:"AGENT_EXCLUDE_NICS"`                       // 统计时排除的网卡，逗号分隔的网卡名称列表，支持通配符
	IncludeMountpoints   string  `json:"include_mountpoints" env:"AGENT_INCLUDE_MOUNTPOINTS"`         // 磁盘统计的包含挂载点列表，使用分号分隔
	MonthRotate          int     `json:"month_rotate" env:"AGENT_MONTH_ROTATE"`                       // 流量统计的月份重置日期（0表示禁用）
	CFAccessClientID     string  `json:"cf_access_client_id" env:"AGENT_CF_ACCESS_CLIENT_ID"`         // Cloudflare Access Client ID
	CFAccessClientSecret string  `json:"cf_access_client_secret" env:"AGENT_CF_ACCESS_CLIENT_SECRET"` // Cloudflare Access Client Secret
	MemoryIncludeCache   bool    `json:"memory_include_cache" env:"AGENT_MEMORY_INCLUDE_CACHE"`       // 包括缓存/缓冲区的内存使用情况
	MemoryReportRawUsed  bool    `json:"memory_report_raw_used" env:"AGENT_MEMORY_REPORT_RAW_USED"`   // 使用原始内存使用情况报告
	CustomDNS            string  `json:"custom_dns" env:"AGENT_CUSTOM_DNS"`                           // 使用的自定义DNS服务器
	EnableGPU            bool    `json:"enable_gpu" env:"AGENT_ENABLE_GPU"`                           // 启用详细GPU监控
	ShowWarning          bool    `json:"show_warning" env:"AGENT_SHOW_WARNING"`                       // Windows 上显示安全警告，作为子进程运行一次
	CustomIpv4           string  `json:"custom_ipv4" env:"AGENT_CUSTOM_IPV4"`                         // 自定义 IPv4 地址
	CustomIpv6           string  `json:"custom_ipv6" env:"AGENT_CUSTOM_IPV6"`                         // 自定义 IPv6 地址
	GetIpAddrFromNic     bool    `json:"get_ip_addr_from_nic" env:"AGENT_GET_IP_ADDR_FROM_NIC"`       // 从网卡获取IP地址
	HostProc             string  `json:"host_proc" env:"HOST_PROC"`                                   // 容器环境下宿主机/proc目录的挂载点，用于监控宿主机进程
	ConfigFile           string  `json:"config_file" env:"AGENT_CONFIG_FILE"`                         // JSON配置文件路径

}

func (config *Config) RemoteControlEnabled() bool {
	if config.IgnoreUnsafeCert {
		return false
	}
	return config.EnableRemoteControl || !config.DisableWebSsh
}

func (config *Config) RemoteExecEnabled() bool {
	if config.IgnoreUnsafeCert {
		return false
	}
	return config.EnableRemoteExec || config.RemoteControlEnabled()
}

func (config *Config) TerminalEnabled() bool {
	if config.IgnoreUnsafeCert {
		return false
	}
	return config.EnableTerminal || config.RemoteControlEnabled()
}

func (config *Config) PingEnabled() bool {
	if config.IgnoreUnsafeCert {
		return false
	}
	return config.EnablePing || config.RemoteControlEnabled()
}

func (config *Config) AutoUpdateEnabled() bool {
	return !config.DisableAutoUpdate && !config.IgnoreUnsafeCert
}

var GlobalConfig = &Config{
	DisableWebSsh: true,
}
