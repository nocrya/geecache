package geecache

import (
	"fmt"
	"geecache/consistenthash"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
)

// 定义默认端口和基础路径
const (
	defaultPort = 8081
	defaultPath = "/geecache"
)

type HTTPPool struct {
	//当前节点的地址
	self string
	//基础路径，用于区分是缓存请求还是其他请求
	basePath string
	//互斥锁，用于保护peers
	mu sync.Mutex
	//记录其他所有节点
	peers       *consistenthash.Map
	httpGetters map[string]*httpGetter
}

var DefaultPool *HTTPPool

func NewHTTPPool(self string, peers ...string) *HTTPPool {
	DefaultPool = &HTTPPool{
		self:        self,
		basePath:    defaultPath,
		peers:       consistenthash.New(3, nil),
		httpGetters: make(map[string]*httpGetter),
	}

	DefaultPool.AddPeers(peers...)
	return DefaultPool
}

// Set 设置其他节点的地址，用于从其他节点获取数据
func (p *HTTPPool) Set(peers ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 1. 初始化一致性哈希环
	// 这里的 3 是虚拟节点倍数，可以根据实际情况调整
	p.peers = consistenthash.New(3, nil)
	p.peers.Add(peers...)

	// 2. 为每个节点创建一个 httpGetter 客户端
	p.httpGetters = make(map[string]*httpGetter, len(peers))
	for _, peer := range peers {
		p.httpGetters[peer] = &httpGetter{baseURL: peer + p.basePath, remover: p}
	}
}

func (p *HTTPPool) PickPeer(key string) (*httpGetter, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 1. 使用一致性哈希找到 key 应该在哪个节点
	if peer := p.peers.Get(key); peer != "" && peer != p.self {
		log.Printf("[GeeCache] Pick peer %s for key %s", peer, key)
		// 2. 返回该节点的客户端
		return p.httpGetters[peer], peer
	}
	return nil, ""
}

func (p *HTTPPool) AddPeers(peers ...string) {
	log.Println("[GeeCache] AddPeers", peers)
	p.Set(peers...)
}

func (p *HTTPPool) RemovePeer(peer string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	log.Println("[GeeCache] RemovePeer", peer)
	p.peers.Remove(peer)
}

func (p *HTTPPool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//检查路径前缀
	if !strings.HasPrefix(r.URL.Path, p.basePath) {
		panic("HTTPPool serving unexpect path: " + r.URL.Path)
	}

	//截取路径：/geecache/<groupname>/<key>
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, p.basePath), "/")
	if len(parts) != 3 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	//获取groupname
	groupname := parts[1]
	//获取key
	key := parts[2]

	group := GetGroup(groupname)

	if group == nil {
		http.Error(w, "group not found", http.StatusNotFound)
		return
	}

	//获取value
	value, err := group.Get(key)
	if err != nil {
		http.Error(w, "key not found", http.StatusNotFound)
		return
	}

	//返回value
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(value.ByteSlice())
}

type httpGetter struct {
	baseURL string
	remover PeerRemover
}

// Get 向远程节点请求数据
// 传的key格式只为key，group待添加
func (h *httpGetter) Get(group string, key string) ([]byte, error) {

	url := h.baseURL + "/" + group + "/" + key

	res, err := http.Get(url)
	if err != nil {
		if h.remover != nil {
			h.remover.RemovePeer(h.baseURL)
		}
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned: %v", res.Status)
	}

	// 读取响应体
	bytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %v", err)
	}

	return bytes, nil
}

// 确保 httpGetter 实现了 PeerGetter 接口
var _ PeerGetter = (*httpGetter)(nil)
