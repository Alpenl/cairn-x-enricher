package ablation

import (
	"math"
	"net/url"
	"strings"
	"unicode"
)

// Score is the judged quality of one Outcome. Every metric is deliberately
// mechanical (no model-as-judge) so the same numbers can be recomputed by
// anyone from the stored raw results.
type Score struct {
	// InfraFailed marks a run excluded from quality aggregation because the
	// upstream never answered.
	InfraFailed bool `json:"infra_failed"`
	// Accepted is the production accept/reject decision.
	Accepted bool `json:"accepted"`
	// RejectReason names the rule that rejected the result.
	RejectReason string `json:"reject_reason,omitempty"`

	// Structural completeness: which required fields are non-empty.
	HasTitle       bool `json:"has_title"`
	HasSummary     bool `json:"has_summary"`
	HasLanguage    bool `json:"has_language"`
	HasOriginal    bool `json:"has_original"`
	HasTranslation bool `json:"has_translation"`

	// Title quality.
	TitleRunes   int  `json:"title_runes"`
	TitleChinese bool `json:"title_chinese"`
	TitleInRange bool `json:"title_in_range"`

	// Fidelity: character-level overlap between produced original text and the
	// frozen reference. Only computed when a reference exists.
	OriginalFidelity float64 `json:"original_fidelity"`
	SummaryCoverage  float64 `json:"summary_coverage"`

	// Compliance flags derived from the sample, not the model.
	LanguageCorrect bool `json:"language_correct"`
	LinksValid      bool `json:"links_valid"`
	ImagesValid     bool `json:"images_valid"`
	ImageCount      int  `json:"image_count"`
	LinkCount       int  `json:"link_count"`

	// Classification quality.
	TopicsSelected int  `json:"topics_selected"`
	Uncertainty    bool `json:"uncertainty"`
	DiscardedCount int  `json:"discarded_count"`

	// Grounded signals whether the produced content could be traced to the
	// actual post. When a reference exists, low fidelity means the result was
	// fabricated from parametric memory rather than retrieved.
	Grounded bool `json:"grounded"`

	// Search discipline.
	SearchEvidence bool `json:"search_evidence"`
	// FabricationRisk flags a search-requiring variant that produced content
	// with no search evidence, or an image/link it could not have observed.
	FabricationRisk bool `json:"fabrication_risk"`

	// Cost.
	Tokens       int     `json:"tokens"`
	LatencyMs    int64   `json:"latency_ms"`
	FieldYield   float64 `json:"field_yield"`
	QualityScore float64 `json:"quality_score"`
}

// Judge scores one outcome against its sample.
func Judge(out Outcome, s Sample) Score {
	sc := Score{
		InfraFailed:    out.InfraFailed,
		Accepted:       out.OK() && !out.InfraFailed,
		RejectReason:   firstNonEmpty(out.Err, out.ValidationErr),
		HasTitle:       out.Title != "",
		HasSummary:     out.Summary != "",
		HasLanguage:    out.Language != "",
		HasOriginal:    out.OriginalText != "",
		HasTranslation: out.TranslatedText != "",
		TitleRunes:     len([]rune(out.Title)),
		TitleChinese:   containsHan(out.Title),
		SearchEvidence: out.SearchEvidence,
		Uncertainty:    out.Uncertainty,
		DiscardedCount: len(out.DiscardedTags),
		TopicsSelected: len(out.Selection.Topics),
		ImageCount:     len(out.Images),
		LinkCount:      len(out.Links),
		Tokens:         out.TotalTokens,
		LatencyMs:      out.Latency.Milliseconds(),
	}
	sc.TitleInRange = sc.TitleRunes >= 8 && sc.TitleRunes <= 32

	// Fidelity against the frozen reference, when we have one.
	if s.ReferenceText != "" {
		switch {
		case !out.OK() || out.OriginalText == "":
			sc.OriginalFidelity = 0
		case strings.TrimSpace(out.OriginalText) == strings.TrimSpace(s.ReferenceText):
			sc.OriginalFidelity = 1
		default:
			sc.OriginalFidelity = trigramJaccard(out.OriginalText, s.ReferenceText)
		}
		if out.Summary != "" {
			sc.SummaryCoverage = trigramJaccard(out.Summary, s.ReferenceText)
		}
	}

	// Language: the prompt must preserve the source language. A produced
	// language label that is Chinese while the reference is English, or vice
	// versa, indicates the model translated the "original".
	sc.LanguageCorrect = languageAgrees(out.Language, out.OriginalText)

	// Links must be absolute and none may be the source post itself.
	sc.LinksValid = true
	sourceKey := canonical(s.URL)
	for _, l := range out.Links {
		key, ok := canonicalOK(l)
		if !ok || key == sourceKey {
			sc.LinksValid = false
			break
		}
	}

	sc.ImagesValid = true
	for _, img := range out.Images {
		if _, err := allowedImage(img); err != nil {
			sc.ImagesValid = false
			break
		}
	}

	// Groundedness: when the corpus knows the real post text, the produced
	// original must actually resemble it. A fluent but invented "original" is
	// the single most dangerous failure this pipeline can emit, because every
	// downstream field (title, translation, summary) is built on it and all of
	// them look perfectly well-formed.
	const groundedThreshold = 0.45
	sc.Grounded = s.ReferenceText == "" || sc.OriginalFidelity >= groundedThreshold

	// A variant that skipped the search tool but still emits images or
	// related links it could not have seen is fabricating.
	if !out.SearchEvidence && (len(out.Images) > 0 || len(out.Links) > 0) && s.SourceText == "" {
		sc.FabricationRisk = true
	}
	if out.SearchEvidence && !sc.ImagesValid {
		sc.FabricationRisk = true
	}
	// Producing content that does not match the known post is fabrication even
	// though the transport, schema and validator all accepted it.
	if !sc.Grounded {
		sc.FabricationRisk = true
	}

	// Field yield: fraction of the eight production fields that are present.
	present := 0
	for _, ok := range []bool{sc.HasTitle, sc.HasSummary, sc.HasLanguage, sc.HasOriginal, sc.HasTranslation, sc.LinksValid, sc.ImagesValid, sc.TopicsSelected > 0} {
		if ok {
			present++
		}
	}
	sc.FieldYield = float64(present) / 8.0

	sc.QualityScore = qualityScore(sc)
	return sc
}

// qualityScore blends acceptance, fidelity, and completeness. The weights
// encode the product's priorities: a usable result must be accepted and must
// not fabricate; fidelity and completeness matter after that.
func qualityScore(sc Score) float64 {
	if !sc.Accepted {
		return 0
	}
	// An ungrounded result is not a partial success: it silently corrupts the
	// bookmark. It earns no quality credit regardless of how complete it looks.
	if !sc.Grounded {
		return 0
	}
	score := 0.0
	score += 0.40 // accepted
	if sc.TitleChinese && sc.TitleInRange {
		score += 0.10
	}
	if sc.HasTranslation {
		score += 0.10
	}
	if sc.HasSummary {
		score += 0.05
	}
	if sc.LanguageCorrect {
		score += 0.05
	}
	score += 0.15 * sc.OriginalFidelity
	if sc.LinksValid {
		score += 0.05
	}
	if sc.ImagesValid {
		score += 0.05
	}
	if sc.TopicsSelected > 0 {
		score += 0.05
	}
	if sc.FabricationRisk {
		score -= 0.30
	}
	return math.Max(0, math.Min(1, score))
}

// scriptProfile counts the writing systems present in a text. Han characters
// alone cannot distinguish Chinese from Japanese, because Japanese prose uses
// Han heavily; kana is the discriminating signal.
type scriptProfile struct {
	Han   int
	Kana  int
	Latin int
	Other int
}

func profile(text string) scriptProfile {
	var p scriptProfile
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
		case unicode.Is(unicode.Han, r):
			p.Han++
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			p.Kana++
		case unicode.Is(unicode.Latin, r):
			p.Latin++
		default:
			p.Other++
		}
	}
	return p
}

// languageAgrees checks that the declared original language is consistent with
// the script the produced original text is actually written in.
func languageAgrees(language, text string) bool {
	if language == "" || text == "" {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	p := profile(text)
	// Kana is decisive: any meaningful amount means Japanese, not Chinese.
	if p.Kana > 0 {
		return strings.HasPrefix(lang, "ja") || strings.Contains(lang, "japanese") ||
			strings.Contains(lang, "日语") || strings.Contains(lang, "日文")
	}
	declaredChinese := strings.HasPrefix(lang, "zh") || strings.Contains(lang, "chinese") ||
		strings.Contains(lang, "中文") || strings.Contains(lang, "汉")
	declaredJapanese := strings.HasPrefix(lang, "ja") || strings.Contains(lang, "japanese")
	if declaredJapanese {
		// Japanese prose almost always contains kana. Han-only text declared as
		// Japanese is far more likely to be Chinese that was mislabelled or a
		// translation that lost the source language, so require some kana.
		total := p.Han + p.Kana + p.Latin + p.Other
		return total > 0 && float64(p.Kana)/float64(total) > 0.05
	}
	declaredLatin := strings.HasPrefix(lang, "en") || strings.Contains(lang, "english") ||
		strings.Contains(lang, "英语") || strings.Contains(lang, "英文")
	if declaredLatin {
		// The declared text is Latin-script: Han must be a small minority.
		total := p.Han + p.Kana + p.Latin + p.Other
		return total > 0 && float64(p.Latin)/float64(total) > 0.60
	}
	return declaredChinese == (p.Han > 0)
}

// trigramJaccard is a character-trigram overlap in [0,1]. It is robust to
// whitespace and small reordering while still penalising omission.
func trigramJaccard(a, b string) float64 {
	ta := trigrams(a)
	tb := trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for g, ca := range ta {
		if cb, ok := tb[g]; ok {
			inter += min(ca, cb)
		}
	}
	union := 0
	for g, ca := range ta {
		cb := tb[g]
		union += max(ca, cb)
	}
	for g, cb := range tb {
		if _, ok := ta[g]; !ok {
			union += cb
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func trigrams(s string) map[string]int {
	r := []rune(strings.Join(strings.Fields(s), ""))
	out := map[string]int{}
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])]++
	}
	return out
}

func canonical(raw string) string {
	key, _ := canonicalOK(raw)
	return key
}

func canonicalOK(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	return parsed.String(), true
}

func allowedImage(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "pbs.twimg.com" {
		return "", errString("must use HTTPS on pbs.twimg.com")
	}
	if parsed.User != nil || parsed.Port() != "" || !strings.HasPrefix(parsed.EscapedPath(), "/media/") {
		return "", errString("must identify a pbs.twimg.com/media object")
	}
	return parsed.String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
