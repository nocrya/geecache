package lru

import (
	"sync"
)

// LFU 按字节上限约束的「最不经常使用」近似缓存：淘汰时选访问频次最低者；
// 频次相同则按插入序号更早者淘汰。淘汰需遍历全表，规模为 O(n)，适合演示或中等 key 数量。
type LFU struct {
	maxBytes  int64
	usedBytes int64
	items     map[string]*lfuEnt
	nextSeq   int64
	mu        sync.Mutex
	onEvicted func(key string, value Value)
}

// lfuEnt 单条缓存项：频次 freq、入队序号 seq（用于同频 tie-break）。
type lfuEnt struct {
	key   string
	value Value
	freq  int
	seq   int64
}

// NewLFU 创建 LFU；onEvicted 仅在容量淘汰时调用（Remove 不触发）。
func NewLFU(maxBytes int64, onEvicted func(key string, value Value)) *LFU {
	return &LFU{
		maxBytes:  maxBytes,
		items:     make(map[string]*lfuEnt),
		onEvicted: onEvicted,
	}
}

// Get 命中则访问计数 +1。
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

// Add 插入或更新；超容量时反复淘汰一条直至能容纳。
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

// Remove 若存在则删除；不调用 onEvicted（与 Cache.Remove 语义一致）。
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

// evictOneLocked 在已持锁下淘汰一条：freq 最小，相同时 seq 最小（最早插入）。
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
