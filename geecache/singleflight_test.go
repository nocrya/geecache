package geecache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallGroupSameKeyDedup(t *testing.T) {
	var g callGroup
	var runs int32
	const n = 40
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := g.Do("k", func() (interface{}, error) {
				atomic.AddInt32(&runs, 1)
				time.Sleep(3 * time.Millisecond)
				return 42, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if runs != 1 {
		t.Fatalf("same-key concurrent Do: want fn runs=1, got %d", runs)
	}
}

func TestCallGroupDifferentKeysParallel(t *testing.T) {
	var g callGroup
	var runs int32
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := string(rune('a' + i))
			_, err := g.Do(key, func() (interface{}, error) {
				atomic.AddInt32(&runs, 1)
				return i, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if int(runs) != n {
		t.Fatalf("distinct keys: want %d fn runs, got %d", n, runs)
	}
}

func TestCallGroupSequentialReuse(t *testing.T) {
	var g callGroup
	var runs int32
	for i := 0; i < 3; i++ {
		v, err := g.Do("x", func() (interface{}, error) {
			atomic.AddInt32(&runs, 1)
			return i, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if v.(int) != i {
			t.Fatalf("round %d: want val %d got %v", i, i, v)
		}
	}
	if runs != 3 {
		t.Fatalf("sequential same key after completion: want 3 runs, got %d", runs)
	}
}
