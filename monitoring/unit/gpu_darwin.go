//go:build darwin
// +build darwin

package monitoring

import (
	"context"
	"strings"
)

// GpuName returns the name of the GPU on Darwin (macOS)
func GpuName() string {
	output, err := (execGPUCommandRunner{}).Run(context.Background(), "system_profiler", "SPDisplaysDataType")
	if err != nil {
		return "Unknown"
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Chipset Model:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Chipset Model:"))
		}
	}

	return "Unknown"
}
