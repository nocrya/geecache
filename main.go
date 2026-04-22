package main

import (
	"flag"
	"fmt"
	"geecache/geecache"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/soheilhy/cmux"
)

var db = map[string]string{
	"Tom":  "630",
	"Jack": "589",
	"Sam":  "567",
}

func main() {
	var port int
	var etcdAddrs string
	var useEtcd bool
	var useGRPC bool

	flag.IntVar(&port, "port", 8001, "geecache server port")
	flag.StringVar(&etcdAddrs, "etcd", "http://localhost:2379", "etcd endpoints (comma-separated)")
	flag.BoolVar(&useEtcd, "use-etcd", true, "use etcd for service discovery")
	flag.BoolVar(&useGRPC, "use-grpc", true, "use grpc for communication")
	flag.Parse()

	listenAddr := fmt.Sprintf(":%d", port)
	selfAddr := fmt.Sprintf("http://localhost:%d", port)

	// 创建 HTTPPool（先不指定 peers，从 etcd 动态获取）
	pool := geecache.NewHTTPPool(selfAddr)

	pool.UseGRPC(useGRPC)
	// 集成 etcd
	if useEtcd {
		etcdEndpoints := strings.Split(etcdAddrs, ",")
		log.Printf("[GeeCache] Connecting to etcd: %v", etcdEndpoints)
		err := pool.RegisterWithEtcd(etcdEndpoints, 10) // TTL 10秒
		if err != nil {
			log.Fatalf("[GeeCache] Failed to register with etcd: %v", err)
		}
		log.Println("[GeeCache] Registered with etcd successfully")
	} else {
		// 如果不使用 etcd，使用硬编码的 peers
		peers := []string{"http://localhost:8001", "http://localhost:8002", "http://localhost:8003"}
		pool.AddPeers(peers...)
		log.Printf("[GeeCache] Using hardcoded peers: %v", peers)
	}

	geecache.NewGroup("scores", 2<<10, geecache.GetterFunc(
		func(key string) ([]byte, error) {
			log.Println("[SlowDB] search key", key)
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("key %s not found", key)
		}), pool)

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}

	m := cmux.New(lis)

	// 处理分流请求
	grpcL := m.Match(cmux.HTTP2HeaderField("content-type", "application/grpc"))
	httpL := m.Match(cmux.HTTP1())

	// 启动gRPC服务，直接传入grpcL监听器
	go func() {
		log.Printf("[GeeCache] gRPC server listening on %s (via cmux)", listenAddr)
		grpcServer := geecache.NewGRPCServer()
		if err := grpcServer.Serve(grpcL); err != nil {
			log.Fatal(err)
		}
	}()

	// 启动HTTP服务：/metrics 走 Prometheus，其余 /geecache/... 走缓存
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/geecache") {
				geecache.DefaultPool.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		})
		log.Printf("[GeeCache] HTTP server listening on %s (via cmux), metrics at /metrics", listenAddr)
		if err := http.Serve(httpL, mux); err != nil {
			log.Fatal(err)
		}
	}()

	// 开始复用
	log.Printf("[GeeCache] is running at %s (HTTP & gRPC)", listenAddr)
	if err := m.Serve(); err != nil {
		log.Fatal(err)
	}
}
