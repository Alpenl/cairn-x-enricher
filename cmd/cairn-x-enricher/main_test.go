package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func TestIsContractFailureClassifiesConfigurationFaults(t *testing.T) {
	// These statuses mean the deployment is misconfigured or the provider
	// broke its contract. Retrying cannot fix them, so readiness must drop.
	for _, status := range []int{
		http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest,
	} {
		err := &enrich.ModelHTTPError{StatusCode: status, Message: "no"}
		if !isContractFailure(err) {
			t.Errorf("isContractFailure(HTTP %d) = false, want true", status)
		}
	}
}

func TestIsContractFailureIgnoresTransientFaults(t *testing.T) {
	for _, status := range []int{
		http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	} {
		err := &enrich.ModelHTTPError{StatusCode: status}
		if isContractFailure(err) {
			t.Errorf("isContractFailure(HTTP %d) = true, want false", status)
		}
	}
	if isContractFailure(nil) {
		t.Error("isContractFailure(nil) = true")
	}
	if isContractFailure(errors.New("random")) {
		t.Error("isContractFailure(generic) = true")
	}
}

func TestIsContractFailureSeesWrappedErrors(t *testing.T) {
	wrapped := errors.Join(errors.New("batch failed"), &enrich.ModelHTTPError{StatusCode: http.StatusUnauthorized})
	if !isContractFailure(wrapped) {
		t.Error("isContractFailure(wrapped) = false, want true")
	}
}

// A stale/conflict response and a rejected internal token must be distinguished:
// the conflict is a superseded job (do not drop readiness), the rejected token
// is a configuration fault (drop readiness).
func TestIsContractFailureDistinguishesStaleFromMisconfiguration(t *testing.T) {
	if isContractFailure(&enrich.ModelHTTPError{StatusCode: http.StatusConflict}) {
		t.Error("a model conflict must not be treated as a contract failure")
	}
	if isContractFailure(&cairn.APIError{StatusCode: http.StatusConflict, Code: "lease_conflict"}) {
		t.Error("a lease conflict must not be treated as a contract failure")
	}
	if !isContractFailure(&cairn.APIError{StatusCode: http.StatusUnauthorized, Code: "unauthorized"}) {
		t.Error("a rejected Worker token must be treated as a contract failure")
	}
}

func TestRootCommandExposesExpectedSubcommands(t *testing.T) {
	root := newRootCommand()
	names := map[string]bool{}
	for _, command := range root.Commands() {
		names[command.Name()] = true
	}
	for _, want := range []string{"serve", "once", "healthcheck", "version"} {
		if !names[want] {
			t.Errorf("root command is missing %q", want)
		}
	}
	if root.Version == "" {
		t.Error("root command has no version, so --version cannot work")
	}
}

func TestNewLoggerAcceptsEveryConfiguredLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if logger := newLogger(level); logger == nil {
			t.Errorf("newLogger(%q) = nil", level)
		}
	}
}

// The root/help path and command discovery must never make a paid call. The
// commands are constructed but not executed, so no client is created.
func TestRootHelpAndCommandDiscoveryMakeNoCalls(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root --help: %v", err)
	}
	root = newRootCommand()
	root.SetArgs([]string{"help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	wanted := map[string]bool{"serve": false, "once": false, "classify": false, "replay": false, "refresh-source": false, "version": false}
	for _, command := range newRootCommand().Commands() {
		if _, ok := wanted[command.Name()]; ok {
			wanted[command.Name()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("command %q is not registered", name)
		}
	}
}

// Replay is an inspection by default and must not silently commit.
func TestReplayCommandRequiresAnIDAndDefaultsToNoCommit(t *testing.T) {
	command := newReplayCommand()
	if flag := command.Flags().Lookup("commit"); flag == nil || flag.DefValue != "false" {
		t.Fatal("replay must default to no commit")
	}
	command.SetArgs([]string{"--id", "0"})
	if err := command.Execute(); err == nil {
		t.Fatal("replay without a positive id must fail before any network call")
	}
}
