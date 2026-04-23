package geecache

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"geecache/consistenthash"
	"geecache/registry"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const defaultHTTPTimeout = 3 * time.Second

// 定义默认端口和基础路径
const (
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
	grpcClients map[string]*GRPCClient

	//注册中心
	registry *registry.EtcdRegistry
	// 是否使用 gRPC
	useGRPC bool
}

var DefaultPool *HTTPPool

func NewHTTPPool(self string, peers ...string) *HTTPPool {
	DefaultPool = &HTTPPool{
		self:        self,
		basePath:    defaultPath,
		peers:       consistenthash.New(3, nil),
		httpGetters: make(map[string]*httpGetter),
		useGRPC:     false,
	}

	DefaultPool.AddPeers(peers...)
	return DefaultPool
}

// SetRegistry 设置 etcd 注册中心
func (p *HTTPPool) SetRegistry(reg *registry.EtcdRegistry) {
	p.registry = reg
}

// RegisterWithEtcd 注册当前节点到 etcd，并监听其他节点变化
func (p *HTTPPool) RegisterWithEtcd(etcdEndpoints []string, ttl int64) error {
	const prefix = "/geecache/nodes/"

	// 1. 初始化注册中心
	reg := registry.NewEtcdRegistry(etcdEndpoints)
	p.registry = reg

	// 2. 注册当前节点
	err := reg.Register(prefix+p.self, p.self, ttl)
	if err != nil {
		return err
	}

	// 3. 获取当前所有节点并初始化
	services, err := reg.GetServices(prefix)
	if err != nil {
		return err
	}
	log.Printf("[HTTPPool] Initial peers from etcd: %v", services)
	p.Set(services...)

	// 4. 监听节点变化
	reg.Watch(prefix, func(events []*clientv3.Event) {
		for _, ev := range events {
			switch ev.Type {
			case clientv3.EventTypePut:
				peer := string(ev.Kv.Value)
				log.Printf("[HTTPPool] Peer added: %s", peer)
				p.AddPeers(peer)
			case clientv3.EventTypeDelete:
				peer := string(ev.Kv.Value)
				log.Printf("[HTTPPool] Peer removed: %s", peer)
				p.RemovePeer(peer)
			}
		}
	})

	return nil
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
	p.grpcClients = make(map[string]*GRPCClient, len(peers))
	for _, peer := range peers {
		p.httpGetters[peer] = NewHTTPGetter(peer, p.basePath, p)
		if c, err := NewGRPCClient(peer, grpcTarget(peer), p); err == nil {
			p.grpcClients[peer] = c
		} else {
			log.Printf("[HTTPPool] grpc client for %s: %v", peer, err)
		}
	}
}

func (p *HTTPPool) PickPeer(key string) (PeerGetter, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	peer := p.peers.Get(key)
	if peer == "" || peer == p.self {
		return nil, ""
	}
	if p.useGRPC {
		if c := p.grpcClients[peer]; c != nil {
			return c, peer
		}
		// gRPC 建连失败时回退到 HTTP，避免 nil 指针
	}
	return p.httpGetters[peer], peer
}

func (p *HTTPPool) AddPeers(peers ...string) {
	log.Println("[HTTPPool] AddPeers", peers)
	p.Set(peers...)
}

func (p *HTTPPool) RemovePeer(peer string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	log.Println("[HTTPPool] RemovePeer", peer)
	p.peers.Remove(peer)
	// 同时清理 httpGetters 中的对应项
	delete(p.httpGetters, peer)
	// 关闭grpc连接
	if c, ok := p.grpcClients[peer]; ok {
		_ = c.conn.Close()
		delete(p.grpcClients, peer)
	}
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

func (p *HTTPPool) UseGRPC(useGRPC bool) {
	p.mu.Lock()
	p.useGRPC = useGRPC
	p.mu.Unlock()
}

// Stop 释放资源：关闭所有 gRPC 到 peer 的连接，并撤销 etcd 租约、关闭 etcd 客户端。
// 应在停止对外监听之后或与之并发调用前，先通过 gRPC/HTTP 的 GracefulStop/Shutdown 停止接收新请求。
func (p *HTTPPool) Stop(ctx context.Context) error {
	p.mu.Lock()
	reg := p.registry
	p.registry = nil
	for _, c := range p.grpcClients {
		if c != nil && c.conn != nil {
			_ = c.conn.Close()
		}
	}
	p.grpcClients = make(map[string]*GRPCClient)
	p.httpGetters = make(map[string]*httpGetter)
	p.mu.Unlock()

	if reg != nil {
		if err := reg.RevokeAndClose(ctx); err != nil {
			return err
		}
	}
	return nil
}

type httpGetter struct {
	peer     string
	basePath string
	client   *http.Client
	remover  PeerRemover
}

func NewHTTPGetter(peer string, basePath string, remover PeerRemover) *httpGetter {
	return &httpGetter{
		peer:     peer,
		basePath: basePath,
		client:   &http.Client{Timeout: defaultHTTPTimeout},
		remover:  remover,
	}
}

// Get 向远程节点请求数据
func (h *httpGetter) Get(group string, key string) ([]byte, error) {

	u := h.peer + h.basePath + "/" + group + "/" + key

	res, err := h.client.Get(u)
	if err != nil {
		if h.remover != nil {
			h.remover.RemovePeer(h.peer)
		}
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned: %v", res.Status)
	}

	bytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %v", err)
	}

	return bytes, nil
}

func (h *httpGetter) Peer() string  { return h.peer }
func (h *httpGetter) Proto() string { return "http" }

// 确保 httpGetter 实现了 PeerGetter 接口
var _ PeerGetter = (*httpGetter)(nil)

func grpcTarget(peer string) string {
	if u, err := url.Parse(peer); err == nil && u.Host != "" {
		return u.Host // "localhost:8001"
	}
	return peer // 已经是 host:port
}
