package geecache

import "sync"

// call 代表正在进行或已完成的调用
type call struct {
	wg  sync.WaitGroup // 用于等待调用完成
	val interface{}    // 调用结果
	err error          // 调用错误
}

// callGroup 管理不同 key 的调用
type callGroup struct {
	mu sync.Mutex       // 保护 m
	m  map[string]*call // key -> call
}

// Do 执行并返回给定函数的结果，确保同一时间只有一个相同 key 的请求在执行
// 如果有重复的 key，重复的调用者会等待第一个调用完成并获得相同的结果
func (g *callGroup) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	// 检查是否已有相同 key 的请求在进行
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		// 已有请求，等待结果
		c.wg.Wait()
		return c.val, c.err
	}

	// 没有相同 key 的请求，创建一个新的 call
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	// 执行函数
	c.val, c.err = fn()
	// 通知等待的 goroutine
	c.wg.Done()

	// 从 map 中移除这个 key，以便后续请求可以重新处理
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}
