package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ===================== 统一响应契约 =====================
//
// 所有 /api/v1 下的接口（SSE / WS / metrics 除外）统一返回：
//
//	{ "code":0, "msg":"ok", "data":..., "meta":{page,size,total}, "request_id":"...", "ts":169... }
//
// code=0 表示成功；非 0 为业务错误码，HTTP 状态码同步映射，
// 便于前端一次判断、也便于网关做统一限流与错误统计。

const (
	CodeOK            = 0
	CodeBadRequest    = 1001
	CodeUnauthorized  = 1002
	CodeForbidden     = 1003
	CodeNotFound      = 1004
	CodeConflict      = 1005
	CodeRateLimited   = 1006
	CodeRiskRejected  = 1007
	CodeIdempotentHit = 1008
	CodeInternal      = 1500
)

// Meta 分页元信息
type Meta struct {
	Page  int `json:"page"`
	Size  int `json:"size"`
	Total int `json:"total"`
	Pages int `json:"pages"`
}

// Resp 统一响应体
type Resp struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	Data      any    `json:"data,omitempty"`
	Meta      *Meta  `json:"meta,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	TS        int64  `json:"ts"`
}

// Ctx 一次请求的上下文（取代裸 (w, r)，让 handler 可以拿到路径参数与登录态）
type Ctx struct {
	W     http.ResponseWriter
	R     *http.Request
	P     map[string]string
	User  *Claims
	ReqID string

	// 审计用：handler 显式声明「做了什么」，比从 URL 反推准确得多
	Action   string
	Resource string
	Detail   string
}

// Path 取路径参数
func (c *Ctx) Path(k string) string { return c.P[k] }

// Act 标记本次操作的审计语义
func (c *Ctx) Act(action, resource, detail string) {
	c.Action, c.Resource, c.Detail = action, resource, detail
}

// Query 读查询参数
func (c *Ctx) Query(k string) string { return c.R.URL.Query().Get(k) }

// Bind 解析 JSON body（限制 1MB，防打爆）
func (c *Ctx) Bind(v any) error {
	defer c.R.Body.Close()
	if c.R.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.W, c.R.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// OK 成功响应
func (c *Ctx) OK(data any) { c.okMeta(data, nil) }

// OKPage 分页成功响应
func (c *Ctx) OKPage(data any, m *Meta) { c.okMeta(data, m) }

func (c *Ctx) okMeta(data any, m *Meta) {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(c.W).Encode(Resp{
		Code: CodeOK, Msg: "ok", Data: data, Meta: m, RequestID: c.ReqID, TS: time.Now().UnixMilli(),
	})
}

// Fail 业务失败
func (c *Ctx) Fail(status, code int, msg string) {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	_ = json.NewEncoder(c.W).Encode(Resp{
		Code: code, Msg: msg, RequestID: c.ReqID, TS: time.Now().UnixMilli(),
	})
}

// Bad 400
func (c *Ctx) Bad(msg string) { c.Fail(http.StatusBadRequest, CodeBadRequest, msg) }

// NotFound 404
func (c *Ctx) NotFound(msg string) { c.Fail(http.StatusNotFound, CodeNotFound, msg) }

// Conflict 409
func (c *Ctx) Conflict(msg string) { c.Fail(http.StatusConflict, CodeConflict, msg) }

// Forbidden 403
func (c *Ctx) Forbidden(msg string) { c.Fail(http.StatusForbidden, CodeForbidden, msg) }

// ===================== 分页 / 排序 / 过滤 =====================

// Page 分页参数
type Page struct {
	Page   int    `json:"page"`
	Size   int    `json:"size"`
	Sort   string `json:"sort"`
	Order  string `json:"order"` // asc / desc
	Filter string `json:"filter"`
}

// ParsePage 解析 page/size/sort/order/filter，带上限保护（防止 size=100000 拖垮服务）
func ParsePage(c *Ctx, defSize int) Page {
	p := Page{Page: 1, Size: defSize, Order: "desc"}
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		p.Page = v
	}
	if v, err := strconv.Atoi(c.Query("size")); err == nil && v > 0 {
		p.Size = v
	}
	// 兼容旧的 limit 语义
	if c.Query("size") == "" {
		if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
			p.Size = v
		}
	}
	if p.Size > 500 {
		p.Size = 500
	}
	p.Sort = c.Query("sort")
	if o := strings.ToLower(c.Query("order")); o == "asc" || o == "desc" {
		p.Order = o
	}
	p.Filter = strings.ToLower(strings.TrimSpace(c.Query("q")))
	return p
}

// Slice 对已排序切片做分页（泛型，避免每个 handler 重复算下标）
func Slice[T any](all []T, p Page) ([]T, *Meta) {
	total := len(all)
	skip := (p.Page - 1) * p.Size
	if skip > total {
		skip = total
	}
	end := skip + p.Size
	if end > total {
		end = total
	}
	page := make([]T, end-skip)
	copy(page, all[skip:end])
	pages := 0
	if p.Size > 0 {
		pages = (total + p.Size - 1) / p.Size
	}
	return page, &Meta{Page: p.Page, Size: p.Size, Total: total, Pages: pages}
}

// SortBy 通用排序：key 取值函数 + 是否倒序
func SortBy[T any](items []T, key func(T) string, desc bool) {
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return key(items[i]) > key(items[j])
		}
		return key(items[i]) < key(items[j])
	})
}

// Match 关键字过滤：对若干字段做不区分大小写包含匹配
func Match(q string, fields ...string) bool {
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// F 解析浮点查询参数
func (c *Ctx) F(k string, def float64) float64 {
	if v := c.Query(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// I 解析整型查询参数
func (c *Ctx) I(k string, def int) int {
	if v := c.Query(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
