package geecache

import (
	"fmt"
	"geecache/lru"
	"log"
	"sync"
	"testing"
)

// 模拟数据源
var db = map[string]string{
	"Tom":  "630",
	"Jack": "589",
	"Sam":  "567",
}

func TestGroupGet(t *testing.T) {
	loadCounts := make(map[string]int, len(db))

	// 创建一个 Group，设置最大缓存条目数为 3
	group := NewGroup("scores", 3, GetterFunc(
		func(key string) ([]byte, error) {
			log.Printf("从数据源获取 %s", key)
			// 记录加载次数，用于验证 singleflight 是否生效
			loadCounts[key]++
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("%s not found", key)
		}), nil)

	// 1. 基础功能测试
	for k, v := range db {
		if view, err := group.Get(k); err != nil || view.String() != v {
			t.Fatalf("failed to get value of %s, want %s, got %v", k, v, view)
		}
	}

	// 2. 并发测试 (模拟缓存击穿)
	const n = 10 // 并发数量
	var wg sync.WaitGroup
	// 重置计数
	loadCounts["Sam"] = 0

	// 同时请求一个不存在的键（或者清空缓存后请求存在的键）
	// 这里我们请求 "Sam"，假设它不在缓存中（或者我们想测试并发回源）

	// 为了测试并发回源，我们需要确保缓存是空的，或者故意让多个 Goroutine 同时 miss
	// 由于 Group 内部有缓存，我们需要测试的是：当缓存未命中时，并发请求是否只触发一次加载

	// 这里的逻辑是：虽然调用了 10 次 Get("Sam")，但由于 singleflight，
	// 底层的 Getter 应该只被调用一次。

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// 请求 "Sam"
			if _, err := group.Get("Sam"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if loadCounts["Sam"] != 1 {
		t.Errorf("expected 1 load, got %d", loadCounts["Sam"])
	} else {
		log.Printf("并发测试通过：10个并发请求只触发了%d次数据源加载", loadCounts["Sam"])
	}
}

// 测试 LRU 的并发安全性
func TestLRUConcurrency(t *testing.T) {
	cache := lru.New(10, nil) // 容量为 10
	var wg sync.WaitGroup
	n := 100

	wg.Add(n * 2)

	// 并发写入
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			cache.Add(fmt.Sprintf("key%d", i), NewByteView([]byte(fmt.Sprintf("val%d", i))))
		}(i)
	}

	// 并发读取
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			cache.Get(fmt.Sprintf("key%d", i))
		}(i)
	}

	wg.Wait()
	t.Log("LRU 并发读写测试通过")
}
