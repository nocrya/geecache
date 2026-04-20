package main

import (
	"flag"
	"fmt"
	"geecache/geecache"
	"log"
	"net"
	"net/http"

	"github.com/soheilhy/cmux"
)

var db = map[string]string{
	"Tom":  "630",
	"Jack": "589",
	"Sam":  "567",
}

func main() {
	var port int
	flag.IntVar(&port, "port", 8001, "geecache server port")
	flag.Parse()

	peers := []string{"http://localhost:8001", "http://localhost:8002", "http://localhost:8003"}

	listenAddr := fmt.Sprintf(":%d", port)
	selfAddr := fmt.Sprintf("http://localhost:%d", port)
	pool := geecache.NewHTTPPool(selfAddr, peers...)

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
		grpcServer := geecache.NewGRPCServer(&geecache.Groups)
		if err := grpcServer.Serve(grpcL); err != nil {
			log.Fatal(err)
		}
	}()

	// 启动HTTP服务
	go func() {
		log.Printf("[GeeCache] HTTP server listening on %s (via cmux)", listenAddr)
		if err := http.Serve(httpL, geecache.DefaultPool); err != nil {
			log.Fatal(err)
		}
	}()

	// 开始复用
	log.Printf("[GeeCache] is running at %s (HTTP & gRPC)", listenAddr)
	if err := m.Serve(); err != nil {
		log.Fatal(err)
	}
}
