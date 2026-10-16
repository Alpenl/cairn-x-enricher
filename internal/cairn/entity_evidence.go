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
	"unicode/utf16"
	"unicode/utf8"
)

var (
	// ErrEntityEvidenceUnavailable means the referenced stored block cannot be read.
	ErrEntityEvidenceUnavailable = errors.New("referenced source evidence is unavailable")
	// ErrEntityEvidenceChanged means the snapshot exists under another source revision.
	ErrEntityEvidenceChanged = errors.New("source evidence reference changed")
)

// EntityEvidenceReference identifies one span in an immutable, owned snapshot.
// No arbitrary upstream URL or source text is accepted as a read parameter.
type EntityEvidenceReference struct {
	SnapshotID      int64
	ContentRevision int64
	BlockID         string
	Start           int64
	End             int64
}

func (ref EntityEvidenceReference) valid() bool {
	const maxInteger = 1<<53 - 1
	return ref.SnapshotID > 0 && ref.SnapshotID <= maxInteger && ref.ContentRevision > 0 &&
		ref.ContentRevision <= maxInteger && ref.Start >= 0 && ref.End > ref.Start &&
		ref.End <= 100_000 && ref.End-ref.Start <= 2048 && len(ref.BlockID) > 0 && len(ref.BlockID) <= 256 &&
		utf8.ValidString(ref.BlockID) && len(utf16.Encode([]rune(ref.BlockID))) <= 64
}

// ParseEntityEvidenceReference accepts only the five canonical span query fields.
func ParseEntityEvidenceReference(query url.Values) (EntityEvidenceReference, error) {
	invalid := errors.New("invalid evidence reference")
	keys := []string{"snapshot_id", "content_revision", "block_id", "start", "end"}
	if len(query) != len(keys) {
		return EntityEvidenceReference{}, invalid
	}
	for _, key := range keys {
		if len(query[key]) != 1 {
			return EntityEvidenceReference{}, invalid
		}
	}
	values := make([]int64, 0, 4)
	for _, key := range []string{"snapshot_id", "content_revision", "start", "end"} {
		value := query.Get(key)
		parsed, err := strconv.ParseUint(value, 10, 53)
		if err != nil || strconv.FormatUint(parsed, 10) != value {
			return EntityEvidenceReference{}, invalid
		}
		values = append(values, int64(parsed))
	}
	ref := EntityEvidenceReference{SnapshotID: values[0], ContentRevision: values[1], BlockID: query.Get("block_id"), Start: values[2], End: values[3]}
	if !ref.valid() {
		return EntityEvidenceReference{}, invalid
	}
	return ref, nil
}

func (ref EntityEvidenceReference) query() string {
	return url.Values{"snapshot_id": {strconv.FormatInt(ref.SnapshotID, 10)},
		"content_revision": {strconv.FormatInt(ref.ContentRevision, 10)}, "block_id": {ref.BlockID},
		"start": {strconv.FormatInt(ref.Start, 10)}, "end": {strconv.FormatInt(ref.End, 10)}}.Encode()
}

// GetEntityEvidence relays only the acknowledged, bounded source-span contract.
// An unsupported route and an unavailable stored reference are different states.
func (c *Client) GetEntityEvidence(ctx context.Context, id int64, ref EntityEvidenceReference) (json.RawMessage, error) {
	if id < 1 || id > 1<<53-1 || !ref.valid() {
		return nil, errors.New("invalid evidence reference")
	}
	response, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/links/%d/entity-evidence?%s", id, ref.query()), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	supported := response.Header.Get("X-Cairn-Entity-Evidence") == "1"
	switch {
	case response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented:
		return nil, ErrV2Unsupported
	case response.StatusCode == http.StatusNotFound && !supported:
		return nil, ErrV2Unsupported
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrEntityEvidenceUnavailable
	case response.StatusCode == http.StatusConflict && supported:
		return nil, ErrEntityEvidenceChanged
	case response.StatusCode != http.StatusOK:
		return nil, apiError(response)
	case !supported:
		return nil, ErrV2Unsupported
	}
	const maxBody = 32 << 10
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	invalid := errors.New("invalid source evidence response")
	if len(payload) > maxBody {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return nil, invalid
	}
	var version int64
	if json.Unmarshal(fields["version"], &version) != nil {
		return nil, invalid
	}
	if version != 1 {
		return nil, ErrV2Unsupported
	}
	for key, expected := range map[string]int64{"bookmark_id": id, "evidence_snapshot_id": ref.SnapshotID,
		"content_revision": ref.ContentRevision, "span_start": ref.Start, "span_end": ref.End} {
		var actual int64
		if json.Unmarshal(fields[key], &actual) != nil || actual != expected {
			return nil, invalid
		}
	}
	var block string
	if json.Unmarshal(fields["block_id"], &block) != nil || block != ref.BlockID {
		return nil, invalid
	}
	return payload, nil
}
