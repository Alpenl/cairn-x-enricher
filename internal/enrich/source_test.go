package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceAndReadingHaveSeparateContracts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		format := request["text"].(map[string]any)["format"].(map[string]any)
		props := format["schema"].(map[string]any)["properties"].(map[string]any)
		if _, ok := props["classification"]; ok {
			t.Error("taxonomy leaked into source/reading request")
		}
		value := map[string]any{"original_text": "immutable source", "original_language": "en", "context_text": "a comment", "related_links": []string{}, "image_urls": []string{}}
		output := []any{}
		if calls == 1 {
			if request["tool_choice"] != "required" || props["summary"] != nil {
				t.Error("source should only retrieve")
			}
			output = append(output, map[string]any{"type": "x_search_call", "status": "completed"})
		} else {
			if request["tools"] != nil {
				t.Error("reading must not search")
			}
			// The independent ReadingResult contract contains only reading
			// fields: the model must not echo source, links, images or tags.
			value = map[string]any{
				"ai_title":          "用于测试的原文阅读增强标题",
				"original_language": "en",
				"translated_text":   "完整中文译文",
				"summary":           "中文摘要",
			}
			if _, ok := props["original_text"]; ok {
				t.Error("reading schema must not require the model to echo original_text")
			}
			if _, ok := props["related_links"]; ok {
				t.Error("reading schema must not require the model to echo related_links")
			}
			if _, ok := props["image_urls"]; ok {
				t.Error("reading schema must not require the model to echo image_urls")
			}
		}
		payload, _ := json.Marshal(value)
		output = append(output, map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(payload)}}})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "grok-test", "output": output})
	}))
	defer server.Close()
	c := NewResponsesClient(server.URL, "key", "grok", 1000, "", server.Client(), testTaxonomy())
	source, err := c.FetchSource(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if source.ContextText != "a comment" || source.OriginalText != "immutable source" {
		t.Fatalf("source/context merged: %+v", source)
	}
	r, err := c.Transform(context.Background(), Input{SourceText: source.OriginalText, RelatedLinks: []string{"https://example.com/related"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.OriginalText != source.OriginalText {
		t.Fatal("reading changed archived source")
	}
	if len(r.RelatedLinks) != 1 || r.RelatedLinks[0] != "https://example.com/related" {
		t.Fatalf("reading must inject source links, got %v", r.RelatedLinks)
	}
	if len(r.ImageURLs) != 0 {
		t.Fatalf("reading must not fabricate images, got %v", r.ImageURLs)
	}
}

func TestSourceRequiresSearchEvidence(t *testing.T) {
	if _, err := decodeSource(responseEnvelope{Status: "completed"}); err == nil {
		t.Fatal("unverified source accepted")
	}
}

// TestReadingContractIsIndependentAndStrict pins the B02-T03/T04 contract: the
// reading pass decodes only reading fields, rejects source echo, and never
// falls back to defaults on malformed or trailing output.
func TestReadingContractIsIndependentAndStrict(t *testing.T) {
	readingEnvelope := func(text string) responseEnvelope {
		return responseEnvelope{Status: "completed", Model: "grok-test", Output: []responseOutputItem{
			{Type: "message", Content: []responseOutputContent{{Type: "output_text", Text: text}}},
		}}
	}
	valid := `{"ai_title":"用于测试的阅读标题","original_language":"en","translated_text":"译文","summary":"摘要"}`

	got, err := decodeReading(readingEnvelope(valid), "groK")
	if err != nil {
		t.Fatalf("valid reading rejected: %v", err)
	}
	if got.AITitle == "" || got.Model != "grok-test" {
		t.Fatalf("reading = %+v", got)
	}

	for name, payload := range map[string]string{
		"missing summary":    `{"ai_title":"用于测试的阅读标题","original_language":"en","translated_text":"译文"}`,
		"trailing content":   valid + `{"extra":true}`,
		"non-json":           `not json`,
		"unknown source key": `{"ai_title":"用于测试的阅读标题","original_language":"en","translated_text":"译文","summary":"摘要","original_text":"模型重写的原文"}`,
		"unknown tag key":    `{"ai_title":"用于测试的阅读标题","original_language":"en","translated_text":"译文","summary":"摘要","classification":{"topics":[]}}`,
	} {
		if _, err := decodeReading(readingEnvelope(payload), "grok"); err == nil {
			t.Errorf("%s: reading accepted malformed payload", name)
		}
	}

	// A model that ignores the contract and echoes source must not be trusted to
	// change the archived original.
	if _, err := validateReading(Input{SourceText: "immutable"}, Result{
		AITitle: "用于测试的阅读标题", OriginalLanguage: "en", OriginalText: "rewritten",
		TranslatedText: "译文", Summary: "摘要", Model: "grok",
	}); err == nil {
		t.Fatal("reading accepted a mutated original text")
	}
}
