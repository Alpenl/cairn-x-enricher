package ablation

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// Record is one (variant, sample) observation with its score.
type Record struct {
	Variant string  `json:"variant"`
	Ablates string  `json:"ablates"`
	Sample  int64   `json:"sample"`
	URL     string  `json:"url"`
	Outcome Outcome `json:"outcome"`
	Score   Score   `json:"score"`
	Skipped string  `json:"skipped,omitempty"`
}

// VariantSummary aggregates one variant across the corpus.
type VariantSummary struct {
	Variant            string  `json:"variant"`
	Ablates            string  `json:"ablates"`
	Runs               int     `json:"runs"`
	Accepted           int     `json:"accepted"`
	AcceptRate         float64 `json:"accept_rate"`
	MeanQuality        float64 `json:"mean_quality"`
	MeanFidelity       float64 `json:"mean_fidelity"`
	MeanFieldYield     float64 `json:"mean_field_yield"`
	MeanTokens         float64 `json:"mean_tokens"`
	MeanLatencyMs      float64 `json:"mean_latency_ms"`
	SearchEvidence     int     `json:"search_evidence"`
	FabricationRisk    int     `json:"fabrication_risk"`
	Skipped            int     `json:"skipped"`
	InfraFailed        int     `json:"infra_failed"`
	LanguageErrors     int     `json:"language_errors"`
	TranslationMiss    int     `json:"translation_missing"`
	ClassificationMiss int     `json:"classification_missing"`
	Failures           int     `json:"failures"`
	// Delta vs FULL, filled in after aggregation.
	QualityDelta float64 `json:"quality_delta_vs_full"`
	TokenDelta   float64 `json:"token_delta_vs_full"`
	LatencyDelta float64 `json:"latency_delta_vs_full"`
}

// Journal appends records to a JSONL file as they complete, so a long run that
// is interrupted by a slow endpoint still yields usable partial data.
type Journal struct {
	mu   sync.Mutex
	file *os.File
}

// OpenJournal creates or truncates a journal at path.
func OpenJournal(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	//nolint:gosec // journal path is an operator-supplied CLI argument
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Journal{file: f}, nil
}

// Append writes one record as a JSON line.
func (j *Journal) Append(rec Record) {
	if j == nil || j.file == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = j.file.Write(append(line, '\n'))
	_ = j.file.Sync()
}

// Close releases the journal file.
func (j *Journal) Close() {
	if j != nil && j.file != nil {
		_ = j.file.Close()
	}
}

// LoadJournal reads back records written by a previous run.
func LoadJournal(path string) ([]Record, error) {
	//nolint:gosec // journal path is an operator-supplied CLI argument
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Run executes the matrix. Each variant is run against every sample with
// bounded parallelism. Every completed observation is appended to the journal
// immediately so an interrupted run still produces data.
func Run(ctx context.Context, r *Runner, variants []Variant, samples []Sample, concurrency int, journal *Journal, done map[string]Record) []Record {
	type job struct {
		v Variant
		s Sample
	}
	jobs := make(chan job)
	records := make([]Record, 0, len(variants)*len(samples))
	for _, rec := range done {
		records = append(records, rec)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup

	if concurrency < 1 {
		concurrency = 1
	}
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if _, ok := done[j.v.Name+"|"+fmt.Sprint(j.s.ID)]; ok {
					continue
				}
				// A variant that needs trusted source text cannot run on a sample
				// that has none. Skipping is recorded rather than scored, so the
				// aggregation never mixes "could not run" with "ran badly".
				if j.v.RequiresSource && j.s.SourceText == "" {
					rec := Record{
						Variant: j.v.Name, Ablates: j.v.Ablates,
						Sample: j.s.ID, URL: j.s.URL,
						Skipped: "sample has no trusted source text",
					}
					mu.Lock()
					records = append(records, rec)
					mu.Unlock()
					journal.Append(rec)
					continue
				}
				out := j.v.Run(ctx, r, j.s)
				score := Judge(out, j.s)
				rec := Record{
					Variant: j.v.Name, Ablates: j.v.Ablates,
					Sample: j.s.ID, URL: j.s.URL,
					Outcome: out, Score: score,
				}
				mu.Lock()
				records = append(records, rec)
				mu.Unlock()
				journal.Append(rec)
				fmt.Fprintf(os.Stderr, "  %-26s sample %-4d %s\n", j.v.Name, j.s.ID, outcomeTag(out))
			}
		}()
	}
	for _, v := range variants {
		corpus := samples
		if v.RequiresSource {
			// A source-only variant needs trusted text. Every harvested sample
			// already carries the stored original text, so it can be used
			// directly; the frozen samples need the paired source corpus.
			corpus = withSourceText(samples)
		}
	dispatch:
		for _, s := range corpus {
			select {
			case <-ctx.Done():
				break dispatch
			case jobs <- job{v: v, s: s}:
			}
		}
	}
	close(jobs)
	wg.Wait()

	sort.Slice(records, func(i, j int) bool {
		if records[i].Variant != records[j].Variant {
			return records[i].Variant < records[j].Variant
		}
		return records[i].Sample < records[j].Sample
	})
	return records
}

// Summarize aggregates records per variant and computes deltas vs FULL.
func Summarize(records []Record) []VariantSummary {
	byVariant := map[string][]Record{}
	order := []string{}
	for _, rec := range records {
		if _, ok := byVariant[rec.Variant]; !ok {
			order = append(order, rec.Variant)
		}
		byVariant[rec.Variant] = append(byVariant[rec.Variant], rec)
	}
	summaries := make([]VariantSummary, 0, len(order))
	for _, name := range order {
		recs := byVariant[name]
		runnable := make([]Record, 0, len(recs))
		for _, rec := range recs {
			// Skipped and infrastructure-failed runs carry no quality signal,
			// so they are reported separately instead of dragging a variant's
			// mean down for reasons unrelated to the ablated decision.
			if rec.Skipped == "" && !rec.Score.InfraFailed {
				runnable = append(runnable, rec)
			}
		}
		infra := 0
		skipped := 0
		for _, rec := range recs {
			if rec.Skipped != "" {
				skipped++
			} else if rec.Score.InfraFailed {
				infra++
			}
		}
		s := VariantSummary{
			Variant: name, Ablates: recs[0].Ablates, Runs: len(runnable),
			Skipped: skipped, InfraFailed: infra,
		}
		if len(runnable) == 0 {
			summaries = append(summaries, s)
			continue
		}
		var sumQuality, sumFidelity, sumYield, sumTokens, sumLatency float64
		for _, rec := range runnable {
			if rec.Score.Accepted {
				s.Accepted++
			}
			if rec.Score.SearchEvidence {
				s.SearchEvidence++
			}
			if rec.Score.FabricationRisk {
				s.FabricationRisk++
			}
			if rec.Score.Accepted && !rec.Score.LanguageCorrect {
				s.LanguageErrors++
			}
			if rec.Score.Accepted && !rec.Score.HasTranslation {
				s.TranslationMiss++
			}
			if rec.Score.Accepted && rec.Score.TopicsSelected == 0 && rec.Variant != "no_classification" {
				s.ClassificationMiss++
			}
			if rec.Outcome.ValidationErr != "" || (rec.Outcome.Err != "" && !rec.Score.InfraFailed) {
				s.Failures++
			}
			sumQuality += rec.Score.QualityScore
			sumFidelity += rec.Score.OriginalFidelity
			sumYield += rec.Score.FieldYield
			sumTokens += float64(rec.Score.Tokens)
			sumLatency += float64(rec.Score.LatencyMs)
		}
		n := float64(len(runnable))
		s.AcceptRate = float64(s.Accepted) / n
		s.MeanQuality = sumQuality / n
		s.MeanFidelity = sumFidelity / n
		s.MeanFieldYield = sumYield / n
		s.MeanTokens = sumTokens / n
		s.MeanLatencyMs = sumLatency / n
		summaries = append(summaries, s)
	}

	// Deltas are relative to FULL, which is the production configuration.
	var full VariantSummary
	found := false
	for _, s := range summaries {
		if s.Variant == "FULL" {
			full, found = s, true
			break
		}
	}
	if found {
		for i := range summaries {
			summaries[i].QualityDelta = summaries[i].MeanQuality - full.MeanQuality
			summaries[i].TokenDelta = summaries[i].MeanTokens - full.MeanTokens
			summaries[i].LatencyDelta = summaries[i].MeanLatencyMs - full.MeanLatencyMs
		}
	}
	return summaries
}

// Report is the persisted experiment artifact.
type Report struct {
	StartedAt  time.Time        `json:"started_at"`
	Duration   time.Duration    `json:"duration_ns"`
	DurationMS int64            `json:"duration_ms"`
	Model      string           `json:"model"`
	Endpoint   string           `json:"endpoint"`
	Samples    int              `json:"samples"`
	Variants   int              `json:"variants"`
	ModelCalls int              `json:"model_calls"`
	Summary    []VariantSummary `json:"summary"`
	Records    []Record         `json:"records"`
}

// WriteJSON persists the full report including raw outcomes.
func WriteJSON(path string, report Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// Main is the CLI entry point for the experiment binary.
func Main(args []string) error {
	fs := flag.NewFlagSet("ablation", flag.ContinueOnError)
	envPath := fs.String("env", "../../.env", "path to dotenv file with endpoint credentials")
	outPath := fs.String("out", "../../experiments/results/ablation.json", "report output path")
	concurrency := fs.Int("concurrency", 3, "parallel model requests")
	model := fs.String("model", "", "model override")
	maxTok := fs.Int("max-tokens", 4096, "max output tokens")
	sampleFilter := fs.String("samples", "", "comma-separated sample IDs to run")
	variantFilter := fs.String("variants", "", "comma-separated variant names to run")
	timeout := fs.Duration("timeout", 15*time.Minute, "overall experiment timeout")
	journalPath := fs.String("journal", "", "JSONL journal path (default: alongside -out)")
	corpusPath := fs.String("corpus", "", "harvested corpus JSON to use instead of the frozen samples")
	corpusMax := fs.Int("max", 0, "cap the corpus size, spread evenly across text length (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	endpoint := EnvFileValue(*envPath, "GROK_MODELS_BASE_URL")
	apiKey := EnvFileValue(*envPath, "XAI_API_KEY")
	if endpoint == "" || apiKey == "" {
		return fmt.Errorf("missing GROK_MODELS_BASE_URL or XAI_API_KEY in %s", *envPath)
	}
	base := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	if *model == "" {
		*model = EnvFileValue(*envPath, "GROK_MODEL")
	}
	if *model == "" {
		return fmt.Errorf("no model configured")
	}

	samples, err := loadSamples(*corpusPath, *corpusMax, *sampleFilter)
	if err != nil {
		return err
	}
	variants := filterVariants(append(Variants(), SourceVariant()), *variantFilter)
	if len(samples) == 0 || len(variants) == 0 {
		return fmt.Errorf("no samples or variants selected")
	}

	runner := &Runner{
		Endpoint: base + "/responses",
		APIKey:   apiKey,
		Model:    *model,
		MaxTok:   *maxTok,
		Catalog:  defaultCatalog(),
		Client:   &httpClient,
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	started := time.Now()
	fmt.Fprintf(os.Stderr, "ablation: %d variants x %d samples = %d runs against %s\n",
		len(variants), len(samples), len(variants)*len(samples), base)

	jPath := *journalPath
	if jPath == "" {
		jPath = strings.TrimSuffix(*outPath, filepath.Ext(*outPath)) + ".jsonl"
	}
	// Resume support: a (variant, sample) pair that already has a non-infra
	// observation in the journal is carried forward instead of re-run. This is
	// what makes a long experiment resumable against a slow endpoint.
	done := map[string]Record{}
	if prior, err := LoadJournal(jPath); err == nil {
		for _, rec := range prior {
			if rec.Skipped == "" && !rec.Score.InfraFailed {
				done[rec.Variant+"|"+fmt.Sprint(rec.Sample)] = rec
			}
		}
	}
	if len(done) > 0 {
		fmt.Fprintf(os.Stderr, "ablation: resuming, %d observations already recorded\n", len(done))
	}
	journal, err := OpenJournal(jPath)
	if err != nil {
		return err
	}
	defer journal.Close()
	for _, rec := range done {
		journal.Append(rec)
	}
	fmt.Fprintf(os.Stderr, "ablation: journaling to %s\n", jPath)

	records := Run(ctx, runner, variants, samples, *concurrency, journal, done)
	report := Report{
		StartedAt: started, Duration: time.Since(started),
		DurationMS: time.Since(started).Milliseconds(),
		Model:      *model, Endpoint: base,
		Samples: len(samples), Variants: len(variants),
		ModelCalls: runner.Calls(),
		Summary:    Summarize(records),
		Records:    records,
	}
	if err := WriteJSON(*outPath, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "ablation: wrote %s (%d model calls, %s)\n",
		*outPath, report.ModelCalls, report.Duration.Round(time.Second))
	return nil
}

// loadSamples returns the evaluation corpus: either the frozen samples or a
// harvested real collection.
func loadSamples(corpusPath string, maxSamples int, csv string) ([]Sample, error) {
	if corpusPath == "" {
		return filterSamples(Samples(), csv), nil
	}
	//nolint:gosec // corpus path is an operator-supplied CLI argument
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		return nil, fmt.Errorf("read corpus %s: %w", corpusPath, err)
	}
	var samples []Sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return nil, fmt.Errorf("decode corpus %s: %w", corpusPath, err)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("corpus %s contains no samples", corpusPath)
	}
	// A harvested sample has no SourceText field of its own; the stored original
	// text is the trusted text, so it doubles as the source for the recovery
	// path. ReferenceText stays separate so groundedness can still be judged.
	for i := range samples {
		if samples[i].SourceText == "" {
			samples[i].SourceText = samples[i].ReferenceText
		}
	}
	if maxSamples > 0 {
		samples = spreadByLength(samples, maxSamples)
	}
	return filterSamples(samples, csv), nil
}

// spreadByLength picks at most max samples evenly across the text-length
// distribution, so a capped run still covers short and long bookmarks.
func spreadByLength(in []Sample, limit int) []Sample {
	sorted := append([]Sample(nil), in...)
	sort.Slice(sorted, func(i, j int) bool {
		return len([]rune(sorted[i].ReferenceText)) < len([]rune(sorted[j].ReferenceText))
	})
	if len(sorted) <= limit {
		return sorted
	}
	out := make([]Sample, 0, limit)
	for i := range limit {
		idx := i * (len(sorted) - 1) / limit
		out = append(out, sorted[idx])
	}
	return out
}

// withSourceText returns samples that carry trusted source text. For a harvest
// corpus that is already true; for the frozen samples the source corpus is used.
func withSourceText(samples []Sample) []Sample {
	out := make([]Sample, len(samples))
	copy(out, samples)
	for i := range out {
		if out[i].SourceText == "" {
			out[i].SourceText = out[i].ReferenceText
		}
	}
	return out
}

func filterSamples(in []Sample, csv string) []Sample {
	if strings.TrimSpace(csv) == "" {
		return in
	}
	want := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		want[strings.TrimSpace(part)] = true
	}
	var out []Sample
	for _, s := range in {
		if want[fmt.Sprint(s.ID)] {
			out = append(out, s)
		}
	}
	return out
}

func filterVariants(in []Variant, csv string) []Variant {
	if strings.TrimSpace(csv) == "" {
		return in
	}
	want := map[string]bool{}
	for _, part := range strings.Split(csv, ",") {
		want[strings.TrimSpace(part)] = true
	}
	var out []Variant
	for _, v := range in {
		if want[v.Name] {
			out = append(out, v)
		}
	}
	return out
}

// defaultCatalog mirrors the production vocabulary shape closely enough to
// exercise taxonomy normalization without needing the Worker.
func defaultCatalog() taxonomy.Catalog {
	return taxonomy.Catalog{
		Version: "ablation-v1",
		Topics: []taxonomy.Term{
			{ID: "llm", Label: "大模型", Aliases: []string{"AI", "人工智能", "语言模型"}, Active: true},
			{ID: "infra", Label: "基础设施", Aliases: []string{"infrastructure", "部署"}, Active: true},
			{ID: "tool", Label: "工具", Aliases: []string{"tool", "工具链"}, Active: true},
			{ID: "research", Label: "研究", Aliases: []string{"paper", "论文"}, Active: true},
			{ID: "product", Label: "产品", Aliases: []string{"product", "发布"}, Active: true},
		},
		Forms: []taxonomy.Term{
			{ID: "post", Label: "帖子", Active: true},
			{ID: "thread", Label: "长推文", Active: true},
			{ID: "media", Label: "图文", Active: true},
			{ID: "link", Label: "链接", Active: true},
		},
		Uses: []taxonomy.Term{
			{ID: "try", Label: "待试", Aliases: []string{"试用"}, Active: true},
			{ID: "read", Label: "待读", Active: true},
			{ID: "ref", Label: "参考", Aliases: []string{"资料"}, Active: true},
			{ID: "share", Label: "分享", Active: true},
		},
	}
}

// outcomeTag summarizes one observation for progress output.
func outcomeTag(out Outcome) string {
	switch {
	case out.InfraFailed:
		return "upstream-failed"
	case out.Err != "":
		return "error: " + truncate(out.Err, 40)
	case out.ValidationErr != "":
		return "rejected: " + truncate(out.ValidationErr, 40)
	default:
		return fmt.Sprintf("ok tok=%d %dms", out.TotalTokens, out.Latency.Milliseconds())
	}
}
