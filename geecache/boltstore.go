package geecache

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

const boltRootBucket = "g"

// BoltStore 将 main 层冷数据落在单文件 BoltDB：按 group 子 bucket 存 key→value。
type BoltStore struct {
	db *bolt.DB
}

// OpenBoltStore 打开或创建 Bolt 文件；用于持久化与启动预热。
func OpenBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("bolt open %q: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(boltRootBucket))
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bolt init bucket: %w", err)
	}
	return &BoltStore{db: db}, nil
}

// Put 写入一条缓存条目（覆盖）。
func (s *BoltStore) Put(group, key string, val []byte) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucketIfNotExists([]byte(boltRootBucket))
		if err != nil {
			return err
		}
		gb, err := root.CreateBucketIfNotExists([]byte(group))
		if err != nil {
			return err
		}
		return gb.Put([]byte(key), val)
	})
}

// Delete 删除磁盘上该 key。
func (s *BoltStore) Delete(group, key string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte(boltRootBucket))
		if root == nil {
			return nil
		}
		gb := root.Bucket([]byte(group))
		if gb == nil {
			return nil
		}
		return gb.Delete([]byte(key))
	})
}

// LoadGroup 遍历某 group 下所有 KV，fn 返回 error 时停止并传递该 error。
func (s *BoltStore) LoadGroup(group string, fn func(key string, val []byte) error) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte(boltRootBucket))
		if root == nil {
			return nil
		}
		gb := root.Bucket([]byte(group))
		if gb == nil {
			return nil
		}
		return gb.ForEach(func(k, v []byte) error {
			cp := append([]byte(nil), v...)
			return fn(string(k), cp)
		})
	})
}

// Sync 刷盘（优雅退出前调用）。
func (s *BoltStore) Sync() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Sync()
}

// Close 关闭数据库。
func (s *BoltStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
