package main

import (
	"errors"
	"net/http"
	"testing"

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
