package server

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"
)

const (
	maximumPingLeaseTasks     = 256
	maximumPingLeaseDuration  = 10 * time.Minute
	maximumLeasedPingInterval = time.Hour
	minimumLeasedPingInterval = time.Second
)

type pingLeaseTaskControl struct {
	TaskID     uint   `json:"ping_task_id"`
	Type       string `json:"ping_type"`
	Target     string `json:"ping_target"`
	IntervalMS int64  `json:"interval_ms"`
	PhaseMS    int64  `json:"phase_ms"`
}

type pingLeaseControl struct {
	Revision  uint64                 `json:"revision"`
	IssuedAt  time.Time              `json:"issued_at"`
	ExpiresAt time.Time              `json:"expires_at"`
	Tasks     []pingLeaseTaskControl `json:"tasks"`
}

type pingLeaseManager struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	revision   uint64
	now        func() time.Time
	runTask    func(context.Context, pingResultWriter, pingLeaseTaskControl)
	batcher    *pingResultBatcher
	newBatcher func(pingResultWriter) (*pingResultBatcher, error)
}

var activePingLease = &pingLeaseManager{
	now:        time.Now,
	newBatcher: newDefaultPingResultBatcher,
	runTask: func(ctx context.Context, writer pingResultWriter, task pingLeaseTaskControl) {
		activeControlWorkers.launch(func() {
			NewPingTaskContext(ctx, writer, task.TaskID, task.Type, task.Target)
		})
	},
}

func (manager *pingLeaseManager) Apply(parent context.Context, writer pingResultWriter, lease pingLeaseControl) error {
	if parent == nil || writer == nil {
		return errors.New("ping lease requires context and result writer")
	}
	now := manager.now()
	if err := validatePingLease(now, lease); err != nil {
		return err
	}
	if !flags.PingEnabled() {
		manager.Stop()
		return errors.New("ping capability is disabled")
	}
	tasks := append([]pingLeaseTaskControl(nil), lease.Tasks...)
	ctx, cancel := context.WithDeadline(parent, lease.ExpiresAt)
	done := make(chan struct{})
	manager.mu.Lock()
	if lease.Revision < manager.revision {
		manager.mu.Unlock()
		cancel()
		return nil
	}
	taskWriter := writer
	if manager.batcher == nil && manager.newBatcher != nil {
		batcher, err := manager.newBatcher(writer)
		if err != nil {
			manager.mu.Unlock()
			cancel()
			return fmt.Errorf("open durable Ping result spool: %w", err)
		}
		manager.batcher = batcher
	}
	if manager.batcher != nil {
		taskWriter = manager.batcher
	}
	previousCancel := manager.cancel
	manager.cancel, manager.done, manager.revision = cancel, done, lease.Revision
	manager.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	go func() {
		defer close(done)
		manager.run(ctx, taskWriter, lease.IssuedAt, tasks)
	}()
	return nil
}

func validatePingLease(now time.Time, lease pingLeaseControl) error {
	if lease.Revision == 0 || lease.IssuedAt.IsZero() || lease.ExpiresAt.IsZero() {
		return errors.New("ping lease metadata is incomplete")
	}
	if lease.IssuedAt.After(now.Add(30*time.Second)) || !lease.ExpiresAt.After(now) || lease.ExpiresAt.Sub(now) > maximumPingLeaseDuration {
		return errors.New("ping lease time bounds are invalid")
	}
	if len(lease.Tasks) > maximumPingLeaseTasks {
		return errors.New("ping lease task count exceeds limit")
	}
	policy := currentPingPolicy()
	seen := make(map[uint]struct{}, len(lease.Tasks))
	for _, task := range lease.Tasks {
		interval := time.Duration(task.IntervalMS) * time.Millisecond
		phase := time.Duration(task.PhaseMS) * time.Millisecond
		if task.TaskID == 0 || interval < minimumLeasedPingInterval || interval > maximumLeasedPingInterval || phase < 0 || phase >= interval {
			return errors.New("ping lease task schedule is invalid")
		}
		if _, exists := seen[task.TaskID]; exists {
			return errors.New("ping lease contains a duplicate task")
		}
		seen[task.TaskID] = struct{}{}
		definition, err := parseAuthorizedPingTarget(policy, task.Type, task.Target)
		if err != nil {
			return errors.New("ping lease target violates local policy")
		}
		if address, err := netip.ParseAddr(definition.host); err == nil {
			if err := validatePingAddress(address.Unmap(), policy.allowPrivate); err != nil {
				return errors.New("ping lease target violates local policy")
			}
		}
	}
	return nil
}

type scheduledLeaseTask struct {
	task     pingLeaseTaskControl
	next     time.Time
	interval time.Duration
}

func (manager *pingLeaseManager) run(ctx context.Context, writer pingResultWriter, issuedAt time.Time, tasks []pingLeaseTaskControl) {
	scheduled := make([]scheduledLeaseTask, len(tasks))
	now := manager.now()
	for index, task := range tasks {
		interval := time.Duration(task.IntervalMS) * time.Millisecond
		next := issuedAt.Add(time.Duration(task.PhaseMS) * time.Millisecond)
		if next.Before(now) {
			steps := now.Sub(next) / interval
			next = next.Add(steps * interval)
			if next.Before(now) {
				next = next.Add(interval)
			}
		}
		scheduled[index] = scheduledLeaseTask{task: task, next: next, interval: interval}
	}
	for len(scheduled) > 0 {
		sort.Slice(scheduled, func(left, right int) bool { return scheduled[left].next.Before(scheduled[right].next) })
		wait := time.Until(scheduled[0].next)
		if manager.now != nil {
			wait = scheduled[0].next.Sub(manager.now())
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
		now = manager.now()
		for index := range scheduled {
			if scheduled[index].next.After(now) {
				break
			}
			manager.runTask(ctx, writer, scheduled[index].task)
			for !scheduled[index].next.After(now) {
				scheduled[index].next = scheduled[index].next.Add(scheduled[index].interval)
			}
		}
	}
}

func (manager *pingLeaseManager) Stop() {
	manager.mu.Lock()
	cancel := manager.cancel
	batcher := manager.batcher
	manager.cancel, manager.done = nil, nil
	manager.batcher = nil
	manager.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if batcher != nil {
		_ = batcher.Close()
	}
}

func (manager *pingLeaseManager) HandleControl(message []byte) bool {
	manager.mu.Lock()
	batcher := manager.batcher
	manager.mu.Unlock()
	return batcher != nil && batcher.HandleControl(message)
}
