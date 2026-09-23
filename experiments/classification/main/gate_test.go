package main

import (
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
)

func TestInvalidGateRejectedBeforeLiveOrRecoveryInputs(t *testing.T) {
	gate := evaluation.DefaultGate()
	gate.MaxAcceptedError = -1
	// No catalog, credentials, transport, output directory, or recovery files
	// exist. Gate validation must be the first operation at both entrypoints.
	for name, err := range map[string]error{
		"live":     runLiveCommand(evaluation.Dataset{}, liveConfig{}, gate),
		"recovery": runRecoveryCommand(evaluation.Dataset{}, "", "", false, gate),
	} {
		if err == nil || !strings.Contains(err.Error(), "invalid gate") {
			t.Fatalf("%s did not reject gate first: %v", name, err)
		}
	}
}
