package lru

// CacheStore is a byte-bounded in-memory cache with Get / Add / Remove.
// LRU (*Cache) and LFU (*LFU) satisfy it; geecache.Group uses it for main/hot tiers.
type CacheStore interface {
	Get(key string) (Value, bool)
	Add(key string, value Value)
	Remove(key string) bool
}
