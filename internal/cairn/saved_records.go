package cairn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// SavedRecordQuery selects exactly one retained log or source; it never reads
// current projections or enables arbitrary internal endpoints.
type SavedRecordQuery struct {
	Kind           string
	SourceRevision int64
	RecordKey      string
	AfterKey       string
}

func savedPositive(value string) bool {
	n, err := strconv.ParseInt(value, 10, 64)
	return err == nil && n > 0 && n <= 1<<53-1 && strconv.FormatInt(n, 10) == value
}

// Valid checks the fixed store, bounded key and required immutable source scope.
func (q SavedRecordQuery) Valid() bool {
	if q.RecordKey != "" && q.AfterKey != "" {
		return false
	}
	if q.Kind != "sources" && q.Kind != "entities" && q.Kind != "overrides" && q.Kind != "events" {
		return false
	}
	if q.Kind == "entities" {
		if q.SourceRevision < 1 || q.SourceRevision > 1<<53-1 {
			return false
		}
	} else if q.SourceRevision != 0 {
		return false
	}
	for _, key := range []string{q.RecordKey, q.AfterKey} {
		if key == "" {
			continue
		}
		if q.Kind == "entities" {
			if !utf8.ValidString(key) || strings.TrimSpace(key) == "" || len(utf16.Encode([]rune(key))) > 200 {
				return false
			}
		} else if !savedPositive(key) {
			return false
		}
	}
	return true
}

// GetSavedRecords reads a bounded page or one selected retained record, without
// substituting current metadata or falling back to internal management APIs.
func (c *Client) GetSavedRecords(ctx context.Context, id int64, q SavedRecordQuery) (json.RawMessage, error) {
	if id < 1 || id > 1<<53-1 || !q.Valid() {
		return nil, errors.New("invalid saved record reference")
	}
	query := url.Values{}
	if q.SourceRevision > 0 {
		query.Set("source_revision", strconv.FormatInt(q.SourceRevision, 10))
	}
	if q.RecordKey != "" {
		query.Set("record_key", q.RecordKey)
	}
	if q.AfterKey != "" {
		query.Set("after_key", q.AfterKey)
	}
	path := fmt.Sprintf("/api/v2/links/%d/saved-records/%s", id, q.Kind)
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	const capability = "X-Cairn-App-Saved-Records"
	response, err := c.doHistoryRead(ctx, path, capability)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	supported := response.Header.Get(capability) == "1"
	switch {
	case response.StatusCode == 405 || response.StatusCode == 501 || response.StatusCode == 404 && !supported:
		return nil, ErrV2Unsupported
	case supported && (response.StatusCode == 404 || response.StatusCode == 410):
		return nil, ErrRunHistoryUnavailable
	case response.StatusCode != http.StatusOK:
		return nil, apiError(response)
	case !supported:
		return nil, ErrV2Unsupported
	}
	limit := 128 << 10
	if q.RecordKey != "" {
		limit = 2 << 20
		if q.Kind == "sources" {
			limit = 10 << 20
		}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	invalid := errors.New("invalid saved record response")
	if len(payload) > limit || !utf8.Valid(payload) {
		return nil, invalid
	}
	var value struct {
		Version        int64                        `json:"version"`
		BookmarkID     int64                        `json:"bookmark_id"`
		Kind           string                       `json:"kind"`
		SourceRevision json.RawMessage              `json:"source_revision"`
		Records        []map[string]json.RawMessage `json:"records"`
		Record         map[string]json.RawMessage   `json:"record"`
		Next           json.RawMessage              `json:"next_after"`
	}
	if json.Unmarshal(payload, &value) != nil {
		return nil, invalid
	}
	if value.Version != 1 {
		return nil, ErrV2Unsupported
	}
	if value.BookmarkID != id || value.Kind != q.Kind || len(value.SourceRevision) == 0 {
		return nil, invalid
	}
	if q.SourceRevision == 0 {
		if string(value.SourceRevision) != "null" {
			return nil, invalid
		}
	} else if string(value.SourceRevision) != strconv.FormatInt(q.SourceRevision, 10) {
		return nil, invalid
	}
	getKey := func(row map[string]json.RawMessage) string {
		var key string
		if json.Unmarshal(row["key"], &key) != nil {
			return ""
		}
		probe := q
		probe.RecordKey, probe.AfterKey = key, ""
		if key == "" || !probe.Valid() {
			return ""
		}
		return key
	}
	if q.RecordKey != "" {
		if getKey(value.Record) != q.RecordKey {
			return nil, invalid
		}
		if _, present := value.Record["payload"]; q.Kind != "overrides" && !present {
			return nil, invalid
		}
	} else {
		if value.Records == nil || len(value.Records) > 50 || len(value.Next) == 0 {
			return nil, invalid
		}
		previous := q.AfterKey
		for _, row := range value.Records {
			key := getKey(row)
			if key == "" {
				return nil, invalid
			}
			if previous != "" {
				before := key < previous // Go string order matches SQLite UTF-8 BINARY.
				if q.Kind != "entities" {
					a, _ := strconv.ParseInt(key, 10, 64)
					b, _ := strconv.ParseInt(previous, 10, 64)
					before = a < b
				}
				if !before {
					return nil, invalid
				}
			}
			for _, field := range []string{"payload", "term", "operation_key", "request_hash", "payload_hash"} {
				if _, present := row[field]; present {
					return nil, invalid
				}
			}
			previous = key
		}
		if string(value.Next) != "null" {
			var next string
			if json.Unmarshal(value.Next, &next) != nil || len(value.Records) == 0 || next != previous {
				return nil, invalid
			}
		}
	}
	return json.RawMessage(payload), nil
}

// Keep the shared transport/connection pool and request timing, but refuse
// redirects for exact history contracts without mutating a concurrent client.
func (c *Client) doHistoryRead(ctx context.Context, path, capability string) (*http.Response, error) {
	transport := *c.httpClient
	transport.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	reader := NewClient(c.baseURL, c.token, &transport)
	return reader.doWithHeaders(ctx, http.MethodGet, path, nil, map[string]string{capability: "1"})
}
