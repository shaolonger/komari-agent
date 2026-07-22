package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/komari-monitor/komari-agent/diagnostics"
	monitoring "github.com/komari-monitor/komari-agent/monitoring/unit"

	pkg_flags "github.com/komari-monitor/komari-agent/cmd/flags"
)

var flags = pkg_flags.GlobalConfig

func buildCapabilityPayload() map[string]interface{} {
	return map[string]interface{}{
		"capability_ping":                 flags.PingEnabled(),
		"capability_terminal":             flags.TerminalEnabled(),
		"capability_remote_exec":          flags.RemoteExecEnabled(),
		"capability_remote_control":       flags.RemoteControlEnabled(),
		"capability_gpu":                  flags.EnableGPU,
		"capability_auto_update":          flags.AutoUpdateEnabled(),
		"capability_private_ping_targets": flags.AllowPrivatePingTargets,
	}
}

func buildBasicInfoPayload() map[string]interface{} {
	static := defaultStaticBasicInfoCache.Get()
	memory := monitoring.Memory()
	ipv4, ipv6, _ := monitoring.GetIPAddress()

	data := map[string]interface{}{
		"cpu_name":       static.CPUName,
		"cpu_cores":      static.CPUCores,
		"arch":           static.Architecture,
		"os":             static.OSName,
		"kernel_version": static.KernelVersion,
		"ipv4":           ipv4,
		"ipv6":           ipv6,
		"mem_total":      memory.RAM.Total,
		"swap_total":     memory.Swap.Total,
		"disk_total":     monitoring.Disk().Total,
		"gpu_name":       static.GPUName,
		"virtualization": static.Virtualization,
		"version":        static.AgentVersion,
	}

	for key, value := range buildCapabilityPayload() {
		data[key] = value
	}

	return data
}

func DoUploadBasicInfoWorks() {
	_ = DoUploadBasicInfoWorksContext(context.Background())
}

func DoUploadBasicInfoWorksContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("basic info worker requires a parent context")
	}
	interval := time.Duration(flags.InfoReportInterval) * time.Minute
	if interval <= 0 {
		return errors.New("basic info interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := uploadBasicInfoContext(ctx); err != nil && ctx.Err() == nil {
				log.Println("Error uploading basic info:", err)
			}
		}
	}
}

func UpdateBasicInfo() {
	err := UpdateBasicInfoContext(context.Background())
	if err != nil {
		log.Println("Error uploading basic info:", err)
	} else {
		log.Println("Basic info uploaded successfully")
	}
}

func UpdateBasicInfoContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("basic info update requires a parent context")
	}
	return uploadBasicInfoContext(ctx)
}

func uploadBasicInfo() error {
	return uploadBasicInfoContext(context.Background())
}

func uploadBasicInfoContext(ctx context.Context) error {
	data := buildBasicInfoPayload()

	// 尝试上传完整数据
	err := tryUploadDataContext(ctx, data)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 兼容 <= 1.0.2
		delete(data, "kernel_version")
		for key := range buildCapabilityPayload() {
			delete(data, key)
		}
		err = tryUploadDataContext(ctx, data)
		if err != nil {
			return err
		}
	}
	return nil
}

func tryUploadData(data map[string]interface{}) error {
	return tryUploadDataContext(context.Background(), data)
}

func tryUploadDataContext(ctx context.Context, data map[string]interface{}) error {
	if ctx == nil {
		return errors.New("basic info upload requires a parent context")
	}
	endpoint := buildClientAPIEndpoint("/api/clients/uploadBasicInfo", nil)
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := newJSONClientRequest("POST", endpoint, payload)
	if err != nil {
		return err
	}

	client := newTelemetryHTTPClient()
	req = req.WithContext(ctx)
	req, cancel := requestWithTimeout(req, 30*time.Second)
	defer cancel()

	requestStarted := time.Now()
	resp, err := client.Do(req)
	diagnostics.ObserveHTTP(requestStarted, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return err
	}
	if len(body) > 64*1024 {
		return errors.New("basic info response exceeds 64 KiB")
	}
	message := string(body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status code: %d,%s", resp.StatusCode, message)
	}

	return nil
}
