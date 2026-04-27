package lru

import (
	"testing"
)

type bytesVal struct{ b []byte }

func (v bytesVal) Len() int { return len(v.b) }

func TestLRUGetPromotes(t *testing.T) {
	// Single-char keys + 10-byte values: ~11 bytes each; cap 30 fits two entries;
	// after Get("a"), a is MRU and b is LRU; adding c evicts b.
	c := New(30, nil)
	c.Add("a", bytesVal{make([]byte, 10)})
	c.Add("b", bytesVal{make([]byte, 10)})
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should exist")
	}
	c.Add("c", bytesVal{make([]byte, 10)})
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should remain")
	}
}

func TestLRUOnEvictedOnlyOnCapacity(t *testing.T) {
	var evicted []string
	c := New(30, func(key string, _ Value) {
		evicted = append(evicted, key)
	})
	c.Add("x", bytesVal{make([]byte, 10)})
	c.Add("y", bytesVal{make([]byte, 10)})
	if len(evicted) != 0 {
		t.Fatalf("no eviction yet; got %v", evicted)
	}
	c.Add("z", bytesVal{make([]byte, 10)})
	if len(evicted) < 1 {
		t.Fatal("expected at least one eviction")
	}
	c.Remove("z")
	if len(evicted) != 1 {
		t.Fatalf("Remove should not trigger onEvicted; evicted=%v", evicted)
	}
}

func TestLRURemove(t *testing.T) {
	c := New(100, nil)
	c.Add("k", bytesVal{[]byte("v")})
	if !c.Remove("k") {
		t.Fatal("Remove existing key")
	}
	if c.Remove("k") {
		t.Fatal("second Remove false")
	}
	if _, ok := c.Get("k"); ok {
		t.Fatal("key gone")
	}
}
