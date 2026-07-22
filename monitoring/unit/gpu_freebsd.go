//go:build freebsd
// +build freebsd

package monitoring

import (
	"context"
	"strings"
)

// GpuName returns the name of the GPU on FreeBSD
func GpuName() string {
	output, err := (execGPUCommandRunner{}).Run(context.Background(), "pciconf", "-lv")
	if err != nil {
		return "Unknown"
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "VGA") || strings.Contains(line, "Display") {
			return line
		}
	}

	return "Unknown"
}
