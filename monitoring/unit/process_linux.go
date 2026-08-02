//go:build linux

package monitoring

import (
	"io"
	"os"
)

func processCountPlatform() (int, error) {
	return countProcessesInDirectory(procRoot(flags.HostProc))
}

func procRoot(configured string) string {
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && info.IsDir() {
			return configured
		}
	}
	return "/proc"
}

func countProcessesInDirectory(directory string) (int, error) {
	handle, err := os.Open(directory)
	if err != nil {
		return 0, err
	}
	defer handle.Close()

	count := 0
	for {
		names, readErr := handle.Readdirnames(256)
		count += countDecimalPIDs(names)
		if readErr == io.EOF {
			return count, nil
		}
		if readErr != nil {
			return 0, readErr
		}
	}
}
