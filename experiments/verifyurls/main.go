// Command verifyurls resolves each sample URL through the endpoint and reports
// the author handle and post id the tool actually returns.
//
// This exists because a mislabelled URL silently invalidates an experiment: if
// the sample claims an ID belongs to author A but the ID belongs to author B,
// a perfectly correct model response looks like a correctness bug. Every sample
// URL must be resolved and confirmed before its scores are trusted.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Alpenl/cairn-x-enricher/experiments/ablation"
)

func main() {
	envPath := flag.String("env", ".env", "dotenv with credentials")
	model := flag.String("model", "", "model override")
	timeout := flag.Duration("timeout", 10*time.Minute, "per-request timeout")
	flag.Parse()

	endpoint := ablation.EnvFileValue(*envPath, "GROK_MODELS_BASE_URL")
	apiKey := ablation.EnvFileValue(*envPath, "XAI_API_KEY")
	if *model == "" {
		*model = ablation.EnvFileValue(*envPath, "GROK_MODEL")
	}
	if endpoint == "" || apiKey == "" {
		fmt.Fprintln(os.Stderr, "missing credentials")
		os.Exit(1)
	}
	base := strings.TrimRight(endpoint, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}

	client := &ablation.Runner{
		Endpoint: base + "/responses", APIKey: apiKey, Model: *model, MaxTok: 800,
		Catalog: ablation.ExportDefaultCatalog(),
	}

	ok := true
	for _, s := range ablation.Samples() {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		author, link, err := ablation.ExportResolveAuthor(ctx, client, s.URL)
		cancel()

		claimed := handleOf(s.URL)
		status := "OK"
		switch {
		case err != nil:
			status = "ERROR"
			ok = false
		case author == "":
			status = "UNRESOLVED"
			ok = false
		case !strings.EqualFold(author, claimed):
			status = "**MISMATCH**"
			ok = false
		}
		fmt.Printf("sample %d: claimed=@%s resolved=@%s link=%s [%s]\n",
			s.ID, claimed, author, link, status)
		if err != nil {
			fmt.Printf("    error: %v\n", err)
		}
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "verification FAILED: fix the corpus before scoring")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "all sample URLs verified")
}

func handleOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

var _ = json.Marshal
