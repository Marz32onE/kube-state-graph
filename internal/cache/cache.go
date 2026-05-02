package cache

import (
	"fmt"
	"time"

	"github.com/dgraph-io/ristretto/v2"

	"github.com/marz32one/kube-state-graph/internal/graph"
	"github.com/marz32one/kube-state-graph/internal/observability"
)

// Cache is the abstraction over the in-process graph cache.
type Cache interface {
	Get(key uint64) (*graph.Graph, bool)
	Set(key uint64, g *graph.Graph, cost int64, ttl time.Duration) bool
	Delete(key uint64)
	Clear()
	Close()
}

type ristrettoCache struct {
	r       *ristretto.Cache[uint64, *graph.Graph]
	metrics *observability.Metrics
}

// New returns a Ristretto-backed Cache with the supplied byte budget.
func New(maxCostBytes int64, metrics *observability.Metrics) (Cache, error) {
	if maxCostBytes <= 0 {
		return nil, fmt.Errorf("maxCostBytes must be positive")
	}
	r, err := ristretto.NewCache(&ristretto.Config[uint64, *graph.Graph]{
		NumCounters: 1_000_000,
		MaxCost:     maxCostBytes,
		BufferItems: 64,
		OnEvict: func(item *ristretto.Item[*graph.Graph]) {
			metrics.CacheEvictions.WithLabelValues("cost").Inc()
		},
		OnReject: func(item *ristretto.Item[*graph.Graph]) {
			metrics.CacheRejected.Inc()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ristretto: %w", err)
	}
	return &ristrettoCache{r: r, metrics: metrics}, nil
}

func (c *ristrettoCache) Get(key uint64) (*graph.Graph, bool) {
	v, ok := c.r.Get(key)
	if ok {
		c.metrics.CacheHits.WithLabelValues("ristretto").Inc()
		return v, true
	}
	c.metrics.CacheMisses.WithLabelValues("ristretto").Inc()
	return nil, false
}

func (c *ristrettoCache) Set(key uint64, g *graph.Graph, cost int64, ttl time.Duration) bool {
	ok := c.r.SetWithTTL(key, g, cost, ttl)
	if ok {
		c.r.Wait() // make Set immediately visible — pairs with singleflight contract
		c.metrics.CacheCostBytes.Set(float64(c.r.Metrics.CostAdded() - c.r.Metrics.CostEvicted()))
		c.metrics.CacheSizeEntries.Set(float64(c.r.Metrics.KeysAdded() - c.r.Metrics.KeysEvicted()))
	}
	return ok
}

func (c *ristrettoCache) Delete(key uint64) {
	c.r.Del(key)
}

func (c *ristrettoCache) Clear() {
	c.r.Clear()
}

func (c *ristrettoCache) Close() {
	c.r.Close()
}
