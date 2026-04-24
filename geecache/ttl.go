package geecache

import (
	"time"
)

// ttlEntry implements lru.Value: same byte accounting as the inner view.
type ttlEntry struct {
	view  ByteView
	until time.Time
}

func (e ttlEntry) Len() int { return e.view.Len() }
