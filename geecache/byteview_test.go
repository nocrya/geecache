package geecache

import (
	"testing"
)

func TestNewByteViewCopiesInput(t *testing.T) {
	src := []byte("hello")
	v := NewByteView(src)
	src[0] = 'x'
	if v.String() != "hello" {
		t.Fatalf("ByteView should not alias input slice; got %q", v.String())
	}
}

func TestByteViewLenStringEqual(t *testing.T) {
	v := NewByteView([]byte("abc"))
	if v.Len() != 3 {
		t.Fatalf("Len: got %d want 3", v.Len())
	}
	if v.String() != "abc" {
		t.Fatalf("String: got %q", v.String())
	}
	other := NewByteView([]byte("abc"))
	if !v.Equal(other) {
		t.Fatal("Equal should be true for same bytes")
	}
	if v.Equal(NewByteView([]byte("ab"))) {
		t.Fatal("Equal should be false for different lengths")
	}
}

func TestByteViewByteSliceIsCopy(t *testing.T) {
	v := NewByteView([]byte("z"))
	b := v.ByteSlice()
	b[0] = 'y'
	if v.String() != "z" {
		t.Fatalf("mutating ByteSlice should not affect view; got %q", v.String())
	}
}
