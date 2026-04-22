package registry

import (
	"context"
	"log"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type EtcdRegistry struct {
	client  *clientv3.Client
	leaseID clientv3.LeaseID
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

// Register 注册服务
// key: 服务在 etcd 中的路径，例如 "/geecache/nodes/localhost:8001"
// val: 服务的值（可以是元数据 JSON，这里简单用地址）
// ttl: 租约时间（秒）
func (r *EtcdRegistry) Register(key, val string, ttl int64) error {
	// 1. 先申请租约
	lease, err := r.client.Grant(context.Background(), ttl)
	if err != nil {
		return err
	}

	// 2. 更新租约ID
	r.leaseID = lease.ID

	// 3. 注册服务（使用刚申请的租约）
	_, err = r.client.Put(context.Background(), key, val, clientv3.WithLease(lease.ID))
	if err != nil {
		return err
	}

	// 4. 启动自动续约（心跳）
	// KeepAlive 会返回一个通道，etcd 客户端会在后台自动发送心跳包
	ch, err := r.client.KeepAlive(context.Background(), lease.ID)
	if err != nil {
		return err
	}

	// 5. 监听心跳结果（防止租约意外失效）
	go func() {
		for {
			select {
			case ka, ok := <-ch:
				if !ok {
					log.Printf("registry: lease %d expired or closed", r.leaseID)
					return
				}
				log.Printf("registry: lease %d keep alive: %v", r.leaseID, ka.TTL)
			case <-time.After(5 * time.Second):
				log.Println("registry: keep alive timeout (10s no response)")
				return
			}
		}
	}()

	log.Printf("registry: server %s with lease %d registered", key, r.leaseID)
	return nil
}

// Watch 监听服务变化
// prefix: 监听的前缀，例如 "/geecache/nodes/"
// handler: 当服务列表变化时的回调函数
func (r *EtcdRegistry) Watch(prefix string, handler func([]*clientv3.Event)) {
	go func() {
		// 监听指定前缀的所有事件（PUT, DELETE）
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
