package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
)

type sourceEvidenceBackend struct {
	*fakeBackend
	calls int
	err   error
}

func (b *sourceEvidenceBackend) GetEntityEvidence(_ context.Context, _ int64, ref cairn.EntityEvidenceReference) (json.RawMessage, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return json.Marshal(map[string]any{"version": 1, "quote": "Acme", "evidence_snapshot_id": ref.SnapshotID})
}

func TestSourceEvidenceProxyIsReadOnlyAndOptional(t *testing.T) {
	query := "snapshot_id=12&content_revision=3&block_id=a&start=2&end=6"
	backend := &sourceEvidenceBackend{fakeBackend: &fakeBackend{}}
	server := New(context.Background(), nil, backend, nil, testLogger(), 1)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/7/entity-evidence?"+query, nil))
	if response.Code != 200 || response.Header().Get("X-Cairn-Entity-Evidence") != "1" || response.Header().Get("Cache-Control") != "no-store" || backend.calls != 1 {
		t.Fatalf("read contract not acknowledged: %d %v %d", response.Code, response.Header(), backend.calls)
	}
	for _, path := range []string{query + "&start=2", query + "&latest=1", query + "&bad=%xx"} {
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/7/entity-evidence?"+path, nil))
		if response.Code != 400 || backend.calls != 1 {
			t.Fatal("invalid query reached backend")
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), method, "/api/bookmarks/7/entity-evidence?"+query, nil))
		if response.Code != 405 || backend.calls != 1 {
			t.Fatal("non-GET reached source reader")
		}
	}
	old := New(context.Background(), nil, &fakeBackend{}, nil, testLogger(), 1)
	response = httptest.NewRecorder()
	old.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/7/entity-evidence?"+query, nil))
	if response.Code != 501 || response.Header().Get("X-Cairn-Entity-Evidence") != "" {
		t.Fatal("older backend pretended to supply source material")
	}
	for _, test := range []struct {
		err    error
		status int
		ack    string
	}{
		{cairn.ErrV2Unsupported, 501, ""},
		{cairn.ErrEntityEvidenceUnavailable, 404, "1"},
		{cairn.ErrEntityEvidenceChanged, 409, "1"},
	} {
		backend.err = test.err
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/bookmarks/7/entity-evidence?"+query, nil))
		if response.Code != test.status || response.Header().Get("X-Cairn-Entity-Evidence") != test.ack {
			t.Fatalf("lost capability state %v: %d %v", test.err, response.Code, response.Header())
		}
	}
}
