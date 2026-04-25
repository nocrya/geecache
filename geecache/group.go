package geecache

import (
	"errors"
	"fmt"
	"geecache/lru"
	"geecache/metrics"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
)

var (
	// 定义一个全局的互斥锁和Map
	mu     sync.RWMutex
	Groups = make(map[string]*Group)
)

// A Group is a cache namespace and associated data loaded spread over a group of machines
type Group struct {
	name      string // 组名
	getter    Getter // 数据源回调（当缓存没命中时，调用这个函数去查数据库）
	mainCache lru.CacheStore
	hotCache  lru.CacheStore
	ttl       time.Duration // 条目存活时间；<=0 表示不启用 TTL
	ttlJitter time.Duration // >0 时在 [ttl, ttl+ttlJitter] 内均匀随机，缓解雪崩同时失效

	// 穿透：空值缓存（精确 TTL）
	negTTL   time.Duration
	negMu    sync.Mutex
	negUntil map[string]time.Time

	// 穿透：布隆记录 getter 侧 ErrNotFound；定期轮换降低「后来又有数据」时的假阳性
	missBloom      *bloom.BloomFilter
	missBloomMu    sync.Mutex
	missBloomStart time.Time
	bloomN         int
	bloomFP        float64
	bloomRotate    time.Duration

	peers        PeerPicker
	singleFlight callGroup

	// persist 可选：Getter 回源 / Set 写入 main 后落盘；main 淘汰时写入冷数据；Invalidate/Purge 删盘。
	persist *BoltStore
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

// NewGroup 创建一个组。hotBytes<=0 时不创建热点层，peer 命中会写入 mainCache。
// eviction: "lru"（默认）或 "lfu"，控制 main/hot 淘汰策略（见 lru.CacheStore）。
// ttl<=0 时不为条目设置过期；ttl>0 时 main/hot 中条目在 Get 时做惰性淘汰。
// ttlJitter>0 时实际过期时间为 ttl + uniform(0, ttlJitter)；为 0 则每条目固定 ttl。
// negTTL>0 时对 Getter 返回的 ErrNotFound 做空值缓存（Get 短路）；bloomN>0 时额外用布隆记录未命中并在轮换周期内短路 Getter（有假阳性，见 bloomRotate）。
// persist 非 nil 时启用 Bolt 冷存储；warm 为 true 时在注册进 Groups 前从 Bolt 预热 main。
func NewGroup(name string, cacheBytes int64, hotBytes int64, eviction string, ttl, ttlJitter, negTTL time.Duration, bloomN int, bloomFP float64, getter Getter, peers PeerPicker, persist *BoltStore, warm bool) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	if bloomN > 0 && bloomFP <= 0 {
		bloomFP = 0.01
	}
	slog.Info("group_created", "group", name, "eviction", eviction, "hot_bytes", hotBytes, "ttl", ttl, "ttl_jitter", ttlJitter, "neg_ttl", negTTL, "bloom_n", bloomN, "bloom_fp", bloomFP, "persist", persist != nil, "warm", warm)

	var g *Group
	onMain := func(key string, value lru.Value) {
		metrics.CacheEvictions.WithLabelValues(name, "main").Inc()
		if g != nil && g.persist != nil {
			if b := valueToPersistBytes(value); len(b) > 0 {
				if err := g.persist.Put(g.name, key, b); err != nil {
					slog.Warn("persist_put_evict", "group", name, "key", key, "err", err)
				}
			}
		}
	}
	onHot := func(key string, value lru.Value) {
		metrics.CacheEvictions.WithLabelValues(name, "hot").Inc()
	}
	main, hot := buildCacheStores(eviction, cacheBytes, hotBytes, onMain, onHot)

	g = &Group{
		name:      name,
		getter:    getter,
		ttl:       ttl,
		ttlJitter: ttlJitter,
		negTTL:    negTTL,
		negUntil:  make(map[string]time.Time),
		mainCache: main,
		peers:     peers,
		persist:   persist,
	}
	if hotBytes > 0 {
		g.hotCache = hot
	}
	if bloomN > 0 {
		g.missBloom = bloom.NewWithEstimates(uint(bloomN), bloomFP)
		g.missBloomStart = time.Now()
		g.bloomN = bloomN
		g.bloomFP = bloomFP
		g.bloomRotate = 5 * negTTL
		if g.bloomRotate <= 0 {
			g.bloomRotate = time.Minute
		}
	}

	if persist != nil && warm {
		n, err := g.warmFromBolt()
		if err != nil {
			slog.Warn("cache_warm", "group", name, "loaded", n, "err", err)
		} else {
			slog.Info("cache_warm", "group", name, "loaded", n)
		}
	}

	mu.Lock()
	Groups[name] = g
	mu.Unlock()
	return g
}

func valueToPersistBytes(v lru.Value) []byte {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case ByteView:
		return t.ByteSlice()
	case ttlEntry:
		return t.view.ByteSlice()
	default:
		return nil
	}
}

func (g *Group) warmFromBolt() (int, error) {
	if g.persist == nil {
		return 0, nil
	}
	n := 0
	err := g.persist.LoadGroup(g.name, func(key string, val []byte) error {
		g.putTier(g.mainCache, key, NewByteView(val))
		n++
		return nil
	})
	return n, err
}

func (g *Group) persistPutView(key string, v ByteView) {
	if g.persist == nil {
		return
	}
	if err := g.persist.Put(g.name, key, v.ByteSlice()); err != nil {
		slog.Warn("persist_put", "group", g.name, "key", key, "err", err)
	}
}

func (g *Group) persistDeleteKey(key string) {
	if g.persist == nil {
		return
	}
	if err := g.persist.Delete(g.name, key); err != nil {
		slog.Warn("persist_delete", "group", g.name, "key", key, "err", err)
	}
}

// GetGroup 根据名字获取组
func GetGroup(name string) *Group {
	mu.RLock()
	g := Groups[name]
	mu.RUnlock()
	return g
}

// Get 核心方法：查找数据
func (g *Group) Get(key string) (ByteView, error) {
	if key == "" {
		return ByteView{}, fmt.Errorf("key is required")
	}

	// 1. 先查本地缓存和热点缓存（含 TTL 惰性淘汰）
	if v, ok := g.peekTier(g.mainCache, key); ok {
		metrics.CacheHits.WithLabelValues(g.name).Inc()
		slog.Info("cache_hit_local", "group", g.name, "key", key)
		return v, nil
	}

	if g.hotCache != nil {
		if v, ok := g.peekTier(g.hotCache, key); ok {
			metrics.CacheHits.WithLabelValues(g.name).Inc()
			slog.Info("cache_hit_hot", "group", g.name, "key", key)
			return v, nil
		}
	}

	if g.peekNegative(key) {
		metrics.NegativeCacheHits.WithLabelValues(g.name).Inc()
		slog.Info("cache_hit_negative", "group", g.name, "key", key)
		return ByteView{}, ErrNotFound
	}

	// 2. 缓存没命中，加载数据
	return g.load(key)
}

// load 负责真正的“回源”操作：从远程节点或数据源获取数据
func (g *Group) load(key string) (ByteView, error) {
	// 使用 singleflight 防止缓存击穿
	value, err := g.singleFlight.Do(key, func() (interface{}, error) {
		// 1. 尝试从远程节点获取
		if g.peers != nil {
			if peer, addr := g.peers.PickPeer(key); addr != "" {
				if value, err := g.getFromPeer(peer, g.name, key); err == nil {
					if g.hotCache != nil {
						g.putTier(g.hotCache, key, value)
					} else {
						g.putTier(g.mainCache, key, value)
					}
					slog.Info("cache_hit_peer", "group", g.name, "key", key, "peer", addr)
					return value, nil
				}
				slog.Warn("cache_peer_miss", "group", g.name, "key", key, "peer", addr)
			}
		}

		// 2. 远程也没有，查数据库（调用 Getter）
		g.maybeRotateMissBloom()
		if g.missBloom != nil {
			g.missBloomMu.Lock()
			blocked := g.missBloom.TestString(key)
			g.missBloomMu.Unlock()
			if blocked {
				metrics.BloomMissBlocks.WithLabelValues(g.name).Inc()
				slog.Info("cache_bloom_miss_block", "group", g.name, "key", key)
				return nil, ErrNotFound
			}
		}

		value, err := g.getLocally(key)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				g.recordNotFound(key)
			}
			return nil, err
		}
		g.putTier(g.mainCache, key, value)
		g.persistPutView(key, value)
		if g.hotCache != nil {
			g.hotCache.Remove(key)
		}
		slog.Info("cache_load_getter", "group", g.name, "key", key)
		return value, nil
	})
	if err != nil {
		return ByteView{}, err
	}
	return value.(ByteView), nil

}

// Set 写入缓存：非归属节点会转发到 owner；owner 写本地 main 并向其它节点广播 Invalidate。
// 不写穿 Getter / DB（演示层缓存）。
func (g *Group) Set(key string, value []byte) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if pw, ok := g.peers.(PeerWriter); ok && pw != nil && !pw.IsOwner(key) {
		return pw.ForwardSet(g.name, key, value)
	}
	return g.setLocalAndBroadcast(key, value)
}

// PurgeKey 删除本机该 key 并向其它节点发 Invalidate（仅删缓存）。非 owner 会转发到 owner。
func (g *Group) PurgeKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if pw, ok := g.peers.(PeerWriter); ok && pw != nil && !pw.IsOwner(key) {
		return pw.ForwardPurge(g.name, key)
	}
	if err := g.InvalidateLocal(key); err != nil {
		return err
	}
	if pw, ok := g.peers.(PeerWriter); ok && pw != nil {
		if err := pw.BroadcastInvalidate(g.name, key); err != nil {
			slog.Warn("cache_purge_broadcast", "group", g.name, "key", key, "err", err)
			return err
		}
	}
	return nil
}

// InvalidateLocal 仅删除本机 main/hot 与负缓存、布隆中与 key 相关状态；不广播。
func (g *Group) InvalidateLocal(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	g.negMu.Lock()
	delete(g.negUntil, key)
	g.negMu.Unlock()
	g.resetMissBloom()
	if g.mainCache != nil {
		g.mainCache.Remove(key)
	}
	if g.hotCache != nil {
		g.hotCache.Remove(key)
	}
	g.persistDeleteKey(key)
	slog.Info("cache_invalidate_local", "group", g.name, "key", key)
	return nil
}

func (g *Group) setLocalAndBroadcast(key string, value []byte) error {
	view := NewByteView(value)
	g.negMu.Lock()
	delete(g.negUntil, key)
	g.negMu.Unlock()
	g.resetMissBloom()
	g.putTier(g.mainCache, key, view)
	g.persistPutView(key, view)
	if g.hotCache != nil {
		g.hotCache.Remove(key)
	}
	if pw, ok := g.peers.(PeerWriter); ok && pw != nil {
		if err := pw.BroadcastInvalidate(g.name, key); err != nil {
			slog.Warn("cache_broadcast_invalidate", "group", g.name, "key", key, "err", err)
			return err
		}
	}
	return nil
}

func (g *Group) resetMissBloom() {
	if g.missBloom == nil || g.bloomN <= 0 {
		return
	}
	g.missBloomMu.Lock()
	g.missBloom = bloom.NewWithEstimates(uint(g.bloomN), g.bloomFP)
	g.missBloomStart = time.Now()
	g.missBloomMu.Unlock()
}

// getFromPeer 从远程节点获取数据
func (g *Group) getFromPeer(peer PeerGetter, group string, key string) (ByteView, error) {
	start := time.Now()
	bytes, err := peer.Get(group, key)
	metrics.PeerRequestDuration.WithLabelValues(g.name, peer.Proto()).Observe(time.Since(start).Seconds())

	if err != nil {
		metrics.LoadErrors.WithLabelValues(g.name, "peer").Inc()
		slog.Error("cache_peer_get_failed", "group", g.name, "key", key, "peer", peer.Peer(), "proto", peer.Proto(), "err", err)
		return ByteView{}, err
	}
	slog.Info("cache_peer_get_ok", "group", group, "key", key, "peer", peer.Peer(), "proto", peer.Proto())
	metrics.Loads.WithLabelValues(g.name, "peer").Inc()
	return NewByteView(bytes), nil
}

// getLocally 从本地数据源获取
func (g *Group) getLocally(key string) (ByteView, error) {
	bytes, err := g.getter.Get(key)
	if err != nil {
		metrics.LoadErrors.WithLabelValues(g.name, "getter").Inc()
		slog.Warn("cache_getter_failed", "group", g.name, "key", key, "err", err)
		return ByteView{}, err
	}
	metrics.Loads.WithLabelValues(g.name, "getter").Inc()
	return NewByteView(bytes), nil
}

func buildCacheStores(eviction string, cacheBytes, hotBytes int64, onMain, onHot func(string, lru.Value)) (main lru.CacheStore, hot lru.CacheStore) {
	switch eviction {
	case "lfu", "LFU":
		main = lru.NewLFU(cacheBytes, onMain)
		if hotBytes > 0 {
			hot = lru.NewLFU(hotBytes, onHot)
		}
	default:
		main = lru.New(cacheBytes, onMain)
		if hotBytes > 0 {
			hot = lru.New(hotBytes, onHot)
		}
	}
	return main, hot
}

func (g *Group) peekTier(c lru.CacheStore, key string) (ByteView, bool) {
	if c == nil {
		return ByteView{}, false
	}
	vi, ok := c.Get(key)
	if !ok {
		return ByteView{}, false
	}
	if g.ttl <= 0 {
		return vi.(ByteView), true
	}
	e := vi.(ttlEntry)
	if time.Now().After(e.until) {
		c.Remove(key)
		g.persistDeleteKey(key)
		metrics.CacheExpired.WithLabelValues(g.name).Inc()
		return ByteView{}, false
	}
	return e.view, true
}

func (g *Group) putTier(c lru.CacheStore, key string, view ByteView) {
	if c == nil {
		return
	}
	if g.ttl > 0 {
		c.Add(key, ttlEntry{view: view, until: time.Now().Add(g.cacheTTLWithJitter())})
		return
	}
	c.Add(key, view)
}

// cacheTTLWithJitter returns ttl + uniform[0, ttlJitter]; ttlJitter<=0 returns ttl.
func (g *Group) cacheTTLWithJitter() time.Duration {
	if g.ttl <= 0 {
		return 0
	}
	if g.ttlJitter <= 0 {
		return g.ttl
	}
	return g.ttl + time.Duration(rand.Int63n(int64(g.ttlJitter)+1))
}

func (g *Group) peekNegative(key string) bool {
	if g.negTTL <= 0 {
		return false
	}
	now := time.Now()
	g.negMu.Lock()
	defer g.negMu.Unlock()
	until, ok := g.negUntil[key]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(g.negUntil, key)
		return false
	}
	return true
}

func (g *Group) recordNotFound(key string) {
	metrics.PenetrationMissRecorded.WithLabelValues(g.name).Inc()
	if g.negTTL > 0 {
		g.negMu.Lock()
		g.negUntil[key] = time.Now().Add(g.negTTL)
		g.negMu.Unlock()
	}
	if g.missBloom != nil {
		g.missBloomMu.Lock()
		g.missBloom.AddString(key)
		g.missBloomMu.Unlock()
	}
}

func (g *Group) maybeRotateMissBloom() {
	if g.missBloom == nil || g.bloomRotate <= 0 {
		return
	}
	g.missBloomMu.Lock()
	defer g.missBloomMu.Unlock()
	if time.Since(g.missBloomStart) < g.bloomRotate {
		return
	}
	g.missBloom = bloom.NewWithEstimates(uint(g.bloomN), g.bloomFP)
	g.missBloomStart = time.Now()
	slog.Info("bloom_miss_rotated", "group", g.name, "every", g.bloomRotate)
}
