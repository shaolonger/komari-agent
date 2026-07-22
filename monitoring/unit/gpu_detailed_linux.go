//go:build linux

package monitoring

import (
	"os"
	"os/exec"
)

func newPlatformGPUProviders() []gpuProvider {
	runner := execGPUCommandRunner{}
	providers := make([]gpuProvider, 0, 2)
	if path := findGPUExecutable("/usr/bin/nvidia-smi", "nvidia-smi"); path != "" {
		providers = append(providers, &nvidiaGPUProvider{path: path, runner: runner})
	}
	if path := findGPUExecutable("/opt/rocm/bin/rocm-smi", "rocm-smi"); path != "" {
		providers = append(providers, &amdGPUProvider{path: path, runner: runner})
	}
	return providers
}

func findGPUExecutable(preferred, name string) string {
	if info, err := os.Stat(preferred); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
		return preferred
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}
