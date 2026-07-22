//go:build linux

package monitoring

import (
	"errors"
	"os"
	"path/filepath"
)

func connectionCountPlatform() (connectionCounts, error) {
	root := procRoot(flags.HostProc)
	tcp, tcpErr := countProcNetProtocol(root, "tcp", "tcp6")
	udp, udpErr := countProcNetProtocol(root, "udp", "udp6")
	return connectionCounts{tcp: tcp, udp: udp}, errors.Join(tcpErr, udpErr)
}

func countProcNetProtocol(procDirectory string, tables ...string) (int, error) {
	total := 0
	succeeded := 0
	var failures []error
	for _, table := range tables {
		file, err := os.Open(filepath.Join(procDirectory, "net", table))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		count, countErr := countProcNetTable(file)
		closeErr := file.Close()
		if countErr != nil || closeErr != nil {
			failures = append(failures, errors.Join(countErr, closeErr))
			continue
		}
		total += count
		succeeded++
	}
	if succeeded > 0 {
		return total, nil
	}
	return 0, errors.Join(failures...)
}
