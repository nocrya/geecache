package geecache

import (
	"fmt"
	"geecache/lru"
	"log"
	"sync"
)

var (
	// 定义一个全局的互斥锁和Map
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// A Group is a cache namespace and associated data loaded spread over a group of machines
type Group struct {
	name      string    // 组名
	getter    Getter    // 数据源回调（当缓存没命中时，调用这个函数去查数据库）
	mainCache lru.Cache // 每个组有自己的 LRU 缓存
	peers     *HTTPPool // 节点管理器（用于去其他节点找数据）
}

// Getter 接口：定义了如何从数据源加载数据
type Getter interface {
	Get(key string) ([]byte, error)
}

// GetterFunc 函数类型实现 Getter 接口
type GetterFunc func(key string) ([]byte, error)

func (f GetterFunc) Get(key string) ([]byte, error) {
	return f(key)
}

// NewGroup 创建一个组
func NewGroup(name string, cacheBytes int64, getter Getter, peers *HTTPPool) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	log.Println("[GeeCache] NewGroup", name)

	g := &Group{
		name:      name,
		getter:    getter,
		mainCache: *lru.New(cacheBytes, nil), // 这里需要调整 lru.Cache 的字段导出
		peers:     peers,
	}

	mu.Lock()
	groups[name] = g
	mu.Unlock()
	return g
}

// GetGroup 根据名字获取组
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// Get 核心方法：查找数据
func (g *Group) Get(key string) (ByteView, error) {
	if key == "" {
		return ByteView{}, fmt.Errorf("key is required")
	}

	// 1. 先查本地缓存
	if v, ok := g.mainCache.Get(key); ok {
		log.Println("[GeeCache] hit")
		return v.(ByteView), nil
	}

	// 2. 缓存没命中，加载数据
	return g.load(key)
}

// load 加载数据（从数据源或远程节点）
func (g *Group) load(key string) (ByteView, error) {
	// 这里可以使用 singleflight 防止缓存击穿（后面会讲）
	// 1. 尝试从远程节点获取
	if g.peers != nil {
		if peer, addr := g.peers.PickPeer(key); addr != "" {
			if value, err := g.getFromPeer(peer, g.name, key); err == nil {
				log.Println("[GeeCache] hit from peer", addr)
				return value, nil
			}
			log.Println("[GeeCache] Failed to get from peer", addr)
		}
	}

	// 2. 远程也没有，查数据库（调用 Getter）
	return g.getLocally(key)
}

// getFromPeer 从远程节点获取数据
func (g *Group) getFromPeer(peer *httpGetter, group string, key string) (ByteView, error) {

	bytes, err := peer.Get(group, key)
	log.Println("[GeeCache] get from peer", peer.baseURL, group, key)
	if err != nil {
		return ByteView{}, err
	}
	return NewByteView(bytes), nil
}

// getLocally 从本地数据源获取
func (g *Group) getLocally(key string) (ByteView, error) {
	bytes, err := g.getter.Get(key)
	if err != nil {
		return ByteView{}, err
	}

	value := NewByteView(bytes)
	// 写入缓存
	g.mainCache.Add(key, value)
	log.Println("[GeeCache] Add to cache", key)
	return value, nil
}
