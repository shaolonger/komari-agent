//go:build !linux

package monitoring

import (
	"fmt"
	"syscall"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func connectionCountPlatform() (connectionCounts, error) {
	connections, err := gnet.Connections("inet")
	if err != nil {
		return connectionCounts{}, fmt.Errorf("failed to get internet connections: %w", err)
	}
	return countInternetConnections(connections), nil
}

func countInternetConnections(connections []gnet.ConnectionStat) connectionCounts {
	var result connectionCounts
	for _, connection := range connections {
		switch connection.Type {
		case uint32(syscall.SOCK_STREAM):
			result.tcp++
		case uint32(syscall.SOCK_DGRAM):
			result.udp++
		}
	}
	return result
}
