package lru

import (
	"testing"
)

type testVal string

func (v testVal) Len() int { return len(v) }

func TestRemove(t *testing.T) {
	var evicted int
	c := New(100, func(key string, value Value) { evicted++ })
	c.Add("a", testVal("1"))
	c.Add("b", testVal("2"))

	if !c.Remove("a") {
		t.Fatal("expected Remove(a) true")
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be gone")
	}
	if v, ok := c.Get("b"); !ok || string(v.(testVal)) != "2" {
		t.Fatalf("b: ok=%v v=%v", ok, v)
	}
	if evicted != 0 {
		t.Fatalf("Remove should not trigger onEvicted, got %d", evicted)
	}
	if c.Remove("a") {
		t.Fatal("double Remove should return false")
	}
}
