package domotz

import (
	"sync"
	"time"
)

// ttlCache is a small concurrency-safe cache with per-entry expiry and
// single-flight loading.
//
// It exists because the Domotz Public API enforces a daily request quota (the
// same quota the config page reports). Resolving a variable's label, unit and
// owning device at query time - rather than freezing them into the saved
// dashboard JSON - would otherwise multiply requests by the number of panels on
// every refresh. Metadata changes rarely, so a short TTL collapses a dashboard
// refresh down to a couple of upstream calls.
//
// Loading is single-flight because the harder limit is concurrency, not the
// daily allowance: GET /meta/usage reports concurrent_allowed: 5. A dashboard
// opening on a cold cache asks every panel for the same collector and device
// lists at once, and without deduplication those identical requests compete for
// a budget of five.
type ttlCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	entries  map[string]cacheEntry
	inflight map[string]*inflightCall

	// generation increments on invalidate, so a load that started before a
	// refresh does not install its now-stale result afterwards.
	generation uint64
}

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

// inflightCall is one in-progress load. Waiters block on done and then read
// value and err, which are written before it closes.
type inflightCall struct {
	done  chan struct{}
	value any
	err   error
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{
		ttl:      ttl,
		now:      time.Now,
		entries:  make(map[string]cacheEntry),
		inflight: make(map[string]*inflightCall),
	}
}

// load returns the live entry for key, joins an in-flight load for it, or runs
// fn as the single loader.
//
// Only successful loads are cached: an error is returned to every waiter but
// leaves the key empty, so a transient 429 does not pin a failure for the whole
// TTL.
func (c *ttlCache) load(key string, fn func() (any, error)) (any, error) {
	c.mu.Lock()

	if entry, ok := c.entries[key]; ok && !c.now().After(entry.expiresAt) {
		c.mu.Unlock()
		return entry.value, nil
	}

	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.value, call.err
	}

	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	generation := c.generation
	c.mu.Unlock()

	call.value, call.err = fn()

	c.mu.Lock()
	delete(c.inflight, key)
	if call.err == nil && c.generation == generation {
		c.entries[key] = cacheEntry{value: call.value, expiresAt: c.now().Add(c.ttl)}
	}
	c.mu.Unlock()

	close(call.done)
	return call.value, call.err
}

func (c *ttlCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return entry.value, true
}

// invalidate drops every entry. Used when the data source settings change and
// when the query editor's refresh button asks for current metadata.
func (c *ttlCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]cacheEntry)
	c.generation++
}
