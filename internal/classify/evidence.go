package classify

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// BlockRole identifies what a piece of evidence is. The roles are kept
// separate because a quoted disagreement is not the same as the author's claim,
// and third-party material must not silently replace the primary text.
type BlockRole string

// Block roles. Each role is explicit because a quoted disagreement is not the
// author's claim and third-party material must not replace the primary text.
const (
	RolePrimary            BlockRole = "primary"
	RoleAuthorContinuation BlockRole = "author_continuation"
	RoleQuoted             BlockRole = "quoted"
	RoleExternalArticle    BlockRole = "external_article"
	RoleThirdParty         BlockRole = "third_party"
	RoleLegacyUnknown      BlockRole = "legacy_unknown"
)

// EvidenceBlock is one ordered piece of objective evidence.
type EvidenceBlock struct {
	ID       string    `json:"id"`
	Role     BlockRole `json:"role"`
	Text     string    `json:"text"`
	URL      string    `json:"url,omitempty"`
	Relation string    `json:"relation,omitempty"`
	Acquired string    `json:"acquired,omitempty"`
}

// Evidence is the objective state sent to the model. It contains no note, no
// why, no curation status and no user stance: those are personal and are not
// evidence about the content.
type Evidence struct {
	Primary   string          `json:"primary"`
	Context   []EvidenceBlock `json:"context,omitempty"`
	Truncated bool            `json:"truncated"`
	Coverage  string          `json:"coverage"`
	// TotalRunes counts source text before request truncation, excluding metadata.
	TotalRunes int `json:"total_runes"`
}

// Budget bounds the serialized evidence. The estimate is explicit and
// conservative rather than pretending a character count is a token count.
type Budget struct {
	MaxRunes int
	// MaxBlocks bounds how many context blocks are sent so a long thread cannot
	// crowd out the primary text.
	MaxBlocks int
	// Byte limits apply after JSON serialization, including metadata/escaping
	// and the complete question instructions/criteria. Zero uses defaults.
	MaxStateBytes   int
	MaxRequestBytes int
}

// DefaultBudget bounds characters and actual HTTP bytes independently. These
// are application limits, not a claim about a provider tokenizer or context size.
func DefaultBudget() Budget {
	return Budget{MaxRunes: 12000, MaxBlocks: 12, MaxStateBytes: 48 << 10, MaxRequestBytes: 128 << 10}
}

func (b Budget) stateBytes() int {
	if b.MaxStateBytes > 0 {
		return b.MaxStateBytes
	}
	return DefaultBudget().MaxStateBytes
}

func (b Budget) requestBytes() int {
	if b.MaxRequestBytes > 0 {
		return b.MaxRequestBytes
	}
	return DefaultBudget().MaxRequestBytes
}

// PrepareEvidence builds the objective state. Personal fields are excluded by
// construction: there is no field on the input for them.
func PrepareEvidence(primary string, context []EvidenceBlock, budget Budget) (Evidence, error) {
	if budget.MaxRunes <= 0 || budget.MaxBlocks <= 0 {
		return Evidence{}, errors.New("evidence budget must be positive")
	}
	trimmedPrimary := strings.TrimSpace(primary)
	if trimmedPrimary == "" {
		return Evidence{}, errors.New("evidence requires primary text")
	}
	evidence := Evidence{Primary: "", Context: []EvidenceBlock{}, Coverage: "complete"}
	evidence.TotalRunes = utf8.RuneCountInString(primary)
	for _, block := range context {
		evidence.TotalRunes += utf8.RuneCountInString(block.Text)
	}
	// The primary text always has priority; only it may be truncated, and the
	// truncation is recorded rather than silent.
	primaryRunes := utf8.RuneCountInString(trimmedPrimary)
	if primaryRunes > budget.MaxRunes {
		evidence.Primary = truncateRunes(trimmedPrimary, budget.MaxRunes)
		evidence.Truncated = true
		evidence.Coverage = "truncated"
		return boundSerializedState(evidence, budget.stateBytes())
	}
	evidence.Primary = trimmedPrimary
	remaining := budget.MaxRunes - primaryRunes
	for index, block := range context {
		if len(evidence.Context) >= budget.MaxBlocks {
			evidence.Truncated = true
			evidence.Coverage = "truncated"
			break
		}
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		runes := utf8.RuneCountInString(text)
		if runes > remaining {
			if remaining < 80 {
				evidence.Truncated = true
				evidence.Coverage = "truncated"
				break
			}
			text = truncateRunes(text, remaining)
			runes = remaining
			evidence.Truncated = true
			evidence.Coverage = "truncated"
		}
		block.Text = text
		if block.Role == "" {
			// An unlabelled block is honestly unknown rather than assumed.
			block.Role = RoleLegacyUnknown
		}
		evidence.Context = append(evidence.Context, block)
		remaining -= runes
		if remaining <= 0 {
			if index+1 < len(context) {
				evidence.Truncated = true
				evidence.Coverage = "truncated"
			}
			break
		}
	}
	return boundSerializedState(evidence, budget.stateBytes())
}

// Keep complete block identities/URLs. If metadata pushes JSON over the byte
// limit, remove trailing context blocks before shortening the primary. Work on
// the prepared copy, never the archived snapshot supplied by the caller.
func boundSerializedState(e Evidence, limit int) (Evidence, error) {
	for {
		encoded, err := e.stateForModel()
		if err != nil {
			return Evidence{}, err
		}
		if len(encoded) <= limit {
			return e, nil
		}
		e.Truncated = true
		e.Coverage = "truncated"
		if len(e.Context) > 0 {
			e.Context = e.Context[:len(e.Context)-1]
			continue
		}
		original := []rune(e.Primary)
		low, high := 1, len(original)
		best := ""
		for low <= high {
			mid := (low + high) / 2
			e.Primary = string(original[:mid])
			encoded, err = e.stateForModel()
			if err != nil {
				return Evidence{}, err
			}
			if len(encoded) <= limit {
				best = e.Primary
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if best == "" {
			return Evidence{}, errors.New("state byte budget cannot fit primary evidence")
		}
		e.Primary = best
		return e, nil
	}
}

// stateForModel serializes only the objective evidence. It is used by tests to
// assert that personal fields cannot appear in the outbound body.
func (e Evidence) stateForModel() ([]byte, error) {
	return json.Marshal(map[string]any{
		"primary": e.Primary, "context": e.Context,
		"truncated": e.Truncated, "coverage": e.Coverage,
	})
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// CheckNoPersonalFields fails if a serialized state contains a personal key.
// It exists so the exclusion is verified on the actual bytes rather than only
// promised in a prompt.
func CheckNoPersonalFields(encoded []byte) error {
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return fmt.Errorf("state is not an object: %w", err)
	}
	return walkPersonal(generic)
}

func walkPersonal(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, entry := range typed {
			switch strings.ToLower(key) {
			case "note", "why", "why_suggestion", "curation_status", "status", "stance", "intent":
				return fmt.Errorf("objective state contains personal field %q", key)
			}
			if err := walkPersonal(entry); err != nil {
				return err
			}
		}
	case []any:
		for _, entry := range typed {
			if err := walkPersonal(entry); err != nil {
				return err
			}
		}
	}
	return nil
}
