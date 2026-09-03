package enrich

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minAITitleRunes         = 8
	maxAITitleRunes         = 32
	maxOriginalLanguageLen  = 32
	maxOriginalTextLength   = 100_000
	maxTranslatedTextLength = 100_000
	maxSummaryLength        = 4_000
	maxRelatedLinks         = 50
	maxImageURLs            = 8
)

func validateCandidate(_ context.Context, candidate Candidate) (Result, error) {
	if !candidate.SearchVerified {
		return Result{}, errors.New("model did not provide evidence of a completed X search")
	}

	result := candidate.Result
	result.AITitle = strings.TrimSpace(result.AITitle)
	result.OriginalLanguage = strings.TrimSpace(result.OriginalLanguage)
	result.OriginalText = strings.TrimSpace(result.OriginalText)
	result.TranslatedText = strings.TrimSpace(result.TranslatedText)
	result.Summary = strings.TrimSpace(result.Summary)
	result.Model = strings.TrimSpace(result.Model)
	titleRunes := utf8.RuneCountInString(result.AITitle)
	if titleRunes < minAITitleRunes || titleRunes > maxAITitleRunes || !containsHan(result.AITitle) {
		return Result{}, fmt.Errorf("ai_title must contain Chinese and be %d to %d characters", minAITitleRunes, maxAITitleRunes)
	}
	if result.OriginalLanguage == "" || len(result.OriginalLanguage) > maxOriginalLanguageLen {
		return Result{}, fmt.Errorf("original_language must contain 1 to %d bytes", maxOriginalLanguageLen)
	}
	if result.OriginalText == "" || len(result.OriginalText) > maxOriginalTextLength {
		return Result{}, fmt.Errorf("original_text must contain 1 to %d bytes", maxOriginalTextLength)
	}
	if result.TranslatedText == "" || len(result.TranslatedText) > maxTranslatedTextLength {
		return Result{}, fmt.Errorf("translated_text must contain 1 to %d bytes", maxTranslatedTextLength)
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

	if len(result.ImageURLs) > maxImageURLs {
		return Result{}, fmt.Errorf("image_urls contains more than %d entries", maxImageURLs)
	}
	seenImages := make(map[string]struct{}, len(result.ImageURLs))
	images := make([]string, 0, len(result.ImageURLs))
	for _, raw := range result.ImageURLs {
		imageURL, err := allowedImageURL(raw)
		if err != nil {
			return Result{}, fmt.Errorf("invalid image URL %q: %w", strings.TrimSpace(raw), err)
		}
		if _, exists := seenImages[imageURL]; exists {
			continue
		}
		seenImages[imageURL] = struct{}{}
		images = append(images, imageURL)
	}
	result.ImageURLs = images
	return result, nil
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func allowedImageURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "pbs.twimg.com" {
		return "", errors.New("must use HTTPS on pbs.twimg.com")
	}
	if parsed.User != nil || parsed.Port() != "" || !strings.HasPrefix(parsed.EscapedPath(), "/media/") {
		return "", errors.New("must identify a pbs.twimg.com/media object without credentials or a custom port")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
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
