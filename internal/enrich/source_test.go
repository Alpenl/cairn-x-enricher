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
			delete(value, "context_text")
			value["original_text"] = "model attempted to rewrite source"
			value["ai_title"] = "用于测试的原文阅读增强标题"
			value["translated_text"] = "完整中文译文"
			value["summary"] = "中文摘要"
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
	r, err := c.Transform(context.Background(), Input{SourceText: source.OriginalText})
	if err != nil {
		t.Fatal(err)
	}
	if r.OriginalText != source.OriginalText {
		t.Fatal("reading changed archived source")
	}
}

func TestSourceRequiresSearchEvidence(t *testing.T) {
	if _, err := decodeSource(responseEnvelope{Status: "completed"}); err == nil {
		t.Fatal("unverified source accepted")
	}
}
