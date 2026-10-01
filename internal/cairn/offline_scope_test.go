package cairn

import (
	"strings"
	"testing"
)

func TestOfflineScopeSeparatesBackendAndCredential(t *testing.T) {
	a := NewClient("https://worker.example/", "private-fixture-key", nil).OfflineScope()
	if len(a) != 64 || strings.Contains(a, "private-fixture-key") || a != NewClient("https://worker.example", "private-fixture-key", nil).OfflineScope() {
		t.Fatal("offline scope must be stable, normalized and opaque")
	}
	for _, client := range []*Client{NewClient("https://other.example", "private-fixture-key", nil), NewClient("https://worker.example", "different-key", nil)} {
		if a == client.OfflineScope() {
			t.Fatal("account or backend change reused old browser storage scope")
		}
	}
}
