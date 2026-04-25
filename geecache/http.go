package geecache

import (
	"bytes"
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

const maxSetBodyBytes = 32 << 20 // 32MiB

var _ PeerWriter = (*HTTPPool)(nil)

const defaultHTTPTimeout = 3 * time.Second

// 定义默认端口和基础路径
const (
	defaultPath = "/geecache"
	// DefaultEtcdInvalidationPrefix 阶段 4：etcd 上失效事件前缀（需与 EnableEtcdInvalidation 一致）。
	DefaultEtcdInvalidationPrefix = "/geecache/inval/"
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

	// etcd 失效传播（可选）：BroadcastInvalidate 成功后写入；Watch 收到后对非 owner 执行 InvalidateLocal
	invalPrefix string
	invalCtx    context.Context
	invalCancel context.CancelFunc
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

// EnableEtcdInvalidation 在已有 etcd registry（RegisterWithEtcd 成功）后启用：
// 1) BroadcastInvalidate 时向 etcd 写入一条失效键；2) Watch 同前缀，非 owner 节点 InvalidateLocal。
// prefix 建议以 '/' 结尾，例如 DefaultEtcdInvalidationPrefix。
func (p *HTTPPool) EnableEtcdInvalidation(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("geecache: invalidation prefix empty")
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	p.mu.Lock()
	reg := p.registry
	if reg == nil {
		p.mu.Unlock()
		return fmt.Errorf("geecache: EnableEtcdInvalidation: call RegisterWithEtcd first")
	}
	if p.invalCancel != nil {
		p.invalCancel()
		p.invalCancel = nil
	}
	p.invalPrefix = prefix
	p.invalCtx, p.invalCancel = context.WithCancel(context.Background())
	ctx := p.invalCtx
	p.mu.Unlock()

	reg.WatchPrefix(ctx, prefix, func(events []*clientv3.Event) {
		p.handleEtcdInvalidationEvents(prefix, events)
	})
	log.Printf("[HTTPPool] etcd invalidation enabled on prefix %q", prefix)
	return nil
}

func (p *HTTPPool) handleEtcdInvalidationEvents(prefix string, events []*clientv3.Event) {
	for _, ev := range events {
		if ev.Type != clientv3.EventTypePut {
			continue
		}
		full := string(ev.Kv.Key)
		gname, kname, ok := parseEtcdInvalKey(prefix, full)
		if !ok {
			continue
		}
		if p.IsOwner(kname) {
			continue
		}
		g := GetGroup(gname)
		if g == nil {
			continue
		}
		if err := g.InvalidateLocal(kname); err != nil {
			log.Printf("[HTTPPool] etcd inval InvalidateLocal: %v", err)
			continue
		}
		log.Printf("[HTTPPool] etcd inval applied group=%q key=%q", gname, kname)
	}
}

func etcdInvalKey(prefix, group, key string) string {
	return prefix + url.PathEscape(group) + "/" + url.PathEscape(key)
}

func parseEtcdInvalKey(prefix, full string) (group, key string, ok bool) {
	if !strings.HasPrefix(full, prefix) {
		return "", "", false
	}
	rel := strings.TrimPrefix(full, prefix)
	idx := strings.Index(rel, "/")
	if idx < 0 {
		return "", "", false
	}
	g, err1 := url.PathUnescape(rel[:idx])
	k, err2 := url.PathUnescape(rel[idx+1:])
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	return g, k, true
}

func (p *HTTPPool) publishInvalidationViaEtcd(group, key string) {
	p.mu.Lock()
	reg := p.registry
	prefix := p.invalPrefix
	p.mu.Unlock()
	if reg == nil || prefix == "" {
		return
	}
	ek := etcdInvalKey(prefix, group, key)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := reg.PutKV(ctx, ek, "1"); err != nil {
		log.Printf("[HTTPPool] etcd publish invalidate %q: %v", ek, err)
	}
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

// IsOwner 若该 key 由本节点持有（一致性哈希落在 self），返回 true。
func (p *HTTPPool) IsOwner(key string) bool {
	peer, _ := p.PickPeer(key)
	return peer == nil
}

// ForwardSet 将 Set 请求发到归属节点（gRPC 优先，否则 HTTP PUT）。
func (p *HTTPPool) ForwardSet(group, key string, value []byte) error {
	owner, err := p.ownerForKey(key)
	if err != nil {
		return err
	}
	p.mu.Lock()
	useGRPC := p.useGRPC
	gc := p.grpcClients[owner]
	hg := p.httpGetters[owner]
	p.mu.Unlock()
	if useGRPC && gc != nil {
		return gc.Set(group, key, value)
	}
	if hg == nil {
		return fmt.Errorf("geecache: ForwardSet: no client for owner %s", owner)
	}
	return hg.Set(group, key, value)
}

// ForwardPurge 将 Purge（删本机+失效其它副本）发到归属节点。
func (p *HTTPPool) ForwardPurge(group, key string) error {
	owner, err := p.ownerForKey(key)
	if err != nil {
		return err
	}
	p.mu.Lock()
	useGRPC := p.useGRPC
	gc := p.grpcClients[owner]
	hg := p.httpGetters[owner]
	p.mu.Unlock()
	if useGRPC && gc != nil {
		return gc.Purge(group, key)
	}
	if hg == nil {
		return fmt.Errorf("geecache: ForwardPurge: no client for owner %s", owner)
	}
	return hg.Purge(group, key)
}

// BroadcastInvalidate 向除本机外的所有 peer 发送仅失效（不删 owner 上的新值）。
func (p *HTTPPool) BroadcastInvalidate(group, key string) error {
	p.mu.Lock()
	type pair struct {
		grpc *GRPCClient
		http *httpGetter
	}
	useGRPC := p.useGRPC
	clients := make(map[string]pair)
	for peer := range p.httpGetters {
		if peer == p.self {
			continue
		}
		clients[peer] = pair{grpc: p.grpcClients[peer], http: p.httpGetters[peer]}
	}
	p.mu.Unlock()

	var firstErr error
	for peer, c := range clients {
		var err error
		if useGRPC && c.grpc != nil {
			err = c.grpc.Invalidate(group, key)
		} else if c.http != nil {
			err = c.http.Invalidate(group, key)
		} else {
			err = fmt.Errorf("no invalidate transport for peer %s", peer)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("peer %s: %w", peer, err)
		}
	}
	p.publishInvalidationViaEtcd(group, key)
	return firstErr
}

func (p *HTTPPool) ownerForKey(key string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.peers == nil || p.peers.IsEmpty() {
		return "", fmt.Errorf("geecache: no peer ring")
	}
	owner := p.peers.Get(key)
	if owner == "" || owner == p.self {
		return "", fmt.Errorf("geecache: local node owns key")
	}
	return owner, nil
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

	switch r.Method {
	case http.MethodGet:
		value, err := group.Get(key)
		if err != nil {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(value.ByteSlice())
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSetBodyBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := group.Set(key, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if r.URL.Query().Get("op") == "invalidate" {
			if err := group.InvalidateLocal(key); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := group.PurgeKey(key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
	if p.invalCancel != nil {
		p.invalCancel()
		p.invalCancel = nil
	}
	p.invalPrefix = ""
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

// Set HTTP PUT 到归属节点。
func (h *httpGetter) Set(group, key string, value []byte) error {
	u := fmt.Sprintf("%s%s/%s/%s", h.peer, h.basePath, url.PathEscape(group), url.PathEscape(key))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(value))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	res, err := h.client.Do(req)
	if err != nil {
		if h.remover != nil {
			h.remover.RemovePeer(h.peer)
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("server returned %s: %s", res.Status, string(b))
	}
	return nil
}

// Invalidate 仅失效副本：DELETE ?op=invalidate
func (h *httpGetter) Invalidate(group, key string) error {
	u := fmt.Sprintf("%s%s/%s/%s?op=invalidate", h.peer, h.basePath, url.PathEscape(group), url.PathEscape(key))
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	res, err := h.client.Do(req)
	if err != nil {
		if h.remover != nil {
			h.remover.RemovePeer(h.peer)
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", res.Status)
	}
	return nil
}

// Purge 归属节点全量清理：DELETE（无 query）
func (h *httpGetter) Purge(group, key string) error {
	u := fmt.Sprintf("%s%s/%s/%s", h.peer, h.basePath, url.PathEscape(group), url.PathEscape(key))
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	res, err := h.client.Do(req)
	if err != nil {
		if h.remover != nil {
			h.remover.RemovePeer(h.peer)
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", res.Status)
	}
	return nil
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
