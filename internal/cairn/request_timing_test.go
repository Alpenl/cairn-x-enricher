package cairn

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestTimingCapturesClosedBodyWithoutRelayingDescriptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server-Timing", `total;dur=12.5;desc="private text", db;dur=3.25, db;dur=1.5, secret;dur=1`)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	ctx, timing := WithRequestTiming(context.Background())
	client := NewClient(server.URL, "fixture", server.Client())
	response, err := client.do(ctx, http.MethodGet, "/fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	_ = response.Body.Close()
	header := timing.Header(time.Millisecond)
	for _, expected := range []string{"nas;dur=1.00", "worker;dur=12.50", "d1;dur=4.75", `upstream_calls;desc="1"`} {
		if !strings.Contains(header, expected) {
			t.Fatalf("missing %q in %q", expected, header)
		}
	}
	if strings.Contains(header, "private") || strings.Contains(header, "secret") {
		t.Fatalf("untrusted metadata was relayed: %q", header)
	}
}

func TestRequestTimingIgnoresInvalidDurationsAndRemainsRequestLocal(t *testing.T) {
	_, timing := WithRequestTiming(context.Background())
	timing.record(time.Millisecond, "total;dur=NaN, db;dur=Inf, db;dur=-1, db;dur=100000000")
	if header := timing.Header(time.Millisecond); !strings.Contains(header, "d1;dur=0.00") || !strings.Contains(header, "worker;dur=0.00") {
		t.Fatalf("invalid durations survived: %q", header)
	}
	_, separate := WithRequestTiming(context.Background())
	if !strings.Contains(separate.Header(0), `upstream_calls;desc="0"`) {
		t.Fatal("request metrics leaked")
	}
}
