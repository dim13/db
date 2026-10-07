// Package lru implements a cache evicting least recently used values,
// approximated by second chance.
package lru

import (
	"container/list"
	"sync"
	"sync/atomic"
)

// Cache maps keys to values.  Hits take no lock, so concurrent readers
// scale.  Values are only evicted by Trim, so callers may hold them during
// an operation.  The zero value is an empty cache without limit.
type Cache[K comparable, V any] struct {
	Limit int // values kept by Trim, 0 for no limit

	mu sync.Mutex // guards l and stores to m
	m  sync.Map   // K to *list.Element
	l  list.List  // of *entry[K, V], most recently added first
	n  atomic.Int64
}

// entry is a cached value and its recently-used state.
type entry[K comparable, V any] struct {
	key  K
	val  V
	used atomic.Bool // hit since last trim, gets a second chance
}

// lookup returns the entry of k without locking.
func (c *Cache[K, V]) lookup(k K) (*entry[K, V], bool) {
	v, ok := c.m.Load(k)
	if !ok {
		return nil, false
	}
	return v.(*list.Element).Value.(*entry[K, V]), true
}

// Peek returns the value of k without marking it used.
func (c *Cache[K, V]) Peek(k K) (V, bool) {
	e, ok := c.lookup(k)
	if !ok {
		var v V
		return v, false
	}
	return e.val, true
}

// Get returns the value of k and marks it used.  On a miss it caches and
// returns the result of load, which runs under the cache lock, so
// concurrent misses of k load it once; load must not call c.
func (c *Cache[K, V]) Get(k K, load func() (V, error)) (V, error) {
	if e, ok := c.lookup(k); ok {
		if !e.used.Load() { // spare the cache line when set
			e.used.Store(true)
		}
		return e.val, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.lookup(k); ok { // loaded by another reader meanwhile
		return e.val, nil
	}
	v, err := load()
	if err != nil {
		return v, err
	}
	c.add(k, v)
	return v, nil
}

// Add caches v under k, replacing any previous value.
func (c *Cache[K, V]) Add(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.add(k, v)
}

// add implements Add; the caller must hold c.mu.
func (c *Cache[K, V]) add(k K, v V) {
	if el, ok := c.m.Load(k); ok {
		c.remove(el.(*list.Element))
	}
	c.m.Store(k, c.l.PushFront(&entry[K, V]{key: k, val: v}))
	c.n.Add(1)
}

// Delete removes k.
func (c *Cache[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m.Load(k); ok {
		c.remove(el.(*list.Element))
	}
}

// remove drops el; the caller must hold c.mu.
func (c *Cache[K, V]) remove(el *list.Element) {
	c.m.Delete(el.Value.(*entry[K, V]).key)
	c.l.Remove(el)
	c.n.Add(-1)
}

// Len returns the number of cached values.
func (c *Cache[K, V]) Len() int {
	return int(c.n.Load())
}

// Range calls f for each value, most recently added first, and returns
// the first error.
func (c *Cache[K, V]) Range(f func(V) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.l.Front(); el != nil; el = el.Next() {
		if err := f(el.Value.(*entry[K, V]).val); err != nil {
			return err
		}
	}
	return nil
}

// Trim evicts least recently used values until at most Limit remain.
// Used values get a second chance.  evict is called before a value is
// dropped and returns false to keep it, e.g. a dirty page that cannot be
// written; an error stops Trim.  evict must not call c.
func (c *Cache[K, V]) Trim(evict func(V) (bool, error)) error {
	if c.Limit == 0 || c.n.Load() <= int64(c.Limit) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Each value is visited at most twice, once to clear used and once to
	// evict, so values evict keeps cannot loop forever.
	for n := 2 * c.l.Len(); c.l.Len() > c.Limit && n > 0; n-- {
		el := c.l.Back()
		e := el.Value.(*entry[K, V])
		if e.used.Swap(false) {
			c.l.MoveToFront(el)
			continue
		}
		ok, err := evict(e.val)
		if err != nil {
			return err
		}
		if !ok {
			c.l.MoveToFront(el)
			continue
		}
		c.remove(el)
	}
	return nil
}
