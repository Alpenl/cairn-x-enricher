package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// CanonicalEntity is an operator-controlled identity, never a model-generated
// name. Identifiers are observable URLs, not URLs that the extension may fetch.
type CanonicalEntity struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	Aliases     []string `json:"aliases"`
	Identifiers []string `json:"identifiers"`
}

// EntityCatalog is supplied by the operator as a bounded versioned JSON file.
// An empty catalog means no controlled identity is known, not an empty entity run.
type EntityCatalog struct {
	Version  string            `json:"version"`
	Entities []CanonicalEntity `json:"entities"`
}

// IdentityEvidence points to material that actually contains an identifier.
// Merely sharing a surface form with a catalog entry is insufficient.
type IdentityEvidence struct {
	Identifier string `json:"identifier"`
	BlockID    string `json:"block_id"`
	Source     string `json:"source"`
}

// CanonicalOption keeps the controlled definition separate from its observable
// support. The model still has to judge whether the named occurrence refers to it.
type CanonicalOption struct {
	Entity   CanonicalEntity    `json:"entity"`
	Evidence []IdentityEvidence `json:"evidence"`
}

const maxEntityCatalogBytes = 1 << 20
const maxCanonicalOptions = 8

var canonicalID = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
var identifierURL = regexp.MustCompile(`https?://[^\s<>"'()\[\]]+`)

// LoadEntityCatalog reads a local operator-selected file with a hard byte limit.
// No path or catalog entry comes from user materials or provider answers.
func LoadEntityCatalog(path string) (EntityCatalog, error) {
	if path == "" {
		return EntityCatalog{Version: "empty-v1", Entities: []CanonicalEntity{}}, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return EntityCatalog{}, fmt.Errorf("locate entity catalog: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return EntityCatalog{}, fmt.Errorf("open entity catalog directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(filepath.Base(absolute))
	if err != nil {
		return EntityCatalog{}, fmt.Errorf("open entity catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxEntityCatalogBytes+1))
	if err != nil {
		return EntityCatalog{}, fmt.Errorf("read entity catalog: %w", err)
	}
	if len(raw) > maxEntityCatalogBytes {
		return EntityCatalog{}, errors.New("entity catalog exceeds byte limit")
	}
	return DecodeEntityCatalog(raw)
}

// DecodeEntityCatalog rejects unknown fields, duplicate identities, incomplete
// definitions and unbounded inputs before any model or Worker operation.
func DecodeEntityCatalog(raw []byte) (EntityCatalog, error) {
	if len(raw) > maxEntityCatalogBytes {
		return EntityCatalog{}, errors.New("entity catalog exceeds byte limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var catalog EntityCatalog
	if err := decoder.Decode(&catalog); err != nil {
		return EntityCatalog{}, fmt.Errorf("decode entity catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return EntityCatalog{}, errors.New("entity catalog has trailing data")
	}
	if strings.TrimSpace(catalog.Version) == "" || strings.TrimSpace(catalog.Version) != catalog.Version || len(catalog.Version) > 80 || catalog.Entities == nil || len(catalog.Entities) > 200 {
		return EntityCatalog{}, errors.New("invalid entity catalog size or version")
	}
	ids := map[string]bool{}
	identityOwners := map[string]string{}
	for _, entry := range catalog.Entities {
		if !canonicalID.MatchString(entry.ID) || ids[entry.ID] || strings.TrimSpace(entry.Label) == "" || strings.TrimSpace(entry.Label) != entry.Label || len([]rune(entry.Label)) > 120 {
			return EntityCatalog{}, errors.New("invalid or duplicate canonical identity")
		}
		ids[entry.ID] = true
		switch entry.Kind {
		case "person", "organization", "product", "project", "place":
		default:
			return EntityCatalog{}, errors.New("invalid canonical entity kind")
		}
		if entry.Aliases == nil || len(entry.Aliases) > 16 || len(entry.Identifiers) < 1 || len(entry.Identifiers) > 8 {
			return EntityCatalog{}, errors.New("invalid canonical aliases or identifiers")
		}
		aliases := map[string]bool{}
		for _, alias := range entry.Aliases {
			key := strings.ToLower(strings.TrimSpace(alias))
			if key == "" || strings.TrimSpace(alias) != alias || len([]rune(alias)) > 120 || aliases[key] {
				return EntityCatalog{}, errors.New("invalid canonical alias")
			}
			aliases[key] = true
		}
		identifiers := map[string]bool{}
		for _, identifier := range entry.Identifiers {
			key := normalizeIdentityURL(identifier)
			if key == "" || identifiers[key] {
				return EntityCatalog{}, errors.New("invalid canonical identifier")
			}
			if owner := identityOwners[key]; owner != "" && owner != entry.ID {
				return EntityCatalog{}, errors.New("canonical identifier belongs to multiple identities")
			}
			identityOwners[key] = entry.ID
			identifiers[key] = true
		}
	}
	sort.Slice(catalog.Entities, func(i, j int) bool { return catalog.Entities[i].ID < catalog.Entities[j].ID })
	return catalog, nil
}

func normalizeIdentityURL(raw string) string {
	if len(raw) > 2048 || strings.ContainsRune(raw, '\\') || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return ""
	}
	value, err := url.Parse(raw)
	if err != nil || value.Hostname() == "" || value.User != nil || value.Opaque != "" || (value.Scheme != "http" && value.Scheme != "https") {
		return ""
	}
	withoutFragment, _, _ := strings.Cut(raw, "#")
	authorityEnd := len(value.Scheme) + 3 + len(value.Host)
	return value.Scheme + "://" + strings.ToLower(value.Host) + withoutFragment[authorityEnd:]
}

func identityEvidence(candidate SurfaceCandidate, entry CanonicalEntity, blocks []Block) []IdentityEvidence {
	evidence := []IdentityEvidence{}
	seen := map[string]bool{}
	add := func(raw, block, source string) {
		normalized := normalizeIdentityURL(raw)
		if normalized == "" {
			return
		}
		for _, identifier := range entry.Identifiers {
			if normalizeIdentityURL(identifier) == normalized {
				key := identifier + "\x00" + block + "\x00" + source
				if !seen[key] {
					evidence = append(evidence, IdentityEvidence{Identifier: identifier, BlockID: block, Source: source})
					seen[key] = true
				}
			}
		}
	}
	if candidate.Kind == "link" {
		add(candidate.SourceURL, "", "stored_link")
		return evidence
	}
	for _, block := range blocks {
		if block.ID != candidate.BlockID {
			continue
		}
		add(block.URL, block.ID, "block_url")
		for _, raw := range identifierURL.FindAllString(block.Text, -1) {
			add(strings.TrimRight(raw, ".,;!?"), block.ID, "block_text")
		}
	}
	return evidence
}

func (catalog EntityCatalog) options(candidate SurfaceCandidate, blocks []Block) []CanonicalOption {
	options := []CanonicalOption{}
	surface := strings.ToLower(strings.TrimSpace(candidate.Surface))
	for _, entry := range catalog.Entities {
		named := strings.ToLower(entry.Label) == surface || candidate.Kind == "link"
		for _, alias := range entry.Aliases {
			named = named || strings.ToLower(alias) == surface
		}
		if !named {
			continue
		}
		evidence := identityEvidence(candidate, entry, blocks)
		if len(evidence) == 0 {
			continue
		}
		options = append(options, CanonicalOption{Entity: entry, Evidence: evidence})
		if len(options) > maxCanonicalOptions {
			// An ambiguous overfull set must not silently lose the correct identity.
			return []CanonicalOption{}
		}
	}
	return options
}
