package monitoring

import "time"

const connectionSampleInterval = 5 * time.Second

type connectionCounts struct {
	tcp int
	udp int
}

var defaultConnectionCountSampler = newLowFrequencySampler(
	connectionCountPlatform,
	time.Now,
	connectionSampleInterval,
)

func ConnectionsCount() (tcpCount, udpCount int, err error) {
	counts, err := defaultConnectionCountSampler.Sample(false)
	return counts.tcp, counts.udp, err
}

func refreshConnectionCounts() (tcpCount, udpCount int, err error) {
	counts, err := defaultConnectionCountSampler.Sample(true)
	return counts.tcp, counts.udp, err
}
