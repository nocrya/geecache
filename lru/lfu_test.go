package lru

import (
	"testing"
)

type tinyVal string

func (v tinyVal) Len() int { return len(v) }

func TestLFUEvictsLowestFreq(t *testing.T) {
	var evicted string
	// maxBytes=6: two entries "a","b" each key1+val2=3 → full; Add "c" forces one eviction.
	f := NewLFU(6, func(k string, _ Value) { evicted = k })
	f.Add("a", tinyVal("11"))
	f.Add("b", tinyVal("11"))
	f.Get("a")
	f.Get("a") // a.freq=3
	f.Get("b") // b.freq=2 → lowest before add c
	f.Add("c", tinyVal("x"))
	if evicted != "b" {
		t.Fatalf("expected evict b (lower freq), got %q", evicted)
	}
	if _, ok := f.Get("b"); ok {
		t.Fatal("b should be gone")
	}
	if _, ok := f.Get("a"); !ok {
		t.Fatal("a should remain")
	}
}
