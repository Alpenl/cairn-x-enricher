package dashboard

import (
	"context"
	"sync"
	"time"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

// taxonomyCacheTTL bounds how long a cached vocabulary may be served. The
// vocabulary only changes on Worker deploy plus process restart, so a short
// TTL is enough to stop every request from re-fetching it while still
// recovering automatically if the Worker rotates it underneath us.
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
	source taxonomySource
	now    func() time.Time

	mu       sync.Mutex
	catalog  taxonomy.Catalog
	rendered *taxonomy.Renderer
	loadedAt time.Time
	// fresh records whether loadedAt still authorises a cache hit. It is
	// separate from loadedAt so Invalidate can force a refetch while
	// keeping the last good catalog as an outage fallback.
	fresh bool
}

func newTaxonomyCache(source taxonomySource) *taxonomyCache {
	return &taxonomyCache{source: source, now: time.Now}
}

// Catalog returns a validated catalog, fetching it at most once per TTL.
func (c *taxonomyCache) Catalog(ctx context.Context) (taxonomy.Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fresh && c.now().Sub(c.loadedAt) < taxonomyCacheTTL {
		return c.catalog, nil
	}
	catalog, err := c.source.GetTaxonomy(ctx)
	if err != nil {
		// Keep serving the last known good vocabulary: an upstream blip
		// should not break curation edits that are already validated
		// against a compatible catalog.
		if c.hasCatalog() {
			return c.catalog, nil
		}
		return taxonomy.Catalog{}, err
	}
	c.catalog = catalog
	c.rendered = taxonomy.NewRenderer(catalog)
	c.loadedAt = c.now()
	c.fresh = true
	return catalog, nil
}

func (c *taxonomyCache) hasCatalog() bool {
	return !c.loadedAt.IsZero() || c.rendered != nil
}

// Invalidate forces the next read to refetch the vocabulary while keeping the
// last known good copy available if that refetch fails.
func (c *taxonomyCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fresh = false
}
