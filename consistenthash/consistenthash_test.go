package consistenthash

import (
	"strconv"
	"testing"
)

func TestMapEmptyGet(t *testing.T) {
	m := New(3, nil)
	if !m.IsEmpty() {
		t.Fatal("new map should be empty")
	}
	if got := m.Get("any"); got != "" {
		t.Fatalf("Get on empty ring: got %q want empty", got)
	}
}

func TestMapAddGetPick(t *testing.T) {
	m := New(2, nil)
	m.Add("node-a", "node-b")
	if m.IsEmpty() {
		t.Fatal("after Add, not empty")
	}
	seen := map[string]bool{"node-a": false, "node-b": false}
	for i := 0; i < 10_000; i++ {
		seen[m.Get(strconv.Itoa(i))] = true
		if seen["node-a"] && seen["node-b"] {
			return
		}
	}
	t.Fatalf("both nodes should be chosen for some keys; seen=%v", seen)
}

func TestMapRemove(t *testing.T) {
	m := New(1, nil)
	m.Add("only")
	before := m.Get("k")
	if before != "only" {
		t.Fatalf("unexpected peer %q", before)
	}
	m.Remove("only")
	if !m.IsEmpty() {
		t.Fatal("after Remove only node, ring empty")
	}
	if m.Get("k") != "" {
		t.Fatal("Get on empty after Remove")
	}
}
