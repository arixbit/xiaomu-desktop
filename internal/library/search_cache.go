package library

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/arixbit/xiaomu-desktop/internal/catalog"
	"github.com/arixbit/xiaomu-desktop/internal/config"
)

const (
	searchCacheTTL    = 10 * time.Minute
	searchCooldown    = 30 * time.Second
	searchCacheLimit  = 256
	searchCacheBudget = 8 << 20
)

type searchKey struct {
	origin, assets, query string
	kind                  config.Kind
	page                  int
}

func (k searchKey) site() searchKey {
	k.query, k.page = "", 0
	return k
}

type searchCacheEntry struct {
	page    catalog.Page
	err     error
	expires time.Time
	bytes   int
}

// SearchCache is a bounded, in-memory cache owned by one imported library.
// Its zero value is ready to use; concurrent scans may safely share it.
// Replace it on manual refresh so cancelled workers cannot refill the new cache.
type SearchCache struct {
	mu      sync.Mutex
	entries map[searchKey]searchCacheEntry
	bytes   int
	clock   func() time.Time
}

func (c *SearchCache) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

func (c *SearchCache) get(key searchKey) (catalog.Page, error, bool) {
	if c == nil {
		return catalog.Page{}, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Fresh results remain useful even if another page has put a site on hold.
	for _, k := range []searchKey{key, key.site()} {
		entry, ok := c.entries[k]
		if !ok {
			continue
		}
		if !now.Before(entry.expires) {
			c.remove(k)
			continue
		}
		page := entry.page
		page.Videos = slices.Clone(page.Videos)
		return page, entry.err, true
	}
	return catalog.Page{}, nil, false
}

func (c *SearchCache) put(key searchKey, page catalog.Page, err error) {
	if c == nil || errors.Is(err, context.Canceled) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			c.remove(k)
		}
	}
	ttl := searchCacheTTL
	if err != nil {
		key, page, ttl = key.site(), catalog.Page{}, searchCooldown
	} else {
		c.remove(key.site())
	}
	c.remove(key)
	size := len(key.origin) + len(key.assets) + len(key.query) + 256
	for _, video := range page.Videos {
		size += 128 + len(video.ID) + len(video.Name) + len(video.Pic) + len(video.Remarks) + len(video.Year) + len(video.Type) + len(video.Content)
	}
	if err != nil {
		size += len(err.Error())
	}
	if size > searchCacheBudget {
		return
	}
	for len(c.entries) >= searchCacheLimit || c.bytes+size > searchCacheBudget {
		var oldest searchKey
		var expires time.Time
		for k, entry := range c.entries {
			if expires.IsZero() || entry.expires.Before(expires) {
				oldest, expires = k, entry.expires
			}
		}
		c.remove(oldest)
	}
	if c.entries == nil {
		c.entries = make(map[searchKey]searchCacheEntry)
	}
	page.Videos = slices.Clone(page.Videos)
	c.entries[key] = searchCacheEntry{page: page, err: err, expires: now.Add(ttl), bytes: size}
	c.bytes += size
}

// remove is called with mu held.
func (c *SearchCache) remove(key searchKey) {
	if entry, ok := c.entries[key]; ok {
		c.bytes -= entry.bytes
		delete(c.entries, key)
	}
}
