package consistenthash

import (
	"hash/crc32"
	"sort"
	"strconv"
	"sync"
)

// Hash 函数类型，用于将数据映射到环上
type Hash func(data []byte) uint32

// Map 一致性哈希环
type Map struct {
	hash     Hash           // 哈希函数，默认使用 crc32
	replicas int            // 虚拟节点的倍数（例如 3，表示 1 个真实节点对应 3 个虚拟节点）
	keys     []int          // 哈希环上的所有虚拟节点哈希值（有序数组，用于二分查找）
	hashMap  map[int]string // 映射关系：虚拟节点的哈希值 -> 真实节点的名称
	mu       sync.RWMutex   // 读写锁，用于并发安全
}

// New 构造函数
func New(replicas int, fn Hash) *Map {
	m := &Map{
		replicas: replicas,
		hashMap:  make(map[int]string),
	}
	if fn == nil {
		// 如果没传哈希函数，默认使用 CRC32
		m.hash = crc32.ChecksumIEEE
	} else {
		m.hash = fn
	}
	return m
}

func (m *Map) IsEmpty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.keys) == 0
}

// Add 添加真实节点到哈希环
func (m *Map) Add(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		// 1. 为每个真实节点创建 replicas 个虚拟节点
		for i := 0; i < m.replicas; i++ {
			// 虚拟节点的命名规则： "编号 + 真实节点名"
			// 例如： "0NodeA", "1NodeA", "2NodeA"
			hash := int(m.hash([]byte(strconv.Itoa(i) + key)))

			// 2. 将虚拟节点的哈希值加入环
			m.keys = append(m.keys, hash)

			// 3. 建立映射：这个哈希值属于真实节点 key
			m.hashMap[hash] = key
		}
	}
	// 4. 排序！这是二分查找的前提
	sort.Ints(m.keys)
}

// Get 根据 key 选择对应的真实节点
func (m *Map) Get(key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.keys) == 0 {
		return ""
	}

	// 1. 计算 key 的哈希值
	hash := int(m.hash([]byte(key)))

	// 2. 二分查找
	// 在 keys 数组中，找到第一个 >= hash 的位置
	// 这对应于在环上顺时针寻找遇到的第一个节点
	idx := sort.Search(len(m.keys), func(i int) bool {
		return m.keys[i] >= hash
	})

	// 3. 返回对应的真实节点
	// 如果 idx == len(keys)，说明 key 的哈希值比环上所有节点都大
	// 根据环的特性，应该回到头部（取模运算），即选择 keys[0]
	return m.hashMap[m.keys[idx%len(m.keys)]]
}

// 添加Remove方法
func (m *Map) Remove(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	newKeys := make([]int, 0, len(m.keys))
	for _, hash := range m.keys {
		if m.hashMap[hash] != key {
			newKeys = append(newKeys, hash)
		}
	}
	m.keys = newKeys

	//清理一下映射关系
	for hash, node := range m.hashMap {
		if node == key {
			delete(m.hashMap, hash)
		}
	}
}
