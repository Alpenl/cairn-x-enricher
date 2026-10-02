package dashboard

import (
	"encoding/json"
	"net/http"
)

// The UI sees display metadata only after negotiation. Recall terms are
// classifier hints, not aliases, and never become searchable UI synonyms.
func writeTopicTaxonomy(writer http.ResponseWriter, request *http.Request, catalog any) {
	raw, err := json.Marshal(catalog)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "invalid_taxonomy")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		writeError(writer, http.StatusInternalServerError, "invalid_taxonomy")
		return
	}
	aware := request.Header.Get("X-Cairn-Tag-System") == "1" && request.Header.Get("X-Cairn-Topic-Granularity") == "1"
	available := false
	topics, _ := payload["topics"].([]any)
	for _, value := range topics {
		term, ok := value.(map[string]any)
		if !ok {
			continue
		}
		delete(term, "recall_terms")
		if !aware {
			delete(term, "granularity")
			delete(term, "navigation")
			delete(term, "relations")
			continue
		}
		if level, _ := term["granularity"].(string); level == "specific" || level == "broad" {
			available = true
			if _, present := term["navigation"]; !present {
				term["navigation"] = false
			}
		}
	}
	if available {
		writer.Header().Set("X-Cairn-Topic-Granularity", "1")
		writer.Header().Set("X-Cairn-Tag-System", "1")
	}
	writeJSON(writer, http.StatusOK, payload)
}
