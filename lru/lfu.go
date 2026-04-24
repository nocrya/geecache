package lru

import (
	"sync"
)

// LFU is a byte-bounded frequency cache: eviction picks minimum freq, tie-break by oldest seq.
// Eviction is O(n) in size; suitable for demo / moderate cardinality vs O(1) LRU list.
type LFU struct {
	maxBytes  int64
	usedBytes int64
	items     map[string]*lfuEnt
	nextSeq   int64
	mu        sync.Mutex
	onEvicted func(key string, value Value)
}

type lfuEnt struct {
	key   string
	value Value
	freq  int
	seq   int64
}

func NewLFU(maxBytes int64, onEvicted func(key string, value Value)) *LFU {
	return &LFU{
		maxBytes:  maxBytes,
		items:     make(map[string]*lfuEnt),
		onEvicted: onEvicted,
	}
}

func (f *LFU) Get(key string) (Value, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.items[key]
	if !ok {
		return nil, false
	}
	e.freq++
	return e.value, true
}

func (f *LFU) Add(key string, value Value) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.items[key]; ok {
		f.usedBytes -= int64(e.value.Len())
		for f.usedBytes+int64(value.Len()) > f.maxBytes {
			f.evictOneLocked()
		}
		e.value = value
		e.freq++
		f.usedBytes += int64(value.Len())
		return
	}
	for f.usedBytes+int64(len(key))+int64(value.Len()) > f.maxBytes {
		f.evictOneLocked()
	}
	f.nextSeq++
	f.items[key] = &lfuEnt{key: key, value: value, freq: 1, seq: f.nextSeq}
	f.usedBytes += int64(len(key)) + int64(value.Len())
}

func (f *LFU) Remove(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.items[key]
	if !ok {
		return false
	}
	delete(f.items, key)
	f.usedBytes -= int64(len(e.key)) + int64(e.value.Len())
	return true
}

func (f *LFU) evictOneLocked() {
	if len(f.items) == 0 {
		return
	}
	var victim *lfuEnt
	for _, e := range f.items {
		if victim == nil || e.freq < victim.freq || (e.freq == victim.freq && e.seq < victim.seq) {
			victim = e
		}
	}
	if victim == nil {
		return
	}
	delete(f.items, victim.key)
	f.usedBytes -= int64(len(victim.key)) + int64(victim.value.Len())
	if f.onEvicted != nil {
		f.onEvicted(victim.key, victim.value)
	}
}
