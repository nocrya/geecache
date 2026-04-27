package lru

import (
	"container/list"
	"sync"
)

// arcReal 为 T1/T2 链表结点承载的数据。
type arcReal struct {
	key   string
	value Value
}

// ARC 自适应替换缓存（IBM ARC 思路的按字节版）：
// - T1：近期出现一次的项（LRU）；
// - T2：频繁项（命中 T1 后升入 T2）；
// - B1/B2：仅从对应真实表淘汰下来的 key 幽灵（无 value），用于调节目标分区 p。
// 容量：|T1|+|T2| 总字节不超过 maxBytes；幽灵 key 总字节亦不超过 maxBytes（超出则淘汰最旧幽灵）。
type ARC struct {
	maxBytes int64
	mu       sync.Mutex
	onEvicted func(string, Value)

	t1, t2 *list.List
	m1, m2 map[string]*list.Element

	b1, b2 *list.List
	g1, g2 map[string]*list.Element

	bytes1, bytes2 int64 // T1、T2 已用字节（key+value）
	ghost1, ghost2 int64 // B1、B2 幽灵仅 key 长度
	p              int64 // T1 目标字节上限，在 [0,maxBytes] 自适应
}

// NewARC 创建 ARC；maxBytes<=0 时按 1 处理。
func NewARC(maxBytes int64, onEvicted func(string, Value)) *ARC {
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &ARC{
		maxBytes:  maxBytes,
		onEvicted: onEvicted,
		t1:        list.New(),
		t2:        list.New(),
		b1:        list.New(),
		b2:        list.New(),
		m1:        make(map[string]*list.Element),
		m2:        make(map[string]*list.Element),
		g1:        make(map[string]*list.Element),
		g2:        make(map[string]*list.Element),
		p:         maxBytes / 2,
	}
}

func (a *ARC) entBytes(key string, v Value) int64 {
	return int64(len(key)) + int64(v.Len())
}

func (a *ARC) totalReal() int64 {
	return a.bytes1 + a.bytes2
}

func (a *ARC) totalGhost() int64 {
	return a.ghost1 + a.ghost2
}

// Get 仅在 T1/T2 中查找；命中 T1 则升入 T2（ARC 规则）。
func (a *ARC) Get(key string) (Value, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ele, ok := a.m1[key]; ok {
		n := ele.Value.(*arcReal)
		a.t1.Remove(ele)
		delete(a.m1, key)
		a.bytes1 -= a.entBytes(n.key, n.value)
		ne := a.t2.PushFront(n)
		a.m2[key] = ne
		a.bytes2 += a.entBytes(n.key, n.value)
		return n.value, true
	}
	if ele, ok := a.m2[key]; ok {
		n := ele.Value.(*arcReal)
		a.t2.Remove(ele)
		delete(a.m2, key)
		a.bytes2 -= a.entBytes(n.key, n.value)
		ne := a.t2.PushFront(n)
		a.m2[key] = ne
		a.bytes2 += a.entBytes(n.key, n.value)
		return n.value, true
	}
	return nil, false
}

// Add 插入或更新；实现 ARC 的 B1/B2 调节与 REPLACE。
func (a *ARC) Add(key string, value Value) {
	a.mu.Lock()
	defer a.mu.Unlock()
	nb := a.entBytes(key, value)

	if ele, ok := a.m1[key]; ok {
		n := ele.Value.(*arcReal)
		a.bytes1 -= int64(n.value.Len())
		for a.totalReal()-int64(n.value.Len())+nb > a.maxBytes {
			a.replaceOneLocked(false)
		}
		n.value = value
		a.bytes1 += int64(n.value.Len())
		a.t1.Remove(ele)
		delete(a.m1, key)
		ne := a.t1.PushFront(n)
		a.m1[key] = ne
		return
	}
	if ele, ok := a.m2[key]; ok {
		n := ele.Value.(*arcReal)
		a.bytes2 -= int64(n.value.Len())
		for a.totalReal()-int64(n.value.Len())+nb > a.maxBytes {
			a.replaceOneLocked(true)
		}
		n.value = value
		a.bytes2 += int64(n.value.Len())
		a.t2.Remove(ele)
		delete(a.m2, key)
		ne := a.t2.PushFront(n)
		a.m2[key] = ne
		return
	}

	if _, ok := a.g1[key]; ok {
		delta := int64(1)
		if a.ghost1 > 0 {
			delta = max64(1, a.ghost2/max64(1, a.ghost1))
		}
		a.p = min64(a.maxBytes, a.p+delta)
		a.deleteGhostB1(key)
		for a.totalReal()+nb > a.maxBytes {
			a.replaceOneLocked(false)
		}
		a.insertT2Locked(key, value)
		return
	}
	if _, ok := a.g2[key]; ok {
		delta := int64(1)
		if a.ghost2 > 0 {
			delta = max64(1, a.ghost1/max64(1, a.ghost2))
		}
		a.p = max64(0, a.p-delta)
		a.deleteGhostB2(key)
		for a.totalReal()+nb > a.maxBytes {
			a.replaceOneLocked(true)
		}
		a.insertT2Locked(key, value)
		return
	}

	// 全新 key：去掉幽灵表中的同名残留后腾实表与幽灵空间，再入 T1。
	a.removeFromGhosts(key)
	for a.totalReal()+nb > a.maxBytes {
		a.replaceOneLocked(false)
	}
	a.trimGhostsForNewLocked()
	n := &arcReal{key: key, value: value}
	ele := a.t1.PushFront(n)
	a.m1[key] = ele
	a.bytes1 += nb
}

// Remove 从 T1/T2/B1/B2 中删除 key；不调用 onEvicted（与 Cache.Remove 一致）。
func (a *ARC) Remove(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ele, ok := a.m1[key]; ok {
		n := ele.Value.(*arcReal)
		a.t1.Remove(ele)
		delete(a.m1, key)
		a.bytes1 -= a.entBytes(n.key, n.value)
		return true
	}
	if ele, ok := a.m2[key]; ok {
		n := ele.Value.(*arcReal)
		a.t2.Remove(ele)
		delete(a.m2, key)
		a.bytes2 -= a.entBytes(n.key, n.value)
		return true
	}
	if _, ok := a.g1[key]; ok {
		a.deleteGhostB1(key)
		return true
	}
	if _, ok := a.g2[key]; ok {
		a.deleteGhostB2(key)
		return true
	}
	return false
}

func (a *ARC) replaceOneLocked(xPreferT2 bool) {
	if a.bytes1 > 0 && (a.bytes1 > a.p || (a.bytes2 > 0 && a.bytes1 == a.p && !xPreferT2)) {
		a.evictRealToGhost(true)
	} else if a.bytes2 > 0 {
		a.evictRealToGhost(false)
	} else if a.bytes1 > 0 {
		a.evictRealToGhost(true)
	}
}

func (a *ARC) evictRealToGhost(fromT1 bool) {
	if fromT1 {
		ele := a.t1.Back()
		if ele == nil {
			return
		}
		n := ele.Value.(*arcReal)
		a.t1.Remove(ele)
		delete(a.m1, n.key)
		a.bytes1 -= a.entBytes(n.key, n.value)
		if a.onEvicted != nil {
			a.onEvicted(n.key, n.value)
		}
		a.pushGhostB1(n.key)
		return
	}
	ele := a.t2.Back()
	if ele == nil {
		return
	}
	n := ele.Value.(*arcReal)
	a.t2.Remove(ele)
	delete(a.m2, n.key)
	a.bytes2 -= a.entBytes(n.key, n.value)
	if a.onEvicted != nil {
		a.onEvicted(n.key, n.value)
	}
	a.pushGhostB2(n.key)
}

func (a *ARC) insertT2Locked(key string, value Value) {
	n := &arcReal{key: key, value: value}
	nb := a.entBytes(key, value)
	ele := a.t2.PushFront(n)
	a.m2[key] = ele
	a.bytes2 += nb
}

func (a *ARC) pushGhostB1(key string) {
	if _, ok := a.g1[key]; ok {
		return
	}
	ele := a.b1.PushFront(key)
	a.g1[key] = ele
	a.ghost1 += int64(len(key))
	a.trimGhostBytesLocked()
}

func (a *ARC) pushGhostB2(key string) {
	if _, ok := a.g2[key]; ok {
		return
	}
	ele := a.b2.PushFront(key)
	a.g2[key] = ele
	a.ghost2 += int64(len(key))
	a.trimGhostBytesLocked()
}

func (a *ARC) deleteGhostB1(key string) {
	ele, ok := a.g1[key]
	if !ok {
		return
	}
	a.b1.Remove(ele)
	delete(a.g1, key)
	a.ghost1 -= int64(len(key))
}

func (a *ARC) deleteGhostB2(key string) {
	ele, ok := a.g2[key]
	if !ok {
		return
	}
	a.b2.Remove(ele)
	delete(a.g2, key)
	a.ghost2 -= int64(len(key))
}

func (a *ARC) removeFromGhosts(key string) {
	if ele, ok := a.g1[key]; ok {
		a.b1.Remove(ele)
		delete(a.g1, key)
		a.ghost1 -= int64(len(key))
	}
	if ele, ok := a.g2[key]; ok {
		a.b2.Remove(ele)
		delete(a.g2, key)
		a.ghost2 -= int64(len(key))
	}
}

func (a *ARC) trimGhostBytesLocked() {
	for a.totalGhost() > a.maxBytes {
		if a.b1.Len() > 0 && a.b2.Len() > 0 {
			e1 := a.b1.Back()
			e2 := a.b2.Back()
			k1 := e1.Value.(string)
			k2 := e2.Value.(string)
			if len(k1) >= len(k2) {
				a.deleteGhostB1(k1)
			} else {
				a.deleteGhostB2(k2)
			}
			continue
		}
		if a.b1.Len() > 0 {
			k := a.b1.Back().Value.(string)
			a.deleteGhostB1(k)
			continue
		}
		if a.b2.Len() > 0 {
			k := a.b2.Back().Value.(string)
			a.deleteGhostB2(k)
			continue
		}
		break
	}
}

// trimGhostsForNewLocked 新 key 入 T1 前：幽灵总字节若已满则删最旧幽灵（论文 CASE IV 的简化）。
func (a *ARC) trimGhostsForNewLocked() {
	for a.totalGhost() >= a.maxBytes && (a.b1.Len() > 0 || a.b2.Len() > 0) {
		if a.b1.Len() > 0 {
			k := a.b1.Back().Value.(string)
			a.deleteGhostB1(k)
			continue
		}
		if a.b2.Len() > 0 {
			k := a.b2.Back().Value.(string)
			a.deleteGhostB2(k)
			continue
		}
		break
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var _ CacheStore = (*ARC)(nil)
