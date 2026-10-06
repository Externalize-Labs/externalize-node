package archive

import (
	"container/list"
	"sync"
)

// lru keeps the most recently used decoded checkpoint files in memory.
// Checkpoint files are immutable, so entries never need invalidating.
type lru struct {
	mu    sync.Mutex
	cap   int
	order *list.List // front = most recent
	items map[string]*list.Element
}

type lruEntry struct {
	key     string
	records [][]byte
}

func newLRU(capacity int) *lru {
	return &lru{cap: capacity, order: list.New(), items: map[string]*list.Element{}}
}

func (l *lru) get(key string) ([][]byte, bool) {
	if l == nil || l.cap <= 0 {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.items[key]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(e)
	return e.Value.(*lruEntry).records, true
}

func (l *lru) put(key string, records [][]byte) {
	if l == nil || l.cap <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.items[key]; ok {
		l.order.MoveToFront(e)
		return
	}
	l.items[key] = l.order.PushFront(&lruEntry{key: key, records: records})
	for l.order.Len() > l.cap {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.items, oldest.Value.(*lruEntry).key)
	}
}
