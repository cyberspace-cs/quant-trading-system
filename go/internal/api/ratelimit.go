package api

import (
	"sync"
	"time"
)

// ===================== 限流：令牌桶 =====================
//
// 读接口与写接口分开限流：读 60 rps（突发 120），写 5 rps（突发 10）。
// 高频量化系统里，限流不是「防用户」，而是防策略异常刷单把柜台打挂。

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter 令牌桶限流器（按 key 维度，key 可以是 user_id / ip / 接口分组）
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // 每秒补充令牌数
	burst   float64 // 桶容量
	calls   int64
}

// NewLimiter 创建限流器
func NewLimiter(rate, burst float64) *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, rate: rate, burst: burst}
}

// Allow 取一个令牌
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// 按时间差补充
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	l.calls++
	// 每 2000 次调用清理一次空闲桶，防止 map 无限膨胀
	if l.calls%2000 == 0 {
		for k, v := range l.buckets {
			if now.Sub(v.last) > 5*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Remaining 剩余令牌（用于响应头 X-RateLimit-Remaining）
func (l *Limiter) Remaining(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok {
		return int(b.tokens)
	}
	return int(l.burst)
}

// ===================== 幂等键 =====================
//
// 下单这类写接口必须幂等：网络抖动客户端重试不能变成重复下单。
// 客户端带 Idempotency-Key，服务端缓存「首次响应」，24h 内重复请求直接回放。

type idemRec struct {
	at     int64
	status int
	body   []byte
}

// IdemStore 幂等键存储（生产用 Redis SETNX + TTL 24h）
type IdemStore struct {
	mu  sync.Mutex
	m   map[string]*idemRec
	ttl time.Duration
}

// NewIdemStore 创建
func NewIdemStore(ttl time.Duration) *IdemStore {
	return &IdemStore{m: map[string]*idemRec{}, ttl: ttl}
}

// Get 命中则回放
func (s *IdemStore) Get(key string) ([]byte, int, bool) {
	if key == "" {
		return nil, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[key]
	if !ok {
		return nil, 0, false
	}
	if time.Now().UnixMilli()-r.at > s.ttl.Milliseconds() {
		delete(s.m, key)
		return nil, 0, false
	}
	return r.body, r.status, true
}

// Put 记录首次响应
func (s *IdemStore) Put(key string, status int, body []byte) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = &idemRec{at: time.Now().UnixMilli(), status: status, body: body}
	// 简单淘汰：超过 5000 条时清掉过期项
	if len(s.m) > 5000 {
		cut := time.Now().UnixMilli() - s.ttl.Milliseconds()
		for k, v := range s.m {
			if v.at < cut {
				delete(s.m, k)
			}
		}
	}
}

// Len 当前条目数
func (s *IdemStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}
