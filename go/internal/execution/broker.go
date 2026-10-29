package execution

import (
	"fmt"
	"sync/atomic"

	"quant/internal/common"
)

// MockBroker 模拟券商柜台：按盘口价格成交，拆成两笔回执以演示 PARTIAL -> FILLED。
// 生产替换为 FIX / 原生 API 的异步回执通道。
type MockBroker struct {
	seq int64
}

func NewMockBroker() *MockBroker { return &MockBroker{} }

func (b *MockBroker) Place(o *common.Order) ([]common.Fill, error) {
	if o.OrigQty <= 0 {
		return nil, fmt.Errorf("委托数量非法")
	}
	// 模拟滑点：买入上浮 1 tick，卖出下浮 1 tick
	px := o.Price
	if o.Side == common.Buy {
		px += 0.01
	} else {
		px -= 0.01
	}
	if px <= 0 {
		px = o.Price
	}

	// 拆两笔回执：60% + 40%（不足 200 股则单笔全成）
	var parts []int64
	if o.OrigQty >= 200 {
		q1 := o.OrigQty / 5 * 3 / 100 * 100
		if q1 <= 0 {
			q1 = o.OrigQty / 2 / 100 * 100
		}
		if q1 <= 0 {
			q1 = o.OrigQty
		}
		parts = []int64{q1, o.OrigQty - q1}
	} else {
		parts = []int64{o.OrigQty}
	}

	now := common.NowMs()
	var fills []common.Fill
	for _, q := range parts {
		if q <= 0 {
			continue
		}
		n := atomic.AddInt64(&b.seq, 1)
		fills = append(fills, common.Fill{
			FillID:     fmt.Sprintf("F%d-%d", now, n),
			OrderID:    o.OrderID,
			Symbol:     o.Symbol,
			Side:       o.Side,
			FilledQty:  q,
			FillPrice:  px,
			FillAmount: px * float64(q),
			FillTime:   now,
		})
	}
	return fills, nil
}
