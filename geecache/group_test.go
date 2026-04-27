package geecache

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func unregisterGroup(name string) {
	mu.Lock()
	delete(Groups, name)
	mu.Unlock()
}

func TestGetEmptyKey(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	g := NewGroup(name, 1024, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(string) ([]byte, error) {
		return nil, errors.New("no")
	}), nil, nil, false)
	if _, err := g.Get(""); err == nil {
		t.Fatal("empty key should error")
	}
}

func TestSetPurgeEmptyKey(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	g := NewGroup(name, 1024, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(string) ([]byte, error) {
		return []byte("x"), nil
	}), nil, nil, false)
	if err := g.Set("", []byte("a")); err == nil {
		t.Fatal("Set empty key")
	}
	if err := g.PurgeKey(""); err == nil {
		t.Fatal("PurgeKey empty key")
	}
	if err := g.InvalidateLocal(""); err == nil {
		t.Fatal("InvalidateLocal empty key")
	}
}

func TestGetLoadsFromGetter(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	var loads int32
	g := NewGroup(name, 4096, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(key string) ([]byte, error) {
		atomic.AddInt32(&loads, 1)
		return []byte("v-" + key), nil
	}), nil, nil, false)

	v, err := g.Get("k1")
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "v-k1" {
		t.Fatalf("got %q", v.String())
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("getter loads = %d", loads)
	}
	v2, err := g.Get("k1")
	if err != nil {
		t.Fatal(err)
	}
	if v2.String() != "v-k1" {
		t.Fatalf("second get %q", v2.String())
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("second Get should be cached; loads=%d", loads)
	}
}

func TestNegativeCache(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	var loads int32
	negTTL := 50 * time.Millisecond
	g := NewGroup(name, 4096, 0, "lru", 0, 0, negTTL, 0, 0, GetterFunc(func(string) ([]byte, error) {
		atomic.AddInt32(&loads, 1)
		return nil, ErrNotFound
	}), nil, nil, false)

	if _, err := g.Get("miss"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first: %v", err)
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("loads=%d", loads)
	}
	if _, err := g.Get("miss"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second: %v", err)
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("negative cache should skip getter; loads=%d", loads)
	}
	time.Sleep(negTTL + 20*time.Millisecond)
	if _, err := g.Get("miss"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after expiry: %v", err)
	}
	if atomic.LoadInt32(&loads) != 2 {
		t.Fatalf("after neg TTL expiry getter should run again; loads=%d", loads)
	}
}

func TestSetInvalidateLocal(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	var loads int32
	g := NewGroup(name, 4096, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(key string) ([]byte, error) {
		atomic.AddInt32(&loads, 1)
		return []byte("from-db"), nil
	}), nil, nil, false)

	if err := g.Set("x", []byte("mem")); err != nil {
		t.Fatal(err)
	}
	v, err := g.Get("x")
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "mem" {
		t.Fatalf("got %q", v.String())
	}
	if err := g.InvalidateLocal("x"); err != nil {
		t.Fatal(err)
	}
	v2, err := g.Get("x")
	if err != nil {
		t.Fatal(err)
	}
	if v2.String() != "from-db" {
		t.Fatalf("after invalidate want db; got %q", v2.String())
	}
	if atomic.LoadInt32(&loads) != 1 {
		t.Fatalf("loads=%d", loads)
	}
}

type mockPeerWriter struct {
	owner   bool
	forward int32
	bcast   int32
}

func (m *mockPeerWriter) PickPeer(string) (PeerGetter, string) { return nil, "" }
func (m *mockPeerWriter) IsOwner(string) bool                  { return m.owner }
func (m *mockPeerWriter) ForwardSet(string, string, []byte) error {
	atomic.AddInt32(&m.forward, 1)
	return nil
}
func (m *mockPeerWriter) ForwardPurge(string, string) error { return nil }
func (m *mockPeerWriter) BroadcastInvalidate(string, string) error {
	atomic.AddInt32(&m.bcast, 1)
	return nil
}

func TestSetNonOwnerForwards(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	pw := &mockPeerWriter{owner: false}
	NewGroup(name, 1024, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(string) ([]byte, error) {
		return []byte("z"), nil
	}), pw, nil, false)

	g := GetGroup(name)
	if err := g.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&pw.forward) != 1 {
		t.Fatalf("ForwardSet calls=%d", pw.forward)
	}
	if atomic.LoadInt32(&pw.bcast) != 0 {
		t.Fatal("non-owner Set should not broadcast locally")
	}
}

func TestSetOwnerBroadcasts(t *testing.T) {
	name := t.Name()
	t.Cleanup(func() { unregisterGroup(name) })
	pw := &mockPeerWriter{owner: true}
	NewGroup(name, 1024, 0, "lru", 0, 0, 0, 0, 0, GetterFunc(func(string) ([]byte, error) {
		return []byte("z"), nil
	}), pw, nil, false)

	g := GetGroup(name)
	if err := g.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&pw.bcast) != 1 {
		t.Fatalf("owner should BroadcastInvalidate; got %d", pw.bcast)
	}
	if atomic.LoadInt32(&pw.forward) != 0 {
		t.Fatal("owner should not ForwardSet")
	}
}
