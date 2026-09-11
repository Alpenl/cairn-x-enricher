package buildinfo

import "testing"

func TestCurrentReportsInjectedBuildMetadata(t *testing.T) {
	original := struct{ version, commit, date string }{Version, Commit, Date}
	t.Cleanup(func() { Version, Commit, Date = original.version, original.commit, original.date })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-09-10T00:00:00Z"
	info := Current()
	if info.Version != "v1.2.3" || info.Commit != "abc1234" || info.Date != "2026-09-10T00:00:00Z" {
		t.Fatalf("Current() = %+v", info)
	}
}

func TestDefaultsAreUsableWithoutLdflags(t *testing.T) {
	// `go run ./cmd/cairn-x-enricher version` must not print empty fields.
	info := Current()
	if info.Version == "" || info.Commit == "" || info.Date == "" {
		t.Fatalf("Current() = %+v, want non-empty defaults", info)
	}
}
