package main

import (
	"context"
	"crypto/tls"
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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"
)

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
	var peersCSV string
	var backingDir string
	var peerAuthToken string

	flag.IntVar(&port, "port", 8001, "geecache server port")
	flag.StringVar(&peersCSV, "peers", "http://localhost:8001,http://localhost:8002,http://localhost:8003", "when use-etcd=false: comma-separated peer base URLs (must include self)")
	flag.StringVar(&backingDir, "backing-dir", "", "Getter reads one file per key from this directory; empty uses ./data/<port>/backing/")
	flag.StringVar(&etcdAddrs, "etcd", "http://localhost:2379", "etcd endpoints (comma-separated)")
	flag.BoolVar(&useEtcd, "use-etcd", true, "use etcd for service discovery")
	flag.BoolVar(&useGRPC, "use-grpc", true, "use grpc for communication")
	flag.DurationVar(&cacheTTL, "cache-ttl", 0, "LRU entry TTL (0 = no expiry)")
	flag.DurationVar(&cacheTTLJitter, "cache-ttl-jitter", 0, "extra random TTL per entry uniform [0,jitter] (0 = fixed ttl)")
	flag.DurationVar(&negTTL, "neg-ttl", 0, "TTL for negative cache of ErrNotFound from getter (0 = off)")
	flag.IntVar(&bloomN, "bloom-n", 0, "Bloom filter estimated keys for miss-set (0 = off)")
	flag.Float64Var(&bloomFP, "bloom-fp", 0.01, "Bloom false-positive rate when bloom-n > 0")
	flag.StringVar(&eviction, "eviction", "lru", "main/hot eviction policy: lru | lfu | arc")
	flag.BoolVar(&etcdInval, "etcd-inval", true, "when use-etcd, publish/listen cache invalidations on etcd (phase 4)")
	flag.StringVar(&persistPath, "persist-path", "", "BoltDB file path for cold cache (empty=off)")
	flag.BoolVar(&warmOnStart, "warm", false, "when persist-path set, load bolt entries into main on startup")
	flag.StringVar(&warmKeys, "warm-keys", "", "comma-separated keys to load via Getter after startup (preheat from backing store)")
	flag.StringVar(&peerAuthToken, "peer-auth-token", "", "non-empty: require same token on peer HTTP header "+geecache.PeerAuthHTTPHeader+" and gRPC metadata x-geecache-peer-token for /geecache and CacheService (set before peers load)")
	var peerMtlsListen string
	var peerMtlsCert, peerMtlsKey, peerMtlsClientCA string
	var peerMtlsServerCA, peerMtlsClientCert, peerMtlsClientKey string
	var peerBaseURL string
	flag.StringVar(&peerMtlsListen, "peer-mtls-listen", "", "if set: separate listener for peer /geecache + gRPC with mTLS (plain -port has no /geecache)")
	flag.StringVar(&peerMtlsCert, "peer-mtls-cert", "", "peer mTLS: server cert PEM (full chain)")
	flag.StringVar(&peerMtlsKey, "peer-mtls-key", "", "peer mTLS: server key PEM")
	flag.StringVar(&peerMtlsClientCA, "peer-mtls-client-ca", "", "peer mTLS: CA PEM to verify peer client certificates")
	flag.StringVar(&peerMtlsServerCA, "peer-mtls-server-ca", "", "peer mTLS outbound: CA PEM to verify peer servers")
	flag.StringVar(&peerMtlsClientCert, "peer-mtls-client-cert", "", "peer mTLS outbound: client cert PEM for dialing peers")
	flag.StringVar(&peerMtlsClientKey, "peer-mtls-client-key", "", "peer mTLS outbound: client key PEM")
	flag.StringVar(&peerBaseURL, "peer-base-url", "", "this node's https base URL on peer-mtls-listen (e.g. https://127.0.0.1:8443); must match -peers/etcd value for self; required when peer-mtls-listen is set")
	flag.Parse()

	peerMTLS := strings.TrimSpace(peerMtlsListen) != ""
	if peerMTLS {
		if strings.TrimSpace(peerBaseURL) == "" {
			log.Fatal("[GeeCache] -peer-base-url is required when -peer-mtls-listen is set")
		}
		if !strings.HasPrefix(strings.TrimSpace(peerBaseURL), "https://") {
			log.Fatal("[GeeCache] -peer-base-url must start with https://")
		}
		for _, pair := range []struct{ path, name string }{
			{peerMtlsCert, "-peer-mtls-cert"},
			{peerMtlsKey, "-peer-mtls-key"},
			{peerMtlsClientCA, "-peer-mtls-client-ca"},
			{peerMtlsServerCA, "-peer-mtls-server-ca"},
			{peerMtlsClientCert, "-peer-mtls-client-cert"},
			{peerMtlsClientKey, "-peer-mtls-client-key"},
		} {
			if strings.TrimSpace(pair.path) == "" {
				log.Fatalf("[GeeCache] %s is required when -peer-mtls-listen is set", pair.name)
			}
		}
	} else {
		if strings.TrimSpace(peerMtlsCert) != "" || strings.TrimSpace(peerMtlsKey) != "" ||
			strings.TrimSpace(peerMtlsClientCA) != "" || strings.TrimSpace(peerMtlsServerCA) != "" ||
			strings.TrimSpace(peerMtlsClientCert) != "" || strings.TrimSpace(peerMtlsClientKey) != "" ||
			strings.TrimSpace(peerBaseURL) != "" {
			log.Fatal("[GeeCache] peer TLS file flags and -peer-base-url are only valid with -peer-mtls-listen")
		}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	bd := backingDir
	if bd == "" {
		bd = filepath.Join("data", strconv.Itoa(port), "backing")
	}
	if err := os.MkdirAll(bd, 0o755); err != nil {
		log.Fatalf("[GeeCache] backing-dir mkdir: %v", err)
	}

	listenAddr := fmt.Sprintf(":%d", port)
	selfAddr := fmt.Sprintf("http://localhost:%d", port)
	if peerMTLS {
		selfAddr = strings.TrimSpace(peerBaseURL)
	}

	// 创建 HTTPPool（先不指定 peers，从 etcd 动态获取）
	pool := geecache.NewHTTPPool(selfAddr)
	if t := strings.TrimSpace(peerAuthToken); t != "" {
		pool.SetPeerAuthToken(t)
	}
	if peerMTLS {
		clientTLS, err := geecache.LoadPeerClientTLS(peerMtlsServerCA, peerMtlsClientCert, peerMtlsClientKey)
		if err != nil {
			log.Fatalf("[GeeCache] peer client TLS: %v", err)
		}
		pool.SetPeerClientTLS(clientTLS)
	}

	pool.UseGRPC(useGRPC)
	if !useEtcd {
		var peers []string
		for _, s := range strings.Split(peersCSV, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				peers = append(peers, s)
			}
		}
		if len(peers) == 0 {
			log.Fatal("[GeeCache] use-etcd=false requires at least one URL in -peers")
		}
		if err := pool.AddPeers(peers...); err != nil {
			log.Fatalf("[GeeCache] AddPeers: %v", err)
		}
		log.Printf("[GeeCache] static peers from -peers: %v", peers)
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

	scores := geecache.NewGroup("scores", 2<<10, 2<<10, eviction, cacheTTL, cacheTTLJitter, negTTL, bloomN, bloomFP, newBackingFileGetter(bd), pool, boltStore, warmOnStart && persistPath != "")

	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}

	m := cmux.New(lis)

	var plainGRPC *grpc.Server
	var httpL net.Listener
	if peerMTLS {
		anyL := m.Match(cmux.Any())
		go func() {
			for {
				c, err := anyL.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		httpL = m.Match(cmux.HTTP1())
	} else {
		grpcL := m.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
		httpL = m.Match(cmux.HTTP1())
		plainGRPC = geecache.NewGRPCServer(strings.TrimSpace(peerAuthToken), nil)
		go func() {
			log.Printf("[GeeCache] gRPC server listening on %s (via cmux)", listenAddr)
			if err := plainGRPC.Serve(grpcL); err != nil {
				log.Printf("[GeeCache] gRPC serve: %v", err)
			}
		}()
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// 标准 pprof（与 import _ "net/http/pprof" 等价，但挂在本 mux 上）；勿对公网暴露。
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if peerMTLS {
			http.NotFound(w, r)
			return
		}
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
		tag := "/metrics /healthz /debug/pprof/"
		if peerMTLS {
			tag += " (peer /geecache on -peer-mtls-listen)"
		} else {
			tag += "/geecache/"
		}
		log.Printf("[GeeCache] HTTP server listening on %s (via cmux); %s", listenAddr, tag)
		if err := httpServer.Serve(httpL); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[GeeCache] HTTP serve: %v", err)
		}
	}()

	var (
		lisPeer        net.Listener
		peerMux        cmux.CMux
		peerGRPC       *grpc.Server
		httpPeerServer *http.Server
	)
	if peerMTLS {
		srvTLS, err := geecache.LoadPeerServerTLS(peerMtlsCert, peerMtlsKey, peerMtlsClientCA)
		if err != nil {
			log.Fatalf("[GeeCache] peer server TLS: %v", err)
		}
		lisPeer, err = net.Listen("tcp", strings.TrimSpace(peerMtlsListen))
		if err != nil {
			log.Fatal(err)
		}
		tlsInner := tls.NewListener(lisPeer, srvTLS)
		peerMux = cmux.New(tlsInner)

		anyPeer := peerMux.Match(cmux.Any())
		go func() {
			for {
				c, err := anyPeer.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()

		grpcPeerL := peerMux.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
		httpPeerL := peerMux.Match(cmux.HTTP1())

		// TLS 已在 tls.Listener 完成；此处 gRPC 仍走 h2 明文帧（与 h2c 分支一致），勿再套一层 grpc credentials.NewTLS。
		peerGRPC = geecache.NewGRPCServer(strings.TrimSpace(peerAuthToken), nil)

		muxPeer := http.NewServeMux()
		muxPeer.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/geecache") {
				geecache.DefaultPool.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		})
		httpPeerServer = &http.Server{
			Handler:           muxPeer,
			ReadHeaderTimeout: 10 * time.Second,
		}

		go func() {
			log.Printf("[GeeCache] peer mTLS gRPC on %s (via cmux)", lisPeer.Addr().String())
			if err := peerGRPC.Serve(grpcPeerL); err != nil {
				log.Printf("[GeeCache] peer gRPC serve: %v", err)
			}
		}()
		go func() {
			log.Printf("[GeeCache] peer mTLS HTTP on %s; /geecache/", lisPeer.Addr().String())
			if err := httpPeerServer.Serve(httpPeerL); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("[GeeCache] peer HTTP serve: %v", err)
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[GeeCache] received %v, shutting down...", sig)

		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if peerGRPC != nil {
			peerGRPC.GracefulStop()
		}
		if plainGRPC != nil {
			plainGRPC.GracefulStop()
		}

		if httpPeerServer != nil {
			if err := httpPeerServer.Shutdown(shutCtx); err != nil {
				log.Printf("[GeeCache] peer HTTP shutdown: %v", err)
			}
		}
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

		if lisPeer != nil {
			if err := lisPeer.Close(); err != nil {
				log.Printf("[GeeCache] peer listener close: %v", err)
			}
		}
		if err := lis.Close(); err != nil {
			log.Printf("[GeeCache] listener close: %v", err)
		}
	}()

	serveCh := make(chan error, 2)
	go func() {
		serveCh <- m.Serve()
	}()
	nWait := 1
	if peerMTLS {
		nWait = 2
		go func() {
			serveCh <- peerMux.Serve()
		}()
	}
	// 让 cmux 先进入 Accept，再向 etcd 宣告本节点，避免其它实例 gRPC 连上「尚未就绪」的端口。
	time.Sleep(50 * time.Millisecond)

	if useEtcd {
		etcdEndpoints := strings.Split(etcdAddrs, ",")
		log.Printf("[GeeCache] Connecting to etcd: %v", etcdEndpoints)
		if err := pool.RegisterWithEtcd(etcdEndpoints, 10); err != nil {
			log.Fatalf("[GeeCache] Failed to register with etcd: %v", err)
		}
		log.Println("[GeeCache] Registered with etcd successfully")
		if etcdInval {
			if err := pool.EnableEtcdInvalidation(geecache.DefaultEtcdInvalidationPrefix); err != nil {
				log.Printf("[GeeCache] etcd invalidation not started: %v", err)
			}
		}
	}

	// warm-keys：在静态 AddPeers 或 etcd RegisterWithEtcd 之后执行，保证环上已有 peer（etcd 模式）。
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

	log.Printf("[GeeCache] is running at %s", listenAddr)
	if peerMTLS {
		log.Printf("[GeeCache] peer mTLS at %s", lisPeer.Addr().String())
	}
	for i := 0; i < nWait; i++ {
		if err := <-serveCh; err != nil {
			log.Printf("[GeeCache] cmux: %v", err)
		}
	}
	log.Println("[GeeCache] bye")
}

// newBackingFileGetter 从目录按「文件名 = key」读字节；不存在则 ErrNotFound（可走负缓存）。
// key 不允许含路径分隔符或 ".."，避免逃出 backing 目录。
func newBackingFileGetter(dir string) geecache.GetterFunc {
	return func(key string) ([]byte, error) {
		slog.Info("getter_backing_file", "key", key, "dir", dir)
		name, err := safeBackingKeyFile(key)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w: %s", geecache.ErrNotFound, key)
			}
			return nil, fmt.Errorf("read backing %q: %w", path, err)
		}
		return b, nil
	}
}

func safeBackingKeyFile(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("%w", geecache.ErrNotFound)
	}
	if strings.Contains(key, "..") || strings.ContainsAny(key, `/\:`) {
		return "", fmt.Errorf("%w: invalid key %q", geecache.ErrNotFound, key)
	}
	if filepath.Base(key) != key {
		return "", fmt.Errorf("%w: invalid key %q", geecache.ErrNotFound, key)
	}
	return key, nil
}
