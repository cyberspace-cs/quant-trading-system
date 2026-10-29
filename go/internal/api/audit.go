package api

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ===================== 审计日志 =====================
//
// 所有写操作（下单、改单、撤单、策略开关、参数变更、规则变更、资金划转…）
// 必须留痕：谁、在什么时候、从哪个 IP、对哪个资源、做了什么、结果如何。
// 这是私募合规（《私募投资基金监督管理暂行办法》内控要求）的硬性条款，
// 也是出事之后唯一能自证清白的证据链。
//
// 生产：MySQL audit_log 表 + Kafka 双写（本仓库 SQL 已有该表）。

// AuditEntry 一条审计记录
type AuditEntry struct {
	ID        int64   `json:"id"`
	TS        int64   `json:"ts"`
	Time      string  `json:"time"`
	UserID    string  `json:"user_id"`
	UserName  string  `json:"user_name"`
	Role      string  `json:"role"`
	Action    string  `json:"action"`
	Resource  string  `json:"resource"`
	Method    string  `json:"method"`
	Path      string  `json:"path"`
	IP        string  `json:"ip"`
	Status    int     `json:"status"`
	Code      int     `json:"code"`
	LatencyMs float64 `json:"latency_ms"`
	Detail    string  `json:"detail"`
	RequestID string  `json:"request_id"`
}

// AuditLog 审计日志存储（环形，保留最近 N 条）
type AuditLog struct {
	mu    sync.RWMutex
	seq   int64
	items []AuditEntry
	cap   int
}

// NewAuditLog 创建
func NewAuditLog(capacity int) *AuditLog {
	if capacity <= 0 {
		capacity = 2000
	}
	return &AuditLog{cap: capacity, items: make([]AuditEntry, 0, capacity)}
}

// Add 追加一条
func (a *AuditLog) Add(e AuditEntry) AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	e.ID = a.seq
	e.TS = time.Now().UnixMilli()
	e.Time = time.UnixMilli(e.TS).Format("2006-01-02 15:04:05.000")
	a.items = append(a.items, e)
	if len(a.items) > a.cap {
		a.items = a.items[len(a.items)-a.cap:]
	}
	return e
}

// Query 查询（倒序 + 关键字 + 按用户/动作过滤）
type AuditQuery struct {
	User   string
	Action string
	Q      string
	From   int64
	To     int64
}

// List 返回过滤后的全量（由上层做分页）
func (a *AuditLog) List(q AuditQuery) []AuditEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AuditEntry, 0, len(a.items))
	for i := len(a.items) - 1; i >= 0; i-- {
		e := a.items[i]
		if q.User != "" && e.UserName != q.User && e.UserID != q.User {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		if q.From > 0 && e.TS < q.From {
			continue
		}
		if q.To > 0 && e.TS > q.To {
			continue
		}
		if !Match(q.Q, e.UserName, e.Action, e.Resource, e.Detail, e.Path) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Count 总数
func (a *AuditLog) Count() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.items)
}

// Stats 审计统计（按动作归类，前端做小图）
func (a *AuditLog) Stats() map[string]int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	m := map[string]int{}
	for _, e := range a.items {
		m[e.Action]++
	}
	return m
}

// Actions 动作清单
func (a *AuditLog) Actions() []string {
	m := a.Stats()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// String 便于日志打印
func (e AuditEntry) String() string {
	return fmt.Sprintf("[%s] %s(%s) %s %s -> %d", e.Time, e.UserName, e.Role, e.Action, e.Resource, e.Status)
}
