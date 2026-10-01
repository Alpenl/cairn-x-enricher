package main

import (
	"context"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/config"
	"github.com/Alpenl/cairn-x-enricher/internal/health"
	"github.com/Alpenl/cairn-x-enricher/internal/processor"
)

type notifiedClassificationQueue struct {
	processor.StageQueue
	claims chan struct{}
}

func (q *notifiedClassificationQueue) ClaimClassification(context.Context, string, string, string) (*cairn.ClassificationJob, error) {
	q.claims <- struct{}{}
	return nil, nil
}

func TestIndependentClassificationWakeupDoesNotWaitForLongPollInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &notifiedClassificationQueue{claims: make(chan struct{}, 2)}
	worker := processor.NewStaged(queue, nil, idleClassifier{}, "", "", discardLogger(), 1)
	wakeup := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runClassificationScheduler(ctx, worker, health.NewTracker(), config.Config{PollInterval: time.Hour, MaxJobsPerRun: 1}, discardLogger(), wakeup)
	}()
	select {
	case <-queue.claims:
	case <-time.After(time.Second):
		t.Fatal("initial classification round missing")
	}
	wakeup <- struct{}{}
	select {
	case <-queue.claims:
	case <-time.After(time.Second):
		t.Fatal("durable classification retry waited for hourly poll")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("classification scheduler did not stop")
	}
}
