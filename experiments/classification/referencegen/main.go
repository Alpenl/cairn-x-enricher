// Command referencegen freezes an authored synthetic benchmark before inference.
// It never calls a model, reads private bookmarks, or labels model predictions.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Alpenl/cairn-x-enricher/internal/classify"
	"github.com/Alpenl/cairn-x-enricher/internal/evaluation"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

type scenario struct {
	ID          string   `json:"id"`
	ZH          string   `json:"zh"`
	EN          string   `json:"en"`
	Topics      []string `json:"topics"`
	Form        *string  `json:"form"`
	Functions   []string `json:"functions"`
	Affordances []string `json:"affordances"`
	Kind        string   `json:"kind"`
}

func main() {
	root := flag.String("source", "experiments/classification/reference-v1", "fixed scenario/taxonomy directory")
	output := flag.String("output", "", "new output directory; never overwrite a frozen dataset")
	flag.Parse()
	if err := generate(*root, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func label(values []string) evaluation.Label {
	if values == nil {
		return evaluation.Label{Unknown: true}
	}
	if len(values) == 0 {
		return evaluation.Label{NotApplicable: true}
	}
	return evaluation.Label{Values: values}
}
func generate(root, output string) error {
	if output == "" {
		return fmt.Errorf("-output is required")
	}
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(root, "scenarios.json")))
	if err != nil {
		return err
	}
	var scenarios []scenario
	if err := json.Unmarshal(raw, &scenarios); err != nil {
		return err
	}
	catalogBytes, err := os.ReadFile(filepath.Clean(filepath.Join(root, "taxonomy.json")))
	if err != nil {
		return err
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		return err
	}
	if err := catalog.Validate(); err != nil {
		return err
	}
	// This command regenerates the already frozen v1 reference corpus. Its
	// original inference identity must not drift when production questions change.
	specBytes, err := os.ReadFile(filepath.Clean(filepath.Join(root, "baseline-spec.json")))
	if err != nil {
		return err
	}
	spec, err := classify.DecodeSpec(specBytes)
	if err != nil {
		return err
	}
	if spec.SpecID != "classify-80156c157660" || spec.SemanticHash != "2a39cb299aa0bf916bb4ac9642c1e515dd07f35883a61da403b4c53c05ce4d26" || spec.TaxonomyVersion != catalog.Version {
		return fmt.Errorf("frozen v1 baseline spec identity changed")
	}
	const seed int64 = 20260922
	samples := []evaluation.Sample{}
	for _, item := range scenarios {
		if item.ID == "" || item.EN == "" || item.ZH == "" {
			return fmt.Errorf("invalid scenario")
		}
		for variant := range 6 {
			language, carrier, primary := "en", "single", "Single post. "+item.EN
			var context []classify.EvidenceBlock
			switch variant {
			case 1:
				language, primary = "zh", "单条帖子。"+item.ZH
			case 2:
				language, primary = "mixed", "单条双语帖子。"+item.ZH+" English version: "+item.EN
			case 3:
				language, carrier, primary = "zh", "external_article", item.ZH+" 这条摘要附有外部文章链接 https://example.invalid/article/"+item.ID
				context = []classify.EvidenceBlock{{ID: "article", Role: classify.RoleExternalArticle, Text: item.EN, URL: "https://example.invalid/article/" + item.ID, Acquired: "synthetic_fixture"}}
			case 4:
				carrier, primary = "author_continuation", "Author thread, post 1/2. "+item.EN
				context = []classify.EvidenceBlock{{ID: "author-post-2", Role: classify.RoleAuthorContinuation, Text: "同一作者的第二条续帖，用中文重复前文：" + item.ZH, Acquired: "synthetic_fixture"}}
				language = "mixed"
			case 5:
				primary = "One long post; the following passage is repeated as a length stress fixture. " + strings.Repeat(item.EN+"\n\n", 18)
				context = []classify.EvidenceBlock{{ID: "unrelated-quote", Role: classify.RoleQuoted, Text: "Unrelated quotation: Software development uses source code. This quoted subject does not replace the primary post.", Acquired: "synthetic_fixture"}}
			}
			completeness, coverage := "complete", "complete"
			if item.Kind == "image_dependent" {
				completeness, coverage = "missing_image", "partial"
			}
			material := classify.Evidence{Primary: primary, Context: context, Coverage: coverage, TotalRunes: utf8.RuneCountInString(primary)}
			for _, block := range context {
				material.TotalRunes += utf8.RuneCountInString(block.Text)
			}
			hash, err := evaluation.HashMaterial(material)
			if err != nil {
				return err
			}
			length := "short"
			if material.TotalRunes > 1000 {
				length = "long"
			}
			form, use := evaluation.Label{Unknown: true}, evaluation.Label{Unknown: true}
			if item.Form != nil {
				form = label([]string{*item.Form})
				if *item.Form == "" {
					form = label([]string{})
				}
			}
			switch item.Kind {
			case "method":
				use = label([]string{"try"})
			case "opinion":
				use = label([]string{"quote", "background"})
			case "outside":
				use = label([]string{})
			}
			samples = append(samples, evaluation.Sample{SampleID: fmt.Sprintf("auto-%s-%d", item.ID, variant), SourceHash: hash, Revision: 1,
				Language: language, Carrier: carrier, LengthBucket: length, Completeness: completeness, GroupID: item.ID, Material: &material,
				Gold:       &evaluation.Gold{Topics: label(item.Topics), ContentFunctions: label(item.Functions), Carriers: label([]string{carrier}), Affordances: label(item.Affordances), Form: form, Use: use},
				Provenance: evaluation.ProvenanceAutomaticReference, Ambiguous: item.Kind == "boundary" || item.Kind == "quotation" || item.Kind == "image_dependent",
				Reference: &evaluation.ReferenceMetadata{Method: "authored_synthetic_scenario", Version: "cairn-auto-reference-v1", Source: "scenarios.json#" + item.ID,
					Basis: "Labels and acceptable choices are fixed by the scenario author before model evaluation. Unspecified functions/affordances and missing-image content remain unknown. No private user intent is inferred."}})
		}
	}
	splits, err := evaluation.SplitByGroup(samples, seed, 0.2, 0.2)
	if err != nil {
		return err
	}
	datasets := []evaluation.Dataset{}
	for _, name := range []string{"train", "dev", "test"} {
		splitName := name
		if name == "test" {
			splitName = "holdout"
		}
		datasets = append(datasets, evaluation.Dataset{Name: "cairn-auto-reference-v1", Split: splitName, Seed: seed, Samples: splits[name]})
	}
	if err := evaluation.ValidateSplits(datasets); err != nil {
		return err
	}
	hashes := map[string]string{}
	counts := map[string]int{}
	for _, dataset := range datasets {
		hash, err := evaluation.HashReference(dataset)
		if err != nil {
			return err
		}
		hashes[dataset.Split] = hash
		counts[dataset.Split] = len(dataset.Samples)
	}
	// Validate every authored label against the exact executable vocabulary.
	terms := map[string][]taxonomy.Term{"topics": catalog.Topics, "content_functions": catalog.ContentFunctions, "carriers": catalog.Carriers, "affordances": catalog.Affordances, "form": catalog.Forms, "use": catalog.Uses}
	for _, sample := range samples {
		gold := sample.Gold
		labels := map[string]evaluation.Label{"topics": gold.Topics, "content_functions": gold.ContentFunctions, "carriers": gold.Carriers, "affordances": gold.Affordances, "form": gold.Form, "use": gold.Use}
		for dimension, label := range labels {
			for _, value := range label.Values {
				valid := false
				for _, term := range terms[dimension] {
					if term.ID == value && term.Active {
						valid = true
						break
					}
				}
				if !valid {
					return fmt.Errorf("invalid %s reference %s", dimension, value)
				}
			}
		}
	}
	manifest := map[string]any{"version": "cairn-auto-reference-v1", "created_at": "2026-09-22", "seed": seed, "samples": len(samples), "scenario_groups": len(scenarios), "splits": counts, "reference_hashes": hashes,
		"spec_id": spec.SpecID, "spec_hash": spec.SemanticHash, "scenario_sha256": hashBytes(raw), "taxonomy_sha256": hashBytes(catalogBytes),
		"reference_basis": "automatic_reference / authored synthetic; not human gold or measured user preference", "gate": evaluation.DefaultGate(),
		"share_baseline_sha": "136faae21a8bddf101fb67977e2c4fabb2b98569", "enricher_baseline_sha": "b3b762fed6e8f1099a24b9ff0193770619c1b917",
		"independent_semantic_reviewer": "not used; owner waived human annotation", "limitations": []string{"40 authored families; variants and translations are not independent samples", "Repeated paragraphs test input length, not natural long-document diversity", "Reference bias is not captured by statistical intervals", "No private or real-user retrieval preference labels", "Legacy v1 historical outputs missing; any new v1 inference is a new baseline"}}
	files := map[string]any{"manifest.json": manifest}
	for _, dataset := range datasets {
		files[dataset.Split+".json"] = dataset
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("create new freeze directory: %w", err)
	}
	for _, name := range names {
		encoded, err := json.MarshalIndent(files[name], "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(output, name), append(encoded, '\n'), 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("frozen samples=%d groups=%d splits=%v; no predictions or model calls\n", len(samples), len(scenarios), counts)
	return nil
}
func hashBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
