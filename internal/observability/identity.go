package observability

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	"github.com/Alpenl/cairn-x-enricher/internal/buildinfo"
)

// Product handlers install these trusted fields before task groups. Callers
// cannot replace them through record attributes, including after WithGroup.
var logIdentity = newLogIdentity(buildinfo.Current().Commit)

func newLogIdentity(commit string) []slog.Attr {
	attrs := []slog.Attr{slog.Int("schema_version", 1), slog.String("service", "cairn-x-enricher")}
	if validCommit(commit) {
		attrs = append(attrs, slog.String("build_sha", commit))
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err == nil {
		attrs = append(attrs, slog.String("instance_id", hex.EncodeToString(id[:])))
	}
	return attrs
}

func validCommit(commit string) bool {
	if len(commit) < 7 || len(commit) > 40 {
		return false
	}
	for _, c := range commit {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
