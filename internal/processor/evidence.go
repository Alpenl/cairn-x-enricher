package processor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/extension"
)

func shortURLHash(raw string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:24] }

// Only idempotent writes are retried here; external fetch is never inside this
// retry loop. The caller gives every operation a bounded context.
func retryEvidenceWrite(ctx context.Context, action func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = action(); err == nil {
			return nil
		}
		if cairn.IsUnsupported(err) {
			return err
		}
	}
	return err
}
func (p *Processor) recoverEvidence(ctx context.Context, remaining *int) {
	if *remaining <= 0 {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	requests, err := p.stages.queue.RecoverableEvidenceRequests(readCtx, min(*remaining, 20))
	cancel()
	if err != nil {
		p.logger.WarnContext(ctx, "evidence recovery listing failed", "error", err)
		return
	}
	for _, request := range requests {
		if ctx.Err() != nil || *remaining <= 0 {
			return
		}
		p.executeEvidence(ctx, request.ID, remaining)
	}
}
func (p *Processor) executeEvidence(ctx context.Context, requestID string, remaining *int) {
	s := p.stages
	if *remaining <= 0 || !s.extensions.Flags.Evidence {
		return
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return
	}
	owner := fmt.Sprintf("%x", token)
	claimCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var request cairn.EvidenceExecution
	err := retryEvidenceWrite(claimCtx, func() error {
		var err error
		request, err = s.queue.ClaimEvidenceRequest(claimCtx, requestID, owner)
		return err
	})
	cancel()
	if err != nil {
		p.logger.WarnContext(ctx, "evidence execution claim failed", "request_id", requestID, "error", err)
		return
	}
	checkpointHash := ""
	if request.CheckpointHash != nil {
		checkpointHash = *request.CheckpointHash
	}
	if request.Status != "checkpointed" {
		if !request.Owned || request.OwnerToken == nil || *request.OwnerToken != owner || request.Status != "fetching" {
			return
		}
		*remaining--
		policy := s.fetchPolicy
		policy.MaxBytes = min(policy.MaxBytes, request.Budget.MaxBytes)
		policy.Timeout = min(policy.Timeout, time.Duration(request.Budget.TimeoutMS)*time.Millisecond, 30*time.Second)
		outcome := extension.FetchOutcome{State: "blocked", Reason: "invalid bounded fetch policy"}
		if request.Scope == "external_link" && policy.MaxBytes > 0 && policy.Timeout > 0 {
			if fetcher, err := extension.ControlledFetcher(policy, s.fetcher); err == nil {
				fetchCtx, stop := context.WithTimeout(ctx, policy.Timeout)
				outcome = s.extensions.RequestEvidence(fetchCtx, fetcher, policy, request.URL)
				stop()
			}
		}
		// Keep the finite result through cancellation long enough to checkpoint it.
		// If the process dies before this durable boundary, a later owner may retry
		// at most once; the server's attempts budget never resets on a replay.
		saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		err = retryEvidenceWrite(saveCtx, func() error {
			var err error
			checkpointHash, err = s.queue.CheckpointEvidenceRequest(saveCtx, request.ID, owner, outcome)
			return err
		})
		stop()
		if err != nil {
			p.logger.WarnContext(ctx, "evidence checkpoint failed", "request_id", request.ID, "error", err)
			return
		}
	}
	finalizeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer stop()
	err = retryEvidenceWrite(finalizeCtx, func() error {
		_, err := s.queue.FinalizeEvidenceRequest(finalizeCtx, request.ID, checkpointHash)
		return err
	})
	if err != nil {
		p.logger.WarnContext(ctx, "evidence result remains recoverable", "request_id", request.ID, "error", err)
	}
}

// Prefer an allowed gap. If all stored gaps are denied, retain one bounded
// blocked result instead of silently dropping the attempted escalation.
func firstMissingAllowlisted(blocks []extension.Block, links []string, policy extension.FetchPolicy) string {
	blocked := ""
	for _, link := range links {
		if extension.DetectGap(blocks, []string{link}, false) != extension.GapExternalLink {
			continue
		}
		if _, err := extension.ValidateURL(policy, link); err == nil {
			return link
		}
		if blocked == "" {
			blocked = link
		}
	}
	return blocked
}
