package dashboard

import (
	"context"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/cairn"
	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// taxonomyCacheTTL bounds how long a cached vocabulary may be served. The
// vocabulary definitions are versioned, while labels and display revisions may
// be edited online. Local edits and explicit refreshes invalidate both catalogs;
// the TTL also bounds externally initiated metadata changes.
const taxonomyCacheTTL = 5 * time.Minute

// taxonomySource is the upstream reader used by the cache.
type taxonomySource interface {
	GetTaxonomy(context.Context) (taxonomy.Catalog, error)
}

// taxonomyCache serves the versioned vocabulary from memory. Curation
// validation happens on every human edit, and the catalog is identical for
// every request, so re-fetching it per edit doubled backend traffic for no
// benefit.
type taxonomyCache struct {
	source  taxonomySource
	now     func() time.Time
	workCtx context.Context
	legacy  snapshotCache[taxonomy.Catalog]
	modern  snapshotCache[cairn.V2Taxonomy]
}

func newTaxonomyCache(source taxonomySource) *taxonomyCache {
	c := &taxonomyCache{source: source, workCtx: context.Background(), now: time.Now}
	c.legacy.now = func() time.Time { return c.now() }
	c.modern.now = func() time.Time { return c.now() }
	return c
}

// Catalog returns a validated catalog, fetching it at most once per TTL.
func (c *taxonomyCache) Catalog(ctx context.Context) (taxonomy.Catalog, error) {
	value, _, err := c.legacy.read(ctx, c.workCtx, taxonomyCacheTTL, c.source.GetTaxonomy)
	return value, err
}

type modernTaxonomySource interface {
	GetV2Taxonomy(context.Context) (cairn.V2Taxonomy, error)
}

// Modern returns the negotiated catalog, including current display revisions.
func (c *taxonomyCache) Modern(ctx context.Context) (cairn.V2Taxonomy, bool, error) {
	source, ok := c.source.(modernTaxonomySource)
	if !ok {
		return cairn.V2Taxonomy{}, false, cairn.ErrV2Unsupported
	}
	return c.modern.read(ctx, c.workCtx, taxonomyCacheTTL, source.GetV2Taxonomy)
}

// Invalidate forces the next read to refetch the vocabulary while keeping the
// last known good copy available if that refetch fails.
func (c *taxonomyCache) Invalidate() {
	c.legacy.invalidate()
	c.modern.invalidate()
}
