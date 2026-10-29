package common

import (
	"strconv"
	"sync"
)

// ============ 内存版 Cache（语义对齐 Redis，用于演示/回放）============

// MemoryCache 线程安全的内存缓存，API 对齐 Redis 常用命令。
// 生产环境替换为 go-redis Cluster：行情读 P99 < 1ms，支持 Bitmap/SortedSet。
type MemoryCache struct {
	mu   sync.RWMutex
	h    map[string]map[string]string
	str  map[string]string
	bits map[string]map[int]int
	set  map[string]map[string]struct{}
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		h:    map[string]map[string]string{},
		str:  map[string]string{},
		bits: map[string]map[int]int{},
		set:  map[string]map[string]struct{}{},
	}
}

func (c *MemoryCache) HSet(key, field, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.h[key] == nil {
		c.h[key] = map[string]string{}
	}
	c.h[key][field] = val
}

func (c *MemoryCache) HGetAll(key string) map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]string{}
	for k, v := range c.h[key] {
		out[k] = v
	}
	return out
}

func (c *MemoryCache) Set(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.str[key] = val
}

func (c *MemoryCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.str[key]
	return v, ok
}

func (c *MemoryCache) Incr(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, _ := strconv.ParseInt(c.str[key], 10, 64)
	n++
	c.str[key] = strconv.FormatInt(n, 10)
	return n
}

func (c *MemoryCache) GetBit(key string, offset int) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bits[key][offset]
}

func (c *MemoryCache) SetBit(key string, offset, val int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bits[key] == nil {
		c.bits[key] = map[int]int{}
	}
	c.bits[key][offset] = val
}

func (c *MemoryCache) SAdd(key string, members ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set[key] == nil {
		c.set[key] = map[string]struct{}{}
	}
	for _, m := range members {
		c.set[key][m] = struct{}{}
	}
}

func (c *MemoryCache) SIsMember(key string, member string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.set[key][member]
	return ok
}

func (c *MemoryCache) Del(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.h, key)
	delete(c.str, key)
	delete(c.bits, key)
	delete(c.set, key)
}

// ============ 内存版 EventBus（语义对齐 Kafka）============

// MemoryBus 进程内事件总线，按 topic 多播；生产替换为 Kafka（acks=all，分区有序）。
type MemoryBus struct {
	mu       sync.RWMutex
	handlers map[string][]func([]byte)
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{handlers: map[string][]func([]byte){}}
}

func (b *MemoryBus) Publish(topic string, payload []byte) {
	b.mu.RLock()
	hs := append([]func([]byte){}, b.handlers[topic]...)
	b.mu.RUnlock()
	for _, h := range hs {
		h(payload)
	}
}

func (b *MemoryBus) Subscribe(topic string, handler func([]byte)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
}
