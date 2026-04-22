package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	CacheHits = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "geecache_cache_hits_total",
			Help: "Hits on the local LRU cache",
		},
		[]string{"group"},
	)

	Loads = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "geecache_loads_total",
			Help: "Successful loads into cache (from peer or backing getter)",
		},
		[]string{"group", "source"},
	)

	LoadErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "geecache_load_errors_total",
			Help: "Failed loads from peer or backing getter",
		},
		[]string{"group", "source"},
	)

	PeerRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "geecache_peer_request_duration_seconds",
			Help:    "Wall time of a single peer Get (HTTP or gRPC)",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"group", "proto"},
	)

	CacheEvictions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "geecache_cache_evictions_total",
			Help: "LRU evictions from main cache",
		},
		[]string{"group"},
	)
)
