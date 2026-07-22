//go:build freebsd

package monitoring

import "github.com/shirou/gopsutil/v4/process"

func processCountPlatform() (int, error) {
	processes, err := process.Pids()
	return len(processes), err
}
