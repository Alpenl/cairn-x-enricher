package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
)

// Critical servers must report an exit even after a panic, so their owner can
// shut down and the deployment supervisor can restart the process.
func criticalServerResult(action func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("critical server task panicked")
		}
	}()
	return action()
}

type restartPolicy struct {
	maxRestarts                int
	base, maximum, stableAfter time.Duration
	wait                       func(context.Context, time.Duration) bool
}

func defaultRestartPolicy() restartPolicy {
	return restartPolicy{3, time.Second, 30 * time.Second, 10 * time.Minute, waitRestart}
}

func waitRestart(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// A panic ends one loop, never its supervisor. Repeated startup panics have a
// finite restart budget and remain visibly degraded; shutdown starts no restart.
func superviseLane(ctx context.Context, tracker *health.Tracker, logger *slog.Logger, name string,
	run func(context.Context), policy restartPolicy) {
	consecutive, total := 0, 0
	for ctx.Err() == nil {
		tracker.PulseLane(name, "starting", policy.maximum+time.Minute)
		started := time.Now()
		func() {
			defer func() { _ = recover() }() // fixed public reason; panic may contain private content
			run(ctx)
		}()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= policy.stableAfter {
			consecutive = 0
		}
		consecutive++
		if consecutive > policy.maxRestarts {
			tracker.FailLane(name, "failed", total)
			logger.Error("scheduler restart limit reached", "component", name, "attempt", total)
			return
		}
		total++
		tracker.FailLane(name, "restarting", total)
		logger.Error("scheduler loop exited; restarting", "component", name, "attempt", total)
		delay := policy.base
		for n := 1; n < consecutive; n++ {
			delay = min(delay*2, policy.maximum)
		}
		if !policy.wait(ctx, min(delay, policy.maximum)) {
			return
		}
	}
}

func waitingGrace(cfg config.Config) time.Duration {
	return max(2*cfg.PollInterval, time.Second) + 30*time.Second
}

func runMonitoredPeriodic(ctx context.Context, tracker *health.Tracker, name string,
	interval, workGrace time.Duration, action func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		tracker.PulseLane(name, "running", workGrace)
		action()
		tracker.PulseLane(name, "waiting", interval+30*time.Second)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
