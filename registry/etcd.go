package registry

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	defaultMaxBackoff = 30 * time.Second
	maintainJoinWait  = 10 * time.Second
)

type EtcdRegistry struct {
	mu      sync.Mutex
	client  *clientv3.Client
	leaseID clientv3.LeaseID

	regKey, regVal string
	regTTL         int64

	stopCtx    context.Context
	stopCancel context.CancelFunc

	wg sync.WaitGroup
}

// NewEtcdRegistry 创建注册中心实例
func NewEtcdRegistry(endpoints []string) *EtcdRegistry {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("NewEtcdRegistry: %v", err)
	}
	return &EtcdRegistry{
		client: client,
	}
}

// Register 注册服务并启动后台租约维护：KeepAlive 通道关闭或 registerOnce 失败时指数退避重试。
func (r *EtcdRegistry) Register(key, val string, ttl int64) error {
	r.mu.Lock()
	if r.stopCancel != nil {
		r.mu.Unlock()
		return fmt.Errorf("registry: already registered")
	}
	r.regKey, r.regVal, r.regTTL = key, val, ttl
	r.stopCtx, r.stopCancel = context.WithCancel(context.Background())
	ctx := r.stopCtx
	r.mu.Unlock()

	ch, err := r.registerOnce(ctx)
	if err != nil {
		r.mu.Lock()
		if r.stopCancel != nil {
			r.stopCancel()
			r.stopCancel = nil
			r.stopCtx = nil
		}
		r.mu.Unlock()
		return err
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.maintainLease(ctx, ch)
	}()

	r.mu.Lock()
	lid := r.leaseID
	r.mu.Unlock()
	log.Printf("registry: server %s with lease %d registered", key, lid)
	return nil
}

// registerOnce：Grant + Put + KeepAlive，并更新 leaseID（供 Revoke 使用）。
func (r *EtcdRegistry) registerOnce(ctx context.Context) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	r.mu.Lock()
	c := r.client
	key, val, ttl := r.regKey, r.regVal, r.regTTL
	r.mu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("registry: client closed")
	}

	lease, err := c.Grant(ctx, ttl)
	if err != nil {
		return nil, err
	}
	_, err = c.Put(ctx, key, val, clientv3.WithLease(lease.ID))
	if err != nil {
		return nil, err
	}
	ch, err := c.KeepAlive(ctx, lease.ID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.leaseID = lease.ID
	r.mu.Unlock()
	return ch, nil
}

// maintainLease 消费 KeepAlive；channel 关闭则退避后重新 registerOnce。
func (r *EtcdRegistry) maintainLease(ctx context.Context, ch <-chan *clientv3.LeaseKeepAliveResponse) {
	backoff := time.Second
	maxBackoff := defaultMaxBackoff
	cur := ch

	for ctx.Err() == nil {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-cur:
				if !ok {
					log.Printf("registry: keepalive channel closed, will re-register")
					goto retry
				}
			}
		}

	retry:
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		nch, err := r.registerOnce(ctx)
		if err != nil {
			log.Printf("registry: re-register failed: %v; next retry in %v", err, backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		cur = nch
		backoff = time.Second
		log.Println("registry: re-register ok")
	}
}

// Watch 监听服务变化
func (r *EtcdRegistry) Watch(prefix string, handler func([]*clientv3.Event)) {
	go func() {
		watchChan := r.client.Watch(context.Background(), prefix, clientv3.WithPrefix())
		for rsp := range watchChan {
			handler(rsp.Events)
		}
	}()
}

// GetServices 获取当前所有服务列表（用于初始化）
func (r *EtcdRegistry) GetServices(prefix string) ([]string, error) {
	rsp, err := r.client.Get(context.Background(), prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var services []string
	for _, kv := range rsp.Kvs {
		services = append(services, string(kv.Value))
	}
	return services, nil
}

// RevokeAndClose 先取消租约维护协程，再 Revoke + Close client。
func (r *EtcdRegistry) RevokeAndClose(ctx context.Context) error {
	r.mu.Lock()
	cancel := r.stopCancel
	r.stopCancel = nil
	r.stopCtx = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	joinWait, joinCancel := context.WithTimeout(context.Background(), maintainJoinWait)
	defer joinCancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-joinWait.Done():
		log.Printf("registry: maintain goroutine did not exit within %v", maintainJoinWait)
	case <-ctx.Done():
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return nil
	}

	if r.leaseID != 0 {
		if _, err := r.client.Revoke(ctx, r.leaseID); err != nil {
			return err
		}
		r.leaseID = 0
	}

	if err := r.client.Close(); err != nil {
		r.client = nil
		return err
	}

	r.client = nil
	return nil
}
