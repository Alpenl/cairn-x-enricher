package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserSourceAndReadingHaveSeparateContracts(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["tools"] != nil || request["tool_choice"] != nil {
			t.Fatal("reading must never retrieve source")
		}
		props := request["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
		for _, key := range []string{"classification", "original_text", "related_links", "image_urls"} {
			if props[key] != nil {
				t.Fatalf("reading contract contains %s", key)
			}
		}
		value := `{"ai_title":"用于测试的原文阅读增强标题","original_language":"en","translated_text":"完整中文译文","summary":"中文摘要"}`
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "grok-test", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": value}}}}})
	}))
	defer server.Close()
	c := NewResponsesClient(server.URL, "key", "grok", 1000, "", server.Client(), testTaxonomy())
	source := Source{OriginalText: "immutable browser source", ContextText: "a comment", RelatedLinks: []string{"https://example.com/related"}}
	result, err := c.Transform(context.Background(), Input{SourceText: source.OriginalText, RelatedLinks: source.RelatedLinks})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.OriginalText != source.OriginalText || len(result.RelatedLinks) != 1 || len(result.ImageURLs) != 0 {
		t.Fatalf("reading changed source or media: %+v calls=%d", result, calls)
	}
}

func TestSourceRequiresSearchEvidence(t *testing.T) {
	if _, err := decodeSource(responseEnvelope{Status: "completed"}); err == nil {
		t.Fatal("unverified source accepted")
	}
}

func TestManualSourceKeepsExactInput(t *testing.T) {
	original := " \n人工原文\n "
	source, err := SourceFromText(original)
	if err != nil || source.OriginalText != original {
		t.Fatalf("manual source changed: %q, %v", source.OriginalText, err)
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
		"duplicate field":    valid[:len(valid)-1] + `,"summary":"另一个摘要"}`,
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

func TestReadingRejectsValidJSONHiddenInsideOversizedOutput(t *testing.T) {
	valid := `{"ai_title":"用于测试的阅读标题","original_language":"en","translated_text":"译文","summary":"摘要"}`
	envelope := responseEnvelope{Status: "completed", Model: "grok-test", Output: []responseOutputItem{
		{Type: "message", Content: []responseOutputContent{{Type: "output_text",
			Text: valid + strings.Repeat(" ", maxModelOutputBytes)}}},
	}}
	if _, err := decodeReading(envelope, "grok"); err == nil {
		t.Fatal("reading accepted a valid JSON prefix followed by hidden oversized output")
	}
}

func TestTransformPreservesLongSourceExactly(t *testing.T) {
	prefix, suffix := " \n", "\n末尾 "
	source := prefix + strings.Repeat("x", maxOriginalTextLength-len(prefix)-len(suffix)) + suffix
	if len(source) != maxOriginalTextLength {
		t.Fatalf("fixture source length = %d", len(source))
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var payload struct {
			Input []struct {
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Input) != 1 {
			t.Errorf("decode reading request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		start := strings.LastIndex(payload.Input[0].Content, "\n{")
		if start < 0 {
			t.Error("reading request omitted source state")
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		var state map[string]string
		if err := json.Unmarshal([]byte(payload.Input[0].Content[start+1:]), &state); err != nil ||
			state["original_text"] != source {
			t.Errorf("reading request did not include the exact long source: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		reading := `{"ai_title":"用于验证长文保留的中文标题","original_language":"fr","translated_text":"长文完整译文","summary":"长文摘要"}`
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": "completed", "model": "grok-test",
			"output": []any{map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": reading}}}}})
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "fixture", "grok-test", 1024, "", server.Client(), testTaxonomy())
	result, err := client.Transform(context.Background(), Input{SourceText: source})
	if err != nil || result.OriginalText != source || calls != 1 {
		t.Fatalf("long source changed or rejected: bytes=%d calls=%d error=%v", len(result.OriginalText), calls, err)
	}
	if _, err := client.Transform(context.Background(), Input{SourceText: source + "x"}); err == nil || calls != 1 {
		t.Fatalf("oversized source reached provider: calls=%d error=%v", calls, err)
	}
}

func TestChineseReadingPreservesArchivedMarkdown(t *testing.T) {
	source := "## 完整标题\n\n第一段。\n\n![示意图](cairn-image:0)\n\n- 原有列表\n\n[出处](https://example.com/article)"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, _ := json.Marshal(map[string]string{
			"ai_title":          "保留完整图文排版的中文文章",
			"original_language": "zh-CN", "translated_text": "模型错误改写的扁平内容", "summary": "测试中文摘要",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "fixture",
			"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": string(payload)}}}},
		})
	}))
	defer server.Close()
	client := NewResponsesClient(server.URL, "key", "fixture", 1000, "", server.Client(), testTaxonomy())
	result, err := client.Transform(context.Background(), Input{ID: 1, URL: "https://x.com/a/status/1", SourceText: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.OriginalText != source || result.TranslatedText != source {
		t.Fatal("Chinese archived structure was rewritten")
	}
}
