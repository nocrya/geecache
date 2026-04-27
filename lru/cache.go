package lru

import (
	"container/list"
	"sync"
)

// Node 双向链表节点，承载 key 与实现了 Value 接口的值。
type Node struct {
	key   string
	value Value
}

// Cache 按字节上限约束的 LRU 缓存：链表越靠前越新，淘汰从表尾取。
type Cache struct {
	maxBytes  int64
	usedBytes int64
	ll        *list.List
	cache     map[string]*list.Element
	onEvicted func(key string, value Value)
	mu        sync.Mutex
}

// Value 缓存值需能报告自身占用的字节数（与 key 一起计入 usedBytes）。
type Value interface {
	Len() int
}

// New 创建 LRU，maxBytes 为总字节上限；onEvicted 在因容量淘汰最旧项时调用（Remove 不触发）。
func New(maxBytes int64, onEvicted func(key string, value Value)) *Cache {
	return &Cache{
		maxBytes:  maxBytes,
		usedBytes: 0,
		ll:        list.New(),
		cache:     make(map[string]*list.Element),
		onEvicted: onEvicted,
	}
}

// Add 插入或更新；超容量时从表尾反复淘汰直至能容纳。
func (c *Cache) Add(key string, value Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// key 已存在：移到表头并更新 value，必要时淘汰旧项为新 value 腾空间。
	if ele, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ele)
		c.usedBytes -= int64(ele.Value.(*Node).value.Len())
		for c.usedBytes+int64(value.Len()) > c.maxBytes {
			c.evictOneLocked()
		}
		ele.Value.(*Node).value = value
		c.usedBytes += int64(value.Len())
		return
	}
	// 新 key：淘汰直至 key+value 能放下，再插到表头。
	for c.usedBytes+int64(len(key))+int64(value.Len()) > c.maxBytes {
		c.evictOneLocked()
	}
	node := &Node{
		key:   key,
		value: value,
	}
	ele := c.ll.PushFront(node)
	c.cache[key] = ele
	c.usedBytes += int64(len(key)) + int64(value.Len())
}

// Remove 若存在则删除该 key；不调用 onEvicted（用于分层挪移、显式删除，与容量淘汰语义区分）。
func (c *Cache) Remove(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ele, ok := c.cache[key]
	if !ok {
		return false
	}
	c.ll.Remove(ele)
	node := ele.Value.(*Node)
	delete(c.cache, node.key)
	c.usedBytes -= int64(len(node.key)) + int64(node.value.Len())
	return true
}

// Get 命中则将对应结点移到表头（视为最近使用）。
func (c *Cache) Get(key string) (value Value, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ele, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ele)
		return ele.Value.(*Node).value, true
	}
	return nil, false
}

// evictOneLocked 从表尾删除最久未使用的一项，并回调 onEvicted。
func (c *Cache) evictOneLocked() {
	ele := c.ll.Back()
	if ele != nil {
		c.ll.Remove(ele)
		node := ele.Value.(*Node)
		delete(c.cache, node.key)
		c.usedBytes -= int64(len(node.key)) + int64(node.value.Len())
		if c.onEvicted != nil {
			c.onEvicted(node.key, node.value)
		}
	}
}
