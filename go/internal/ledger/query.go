package ledger

import (
	"sort"

	"quant/internal/common"
)

// Orders 返回订单列表（按创建时间倒序），供 API / 前端查询。
func (p *Portfolio) Orders() []*common.Order {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*common.Order, 0, len(p.orders))
	for _, o := range p.orders {
		cp := *o
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Fills 返回成交明细（按时间倒序）。
func (p *Portfolio) Fills() []*common.Fill {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*common.Fill, len(p.fills))
	copy(out, p.fills)
	sort.Slice(out, func(i, j int) bool { return out[i].FillTime > out[j].FillTime })
	return out
}

// RiskLogs 返回风控审计日志（按时间倒序）。
func (p *Portfolio) RiskLogs() []RiskLog {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]RiskLog, len(p.riskLogs))
	copy(out, p.riskLogs)
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// CancelOrder 将订单置为已撤（仅 SENT/PARTIAL 可撤），返回是否成功。
func (p *Portfolio) CancelOrder(orderID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, ok := p.orders[orderID]
	if !ok {
		return false
	}
	if o.Status != common.Sent && o.Status != common.Partial {
		return false
	}
	o.Status = common.Cancelled
	o.UpdatedAt = common.NowMs()
	return true
}

// OpenOrders 返回在途订单（非终态）。
func (p *Portfolio) OpenOrders() []*common.Order {
	all := p.Orders()
	out := make([]*common.Order, 0)
	for _, o := range all {
		if o.Status == common.Init || o.Status == common.Sent || o.Status == common.Partial {
			out = append(out, o)
		}
	}
	return out
}
