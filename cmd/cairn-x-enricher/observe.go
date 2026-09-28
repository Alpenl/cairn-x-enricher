package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/observability"
	"github.com/spf13/cobra"
)

func newObserveCommand() *cobra.Command {
	address := strings.TrimSpace(os.Getenv("CAIRN_OBSERVABILITY_CONTROL_ADDR"))
	if address == "" {
		address = "127.0.0.1:9090"
	}
	command := &cobra.Command{Use: "observe", Short: "Inspect or change local observability without restarting serve"}
	command.PersistentFlags().StringVar(&address, "address", address, "container-loopback control address")
	command.AddCommand(&cobra.Command{Use: "show", Short: "Show desired and effective local observability state", RunE: func(cmd *cobra.Command, _ []string) error {
		return callObserve(cmd.Context(), address, http.MethodGet, "/v1/observability", nil, cmd.OutOrStdout())
	}})
	var mode string
	var duration time.Duration
	var expected uint64
	set := &cobra.Command{Use: "set-log", Short: "Set local JSON logging mode with version check", RunE: func(cmd *cobra.Command, _ []string) error {
		if mode == string(observability.LogDiagnostic) && duration == 0 {
			duration = 15 * time.Minute
		}
		if duration < 0 || duration > observability.MaxDiagnostic || duration%time.Second != 0 {
			return fmt.Errorf("duration must be whole seconds within 0..%s", observability.MaxDiagnostic)
		}
		body := map[string]any{"expected_version": expected, "mode": mode, "duration_seconds": int64(duration / time.Second)}
		return callObserve(cmd.Context(), address, http.MethodPut, "/v1/observability/logs", body, cmd.OutOrStdout())
	}}
	set.Flags().StringVar(&mode, "mode", "", "off, basic, or diagnostic")
	set.Flags().DurationVar(&duration, "duration", 0, "diagnostic lifetime (default 15m, maximum 1h)")
	set.Flags().Uint64Var(&expected, "expected-version", 0, "version returned by observe show")
	_ = set.MarkFlagRequired("mode")
	_ = set.MarkFlagRequired("expected-version")
	command.AddCommand(set)
	return command
}

func callObserve(ctx context.Context, address, method, path string, body any, output io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("observability control address must be a literal loopback IP and port")
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	//nolint:gosec // The CLI accepts only a literal loopback IP and fixed paths.
	request, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	//nolint:gosec // The request URL was restricted to a literal loopback IP.
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("observability control returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	_, err = output.Write(data)
	return err
}
