package lru

import (
	"fmt"
	"testing"
)

type benchV struct{ n int }

func (b benchV) Len() int { return b.n }

func benchFill(c CacheStore, n int, valSize int) {
	v := benchV{n: valSize}
	for i := range n {
		c.Add(fmt.Sprintf("k%d", i), v)
	}
}

func BenchmarkCacheStore_LRU_Get(b *testing.B) {
	c := New(1<<20, nil)
	benchFill(c, 5000, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(fmt.Sprintf("k%d", i%5000))
	}
}

func BenchmarkCacheStore_LFU_Get(b *testing.B) {
	f := NewLFU(1<<20, nil)
	benchFill(f, 5000, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Get(fmt.Sprintf("k%d", i%5000))
	}
}
