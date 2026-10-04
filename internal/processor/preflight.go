package processor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

// ErrSourcePreflightPaused keeps an unavailable source provider from consuming
// business leases while the Reader and independent classifier remain usable.
var ErrSourcePreflightPaused = errors.New("source provider contract check is paused")

// ErrSourcePreflightUnverified defers the first check until the Reader and
// independent classification scheduler have started.
var ErrSourcePreflightUnverified = errors.New("source provider contract check is pending")

type sourcePreflight struct {
	mu      sync.Mutex
	ready   bool
	check   func(context.Context) error
	gate    *componentPause
	status  func(context.Context) (cairn.ProviderCheckStatus, error)
	recover func(context.Context) (cairn.ProviderCheckStatus, error)
}

// SetSourcePreflight is configured once before schedulers start. An initial
// failure pauses only paid source/reading work; recovery never drains job leases.
func (p *Processor) SetSourcePreflight(check func(context.Context) error, initialErr error) {
	if p.stages == nil || check == nil || initialErr == nil {
		return
	}
	gate := newComponentPause()
	gate.trip("source provider contract check failed")
	if errors.Is(initialErr, ErrSourcePreflightUnverified) {
		gate.until = gate.now()
		gate.reason = "source provider contract check pending"
	}
	p.stages.preflight = &sourcePreflight{check: check, gate: gate}
}

func (g *sourcePreflight) state() (bool, string, time.Duration) {
	g.mu.Lock()
	ready := g.ready
	g.mu.Unlock()
	if ready {
		return false, "", 0
	}
	return g.gate.state()
}

func (p *Processor) checkSourcePreflight(ctx context.Context) error {
	if p.stages == nil || p.stages.preflight == nil {
		return nil
	}
	g := p.stages.preflight
	g.mu.Lock()
	if g.ready {
		g.mu.Unlock()
		return nil
	}
	if ctx.Err() != nil {
		g.mu.Unlock()
		return ctx.Err()
	}
	allowed, _, epoch, _ := g.gate.beginStageProbe()
	g.mu.Unlock()
	if !allowed {
		return ErrSourcePreflightPaused
	}
	// The provider callback retains its own persistent paid admission. A
	// cancelled caller cannot grant a recovery permission to another caller.
	defer func() {
		if recovered := recover(); recovered != nil {
			g.gate.finishStageProbe(epoch, false, "source provider contract check failed")
			panic(recovered)
		}
		g.gate.releaseStageProbe(epoch)
	}()
	probeCtx, cancel := context.WithTimeout(ctx, p.stages.paidStageTimeout)
	defer cancel()
	finish := trackProgress(ctx, p.stages.paidStageTimeout+30*time.Second)
	defer finish()
	if err := g.check(probeCtx); err != nil {
		g.gate.finishStageProbe(epoch, false, "source provider contract check failed")
		var retry *PreflightRetry
		if errors.As(err, &retry) {
			g.gate.mu.Lock()
			if retry.At.After(g.gate.until) {
				g.gate.until = retry.At
			}
			g.gate.mu.Unlock()
		}
		return ErrSourcePreflightPaused
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.gate.finishStageProbe(epoch, true, "") {
		return ErrSourcePreflightPaused
	}
	g.ready = true
	for _, pause := range []*componentPause{p.stages.sourcePause, p.stages.readingPause} {
		if pause != nil {
			pause.mu.Lock()
			if !pause.until.IsZero() && !pause.probing {
				pause.until = pause.now()
			}
			pause.mu.Unlock()
		}
	}
	return nil
}

// SetClassificationWakeup installs a coalescing notification channel. The
// persisted queue remains authoritative, and periodic polls remain a fallback.
func (p *Processor) SetClassificationWakeup(wakeup chan<- struct{}) {
	if p.stages != nil {
		p.stages.classificationWakeup = wakeup
	}
}

func (p *Processor) notifyClassification() {
	if p.stages == nil || p.stages.classificationWakeup == nil {
		return
	}
	select {
	case p.stages.classificationWakeup <- struct{}{}:
	default:
	}
}

// SetSourceRecovery connects persistent status and manual admission before serving.
func (p *Processor) SetSourceRecovery(status, requestRecovery func(context.Context) (cairn.ProviderCheckStatus, error)) {
	if p.stages != nil && p.stages.preflight != nil {
		p.stages.preflight.status = status
		p.stages.preflight.recover = requestRecovery
	}
}

// SourceRecoveryStatus reads the shared check result without starting a model call.
func (p *Processor) SourceRecoveryStatus(ctx context.Context) (cairn.ProviderCheckStatus, error) {
	if p.stages == nil || p.stages.preflight == nil || p.stages.preflight.status == nil {
		return cairn.ProviderCheckStatus{}, errors.New("source recovery unavailable")
	}
	return p.stages.preflight.status(ctx)
}

// RecoverSource only advances a probe; no business leases or paid history are reset.
func (p *Processor) RecoverSource(ctx context.Context) (cairn.ProviderCheckStatus, error) {
	if p.stages == nil || p.stages.preflight == nil || p.stages.preflight.recover == nil {
		return cairn.ProviderCheckStatus{}, errors.New("source recovery unavailable")
	}
	g := p.stages.preflight
	status, err := g.recover(ctx)
	if err != nil || (!status.Accepted && status.State != "pending") {
		return status, err
	}
	g.mu.Lock()
	g.ready = false
	g.gate.mu.Lock()
	// A running local probe owns its epoch and must finish normally.
	if !g.gate.probing {
		g.gate.until = g.gate.now()
		g.gate.reason = "source provider contract check pending"
	}
	g.gate.mu.Unlock()
	g.mu.Unlock()
	return status, nil
}

// PreflightRetry carries a remote retry deadline without disclosing provider text.
type PreflightRetry struct{ At time.Time }

func (e *PreflightRetry) Error() string { return "source provider check waiting for retry" }
