//go:build darwin

package monitoring

import "golang.org/x/sys/unix"

func processCountPlatform() (int, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	return len(processes), err
}
