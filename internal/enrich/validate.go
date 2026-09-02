package enrich

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	maxOriginalTextLength = 100_000
	maxSummaryLength      = 4_000
	maxRelatedLinks       = 50
)

func validateCandidate(_ context.Context, candidate Candidate) (Result, error) {
	if !candidate.SearchVerified {
		return Result{}, errors.New("model did not provide evidence of a completed X search")
	}

	result := candidate.Result
	result.OriginalText = strings.TrimSpace(result.OriginalText)
	result.Summary = strings.TrimSpace(result.Summary)
	result.Model = strings.TrimSpace(result.Model)
	if result.OriginalText == "" || len(result.OriginalText) > maxOriginalTextLength {
		return Result{}, fmt.Errorf("original_text must contain 1 to %d bytes", maxOriginalTextLength)
	}
	if result.Summary == "" || len(result.Summary) > maxSummaryLength {
		return Result{}, fmt.Errorf("summary must contain 1 to %d bytes", maxSummaryLength)
	}
	if result.Model == "" || len(result.Model) > 200 {
		return Result{}, errors.New("model identifier is missing or too long")
	}
	if len(result.RelatedLinks) > maxRelatedLinks {
		return Result{}, fmt.Errorf("related_links contains more than %d entries", maxRelatedLinks)
	}

	seen := make(map[string]struct{}, len(result.RelatedLinks))
	links := make([]string, 0, len(result.RelatedLinks))
	sourceKey, _ := canonicalURL(candidate.Input.URL)
	for _, raw := range result.RelatedLinks {
		link := strings.TrimSpace(raw)
		key, err := canonicalURL(link)
		if err != nil {
			return Result{}, fmt.Errorf("invalid related link %q: %w", link, err)
		}
		if key == sourceKey {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, link)
	}
	result.RelatedLinks = links
	return result, nil
}

func canonicalURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return "", errors.New("must not contain credentials")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return parsed.String(), nil
}
