package geecache

import (
	"errors"
)

// ByteView 是缓存值的只读视图
type ByteView struct {
	b []byte
}

func NewByteView(b []byte) ByteView {
	return ByteView{b: cloneBytes(b)}
}

// Len 返回数据的长度
func (v ByteView) Len() int {
	return len(v.b)
}

// ByteSlice 返回数据的副本
// 保证缓存中的数据不被外部修改
func (v ByteView) ByteSlice() []byte {
	return cloneBytes(v.b)
}

// String 返回字符串形式
func (v ByteView) String() string {
	return string(v.b)
}

// 辅助函数：复制切片
func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// Equal 判断两个 ByteView 是否相等
func (v ByteView) Equal(other ByteView) bool {
	return string(v.b) == string(other.b)
}

// 定义一个标准的缓存未命中错误
var ErrNotFound = errors.New("key not found")
