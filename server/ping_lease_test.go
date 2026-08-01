package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestPingLeaseRunsThenStopsAtExpiry(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.DisableWebSsh = true
	flags.EnablePing = true
	flags.AllowPrivatePingTargets = true
	flags.AllowedPingTypes = "tcp"
	flags.AllowedPingTCPPorts = "80"

	var runs atomic.Int32
	manager := &pingLeaseManager{now: time.Now, runTask: func(context.Context, pingResultWriter, pingLeaseTaskControl) { runs.Add(1) }}
	now := time.Now()
	lease := pingLeaseControl{
		Revision: 1, IssuedAt: now.Add(5 * time.Millisecond), ExpiresAt: now.Add(40 * time.Millisecond),
		Tasks: []pingLeaseTaskControl{{TaskID: 1, Type: "tcp", Target: "127.0.0.1:80", IntervalMS: 1000, PhaseMS: 0}},
	}
	if err := manager.Apply(t.Context(), &pingResultCapture{}, lease); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs before expiry = %d", runs.Load())
	}
	time.Sleep(60 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("lease kept running after expiry: %d", runs.Load())
	}
}

func TestPingLeaseRejectsUnsafeSchedulesAndLocalPolicyViolations(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.DisableWebSsh = true
	flags.EnablePing = true
	flags.AllowPrivatePingTargets = false
	flags.AllowedPingTypes = "tcp"
	flags.AllowedPingTCPPorts = "80"
	now := time.Now()
	base := pingLeaseControl{Revision: 1, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	tests := []pingLeaseControl{
		{Revision: 0, IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{Revision: 1, IssuedAt: now, ExpiresAt: now.Add(11 * time.Minute)},
		{Revision: 1, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Tasks: []pingLeaseTaskControl{{TaskID: 1, Type: "tcp", Target: "127.0.0.1:80", IntervalMS: 1000}}},
		{Revision: 1, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Tasks: []pingLeaseTaskControl{{TaskID: 1, Type: "tcp", Target: "example.com:80", IntervalMS: 999}}},
	}
	manager := &pingLeaseManager{now: time.Now, runTask: func(context.Context, pingResultWriter, pingLeaseTaskControl) {}}
	for index, lease := range tests {
		if err := manager.Apply(t.Context(), &pingResultCapture{}, lease); err == nil {
			t.Fatalf("unsafe lease %d accepted: %#v", index, lease)
		}
	}
	base.Tasks = []pingLeaseTaskControl{{TaskID: 1, Type: "tcp", Target: "example.com:80", IntervalMS: 1000}}
	if err := manager.Apply(t.Context(), &pingResultCapture{}, base); err != nil {
		t.Fatalf("safe lease rejected: %v", err)
	}
	manager.Stop()
}

func TestPingLeaseOlderRevisionCannotReplaceNewerLease(t *testing.T) {
	useServerFlagsSnapshot(t)
	flags.DisableWebSsh = true
	flags.EnablePing = true
	flags.AllowPrivatePingTargets = true
	flags.AllowedPingTypes = "tcp"
	flags.AllowedPingTCPPorts = "80"
	manager := &pingLeaseManager{now: time.Now, runTask: func(context.Context, pingResultWriter, pingLeaseTaskControl) {}}
	now := time.Now()
	lease := func(revision uint64) pingLeaseControl {
		return pingLeaseControl{Revision: revision, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Tasks: []pingLeaseTaskControl{{TaskID: 1, Type: "tcp", Target: "127.0.0.1:80", IntervalMS: 1000}}}
	}
	if err := manager.Apply(t.Context(), &pingResultCapture{}, lease(2)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Apply(t.Context(), &pingResultCapture{}, lease(1)); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	revision := manager.revision
	manager.mu.Unlock()
	manager.Stop()
	if revision != 2 {
		t.Fatalf("revision = %d", revision)
	}
}
