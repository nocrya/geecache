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

	flag.IntVar(&port, "port", 8001, "geecache server port")
	flag.StringVar(&etcdAddrs, "etcd", "http://localhost:2379", "etcd endpoints (comma-separated)")
	flag.BoolVar(&useEtcd, "use-etcd", true, "use etcd for service discovery")
	flag.BoolVar(&useGRPC, "use-grpc", true, "use grpc for communication")
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
