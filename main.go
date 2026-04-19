package main

import (
	"flag"
	"fmt"
	"geecache/geecache"
	"log"
	"net/http"
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
	addr := fmt.Sprintf("http://localhost:%d", port)
	pool := geecache.NewHTTPPool(addr, peers...)

	geecache.NewGroup("scores", 2<<10, geecache.GetterFunc(
		func(key string) ([]byte, error) {
			log.Println("[SlowDB] search key", key)
			if v, ok := db[key]; ok {
				return []byte(v), nil
			}
			return nil, fmt.Errorf("key %s not found", key)
		}), pool)
	log.Println("geecache is running at", fmt.Sprintf("http://localhost:%d", port))
	log.Fatal(http.ListenAndServe(listenAddr, geecache.DefaultPool))
}
