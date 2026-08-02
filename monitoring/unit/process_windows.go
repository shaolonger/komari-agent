//go:build windows

package monitoring

import "golang.org/x/sys/windows"

func processCountPlatform() (int, error) {
	const dwordSize = uint32(4)
	for bufferSize := 1024; ; bufferSize += 1024 {
		processes := make([]uint32, bufferSize)
		var bytesReturned uint32
		if err := windows.EnumProcesses(processes, &bytesReturned); err != nil {
			return 0, err
		}
		count := int(bytesReturned / dwordSize)
		if count < len(processes) {
			return count, nil
		}
	}
}
