package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestGetV2EffectiveBatchContractAndOldWorker(t *testing.T) {
	var sent []int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v2/links/effective-batch" ||
			request.Header.Get("Authorization") != "Bearer internal" {
			t.Errorf("unexpected batch request: %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode batch request: %v", err)
		}
		sent = body.IDs
		_, _ = writer.Write([]byte(`{"version":1,"items":[{"id":8,"effective":{"topics":["llm"]}},` +
			`{"id":7,"effective":{"topics":["design"]}}],"missing_ids":[9],` +
			`"d1":{"scope":"effective_view_only","sql_count":1,"rows_read":12,"rows_written":0}}`))
	}))
	client := NewClient(server.URL, "internal", server.Client())
	views, err := client.GetV2EffectiveBatch(context.Background(), []int64{8, 9, 7})
	server.Close()
	if err != nil || !reflect.DeepEqual(sent, []int64{8, 9, 7}) || len(views) != 2 ||
		views[9] != nil || !json.Valid(views[7]) {
		t.Fatalf("effective batch = %v, %v; sent %v", views, err, sent)
	}
	for _, ids := range [][]int64{nil, {0}, {7, 7}, make([]int64, 51)} {
		if _, err := client.GetV2EffectiveBatch(context.Background(), ids); err == nil {
			t.Fatalf("accepted invalid batch %v", ids)
		}
	}
	old := httptest.NewServer(http.NotFoundHandler())
	_, err = NewClient(old.URL, "internal", old.Client()).GetV2EffectiveBatch(context.Background(), []int64{7})
	old.Close()
	if !errors.Is(err, ErrV2Unsupported) {
		t.Fatalf("old Worker = %v, want unsupported", err)
	}
}

func TestGetV2EffectiveBatchRejectsPartialOrForeignResults(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"items":[{"id":7}],"missing_ids":[],"d1":{"scope":"effective_view_only","sql_count":1,"rows_read":1,"rows_written":0}}`,
		`{"version":1,"items":[{"id":7},{"id":8}],"missing_ids":[8],"d1":{"scope":"effective_view_only","sql_count":1,"rows_read":1,"rows_written":0}}`,
		`{"version":1,"items":[{"id":7},{"id":99}],"missing_ids":[],"d1":{"scope":"effective_view_only","sql_count":1,"rows_read":1,"rows_written":0}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		_, err := NewClient(server.URL, "internal", server.Client()).GetV2EffectiveBatch(context.Background(), []int64{7, 8})
		server.Close()
		if err == nil {
			t.Fatalf("accepted invalid Worker result %s", body)
		}
	}
}
