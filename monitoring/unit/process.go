package monitoring

import "time"

const processSampleInterval = 5 * time.Second

var defaultProcessCountSampler = newLowFrequencySampler(
	processCountPlatform,
	time.Now,
	processSampleInterval,
)

// ProcessCount returns the last valid low-frequency platform sample.
func ProcessCount() int {
	count, _ := defaultProcessCountSampler.Sample(false)
	return count
}

func refreshProcessCount() int {
	count, _ := defaultProcessCountSampler.Sample(true)
	return count
}

// RefreshConnectionProcessCounts is the event hook used after host namespace
// or other platform resource changes.
func RefreshConnectionProcessCounts() (tcpCount, udpCount, processCount int, err error) {
	tcpCount, udpCount, err = refreshConnectionCounts()
	processCount = refreshProcessCount()
	return tcpCount, udpCount, processCount, err
}
