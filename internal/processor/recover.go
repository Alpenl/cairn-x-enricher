package processor

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// RecoverJob converts a panic in a job-executing goroutine into a logged error.
//
// A panic in a bare goroutine terminates the whole process, taking down the
// HTTP server with it, and restarts under `restart: unless-stopped` would
// crash-loop without recording which bookmark was responsible. Reporting the
// panic instead keeps the service serving and makes the failure attributable.
//
// Usage: `defer processor.RecoverJob(logger, "manual enrichment", jobID, &err)`.
// The named return value lets the caller record the recovered failure as a
// normal job failure rather than losing it.
func RecoverJob(logger *slog.Logger, operation string, jobID int64, err *error) {
	recovered := recover()
	if recovered == nil {
		return
	}
	// The stack is essential here: this path is only reachable from a bug, and
	// the panic value alone rarely identifies it.
	logger.Error(operation+" panicked",
		"link_id", jobID,
		"panic", fmt.Sprint(recovered),
		"stack", string(debug.Stack()),
	)
	if err != nil && *err == nil {
		*err = fmt.Errorf("%s panicked: %v", operation, recovered)
	}
}

// RecoverTask is RecoverJob for long-running goroutines that do not have a
// single job to attribute the failure to, such as a scheduler loop.
func RecoverTask(logger *slog.Logger, operation string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	logger.Error(operation+" panicked",
		"panic", fmt.Sprint(recovered),
		"stack", string(debug.Stack()),
	)
}
