package lru

// CacheStore 为按字节上限的内存缓存抽象（Get / Add / Remove）。
// *Cache（LRU）、*LFU、*ARC 均实现本接口；geecache.Group 的 main/hot 使用。
type CacheStore interface {
	Get(key string) (Value, bool)
	Add(key string, value Value)
	Remove(key string) bool
}
