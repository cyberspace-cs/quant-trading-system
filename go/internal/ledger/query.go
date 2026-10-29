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

// Order 按 ID 取单
func (p *Portfolio) Order(orderID string) (*common.Order, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	o, ok := p.orders[orderID]
	if !ok {
		return nil, false
	}
	cp := *o
	return &cp, true
}

// OrdersByParent 取母单下的全部子单（拆单明细）
func (p *Portfolio) OrdersByParent(parentID string) []*common.Order {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*common.Order, 0, 8)
	for _, o := range p.orders {
		if o.ParentID == parentID {
			cp := *o
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// FillsByOrder 取某订单的成交明细
func (p *Portfolio) FillsByOrder(orderID string) []*common.Fill {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*common.Fill, 0, 4)
	for _, f := range p.fills {
		if f.OrderID == orderID {
			cp := *f
			out = append(out, &cp)
		}
	}
	return out
}

// AmendOrder 改单：仅 SENT/PARTIAL 可改，且只能缩小数量或修改价格（A 股拆单场景的常见约束）。
func (p *Portfolio) AmendOrder(orderID string, newQty int64, newPrice float64) (*common.Order, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, ok := p.orders[orderID]
	if !ok {
		return nil, "order not found"
	}
	if o.Status != common.Sent && o.Status != common.Partial {
		return nil, "order not amendable in status " + common.StatusName(o.Status)
	}
	if newQty > 0 {
		if newQty < o.FilledQty {
			return nil, "new qty less than filled qty"
		}
		o.OrigQty = newQty
	}
	if newPrice > 0 {
		o.Price = newPrice
	}
	o.UpdatedAt = common.NowMs()
	cp := *o
	return &cp, ""
}
