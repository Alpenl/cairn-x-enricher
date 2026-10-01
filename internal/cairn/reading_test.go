package cairn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const modernReadingFixture = `{"version":1,"detail":{"id":7,"url":"https://x.com/a/status/7","status":"completed",` +
	`"original_text":"private text","classification":{"topics":["ai_coding"],"resource_kinds":["software"],"content_functions":["tool"]},` +
	`"custom_tags":[{"id":"personal","tag_ref":"custom/default/personal","label":"我的项目","revision":2,"status":"active"}],` +
	`"cache_identity":{"schema_version":1,"content_revision":2,"body_revision":1,` +
	`"personal_revision":3,"latest_decision_id":4,"latest_entity_revision":1}},` +
	`"selection":{"id":7,"revision":3,"available":true,"selection":{"topics":["ai_coding"],` +
	`"resource_kinds":["software"],"content_functions":["tool"],"carriers":[],"affordances":[],"form":"","use":""},"state":{"version":1}},` +
	`"entities":{"id":7,"revision":3,"state":"completed_empty","entities":[]}}`

func serveReadingFixture(t *testing.T, payload string, status int, headers map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/enrichment/jobs/7/reading" {
			t.Errorf("unexpected snapshot request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Cairn-Tag-System") != "1" || r.Header.Get("X-Cairn-Content-Functions") != "1" {
			t.Error("reading request must negotiate both effective tag dimensions")
		}
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReadingSnapshotRejectsCrossRevisionState(t *testing.T) {
	headers := map[string]string{"X-Cairn-Tag-System": "1", "X-Cairn-Content-Functions": "1"}
	server := serveReadingFixture(t, modernReadingFixture, http.StatusOK, headers)
	reading, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
	if err != nil || reading.Detail.OriginalText != "private text" ||
		!reading.Selection.Available || reading.Selection.Revision != 3 {
		t.Fatalf("valid reading = %+v, %v", reading, err)
	}
	if len(reading.Detail.Classification.ResourceKinds) != 1 || len(reading.Detail.Classification.ContentFunctions) != 1 ||
		len(reading.Detail.CustomTags) != 1 || reading.Detail.CustomTags[0].Label != "我的项目" {
		t.Fatalf("reading must preserve all effective tags: %+v", reading.Detail.Bookmark)
	}
	for _, payload := range []string{
		strings.Replace(modernReadingFixture, `"revision":3,"available"`, `"revision":2,"available"`, 1),
		strings.Replace(modernReadingFixture, `"revision":3,"state":"completed_empty"`, `"revision":2,"state":"completed_empty"`, 1),
	} {
		server := serveReadingFixture(t, payload, http.StatusOK, headers)
		_, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
		if err == nil {
			t.Fatal("cross-revision reading must be rejected")
		}
	}
}

func TestReadingSnapshotRejectsIncompleteModernProjection(t *testing.T) {
	headers := map[string]string{"X-Cairn-Tag-System": "1", "X-Cairn-Content-Functions": "1"}
	for name, payload := range map[string]string{
		"old selection":          strings.ReplaceAll(modernReadingFixture, `"resource_kinds":["software"],`, ""),
		"old detail":             strings.Replace(modernReadingFixture, `"resource_kinds":["software"],"content_functions":["tool"]`, `"form":"tool","use":"try"`, 1),
		"missing classification": strings.Replace(modernReadingFixture, `"classification":{"topics":["ai_coding"],"resource_kinds":["software"],"content_functions":["tool"]}`, `"classification":null`, 1),
		"different topics":       strings.Replace(modernReadingFixture, `"topics":["ai_coding"]`, `"topics":["agent_workflow"]`, 1),
		"different resources":    strings.Replace(modernReadingFixture, `"resource_kinds":["software"]`, `"resource_kinds":["reference"]`, 1),
		"different functions":    strings.Replace(modernReadingFixture, `"content_functions":["tool"]`, `"content_functions":["method"]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			server := serveReadingFixture(t, payload, http.StatusOK, headers)
			_, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
			if err == nil || errors.Is(err, ErrV2Unsupported) {
				t.Fatalf("acknowledged but incomplete modern snapshot must be rejected: %v", err)
			}
		})
	}
}

func TestReadingSnapshotAllowsUnclassifiedAndUnchangedBody(t *testing.T) {
	headers := map[string]string{"X-Cairn-Tag-System": "1", "X-Cairn-Content-Functions": "1"}
	bodyRevision := int64(1)
	for name, payload := range map[string]string{
		"unclassified": strings.NewReplacer(
			`"classification":{"topics":["ai_coding"],"resource_kinds":["software"],"content_functions":["tool"]}`, `"classification":null`,
			`["ai_coding"]`, `[]`, `["software"]`, `[]`, `["tool"]`, `[]`,
		).Replace(modernReadingFixture),
		"unchanged body": strings.NewReplacer(`"version":1`, `"version":1,"body_unchanged":true`,
			`"original_text":"private text"`, `"original_text":null`).Replace(modernReadingFixture),
	} {
		t.Run(name, func(t *testing.T) {
			server := serveReadingFixture(t, payload, http.StatusOK, headers)
			_, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, &bodyRevision)
			if err != nil {
				t.Fatalf("valid snapshot must be accepted: %v", err)
			}
		})
	}
}

func TestReadingSnapshotRecognizesUnsupportedWorker(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		headers map[string]string
	}{
		"route absent":      {status: http.StatusNotFound},
		"legacy projection": {status: http.StatusOK},
		"tag system only":   {status: http.StatusOK, headers: map[string]string{"X-Cairn-Tag-System": "1"}},
		"functions only":    {status: http.StatusOK, headers: map[string]string{"X-Cairn-Content-Functions": "1"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := serveReadingFixture(t, modernReadingFixture, tc.status, tc.headers)
			_, err := NewClient(server.URL, "token", server.Client()).GetReading(context.Background(), 7, nil)
			if !errors.Is(err, ErrV2Unsupported) {
				t.Fatalf("old Worker must permit ordinary-detail fallback: %v", err)
			}
		})
	}
}
