package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
	"github.com/joho/godotenv"
)

// Explicitly opt in: sends one synthetic post, never private bookmarks.
func TestLiveJev(t *testing.T) {
	if os.Getenv("CAIRN_TEST_LIVE_TYPESAFE") != "1" {
		t.Skip("set CAIRN_TEST_LIVE_TYPESAFE=1 for one paid synthetic API check")
	}
	values, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local .env")
	}
	key := values["TYPESAFE_API_KEY"]
	if key == "" {
		t.Fatal("TYPESAFE_API_KEY missing")
	}
	catalogBytes, err := os.ReadFile("../../../cairn-share/worker/src/taxonomy.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog taxonomy.Catalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient("https://api.typesafe.ai", key, "jev-latest", &http.Client{Timeout: 60 * time.Second}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	r, err := c.Classify(context.Background(), Input{URL: "https://example.com/synthetic", OriginalText: "A practical method for evaluating large language models: create a fixed test set, measure factual accuracy, compare model outputs blindly, and report failure categories.", Note: "Try this evaluation method."})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%s elapsed=%s topics=%v form=%s use=%s uncertain=%v usage=%s", r.Model, time.Since(started).Round(time.Millisecond), r.Classification.Topics, r.Classification.Form, r.Classification.Use, r.Classification.Uncertainty, r.Usage)
	for id, answer := range r.Answers {
		if answer.Noul != nil {
			t.Logf("%s p=%.4f", id, *answer.Noul)
		}
	}
}
