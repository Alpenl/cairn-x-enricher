package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/observability"
)

const maxWorkerPolicyVersion = 1<<53 - 1 // JavaScript/D1 exact integer range.

func publishWorkerPolicyOnce(ctx context.Context, store *observability.Store, client *cairn.Client) {
	status := store.Snapshot()
	policy := status.Desired
	if status.WorkerPersistedVersion != nil && *status.WorkerPersistedVersion == policy.Version &&
		status.WorkerPublishState == "confirmed" && status.WorkerLastConfirmedAt != nil &&
		time.Since(*status.WorkerLastConfirmedAt) < 10*time.Minute {
		return
	}
	if policy.Version > maxWorkerPolicyVersion {
		store.SetWorkerPublishResult(nil, "rejected")
		return
	}
	config := cairn.ObservabilityConfig{Version: int64(policy.Version), Logs: string(policy.Logs)}
	if policy.Logs == observability.LogDiagnostic {
		config.FallbackLogs = string(policy.FallbackLogs)
		config.DiagnosticUntil = policy.DiagnosticUntil.UnixMilli()
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := client.PublishObservability(requestCtx, config)
	if err == nil {
		store.SetWorkerPublishResult(&policy.Version, "confirmed")
		return
	}
	var apiErr *cairn.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusConflict:
			store.SetWorkerPublishResult(nil, "conflict")
		case http.StatusBadRequest:
			store.SetWorkerPublishResult(nil, "rejected")
		default:
			store.SetWorkerPublishResult(nil, "unavailable")
		}
		return
	}
	store.SetWorkerPublishResult(nil, "unavailable")
}

func runWorkerPolicyPublisher(ctx context.Context, store *observability.Store, client *cairn.Client, tracker *health.Tracker) {
	runMonitoredPeriodic(ctx, tracker, "policy_publisher", 5*time.Second, 10*time.Second, func() {
		publishWorkerPolicyOnce(ctx, store, client)
	})
}
