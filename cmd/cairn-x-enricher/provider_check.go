package main

import (
	"context"
	"errors"
	"net"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/enrich"
)

func providerCheckReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	var upstream *enrich.ModelHTTPError
	if errors.As(err, &upstream) {
		if upstream.StatusCode == 401 || upstream.StatusCode == 403 {
			return "unauthorized"
		}
		if upstream.StatusCode == 429 {
			return "rate_limited"
		}
		return "unavailable"
	}
	var api *cairn.APIError
	if errors.As(err, &api) && api.StatusCode == 429 {
		if api.Code == "canary_cooldown" {
			return "rate_limited"
		}
		return "budget_exhausted"
	}
	return "invalid_response"
}
