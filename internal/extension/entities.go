package extension

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// SurfaceCandidate is one entity surface form located in the stored evidence.
// Every candidate must point back to the exact span it came from so a human can
// verify it; the extractor never invents a name that is not in the text.
type SurfaceCandidate struct {
	Surface string `json:"surface"`
	// Start and End are rune offsets into the block the span belongs to.
	Start int `json:"start"`
	End   int `json:"end"`
	// BlockID identifies the evidence block the span lives in.
	BlockID string `json:"block_id"`
	// SourceURL is set only when the candidate is a URL that already exists in
	// the stored links, never one the extractor chose.
	SourceURL string `json:"source_url,omitempty"`
	// Kind is "surface" for a name found in text or "link" for a stored URL.
	Kind string `json:"kind"`
}

// Block is the extractor's view of persisted evidence.
type Block struct {
	ID   string
	Text string
	Role string `json:"role,omitempty"`
	URL  string `json:"url,omitempty"`
}

// MaxCandidates bounds the candidate set so a long article cannot produce an
// unbounded fan-out.
const MaxCandidates = 40

// MinSurfaceRunes avoids extracting single characters.
const MinSurfaceRunes = 2

// ExtractCandidates finds capitalized/latin and CJK run-name candidates with
// their exact rune spans. It is purely local: no model, no network, no new
// names. A caller may pass stored links to surface existing URLs.
func ExtractCandidates(blocks []Block, storedURLs []string) []SurfaceCandidate {
	candidates := []SurfaceCandidate{}
	seen := map[string]bool{}
	for _, block := range blocks {
		runes := []rune(block.Text)
		index := 0
		for index < len(runes) {
			start := index
			if !isRunStart(runes[index]) {
				index++
				continue
			}
			index++
			for index < len(runes) && isRunPart(runes[index]) {
				index++
			}
			trimmedStart, trimmedEnd := trimSpan(runes, start, index)
			surface := string(runes[trimmedStart:trimmedEnd])
			if len([]rune(surface)) < MinSurfaceRunes || !entitySurfaceFits(surface) {
				continue
			}
			key := fmt.Sprintf("%s:%d:%d", block.ID, trimmedStart, trimmedEnd)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, SurfaceCandidate{
				Surface: surface, Start: trimmedStart, End: trimmedEnd, BlockID: block.ID, Kind: "surface",
			})
			if len(candidates) >= MaxCandidates {
				return candidates
			}
		}
	}
	// Stored links are surfaced verbatim so the model may only choose among
	// URLs that already exist in the record.
	for _, value := range storedURLs {
		if (!strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://")) || !entitySurfaceFits(value) {
			continue
		}
		key := "url:" + strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, SurfaceCandidate{Surface: value, SourceURL: value, Kind: "link"})
		if len(candidates) >= MaxCandidates {
			break
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].BlockID == candidates[j].BlockID {
			return candidates[i].Start < candidates[j].Start
		}
		return candidates[i].BlockID < candidates[j].BlockID
	})
	return candidates
}

// The existing stored entity term contract allows 120 UTF-16 units. Skip an
// overlong run rather than truncating it into a name absent from the source.
func entitySurfaceFits(value string) bool {
	units := 0
	for _, r := range value {
		units++
		if r > 0xffff {
			units++
		}
		if units > 120 {
			return false
		}
	}
	return true
}

// EntityVerdict is the narrow judgment the model is allowed to make about one
// candidate. It never chooses a name outside the candidate set.
type EntityVerdict struct {
	Surface string `json:"surface"`
	// Canonical is the matched candidate ID from a controlled list, or empty.
	Canonical string `json:"canonical"`
	// Decision is one of relevant, incidental, none, unknown.
	Decision string `json:"decision"`
	// Reason is derived from the observable evidence, never guessed.
	Reason string `json:"reason"`
}

// ValidateVerdict rejects a model answer that invents a name or a canonical id
// outside the allowed set, and requires an explicit unknown/none option.
func ValidateVerdict(verdict EntityVerdict, allowedSurfaces map[string]bool, allowedCanonical map[string]bool) error {
	if !allowedSurfaces[strings.ToLower(verdict.Surface)] {
		return errors.New("verdict names a surface that is not in the candidate set")
	}
	switch verdict.Decision {
	case "relevant", "incidental", "none", "unknown":
	default:
		return errors.New("verdict decision must be relevant, incidental, none or unknown")
	}
	if verdict.Canonical != "" && !allowedCanonical[verdict.Canonical] {
		return errors.New("verdict canonical id is not in the controlled candidate set")
	}
	// Same-name entities are never merged without evidence: an empty canonical
	// is the correct answer when nothing supports a match.
	return nil
}

func isRunStart(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLetter(r) && unicode.Is(unicode.Han, r) || unicode.IsDigit(r)
}

func isRunPart(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	// Allow a small set of joiners inside a name so "GPT-4" stays one surface.
	switch r {
	case '-', '_', '.', '+', '#':
		return true
	default:
		return false
	}
}

func trimSpan(runes []rune, start, end int) (int, int) {
	for start < end && !unicode.IsLetter(runes[start]) && !unicode.IsDigit(runes[start]) {
		start++
	}
	for end > start && !unicode.IsLetter(runes[end-1]) && !unicode.IsDigit(runes[end-1]) {
		end--
	}
	return start, end
}
