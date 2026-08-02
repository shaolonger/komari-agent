package monitoring

import (
	"errors"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func TestNetworkSamplerUsesElapsedCounterDeltas(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	samples := [][]gnet.IOCountersStat{
		{{Name: "eth0", BytesSent: 1_000, BytesRecv: 2_000}},
		{{Name: "eth0", BytesSent: 3_000, BytesRecv: 5_000}},
		{{Name: "eth0", BytesSent: 100, BytesRecv: 100}},
	}
	index := 0
	sampler := newNetworkSampler(func(bool) ([]gnet.IOCountersStat, error) {
		result := samples[index]
		index++
		return result, nil
	}, func() time.Time { return now })

	totalUp, totalDown, upSpeed, downSpeed, err := sampler.Sample(nil, nil)
	if err != nil || totalUp != 1_000 || totalDown != 2_000 || upSpeed != 0 || downSpeed != 0 {
		t.Fatalf("unexpected first network sample: %d %d %d %d %v", totalUp, totalDown, upSpeed, downSpeed, err)
	}

	now = now.Add(2 * time.Second)
	_, _, upSpeed, downSpeed, err = sampler.Sample(nil, nil)
	if err != nil || upSpeed != 1_000 || downSpeed != 1_500 {
		t.Fatalf("unexpected delta speed: up=%d down=%d err=%v", upSpeed, downSpeed, err)
	}

	now = now.Add(time.Second)
	_, _, upSpeed, downSpeed, err = sampler.Sample(nil, nil)
	if err != nil || upSpeed != 0 || downSpeed != 0 {
		t.Fatalf("counter reset underflowed: up=%d down=%d err=%v", upSpeed, downSpeed, err)
	}
}

func TestNetworkSamplerAppliesNICFiltersBeforeDeltas(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	source := func(bool) ([]gnet.IOCountersStat, error) {
		return []gnet.IOCountersStat{
			{Name: "eth0", BytesSent: 100, BytesRecv: 200},
			{Name: "wlan0", BytesSent: 300, BytesRecv: 400},
			{Name: "lo", BytesSent: 500, BytesRecv: 600},
		}, nil
	}
	sampler := newNetworkSampler(source, func() time.Time { return now })
	include := map[string]struct{}{"wlan0": {}}
	totalUp, totalDown, _, _, err := sampler.Sample(include, nil)
	if err != nil || totalUp != 300 || totalDown != 400 {
		t.Fatalf("NIC filter failed: up=%d down=%d err=%v", totalUp, totalDown, err)
	}
}

func TestNetworkSamplerPropagatesSourceError(t *testing.T) {
	expected := errors.New("network source failed")
	sampler := newNetworkSampler(func(bool) ([]gnet.IOCountersStat, error) {
		return nil, expected
	}, time.Now)
	if _, _, _, _, err := sampler.Sample(nil, nil); !errors.Is(err, expected) {
		t.Fatalf("expected source error, got %v", err)
	}
}
