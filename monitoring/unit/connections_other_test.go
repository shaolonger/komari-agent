//go:build !linux

package monitoring

import (
	"syscall"
	"testing"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func TestCountInternetConnections(t *testing.T) {
	connections := []gnet.ConnectionStat{
		{Type: uint32(syscall.SOCK_STREAM)},
		{Type: uint32(syscall.SOCK_DGRAM)},
		{Type: uint32(syscall.SOCK_STREAM)},
		{Type: uint32(syscall.SOCK_RAW)},
	}
	if got := countInternetConnections(connections); got != (connectionCounts{tcp: 2, udp: 1}) {
		t.Fatalf("counts = %+v", got)
	}
}
