package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"geecache/geecache"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	var cacheTTL time.Duration
	var cacheTTLJitter time.Duration
	var negTTL time.Duration
	var bloomN int
	var bloomFP float64
	var eviction string
	var etcdInval bool
	var persistPath string
	var warmOnStart bool
	var warmKeys string

	flag.IntVar(&port, "port", 8001, "geecache server port")
	flag.StringVar(&etcdAddrs, "etcd", "http://localhost:2379", "etcd endpoints (comma-separated)")
	flag.BoolVar(&useEtcd, "use-etcd", true, "use etcd for service discovery")
	flag.BoolVar(&useGRPC, "use-grpc", true, "use grpc for communication")
	flag.DurationVar(&cacheTTL, "cache-ttl", 0, "LRU entry TTL (0 = no expiry)")
	flag.DurationVar(&cacheTTLJitter, "cache-ttl-jitter", 0, "extra random TTL per entry uniform [0,jitter] (0 = fixed ttl)")
	flag.DurationVar(&negTTL, "neg-ttl", 0, "TTL for negative cache of ErrNotFound from getter (0 = off)")
	flag.IntVar(&bloomN, "bloom-n", 0, "Bloom filter estimated keys for miss-set (0 = off)")
	flag.Float64Var(&bloomFP, "bloom-fp", 0.01, "Bloom false-positive rate when bloom-n > 0")
	flag.StringVar(&eviction, "eviction", "lru", "main/hot eviction policy: lru | lfu")
	flag.BoolVar(&etcdInval, "etcd-inval", true, "when use-etcd, publish/listen cache invalidations on etcd (phase 4)")
	flag.StringVar(&persistPath, "persist-path", "", "BoltDB file path for cold cache (empty=off)")
	flag.BoolVar(&warmOnStart, "warm", true, "when persist-path set, load bolt entries into main on startup")
	flag.StringVar(&warmKeys, "warm-keys", "", "comma-separated keys to load via Getter after startup (preheat from backing store)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

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
		if etcdInval {
			if err := pool.EnableEtcdInvalidation(geecache.DefaultEtcdInvalidationPrefix); err != nil {
				log.Printf("[GeeCache] etcd invalidation not started: %v", err)
			}
		}
	} else {
		// 如果不使用 etcd，使用硬编码的 peers
		peers := []string{"http://localhost:8001", "http://localhost:8002", "http://localhost:8003"}
		pool.AddPeers(peers...)
		log.Printf("[GeeCache] Using hardcoded peers: %v", peers)
	}

	var boltStore *geecache.BoltStore
	if persistPath != "" {
		var err error
		boltStore, err = geecache.OpenBoltStore(persistPath)
		if err != nil {
			log.Fatalf("[GeeCache] persist-path: %v", err)
		}
		log.Printf("[GeeCache] Bolt persist enabled: %s", persistPath)
	}

	scores := geecache.NewGroup("scores", 2<<10, 2<<10, eviction, cacheTTL, cacheTTLJitter, negTTL, bloomN, bloomFP, geecache.GetterFunc(
		func(key string) ([]byte, error) {
			log.Println("[SlowDB] search key", key)
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("%w: %s", geecache.ErrNotFound, key)
		}), pool, boltStore, warmOnStart && persistPath != "")

	if warmKeys != "" {
		for _, k := range strings.Split(warmKeys, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, err := scores.Get(k); err != nil {
				log.Printf("[GeeCache] warm-keys %q: %v", k, err)
			} else {
				log.Printf("[GeeCache] warm-keys loaded %q", k)
			}
		}
	}

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}

	m := cmux.New(lis)

	grpcL := m.Match(cmux.HTTP2HeaderField("content-type", "application/grpc"))
	httpL := m.Match(cmux.HTTP1())

	grpcServer := geecache.NewGRPCServer()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	// 标准 pprof（与 import _ "net/http/pprof" 等价，但挂在本 mux 上）；勿对公网暴露。
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/geecache") {
			geecache.DefaultPool.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("[GeeCache] gRPC server listening on %s (via cmux)", listenAddr)
		if err := grpcServer.Serve(grpcL); err != nil {
			log.Printf("[GeeCache] gRPC serve: %v", err)
		}
	}()

	go func() {
		log.Printf("[GeeCache] HTTP server listening on %s (via cmux); /metrics /debug/pprof/", listenAddr)
		if err := httpServer.Serve(httpL); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[GeeCache] HTTP serve: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[GeeCache] received %v, shutting down...", sig)

		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		grpcServer.GracefulStop()

		if err := httpServer.Shutdown(shutCtx); err != nil {
			log.Printf("[GeeCache] HTTP shutdown: %v", err)
		}

		if err := pool.Stop(shutCtx); err != nil {
			log.Printf("[GeeCache] pool stop: %v", err)
		}

		if boltStore != nil {
			if err := boltStore.Sync(); err != nil {
				log.Printf("[GeeCache] bolt sync: %v", err)
			}
			if err := boltStore.Close(); err != nil {
				log.Printf("[GeeCache] bolt close: %v", err)
			}
		}

		if err := lis.Close(); err != nil {
			log.Printf("[GeeCache] listener close: %v", err)
		}
	}()

	log.Printf("[GeeCache] is running at %s (HTTP & gRPC)", listenAddr)
	if err := m.Serve(); err != nil {
		log.Printf("[GeeCache] cmux: %v", err)
	}
	log.Println("[GeeCache] bye")
}
