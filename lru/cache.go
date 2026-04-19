package lru

import (
	"container/list"
)

// Node 双链表节点
type Node struct {
	key   string
	value Value
}

// Cache 缓存结构体
type Cache struct {
	maxBytes  int64
	usedBytes int64
	ll        *list.List
	cache     map[string]*list.Element
	onEvicted func(key string, value Value)
}

type Value interface {
	Len() int
}

func New(maxBytes int64, onEvicted func(key string, value Value)) *Cache {
	return &Cache{
		maxBytes:  maxBytes,
		usedBytes: 0,
		ll:        list.New(),
		cache:     make(map[string]*list.Element),
		onEvicted: onEvicted,
	}
}

func (c *Cache) Add(key string, value Value) {
	//如果key存在，更新value
	if ele, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ele)
		c.usedBytes -= int64(ele.Value.(*Node).value.Len())
		for c.usedBytes+int64(value.Len()) > c.maxBytes {
			c.removeOldest()
		}
		ele.Value.(*Node).value = value
		c.usedBytes += int64(value.Len())
		return
	}
	//如果key不存在，添加新节点
	//检查容量限制，超出则删除最老的节点
	for c.usedBytes+int64(len(key))+int64(value.Len()) > c.maxBytes {
		c.removeOldest()
	}
	//添加新节点
	node := &Node{
		key:   key,
		value: value,
	}
	ele := c.ll.PushFront(node)
	c.cache[key] = ele
	c.usedBytes += int64(len(key)) + int64(value.Len())
}

func (c *Cache) Get(key string) (value Value, ok bool) {
	if ele, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ele)
		return ele.Value.(*Node).value, true
	}
	return nil, false
}

func (c *Cache) removeOldest() {
	//1.获取链表的最后一个节点
	ele := c.ll.Back()
	if ele != nil {
		///2.从链表和缓存map中删除该节点
		c.ll.Remove(ele)
		node := ele.Value.(*Node)
		delete(c.cache, node.key)
		c.usedBytes -= int64(len(node.key)) + int64(node.value.Len())
		//3.调用回调函数
		if c.onEvicted != nil {
			c.onEvicted(node.key, node.value)
		}
	}
}
