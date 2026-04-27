package geecache

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCallGroupDoDedupesSameKey(t *testing.T) {
	var g callGroup
	var calls int32
	// Leader must stay in-flight until waiters block on c.wg; otherwise a very
	// fast fn can finish and clear the slot before other goroutines observe it.
	hold := make(chan struct{})
	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			v, err := g.Do("k", func() (interface{}, error) {
				if atomic.AddInt32(&calls, 1) != 1 {
					t.Error("fn invoked more than once")
				}
				<-hold
				return 42, nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			if v.(int) != 42 {
				t.Errorf("got %v want 42", v)
			}
		}()
	}
	for atomic.LoadInt32(&calls) == 0 {
		runtime.Gosched()
	}
	time.Sleep(30 * time.Millisecond)
	close(hold)
	wg.Wait()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("fn should run once for same key; got %d", calls)
	}
}

func TestCallGroupDoDifferentKeys(t *testing.T) {
	var g callGroup
	var calls int32
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = g.Do("a", func() (interface{}, error) {
			atomic.AddInt32(&calls, 1)
			return 1, nil
		})
	}()
	go func() {
		defer wg.Done()
		_, _ = g.Do("b", func() (interface{}, error) {
			atomic.AddInt32(&calls, 1)
			return 2, nil
		})
	}()
	wg.Wait()
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("want 2 runs for different keys; got %d", calls)
	}
}

func TestCallGroupDoPropagatesError(t *testing.T) {
	var g callGroup
	want := errors.New("boom")
	_, err := g.Do("x", func() (interface{}, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v want %v", err, want)
	}
	// map entry cleared; second call runs fn again
	var second int32
	_, err2 := g.Do("x", func() (interface{}, error) {
		atomic.AddInt32(&second, 1)
		return "ok", nil
	})
	if err2 != nil {
		t.Fatal(err2)
	}
	if atomic.LoadInt32(&second) != 1 {
		t.Fatal("second Do after error should invoke fn again")
	}
}
