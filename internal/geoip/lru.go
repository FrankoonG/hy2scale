package geoip

import (
	"container/list"
	"sync"
)

// lru caches lookup results. clear() bumps a generation so a lookup that
// ran against the databases being replaced does not store its answer.
type lru struct {
	mu  sync.Mutex
	max int
	gen uint64
	ll  *list.List
	m   map[cacheKey]*list.Element
}

type lruEntry struct {
	k cacheKey
	v Result
}

func newLRU(max int) *lru {
	return &lru{max: max, ll: list.New(), m: map[cacheKey]*list.Element{}}
}

func (c *lru) get(k cacheKey) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*lruEntry).v, true
	}
	return Result{}, false
}

func (c *lru) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *lru) put(k cacheKey, v Result, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	if e, ok := c.m[k]; ok {
		e.Value.(*lruEntry).v = v
		c.ll.MoveToFront(e)
		return
	}
	c.m[k] = c.ll.PushFront(&lruEntry{k, v})
	for c.ll.Len() > c.max {
		e := c.ll.Back()
		c.ll.Remove(e)
		delete(c.m, e.Value.(*lruEntry).k)
	}
}

func (c *lru) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.ll.Init()
	c.m = map[cacheKey]*list.Element{}
}
